package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"obor/internal/app/wire"
)

// T-850: obor review fix links fix tasks to the open review; serve verifies once every linked task merged.

// fix is an obor review fix call from the lead.
func (r *reviewRig) fix(id, note string, tasks ...string) (string, error) {
	return r.a.fixCall(wire.Call{Root: r.p.root, CallID: id, Role: "lead", Agent: "pj-lead", After: tasks, Note: note})
}

// fixPass is one fix-round pass.
func (r *reviewRig) fixPass() {
	r.a.fixPass(context.Background(), r.p, &Loop{Review: r.git, Agents: r.agents})
}

// fixRig is a rig with the open review RV-5 (its file written, RV-4 closed) and T-a, T-b dispatched and linked.
func fixRig(t *testing.T) *reviewRig {
	t.Helper()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	r.submitted("T-a")
	r.submitted("T-b")
	reply, err := r.fix("f1", "check #2 too", "T-a", "T-b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply, "FIX LINKED to RV-5: T-a,T-b ") {
		t.Fatalf("the fix reply %q", reply)
	}
	return r
}

// Linked tasks merge one by one, with a restart between the merges and between the last merge and the pass that
// briefs, and again after the brief: one verify, after the last merge, naming both tasks; close's pending set empties.
func TestReviewFixVerifiesOnceAfterLastMerge(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	if got := strings.Join(ReviewFixPending(r.p.root, "RV-5"), ","); got != "T-a,T-b" {
		t.Fatalf("precondition: pending %q, want T-a,T-b", got)
	}
	r.fixPass()
	r.merged("T-a")
	r.fixPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || len(r.git.marked) != 0 {
		t.Fatalf("T-b not merged yet: %d verify briefs, %d marks; want none", n, len(r.git.marked))
	}
	r.reload() // a restart between the merges
	r.fixPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 {
		t.Fatalf("after the restart, T-b not merged: %d verify briefs, want none", n)
	}
	r.merged("T-b")
	r.reload() // a restart between the last merge and the brief
	r.fixPass()
	r.fixPass()
	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}}) // delivered: no pending verify holds a second
	r.reload()                                                        // and after the brief
	r.fixPass()
	briefs := verifyBriefs(t, r.p.root)
	if len(briefs) != 1 || len(r.git.marked) != 1 {
		t.Fatalf("%d verify briefs, %d marks; want one of each", len(briefs), len(r.git.marked))
	}
	b := briefs[0]
	if strings.Join(b.Verify, ",") != "T-a,T-b" || !strings.Contains(b.Line, "fixed in T-a (aaaaaaa), T-b (bbbbbbb)") ||
		!strings.Contains(b.Line, "Lead's note: check #2 too") || !strings.HasPrefix(b.Line, "[obor obor] ") {
		t.Errorf("the verify brief: %+v", b)
	}
	if got := ReviewFixPending(r.p.root, "RV-5"); len(got) != 0 {
		t.Errorf("after the verify: pending %q, want none", got)
	}
}

// A second obor review fix adds its task: the round verifies once all three merged.
func TestReviewFixAddsTasks(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.submitted("T-c")
	reply, err := r.fix("f2", "", "T-c")
	if err != nil || !strings.HasPrefix(reply, "FIX LINKED to RV-5: T-a,T-b,T-c ") {
		t.Fatalf("the second fix: %q, %v", reply, err)
	}
	r.git.merges = append(r.git.merges, "ccccccc3 Merge T-c: three")
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 {
		t.Fatalf("T-c not merged yet: %d verify briefs, want none", n)
	}
	r.merged("T-c")
	r.fixPass()
	if briefs := verifyBriefs(t, r.p.root); len(briefs) != 1 || strings.Join(briefs[0].Verify, ",") != "T-a,T-b,T-c" {
		t.Errorf("verify briefs %+v, want one naming T-a,T-b,T-c", briefs)
	}
}

// A linked task dropped: the lead gets one line (not again on later passes or after a restart) and the verify waits
// for the rest, then names only the merged task.
func TestReviewFixTaskDropped(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.dropped("T-b")
	r.fixPass()
	r.fixPass()
	r.reload()
	r.fixPass()
	var lines []string
	for _, l := range r.leadLines() {
		if strings.Contains(l, "fix") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || lines[0] != "[obor] RV-5 fix T-b ended unmerged: the verify waits for T-a" {
		t.Fatalf("the lead's fix lines %q, want one naming T-b", lines)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 0 {
		t.Fatalf("T-a not merged yet: %d verify briefs, want none", n)
	}
	if got := strings.Join(ReviewFixPending(r.p.root, "RV-5"), ","); got != "T-a" {
		t.Errorf("pending %q, want T-a (T-b dropped)", got)
	}
	r.merged("T-a")
	r.fixPass()
	if briefs := verifyBriefs(t, r.p.root); len(briefs) != 1 || strings.Join(briefs[0].Verify, ",") != "T-a" {
		t.Errorf("verify briefs %+v, want one naming T-a only", briefs)
	}
}

// The round's review closed (a forced close): its pending tasks merging verify nothing.
func TestReviewFixClosedReview(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.git.last = "RV-5 closed · 2222222 · approved · forced"
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || len(r.git.marked) != 0 {
		t.Errorf("a closed review's round: %d verify briefs, %d marks; want none", n, len(r.git.marked))
	}
}

// A verify that fails (git refusing the mark): the lead gets one line, the round waits for a verify by hand.
func TestReviewFixVerifyFails(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.git.markErr = errors.New("disk full")
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	r.fixPass()
	var lines []string
	for _, l := range r.leadLines() {
		if strings.Contains(l, "fix verify") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "RV-5 fix verify of T-a,T-b failed: disk full") {
		t.Fatalf("the lead's failure lines %q, want one", lines)
	}
	if got := strings.Join(ReviewFixPending(r.p.root, "RV-5"), ","); got != "T-a,T-b" {
		t.Errorf("pending %q, want T-a,T-b (not verified)", got)
	}
}

// Refused, nothing recorded: no open review, a task not dispatched, a task dropped already, no task.
func TestReviewFixRefused(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.submitted("T-a")
	if _, err := r.fix("f1", "", "T-a"); err == nil || !strings.Contains(err.Error(), "no open review") {
		t.Errorf("no open review: %v, want the refusal", err)
	}
	openReviewFile(t, r.p.root)
	if _, err := r.fix("f2", "", "T-a", "T-z"); err == nil || !strings.Contains(err.Error(), "T-z is not dispatched") {
		t.Errorf("a task not dispatched: %v, want the refusal", err)
	}
	r.submitted("T-b")
	r.dropped("T-b")
	if _, err := r.fix("f3", "", "T-b"); err == nil || !strings.Contains(err.Error(), "T-b dropped already") {
		t.Errorf("a dropped task: %v, want the refusal", err)
	}
	if _, err := r.fix("f4", ""); err == nil {
		t.Error("no task: accepted")
	}
	if n := r.facts("review.fix"); n != 0 || r.p.folded.fix != nil {
		t.Errorf("refused fixes recorded %d facts (round %+v)", n, r.p.folded.fix)
	}
	reply, err := r.fix("f5", "", "T-a")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := r.fix("f5", "", "T-a"); err != nil || again != reply || r.facts("review.fix") != 1 {
		t.Errorf("the same call id again: %q, %v, %d facts; want the stored reply, one fact", again, err, r.facts("review.fix"))
	}
}
