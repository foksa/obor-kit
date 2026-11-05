package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"obor/internal/app/event"
)

// T-858: idle counts a verify the pipeline still owes — requested or admitted (before its brief) and a failed one its
// fix round requests again — so no idle line fires between a fix round's last merge and the verify's brief.

// afterStepsDone records task's after-merge steps as done: its merge alone is no work in flight.
func (r *reviewRig) afterStepsDone(task string) {
	r.t.Helper()
	for _, step := range afterSteps {
		r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindResult}, nil)
	}
}

// idlePass is the merge loop's idle check after its advance.
func (r *reviewRig) idlePass() { r.a.idleLines(context.Background()) }

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
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Idle != 0 {
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
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the verify is briefed, want none", lines)
	}

	r.finish()
	if v := r.lastVerify(); v.state != verifyDone || !r.idleOK() {
		t.Fatalf("precondition: the verify %s (idle %t), want done with nothing in flight", v.state, r.idleOK())
	}
	r.idlePass()
	lines := r.idlePosts()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[obor] idle: nothing in flight") {
		t.Errorf("idle lines %q after the verify is done, want one", lines)
	}
	r.idlePass()
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines after another pass, want the one", n)
	}
}

// T-858: the reviewer's error stop failed the verify, the fix round's tasks pending: the round waits to request its
// verify again and no idle line fires; the retried verify is in flight once requested and briefed, and the line comes
// only when the reviewer finished it.
func TestIdleFailedVerifyWaitingForItsRetry(t *testing.T) {
	t.Parallel()
	r, _ := briefedVerify(t)
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	r.stop("s1", "overloaded", "API overloaded")
	if v := r.lastVerify(); v.state != verifyFailed {
		t.Fatalf("precondition: the verify %s, want failed", v.state)
	}
	if r.idleOK() {
		t.Fatal("the failed verify its fix round requests again is idle, want in flight")
	}
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the round waits to request its verify again, want none", lines)
	}

	r.fixPass() // the reviewer still working: the new verify requested, not briefed
	if v := r.lastVerify(); v.state != verifyRequested || r.idleOK() {
		t.Fatalf("precondition: the retried verify %s (idle %t), want requested and in flight", v.state, r.idleOK())
	}
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 0 {
		t.Errorf("idle lines %q while the retried verify is requested, want none", lines)
	}

	r.working(false)
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyBriefed || r.idleOK() {
		t.Fatalf("precondition: the retried verify %s (idle %t), want briefed and in flight", v.state, r.idleOK())
	}
	r.finish()
	if v := r.lastVerify(); v.state != verifyDone || !r.idleOK() {
		t.Fatalf("precondition: the retried verify %s (idle %t), want done with nothing in flight", v.state, r.idleOK())
	}
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 1 || !strings.HasPrefix(lines[0], "[obor] idle: nothing in flight") {
		t.Errorf("idle lines %q after the retried verify is done, want one", lines)
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
// the round stops counting and the one idle line comes out; later passes add none.
func TestIdleClosedRoundStopsCounting(t *testing.T) {
	t.Parallel()
	r, _ := briefedVerify(t)
	r.afterStepsDone("T-a")
	r.afterStepsDone("T-b")
	r.stop("s1", "overloaded", "API overloaded")
	if r.idleOK() {
		t.Fatal("precondition: the failed verify's round is in flight before the close")
	}
	r.git.last = "RV-5 closed · 2222222 · approved · forced"
	r.fixPass()
	if got := r.pending(); got != "" {
		t.Fatalf("after the closed pass: pending %q, want nothing", got)
	}
	if !r.idleOK() {
		t.Fatal("the closed round still keeps the pipeline from idle")
	}
	r.fixPass() // pending is empty: the drop is not recorded again
	if n := r.dropRecords(); n != 1 {
		t.Fatalf("%d drop records for the closed round, want one", n)
	}
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 1 {
		t.Fatalf("idle lines %q after the closed round, want one", lines)
	}
	r.idlePass()
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines after another pass, want the one", n)
	}
}

// T-858: a failed verify no fix round retries is not in flight by itself: with nothing else running the pipeline is
// idle and posts its one line.
func TestIdleFailedVerifyByHandIsNotInFlight(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	r.submitted("T-a")
	r.merged("T-a")
	r.afterStepsDone("T-a")
	if reply, err := r.verifyCall("v1", "T-a"); err != nil || !strings.HasPrefix(reply, "BRIEFED pj-rev") {
		t.Fatalf("verify T-a: %q, %v; want BRIEFED", reply, err)
	}
	r.stop("s1", "billing_error", "")
	if v := r.lastVerify(); v.state != verifyFailed {
		t.Fatalf("precondition: the verify %s, want failed", v.state)
	}
	if !r.idleOK() {
		t.Fatal("a failed verify no round retries keeps the pipeline from idle")
	}
	r.idlePass()
	if lines := r.idlePosts(); len(lines) != 1 {
		t.Errorf("idle lines %q after the failed verify, want one", lines)
	}
}
