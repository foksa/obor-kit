package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

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

// A brief moves only an idle deep dive's worktree: a working one (its pane working, or a brief open) keeps its
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

// obor brief --fresh is serve's: the brief is validated, admitted and recorded first, and only then does the
// delivery stop the running role agent (serve's role stop) and start it fresh on the brief's line.

// freshCfg names every role a brief may start fresh.
const freshCfg = `{"version":2,"project":{"name":"proj","agent_prefix":"pj"},"agents":{"roles":` +
	`{"integrator":"","reviewer":"","deep_dive":"","advisor":"","researcher":"","triage":""}}}`

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
		{ID: "w:lead", Group: "proj", Cwd: root, Agent: "claude", Label: p.out.Lead(), Status: "idle"},
		{ID: "w:role", Group: "proj", Cwd: root, Agent: kind, Label: agent, Status: status},
	}}, false)
	// An idle deep dive's brief moves its role worktree first.
	a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{Workflow: WorkflowOf(&reviewGitFake{head: "base1"})}), nil
	}
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
				_, atShell := a.shellPane(p, loadCfg(t, p.root), c.agent)
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
				s := &FakeAgents{}
				fx := withAppCfg(a, &Loop{Agents: s, Prompter: s})
				a.deliver(context.Background(), p, fx)
				want := []string{"start " + c.agent + " w:role"}
				if running {
					want = append([]string{"stop " + c.agent + " w:role force=false"}, want...)
				}
				if !slices.Equal(s.Events(), want) {
					t.Errorf("events %q, want %q", s.Events(), want)
				}
				if len(s.Launches()) != 1 || s.Launches()[0] != (port.Launch{Agent: c.agent, Pane: "w:role", Fresh: true,
					Prompt: "[obor pj-lead] look at x"}) {
					t.Errorf("launches %+v, want %s fresh in w:role on the brief's line", s.Launches(), c.agent)
				}
				same(t, len(s.Texts()), 0, "lines typed")
				key := event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + fmt.Sprint(seq)}
				same(t, resultOf(p, key), `"`+sentStart+`"`, "the line's delivery")
				same(t, freshNeeds(a, p), "", "needs-you")
				a.deliver(context.Background(), p, fx)
				same(t, len(s.Events()), len(want), "stops and starts after a second pass")
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
	s := &FakeAgents{}
	fx := withAppCfg(a, &Loop{Agents: s, Prompter: s})
	a.deliver(context.Background(), p, fx) // busy: the old line waits
	a.mu.Lock()
	queued := len(p.out.Queued())
	a.mu.Unlock()
	if queued != 1 || len(s.Events())+len(s.Texts()) != 0 {
		t.Fatalf("precondition: %d lines queued, events %q, typed %q", queued, s.Events(), s.Texts())
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
	if want := []string{"stop pj-advisor w:role force=false", "start pj-advisor w:role"}; !slices.Equal(s.Events(), want) {
		t.Errorf("events %q, want %q", s.Events(), want)
	}
	if len(s.Launches()) != 1 || s.Launches()[0].Prompt != "[obor pj-lead] new" {
		t.Errorf("launches %+v, want one start on the new text only", s.Launches())
	}
	same(t, len(s.Texts()), 0, "lines typed")
	a.mu.Lock()
	queued = len(p.out.Queued())
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
	s := &FakeAgents{}
	a.deliver(context.Background(), p, withAppCfg(a, &Loop{Agents: s, Prompter: s}))
	same(t, len(s.Events()), 0, "stops and starts")
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
	s := &FakeAgents{}
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
	for _, e := range s.Events() {
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
	s := &FakeAgents{OnStop: stopFails(errors.New("pj-researcher still working 30s after Escape"))}
	fx := withAppCfg(a, &Loop{Agents: s, Prompter: s})
	a.deliver(context.Background(), p, fx)
	a.msgNeedsYou(p, now)
	same(t, len(s.Events()), 1, "stops tried")
	same(t, len(s.Launches()), 0, "starts after a failed stop")
	a.mu.Lock()
	queued := p.out.Queued()
	a.mu.Unlock()
	if len(queued) != 1 || queued[0].Msg().Line != "[obor pj-lead] dig" {
		t.Fatalf("queued %+v, want the fresh brief still pending", queued)
	}
	if got := freshNeeds(a, p); !strings.Contains(got, "fresh brief to pj-researcher") || !strings.Contains(got, "still working") {
		t.Errorf("needs-you %q, want one naming pj-researcher and the stop's error", got)
	}

	now = now.Add(time.Second)
	a.deliver(context.Background(), p, fx)
	same(t, len(s.Events()), 1, "stops tried within roleStopRetry")

	now = now.Add(roleStopRetry)
	s.OnStop = nil
	a.deliver(context.Background(), p, fx)
	a.msgNeedsYou(p, now)
	if len(s.Launches()) != 1 || s.Launches()[0].Prompt != "[obor pj-lead] dig" || !s.Launches()[0].Fresh {
		t.Errorf("launches %+v, want the fresh start on the brief", s.Launches())
	}
	same(t, len(s.Texts()), 0, "lines typed")
	same(t, freshNeeds(a, p), "", "needs-you after the start")
}

// TestBriefCompletionReplay: a brief whose work serve saw before a restart (orderly and SIGKILL) gets its
// finished line when its agent is idle on reconnect, for a brief with a text and a fresh start's brief.
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
			l := &FakeAgents{}
			a.deliver(context.Background(), p, linerLoop(l))
			lineStatus(a, "w:rev", "working", now)
			finishBriefs(a, p, linerLoop(l)) // the steps that record the work
			lines := 0                       // the brief's own line
			if note != "" {
				lines = 1
			}
			if !p.out.Brief("pj-rev").Worked() || len(l.Lines()) != lines {
				t.Fatalf("precondition: work seen, no finished line yet; worked %v, calls %v", p.out.Brief("pj-rev").Worked(), l.Lines())
			}
			b, q := lineReload(t, a, p, &now) // the reviewer idle on reconnect
			if q.out.Brief("pj-rev") == nil || !q.out.Brief("pj-rev").Worked() {
				t.Fatal("the brief's observed work is not replayed")
			}
			out := &FakeAgents{}
			finishBriefs(b, q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			finishBriefs(b, q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			if !lineTyped(out.Lines(), "finished") || q.briefCount() != 0 {
				t.Fatalf("want one finished line after reload, got %v (open briefs %d)", out.Lines(), q.briefCount())
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
	l := &FakeAgents{}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev"}); err != nil {
		t.Fatal(err)
	}
	lineStatus(a, "w:rev", "working", now)
	a.deliver(context.Background(), p, linerLoop(l))
	if !p.out.Brief("pj-rev").Worked() {
		t.Fatal("precondition: the first brief's work is seen")
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	b, q := lineReload(t, a, p, &now)
	if br := q.out.Brief("pj-rev"); br == nil || br.Worked() {
		t.Fatalf("the newer brief is armed by the older one's work: %+v", br)
	}
	b.deliver(context.Background(), q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	if len(l.Lines()) != 1 || l.Lines()[0] != "w:rev: [obor owner] again" {
		t.Fatalf("want only the newer brief's line, got %v", l.Lines())
	}
}

// TestBriefKeepsAStartedDelivery: a line whose delivery is in flight (its intent is pending: it may have
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
	if len(p.out.Queued()) != 1 {
		t.Fatalf("precondition: one brief queued, got %d", len(p.out.Queued()))
	}
	q := p.out.Queued()[0]
	a.record(p, event.Record{Task: event.MsgTask, Attempt: 1, Step: outbox.StepDeliver + strconv.FormatInt(q.Seq(), 10),
		Kind: event.KindIntent, Class: event.ClassPrompt}, outbox.LineData{To: "pj-rev"})
	if m := p.out.Line(q.Seq()); m == nil || m.State() != outbox.Sending {
		t.Fatalf("precondition: the delivery in flight; queued %+v", p.out.Queued())
	}
	reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, "replaces") {
		t.Errorf("reply %q says it replaced a line whose delivery may have landed", reply)
	}
	if m := p.out.Line(q.Seq()); m == nil || m.State() != outbox.Sending {
		t.Errorf("the in-flight line was superseded: queued %+v", p.out.Queued())
	}
	if n := len(p.out.Queued()); n != 2 {
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

// A brief's supersede is its own fact: with the log refusing every
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
			if len(p.out.Queued()) != 1 {
				t.Fatalf("precondition: brief A queued, got %d", len(p.out.Queued()))
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
			for _, m := range q.out.Queued() {
				if m.Msg().Line == "[obor owner] first" {
					t.Errorf("brief A is queued again after the restart: %+v", m)
				}
			}
			again, err := b.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: q.root, CallID: "b2", Role: "lead"},
				To: "pj-rev", Text: note})
			if err != nil || again != reply {
				t.Errorf("retry of b2: %q, %v; want the stored reply %q", again, err, reply)
			}
			l := &FakeAgents{}
			lineStatus(b, "w:rev", "idle", now)
			b.deliver(context.Background(), q, linerLoop(l))
			b.deliver(context.Background(), q, linerLoop(l))
			want := []string{}
			if note != "" {
				want = []string{"w:rev: [obor owner] second"}
			}
			if strings.Join(l.Lines(), "|") != strings.Join(want, "|") {
				t.Errorf("delivered %v, want %v: brief A never", l.Lines(), want)
			}
		})
	}
}

// A line routed for delivery (no intent yet) that a newer brief supersedes
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
	q := *p.out.Queued()[0]
	l := &FakeAgents{}
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
	if len(l.Lines()) != 0 {
		t.Errorf("the retired line was transmitted: %v", l.Lines())
	}
	if _, written := p.log.Intent(event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(q.Seq(), 10)}); written {
		t.Errorf("an intent was written for the retired line")
	}
	a.deliver(context.Background(), p, fx)
	a.deliver(context.Background(), p, fx)
	if len(l.Lines()) != 1 || l.Lines()[0] != "w:rev: [obor owner] second" {
		t.Errorf("delivered %v, want brief B alone", l.Lines())
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
		d, during := &FakeAgents{}, true
		d.OnPrompt = func(context.Context, string, string, bool) (bool, error) {
			if during { // the first delivery, A's: its claim is set, B arrives now
				during = false
				var err error
				if reply, err = a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
					To: "pj-rev", Text: "second"}); err != nil {
					t.Error(err)
				}
			}
			return true, nil
		}
		fx := withAppCfg(a, &Loop{Prompter: d, Inbox: d})
		a.deliver(context.Background(), p, fx)
		if reply == "" || strings.Contains(reply, "replaces") {
			t.Errorf("brief B during A's delivery: %q; want it recorded, replacing nothing", reply)
		}
		a.deliver(context.Background(), p, fx)
		a.deliver(context.Background(), p, fx)
		if got := strings.Join(d.Lines(), "|"); got != "w:rev: [obor owner] first|w:rev: [obor owner] second" {
			t.Errorf("delivered %q, want A once, then B once", got)
		}
	})
}

// TestBriefSupersedesSeveralLines: every undelivered brief line of the agent is retired by the one briefed
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
	first := p.out.Queued()[0].Seq()
	step := outbox.StepDeliver + strconv.FormatInt(first, 10)
	a.record(p, event.Record{Task: event.MsgTask, Attempt: 1, Step: step, Kind: event.KindIntent, Class: event.ClassPrompt}, outbox.LineData{To: "pj-rev"})
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact}, // A's delivery refused: undelivered again
		event.FactData{Machine: Machine, Reason: step + ": refused", Step: step})
	if len(p.out.Queued()) != 2 || p.out.Line(first) == nil || p.out.Line(first).State() != outbox.Waiting {
		t.Fatalf("precondition: two undelivered brief lines, none claimed; queued %+v", p.out.Queued())
	}
	reply, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b3", Role: "lead"},
		To: "pj-rev", Text: "third"})
	if err != nil || !strings.HasSuffix(reply, "(replaces 2 undelivered briefs)") {
		t.Fatalf("brief C: %q, %v", reply, err)
	}
	check := func(when string, p *proj) {
		if q := p.out.Queued(); len(q) != 1 || q[0].Msg().Line != "[obor owner] third" {
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
// finished line once the agent is idle on reconnect, for a brief with a text and a fresh start's brief (the retained
// observation → reload probes).
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
		a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{lead, rev}}, false)
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
			l := &FakeAgents{}
			if c.note != "" {
				a.deliver(context.Background(), p, linerLoop(l)) // the brief's line typed
				if len(l.Lines()) != 1 || p.out.Brief("pj-rev").Worked() {
					t.Fatalf("precondition: the line typed, no work seen yet; calls %v", l.Lines())
				}
			}
			if c.seen != nil {
				c.seen(a, p)
			}
			if b := p.out.Brief("pj-rev"); !b.Worked() || !b.Logged() {
				t.Fatalf("the work seen is not in the log once its caller returns: %+v", *b)
			}
			b, q := lineReload(t, a, p, &now) // no pass since; the reviewer idle on reconnect
			if q.out.Brief("pj-rev") == nil || !q.out.Brief("pj-rev").Worked() {
				t.Fatal("the brief's observed work is not replayed")
			}
			out := &FakeAgents{}
			finishBriefs(b, q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			finishBriefs(b, q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			if !lineTyped(out.Lines(), "finished") || q.briefCount() != 0 {
				t.Fatalf("want one finished line after reload, got %v (open briefs %d)", out.Lines(), q.briefCount())
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
	l := &FakeAgents{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.Lines()) != 0 {
		t.Fatalf("precondition: the line waits for the working pane; typed %v", l.Lines())
	}
	now = now.Add(time.Hour)
	lineStatus(a, "w:rev", "idle", now)
	a.deliver(context.Background(), p, linerLoop(l)) // typed an hour after the brief
	if len(l.Lines()) != 1 {
		t.Fatalf("precondition: the brief's line typed; calls %v", l.Lines())
	}
	now = now.Add(2 * time.Minute)
	pane := a.obs["w:rev"].pane
	pane.Status = "working"
	eventNow(a, port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
	now = now.Add(time.Minute)
	b, q := lineReload(t, a, p, &now) // idle on reconnect, 3 minutes after the typing
	out := &FakeAgents{}
	finishBriefs(b, q, linerLoop(out))
	b.deliver(context.Background(), q, linerLoop(out))
	finishBriefs(b, q, linerLoop(out))
	b.deliver(context.Background(), q, linerLoop(out))
	if want := "pj-lead: [obor] pj-rev finished (briefed 3m ago)"; len(out.Lines()) != 1 || out.Lines()[0] != want {
		t.Fatalf("want %q, got %v", want, out.Lines())
	}
}

// TestBriefStatus: the app's status lists a role agent's open brief with its line: not delivered while its line is
// queued, delivered once typed, worked on once its pane worked; none once it finished.
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
	if got := a.Status().Project.Briefs; len(got) != 1 || got[0].To != "pj-rev" || got[0].Delivered || got[0].Since == 0 ||
		!strings.HasSuffix(got[0].Text, "] verify 1") {
		t.Errorf("briefed, its line queued: %+v; want pj-rev, not delivered, its time, its line", got)
	}
	l := &FakeAgents{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.Lines()) != 1 {
		t.Fatalf("precondition: the brief's line typed, got %v", l.Lines())
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

// TestBriefStatusTextBounded: a long brief's status carries the head of its line, wire.BriefTextMax bytes cut at a rune
// boundary, not the whole brief.
func TestBriefStatusTextBounded(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	long := strings.Repeat("é", wire.BriefTextMax)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: long}); err != nil {
		t.Fatal(err)
	}
	got := a.Status().Project.Briefs
	if len(got) != 1 {
		t.Fatalf("precondition: one brief, got %+v", got)
	}
	if txt := got[0].Text; txt == "" || len(txt) > wire.BriefTextMax || !utf8.ValidString(txt) || !strings.HasPrefix(txt, "[obor ") {
		t.Errorf("text %q (%d bytes): want its line's head, at most %d bytes, whole runes", txt, len(txt), wire.BriefTextMax)
	}
	if !got[0].TextClipped {
		t.Errorf("text of a %d-byte line: want marked clipped", len(long))
	}
}

// TestClipBrief: a brief's text of wire.BriefTextMax bytes is kept whole and not marked; one byte more is cut to the
// bound and marked clipped.
func TestClipBrief(t *testing.T) {
	t.Parallel()
	whole := wire.BriefStatus{Text: strings.Repeat("a", wire.BriefTextMax)}
	clipBrief(&whole)
	if len(whole.Text) != wire.BriefTextMax || whole.TextClipped {
		t.Errorf("%d bytes: got %d bytes, clipped %v; want kept whole, not clipped", wire.BriefTextMax, len(whole.Text), whole.TextClipped)
	}
	cut := wire.BriefStatus{Text: strings.Repeat("a", wire.BriefTextMax+1)}
	clipBrief(&cut)
	if len(cut.Text) != wire.BriefTextMax || !cut.TextClipped {
		t.Errorf("%d bytes: got %d bytes, clipped %v; want cut to %d, clipped", wire.BriefTextMax+1, len(cut.Text), cut.TextClipped, wire.BriefTextMax)
	}
}

// TestBriefStage: an open brief's stage is serve's: not delivered while its line is queued, working once typed and
// while its pane works or its status is unknown, waiting while its pane is blocked at a prompt, done once its pane
// worked and is ready (the rule that closes it), working when serve sees no pane for its agent (not connected).
func TestBriefStage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	stage := func() string {
		t.Helper()
		got := a.Status().Project.Briefs
		if len(got) != 1 {
			t.Fatalf("want one open brief, got %+v", got)
		}
		return got[0].Stage
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "verify 1"}); err != nil {
		t.Fatal(err)
	}
	if got := stage(); got != wire.BriefNotDelivered {
		t.Errorf("its line queued: stage %q, want %q", got, wire.BriefNotDelivered)
	}
	l := &FakeAgents{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.Lines()) != 1 {
		t.Fatalf("precondition: the brief's line typed, got %v", l.Lines())
	}
	if got := stage(); got != wire.BriefWorking {
		t.Errorf("typed, its pane idle, not worked on yet: stage %q, want %q", got, wire.BriefWorking)
	}
	for _, c := range []struct{ status, want string }{
		{"working", wire.BriefWorking},
		{"blocked", wire.BriefWaiting},
		{"unknown", wire.BriefWorking},
		{"idle", wire.BriefDone},
	} {
		lineStatus(a, "w:rev", c.status, now)
		if got := stage(); got != c.want {
			t.Errorf("its pane %s: stage %q, want %q", c.status, got, c.want)
		}
	}
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()
	if got := stage(); got != wire.BriefWorking {
		t.Errorf("worked, its pane not seen (serve not connected): stage %q, want %q", got, wire.BriefWorking)
	}
}

// A queued line's delivery as explicit states. The case → test table: each state a line can be in
// while queued (waiting, sending, sending with its question settled) × each event (delivered, a refusal before a byte,
// an error after a byte, its question settled, its task dropped, replay), one subtest per row, run through the log's
// records on T-1's answer to pj-l-01 (busyAnswered). Sent and retired lines leave the outbox: next names which.

// Two passes logging a brief's seen work at once (the merge loop's and the watchdog's) record its fact worked once:
// the brief is claimed under mu while its fact is recorded, so one pass records it and the other finds the claim.
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
	p := &proj{name: "proj", log: log, folded: newFolded(), out: outbox.New("pj-lead", "")}
	const rounds = 40
	for i := range rounds {
		agent := "pj-r" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		data, _ := json.Marshal(event.Message{To: agent}) // a brief without a line, its work seen
		p.out.Fold(event.Record{Seq: int64(1000 + i), Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact,
			Data: data}, p.oldLineOf)
		p.out.SawWork(agent)
		b := p.out.Brief(agent)
		if b == nil || !b.Worked() || b.Logged() {
			t.Fatalf("round %d: precondition: an open brief, its work seen, not logged; got %+v", i, b)
		}
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); a.logWorked(p) }()
		}
		wg.Wait()
		if !b.Logged() || b.Logging {
			t.Fatalf("round %d: brief logged %v, logging %v; want its fact folded and no claim left", i, b.Logged(), b.Logging)
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
			var d outbox.Worked
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

// finishBriefs is advance's two steps that end finished briefs, in its order: the seen work logged (logWorked), then
// the finished briefs' lines recorded (briefsDone).
func finishBriefs(a *App, p *proj, fx *Loop) {
	a.logWorked(p)
	a.briefsDone(p, fx, a.opt.Now())
}
