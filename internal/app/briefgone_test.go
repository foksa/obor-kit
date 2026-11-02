package app_test

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strconv"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/herdr/herdrtest"
)

// T-732: a worked brief ends with the usual finished line once its agent is gone: its pane at its shell or closed, or
// not identified for a minute; a brief not worked on, or under a usage-limit stop, stays open.

// briefWorked is the line rig with pj-rev briefed, its line delivered and its work seen (working, no idle yet).
func briefWorked(t *testing.T) *loopRig {
	r := lineRig(t, "idle", "idle")
	r.start()
	if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "verify finding 3"}); err != nil {
		t.Fatal(err)
	}
	r.waitBriefDelivered()
	r.agentIs("w1:p6", "pj-rev", "working")
	r.waitFor("the work in the log", func() bool { return r.taskStep(event.MsgTask, "fact worked") == 1 })
	r.passes()
	if finishedLines(r) != 0 || len(r.briefs()) != 1 {
		t.Fatalf("working: %d finished lines, briefs %+v; want none, one open", finishedLines(r), r.briefs())
	}
	return r
}

// briefs is the open briefs obor status shows.
func (r *loopRig) briefs() []wire.BriefStatus { return r.app.Status().Project.Briefs }

// agentGone changes pane by fn and waits until obor status no longer shows agent.
func (r *loopRig) agentGone(pane, agent string, fn func(p *herdrtest.Pane)) {
	r.t.Helper()
	r.f.Set(pane, fn)
	r.waitFor(agent+" gone", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == agent {
				r.push()
				return false
			}
		}
		return true
	})
}

func TestBriefEndsWhenItsAgentIsGone(t *testing.T) {
	t.Parallel()
	t.Run("exits at its shell", func(t *testing.T) {
		t.Parallel()
		r := briefWorked(t)
		r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
		r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
		r.passes()
		if n, b := finishedLines(r), r.briefs(); n != 1 || len(b) != 0 {
			t.Errorf("%d finished lines, briefs %+v; want one, none open", n, b)
		}
	})
	t.Run("pane closed", func(t *testing.T) {
		t.Parallel()
		r := briefWorked(t)
		r.f.Remove("w1:p6")
		r.waitFor("the finished line", func() bool { r.push(); return finishedLines(r) == 1 })
		if b := r.briefs(); len(b) != 0 {
			t.Errorf("briefs %+v, want none open", b)
		}
	})
	t.Run("not identified", func(t *testing.T) {
		t.Parallel()
		r := briefWorked(t)
		// The pane still runs its agent, but nothing proves it is pj-rev (its label and name gone).
		r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Name, p.Label = "", "" })
		r.passes()
		r.later(30 * time.Second)
		if n, b := finishedLines(r), r.briefs(); n != 0 || len(b) != 1 {
			t.Fatalf("30s not identified: %d finished lines, briefs %+v; want none, one open", n, b)
		}
		r.later(31 * time.Second)
		r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
		if b := r.briefs(); len(b) != 0 {
			t.Errorf("61s not identified: briefs %+v, want none open", b)
		}
	})
}

// A brief not worked on stays open when its agent stops (a brief to a stopped role agent starts it).
func TestBriefNotWorkedStaysOpenWhenItsAgentStops(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.start()
	if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "verify finding 3"}); err != nil {
		t.Fatal(err)
	}
	r.waitBriefDelivered()
	r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
	r.later(2 * time.Minute)
	if n, b := finishedLines(r), r.briefs(); n != 0 || len(b) != 1 || b[0].To != "pj-rev" {
		t.Errorf("%d finished lines, briefs %+v; want none, pj-rev's open", n, b)
	}
}

// A usage-limit stop in force keeps the brief open though its agent exits.
func TestBriefUnderAStopStaysOpenWhenItsAgentIsGone(t *testing.T) {
	t.Parallel()
	r := claudeRoleStop(t, true, "rate_limit", resetIn(time.Hour))
	r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
	r.later(2 * time.Minute)
	if n, b := finishedLines(r), r.briefs(); n != 0 || len(b) != 1 {
		t.Errorf("%d finished lines, briefs %+v; want none, one open", n, b)
	}
}

// A worked brief left open in the log (an earlier serve's) whose agent is gone ends on the first pass after replay.
func TestReplayedWorkedBriefOfAGoneAgentEnds(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "")
	r.f.Agent("w1:p5", "pj-lead", "idle", r.p.Root).Workspace = "w1"
	r.f.Shell("w1:p6", "pj-rev", r.p.Root).Workspace = "w1" // its agent stopped
	at := r.clock.Now().Add(-2 * time.Hour)
	brief, _ := json.Marshal(event.Message{To: "pj-rev", From: "pj-lead", Line: "[obor pj-lead] look at x"})
	if err := app.WriteLog(r.p.Root, at, event.Record{Task: event.MsgTask, Step: event.StepBriefed,
		Kind: event.KindFact, Data: brief}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	recs, err := event.ReadLog(r.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Task == event.MsgTask && rec.Step == event.StepBriefed {
			seq = rec.Seq
		}
	}
	if seq == 0 {
		t.Fatal("no briefed fact in the log")
	}
	worked, _ := json.Marshal(map[string]any{"to": "pj-rev", "brief": seq})
	delivered, _ := json.Marshal(map[string]any{"to": "pj-rev"})
	if err := app.WriteLog(r.p.Root, at,
		event.Record{Task: event.MsgTask, Step: "deliver." + strconv.FormatInt(seq, 10), Kind: event.KindIntent, Data: delivered},
		event.Record{Task: event.MsgTask, Step: "deliver." + strconv.FormatInt(seq, 10), Kind: event.KindResult},
		event.Record{Task: event.MsgTask, Step: event.StepWorked, Kind: event.KindFact, Data: worked}); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
	if b := r.briefs(); len(b) != 0 {
		t.Errorf("briefs %+v, want none open", b)
	}
	if n := r.typedTo("pj-rev", "[obor pj-lead] look at x"); n != 0 {
		t.Errorf("the delivered brief typed %d times, want none", n)
	}
}
