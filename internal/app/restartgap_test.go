package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"sync/atomic"
	"testing"
)

// showsRestart: obor status' running line shows the pending self-restart.
func (r *concRig) showsRestart(name string) bool {
	for _, s := range r.status(name).Running {
		if s.Step == event.StepServeRestart {
			return true
		}
	}
	return false
}

// T-972: a pending self-restart holds nothing back. While it waits, lane tests start and end in turn, and the restart
// goes in the first pass where none runs, never cutting one. (T-965's drain and the 10-minute deadline are gone.)
func TestPendingRestartGoesInTheFirstGap(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	f := r.fakes["pj"]
	// T-1's lane test waits for its own release; T-5's gate waits, so its candidate comes later, while the restart
	// is pending.
	f.hold, f.holdTest = "T-5", "T-1"
	f.unbased = false // a green gate's candidate has a Base: its lane test is next
	f.testRelease = make(chan struct{})

	// T-1's gate is green with a Base: its lane test is the work running when the new binary appears.
	r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{
		Task: "T-1", Lane: "L-01", SHA: "c-T-1", Target: "b", Base: "b0", Patch: "p1"}})

	var calls atomic.Int32
	r.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f, Kit: r.kit,
			// serve ends on the restart, as its exec does.
			Restart: func(context.Context) error { calls.Add(1); r.cancel(); return nil }}, nil
	}
	r.a.opt.Binary = func() bool { return true }
	r.run()
	defer r.stop()

	// Precondition: T-1's lane test runs, its result not yet folded; no restart.
	r.waitFor("T-1's lane test", func() bool { return f.count(f.tests, "T-1") == 1 })
	if s := r.state("pj", "T-1"); s != stCandidate {
		t.Fatalf("precondition: T-1 is %s, want candidate", s)
	}

	// A tick sees a new binary: the restart is pending, and it waits for the running test.
	r.a.mu.Lock()
	r.a.follows++
	r.a.mu.Unlock()
	r.wake()
	r.waitFor("the pending restart shown", func() bool { return r.showsRestart("pj") })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times with T-1's lane test running, want 0", n)
	}

	// T-5's gate goes green while the restart waits: its lane test starts beside the pending restart (T-972: no
	// drain), runs and ends, and the restart holds for it.
	close(f.release)
	r.waitFor("T-5's lane test", func() bool { return f.count(f.tests, "T-5") == 1 })
	r.waitFor("T-5 tested", func() bool { return r.count("pj", "T-5", event.KindResult, event.StepTest) == 1 })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times while T-1's lane test runs, want 0", n)
	}

	// A task submitted now (T-6) goes through its gate to a lane test beside the pending restart, in turn with
	// T-5's: the restart still waits.
	r.add("pj", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06"})
	r.add("pj", "T-6", event.StepSubmit, event.KindResult, submitOut{SHA: "s6"})
	r.waitFor("T-6's lane test", func() bool { return f.count(f.tests, "T-6") == 1 })
	r.waitFor("T-6 tested", func() bool { return r.count("pj", "T-6", event.KindResult, event.StepTest) == 1 })
	if n := calls.Load(); n != 0 {
		t.Fatalf("restarted %d times with a lane test running or just ended, want 0", n)
	}

	// T-1's test ends: the first pass where nothing runs, however long it took, and the restart goes.
	close(f.testRelease)
	r.waitFor("the restart", func() bool { return calls.Load() == 1 })

	// No lane test was cut: each one's green result is folded, and none failed.
	for _, task := range []string{"T-1", "T-5", "T-6"} {
		if n := r.count("pj", task, event.KindResult, event.StepTest); n != 1 {
			t.Errorf("%s's test results %d, want the one green result it folded", task, n)
		}
	}
	if n := r.count("pj", "T-1", event.KindFact, event.StepFailed) +
		r.count("pj", "T-5", event.KindFact, event.StepFailed) +
		r.count("pj", "T-6", event.KindFact, event.StepFailed); n != 0 {
		t.Errorf("failed steps %d, want none: a cut lane test would be one", n)
	}
}
