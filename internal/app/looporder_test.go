package app

import "testing"

// The choices of one merge-loop task over p's loop map are by task name, never Go's map order (T-615). Each test has
// two tasks the choice could take and asks it many times: map order would pick T-2 in about half of them.

const orderTries = 64

// twoInLane is a project whose loop holds T-2 and T-1, both dispatched to lane L1 by agent w1, their work jobs out.
func twoInLane() *proj {
	mk := func(task string) *mtask {
		return &mtask{task: task, lane: "L1", agent: "w1", attempt: 1, state: stDispatched, work: StepWork}
	}
	return &proj{merging: true, log: &Log{}, ended: map[string]bool{}, held: map[string]string{},
		folded: folded{loop: map[string]*mtask{"T-2": mk("T-2"), "T-1": mk("T-1")}, calls: map[string]string{}}}
}

func TestLoopTasksSortedByName(t *testing.T) {
	p := twoInLane()
	p.folded.loop["T-10"] = &mtask{task: "T-10"}
	for range orderTries {
		if got := p.loopTasks(); len(got) != 3 || got[0] != "T-1" || got[1] != "T-10" || got[2] != "T-2" {
			t.Fatalf("loopTasks = %v; want [T-1 T-10 T-2]", got)
		}
	}
}

func TestWorkerJobNextTwoJobsInLaneTakesFirstTaskByName(t *testing.T) {
	for range orderTries {
		p := twoInLane()
		if _, ok := p.folded.loop["T-2"].workerJob(); !ok {
			t.Fatal("precondition: T-2 has a worker job open")
		}
		mt, k, ok, err := (&App{}).callerJob(p, Call{Role: "worker", Lane: "L1", Agent: "w1"})
		if err != nil || !ok || mt.task != "T-1" || k != (Key{"T-1", 1, StepWork}) {
			t.Fatalf("callerJob = %s %v %v %v; want T-1's work job", mt.task, k, ok, err)
		}
	}
}

func TestIntegratorJobNextEqualSeqsTakesFirstTaskByName(t *testing.T) {
	for range orderTries {
		p := twoInLane()
		p.integrator = "int"
		for _, mt := range p.folded.loop {
			mt.state, mt.job = stAwaiting, StepReview
		}
		if _, ok := p.folded.loop["T-2"].integratorJob(); !ok {
			t.Fatal("precondition: T-2 has an integrator job open")
		}
		if _, ok := p.log.IntentOf(Key{"T-2", 1, StepReview}); ok {
			t.Fatal("precondition: no intent recorded (equal seqs)")
		}
		mt, k, ok, err := (&App{}).callerJob(p, Call{Role: "integrator", Agent: "int"})
		if err != nil || !ok || mt.task != "T-1" || k != (Key{"T-1", 1, StepReview}) {
			t.Fatalf("callerJob = %s %v %v %v; want T-1's review job", mt.task, k, ok, err)
		}
	}
}

func TestDropByLaneTwoOpenTakesFirstTaskByName(t *testing.T) {
	for range orderTries {
		p := twoInLane()
		got, err := (&App{}).dropDecide(p, &Call{Lane: "L1", Note: "why", CallID: "d"})
		if err != nil || got.task != "T-1" || !p.ended["T-1"] || p.ended["T-2"] {
			t.Fatalf("dropDecide = %q %v (ended %v); want T-1 only", got.task, err, p.ended)
		}
	}
}

func TestHeldLaneOfTwoDispatchesIsFirstTaskByName(t *testing.T) {
	for range orderTries {
		p := twoInLane()
		if !p.folded.loop["T-2"].dispatching() {
			t.Fatal("precondition: T-2's dispatch holds its lane")
		}
		mt, held := (&App{}).held(p, "T-3")
		if mt.task != "" || len(held) != 1 || held["L1"] != "T-1" {
			t.Fatalf("held = %q %v; want no record of T-3 and L1 held by T-1", mt.task, held)
		}
	}
}
