package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-858: idle counts a verify the pipeline still owes — requested or admitted (before its brief) — so no idle line
// fires between a fix round's last merge and the verify's brief (T-869: a failed one its round requests again within the
// pass, inside the settle window, T-859).

// afterStepsDone records task's after-merge steps as done: its merge alone is no work in flight.
func (r *reviewRig) afterStepsDone(task string) {
	r.t.Helper()
	for _, step := range afterSteps {
		r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindResult}, nil)
	}
}

// idlePass is the merge loop's rule pass after its advance.
func (r *reviewRig) idlePass() { r.a.rulePass(context.Background(), rules) }

// idleSettled lets the idle window pass at the rig's clock: the first check starts (or keeps) it, the clock moves past
// idleSettle, and the next check posts the line.
func (r *reviewRig) idleSettled() {
	r.t.Helper()
	r.idlePass()
	r.now = r.now.Add(idleSettle)
	r.idlePass()
}

// idleOK is idleNow's answer.
func (r *reviewRig) idleOK() bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.p.idleNow().ok
}

// idlePosts are the idle lines the log recorded to the lead, in order.
func (r *reviewRig) idlePosts() []string {
	r.t.Helper()
	var out []string
	for _, rec := range readLog(r.t, r.p.root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == ruleIdle {
			out = append(out, m.Line)
		}
	}
	return out
}

// T-858: the last linked fix merged, the verify requested but not briefed: no idle line. Briefed, the reviewer
// working: still none. Finished, nothing else in flight: one idle line.
func TestIdleWaitsForTheVerifyBrief(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	startedReview(t, r)
	r.working(true) // the reviewer works: the verify is requested, not briefed
	r.merged("T-a")
	r.merged("T-b")
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	if !r.idleOK() {
		t.Fatal("precondition: the merged fixes with no verify yet are idle")
	}
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyRequested {
		t.Fatalf("precondition: the verify %s, want requested", v.state)
	}
	// T-859 keeps this input: a requested verify can outlast the settle window (the reviewer working), so no idle line
	// may go out before its brief. The first pass starts the window; the one past it would post the line without it.
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Fatalf("idle lines %q on the first idle check, want none", lines)
	}
	r.now = r.now.Add(2 * idleSettle)
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the verify is requested, want none", lines)
	}

	r.working(false)
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyBriefed || r.idleOK() {
		t.Fatalf("precondition: the verify %s (idle %t), want briefed and in flight", v.state, r.idleOK())
	}
	r.idlePass()
	r.now = r.now.Add(2 * idleSettle)
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the verify is briefed, want none", lines)
	}

	r.finish()
	if v := r.lastVerify(); v.state != verifyDone || !r.idleOK() {
		t.Fatalf("precondition: the verify %s (idle %t), want done with nothing in flight", v.state, r.idleOK())
	}
	r.idleSettled()
	lines := r.idlePosts()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[obor] idle: nothing in flight") {
		t.Errorf("idle lines %q after the verify is done, want one", lines)
	}
	r.idlePass()
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines after another pass, want the one", n)
	}
}

// T-859: the last linked fix merges and a connection check's idle pass runs before the merge loop's fix pass: the
// check only starts the window, the fix pass requests and briefs the verify within it, and no idle line goes out.
func TestIdleSettleCoversTheVerifyRace(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	startedReview(t, r)
	r.merged("T-a")
	r.merged("T-b")
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	if !r.idleOK() {
		t.Fatal("precondition: the merged fixes with no verify yet are idle")
	}
	r.idlePass() // the connection check: the pipeline looks empty, the window starts
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Fatalf("idle lines %q on the first idle check, want none", lines)
	}
	r.now = r.now.Add(5 * time.Second) // the merge loop's fix pass, seconds later
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyBriefed {
		t.Fatalf("precondition: the verify %s, want briefed within the window", v.state)
	}
	r.now = r.now.Add(2 * idleSettle) // past the window: the verify is still work
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the verify is briefed, want none", lines)
	}
	r.finish()
	r.idleSettled()
	if lines := r.idlePosts(); len(lines) != 1 {
		t.Errorf("idle lines %q after the verify is done, want one", lines)
	}
}

// dropRecords are the log's message facts that drop fix tasks (FixDrop).
func (r *reviewRig) dropRecords() int {
	r.t.Helper()
	n := 0
	for _, rec := range readLog(r.t, r.p.root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && len(m.FixDrop) > 0 {
			n++
		}
	}
	return n
}

// T-858: the round's review force-closed under a failed verify: the next pass records its pending tasks dropped (once),
// requests no verify again, and the one idle line comes out; later passes add none. T-869 (DD U2): the failed verify
// is not in flight by itself (its round asks again within the pass, inside the settle window).
func TestIdleClosedRoundStopsCounting(t *testing.T) {
	t.Parallel()
	r, _ := briefedVerify(t)
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	r.stop("s1", "overloaded", "API overloaded")
	if !r.idleOK() || r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Fatalf("precondition: idle %t, %d verify requests; want the failed verify not in flight, one request",
			r.idleOK(), r.facts(event.StepReviewVerifyRequest))
	}
	r.git.last = "RV-5 closed · 2222222 · approved · forced"
	r.fixPass()
	if got := r.pending(); got != "" {
		t.Fatalf("after the closed pass: pending %q, want nothing", got)
	}
	if !r.idleOK() || r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Fatalf("the closed round: idle %t, %d verify requests; want idle, no new request", r.idleOK(),
			r.facts(event.StepReviewVerifyRequest))
	}
	r.fixPass() // pending is empty: the drop is not recorded again
	if n := r.dropRecords(); n != 1 {
		t.Fatalf("%d drop records for the closed round, want one", n)
	}
	r.idleSettled()
	if lines := r.idlePosts(); len(lines) != 1 {
		t.Fatalf("idle lines %q after the closed round, want one", lines)
	}
	r.idlePass()
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines after another pass, want the one", n)
	}
}

// lastFinished is the log's newest finished line (a message fact with Ends), its seq and time.
func lastFinished(t *testing.T, root string) (seq int64, at time.Time) {
	t.Helper()
	for _, rec := range readLog(t, root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Ends != "" {
			seq, at = rec.Seq, event.RecordTime(rec)
		}
	}
	if seq == 0 {
		t.Fatal("no finished line in the log")
	}
	return seq, at
}

// T-868: a merge, then an hour-long verify: the idle state is keyed by the verify's finished line, not the merge, so
// its age counts from the verify's end and the acknowledgement of the merge's state does not cover it.
func TestIdleKeyIsTheVerifyEnd(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	startedReview(t, r)
	r.merged("T-a")
	r.merged("T-b")
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	if !r.idleOK() {
		t.Fatal("precondition: the merged fixes with nothing in flight are idle")
	}
	// The lead acknowledges the state the merges left, before the verify is in flight.
	if got, err := r.a.idleCall(wire.Call{Root: r.p.root, CallID: "i1", Role: "lead", Agent: "pj-lead",
		Note: "the fixes are in"}); err != nil || got != "ACKED idle: the fixes are in" {
		t.Fatalf("obor idle: %q, %v", got, err)
	}
	r.working(true) // the reviewer works: the fix round's verify is requested, not briefed
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyRequested || r.idleOK() {
		t.Fatalf("precondition: the verify %s (idle %t), want requested and in flight", v.state, r.idleOK())
	}
	r.now = r.now.Add(time.Hour) // the verify runs an hour
	r.working(false)
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyBriefed {
		t.Fatalf("precondition: the verify %s, want briefed", v.state)
	}
	r.now = r.now.Add(time.Hour)
	r.finish()
	if v := r.lastVerify(); v.state != verifyDone || !r.idleOK() {
		t.Fatalf("precondition: the verify %s (idle %t), want done with nothing in flight", v.state, r.idleOK())
	}
	finished, at := lastFinished(t, r.p.root)
	r.idleSettled()
	if lines := r.idlePosts(); len(lines) != 1 || !strings.HasPrefix(lines[0], "[obor] idle: nothing in flight") {
		t.Fatalf("idle lines %q after the verify, want one: the merge's acknowledgement does not cover its end", lines)
	}
	if _, did := r.p.folded.did[event.Of{Kind: ruleIdle, Seq: finished}]; !did {
		t.Errorf("the idle line is not keyed by the finished record %d", finished)
	}
	if st := r.a.Status().Project.Idle; st == nil || st.Since != at.Unix() || st.Acked {
		t.Errorf("status idle %+v, want since the finished record (%d) and not acknowledged", st, at.Unix())
	}
}

// T-868: with no work after it, the state is keyed as before: the merge (or the drop), and a merge's last after-merge
// step once that is newer. `last` names the last merge or drop.
func TestIdleKeyIsTheNewestEnd(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		last string
		// setup records the ends and returns the newest one's seq and time.
		setup func(r *reviewRig) (int64, time.Time)
	}{
		{name: "a merge with nothing after: the merge", last: "T-a merged (outside obor serve)",
			setup: func(r *reviewRig) (int64, time.Time) {
				r.submitted("T-a")
				seq, _ := r.a.recordSeq(r.p, event.Record{Task: "T-a", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact},
					event.MergedData{Outside: true})
				return seq, r.now
			}},
		{name: "a drop: the drop", last: "T-a dropped: superseded",
			setup: func(r *reviewRig) (int64, time.Time) {
				r.submitted("T-a")
				seq, _ := r.a.recordSeq(r.p, event.Record{Task: "T-a", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
					event.FactData{Reason: "superseded"})
				return seq, r.now
			}},
		{name: "a merge's last after-merge step: that step", last: "T-a merged ccccccc",
			setup: func(r *reviewRig) (int64, time.Time) {
				r.submitted("T-a")
				r.merged("T-a")
				r.now = r.now.Add(time.Minute)
				r.a.recordSeq(r.p, event.Record{Task: "T-a", Attempt: 1, Step: event.StepAfterMerge, Kind: event.KindResult}, nil)
				r.now = r.now.Add(time.Minute)
				seq, _ := r.a.recordSeq(r.p, event.Record{Task: "T-a", Attempt: 1, Step: event.StepStop, Kind: event.KindResult}, nil)
				return seq, r.now
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReviewRig(t)
			want, at := c.setup(r)
			r.a.mu.Lock()
			n := r.p.idleNow()
			r.a.mu.Unlock()
			if !n.ok || n.end != want || !n.at.Equal(at) || n.last != c.last {
				t.Errorf("idle %+v, want key %d at %v with last %q", n, want, at, c.last)
			}
		})
	}
}
