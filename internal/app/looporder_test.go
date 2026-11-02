package app

import (
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"testing"
)

// The choices of one merge-loop task over p's loop map never follow Go's map order (T-615): a lane's task is its newest
// dispatch's, the other choices are by task name. Each test has two tasks the choice could take and asks it many
// times: map order would pick the other one in about half of them.

const orderTries = 64

// twoInLane is a project whose loop holds T-2 and T-1, both dispatched to lane L1 by agent w1, their work jobs out;
// T-2's dispatch is the newer (seq 7 after T-1's 3): name order and dispatch order disagree.
func twoInLane() *proj {
	mk := func(task string, seq int64) *mtask {
		return &mtask{task: task, lane: "L1", agent: "w1", attempt: 1, state: stDispatched, attemptVars: attemptVars{work: event.StepWork}, dispSeq: seq}
	}
	return &proj{merging: true, log: &event.Log{}, ended: map[string]bool{}, held: map[string]string{},
		folded: folded{loop: map[string]*mtask{"T-2": mk("T-2", 7), "T-1": mk("T-1", 3)}, calls: map[string]string{}}}
}

func TestLoopTasksSortedByName(t *testing.T) {
	t.Parallel()
	p := twoInLane()
	p.folded.loop["T-10"] = &mtask{task: "T-10"}
	for range orderTries {
		if got := p.loopTasks(); len(got) != 3 || got[0] != "T-1" || got[1] != "T-10" || got[2] != "T-2" {
			t.Fatalf("loopTasks = %v; want [T-1 T-10 T-2]", got)
		}
	}
}

func TestWorkerJobNextTwoJobsInLaneTakesFirstTaskByName(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		if _, ok := p.folded.loop["T-2"].workerJob(); !ok {
			t.Fatal("precondition: T-2 has a worker job open")
		}
		mt, k, ok, err := (&App{}).callerJob(p, wire.Call{Role: "worker", Lane: "L1", Agent: "w1"})
		if err != nil || !ok || mt.task != "T-1" || k != (event.Key{Task: "T-1", Attempt: 1, Step: event.StepWork}) {
			t.Fatalf("callerJob = %s %v %v %v; want T-1's work job", mt.task, k, ok, err)
		}
	}
}

func TestIntegratorJobNextEqualSeqsTakesFirstTaskByName(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		p.integrator = "int"
		for _, mt := range p.folded.loop {
			mt.state, mt.job = stAwaiting, event.StepReview
		}
		if _, ok := p.folded.loop["T-2"].integratorJob(); !ok {
			t.Fatal("precondition: T-2 has an integrator job open")
		}
		if _, ok := p.log.IntentOf(event.Key{Task: "T-2", Attempt: 1, Step: event.StepReview}); ok {
			t.Fatal("precondition: no intent recorded (equal seqs)")
		}
		mt, k, ok, err := (&App{}).callerJob(p, wire.Call{Role: "integrator", Agent: "int"})
		if err != nil || !ok || mt.task != "T-1" || k != (event.Key{Task: "T-1", Attempt: 1, Step: event.StepReview}) {
			t.Fatalf("callerJob = %s %v %v %v; want T-1's review job", mt.task, k, ok, err)
		}
	}
}

func TestDropByLaneTwoOpenTakesNewestDispatch(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		if p.folded.loop["T-2"].dispSeq <= p.folded.loop["T-1"].dispSeq {
			t.Fatal("precondition: T-2's dispatch is the newer, T-1 the first by name")
		}
		got, err := (&App{}).dropDecide(p, &wire.Call{Lane: "L1", Note: "why", CallID: "d"})
		if err != nil || got.task != "T-2" || !p.ended["T-2"] || p.ended["T-1"] {
			t.Fatalf("dropDecide = %q %v (ended %v); want T-2 only", got.task, err, p.ended)
		}
	}
}

func TestHeldLaneOfTwoDispatchesIsNewestDispatch(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		if !p.folded.loop["T-1"].dispatching() || p.folded.loop["T-2"].dispSeq <= p.folded.loop["T-1"].dispSeq {
			t.Fatal("precondition: both dispatches hold L1, T-2's the newer, T-1 the first by name")
		}
		mt, held := (&App{}).held(p, "T-3")
		if mt.task != "" || len(held) != 1 || held["L1"] != "T-2" {
			t.Fatalf("held = %q %v; want no record of T-3 and L1 held by T-2", mt.task, held)
		}
	}
}
