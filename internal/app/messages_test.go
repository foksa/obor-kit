package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// recSeq is the seq of the first record of task with step and kind (0: none).
func recSeq(t *testing.T, h *harness, task, step, kind string) int64 {
	t.Helper()
	for _, rec := range readLog(t, h.root) {
		if rec.Task == task && rec.Step == step && rec.Kind == kind {
			return rec.Seq
		}
	}
	return 0
}

// readBriefLine is the line of the last brief to agent in the log.
func readBriefLine(t *testing.T, h *harness, agent string) string {
	t.Helper()
	line := ""
	for _, rec := range readLog(t, h.root) {
		var m event.Message
		if rec.Step == event.StepBriefed && json.Unmarshal(rec.Data, &m) == nil && m.To == agent {
			line = m.Line
		}
	}
	return line
}

// T-853: a brief moves only an idle deep dive's worktree: a working one (its pane working, or a brief open) keeps its
// head for its turn, and a plain brief to the reviewer never moves the reviewer's (its head is the reviewed one). A
// worktree's git failing refuses the brief, nothing recorded.
func TestBriefMovesAnIdleDeepDive(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, to, moved string
		prep            func(r *admitRig)
	}{
		{"idle deep dive", "pj-deep-dive", "deep_dive@base1", func(*admitRig) {}},
		{"working deep dive", "pj-deep-dive", "", func(r *admitRig) { r.h.work("w1:dd", true) }},
		// The first brief (idle) moved it; the second, with that brief open, moves nothing more.
		{"deep dive with a brief open", "pj-deep-dive", "deep_dive@base1", func(r *admitRig) {
			if _, err := r.h.a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: r.h.root, CallID: "b0",
				Role: "lead"}, To: "pj-deep-dive", Text: "first"}); err != nil {
				r.h.t.Fatal(err)
			}
		}},
		{"reviewer", "pj-rev", "", func(*admitRig) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newAdmitRig(t, false)
			h := r.h
			c.prep(r)
			if _, err := h.a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: h.root, CallID: "b1", Role: "lead"},
				To: c.to, Text: "look at x"}); err != nil {
				t.Fatal(err)
			}
			if got := r.git.detachedAt(); got != c.moved {
				t.Errorf("detached %q, want %q", got, c.moved)
			}
			if readBriefLine(t, h, c.to) != "[obor lead] look at x" && readBriefLine(t, h, c.to) != "[obor owner] look at x" {
				t.Errorf("the brief was not recorded: %q", readBriefLine(t, h, c.to))
			}
		})
	}
	t.Run("git fails", func(t *testing.T) {
		t.Parallel()
		r := newAdmitRig(t, false)
		h := r.h
		r.git.detachErr = errors.New("no worktree")
		if _, err := h.a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: h.root, CallID: "b1", Role: "lead"},
			To: "pj-deep-dive", Text: "look at x"}); err == nil ||
			!strings.Contains(err.Error(), "pj-deep-dive's worktree: no worktree") {
			t.Fatalf("brief: %v, want refused naming the worktree", err)
		}
		if got := readBriefLine(t, h, "pj-deep-dive"); got != "" {
			t.Errorf("a refused brief was recorded: %q", got)
		}
	})
}

// T-794: obor brief --fresh is serve's: the brief is validated, admitted and recorded first, and only then does the
// delivery stop the running role agent (serve's role stop) and start it fresh on the brief's line.

// freshSpy is the agents, typer of a fresh brief's delivery: every stop and start in order, the starts, the lines
// typed; stopErr fails each stop.
type freshSpy struct {
	events   []string
	launches []port.Launch
	typed    []string
	stopErr  error
}

func (s *freshSpy) Launch(_ context.Context, l port.Launch) (port.Started, error) {
	s.events = append(s.events, "start "+l.Agent+" "+l.Pane)
	s.launches = append(s.launches, l)
	return port.Started{Line: "STARTED " + l.Agent}, nil
}

func (s *freshSpy) Stop(_ context.Context, agent, pane string, force bool) (string, error) {
	s.events = append(s.events, fmt.Sprintf("stop %s %s force=%t", agent, pane, force))
	if s.stopErr != nil {
		return "", s.stopErr
	}
	return "STOPPED " + agent, nil
}

func (s *freshSpy) Type(_ context.Context, _, text string, _ bool) (bool, error) {
	s.typed = append(s.typed, text)
	return true, nil
}

// freshCfg names every role a brief may start fresh.
const freshCfg = `{"name":"proj","prefix":"pj","roles":{"integrator":"","reviewer":"","deep_dive":"","advisor":"",` +
	`"researcher":"","triage":""}}`

// freshApp is a merging project with every role, the lead's pane and agent's labelled pane w:role running kind
// ("": at its shell) with status.
func freshApp(t *testing.T, now *time.Time, agent, kind, status string) (*App, *proj) {
	t.Helper()
	root := lineRoot(t)
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(freshCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	a, p := lineApp(t, root, now)
	a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{
		{ID: "w:lead", Group: "proj", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
		{ID: "w:role", Group: "proj", Cwd: root, Agent: kind, Label: agent, Status: status},
	}})
	// An idle deep dive's brief moves its role worktree first (T-853).
	a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(&reviewGitFake{head: "base1"})}, nil }
	return a, p
}

// briefSeq is the seq of the last briefed fact to agent in p's log (0: none).
func briefSeq(t *testing.T, p *proj, agent string) int64 {
	t.Helper()
	var seq int64
	for _, rec := range readLog(t, p.root) {
		if rec.Step == event.StepBriefed && strings.Contains(string(rec.Data), `"to":"`+agent+`"`) {
			seq = rec.Seq
		}
	}
	return seq
}

// freshNeeds is p's needs-you reason in force for its lines ("": none).
func freshNeeds(a *App, p *proj) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range p.folded.active[event.MsgTask] {
		if f.Step == event.StepNeedsYou {
			return f.Fact().Reason
		}
	}
	return ""
}

// Each role kind a fresh brief starts: running (working), it is stopped through serve's role stop and then started
// fresh in its pane on the brief's line, nothing typed; stopped (its pane at its shell), it is started on the line, no
// stop attempted, no error. Either way one start, and a second pass starts nothing more.
func TestFreshBriefPerRole(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ role, agent string }{
		{"reviewer", "pj-rev"}, {"deep_dive", "pj-deep-dive"}, {"advisor", "pj-advisor"},
		{"researcher", "pj-researcher"}, {"triage", "pj-triage"},
	} {
		for _, running := range []bool{true, false} {
			name := c.role + map[bool]string{true: "/running", false: "/stopped"}[running]
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
				kind, status := "", ""
				if running {
					kind, status = "claude", "working"
				}
				a, p := freshApp(t, &now, c.agent, kind, status)
				a.mu.Lock()
				w := a.whereIs(p, c.agent)
				_, atShell := a.shellPane(p, c.agent)
				a.mu.Unlock()
				if running && (w.State != Identified || Ready(w.Pane.Status)) || !running && !atShell {
					t.Fatalf("precondition: %s %+v, at its shell %v", c.agent, w, atShell)
				}
				reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1",
					Role: "lead", Agent: "pj-lead"}, To: c.agent, Text: "look at x", Fresh: true})
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(reply, "BRIEFED "+c.agent) || !strings.Contains(reply, "fresh") {
					t.Errorf("reply %q", reply)
				}
				seq := briefSeq(t, p, c.agent)
				if seq == 0 {
					t.Fatal("no briefed fact")
				}
				s := &freshSpy{}
				fx := &Loop{Agents: s, Typer: s}
				a.deliver(context.Background(), p, fx)
				want := []string{"start " + c.agent + " w:role"}
				if running {
					want = append([]string{"stop " + c.agent + " w:role force=false"}, want...)
				}
				if !slices.Equal(s.events, want) {
					t.Errorf("events %q, want %q", s.events, want)
				}
				if len(s.launches) != 1 || s.launches[0] != (port.Launch{Agent: c.agent, Pane: "w:role", Fresh: true,
					Prompt: "[obor pj-lead] look at x"}) {
					t.Errorf("launches %+v, want %s fresh in w:role on the brief's line", s.launches, c.agent)
				}
				same(t, len(s.typed), 0, "lines typed")
				key := event.Key{Task: event.MsgTask, Step: stepDeliver + fmt.Sprint(seq)}
				same(t, resultOf(p, key), `"`+sentStart+`"`, "the line's delivery")
				same(t, freshNeeds(a, p), "", "needs-you")
				a.deliver(context.Background(), p, fx)
				same(t, len(s.events), len(want), "stops and starts after a second pass")
			})
		}
	}
}

// An older brief queued for a busy agent, then a fresh one: the fresh brief supersedes it when it is recorded, so
// the stop and the fresh start carry the new text only, and the old line is never typed or started.
func TestFreshBriefSupersedesQueued(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a, p := freshApp(t, &now, "pj-advisor", "codex", "working")
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead",
		Agent: "pj-lead"}, To: "pj-advisor", Text: "old"}); err != nil {
		t.Fatal(err)
	}
	s := &freshSpy{}
	fx := &Loop{Agents: s, Typer: s}
	a.deliver(context.Background(), p, fx) // busy: the old line waits
	a.mu.Lock()
	queued := len(p.queuedLines())
	a.mu.Unlock()
	if queued != 1 || len(s.events)+len(s.typed) != 0 {
		t.Fatalf("precondition: %d lines queued, events %q, typed %q", queued, s.events, s.typed)
	}
	reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead",
		Agent: "pj-lead"}, To: "pj-advisor", Text: "new", Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "replaces 1 undelivered brief") {
		t.Errorf("reply %q, want it to replace the old brief", reply)
	}
	a.deliver(context.Background(), p, fx)
	if want := []string{"stop pj-advisor w:role force=false", "start pj-advisor w:role"}; !slices.Equal(s.events, want) {
		t.Errorf("events %q, want %q", s.events, want)
	}
	if len(s.launches) != 1 || s.launches[0].Prompt != "[obor pj-lead] new" {
		t.Errorf("launches %+v, want one start on the new text only", s.launches)
	}
	same(t, len(s.typed), 0, "lines typed")
	a.mu.Lock()
	queued = len(p.queuedLines())
	a.mu.Unlock()
	same(t, queued, 0, "lines still queued")
}

// A refused brief --fresh changes nothing: not the lead or the owner, a worker or the integrator as the target, no
// text — and a running agent is not stopped by the delivery that follows (nothing was recorded).
func TestFreshBriefRefusedStopsNothing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a, p := freshApp(t, &now, "pj-advisor", "claude", "working")
	for _, c := range []struct {
		name string
		call wire.BriefReq
		want string
	}{
		{"a worker calls", wire.BriefReq{To: "pj-advisor", Text: "x", Envelope: wire.Envelope{Role: "worker",
			Agent: "pj-l-01"}}, "the lead or the owner briefs"},
		{"the integrator", wire.BriefReq{To: "pj-int", Text: "x", Envelope: wire.Envelope{Role: "lead"}},
			"no role agent serve starts fresh"},
		{"no text", wire.BriefReq{To: "pj-advisor", Text: " ", Envelope: wire.Envelope{Role: "lead"}},
			"--fresh takes a text"},
	} {
		c.call.Root, c.call.CallID, c.call.Fresh = p.root, "b-"+c.name, true
		if _, err := a.briefCall(c.call); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	same(t, briefSeq(t, p, "pj-advisor")+briefSeq(t, p, "pj-int"), int64(0), "briefed facts")
	s := &freshSpy{}
	a.deliver(context.Background(), p, &Loop{Agents: s, Typer: s})
	same(t, len(s.events), 0, "stops and starts")
	if w := a.whereIs(p, "pj-advisor"); w.State != Identified {
		t.Errorf("pj-advisor %+v, want still running", w)
	}
}

// A brief --fresh to the idle deep dive whose worktree's move fails is refused, and the running deep dive is not
// stopped: nothing is recorded, the delivery stops nothing.
func TestFreshBriefMoveFailsStopsNothing(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, false)
	h, ctx := r.h, context.Background()
	s := &freshSpy{}
	r.fx.Agents = s
	r.git.detachErr = errors.New("no worktree")
	h.a.mu.Lock()
	w := h.a.whereIs(h.a.p, "pj-deep-dive")
	h.a.mu.Unlock()
	if w.State != Identified {
		t.Fatalf("precondition: pj-deep-dive %+v", w)
	}
	_, err := h.a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: h.root, CallID: "b1", Role: "lead",
		Agent: "pj-lead"}, To: "pj-deep-dive", Text: "look at x", Fresh: true})
	if err == nil || !strings.Contains(err.Error(), "pj-deep-dive's worktree: no worktree") {
		t.Fatalf("brief --fresh with the move failing: %v, want refused naming the worktree", err)
	}
	h.a.deliver(ctx, h.a.p, r.fx)
	same(t, recSeq(t, h, event.MsgTask, event.StepBriefed, event.KindFact), int64(0), "briefed facts")
	for _, e := range s.events {
		if strings.Contains(e, "pj-deep-dive") {
			t.Errorf("event %q after a refused brief --fresh, want pj-deep-dive untouched", e)
		}
	}
}

// A stop that fails after the brief is recorded: nothing is started, the brief stays queued, a needs-you names the
// agent at once; the next pass inside roleStopRetry does not stop again, a later delivery stops and starts it on the
// brief, and the needs-you ends.
func TestFreshBriefStopFails(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a, p := freshApp(t, &now, "pj-researcher", "claude", "working")
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead",
		Agent: "pj-lead"}, To: "pj-researcher", Text: "dig", Fresh: true}); err != nil {
		t.Fatal(err)
	}
	s := &freshSpy{stopErr: errors.New("pj-researcher still working 30s after Escape")}
	fx := &Loop{Agents: s, Typer: s}
	a.deliver(context.Background(), p, fx)
	same(t, len(s.events), 1, "stops tried")
	same(t, len(s.launches), 0, "starts after a failed stop")
	a.mu.Lock()
	queued := p.queuedLines()
	a.mu.Unlock()
	if len(queued) != 1 || queued[0].msg.Line != "[obor pj-lead] dig" {
		t.Fatalf("queued %+v, want the fresh brief still pending", queued)
	}
	if got := freshNeeds(a, p); !strings.Contains(got, "fresh brief to pj-researcher") || !strings.Contains(got, "still working") {
		t.Errorf("needs-you %q, want one naming pj-researcher and the stop's error", got)
	}

	now = now.Add(time.Second)
	a.deliver(context.Background(), p, fx)
	same(t, len(s.events), 1, "stops tried within roleStopRetry")

	now = now.Add(roleStopRetry)
	s.stopErr = nil
	a.deliver(context.Background(), p, fx)
	if len(s.launches) != 1 || s.launches[0].Prompt != "[obor pj-lead] dig" || !s.launches[0].Fresh {
		t.Errorf("launches %+v, want the fresh start on the brief", s.launches)
	}
	same(t, len(s.typed), 0, "lines typed")
	same(t, freshNeeds(a, p), "", "needs-you after the start")
}

// leadSends queues n obor send lines "[obor other] m<i>" for the lead.
func leadSends(t *testing.T, a *App, p *proj, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		if _, err := a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "s" + id, Role: "lead"},
			From: "other", Text: "m" + id}); err != nil {
			t.Fatal(err)
		}
	}
}

// delivered counts the delivery results recorded for the lead's lines in p's log.
func deliveredResults(t *testing.T, p *proj) int {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Kind == event.KindResult && r.Task == event.MsgTask && strings.HasPrefix(r.Step, stepDeliver) {
			n++
		}
	}
	return n
}

// TestLeadLinesOnePost (T-651): the lines queued for the lead go out as one post, one per row in order, each with
// its delivery fact; a worker's line stays its own post.
func TestLeadLinesOnePost(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "check"}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.queuedLines()); n != 4 {
		t.Fatalf("precondition: four lines queued, got %d", n)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || l.calls[1] != "w:rev: [obor owner] check" || !oneLeadPost(l.calls[0]) {
		t.Errorf("posted %q, want one lead post of m1..m3 in order, then the brief", l.calls)
	}
	if n := len(p.queuedLines()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if n := deliveredResults(t, p); n != 4 {
		t.Errorf("%d delivery results, want 4 (three lead lines, one brief)", n)
	}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 {
		t.Errorf("delivered again: %q", l.calls)
	}
}

// TestLeadLinesBatchReplay: a restart finds every line of the batch delivered.
func TestLeadLinesBatchReplay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: one post, got %q", l.calls)
	}
	b, q := lineReload(t, a, p, &now)
	if n := len(q.queuedLines()); n != 0 {
		t.Errorf("after replay %d lines queued, want none", n)
	}
	b.deliver(context.Background(), q, linerLoop(l))
	if len(l.calls) != 1 {
		t.Errorf("replay posted again: %q", l.calls)
	}
}

// TestLeadLinesFailedPost: a post refused before a byte leaves all lines undelivered and the next pass posts them
// as one.
func TestLeadLinesFailedPost(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	l := &linerSpy{refuse: true}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", l.calls)
	}
	if n := len(p.queuedLines()); n != 3 {
		t.Fatalf("%d lines queued after a refused post, want 3", n)
	}
	if n := deliveredResults(t, p); n != 0 {
		t.Errorf("%d delivery results after a refused post, want 0", n)
	}
	for _, q := range p.queuedLines() {
		if q.state != lineWaiting {
			t.Errorf("line %d still claimed after the refusal", q.seq)
		}
	}
	l.refuse = false
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !oneLeadPost(l.calls[1]) {
		t.Errorf("retry posted %q, want the three lines as one", l.calls)
	}
	if n := len(p.queuedLines()); n != 0 {
		t.Errorf("%d lines queued after the retry", n)
	}
}

// oneLeadPost: call is one post to the lead of the lines m1, m2, m3 in order, one per row (a send line carries a
// reply hint after its text).
func oneLeadPost(call string) bool {
	rows := strings.Split(strings.TrimPrefix(call, "pj-lead: "), "\n")
	if !strings.HasPrefix(call, "pj-lead: ") || len(rows) != 3 {
		return false
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "[obor other] m"+strconv.Itoa(i+1)) {
			return false
		}
	}
	return true
}

// T-654 (RV-72 #1 #2): a post's intent names its members and its target; only its result settles them.

// leadSend queues one obor send line "[obor other] <note>" for the lead (as it is now), and is its seq.
func leadSend(t *testing.T, a *App, p *proj, note string) int64 {
	t.Helper()
	if _, err := a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "s-" + note, Role: "lead"},
		From: "other", Text: note}); err != nil {
		t.Fatal(err)
	}
	qs := p.queuedLines()
	return qs[len(qs)-1].seq
}

// deliveryResults counts the delivery results of p's log by their line's seq.
func deliveryResults(t *testing.T, p *proj) map[int64]int {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]int{}
	for _, r := range recs {
		if r.Kind == event.KindResult && r.Task == event.MsgTask && strings.HasPrefix(r.Step, stepDeliver) {
			seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, stepDeliver), 10, 64)
			out[seq]++
		}
	}
	return out
}

// postOf: call is one post to target of exactly the lines notes, in order.
func postOf(call, target string, notes ...string) bool {
	rows := strings.Split(strings.TrimPrefix(call, target+": "), "\n")
	if !strings.HasPrefix(call, target+": ") || len(rows) != len(notes) {
		return false
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "[obor other] "+notes[i]+" ") {
			return false
		}
	}
	return true
}

// lostLeadResult posts [A] with the result's append refused (kept in p.unwritten), and is A's seq.
func lostLeadResult(t *testing.T, a *App, p *proj, l *linerSpy) int64 {
	t.Helper()
	seqA := leadSend(t, a, p, "A")
	refuseOnce(p, stepDeliver+strconv.FormatInt(seqA, 10))
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 || !postOf(l.calls[0], "pj-lead", "A") {
		t.Fatalf("precondition: one post of A, got %q", l.calls)
	}
	if n := len(p.queuedLines()); n != 1 || deliveryResults(t, p)[seqA] != 0 {
		t.Fatalf("precondition: A queued, its result not in the log (queued %d)", n)
	}
	return seqA
}

// TestLeadLateResultNewLine: [A]'s result append fails; B queues; the next pass writes A's kept result (nothing
// transmitted for A) and posts B on its own; each delivered once.
func TestLeadLateResultNewLine(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	l := &linerSpy{}
	seqA := lostLeadResult(t, a, p, l)
	seqB := leadSend(t, a, p, "B")
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !postOf(l.calls[1], "pj-lead", "B") {
		t.Errorf("posted %q, want A's post, then B alone", l.calls)
	}
	if n := len(p.queuedLines()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if got := deliveryResults(t, p); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one for A and one for B", got)
	}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 {
		t.Errorf("delivered again: %q", l.calls)
	}
}

// TestLeadLateResultNewLineReplay: the same with a restart between: A's post (its intent pending, may have landed) goes
// again with exactly its member, B on its own; each settled by one result, nothing queued, nothing posted again.
func TestLeadLateResultNewLineReplay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	l := &linerSpy{}
	seqA := lostLeadResult(t, a, p, l)
	b, q := lineReload(t, a, p, &now)
	seqB := leadSend(t, b, q, "B")
	for range 3 {
		b.deliver(context.Background(), q, linerLoop(l))
	}
	if len(l.calls) != 3 || !postOf(l.calls[1], "pj-lead", "A") || !postOf(l.calls[2], "pj-lead", "B") {
		t.Errorf("posted %q, want A, A again alone (its result never reached the log), then B alone", l.calls)
	}
	if n := len(q.queuedLines()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if got := deliveryResults(t, q); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one for A and one for B", got)
	}
	c, r := lineReload(t, b, q, &now)
	c.deliver(context.Background(), r, linerLoop(l))
	if n := len(r.queuedLines()); n != 0 || len(l.calls) != 3 {
		t.Errorf("after a second replay: %d queued, posted %q", n, l.calls)
	}
}

// TestLeadLinesTwoTargets: a line queued for the old lead and one for the new: two posts, each to its own target with
// its own line.
func TestLeadLinesTwoTargets(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	seqA := leadSend(t, a, p, "A")
	a.mu.Lock()
	p.out.leadAgent = "pj-newlead"
	a.mu.Unlock()
	seqB := leadSend(t, a, p, "B")
	if qs := p.queuedLines(); len(qs) != 2 || qs[0].msg.To != "pj-lead" || qs[1].msg.To != "pj-newlead" {
		t.Fatalf("precondition: a line to each lead, got %d", len(qs))
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !postOf(l.calls[0], "pj-lead", "A") || !postOf(l.calls[1], "pj-newlead", "B") {
		t.Errorf("posted %q, want A to pj-lead, B to pj-newlead", l.calls)
	}
	if got := deliveryResults(t, p); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one each", got)
	}
}

// TestLeadLinesOldTargetNoInbox: the old lead has no inbox record: its line waits (the 24 h needs-you as before); the
// new lead's line goes out.
func TestLeadLinesOldTargetNoInbox(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	a.opt.Inboxed = func(agent string) (string, bool) { return "", agent != "pj-lead" }
	seqA := leadSend(t, a, p, "A")
	a.mu.Lock()
	p.out.leadAgent = "pj-newlead"
	a.mu.Unlock()
	seqB := leadSend(t, a, p, "B")
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 || !postOf(l.calls[0], "pj-newlead", "B") {
		t.Errorf("posted %q, want only B to pj-newlead", l.calls)
	}
	if qs := p.queuedLines(); len(qs) != 1 || qs[0].seq != seqA {
		t.Errorf("queued %d lines, want A alone", len(qs))
	}
	if got := deliveryResults(t, p); got[seqB] != 1 || len(got) != 1 {
		t.Errorf("delivery results %v, want B's alone", got)
	}
	now = now.Add(msgWait + time.Minute)
	a.deliver(context.Background(), p, linerLoop(l))
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	needs := 0
	for _, r := range recs {
		if r.Task == event.MsgTask && r.Kind == event.KindFact && r.Step == event.StepNeedsYou &&
			strings.Contains(r.Fact().Reason, "line to pj-lead from other") {
			needs++
		}
	}
	if needs != 1 || len(l.calls) != 1 {
		t.Errorf("needs-you of A's line %d (want 1), posted %q", needs, l.calls)
	}
}

// lineKinds are the three kinds of line: an obor send line to the lead, an obor brief text to the reviewer, and the
// lead's finished line of a brief whose reviewer worked and is idle again.
var lineKinds = []string{"send", "brief", "finished"}

// lineTyped: calls is the line of kind typed once (a finished line's age aside; a brief typed into pj-rev's pane).
func lineTyped(calls []string, kind string) bool {
	want := map[string]string{"send": "pj-lead: " + sendLine("other", "hello"), "brief": "w:rev: [obor owner] verify finding 1",
		"finished": "pj-lead: [obor] pj-rev finished (briefed "}[kind]
	return len(calls) == 1 && strings.HasPrefix(calls[0], want)
}

// queuedAt is p's undelivered line of fact seq (nil: none), by its outbox's queuedLines.
func (p *proj) queuedAt(seq int64) *queued {
	for _, q := range p.queuedLines() {
		if q.seq == seq {
			return q
		}
	}
	return nil
}

// TestRefusedLineReplay: a line refused before typing stays queued across a restart (orderly and SIGKILL) and is
// typed once after it, for each kind of line (RV-50 #1).
func TestRefusedLineReplay(t *testing.T) {
	t.Parallel()
	for _, kind := range lineKinds {
		t.Run(kind+"/reload", func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			queueLine(t, a, p, kind, now)
			l := &linerSpy{refuse: true}
			a.deliver(context.Background(), p, linerLoop(l))
			if len(l.calls) != 1 || len(p.queuedLines()) != 1 {
				t.Fatalf("precondition: one refused try of one queued line; calls %v, queued %d", l.calls, len(p.queuedLines()))
			}
			b, q := lineReload(t, a, p, &now)
			if len(q.queuedLines()) != 1 {
				t.Fatalf("the refused line is not queued after reload: %d", len(q.queuedLines()))
			}
			good := &linerSpy{}
			b.deliver(context.Background(), q, linerLoop(good))
			b.deliver(context.Background(), q, linerLoop(good))
			if !lineTyped(good.calls, kind) || len(q.queuedLines()) != 0 {
				t.Fatalf("after reload: want the %s line typed once, got %v (queued %d)", kind, good.calls, len(q.queuedLines()))
			}
			c, s := lineReload(t, b, q, &now)
			c.deliver(context.Background(), s, linerLoop(good))
			if len(good.calls) != 1 {
				t.Fatalf("typed again after a second reload: %v", good.calls)
			}
		})
		t.Run(kind+"/kill", func(t *testing.T) {
			t.Parallel()
			calls := killedLines(t, kind, "refused")
			if !lineTyped(calls, kind) {
				t.Fatalf("after SIGKILL: want the %s line typed once, got %v", kind, calls)
			}
		})
	}
}

// TestRefusedLineRetryKilled: a refused line's retry cut by a SIGKILL once its typing began is delivered once more
// after the restart (T-524: a line is never lost; it may reach its agent twice); a line queued without a delivery is
// typed once (control).
func TestRefusedLineRetryKilled(t *testing.T) {
	t.Parallel()
	for _, kind := range lineKinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			if calls := killedLines(t, kind, "retry"); !lineTyped(calls, kind) {
				t.Fatalf("a retry cut while typing: want the %s line delivered once after the restart, got %v", kind,
					calls)
			}
		})
	}
	t.Run("queued control", func(t *testing.T) {
		t.Parallel()
		if calls := killedLines(t, "send", "queued"); !lineTyped(calls, "send") {
			t.Fatalf("a queued line: want one delivery, got %v", calls)
		}
	})
}

// killedLines runs TestRefusedLineKillChild for kind and mode, kills it with SIGKILL once it is ready, and is the lines
// typed by two reloads after it (two deliver passes each).
func killedLines(t *testing.T, kind, mode string) []string {
	t.Helper()
	root := lineRoot(t)
	child := exec.Command(os.Args[0], "-test.run=^TestRefusedLineKillChild$")
	child.Env = append(os.Environ(), "LINE_ROOT="+root, "LINE_KIND="+kind, "LINE_MODE="+mode)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	ready := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(root, "kill-ready")); err == nil {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("the child did not get ready")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if st := child.ProcessState.Sys().(syscall.WaitStatus); !st.Signaled() || st.Signal() != syscall.SIGKILL {
		t.Fatalf("the child did not end by SIGKILL: %v", child.ProcessState)
	}
	now := time.Now()
	l := &linerSpy{}
	a, p := lineApp(t, root, &now)
	if p.out.lead() != "pj-lead" {
		t.Fatalf("precondition: lead pj-lead, got %s", p.out.lead())
	}
	a.deliver(context.Background(), p, linerLoop(l))
	a.deliver(context.Background(), p, linerLoop(l))
	b, q := lineReload(t, a, p, &now)
	b.deliver(context.Background(), q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	return l.calls
}

// TestRefusedLineKillChild is killedLines's child: it queues a line of LINE_KIND; refused: its delivery is refused; retry:
// refused, then its retry hangs once typing began; queued: nothing delivered; worked (no line queued): a fresh
// start's brief whose work a pass saw. Then it writes kill-ready and waits.
func TestRefusedLineKillChild(t *testing.T) {
	t.Parallel()
	root := os.Getenv("LINE_ROOT")
	if root == "" {
		t.Skip("killedLines's child only")
	}
	now := time.Now()
	a, p := lineApp(t, root, &now)
	if os.Getenv("LINE_MODE") != "worked" {
		queueLine(t, a, p, os.Getenv("LINE_KIND"), now)
	}
	ready := func() { _ = os.WriteFile(filepath.Join(root, "kill-ready"), []byte("ready"), 0o600) }
	switch os.Getenv("LINE_MODE") {
	case "refused":
		a.deliver(context.Background(), p, linerLoop(&linerSpy{refuse: true}))
	case "retry":
		a.deliver(context.Background(), p, linerLoop(&linerSpy{refuse: true}))
		a.deliver(context.Background(), p, linerLoop(&linerSpy{hang: ready}))
	case "worked":
		if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev"}); err != nil {
			t.Fatal(err)
		}
		lineStatus(a, "w:rev", "working", now)
		a.deliver(context.Background(), p, linerLoop(&linerSpy{}))
	}
	ready()
	park()
}

// TestBriefCompletionReplay: a brief whose work serve saw before a restart (orderly and SIGKILL) gets its
// finished line when its agent is idle on reconnect, for a brief with a text and a fresh start's brief (RV-50 #3).
func TestBriefCompletionReplay(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"verify finding 1", ""} {
		form := map[bool]string{true: "verify", false: "fresh"}[note != ""]
		t.Run(form+"/reload", func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
				To: "pj-rev", Text: note}); err != nil {
				t.Fatal(err)
			}
			l := &linerSpy{}
			a.deliver(context.Background(), p, linerLoop(l))
			lineStatus(a, "w:rev", "working", now)
			a.deliver(context.Background(), p, linerLoop(l)) // the pass that records the work
			lines := 0                                       // the brief's own line
			if note != "" {
				lines = 1
			}
			if !p.out.brief("pj-rev").worked || len(l.calls) != lines {
				t.Fatalf("precondition: work seen, no finished line yet; worked %v, calls %v", p.out.brief("pj-rev").worked, l.calls)
			}
			b, q := lineReload(t, a, p, &now) // the reviewer idle on reconnect
			if q.out.brief("pj-rev") == nil || !q.out.brief("pj-rev").worked {
				t.Fatal("the brief's observed work is not replayed")
			}
			out := &linerSpy{}
			b.deliver(context.Background(), q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			if !lineTyped(out.calls, "finished") || q.briefCount() != 0 {
				t.Fatalf("want one finished line after reload, got %v (open briefs %d)", out.calls, q.briefCount())
			}
		})
	}
	t.Run("fresh/kill", func(t *testing.T) {
		t.Parallel()
		calls := killedLines(t, "", "worked")
		if !lineTyped(calls, "finished") {
			t.Fatalf("want one finished line after SIGKILL, got %v", calls)
		}
	})
}

// TestWorkedNotNewerBrief: the recorded work of a brief does not arm a newer brief that replaced it.
func TestWorkedNotNewerBrief(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	l := &linerSpy{}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev"}); err != nil {
		t.Fatal(err)
	}
	lineStatus(a, "w:rev", "working", now)
	a.deliver(context.Background(), p, linerLoop(l))
	if !p.out.brief("pj-rev").worked {
		t.Fatal("precondition: the first brief's work is seen")
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	b, q := lineReload(t, a, p, &now)
	if br := q.out.brief("pj-rev"); br == nil || br.worked {
		t.Fatalf("the newer brief is armed by the older one's work: %+v", br)
	}
	b.deliver(context.Background(), q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	if len(l.calls) != 1 || l.calls[0] != "w:rev: [obor owner] again" {
		t.Fatalf("want only the newer brief's line, got %v", l.calls)
	}
}

// TestBriefKeepsAStartedDelivery (T-575): a line whose delivery is in flight (its intent is pending: it may have
// landed) is not superseded by a newer brief.
func TestBriefKeepsAStartedDelivery(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now) // the brief's line waits
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if len(p.queuedLines()) != 1 {
		t.Fatalf("precondition: one brief queued, got %d", len(p.queuedLines()))
	}
	q := p.queuedLines()[0]
	a.record(p, event.Record{Task: event.MsgTask, Attempt: 1, Step: stepDeliver + strconv.FormatInt(q.seq, 10),
		Kind: event.KindIntent, Class: event.ClassPrompt}, lineData{To: "pj-rev"})
	if m := p.queuedAt(q.seq); m == nil || m.state != lineSending {
		t.Fatalf("precondition: the delivery in flight; queued %+v", p.queuedLines())
	}
	reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, "replaces") {
		t.Errorf("reply %q says it replaced a line whose delivery may have landed", reply)
	}
	if m := p.queuedAt(q.seq); m == nil || m.state != lineSending {
		t.Errorf("the in-flight line was superseded: queued %+v", p.queuedLines())
	}
	if n := len(p.queuedLines()); n != 2 {
		t.Errorf("queued lines %d, want the in-flight one and the newer brief's", n)
	}
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Task == event.MsgTask && rec.Step == event.StepBriefed && len(m.Supersedes) > 0 {
			t.Errorf("a brief superseded the in-flight line: %+v", rec)
		}
	}
}

// TestBriefSupersededEndsNeedsYou (T-575): the 24 h needs-you of a brief line ends when a newer brief supersedes it;
// the newer brief's own line waits as usual.
func TestBriefSupersededEndsNeedsYou(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	l := &linerSpy{}
	a.opt.Loop = func(string) (*Loop, error) { return &Loop{Typer: l, Inbox: l}, nil }
	now = now.Add(25 * time.Hour)
	a.advance(context.Background())
	if len(p.folded.active[event.MsgTask]) != 1 {
		t.Fatalf("precondition: the 25 h brief line's needs-you, got %d", len(p.folded.active[event.MsgTask]))
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	a.advance(context.Background())
	a.advance(context.Background())
	if len(p.folded.active[event.MsgTask]) != 0 {
		t.Errorf("the needs-you of the superseded line is still in force: %+v", p.folded.active[event.MsgTask])
	}
	if q := p.queuedLines(); len(q) != 1 || q[0].msg.Line != "[obor owner] second" || len(l.calls) != 0 {
		t.Errorf("queued %v, typed %v; want the newer brief's line alone, waiting", q, l.calls)
	}
}

// TestBriefSupersedeSurvivesACrash (RV-61 #2): a brief's supersede is its own fact: with the log refusing every
// append after the new brief's, a restart right after it keeps the old line retired (never delivered), with a line
// or a fresh start's brief; a retry of the new brief's call id gets the stored reply.
func TestBriefSupersedeSurvivesACrash(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"second", ""} {
		t.Run("note="+note, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			lineStatus(a, "w:rev", "working", now) // brief A waits
			if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
				To: "pj-rev", Text: "first"}); err != nil {
				t.Fatal(err)
			}
			if len(p.queuedLines()) != 1 {
				t.Fatalf("precondition: brief A queued, got %d", len(p.queuedLines()))
			}
			briefed := false
			p.log.Refuse = func(r event.Record) error { // the crash cut: nothing after the new brief reaches the log
				if briefed {
					return errors.New("cut")
				}
				briefed = r.Step == event.StepBriefed
				return nil
			}
			reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
				To: "pj-rev", Text: note})
			if err != nil || !strings.HasSuffix(reply, "(replaces 1 undelivered brief)") {
				t.Fatalf("brief B: %q, %v; want it recorded, replacing A", reply, err)
			}
			if !briefed {
				t.Fatal("precondition: brief B's fact is in the log")
			}
			b, q := lineReload(t, a, p, &now)
			for _, m := range q.queuedLines() {
				if m.msg.Line == "[obor owner] first" {
					t.Errorf("brief A is queued again after the restart: %+v", m)
				}
			}
			again, err := b.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: q.root, CallID: "b2", Role: "lead"},
				To: "pj-rev", Text: note})
			if err != nil || again != reply {
				t.Errorf("retry of b2: %q, %v; want the stored reply %q", again, err, reply)
			}
			l := &linerSpy{}
			lineStatus(b, "w:rev", "idle", now)
			b.deliver(context.Background(), q, linerLoop(l))
			b.deliver(context.Background(), q, linerLoop(l))
			want := []string{}
			if note != "" {
				want = []string{"w:rev: [obor owner] second"}
			}
			if strings.Join(l.calls, "|") != strings.Join(want, "|") {
				t.Errorf("delivered %v, want %v: brief A never", l.calls, want)
			}
		})
	}
}

// duringSpy is a linerSpy that runs during once inside its first delivery (the line's claim is set then).
type duringSpy struct {
	linerSpy
	during func()
}

func (d *duringSpy) Type(ctx context.Context, agent, text string, x bool) (bool, error) {
	if f := d.during; f != nil {
		d.during = nil
		f()
	}
	return d.linerSpy.Type(ctx, agent, text, x)
}

func (d *duringSpy) Post(ctx context.Context, _, agent, line string) (bool, error) {
	return d.Type(ctx, agent, line, false)
}

// TestBriefRetiresARoutedLine (RV-61 #3): a line routed for delivery (no intent yet) that a newer brief supersedes
// before its claim is not transmitted, and only the newer one is delivered; the inverse, a line claimed first, is
// kept by the newer brief and delivered once, and the newer brief after it.
func TestBriefRetiresARoutedLine(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	lineStatus(a, "w:rev", "idle", now)
	q := *p.queuedLines()[0]
	l := &linerSpy{}
	fx := linerLoop(l)
	a.mu.Lock()
	post, target, ok := a.route(p, fx, q)
	a.mu.Unlock()
	if !ok {
		t.Fatal("precondition: brief A routed")
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	a.deliverLine(context.Background(), p, fx, q, post, target)
	if len(l.calls) != 0 {
		t.Errorf("the retired line was transmitted: %v", l.calls)
	}
	if _, written := p.log.Intent(event.Key{Task: event.MsgTask, Step: stepDeliver + strconv.FormatInt(q.seq, 10)}); written {
		t.Errorf("an intent was written for the retired line")
	}
	a.deliver(context.Background(), p, fx)
	a.deliver(context.Background(), p, fx)
	if len(l.calls) != 1 || l.calls[0] != "w:rev: [obor owner] second" {
		t.Errorf("delivered %v, want brief B alone", l.calls)
	}

	t.Run("claimed first", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		a, p := lineApp(t, lineRoot(t), &now)
		lineStatus(a, "w:rev", "working", now)
		if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev", Text: "first"}); err != nil {
			t.Fatal(err)
		}
		lineStatus(a, "w:rev", "idle", now)
		var reply string
		d := &duringSpy{}
		d.during = func() { // A's claim is set: B arrives now
			var err error
			if reply, err = a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
				To: "pj-rev", Text: "second"}); err != nil {
				t.Error(err)
			}
		}
		fx := &Loop{Typer: d, Inbox: d}
		a.deliver(context.Background(), p, fx)
		if reply == "" || strings.Contains(reply, "replaces") {
			t.Errorf("brief B during A's delivery: %q; want it recorded, replacing nothing", reply)
		}
		a.deliver(context.Background(), p, fx)
		a.deliver(context.Background(), p, fx)
		if got := strings.Join(d.calls, "|"); got != "w:rev: [obor owner] first|w:rev: [obor owner] second" {
			t.Errorf("delivered %q, want A once, then B once", got)
		}
	})
}

// TestBriefSupersedesSeveralLines (T-580): every undelivered brief line of the agent is retired by the one briefed
// fact, live and on replay.
func TestBriefSupersedesSeveralLines(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	first := p.queuedLines()[0].seq
	step := stepDeliver + strconv.FormatInt(first, 10)
	a.record(p, event.Record{Task: event.MsgTask, Attempt: 1, Step: step, Kind: event.KindIntent, Class: event.ClassPrompt}, lineData{To: "pj-rev"})
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact}, // A's delivery refused: undelivered again
		event.FactData{Machine: Machine, Reason: step + ": refused", Step: step})
	if len(p.queuedLines()) != 2 || p.queuedAt(first) == nil || p.queuedAt(first).state != lineWaiting {
		t.Fatalf("precondition: two undelivered brief lines, none claimed; queued %+v", p.queuedLines())
	}
	reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b3", Role: "lead"},
		To: "pj-rev", Text: "third"})
	if err != nil || !strings.HasSuffix(reply, "(replaces 2 undelivered briefs)") {
		t.Fatalf("brief C: %q, %v", reply, err)
	}
	check := func(when string, p *proj) {
		if q := p.queuedLines(); len(q) != 1 || q[0].msg.Line != "[obor owner] third" {
			t.Errorf("%s: queued %+v, want brief C alone", when, q)
		}
	}
	check("live", p)
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	facts := 0
	for _, rec := range recs {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Task == event.MsgTask && rec.Step == event.StepBriefed && len(m.Supersedes) > 0 {
			facts++
			if len(m.Supersedes) != 2 {
				t.Errorf("briefed %+v supersedes %v, want both lines", rec, m.Supersedes)
			}
		}
	}
	if facts != 1 {
		t.Errorf("briefed facts that supersede: %d, want the one", facts)
	}
	_, q := lineReload(t, a, p, &now)
	check("replay", q)
}

// TestBriefWorkDurableAtOnce: the work a brief's agent is seen doing is in the log as soon as its caller (a herdr status
// event, a snapshot, a fresh start's brief) returns, before any delivery pass: a restart right then still gets the
// finished line once the agent is idle on reconnect, for a brief with a text and a fresh start's brief (RV-50 #3,
// the retained observation → reload probes).
func TestBriefWorkDurableAtOnce(t *testing.T) {
	t.Parallel()
	working := func(a *App, p *proj) {
		pane := a.obs["w:rev"].pane
		pane.Status = "working"
		eventNow(a, port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
	}
	snapshot := func(a *App, p *proj) {
		lead, rev := a.obs["w:lead"].pane, a.obs["w:rev"].pane
		rev.Status = "working"
		a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{lead, rev}})
	}
	for _, c := range []struct {
		name string
		note string
		seen func(*App, *proj)
	}{
		{"verify/event", "verify finding 1", working},
		{"fresh/event", "", working},
		{"verify/snapshot", "verify finding 1", snapshot},
		{"fresh/snapshot", "", snapshot},
		{"fresh/brief", "", nil}, // the pane works already when the fresh start's brief is recorded
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			if c.seen == nil {
				lineStatus(a, "w:rev", "working", now)
			}
			if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
				To: "pj-rev", Text: c.note}); err != nil {
				t.Fatal(err)
			}
			l := &linerSpy{}
			if c.note != "" {
				a.deliver(context.Background(), p, linerLoop(l)) // the brief's line typed
				if len(l.calls) != 1 || p.out.brief("pj-rev").worked {
					t.Fatalf("precondition: the line typed, no work seen yet; calls %v", l.calls)
				}
			}
			if c.seen != nil {
				c.seen(a, p)
			}
			if b := p.out.brief("pj-rev"); !b.worked || !b.logged {
				t.Fatalf("the work seen is not in the log once its caller returns: %+v", *b)
			}
			b, q := lineReload(t, a, p, &now) // no pass since; the reviewer idle on reconnect
			if q.out.brief("pj-rev") == nil || !q.out.brief("pj-rev").worked {
				t.Fatal("the brief's observed work is not replayed")
			}
			out := &linerSpy{}
			b.deliver(context.Background(), q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			if !lineTyped(out.calls, "finished") || q.briefCount() != 0 {
				t.Fatalf("want one finished line after reload, got %v (open briefs %d)", out.calls, q.briefCount())
			}
		})
	}
}

// TestFinishedLineAgeFromTyping: a finished line's age counts from when the brief's line was typed, also across a
// restart, not from when the brief was recorded (a brief queued for an hour is not "briefed 1h ago").
func TestFinishedLineAgeFromTyping(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now) // busy: the brief waits
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "verify finding 1"}); err != nil {
		t.Fatal(err)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 0 {
		t.Fatalf("precondition: the line waits for the working pane; typed %v", l.calls)
	}
	now = now.Add(time.Hour)
	lineStatus(a, "w:rev", "idle", now)
	a.deliver(context.Background(), p, linerLoop(l)) // typed an hour after the brief
	if len(l.calls) != 1 {
		t.Fatalf("precondition: the brief's line typed; calls %v", l.calls)
	}
	now = now.Add(2 * time.Minute)
	pane := a.obs["w:rev"].pane
	pane.Status = "working"
	eventNow(a, port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
	now = now.Add(time.Minute)
	b, q := lineReload(t, a, p, &now) // idle on reconnect, 3 minutes after the typing
	out := &linerSpy{}
	b.deliver(context.Background(), q, linerLoop(out))
	b.deliver(context.Background(), q, linerLoop(out))
	if want := "pj-lead: [obor] pj-rev finished (briefed 3m ago)"; len(out.calls) != 1 || out.calls[0] != want {
		t.Fatalf("want %q, got %v", want, out.calls)
	}
}

// registerLead rewrites root's config with prefix: the lead is <prefix>-lead (obor setup rename's change, T-765: a
// lead's agent name is its prefix's).
func registerLead(t *testing.T, root, prefix string) {
	t.Helper()
	cfg := `{"name":"proj","prefix":"` + prefix + `","roles":{"lead":"","integrator":"","reviewer":""}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestQueuedLeadLineAfterRename: a lead line (a send, a finished line) queued for want of an inbox is
// neither typed into the old lead's idle pane after the lead is renamed and serve restarts, nor lost: it is posted
// once its inbox is recorded (V1).
func TestQueuedLeadLineAfterRename(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"send", "finished"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			a.opt.Inboxed = func(string) (string, bool) { return "", false }
			queueLine(t, a, p, kind, now)
			first := &linerSpy{}
			a.deliver(context.Background(), p, &Loop{Typer: first, Inbox: &inboxSpy{}}) // records the finished line
			if len(first.calls) != 0 || len(p.queuedLines()) != 1 || !p.queuedLines()[0].lead {
				t.Fatalf("precondition: one lead line queued, nothing typed; typed %v, queued %d", first.calls, len(p.queuedLines()))
			}

			registerLead(t, p.root, "pj-new")
			b, q := lineReload(t, a, p, &now)
			if q.out.lead() != "pj-new-lead" || len(q.queuedLines()) != 1 {
				t.Fatalf("precondition: renamed lead %q with one line queued, got %d", q.out.lead(), len(q.queuedLines()))
			}
			pane := b.obs["w:lead"].pane
			pane.Label = "pj-lead" // the old lead's session still open and idle under its old name
			eventNow(b, port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
			if w := b.whereIs(q, "pj-lead"); w.State != Identified || !Ready(w.Pane.Status) {
				t.Fatal("precondition: the old lead's pane is idle under its old name")
			}

			b.opt.Inboxed = func(string) (string, bool) { return "", false }
			lines, posts := &linerSpy{}, &inboxSpy{}
			b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
			if len(lines.calls) != 0 || len(posts.calls) != 0 || len(q.queuedLines()) != 1 {
				t.Fatalf("no inbox: typed %q, posted %q, queued %d; want nothing tried, still queued", lines.calls,
					posts.calls, len(q.queuedLines()))
			}

			b.opt.Inboxed = func(string) (string, bool) { return "", true }
			b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
			b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
			if len(lines.calls) != 0 || len(posts.calls) != 1 || !strings.HasPrefix(posts.calls[0], "pj-lead: [obor") ||
				len(q.queuedLines()) != 0 {
				t.Errorf("inbox recorded: typed %q, posted %q, queued %d; want posted once, never typed", lines.calls,
					posts.calls, len(q.queuedLines()))
			}
		})
	}
}

// TestRunningLeadHint: the queued send's reply names `obor restart <lead>` (obor lead starts nothing for a
// running lead), and the upgrade guide and state-model row 47 say the same (V2).
func TestRunningLeadHint(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	a.opt.Inboxed = func(string) (string, bool) { return "", false }
	reply, err := a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "s1"}, From: "other",
		Text: "hello"})
	hint := "`obor restart pj-lead` once it is idle registers it, or quit it and run `obor lead`"
	if err != nil || !strings.Contains(reply, hint) || strings.Contains(reply, "obor lead registers") {
		t.Errorf("reply %q (%v); want it to name %s", reply, err, hint)
	}
	for file, want := range map[string]string{
		"state-model.md": strings.ReplaceAll(hint, "pj-lead", "<lead>"),
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", file))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(b)), " ") // the guide wraps its lines
		if !strings.Contains(text, want) || strings.Contains(text, "obor lead registers it") ||
			strings.Contains(text, "restarted once (`obor lead`)") {
			t.Errorf("docs/%s: want %q, and no recipe that has obor lead register a running lead", file, want)
		}
	}
}

// TestBriefStatus (T-664): the app's status lists a role agent's open brief: not delivered while its line is queued,
// delivered once typed, worked on once its pane worked; none once it finished.
func TestBriefStatus(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	if got := a.Status().Project.Briefs; len(got) != 0 {
		t.Fatalf("precondition: no brief, got %+v", got)
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "verify 1"}); err != nil {
		t.Fatal(err)
	}
	if got := a.Status().Project.Briefs; len(got) != 1 || got[0].To != "pj-rev" || got[0].Delivered || got[0].Since == 0 {
		t.Errorf("briefed, its line queued: %+v; want pj-rev, not delivered, its time", got)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: the brief's line typed, got %v", l.calls)
	}
	if got := a.Status().Project.Briefs; len(got) != 1 || !got[0].Delivered || got[0].Worked {
		t.Errorf("its line typed: %+v; want delivered, not worked on", got)
	}
	lineStatus(a, "w:rev", "working", now)
	a.deliver(context.Background(), p, linerLoop(l))
	if got := a.Status().Project.Briefs; len(got) != 1 || !got[0].Worked {
		t.Errorf("its pane worked: %+v; want worked on", got)
	}
}

// T-748 (RV-90): a queued line's delivery as explicit states. The case → test table: each state a line can be in
// while queued (waiting, sending, sending with its question settled) × each event (delivered, a refusal before a byte,
// an error after a byte, its question settled, its task dropped, replay), one subtest per row, run through the log's
// records on T-1's answer to pj-l-01 (busyAnswered). Sent and retired lines leave the outbox: next names which.

// answerSeq is the seq of h's one queued line (precondition: exactly one).
func answerSeq(t *testing.T, h *harness) int64 {
	t.Helper()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	qs := h.proj().queuedLines()
	if len(qs) != 1 || !qs[0].answer {
		t.Fatalf("precondition: one queued answer, got %d lines", len(qs))
	}
	return qs[0].seq
}

// lineAt is the state of line seq in p (ok false: it left the outbox).
func lineAt(h *harness, p *proj, seq int64) (lineState, bool) {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	q := p.out.msgs[seq]
	if q == nil {
		return 0, false
	}
	return q.state, true
}

// deliverKey is line seq's delivery key step.
func deliverKey(seq int64) string { return stepDeliver + strconv.FormatInt(seq, 10) }

// lineRec records r with data in h's project (fatal when the log refuses it).
func lineRec(t *testing.T, h *harness, r event.Record, data any) {
	t.Helper()
	if !h.a.record(h.proj(), r, data) {
		t.Fatalf("%s not recorded", r.Step)
	}
}

// claimIntent records line seq's delivery intent: its delivery began (sending).
func claimIntent(t *testing.T, h *harness, seq int64) {
	lineRec(t, h, event.Record{Task: event.MsgTask, Step: deliverKey(seq), Kind: event.KindIntent,
		Class: event.ClassPrompt}, lineData{To: "pj-l-01"})
}

// submitT1 records T-1's submission: its question is settled another way.
func submitT1(t *testing.T, h *harness) {
	lineRec(t, h, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Lane: "L-01", Agent: "pj-l-01", Merge: true})
}

// answerIn is busyAnswered's answer moved into state from, by the log's records.
func answerIn(t *testing.T, from lineState) (*harness, int64) {
	t.Helper()
	h := busyAnswered(t)
	seq := answerSeq(t, h)
	if from != lineWaiting {
		claimIntent(t, h, seq)
	}
	if from == lineSendingSettled {
		submitT1(t, h)
	}
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != from {
		t.Fatalf("precondition: the answer in state %d (queued %v), want %d", s, ok, from)
	}
	return h, seq
}

func TestLineStateTable(t *testing.T) {
	t.Parallel()
	events := map[string]struct {
		e  lineEvent // the event next sees ("replay": none)
		do func(t *testing.T, h *harness, seq int64)
	}{
		"delivered": {lineDelivered, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: deliverKey(seq), Kind: event.KindResult}, "sent")
		}},
		"refused before a byte": {lineRefused, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
				event.FactData{Machine: Machine, Reason: deliverKey(seq) + ": dial: connection refused", Step: deliverKey(seq),
					Via: viaInbox})
		}},
		"error after a byte": {lineDelivered, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: deliverKey(seq), Kind: event.KindResult},
				"sent: broken pipe")
		}},
		"settled": {lineSettled, func(t *testing.T, h *harness, _ int64) { submitT1(t, h) }},
		"dropped": {lineSettled, func(t *testing.T, h *harness, _ int64) {
			lineRec(t, h, event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
				event.FactData{Lane: "L-01", Merge: true})
		}},
	}
	const gone = lineState(255) // the line left the outbox (sent or retired: next says which)
	for _, row := range []struct {
		from  lineState
		event string
		want  lineState
	}{
		{lineWaiting, "delivered", lineSent},
		{lineWaiting, "refused before a byte", lineWaiting},
		{lineWaiting, "error after a byte", lineSent},
		{lineWaiting, "settled", lineRetired},
		{lineWaiting, "dropped", lineRetired},
		{lineWaiting, "replay", lineWaiting},
		{lineSending, "delivered", lineSent},
		{lineSending, "refused before a byte", lineWaiting},
		{lineSending, "error after a byte", lineSent},
		{lineSending, "settled", lineSendingSettled},
		{lineSending, "dropped", lineSendingSettled},
		{lineSending, "replay", lineSending},
		{lineSendingSettled, "delivered", lineSent},
		{lineSendingSettled, "refused before a byte", lineRetired}, // RV-90 #1's race: settled, then refused
		{lineSendingSettled, "error after a byte", lineSent},
		{lineSendingSettled, "settled", lineSendingSettled},
		{lineSendingSettled, "dropped", lineSendingSettled},
		{lineSendingSettled, "replay", lineSendingSettled},
	} {
		name := []string{"waiting", "sending", "sending-settled"}[row.from] + "/" + row.event
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, seq := answerIn(t, row.from)
			if row.event == "replay" {
				s, live := lineAt(h, h.proj(), seq)
				if r, ok := lineAt(h, replayed(t, h), seq); !live || !ok || s != row.want || r != row.want {
					t.Errorf("live %d (queued %v), replayed %d (queued %v); want %d both", s, live, r, ok, row.want)
				}
				return
			}
			ev := events[row.event]
			if got := row.from.next(ev.e); got != row.want {
				t.Errorf("next: %d, want %d", got, row.want)
			}
			ev.do(t, h, seq)
			want := row.want
			if want == lineSent || want == lineRetired {
				want = gone
			}
			check := func(when string, p *proj) {
				got, ok := lineAt(h, p, seq)
				if !ok {
					got = gone
				}
				if got != want {
					t.Errorf("%s: state %d, want %d (%d: left the outbox)", when, got, want, gone)
				}
			}
			check("live", h.proj())
			check("replay", replayed(t, h))
		})
	}
}

// Ended is ended: nothing moves a sent or retired line.
func TestLineStateEndedStays(t *testing.T) {
	t.Parallel()
	for _, s := range []lineState{lineSent, lineRetired} {
		for _, e := range []lineEvent{lineClaimed, lineDelivered, lineRefused, lineSettled, lineClosed} {
			if got := s.next(e); got != s {
				t.Errorf("%d on %d: %d, want it to stay", s, e, got)
			}
		}
	}
}

// RV-90 #1 (probe TestRV90RefusedAndSettled): a post refused before a byte, then the question settled (task_done):
// the answer retires, the next idle sends nothing, and replay keeps it retired.
func TestRV90RefusedAndSettled(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	fx := &Loop{Inbox: in, Typer: &linerSpy{}}
	h.a.deliver(context.Background(), h.proj(), fx)
	seq := answerSeq(t, h)
	if s, _ := lineAt(h, h.proj(), seq); len(in.calls) != 1 || s != lineWaiting {
		t.Fatalf("precondition: one refused post, the answer waiting; posts %q, state %d", in.calls, s)
	}
	submitT1(t, h)
	if q := h.lines(); len(q) != 0 {
		t.Errorf("queued %q after task_done; want the refused answer retired", q)
	}
	in.refuse = false
	typer := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
	if len(in.calls) != 1 || len(typer.calls) != 0 {
		t.Errorf("posts %q, typed %q at the next idle; want the settled answer never sent", in.calls, typer.calls)
	}
	if _, ok := lineAt(h, replayed(t, h), seq); ok {
		t.Error("replay brought the retired answer back")
	}
}

// RV-90 #2 (probe TestRV90RefusedReason): a refused post keeps its reason (obor status, the needs-you), and the next
// attempt to the idle identified worker types it instead of dialling the dead socket again.
func TestRV90RefusedReason(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in})
	if len(in.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", in.calls)
	}
	const why = "its delivery was refused before a byte went out: agent_not_ready: no typing"
	if ms := h.a.Status().Project.Messages; len(ms) != 1 || ms[0].Held != why {
		t.Errorf("status lines %+v; want the answer held %q", ms, why)
	}
	h.now = h.now.Add(msgWait + time.Minute) // nothing types here: the post is tried again, refused again
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in})
	var reasons []string
	for _, f := range h.a.Status().Project.Facts {
		if f.Task == event.MsgTask && f.Step == event.StepNeedsYou {
			reasons = append(reasons, f.Fact().Reason)
		}
	}
	if len(in.calls) != 2 || len(reasons) != 1 || !strings.HasSuffix(reasons[0], "(undelivered: "+why+")") {
		t.Errorf("posts %q, needs-you %q; want a second refused post and the needs-you naming its refusal", in.calls,
			reasons)
	}
	typer := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
	if len(in.calls) != 2 || !slices.Equal(typer.calls, []string{"w1:p1: [obor answer] move the helpers down"}) ||
		len(h.lines()) != 0 {
		t.Errorf("posts %q, typed %q, queued %q; want the answer typed into w1:p1 after the refused post", in.calls,
			typer.calls, h.lines())
	}
}

// The typed fallback re-checks its target: after a refused post, a worker that is busy now gets nothing typed (held
// busy); idle again, the line is typed; replay keeps the refusal, so the restarted pass types it too.
func TestRefusedPostTypedOnlyWhenIdle(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in})
	seq := answerSeq(t, h)
	r := replayed(t, h)
	h.a.mu.Lock()
	rq := r.out.msgs[seq]
	h.a.mu.Unlock()
	if rq == nil || !rq.refusedPost || rq.state != lineWaiting || !strings.Contains(rq.refused, "agent_not_ready") {
		t.Errorf("replayed line %+v; want it waiting with its refused post and reason", rq)
	}
	h2 := staleHerdr(t)
	in2 := &linerSpy{refuse: true}
	h2.a.deliver(context.Background(), h2.proj(), &Loop{Inbox: in2})
	h2.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	typer := &linerSpy{}
	h2.a.deliver(context.Background(), h2.proj(), &Loop{Inbox: in2, Typer: typer})
	if len(in2.calls) != 1 || len(typer.calls) != 0 || len(h2.lines()) != 1 {
		t.Fatalf("busy: posts %q, typed %q; want nothing sent to the working worker", in2.calls, typer.calls)
	}
	h2.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	h2.a.deliver(context.Background(), h2.proj(), &Loop{Inbox: in2, Typer: typer})
	if len(in2.calls) != 1 || len(typer.calls) != 1 || len(h2.lines()) != 0 {
		t.Errorf("idle: posts %q, typed %q, queued %q; want it typed once", in2.calls, typer.calls, h2.lines())
	}
}

// A lead line's refused post keeps its reason too (obor status), and is posted again, never typed (T-748).
func TestLeadLineRefusedReason(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 1)
	l := &linerSpy{refuse: true}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", l.calls)
	}
	ms := a.Status().Project.Messages
	if len(ms) != 1 || !strings.Contains(ms[0].Held, "refused before a byte went out: agent_not_ready") {
		t.Errorf("status lines %+v; want the lead line held with its refusal", ms)
	}
	l.refuse = false
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !strings.HasPrefix(l.calls[1], "pj-lead: ") || len(p.queuedLines()) != 0 {
		t.Errorf("calls %q; want the lead line posted again to pj-lead (not typed into its pane)", l.calls)
	}
}

// RV-90 #3 (probe TestRV90Drop): obor drop retires the task's undelivered answer; the lane's worker idle again gets
// nothing. A reject retires it the same way.
func TestRV90Drop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rec  event.Record
		data any
	}{
		{"drop", event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Merge: true}},
		{"reject", event.Record{Task: "T-1", Attempt: 1, Step: "reject.r1", Kind: event.KindResult},
			verdictResult{Job: "T-1/1/review", Reply: "REJECT T-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := busyAnswered(t)
			lineRec(t, h, tc.rec, tc.data)
			if q := h.lines(); len(q) != 0 {
				t.Errorf("queued %q; want the answer retired", q)
			}
			h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
			in, typer := &kindSpy{}, &linerSpy{}
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
			if len(in.calls) != 0 || len(typer.calls) != 0 {
				t.Errorf("posted %q, typed %q; want nothing sent to the lane's worker", in.calls, typer.calls)
			}
		})
	}
}

// blockingPoster posts nothing until release closes: started is closed once its first Post began (a delivery whose
// effect is in flight).
type blockingPoster struct {
	started, release chan struct{}
	once             sync.Once
}

func (b *blockingPoster) Post(context.Context, string, string, string) (bool, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return true, nil
}

// closedUnsent: line seq's delivery has result retired in h's log, and no other result.
func closedUnsent(h *harness, seq int64) (closed bool, others []string) {
	for _, r := range h.records() {
		if r.Kind != event.KindResult || r.Step != deliverKey(seq) {
			continue
		}
		if strings.Contains(string(r.Data), retiredResult) {
			closed = true
		} else {
			others = append(others, string(r.Data))
		}
	}
	return closed, others
}

// msgNeeds is the needs-you facts of h's lines.
func msgNeeds(h *harness) []string {
	var out []string
	for _, f := range h.a.Status().Project.Facts {
		if f.Task == event.MsgTask && f.Step == event.StepNeedsYou {
			out = append(out, f.Fact().Reason)
		}
	}
	return out
}

// A line settled while its delivery was open (a crash cut it: its intent pending, no result) is never sent again:
// the next pass closes its delivery unsent (result retired), whatever its worker's pane (T-749, RV-91 #1: idle,
// working, not identified, two panes claiming it, its lane released), live and on replay; it leaves obor status and
// raises no needs-you, even past msgWait.
func TestSettledOpenDeliveryClosedUnsent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		pane  func(h *harness) // the worker's pane before the pass
		where string           // the precondition: pj-l-01's place in the identity table, as h.where says it (prefix)
	}{
		{"idle", func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" }) }, "identified w1:p1"},
		{"working", func(*harness) {}, "identified w1:p1"},
		{"not identified", func(h *harness) { h.remove("w1:p1", port.EventClosed) }, "unavailable"},
		{"ambiguous", func(h *harness) { // a second pane with the same proof (its session)
			tw := h.twin()
			tw.Session = h.pane("w1:p1").Session
			h.add(tw)
		}, "ambiguous"},
		{"lane released", func(h *harness) {
			h.freeLanes()
			h.remove("w1:p1", port.EventExited)
		}, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, seq := answerIn(t, lineSendingSettled)
			tc.pane(h)
			if w := h.where("pj-l-01"); !strings.HasPrefix(w, tc.where) {
				t.Fatalf("precondition: pj-l-01 is %q, want %s", w, tc.where)
			}
			if ms := h.a.Status().Project.Messages; len(ms) != 1 {
				t.Fatalf("precondition: status lines %+v, want the settled answer shown", ms)
			}
			h.now = h.now.Add(msgWait + time.Minute) // it would raise its needs-you if it stayed
			in, typer := &kindSpy{}, &linerSpy{}
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
			if len(in.calls) != 0 || len(typer.calls) != 0 {
				t.Errorf("posted %q, typed %q; want the settled answer not sent again", in.calls, typer.calls)
			}
			if _, ok := lineAt(h, h.proj(), seq); ok {
				t.Error("the settled line is still queued after the pass")
			}
			if ms := h.a.Status().Project.Messages; len(ms) != 0 {
				t.Errorf("status lines %+v; want the closed answer gone", ms)
			}
			if n := msgNeeds(h); len(n) != 0 {
				t.Errorf("needs-you of the lines %q; want none for a closed line", n)
			}
			if closed, others := closedUnsent(h, seq); !closed || len(others) != 0 {
				t.Errorf("retired result %v, other results %q; want its delivery closed unsent only", closed, others)
			}
			if _, ok := lineAt(h, replayed(t, h), seq); ok {
				t.Error("replay brought the closed line back")
			}
		})
	}
}

// A serve restart replays the settled open delivery (sending, settled); the new serve's first pass closes it, its
// worker still working, and a replay after that keeps it closed (T-749).
func TestSettledOpenDeliveryClosedAfterReplay(t *testing.T) {
	t.Parallel()
	h, seq := answerIn(t, lineSendingSettled)
	h.a.closeLog()
	a := New(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	p, err := a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := lineAt(&harness{a: a}, p, seq); !ok || s != lineSendingSettled {
		t.Fatalf("precondition: replayed line in state %d (queued %v), want sending settled", s, ok)
	}
	h.now = h.now.Add(msgWait + time.Minute)
	in, typer := &kindSpy{}, &linerSpy{}
	a.deliver(context.Background(), p, &Loop{Inbox: in, Typer: typer})
	if len(in.calls) != 0 || len(typer.calls) != 0 {
		t.Errorf("posted %q, typed %q; want nothing sent", in.calls, typer.calls)
	}
	if _, ok := lineAt(&harness{a: a}, p, seq); ok {
		t.Error("the replayed settled line is still queued after the pass")
	}
	p.log.Close()
	if closed, others := closedUnsent(h, seq); !closed || len(others) != 0 {
		t.Errorf("retired result %v, other results %q; want its delivery closed unsent only", closed, others)
	}
	if _, ok := lineAt(h, replayed(t, h), seq); ok {
		t.Error("a second replay brought the closed line back")
	}
}

// A delivery whose effect is in flight (its post runs now) when its question is settled is left to its outcome: a
// pass meanwhile closes nothing, and the post's result (sent) is what ends the line (T-749).
func TestSettledInFlightDeliveryLeftAlone(t *testing.T) {
	t.Parallel()
	h := busyAnswered(t)
	seq := answerSeq(t, h)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	post := &blockingPoster{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: post, Typer: &linerSpy{}})
	}()
	<-post.started
	submitT1(t, h)
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != lineSendingSettled {
		t.Fatalf("precondition: the in-flight line in state %d (queued %v), want sending settled", s, ok)
	}
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: &kindSpy{}, Typer: &linerSpy{}})
	if closed, others := closedUnsent(h, seq); closed || len(others) != 0 {
		t.Errorf("retired result %v, other results %q while its post runs; want none", closed, others)
	}
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != lineSendingSettled {
		t.Errorf("the in-flight line in state %d (queued %v); want it left sending settled", s, ok)
	}
	close(post.release)
	<-done
	if closed, others := closedUnsent(h, seq); closed || len(others) != 1 || others[0] != `"sent"` {
		t.Errorf("retired result %v, other results %q; want the post's result sent only", closed, others)
	}
	if _, ok := lineAt(h, h.proj(), seq); ok {
		t.Error("the delivered line is still queued")
	}
}

// Two passes logging a brief's seen work at once (the merge loop's and the watchdog's) record its fact worked once:
// the brief is claimed under mu while its fact is recorded (T-615 attempt 3; the gate saw two).
func TestLogWorkedTwoPassesAtOnceRecordOneFact(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	a := &App{opt: Options{Now: func() time.Time { return logNow }, Out: io.Discard}}
	p := &proj{name: "proj", log: log, folded: newFolded(), out: newOutbox("pj-lead", "")}
	const rounds = 40
	for i := range rounds {
		agent := "pj-r" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		b := &brief{seq: int64(1000 + i), worked: true}
		p.out.briefs[agent] = b
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); a.logWorked(p) }()
		}
		wg.Wait()
		if !b.logged || b.logging {
			t.Fatalf("round %d: brief logged %v, logging %v; want its fact folded and no claim left", i, b.logged, b.logging)
		}
	}
	db.Close()

	db, err = store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, recs, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	per := map[int64]int{}
	for _, r := range recs {
		if r.Task == event.MsgTask && r.Step == event.StepWorked {
			var d workedData
			_ = json.Unmarshal(r.Data, &d)
			per[d.Brief]++
		}
	}
	if len(per) != rounds {
		t.Fatalf("worked facts for %d briefs, want %d: %v", len(per), rounds, per)
	}
	for seq, n := range per {
		if n != 1 {
			t.Errorf("brief %d: %d worked facts, want 1", seq, n)
		}
	}
}
