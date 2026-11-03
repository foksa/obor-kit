package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"sync/atomic"
	"testing"
	"time"
)

// RV-42-verify-2 #1 (T-397): a dropped fact is terminal for the task whatever attempt it names; any record of the task
// after it is ignored, and only a new dispatch reopens it.

// addAt is add at attempt n.
func (r *concRig) addAt(name, task string, n int, step, kind string, d any) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: task, Attempt: n, Step: step, Kind: kind, Data: raw}, a.opt.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

// needsYouT7 leaves pq's T-7 needs-you at attempt 1: a submission of it records attempt 2.
func needsYouT7(r *concRig) {
	r.add("pq", "T-7", event.StepSubmitted, event.KindFact, submitData{Lane: "L-07", Agent: "pq-l-07"})
	r.add("pq", "T-7", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-07", Merge: true, Cause: causeGate, Reason: "owner"})
}

// raceDrop is the reviewer's probe (TestVerifyRV42DropAgainstDecidedSubmission): a submission of T-7 passes its guards
// and pauses before its append; drop decides against attempt 1 and pauses before its; the submission records
// submitted at attempt 2, then drop records dropped at attempt 1. The injected clock is only the barrier before each
// append.
func raceDrop(t *testing.T, r *concRig) (sub, drop callAnswer) {
	p := r.proj("pq")
	now := r.app("pq").opt.Now()
	lane := port.Lane{Name: "L-07", Task: "T-7", Agent: "pq-l-07"}
	r.app("pq").mu.Lock()
	p.lanes = []port.Lane{lane}
	r.app("pq").mu.Unlock()
	r.app("pq").lanes = &fakeLanes{lanes: []port.Lane{lane}}
	subAt, dropAt, subGo, dropGo := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r.app("pq").opt.Now = func() time.Time {
		switch calls.Add(1) {
		case 1:
			close(subAt)
			<-subGo
		case 2:
			close(dropAt)
			<-dropGo
		}
		return now
	}
	defer func() { r.app("pq").opt.Now = func() time.Time { return now } }()
	submitted, dropped := make(chan callAnswer, 1), make(chan callAnswer, 1)
	go func() {
		reply, err := r.app("pq").taskDone(wire.Call{Root: p.root, Task: "T-7", Agent: "pq-l-07", Role: "worker", Lane: "L-07",
			CallID: "done", Report: "Summary: fixed\nSelf-check: passed\nSkipped: none\nOpen questions: none"})
		submitted <- callAnswer{reply, err}
	}()
	wait := func(c chan struct{}, what string) {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not reach its append", what)
		}
	}
	wait(subAt, "the submission")
	go func() {
		reply, err := r.dropCall("pq", "T-7", "drop")
		dropped <- callAnswer{reply, err}
	}()
	wait(dropAt, "the drop")
	close(subGo)
	select {
	case sub = <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the submission did not record")
	}
	close(dropGo)
	select {
	case drop = <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("the drop did not record")
	}
	return sub, drop
}

// Dropped ends the task: each case's first action after its setup is the named entry; then T-7 is in state want at
// attempt (live and after a replay of the log) and no drop mark is left.
func TestDroppedEndsTheTask(t *testing.T) {
	t.Parallel()
	dropOK := func(t *testing.T, a callAnswer) {
		if a.err != nil || !isDropped(a.reply, "T-7") {
			t.Fatalf("drop: %q, %v; want DROPPED T-7", a.reply, a.err)
		}
	}
	// dropAfterNext records the race's outcome by hand: submitted at attempt 2, then dropped at attempt 1.
	dropAfterNext := func(r *concRig) {
		r.addAt("pq", "T-7", 2, event.StepSubmitted, event.KindFact, submitData{Lane: "L-07", Agent: "pq-l-07"})
		if s := r.state("pq", "T-7"); s != stSubmitted {
			r.t.Fatalf("precondition: T-7 is %s after its next attempt's submission, want submitted", s)
		}
		r.addAt("pq", "T-7", 1, event.StepDropped, event.KindFact, event.FactData{Lane: "L-07", Merge: true, Reason: "why"})
	}
	for _, c := range []struct {
		name    string
		act     func(t *testing.T, r *concRig)
		want    string
		attempt int
	}{
		{"task_done decided before the drop", func(t *testing.T, r *concRig) {
			sub, drop := raceDrop(t, r)
			dropOK(t, drop)
			if sub.err != nil || sub.reply != taskDoneReply {
				t.Errorf("task_done: %q, %v; want submitted (recorded before the drop)", sub.reply, sub.err)
			}
			if n := r.count("pq", "T-7", event.KindFact, event.StepSubmitted); n != 2 {
				t.Errorf("precondition: %d submitted facts, want the race's second", n)
			}
		}, stDropped, 2},
		{"dropped at N after submitted at N+1", func(t *testing.T, r *concRig) { dropAfterNext(r) }, stDropped, 2},
		{"late gate result after dropped", func(t *testing.T, r *concRig) {
			dropAfterNext(r)
			r.addAt("pq", "T-7", 2, event.StepSubmit, event.KindResult, submitOut{SHA: "s7"})
			r.addAt("pq", "T-7", 2, event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-7",
				SHA: "c7", Target: "b", Patch: "p"}})
		}, stDropped, 2},
		{"late verdict result after dropped", func(t *testing.T, r *concRig) {
			dropAfterNext(r)
			r.addAt("pq", "T-7", 2, verdictStep(wire.MethodAccept, "v9"), event.KindResult, verdictResult{Job: "T-7/2/review",
				Reply: "ACCEPTED", Patch: "p"})
		}, stDropped, 2},
		{"dispatch after dropped", func(t *testing.T, r *concRig) {
			dropAfterNext(r)
			r.addAt("pq", "T-7", 3, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-07", Agent: "pq-l-07",
				Target: "target", Branch: "task/T-7"})
		}, stDispatched, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			needsYouT7(r)
			if s := r.state("pq", "T-7"); s != stNeedsYou {
				t.Fatalf("precondition: T-7 is %s, want needs-you", s)
			}
			c.act(t, r)
			p := r.proj("pq")
			r.app("pq").mu.Lock()
			live := *p.folded.loop["T-7"]
			ended := p.ended["T-7"]
			r.app("pq").mu.Unlock()
			if live.state != c.want || live.attempt != c.attempt {
				t.Errorf("live: %s at attempt %d; want %s at %d", live.state, live.attempt, c.want, c.attempt)
			}
			if ended {
				t.Error("the drop mark is left set")
			}
			r.app("pq").closeLog() // the first serve ends: the replay is the next one
			b := New(Options{Root: r.roots["pq"], Now: r.app("pq").opt.Now}, &fakePanes{}, &fakeLanes{})
			b.loadProject()
			if mt := b.p.folded.loop["T-7"]; mt == nil || mt.state != c.want || mt.attempt != c.attempt {
				t.Errorf("replay: %+v; want %s at attempt %d", mt, c.want, c.attempt)
			}
		})
	}
}
