package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// rowWant is what one state's row (states.go) makes the loop's functions do, written out by hand from
// docs/state-model.md: each TestState<Name>Row<N> checks its row against the behaviour.
type rowWant struct {
	mt       mtask
	waits    bool              // no step of its own: pick is nil
	name     string            // stepName
	verbs    verb              // the task verbs it takes (mtask.may)
	refuse   string            // obor resume's refusal by its state ("": it takes a resume)
	verdicts map[string]string // the integrator's verdict → the state it folds to (absent: no change)
	open     bool              // openJob: a verdict answers its job
	scratch  bool              // it holds the scratch: another task's gate waits (scratchHolder)
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
	for _, v := range allVerbs {
		same(t, mt.may(v), w.verbs&v != 0, fmt.Sprintf("%s: takes %s", mt.state, verbName[v]))
	}
	if _, _, err := (&proj{}).resumeFits(mt); w.refuse != "" && (err == nil || err.Error() != "resume: "+w.refuse) {
		t.Errorf("%s: resume %v, want refused %q", mt.state, err, w.refuse)
	}
	for _, m := range []string{wire.MethodAccept, wire.MethodSendback, wire.MethodReject, wire.MethodResolved} {
		c := mt
		c.verdict(m, verdictResult{Patch: "p", Note: "why", SHA: "s"})
		want, takes := w.verdicts[m]
		if !takes {
			want = mt.shown()
		}
		same(t, c.shown(), want, mt.state+": state after "+m)
	}
	same(t, mt.openJob(), w.open, mt.state+": job open")
	p := &proj{folded: folded{loop: map[string]*mtask{"T-1": &mt}}}
	same(t, busy(p, "T-2"), w.scratch, mt.state+": scratch busy for another task")
	same(t, busy(p, "T-1"), false, mt.state+": scratch busy for itself")
	same(t, mt.landed(), w.landed, mt.state+": drive checks a merge outside")
	same(t, row.ended, w.ended, mt.state+": ended")
	if w.verbs&vDrop == 0 {
		p := &proj{merging: true, folded: folded{loop: map[string]*mtask{"T-1": &mt}}, ended: map[string]bool{}, held: map[string]string{}}
		_, _, err := (&App{}).dropDecide(p, wire.DropReq{Envelope: wire.Envelope{CallID: "d"}, Task: "T-1", Why: "why"})
		if err == nil || !strings.Contains(err.Error(), "is "+mt.state+" already") {
			t.Errorf("%s: drop %v, want refused as ended", mt.state, err)
		}
	}
	same(t, mt.resumeRule(w.cause), w.resume, mt.state+": resume rule ("+w.cause+")")
}

func TestStatesOneRowPerState(t *testing.T) {
	t.Parallel()
	want := []string{stSubmitted, stCandidate, stGreen, stAwaiting, stRed, stConflict, stResolving, stResolved, stAccepted,
		stMoved, stPublished, stPending, stMerged, stRejected, stDropped, stWaiting, stDispatched, stWorking, stDriving,
		stDropping}
	var got []string
	for st := range states {
		got = append(got, st)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("states %v, want the 20 of loop.go %v", got, want)
	}
}

func TestStateSubmittedRow18(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stSubmitted}, name: event.StepSubmit, refuse: "T-1 is submitted; retry when it settles",
		landed: true})
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stSubmitted, attemptVars: attemptVars{input: "abc", gates: 1}}, name: "gate.2",
		refuse: "T-1 is submitted; retry when it settles", landed: true})
}

// A candidate (the slot merged and compiled it) runs its lane test next, test.N; only a drop takes it while it
// waits, and a merge outside may have landed it.
func TestStateCandidateRow18b(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stCandidate}, name: "test", refuse: "T-1 is candidate; retry when it settles",
		landed: true})
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stCandidate, attemptVars: attemptVars{tests: 1}}, name: "test.2",
		refuse: "T-1 is candidate; retry when it settles", landed: true})
}

func TestStateGreenRow19(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stGreen, attemptVars: attemptVars{reviews: 1}}, name: "review.2", landed: true})
}

func TestStateAwaitingAcceptRow26(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stAwaiting, attemptVars: attemptVars{job: "review"}}, name: "review", open: true, landed: true,
		verdicts: map[string]string{wire.MethodAccept: stAccepted, wire.MethodSendback: stWorking, wire.MethodReject: stRejected}})
	// row 22: past K sendbacks the integrator's sendback goes to the owner
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stAwaiting, attemptVars: attemptVars{job: "review"}, sendbacks: K}, name: "review", open: true,
		landed: true, verdicts: map[string]string{wire.MethodAccept: stAccepted, wire.MethodSendback: "fail", wire.MethodReject: stRejected}})
}

// The owner's drive waits for obor land (its only step, the lead's line when it came back, needs a line); the
// lead cannot send it back (no worker), no verdict moves it, drop takes it. The integrator's sendback of a drive goes
// back to driving whatever the count: never the owner's needs-you past K, never a worker.
func TestStateDrivingRowT751(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume | vDrop | vLand, mt: mtask{state: stDriving, drive: true}, waits: true, name: "the drive"})
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stAwaiting, drive: true, attemptVars: attemptVars{job: "review"}, sendbacks: K},
		name: "review", open: true, landed: true,
		verdicts: map[string]string{wire.MethodAccept: stAccepted, wire.MethodSendback: stDriving, wire.MethodReject: stRejected}})
}

func TestStateRedRow21(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stRed}, name: "a step (red)", refuse: "T-1 is red; retry when it settles",
		landed: true})
}

func TestStateConflictRow23(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stConflict}, name: "resolve", refuse: "T-1 is conflict; retry when it settles",
		scratch: true, landed: true})
}

func TestStateResolvingRow30(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stResolving, attemptVars: attemptVars{job: "resolve.2"}}, name: "resolve.2", open: true, scratch: true,
		landed: true, verdicts: map[string]string{wire.MethodResolved: stResolved, wire.MethodReject: "fail"}})
}

func TestStateResolvedRow18(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stResolved, attemptVars: attemptVars{gates: 1}}, name: "gate.2",
		refuse: "T-1 is resolved; retry when it settles", scratch: true})
}

// TestBlockDueRow22: a due block (here a gate failure on a resolved merge) takes only a drop, records its needs-you
// next, is checked for a merge outside first, and frees the scratch its phase held.
func TestBlockDueRow22(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stResolved, needs: block{cause: causeGate, why: "gate: x"}},
		name: "a step (fail)", refuse: "T-1 is fail; retry when it settles", landed: true})
}

func TestStateAcceptedRow33(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stAccepted}, name: "publish", refuse: "T-1 is accepted; retry when it settles"})
}

func TestStateMovedRow32(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stMoved, attemptVars: attemptVars{input: "abc", gates: 2}}, name: "gate.3",
		refuse: "T-1 is moved; retry when it settles"})
}

func TestStatePublishedRow34(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stPublished}, name: "a step (published)",
		refuse: "T-1 is published; retry when it settles"})
}

// A pending publish has no step of its own; drive's Landed rule ends it (landed).
func TestStatePendingRow34(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vDrop, mt: mtask{state: stPending}, waits: true, name: "a step (pending)",
		refuse: "T-1 is pending", landed: true})
}

func TestStateMergedRow38(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume, mt: mtask{state: stMerged, after: len(afterSteps)}, waits: true, name: "a step (merged)",
		ended: true, cause: "after_merge failed", resume: resumeRule{noReprompt: true}})
}

func TestStateRejectedRow29(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stRejected, needs: heldOf(causeRejected)}, waits: true,
		name: "a step (rejected)", ended: true, cause: causeRejected,
		resume: resumeRule{closed: true}})
	// a failed unpin's needs-you is retried
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stRejected, needs: heldOf("unpin failed")}, waits: true,
		name: "a step (rejected)", ended: true, cause: "unpin failed"})
}

func TestStateDroppedRow44(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: 0, mt: mtask{state: stDropped}, waits: true, name: "a step (dropped)", refuse: "T-1 is dropped",
		ended: true})
}

// A dropping task runs the drop's steps (its worker's stop, lane.free, then dropped): no task_done, blocked or
// not; a drop again (--force, --discard) and obor resume (retry) take it; a needs-you of a step waits for those.
func TestStateDroppingRowT852(t *testing.T) {
	t.Parallel()
	retry := resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stDropping}, name: "the drop",
		refuse: "T-1 is dropping with nothing to resume (no question, no needs-you)", resume: retry})
	for _, cause := range []string{causeStop, causeLane} {
		checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stDropping, needs: heldOf(cause)}, waits: true,
			name: "a step (needs-you)", landed: true, cause: cause, resume: retry})
	}
}

func TestStateWaitingRow2a(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: vResume | vDrop, mt: mtask{state: stWaiting}, name: "the dispatch",
		cause: causeAfter, resume: resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}})
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
		ended: map[string]bool{}, held: map[string]string{}, log: &event.Log{}}
	if got, _, err := (&App{}).dropDecide(p, wire.DropReq{Envelope: wire.Envelope{CallID: "d"}, Task: "T-1",
		Why: "why"}); err != nil || got.task != "T-1" ||
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
		{mtask{state: stWaiting, needs: heldOf(causeAfter), attempt: 3}, true, 3},
		{mtask{state: stMerged, attempt: 3}, true, 4},
		{mtask{state: stWorking, attempt: 3}, false, 4},
	} {
		if c.mt.dispatchable() != c.ok || c.mt.nextAttempt() != c.attempt {
			t.Errorf("%s: dispatchable %v, attempt %d; want %v, %d", c.mt.shown(), c.mt.dispatchable(), c.mt.nextAttempt(),
				c.ok, c.attempt)
		}
	}
}

func TestStateDispatchedRow3(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: workVerbs, mt: mtask{state: stDispatched}, name: "the dispatch",
		cause: "lane", resume: resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}})
}

func TestStateWorkingRow24(t *testing.T) {
	t.Parallel()
	checkRow(t, rowWant{verbs: workVerbs, mt: mtask{state: stWorking}, name: "the fix", landed: true,
		cause: causeUndelivered, resume: resumeRule{to: wire.ResumeRetry, retry: true}})
	// a dispatched attempt's work job: obor resume's default by cause
	checkRow(t, rowWant{verbs: workVerbs, mt: mtask{state: stWorking, attemptVars: attemptVars{work: "work"}}, name: "the fix",
		landed: true, cause: causeUndelivered})
}

// TestBlockHeldRow40: a held block keeps its phase: the owner decides (resume, drop, a task_done), nothing runs, and
// what the phase holds stays held.
func TestBlockHeldRow40(t *testing.T) {
	t.Parallel()
	// a resolve job held stays open, in the scratch
	checkRow(t, rowWant{verbs: vResume | vDrop | vSubmit, mt: mtask{state: stResolving, attemptVars: attemptVars{job: "resolve"}, needs: heldOf(event.CauseHeld),
		lane: "L-01", agent: "l-01"}, waits: true, name: "a step (needs-you)", open: true, scratch: true, landed: true,
		cause: event.CauseHeld})
	// a resolve rejected (closed): its job ended, the scratch is free
	checkRow(t, rowWant{verbs: vResume | vDrop | vSubmit, mt: mtask{state: stResolving, attemptVars: attemptVars{job: "resolve"},
		needs: closedOf(causeResolveRejected), lane: "L-01", agent: "l-01"}, waits: true, name: "a step (needs-you)", landed: true,
		cause: causeResolveRejected})
	checkRow(t, rowWant{verbs: vResume | vDrop | vSubmit, mt: mtask{state: stDispatched, needs: heldOf("lane"), lane: "L-01",
		agent: "l-01"}, waits: true, name: "a step (needs-you)", landed: true,
		cause: "lane", resume: resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}})
	// a needs-you whose task has no lane (a waiting one): resume (retry) or drop it
	checkRow(t, rowWant{verbs: vResume | vDrop | vSubmit, mt: mtask{state: stWaiting, needs: heldOf(causeAfter)}, waits: true,
		name: "a step (needs-you)", landed: true,
		cause: causeAfter, resume: resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}})
}

// statePhase is each state's phase with no live overlay: the states table's phase column, spelled out so a
// changed row is a changed test.
var statePhase = map[string]string{
	stWaiting: wire.PhaseWaiting, stDispatched: wire.PhaseDispatching, stWorking: wire.PhaseWorking,
	stDriving: wire.PhaseDriving, stSubmitted: wire.PhaseSubmitted, stCandidate: wire.PhaseTesting, stRed: wire.PhaseGating,
	stResolved: wire.PhaseGating, stMoved: wire.PhaseGating, stGreen: wire.PhaseReview, stConflict: wire.PhaseReview,
	stAwaiting: wire.PhaseReviewing, stResolving: wire.PhaseReviewing, stAccepted: wire.PhasePublish,
	stPublished: wire.PhasePublish, stPending: wire.PhasePublish, stMerged: wire.PhaseMerged, stRejected: wire.PhaseRejected, stDropped: wire.PhaseDropped,
	stDropping: wire.PhaseDropping,
}

// Every state of the table, with and without each live overlay, has exactly one phase: a needs-you in
// force wins (held only for the integrator's hold verdict, C7), then the gate or publish running or pending, else the
// state's row. A green task is in review. The merge loop's needs-you is a block on any
// state (due or held), and wins over every overlay but on an ended state (merged, rejected), whose row it leaves.
func TestPhaseOfEveryStateAndOverlay(t *testing.T) {
	t.Parallel()
	if len(statePhase) != len(states) {
		t.Fatalf("precondition: %d phases for %d states", len(statePhase), len(states))
	}
	for st, row := range states {
		want, ok := statePhase[st]
		if !ok || row.phase != want {
			t.Errorf("%s: row phase %q, want %q", st, row.phase, want)
		}
		mt := &mtask{task: "T-1", lane: "L-01", state: st}
		due := &mtask{task: "T-1", lane: "L-01", state: st, needs: block{cause: causeGate, why: "gate: x"}}
		held := &mtask{task: "T-1", lane: "L-01", state: st, needs: heldOf(causeStalled)}
		for _, c := range []struct {
			name string
			o    overlays
			want string
		}{
			{"no overlay", overlays{}, want},
			{"a watchdog's needs-you", overlays{blocked: true}, wire.PhaseNeedsYou},
			{"its gate running", overlays{step: wire.PhaseGating}, wire.PhaseGating},
			{"its gate pending", overlays{step: wire.PhaseGatePending}, wire.PhaseGatePending},
			{"its lane test running", overlays{step: wire.PhaseTesting}, wire.PhaseTesting},
			{"its lane test pending", overlays{step: wire.PhaseTestPending}, wire.PhaseTestPending},
			{"its publish running", overlays{step: wire.PhaseMerging}, wire.PhaseMerging},
			{"its publish pending", overlays{step: wire.PhasePublishPending}, wire.PhasePublishPending},
			{"a needs-you and a step", overlays{blocked: true, step: wire.PhaseGating}, wire.PhaseNeedsYou},
		} {
			if got := phase(mt, c.o); got != c.want {
				t.Errorf("%s, %s: phase %q, want %q", st, c.name, got, c.want)
			}
			blocked := wire.PhaseNeedsYou // the loop's needs-you wins over every overlay
			if row.ended {
				blocked = c.want
			}
			if got := phase(held, c.o); got != blocked {
				t.Errorf("%s held, %s: phase %q, want %q", st, c.name, got, blocked)
			}
			if got := phase(due, c.o); !row.ended && got != wire.PhaseNeedsYou {
				t.Errorf("%s due, %s: phase %q, want needs-you", st, c.name, got)
			}
		}
	}
	// held is the integrator's hold verdict alone (C7): another cause of the loop's needs-you is needs-you.
	held := &mtask{task: "T-1", state: stAwaiting, needs: heldOf(event.CauseHeld)}
	if got := phase(held, overlays{step: wire.PhaseGating}); got != wire.PhaseHeld {
		t.Errorf("the integrator's hold: phase %q, want held", got)
	}
	other := &mtask{task: "T-1", state: stAwaiting, needs: heldOf(causeStalled)}
	if got := phase(other, overlays{}); got != wire.PhaseNeedsYou {
		t.Errorf("a stalled job's needs-you: phase %q, want needs-you, not held", got)
	}
	// The pipeline's stage is the phase's: the merge loop's states have one, the worker's, the drive's, a drop's and the ended none.
	for st, ph := range statePhase {
		inLoop := !strings.Contains(" waiting dispatched working driving merged rejected dropped dropping ", " "+st+" ")
		if (wire.PhaseStage(ph) != "") != inLoop {
			t.Errorf("%s: phase %s, stage %q", st, ph, wire.PhaseStage(ph))
		}
	}
}

// The step overlay is the task's open gate or publish: gating or merging while its effect runs (a running one
// wins over a pending one), else gate pending or publish pending; another step's intent (a prompt) or a task-less one
// (nightly) gives none. A watchdog's needs-you blocks its task and any task on its lane.
func TestPhaseOverlaysFromTheLog(t *testing.T) {
	t.Parallel()
	rec := func(task, step string) event.Record {
		return event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindIntent}
	}
	gate1, pub2 := rec("T-1", "gate"), rec("T-2", "publish.2")
	p := &proj{exec: map[event.Key]time.Time{gate1.Key(): time.Now()}}
	got := p.stepPhases([]event.Record{rec("T-1", "gate.2"), gate1, pub2, rec("T-3", "prompt.work"),
		rec("T-4", "publish"), rec("", "nightly.1")})
	want := map[string]string{"T-1": wire.PhaseGating, "T-2": wire.PhasePublishPending, "T-4": wire.PhasePublishPending}
	if len(got) != len(want) {
		t.Errorf("step phases %v, want %v", got, want)
	}
	for task, w := range want {
		if got[task] != w {
			t.Errorf("%s: step phase %q, want %q", task, got[task], w)
		}
	}
	p.exec[pub2.Key()] = time.Now()
	if got := p.stepPhases([]event.Record{pub2}); got["T-2"] != wire.PhaseMerging {
		t.Errorf("its publish running: %q, want merging", got["T-2"])
	}

	stalled := event.Record{Task: "T-9", Step: event.StepNeedsYou, Kind: event.KindFact,
		Data: []byte(`{"lane":"L-01","reason":"stalled"}`)}
	p = &proj{folded: folded{active: map[string][]event.Record{"T-9": {stalled}}}}
	if !p.blocked(&mtask{task: "T-1", lane: "L-01"}) || !p.blocked(&mtask{task: "T-9"}) {
		t.Error("a needs-you on the lane or the task: not blocked")
	}
	if p.blocked(&mtask{task: "T-1", lane: "L-02"}) {
		t.Error("a needs-you on another lane and task: blocked")
	}
	p.folded.active["T-9"] = []event.Record{{Task: "T-9", Step: event.StepStalled, Kind: event.KindFact}}
	if p.blocked(&mtask{task: "T-9"}) {
		t.Error("a stalled fact (no needs-you yet): blocked")
	}
}

// Serve's status ships the phase: a green task reads review (not the lane table's done of before), and its
// lane row reads it finished, so a dispatch of it is no redo in its lane.
func TestStatusShipsTheGreenTasksPhase(t *testing.T) {
	h, _ := seedQuestions(t, "Summary: done.")
	var ts wire.TaskStatus
	for _, x := range h.a.Status().Project.Tasks {
		if x.Task == "T-1" {
			ts = x
		}
	}
	if ts.State != stGreen || ts.Phase != wire.PhaseReview || wire.PhaseStage(ts.Phase) != wire.StageReview {
		t.Errorf("T-1: %+v; want green, phase review, stage review", ts)
	}
	h.a.mu.Lock()
	finished := h.a.p.finishedIn("T-1")
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-2"}
	other := h.a.p.finishedIn("T-1")
	h.a.mu.Unlock()
	if !finished || other {
		t.Errorf("finishedIn: %v with T-1's row, %v with none; want true, false", finished, other)
	}
}

// The merge stage is the task's own, not the display phase's: a watchdog's needs-you (a stalled
// worker, a question) turns a working or dispatching task's phase to needs-you but leaves it out of the merge loop;
// the merge loop's needs-you (held, red past K) keeps its stage; a gate or publish step gives its stage.
func TestStageIgnoresTheWatchdogsNeedsYou(t *testing.T) {
	t.Parallel()
	a := &App{}
	stalled := event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact,
		Data: []byte(`{"lane":"L-01","reason":"stalled"}`)}
	p := &proj{folded: folded{active: map[string][]event.Record{"T-1": {stalled}}}}
	for _, c := range []struct {
		name  string
		mt    *mtask
		steps map[string]string
		phase string
		stage string
	}{
		{"working, watchdog needs-you", &mtask{task: "T-1", lane: "L-01", state: stWorking}, nil, wire.PhaseNeedsYou, ""},
		{"dispatching, watchdog needs-you", &mtask{task: "T-1", lane: "L-01", state: stDispatched}, nil, wire.PhaseNeedsYou, ""},
		{"merge loop held", &mtask{task: "T-1", lane: "L-01", state: stAwaiting, needs: heldOf(event.CauseHeld)},
			nil, wire.PhaseHeld, wire.StageHeld},
		{"merge loop red past K", &mtask{task: "T-1", lane: "L-01", state: stRed, needs: heldOf(causeRed)},
			nil, wire.PhaseNeedsYou, wire.StageHeld},
		{"needs-you on a dispatch", &mtask{task: "T-1", lane: "L-01", state: stDispatched,
			needs: heldOf(causeStalled)}, nil, wire.PhaseNeedsYou, ""},
		{"needs-you on an undelivered fix job", &mtask{task: "T-1", lane: "L-01", state: stWorking,
			needs: heldOf(causeStalled), attemptVars: attemptVars{work: "fix"}}, nil, wire.PhaseNeedsYou, ""},
		{"gating, watchdog needs-you", &mtask{task: "T-1", lane: "L-01", state: stSubmitted},
			map[string]string{"T-1": wire.PhaseGating}, wire.PhaseNeedsYou, wire.StageGating},
	} {
		if got := a.phaseOf(p, c.mt, c.steps); got != c.phase {
			t.Errorf("%s: precondition phase %q, want %q", c.name, got, c.phase)
		}
		if got := a.stageOf(p, c.mt, c.steps); got != c.stage {
			t.Errorf("%s: stage %q, want %q", c.name, got, c.stage)
		}
	}
}
