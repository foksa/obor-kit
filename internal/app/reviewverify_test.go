package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// T-823, T-856: a verify is one record (requested → admitted → briefed → done | superseded, or failed), admitted only
// while the reviewer is idle, its tasks verified and covered once the reviewer finished its brief.

// headSHA is the rig's integration branch head (reviewGitFake.Head).
const headSHA = "abcdef0123456789abcdef0123456789abcdef01"

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

// startedReview records the open review's start fact (RV-5 covers T-start at 1234567…; the verifies add to it).
func startedReview(t *testing.T, r *reviewRig) {
	t.Helper()
	if !r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: "RV-5", Head: "1234567890abcdef", Tasks: []string{"T-start"}}) {
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

// verifyCall is obor review verify of tasks from the lead.
func (r *reviewRig) verifyCall(call string, tasks ...string) (string, error) {
	return r.a.verifyCall(wire.Call{Root: r.p.root, CallID: call, Role: "lead", Agent: "pj-lead", After: tasks})
}

// verifyPass is one verify admission pass.
func (r *reviewRig) verifyPass() {
	r.a.verifyPass(context.Background(), r.p, &Loop{Review: r.git, Agents: r.agents})
}

// working sets the reviewer's pane working (true) or idle.
func (r *reviewRig) working(on bool) {
	status := "idle"
	if on {
		status = "working"
	}
	lineStatus(r.a, "w:rev", status, r.now)
}

// finish is the reviewer taking its open brief: its line delivered, its pane working, then idle; the finished line
// that ends the brief recorded.
func (r *reviewRig) finish() {
	r.t.Helper()
	if r.p.out.brief("pj-rev") == nil {
		r.t.Fatal("precondition: the reviewer has no open brief to finish")
	}
	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}})
	r.working(true)
	r.working(false)
	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}})
	if r.p.out.brief("pj-rev") != nil {
		r.t.Fatal("precondition: the reviewer's brief is still open after its work")
	}
}

// lastVerify is the newest verify record (the test fails when there is none).
func (r *reviewRig) lastVerify() *verifyRec {
	r.t.Helper()
	vs := r.p.folded.rv.sorted()
	if len(vs) == 0 {
		r.t.Fatal("no verify recorded")
	}
	return vs[len(vs)-1]
}

// T-856: fixes merge while the reviewer is idle: one verify, its worktree moved to the head, briefed and the head
// marked once briefed; the tasks are verified only when the reviewer finished that brief.
func TestVerifyReviewerIdle(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	startedReview(t, r)
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	v := r.lastVerify()
	if v.state != verifyBriefed || len(r.git.detached) != 1 || !strings.HasSuffix(r.git.detached[0], "@"+headSHA) ||
		strings.Join(r.git.marked, ",") != headSHA || len(verifyBriefs(t, r.p.root)) != 1 {
		t.Fatalf("state %s, detached %q, marked %q, %d briefs; want briefed once, moved and marked at the head",
			v.state, r.git.detached, r.git.marked, len(verifyBriefs(t, r.p.root)))
	}
	if got := r.pending(); got != "T-a,T-b" {
		t.Fatalf("briefed, not finished: pending %q, want T-a,T-b", got)
	}
	r.finish()
	c := ReadReviewCycle(r.p.root, "RV-5")
	if r.lastVerify().state != verifyDone || len(c.Pending) != 0 || strings.Join(c.Covered, ",") != "T-start,T-a,T-b" ||
		c.Reviewed != headSHA {
		t.Errorf("state %s, cycle %+v; want done, nothing pending, T-a,T-b covered, the verify's head reviewed",
			r.lastVerify().state, c)
	}
}

// T-856: fixes merge while the reviewer works: the verify is requested and waits; the reviewer's worktree is not moved
// and nothing is briefed or marked until it is idle.
func TestVerifyWaitsWhileReviewerWorks(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.working(true)
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyRequested || r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Fatalf("state %s, %d requests; want one requested", v.state, r.facts(event.StepReviewVerifyRequest))
	}
	if len(r.git.detached) != 0 || len(r.git.marked) != 0 || len(verifyBriefs(t, r.p.root)) != 0 {
		t.Fatalf("while the reviewer works: detached %q, marked %q, %d briefs; want none", r.git.detached, r.git.marked,
			len(verifyBriefs(t, r.p.root)))
	}
	r.working(false)
	r.fixPass()
	if v := r.lastVerify(); v.state != verifyBriefed || len(r.git.detached) != 1 || len(verifyBriefs(t, r.p.root)) != 1 {
		t.Errorf("idle: state %s, detached %q, %d briefs; want briefed, moved once", v.state, r.git.detached,
			len(verifyBriefs(t, r.p.root)))
	}
}

// T-856: obor review verify while the reviewer works: recorded, the reply says it waits, no move; the same call id
// again gets the request's reply; once the reviewer is idle a pass briefs it. With the reviewer idle the call itself
// briefs (BRIEFED).
func TestVerifyByHandWaits(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	r.working(true)
	reply, err := r.verifyCall("v1", "T-a")
	if err != nil || !strings.HasPrefix(reply, "VERIFY WAITS (RV-5: T-a): pj-rev is working") {
		t.Fatalf("verify while the reviewer works: %q, %v; want VERIFY WAITS naming the working reviewer", reply, err)
	}
	if len(r.git.detached) != 0 || len(verifyBriefs(t, r.p.root)) != 0 || r.lastVerify().state != verifyRequested {
		t.Fatalf("detached %q, %d briefs, state %s; want no move, no brief, requested", r.git.detached,
			len(verifyBriefs(t, r.p.root)), r.lastVerify().state)
	}
	if again, err := r.verifyCall("v1", "T-a"); err != nil || !strings.HasPrefix(again, "VERIFY REQUESTED (RV-5: T-a;") ||
		r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Errorf("the same call id again: %q, %v, %d requests; want the stored reply, one request", again, err,
			r.facts(event.StepReviewVerifyRequest))
	}
	r.verifyPass()
	if len(r.git.detached) != 0 {
		t.Fatalf("a pass while the reviewer works moved its worktree: %q", r.git.detached)
	}
	r.working(false)
	r.verifyPass()
	if b := verifyBriefs(t, r.p.root); len(b) != 1 || !strings.HasPrefix(b[0].Line, "[obor pj-lead] The RV-5 findings were fixed in T-a") ||
		len(r.git.detached) != 1 {
		t.Errorf("idle: briefs %+v, detached %q; want the lead's verify briefed, moved once", b, r.git.detached)
	}

	idle := newReviewRig(t)
	openReviewFile(t, idle.p.root)
	if reply, err := idle.verifyCall("v1", "T-a"); err != nil || !strings.HasPrefix(reply, "BRIEFED pj-rev") {
		t.Errorf("verify with the reviewer idle: %q, %v; want BRIEFED", reply, err)
	}
}

// T-856: an auto verify's brief queued, then an ordinary brief to the reviewer supersedes it: the tasks are pending
// again (close refused), no new verify while the ordinary brief is open, a new one once the reviewer finished it.
func TestVerifySupersededByBrief(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	first := r.lastVerify()
	if first.state != verifyBriefed {
		t.Fatalf("precondition: the auto verify %s, want briefed (its line queued)", first.state)
	}
	if _, err := r.a.briefCall(wire.Call{Root: r.p.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-rev",
		Note: "look at the flaky test"}); err != nil {
		t.Fatal(err)
	}
	if first.state != verifySuperseded {
		t.Fatalf("after the ordinary brief: the verify %s, want superseded", first.state)
	}
	if got := r.pending(); got != "T-a,T-b" {
		t.Errorf("superseded: close's pending %q, want T-a,T-b", got)
	}
	r.fixPass()
	second := r.lastVerify()
	if second == first || second.state != verifyRequested || len(verifyBriefs(t, r.p.root)) != 1 {
		t.Fatalf("the ordinary brief open: a new verify %s, %d verify briefs; want a new one requested, not briefed",
			second.state, len(verifyBriefs(t, r.p.root)))
	}
	r.finish() // the reviewer works on the ordinary brief and finishes it
	r.fixPass()
	if second.state != verifyBriefed || len(verifyBriefs(t, r.p.root)) != 2 || r.facts(event.StepReviewVerifyRequest) != 2 {
		t.Errorf("the reviewer idle again: the new verify %s, %d verify briefs, %d requests; want it briefed, two of each",
			second.state, len(verifyBriefs(t, r.p.root)), r.facts(event.StepReviewVerifyRequest))
	}
}

// T-856: close before the verify's brief is delivered (or finished) is refused: the tasks are pending.
func TestVerifyCloseBeforeDone(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	if _, err := r.verifyCall("v1", "T-a"); err != nil {
		t.Fatal(err)
	}
	if got := r.pending(); got != "T-a" {
		t.Errorf("a by-hand verify queued: pending %q, want T-a", got)
	}
	r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}})
	if got := r.pending(); got != "T-a" {
		t.Errorf("delivered, not finished: pending %q, want T-a", got)
	}
	r.finish()
	if got := r.pending(); got != "" {
		t.Errorf("finished: pending %q, want none", got)
	}
}

// T-856: the reviewer's worktree move fails (local changes, an occupied dir): the verify failed, the ref not moved,
// the tasks pending, close's sha still the review start's; the lead told once.
func TestVerifyMoveFails(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	startedReview(t, r)
	r.git.detachErr = errors.New("git worktree add: '/x/reviewer' already exists")
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	c := ReadReviewCycle(r.p.root, "RV-5")
	if v := r.lastVerify(); v.state != verifyFailed || len(r.git.marked) != 0 || len(verifyBriefs(t, r.p.root)) != 0 {
		t.Fatalf("state %s, marked %q, %d briefs; want failed, nothing marked or briefed", v.state, r.git.marked,
			len(verifyBriefs(t, r.p.root)))
	}
	if strings.Join(c.Pending, ",") != "T-a,T-b" || c.Reviewed != "1234567890abcdef" ||
		strings.Join(c.Covered, ",") != "T-start" {
		t.Errorf("cycle %+v; want T-a,T-b pending, the start's head reviewed, only T-start covered", c)
	}
	if n := len(r.leadLines()); n != 1 {
		t.Errorf("%d lead lines %q, want the one failure line", n, r.leadLines())
	}
}

// T-856: a restart at each arrow of the lifecycle replays the same state: no second verify, no lost one.
func TestVerifyRestartEachArrow(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	startedReview(t, r)
	r.working(true)
	r.merged("T-a")
	r.merged("T-b")
	r.fixPass()
	seq := r.lastVerify().seq
	state := func() verifyState { return r.p.folded.rv.verifies[seq].state }

	r.reload() // requested →
	if state() != verifyRequested {
		t.Fatalf("requested, replayed: %s", state())
	}
	r.working(true)
	r.fixPass()
	if state() != verifyRequested || r.facts(event.StepReviewVerifyRequest) != 1 || len(r.git.detached) != 0 {
		t.Fatalf("requested, the reviewer working: %s, %d requests, detached %q", state(),
			r.facts(event.StepReviewVerifyRequest), r.git.detached)
	}

	r.working(false)
	r.refuseStep(event.StepBriefed) // admitted: moved, its brief not recorded (a crash before the append)
	r.fixPass()
	if state() != verifyRequested || len(r.git.detached) != 1 || len(verifyBriefs(t, r.p.root)) != 0 || len(r.git.marked) != 0 {
		t.Fatalf("admitted, the brief refused: %s, detached %q, %d briefs, marked %q; want requested, moved, nothing briefed or marked",
			state(), r.git.detached, len(verifyBriefs(t, r.p.root)), r.git.marked)
	}
	r.reload()
	if state() != verifyRequested {
		t.Fatalf("admitted, replayed: %s, want requested", state())
	}
	r.fixPass() // → briefed
	if state() != verifyBriefed || len(verifyBriefs(t, r.p.root)) != 1 {
		t.Fatalf("briefed: %s, %d briefs", state(), len(verifyBriefs(t, r.p.root)))
	}

	r.reload()
	r.fixPass()
	if state() != verifyBriefed || len(verifyBriefs(t, r.p.root)) != 1 || r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Fatalf("briefed, replayed: %s, %d briefs, %d requests; want briefed, one of each", state(),
			len(verifyBriefs(t, r.p.root)), r.facts(event.StepReviewVerifyRequest))
	}

	r.finish() // → done
	r.reload()
	r.fixPass()
	if state() != verifyDone || r.pending() != "" || len(verifyBriefs(t, r.p.root)) != 1 ||
		r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Errorf("done, replayed: %s, pending %q, %d briefs, %d requests; want done, none pending, one of each", state(),
			r.pending(), len(verifyBriefs(t, r.p.root)), r.facts(event.StepReviewVerifyRequest))
	}
}

// T-823, T-856: a second obor review verify is refused while the first is briefed (delivered or not); once the
// reviewer finished it a second is accepted, and each covers its tasks once its own brief is done.
func TestReviewVerifyPendingAndCoverage(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	covered := func() string { return strings.Join(ReadReviewCycle(r.p.root, "RV-5").Covered, ",") }
	if _, err := r.verifyCall("v1", "T-a"); err != nil {
		t.Fatal(err)
	}
	if got := covered(); got != "T-start" {
		t.Fatalf("precondition: %q covered while the verify brief is still queued, want the start's T-start", got)
	}
	for _, delivered := range []bool{false, true} {
		if delivered {
			r.a.deliver(context.Background(), r.p, &Loop{Typer: &linerSpy{}})
		}
		_, err := r.verifyCall("v2", "T-b")
		if err == nil || !strings.Contains(err.Error(), "a verify is pending (T-a, briefed)") {
			t.Fatalf("a second verify, the first briefed (delivered %t): %v, want the pending refusal", delivered, err)
		}
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || len(r.git.marked) != 1 || r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Errorf("after the refused verifies: %d briefs, %d marks, %d requests; want one of each", n, len(r.git.marked),
			r.facts(event.StepReviewVerifyRequest))
	}
	if got := covered(); got != "T-start" {
		t.Errorf("delivered, not finished: %q covered, want T-start", got)
	}

	r.finish()
	if _, err := r.verifyCall("v2", "T-b"); err != nil {
		t.Fatalf("a second verify after the first is done: %v", err)
	}
	if got := covered(); got != "T-start,T-a" {
		t.Errorf("covered %q; want the start's T-start and the done first verify's T-a", got)
	}
	r.finish()
	if got := covered(); got != "T-start,T-a,T-b" {
		t.Errorf("covered %q; want the start's T-start and both done verifies' T-a,T-b", got)
	}
	for _, rec := range readLog(t, r.p.root) { // the coverage is the records' (T-823)
		if rec.Task == event.ReviewTask && rec.Step == event.StepReviewVerify {
			t.Errorf("a new verify wrote a %s fact", event.StepReviewVerify)
		}
	}
}

// T-823, T-856: a refused brief append fails the call, the verify stays requested with nothing covered; after a
// restart a pass briefs it once, and the same call id gets the request's reply.
func TestReviewVerifyBriefRefused(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	r.refuseStep(event.StepBriefed)
	if _, err := r.verifyCall("v1", "T-a"); err == nil || !strings.Contains(err.Error(), "refused it") {
		t.Fatalf("a refused brief append: %v, want the verify to fail", err)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || r.lastVerify().state != verifyRequested || len(r.git.marked) != 0 {
		t.Fatalf("precondition: %d briefs, state %s, marked %q after the refused append; want none, requested, none",
			n, r.lastVerify().state, r.git.marked)
	}

	r.reload() // a restart: the refusal is not carried
	if reply, err := r.verifyCall("v1", "T-a"); err != nil || !strings.HasPrefix(reply, "VERIFY REQUESTED") {
		t.Fatalf("the same call id after a restart: %q, %v; want the request's reply", reply, err)
	}
	r.verifyPass()
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || r.facts(event.StepReviewVerifyRequest) != 1 {
		t.Fatalf("%d briefs, %d requests after the restart's passes, want one of each", n, r.facts(event.StepReviewVerifyRequest))
	}
	r.finish()
	if c := ReadReviewCycle(r.p.root, "RV-5"); strings.Join(c.Covered, ",") != "T-start,T-a" {
		t.Errorf("covered %q; want the start's T-start and the done brief's T-a", c.Covered)
	}
}

// T-856: a verify brief from a log before T-856 (no request) covers its tasks once its brief ended, not when it is
// queued, delivered or replaced; a store from before T-823 keeps its review.verify facts' coverage.
func TestReviewCoveredOldVerifyBriefs(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	if ReadReviewCycle(r.p.root, "RV-5").Started {
		t.Fatal("precondition: a start is recorded before any review ran")
	}
	covered := func() string { return strings.Join(ReadReviewCycle(r.p.root, "RV-5").Covered, ",") }
	r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: "RV-5", Head: "abcdef0123456789", Tasks: []string{"T-a", "T-b"}})
	replaced := briefFact(t, r, event.Message{To: "pj-rev", Line: "[obor lead] verify T-e", Verify: []string{"T-e"}})
	deliverFact(t, r, replaced, "sent")
	queued := briefFact(t, r, event.Message{To: "pj-rev", Line: "[obor lead] verify T-d", Verify: []string{"T-d"}})
	if got := covered(); got != "T-a,T-b" {
		t.Fatalf("a queued verify brief, one replaced: %q covered, want T-a,T-b", got)
	}
	deliverFact(t, r, queued, "sent")
	if got := covered(); got != "T-a,T-b" {
		t.Fatalf("delivered, not finished: %q covered, want T-a,T-b", got)
	}
	r.a.record(r.p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
		event.Message{To: "pj-lead", From: "obor", Ends: "pj-rev", Line: "[obor] pj-rev finished (briefed 1m ago)"})
	c := ReadReviewCycle(r.p.root, "RV-5")
	if !c.Started || c.Start != "abcdef0123456789" || strings.Join(c.Covered, ",") != "T-a,T-b,T-d" {
		t.Errorf("covered %q, start %q, started %t; want T-a,T-b,T-d at the start's head", c.Covered, c.Start, c.Started)
	}

	// An older store's fact (T-822) still covers, read in log order with the briefs'.
	r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewVerify, Kind: event.KindFact},
		ReviewData{Tasks: []string{"T-f"}})
	if got := covered(); got != "T-a,T-b,T-d,T-f" {
		t.Errorf("an old review.verify fact: %q covered, want the briefs' T-a,T-b,T-d then T-f", got)
	}
}
