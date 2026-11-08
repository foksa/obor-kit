package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

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
	a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{
		{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
		{ID: "w:role", Workspace: "w", Cwd: root, Agent: kind, Label: agent, Status: status},
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
				reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: c.agent, Note: "look at x", Fresh: true,
					Role: "lead", Agent: "pj-lead"})
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
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-advisor", Note: "old", Role: "lead",
		Agent: "pj-lead"}); err != nil {
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
	reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-advisor", Note: "new", Fresh: true,
		Role: "lead", Agent: "pj-lead"})
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
		call wire.Call
		want string
	}{
		{"a worker calls", wire.Call{To: "pj-advisor", Note: "x", Role: "worker", Agent: "pj-l-01"}, "the lead or the owner briefs"},
		{"the integrator", wire.Call{To: "pj-int", Note: "x", Role: "lead"}, "no role agent serve starts fresh"},
		{"no text", wire.Call{To: "pj-advisor", Note: " ", Role: "lead"}, "--fresh takes a text"},
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
	_, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-deep-dive",
		Note: "look at x", Fresh: true})
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
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-researcher", Note: "dig", Fresh: true,
		Role: "lead", Agent: "pj-lead"}); err != nil {
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
