package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"sync/atomic"
	"testing"
)

// T-965 (docs/plan/merge-flow-rework.md §3 "the drain"): while a self-restart is pending, pick starts no new lane test
// — the restart waits for the running ones, so idle comes within one lane test — and the slot's steps still start: a
// publish runs. Once the running test ends, the next pass restarts serve.
func TestPendingRestartDrainsLaneTests(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	f := r.fakes["pj"]
	// T-1's and T-5's gates are green with a Base: both are candidates, so both would test. T-6 is accepted: its
	// publish is the slot step that must still start.
	r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{
		Task: "T-1", Lane: "L-01", SHA: "c-T-1", Target: "b", Base: "b0", Patch: "p1"}})
	r.add("pj", "T-5", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-05", Input: "s5"})
	r.add("pj", "T-5", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{
		Task: "T-5", Lane: "L-05", SHA: "c-T-5", Target: "b", Base: "b0", Patch: "p5"}})
	r.add("pj", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06"})
	r.add("pj", "T-6", event.StepSubmit, event.KindResult, submitOut{SHA: "s6"})
	r.add("pj", "T-6", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-06", Input: "s6"})
	r.add("pj", "T-6", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{
		Task: "T-6", SHA: "c-T-6", Target: "b", Patch: "p6"}})
	r.add("pj", "T-6", event.StepReview, event.KindIntent, JobData{Patch: "p6"})
	r.add("pj", "T-6", verdictStep(wire.MethodAccept, "a1"), event.KindResult, verdictResult{Job: "T-6/1/review", Patch: "p6"})

	p := r.proj("pj")
	r.a.mu.Lock()
	for task, want := range map[string]string{"T-1": stCandidate, "T-5": stCandidate, "T-6": stAccepted} {
		if got := p.folded.loop[task].state; got != want {
			r.a.mu.Unlock()
			t.Fatalf("precondition: %s is %s, want %s", task, got, want)
		}
	}
	p.tests["T-1"] = event.Key{Task: "T-1", Attempt: 1, Step: event.StepTest} // T-1's test runs
	p.restarting = r.a.opt.Now()                                              // a new binary: the restart is pending
	r.a.mu.Unlock()

	var calls atomic.Int32
	r.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Scratch: f, Agents: f, Typer: f, Kit: r.kit,
			Restart: func(context.Context) error { calls.Add(1); return nil }}, nil
	}
	r.a.opt.Binary = func() bool { return true }
	r.run()
	defer r.stop()

	// The publish still starts; no test does, while the restart is pending and T-1's test runs.
	r.waitFor("T-6 published", func() bool { return f.count(f.pubs, "T-6") == 1 })
	r.waitFor("T-6 merged", func() bool { return r.state("pj", "T-6") == stMerged })
	if n := f.count(f.tests, "T-1") + f.count(f.tests, "T-5"); n != 0 {
		t.Fatalf("a lane test started while the restart is pending (%d calls)", n)
	}
	if n := r.count("pj", "T-5", event.KindIntent, event.StepTest); n != 0 {
		t.Errorf("T-5's test intents %d, want none while the restart is pending", n)
	}

	// The running test holds the restart: a tick notices the new binary and waits.
	passes := func() int {
		r.a.mu.Lock()
		defer r.a.mu.Unlock()
		return r.a.passes
	}
	tick := func() {
		before := passes()
		r.a.mu.Lock()
		r.a.follows++
		r.a.mu.Unlock()
		r.wake()
		r.waitFor("a pass of the tick", func() bool { return passes() > before })
	}
	tick()
	// idle is false only because T-1's test runs: no gate or publish does (T-6 is merged).
	if calls.Load() != 0 {
		t.Fatalf("serve restarted %d times with a lane test running, want it to wait", calls.Load())
	}
	r.waitFor("the pending restart shown", func() bool {
		for _, s := range r.status("pj").Running {
			if s.Step == event.StepServeRestart {
				return true
			}
		}
		return false
	})
	if calls.Load() != 0 {
		t.Fatalf("restarted %d times while the lane test runs, want 0", calls.Load())
	}

	// The test ends: the next pass restarts on the new binary.
	r.a.mu.Lock()
	delete(p.tests, "T-1")
	r.a.mu.Unlock()
	tick()
	r.waitFor("the restart", func() bool { return calls.Load() == 1 })
}
