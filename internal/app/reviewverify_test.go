package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// T-823: a verify's coverage lives in its brief's one fact, and one verify may be pending at a time.

// openReviewFile writes the open review's file (RV-5) in the rig's main checkout.
func openReviewFile(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reviews", "RV-5.md"), []byte("# RV-5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// verifyBriefs are the log's verify briefs of the reviewer, in order.
func verifyBriefs(t *testing.T, root string) []event.Message {
	t.Helper()
	var out []event.Message
	for _, r := range readLog(t, root) {
		var m event.Message
		if r.Task == event.MsgTask && r.Step == event.StepBriefed && json.Unmarshal(r.Data, &m) == nil &&
			m.To == "pj-rev" && len(m.Verify) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// startedReview records the open review's start fact (RV-5 covers T-start; the verifies add to it).
func startedReview(t *testing.T, r *reviewRig) {
	t.Helper()
	if !r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: "RV-5", Head: "abcdef0123456789", Tasks: []string{"T-start"}}) {
		t.Fatal("the log refused the review start")
	}
}

// briefFact records a brief fact and is its seq (the test fails when the log refuses it).
func briefFact(t *testing.T, r *reviewRig, m event.Message) int64 {
	t.Helper()
	seq, ok := r.a.recordSeq(r.p, event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact}, m)
	if !ok {
		t.Fatal("the log refused the brief fact")
	}
	return seq
}

// deliverFact records fact seq's delivery result: a line is delivered unless its result is retiredResult.
func deliverFact(t *testing.T, r *reviewRig, seq int64, res string) {
	t.Helper()
	if !r.a.record(r.p, event.Record{Task: event.MsgTask, Step: stepDeliver + strconv.FormatInt(seq, 10),
		Kind: event.KindResult}, res) {
		t.Fatal("the log refused the delivery result")
	}
}

// T-823: two verifies back to back: the second is refused while the first brief is undelivered; once it is delivered a
// second is accepted, and each covers its tasks once its own brief was delivered.
func TestReviewVerifyPendingAndCoverage(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	verify := func(call string, tasks ...string) error {
		_, err := r.a.verifyCall(wire.Call{Root: r.p.root, CallID: call, Role: "lead", Agent: "pj-lead", After: tasks})
		return err
	}
	if err := verify("v1", "T-a"); err != nil {
		t.Fatal(err)
	}
	if covered, _, ok := ReviewCovered(r.p.root); !ok || strings.Join(covered, ",") != "T-start" {
		t.Fatalf("precondition: %q covered while the verify brief is still queued (ok %t), want the start's T-start", covered, ok)
	}
	err := verify("v2", "T-b")
	if err == nil || !strings.Contains(err.Error(), "a verify is pending") {
		t.Fatalf("a second verify with the first brief undelivered: %v, want the pending refusal", err)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || len(r.git.marked) != 1 {
		t.Errorf("after the refused verify: %d briefs, %d marks; want one of each (nothing recorded)", n, len(r.git.marked))
	}

	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}}) // the reviewer's line goes out
	if err := verify("v2", "T-b"); err != nil {
		t.Fatalf("a second verify after the first brief's delivery: %v", err)
	}
	if covered, _, ok := ReviewCovered(r.p.root); !ok || strings.Join(covered, ",") != "T-start,T-a" {
		t.Errorf("covered %q, ok %t; want the start's T-start and the delivered first verify's T-a", covered, ok)
	}
	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}})
	if covered, _, ok := ReviewCovered(r.p.root); !ok || strings.Join(covered, ",") != "T-start,T-a,T-b" {
		t.Errorf("covered %q, ok %t; want the start's T-start and both delivered verifies' T-a,T-b", covered, ok)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 2 {
		t.Errorf("%d verify briefs, want two", n)
	}
	for _, rec := range readLog(t, r.p.root) { // the coverage is the brief's own fact (T-823)
		if rec.Task == event.ReviewTask && rec.Step == event.StepReviewVerify {
			t.Errorf("a new verify wrote a %s fact", event.StepReviewVerify)
		}
	}
}

// T-823: a refused brief append fails the verify with nothing covered; the same call id retried after a restart records
// one brief, whose tasks cover once it is delivered.
func TestReviewVerifyBriefRefused(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	r.refuseStep(event.StepBriefed)
	call := wire.Call{Root: r.p.root, CallID: "v1", Role: "lead", Agent: "pj-lead", After: []string{"T-a"}}
	if _, err := r.a.verifyCall(call); err == nil || !strings.Contains(err.Error(), "refused it") {
		t.Fatalf("a refused brief append: %v, want the verify to fail", err)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 0 {
		t.Fatalf("precondition: %d briefs after the refused append, want none", n)
	}
	if covered, _, _ := ReviewCovered(r.p.root); strings.Join(covered, ",") != "T-start" {
		t.Fatalf("precondition: %q covered, want only the start's T-start", covered)
	}

	r.reload() // a restart: the refusal is not carried
	if _, err := r.a.verifyCall(call); err != nil {
		t.Fatalf("the same call id retried after a restart: %v", err)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 1 {
		t.Fatalf("%d briefs after the retry, want one", n)
	}
	if covered, _, _ := ReviewCovered(r.p.root); strings.Join(covered, ",") != "T-start" {
		t.Errorf("%q covered while the brief is still queued, want only the start's T-start", covered)
	}
	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}})
	if covered, _, ok := ReviewCovered(r.p.root); !ok || strings.Join(covered, ",") != "T-start,T-a" {
		t.Errorf("covered %q, ok %t; want the start's T-start and the delivered brief's T-a", covered, ok)
	}
}

// T-823: a verify brief covers its tasks only once delivered: queued covers nothing, a line closed unsent too, and a
// store from before T-823 keeps its review.verify facts' coverage.
func TestReviewCoveredVerifyDelivery(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	if _, _, ok := ReviewCovered(r.p.root); ok {
		t.Fatal("precondition: a start is recorded before any review ran")
	}
	r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: "RV-5", Head: "abcdef0123456789", Tasks: []string{"T-a", "T-b"}})
	queued := briefFact(t, r, event.Message{To: "pj-rev", Line: "[obor lead] verify T-d", Verify: []string{"T-d"}})
	if covered, _, _ := ReviewCovered(r.p.root); strings.Join(covered, ",") != "T-a,T-b" {
		t.Fatalf("a queued verify brief: %q covered, want T-a,T-b", covered)
	}
	retired := briefFact(t, r, event.Message{To: "pj-rev", Line: "[obor lead] verify T-e", Verify: []string{"T-e"}})
	deliverFact(t, r, retired, retiredResult)
	deliverFact(t, r, queued, "sent")
	covered, start, ok := ReviewCovered(r.p.root)
	if !ok || start != "abcdef0123456789" || strings.Join(covered, ",") != "T-a,T-b,T-d" {
		t.Errorf("covered %q, start %q, ok %t; want T-a,T-b,T-d at the start's head", covered, start, ok)
	}

	// An older store's fact (T-822) still covers, read in log order with the briefs'.
	r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewVerify, Kind: event.KindFact},
		ReviewData{Tasks: []string{"T-f"}})
	if covered, _, _ := ReviewCovered(r.p.root); strings.Join(covered, ",") != "T-a,T-b,T-d,T-f" {
		t.Errorf("an old review.verify fact: %q covered, want the briefs' T-a,T-b,T-d then T-f", covered)
	}
}
