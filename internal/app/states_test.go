package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// rowWant is what one state's row (states.go) makes the loop's functions do, written out by hand from
// docs/state-model.md: each TestState<Name>Row<N> checks its row against the behaviour.
type rowWant struct {
	mt       mtask
	waits    bool              // no step of its own: pick is nil
	name     string            // stepName
	send     string            // unsendable ("": the lead may send it back)
	verdicts map[string]string // the integrator's verdict → the state it folds to (absent: no change)
	open     bool              // openJob: a verdict answers its job
	scratch  bool              // it holds the scratch: another task's gate waits (scratchBusy)
	landed   bool              // drive checks for an obor merge outside first
	ended    bool              // terminal: drop refuses it
	cause    string            // the needs-you's cause resume is asked about
	resume   resumeRule
}

func checkRow(t *testing.T, w rowWant) {
	t.Helper()
	mt := w.mt
	mt.task, mt.attempt = "T-1", 1
	row, ok := states[mt.state]
	if !ok {
		t.Fatalf("precondition: no row for state %q", mt.state)
	}
	if w.waits && (&App{}).pick(&proj{}, &mt) != nil || !w.waits && row.pick == nil {
		t.Errorf("%s: pick %v, want waits %v", mt.state, row.pick != nil, w.waits)
	}
	same(t, mt.stepName(), w.name, mt.state+": step name")
	same(t, mt.unsendable(), w.send, mt.state+": unsendable")
	for _, m := range []string{MethodAccept, MethodSendback, MethodReject, MethodResolved} {
		c := mt
		c.verdict(m, verdictResult{Patch: "p", Note: "why", SHA: "s"})
		want, takes := w.verdicts[m]
		if !takes {
			want = mt.state
		}
		same(t, c.state, want, mt.state+": state after "+m)
	}
	same(t, mt.openJob(), w.open, mt.state+": job open")
	p := &proj{folded: folded{loop: map[string]*mtask{"T-1": &mt}}}
	same(t, p.scratchBusy("T-2"), w.scratch, mt.state+": scratch busy for another task")
	same(t, p.scratchBusy("T-1"), false, mt.state+": scratch busy for itself")
	same(t, row.landed, w.landed, mt.state+": drive checks a merge outside")
	same(t, row.ended, w.ended, mt.state+": ended")
	if w.ended {
		p := &proj{merging: true, folded: folded{loop: map[string]*mtask{"T-1": &mt}}, ended: map[string]bool{}, held: map[string]string{}}
		_, err := (&App{}).dropDecide(p, &Call{Task: "T-1", Note: "why", CallID: "d"})
		if err == nil || !strings.Contains(err.Error(), "is "+mt.state+" already") {
			t.Errorf("%s: drop %v, want refused as ended", mt.state, err)
		}
	}
	same(t, mt.resumeRule(w.cause), w.resume, mt.state+": resume rule ("+w.cause+")")
}

// needsOf is a needs-you fact of cause.
func needsOf(cause string) Record {
	data, _ := json.Marshal(FactData{Merge: true, Cause: cause, Reason: cause + ": why"})
	return Record{Seq: 1, Kind: KindFact, Step: StepNeedsYou, Data: data}
}

func TestStatesOneRowPerState(t *testing.T) {
	want := []string{stSubmitted, stGreen, stAwaiting, stRed, stConflict, stResolving, stResolved, stFail, stAccepted,
		stMoved, stPublished, stMerged, stRejected, stDropped, stWaiting, stDispatched, stWorking, stNeedsYou}
	var got []string
	for st := range states {
		got = append(got, st)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("states %v, want the 18 of loop.go %v", got, want)
	}
}

func TestStateSubmittedRow18(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stSubmitted}, name: StepSubmit, send: "T-1 is submitted; retry when it settles",
		landed: true})
	checkRow(t, rowWant{mt: mtask{state: stSubmitted, input: "abc", gates: 1}, name: "gate.2",
		send: "T-1 is submitted; retry when it settles", landed: true})
}

func TestStateGreenRow19(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stGreen, reviews: 1}, name: "review.2", landed: true})
}

func TestStateAwaitingAcceptRow26(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stAwaiting, job: "review"}, name: "review", open: true, landed: true,
		verdicts: map[string]string{MethodAccept: stAccepted, MethodSendback: stWorking, MethodReject: stRejected}})
	// row 22: past K sendbacks the integrator's sendback goes to the owner
	checkRow(t, rowWant{mt: mtask{state: stAwaiting, job: "review", sendbacks: K}, name: "review", open: true,
		landed: true, verdicts: map[string]string{MethodAccept: stAccepted, MethodSendback: stFail, MethodReject: stRejected}})
}

func TestStateRedRow21(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stRed}, name: "a step (red)", send: "T-1 is red; retry when it settles",
		landed: true})
}

func TestStateConflictRow23(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stConflict}, name: "resolve", send: "T-1 is conflict; retry when it settles",
		scratch: true, landed: true})
}

func TestStateResolvingRow30(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stResolving, job: "resolve.2"}, name: "resolve.2", open: true, scratch: true,
		landed: true, verdicts: map[string]string{MethodResolved: stResolved, MethodReject: stFail}})
}

func TestStateResolvedRow18(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stResolved, gates: 1}, name: "gate.2",
		send: "T-1 is resolved; retry when it settles", scratch: true})
}

func TestStateFailRow22(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stFail}, name: "a step (fail)", send: "T-1 is fail; retry when it settles",
		landed: true})
}

func TestStateAcceptedRow33(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stAccepted}, name: "publish", send: "T-1 is accepted; retry when it settles"})
}

func TestStateMovedRow32(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stMoved, input: "abc", gates: 2}, name: "gate.3",
		send: "T-1 is moved; retry when it settles"})
}

func TestStatePublishedRow34(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stPublished}, name: "a step (published)",
		send: "T-1 is published; retry when it settles"})
}

func TestStateMergedRow38(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stMerged, after: len(afterSteps)}, waits: true, name: "a step (merged)",
		send: "T-1 is merged", ended: true, cause: "after_merge failed", resume: resumeRule{noReprompt: true}})
}

func TestStateRejectedRow29(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stRejected, needs: needsOf(causeRejected)}, waits: true,
		name: "a step (rejected)", send: "T-1 is rejected", ended: true, cause: causeRejected,
		resume: resumeRule{closed: true}})
	// a failed unpin's needs-you is retried
	checkRow(t, rowWant{mt: mtask{state: stRejected, needs: needsOf("unpin failed")}, waits: true,
		name: "a step (rejected)", send: "T-1 is rejected", ended: true, cause: "unpin failed"})
}

func TestStateDroppedRow44(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stDropped}, waits: true, name: "a step (dropped)", send: "T-1 is dropped",
		ended: true})
}

func TestStateWaitingRow2a(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stWaiting}, name: "the dispatch", send: "T-1 is waiting",
		cause: causeAfter, resume: resumeRule{to: ResumeRetry, only: ResumeRetry, retry: true}})
	// pick: nothing while a task it waits for is open; a step once all are merged (one with no loop record: the step
	// asks the target for its merge evidence), or one is dropped
	for _, c := range []struct {
		name string
		deps map[string]string // task → its state ("": no loop record)
		step bool
	}{
		{"T-2 open", map[string]string{"T-2": stWorking}, false},
		{"T-2 not in the loop", map[string]string{"T-2": "", "T-3": stMerged}, true},
		{"T-2 not in the loop, T-3 open", map[string]string{"T-2": "", "T-3": stWorking}, false},
		{"T-2 merged, T-3 open", map[string]string{"T-2": stMerged, "T-3": stAwaiting}, false},
		{"all merged", map[string]string{"T-2": stMerged, "T-3": stMerged}, true},
		{"T-3 dropped, T-2 open", map[string]string{"T-2": stWorking, "T-3": stDropped}, true}, // needs-you
		{"T-2 rejected", map[string]string{"T-2": stRejected, "T-3": stWorking}, true},
	} {
		mt := mtask{task: "T-1", attempt: 1, state: stWaiting, disp: dispatchData{After: []string{"T-2", "T-3"}}}
		p := &proj{dispatching: true, folded: folded{loop: map[string]*mtask{"T-1": &mt}}}
		for task, st := range c.deps {
			if st != "" {
				p.folded.loop[task] = &mtask{task: task, attempt: 1, state: st}
			}
		}
		if got := (&App{}).pick(p, &mt) != nil; got != c.step {
			t.Errorf("%s: a step %v, want %v", c.name, got, c.step)
		}
		p.dispatching = false
		if (&App{}).pick(p, &mt) != nil {
			t.Errorf("%s: a step without the dispatch adapters", c.name)
		}
	}
	// drop: a waiting task is open, obor drop <T> takes it
	mt := mtask{task: "T-1", attempt: 1, state: stWaiting}
	p := &proj{merging: true, folded: folded{loop: map[string]*mtask{"T-1": &mt}, calls: map[string]string{}},
		ended: map[string]bool{}, held: map[string]string{}, log: &Log{}}
	if got, err := (&App{}).dropDecide(p, &Call{Task: "T-1", Note: "why", CallID: "d"}); err != nil || got.task != "T-1" ||
		!p.ended["T-1"] {
		t.Errorf("drop of a waiting task: %+v, %v; want T-1 marked ended", got, err)
	}
	// dispatchable: waiting, or a needs-you its wait left, takes a new request; its attempt is the wait's own
	for _, c := range []struct {
		mt      mtask
		ok      bool
		attempt int
	}{
		{mtask{state: stWaiting, attempt: 3}, true, 3},
		{mtask{state: stNeedsYou, left: stWaiting, attempt: 3}, true, 3},
		{mtask{state: stMerged, attempt: 3}, true, 4},
		{mtask{state: stWorking, attempt: 3}, false, 4},
	} {
		if c.mt.dispatchable() != c.ok || c.mt.nextAttempt() != c.attempt {
			t.Errorf("%s: dispatchable %v, attempt %d; want %v, %d", c.mt.kept(), c.mt.dispatchable(), c.mt.nextAttempt(),
				c.ok, c.attempt)
		}
	}
}

func TestStateDispatchedRow3(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stDispatched}, name: "the dispatch", send: "T-1 is dispatched",
		cause: "lane", resume: resumeRule{to: ResumeRetry, only: ResumeRetry, retry: true}})
}

func TestStateWorkingRow24(t *testing.T) {
	checkRow(t, rowWant{mt: mtask{state: stWorking}, name: "the fix", send: "T-1 is working", landed: true,
		cause: causeUndelivered, resume: resumeRule{to: ResumeRetry, retry: true}})
	// a dispatched attempt's work job: obor resume's default by cause
	checkRow(t, rowWant{mt: mtask{state: stWorking, work: "work"}, name: "the fix", send: "T-1 is working",
		landed: true, cause: causeUndelivered})
}

func TestStateNeedsYouRow40(t *testing.T) {
	// the state it left is kept: a resolve job held stays open, in the scratch
	checkRow(t, rowWant{mt: mtask{state: stNeedsYou, left: stResolving, job: "resolve", needs: needsOf(causeHeld),
		lane: "O-01", agent: "o-01"}, waits: true, name: "a step (needs-you)", open: true, scratch: true, landed: true,
		cause: causeHeld})
	checkRow(t, rowWant{mt: mtask{state: stNeedsYou, left: stDispatched, needs: needsOf("lane"), lane: "O-01",
		agent: "o-01"}, waits: true, name: "a step (needs-you)", landed: true,
		cause: "lane", resume: resumeRule{to: ResumeRetry, only: ResumeRetry, retry: true}})
	// T-619: a needs-you whose task has no lane (a waiting one) has no worker to send it to: resume or drop it
	checkRow(t, rowWant{mt: mtask{state: stNeedsYou, left: stWaiting, needs: needsOf(causeAfter)}, waits: true,
		name: "a step (needs-you)", send: "T-1 has no lane yet: obor resume or obor drop it", landed: true,
		cause: causeAfter, resume: resumeRule{to: ResumeRetry, only: ResumeRetry, retry: true}})
}
