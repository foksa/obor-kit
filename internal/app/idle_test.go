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

// A keyless idle.ack names an epoch, no key: it acknowledges nothing, while its call's reply still folds for the
// caller.
func TestKeylessIdleAckAcknowledgesNothing(t *testing.T) {
	t.Parallel()
	p := &proj{folded: newFolded()}
	p.foldRecord(event.Record{Task: event.IdleTask, Step: event.StepIdleAck, Kind: event.KindFact,
		Data: []byte(`{"epoch":9,"why":"old ack","by":"ob-lead","call":"c1","reply":"ACKED idle: old ack"}`)})
	if p.folded.idleAck.Key != 0 || p.folded.idleAck.Why != "" {
		t.Errorf("an old idle record acknowledged %+v; want nothing", p.folded.idleAck)
	}
	if got := p.folded.calls[verdictStep(wire.MethodIdle, "c1")]; got != "ACKED idle: old ack" {
		t.Errorf("the old ack's call reply %q, want the stored reply", got)
	}
}

// The last linked fix merged, the verify owed but not briefed (the reviewer working): no idle line.
// Briefed, the reviewer working: still none. Finished, nothing else in flight: one idle line.
func TestIdleWaitsForTheVerifyBrief(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.working(true) // the reviewer works: the verify is owed, not briefed
	r.merged("T-a")
	r.merged("T-b")
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	if !r.idleOK() {
		t.Fatal("precondition: the merged fixes before a verify pass are idle")
	}
	// The first pass starts the window; the one past it would post the line without the owed verify.
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 || r.idleOK() {
		t.Fatalf("idle lines %q on the first pass (idle %t), want none, the verify owed", lines, r.idleOK())
	}
	r.now = r.now.Add(2 * idleSettle)
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the verify is owed, want none", lines)
	}

	r.working(false)
	r.idlePass()
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || r.idleOK() {
		t.Fatalf("precondition: %d verify briefs (idle %t), want one, in flight", n, r.idleOK())
	}
	r.now = r.now.Add(2 * idleSettle)
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the verify is briefed, want none", lines)
	}

	r.finish()
	r.idleSettled()
	lines := r.idlePosts()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[obor] idle: nothing in flight") {
		t.Errorf("idle lines %q after the verify finished, want one", lines)
	}
	r.idlePass()
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines after another pass, want the one", n)
	}
}

// The review force-closed under a verify that did not finish: the pass finds it closed (its
// review.closed fact), owes no verify, lines nothing of it, and the one idle line comes out; later passes add none.
func TestIdleClosedRoundStopsCounting(t *testing.T) {
	t.Parallel()
	r := briefedVerify(t)
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	r.stop("s1", "overloaded", "API overloaded")
	r.working(false)
	owed := func() bool { // the verify rule's condition read, its action not run
		verifyRule(context.Background(), r.a, r.p, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}, r.now)
		r.a.mu.Lock()
		defer r.a.mu.Unlock()
		return r.p.verifyDue
	}
	if !owed() {
		t.Fatal("precondition: the verify that did not finish is not owed")
	}
	r.closedBy("RV-5", "RV-5 closed · 2222222 · approved · forced")
	if owed() {
		t.Error("a closed review owes a verify")
	}
	r.idleSettled()
	if lines := r.idlePosts(); len(lines) != 1 {
		t.Fatalf("idle lines %q after the closed round, want one", lines)
	}
	r.idlePass()
	if n, l := len(r.idlePosts()), r.verifyLines(); n != 1 || len(l) != 0 || len(verifyBriefs(t, r.p.root)) != 1 {
		t.Errorf("%d idle lines, verify lines %q, %d verify briefs after another pass; want the one, none, the first",
			n, l, len(verifyBriefs(t, r.p.root)))
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

// A merge, then an hour-long verify: the idle state is keyed by the verify's finished line, not the merge, so
// its age counts from the verify's end and the acknowledgement of the merge's state does not cover it.
func TestIdleKeyIsTheVerifyEnd(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	if !r.idleOK() {
		t.Fatal("precondition: the merged fixes before a verify pass are idle")
	}
	// The lead acknowledges the state the merges left, before the verify is in flight.
	if got, err := r.a.idleCall(wire.IdleReq{Envelope: wire.Envelope{Root: r.p.root, CallID: "i1", Role: "lead",
		Agent: "pj-lead"}, Why: "the fixes are in"}); err != nil || got != "ACKED idle: the fixes are in" {
		t.Fatalf("obor idle: %q, %v", got, err)
	}
	r.working(true) // the reviewer works: the verify is owed, not briefed
	r.idlePass()
	if r.idleOK() || len(verifyBriefs(t, r.p.root)) != 0 {
		t.Fatalf("precondition: idle %t, %d verify briefs; want the verify owed, not briefed", r.idleOK(),
			len(verifyBriefs(t, r.p.root)))
	}
	r.now = r.now.Add(time.Hour) // the verify runs an hour
	r.working(false)
	r.idlePass()
	if n := len(verifyBriefs(t, r.p.root)); n != 1 {
		t.Fatalf("precondition: %d verify briefs, want one", n)
	}
	r.now = r.now.Add(time.Hour)
	r.finish()
	r.idlePass() // the verify rule's pass reads the finished verify: nothing owed (the idle window starts)
	if !r.idleOK() {
		t.Fatal("precondition: the verify finished, nothing in flight, not idle")
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

// A review request ended by a failed --after task is an idle end of its own: that end is kept from setting
// the on-merge marker (reviewEnd), but idle reads its own (reviewIdleEnd), so the key and age count from the request's
// end (12:10), not from the task's drop (12:00). A --rm and a firing are ends of both markers.
func TestIdleKeyIsTheRequestEnd(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.submitted("T-b")
	r.mustCall("r1", []string{"T-b"}, "", false)
	if q := r.p.folded.review; q == nil || len(q.after) != 1 {
		t.Fatalf("precondition: no request waiting on T-b: %+v", q)
	}
	dropped := r.now
	r.dropped("T-b")
	r.now = r.now.Add(10 * time.Minute)
	r.pass()
	if r.p.folded.review != nil {
		t.Fatal("precondition: the request is still open")
	}
	end, at := lastFact(t, r.p.root, event.MsgTask, event.StepMessage)
	if !at.Equal(r.now) {
		t.Fatalf("precondition: the request ended at %v, want %v", at, r.now)
	}
	r.a.mu.Lock()
	n := r.p.idleNow()
	marker := r.p.folded.reviewEnd.seq
	r.a.mu.Unlock()
	if !n.ok || n.end != end || !n.at.Equal(at) || n.last != "T-b dropped: superseded" {
		t.Errorf("idle %+v, want key %d at %v (the request's end, not the drop at %v), last T-b dropped: superseded",
			n, end, at, dropped)
	}
	if marker != 0 {
		t.Errorf("the failed end set the on-merge marker (seq %d), want it unset (T-906)", marker)
	}

	// a --rm ends the request too, and is the marker's end as before
	r.mustCall("r2", nil, "", false)
	r.now = r.now.Add(time.Minute)
	r.mustCall("rm2", nil, "", true)
	rm, _ := lastFact(t, r.p.root, event.ReviewTask, event.StepReviewRm)
	r.a.mu.Lock()
	idleSeq, marker := r.p.folded.reviewIdleEnd.seq, r.p.folded.reviewEnd.seq
	r.a.mu.Unlock()
	if idleSeq != rm || marker != rm {
		t.Errorf("after the --rm: idle end %d, on-merge marker %d; want both %d", idleSeq, marker, rm)
	}

	// a firing ends it for both as well
	r.mustCall("r3", nil, "", false)
	r.now = r.now.Add(time.Minute)
	r.pass()
	fire, _ := lastFact(t, r.p.root, event.ReviewTask, event.StepReviewFire)
	r.a.mu.Lock()
	idleSeq, marker = r.p.folded.reviewIdleEnd.seq, r.p.folded.reviewEnd.seq
	r.a.mu.Unlock()
	if idleSeq != fire || marker != fire {
		t.Errorf("after the firing: idle end %d, on-merge marker %d; want both %d", idleSeq, marker, fire)
	}
}

// lastFact is the seq and time of the newest record of task at step (a test fails when there is none).
func lastFact(t *testing.T, root, task, step string) (int64, time.Time) {
	t.Helper()
	var seq int64
	var at time.Time
	for _, rec := range readLog(t, root) {
		if rec.Task == task && rec.Step == step {
			seq, at = rec.Seq, event.RecordTime(rec)
		}
	}
	if seq == 0 {
		t.Fatalf("no %s %s record in the log", task, step)
	}
	return seq, at
}

// With no work after it, the state is keyed as before: the merge (or the drop), and a merge's last after-merge
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

// An effect's durable sequence and time must identify the same idle epoch live
// and after replay. Use do, rather than injecting a result through record.
func TestEffectResultIdleEpochMatchesReplay(t *testing.T) {
	t.Parallel()
	h := ruleHarness(t)
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	addRecord(t, h, "T-1", event.StepMerged, event.KindFact, event.MergedData{Head: "abcdef0"})
	p := h.proj()
	for _, step := range afterSteps {
		h.now = h.now.Add(time.Minute)
		_, err := h.a.do(context.Background(), p, event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: step}},
			func(context.Context) (any, error) { return "done", nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	live := p.idleNow()
	replayed := reloadProj(t, h).idleNow()
	if !live.ok || live.end != replayed.end || !live.at.Equal(replayed.at) {
		t.Fatalf("live idle %+v, replayed %+v; want the same final effect's epoch", live, replayed)
	}
}
