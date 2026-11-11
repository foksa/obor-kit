package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// The rule's table (docs/design.md "Change rules" 6: condition → action).
func TestVerifyRule(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setup func(r *reviewRig)
		want  string // "": no action; "brief": the verify's brief; else the line's prefix
	}{
		{name: "no linked task", setup: func(r *reviewRig) { r.fixTask("T-c", "Review: on merge") }},
		{name: "a spec naming another review", setup: func(r *reviewRig) { r.fixTask("T-c", "Fixes: RV-4") }},
		{name: "a linked task open", setup: func(r *reviewRig) {
			r.fixTask("T-a", "Fixes: RV-5")
			r.fixTask("T-b", "Fixes: RV-5")
			r.merged("T-a")
		}},
		{name: "all merged, the reviewer idle", want: "brief", setup: func(r *reviewRig) {
			r.fixTask("T-a", "Fixes: RV-5")
			r.merged("T-a")
		}},
		{name: "all merged, the reviewer working", setup: func(r *reviewRig) {
			r.fixTask("T-a", "Fixes: RV-5")
			r.merged("T-a")
			r.working(true)
		}},
		{name: "all merged, the reviewer holding a brief", setup: func(r *reviewRig) {
			r.fixTask("T-a", "Fixes: RV-5")
			r.merged("T-a")
			r.leadBrief("b1")
		}},
		{name: "all merged and covered by a finished verify", setup: func(r *reviewRig) {
			r.fixTask("T-a", "Fixes: RV-5")
			r.merged("T-a")
			r.verifyPass()
			r.finish()
		}},
		{name: "a linked task dropped", want: "[obor] RV-5 fix T-b ended unmerged (dropped): not counted, the verify waits for T-a",
			setup: func(r *reviewRig) {
				r.fixTask("T-a", "Fixes: RV-5")
				r.fixTask("T-b", "Fixes: RV-5")
				r.dropped("T-b")
			}},
		{name: "a verify brief replaced", want: "[obor] RV-5 verify of T-a did not finish (a newer brief replaced it)",
			setup: func(r *reviewRig) {
				r.fixTask("T-a", "Fixes: RV-5")
				r.merged("T-a")
				r.verifyPass()
				r.leadBrief("b1")
			}},
		{name: "the review closed (a forced close)", setup: func(r *reviewRig) {
			r.fixTask("T-a", "Fixes: RV-5")
			r.merged("T-a")
			r.closedBy("RV-5", "RV-5 closed · 2222222 · approved · forced")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReviewRig(t)
			openReviewFile(t, r.p.root)
			startedReview(t, r)
			c.setup(r)
			key, act, ok := verifyRule(context.Background(), r.a, r.p, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}, r.now)
			switch {
			case c.want == "":
				if ok {
					t.Errorf("an action (key %d, line %q, call %t), want none", key, act.line, act.call != nil)
				}
			case c.want == "brief":
				if !ok || act.call == nil || act.line != "" || key == 0 {
					t.Errorf("ok %t, key %d, line %q, call %t; want the brief, no line", ok, key, act.line, act.call != nil)
				}
			default:
				if !ok || act.call != nil || !strings.HasPrefix(act.line, c.want) {
					t.Errorf("ok %t, line %q, call %t; want the line %q", ok, act.line, act.call != nil, c.want)
				}
			}
		})
	}
}

// A spec with Fixes: RV-5 dispatched while RV-5 is open is linked: the review card shows it, close is refused while it
// is open (and once merged, until a verify covers it).
func TestFixLinkedBySpec(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.fixTask("T-c", "Fixes: RV-4") // dispatch refuses it; a link of another review is no link of RV-5
	if got := r.pending(); got != "T-a,T-b" {
		t.Fatalf("pending %q, want T-a,T-b (T-c fixes another review)", got)
	}
	fixes := func() []wire.ReviewFix { return r.a.Status().Project.ReviewOpen.Fixes }
	r.verifyPass()
	if got := fixes(); len(got) != 2 || got[0] != (wire.ReviewFix{Task: "T-a", State: "open"}) ||
		got[1] != (wire.ReviewFix{Task: "T-b", State: "open"}) {
		t.Errorf("the review card's fixes %+v, want the two open links", got)
	}
	r.merged("T-a")
	r.working(true)
	r.verifyPass()
	if got, pending := fixes(), r.pending(); len(got) != 2 || got[0].State != "merged" || got[1].State != "open" ||
		pending != "T-a,T-b" {
		t.Errorf("T-a merged: the review card's fixes %+v, pending %q; want T-a merged, both pending", got, pending)
	}
}

// All linked tasks merged, the reviewer idle: one detach and one brief at the integration head, naming each task's
// merge and each task's Fixes: note; further passes brief nothing more.
func TestVerifyBriefsOnceAtTheHead(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.verifyPass()
	briefs := verifyBriefs(t, r.p.root)
	if len(briefs) != 1 || len(r.git.detached) != 1 || !strings.HasSuffix(r.git.detached[0], "@"+headSHA) {
		t.Fatalf("%d verify briefs, detached %q; want one brief, one move to %s", len(briefs), r.git.detached, headSHA)
	}
	b := briefs[0]
	if b.Task != "T-a,T-b" || b.Reviewed != headSHA || !strings.HasPrefix(b.Line, "[obor obor] The RV-5 findings were"+
		" fixed in T-a (ccccccc), T-b (ccccccc); main is at abcdef0") ||
		!strings.HasSuffix(b.Line, resultLine+" Do not commit.\nLead's note: T-a: #1, #3 — re-run with an old status JSON") {
		t.Errorf("the verify brief %+v", b)
	}
	// The verify brief itself, not only the role text, asks for the file's Result first line.
	if !strings.Contains(b.Line, "Result: approved | changes requested · <sha reviewed> · <scope>") {
		t.Errorf("the verify brief does not ask for the Result line: %q", b.Line)
	}
	if got := r.pending(); got != "T-a,T-b" { // briefed, not finished
		t.Errorf("briefed, not finished: pending %q, want T-a,T-b", got)
	}
	if l := r.verifyLines(); len(l) != 0 {
		t.Errorf("verify lines %q, want none", l)
	}
}

// The reviewer working, or holding the lead's brief: no verify brief; once idle with no open brief: briefed.
func TestVerifyWaitsForTheReviewer(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.working(true)
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || len(r.git.detached) != 0 {
		t.Fatalf("the reviewer working: %d briefs, detached %q; want none", n, r.git.detached)
	}
	r.working(false)
	r.leadBrief("b1")
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || len(r.git.detached) != 0 {
		t.Fatalf("the reviewer holding a brief: %d briefs, detached %q; want none", n, r.git.detached)
	}
	r.finish()
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || len(r.git.detached) != 1 {
		t.Errorf("the reviewer idle: %d briefs, detached %q; want one of each", n, r.git.detached)
	}
}

// A lead's brief replaces the verify's open brief: one line, no brief while the lead's is open, the verify briefed again
// on the next idle pass; nothing covered and close's head the start's until the second one finishes.
func TestVerifyAgainAfterANewerBrief(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.leadBrief("b1")
	r.verifyPass()
	r.verifyPass()
	if n, c := len(verifyBriefs(t, r.p.root)), r.cycle(); n != 1 || c.reviewed != startHead ||
		strings.Join(c.pending, ",") != "T-a,T-b" {
		t.Fatalf("replaced: %d verify briefs, cycle %+v; want one, T-a,T-b pending at the start's head", n, c)
	}
	r.finish() // the lead's brief
	r.verifyPass()
	r.verifyPass()
	want := "[obor] RV-5 verify of T-a,T-b did not finish (a newer brief replaced it): serve briefs it again once the" +
		" reviewer is idle"
	if n, l := len(verifyBriefs(t, r.p.root)), r.verifyLines(); n != 2 || len(l) != 1 || l[0] != want {
		t.Fatalf("the reviewer idle again: %d verify briefs, lines %q; want two, the one line %q", n, l, want)
	}
	r.finish()
	if c := r.cycle(); len(c.pending) != 0 || c.reviewed != headSHA || strings.Join(c.covered, ",") != "T-start,T-a,T-b" {
		t.Errorf("the second verify finished: cycle %+v; want nothing pending, its head, T-a,T-b covered", c)
	}
}

// The reviewer stops on an error that is not a usage limit: the verify did not finish (one line), the next idle pass
// briefs it again; after a restart nothing is lined or briefed twice.
func TestVerifyAgainAfterAStop(t *testing.T) {
	t.Parallel()
	r := briefedVerify(t)
	r.stop("s1", "overloaded", "API overloaded")
	if r.p.out.Brief("pj-rev") != nil {
		t.Fatal("precondition: the error stop left the reviewer's brief open")
	}
	r.verifyPass() // the line
	r.reload()
	r.working(false)
	r.verifyPass() // the brief
	r.verifyPass()
	want := "[obor] RV-5 verify of T-a,T-b did not finish (the reviewer stopped: overloaded): serve briefs it again once" +
		" the reviewer is idle"
	if n, l := len(verifyBriefs(t, r.p.root)), r.verifyLines(); n != 2 || len(l) != 1 || l[0] != want {
		t.Errorf("after the stop: %d verify briefs, lines %q; want two, the one line %q", n, l, want)
	}
}

// A usage limit keeps the verify's brief open (as for any brief): no line, no second brief.
func TestVerifyUsageLimitKeepsTheBrief(t *testing.T) {
	t.Parallel()
	r := briefedVerify(t)
	r.stop("s1", "usage_limit_exceeded", "You've hit your usage limit. Try again at 6:30 PM.")
	r.working(false)
	r.verifyPass()
	r.verifyPass()
	if b := r.p.out.Brief("pj-rev"); b == nil || b.Stop() == nil {
		t.Fatalf("the reviewer's brief %+v, want it open and stopped", b)
	}
	if n, l := len(verifyBriefs(t, r.p.root)), r.verifyLines(); n != 1 || len(l) != 0 || r.pending() != "T-a,T-b" {
		t.Errorf("%d verify briefs, lines %q, pending %q; want one, none, T-a,T-b", n, l, r.pending())
	}
}

// The reviewer's worktree move fails: one line, retried on the next pass (no second line for the same failure), briefed
// once it moves.
func TestVerifyGitFailsRetried(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.git.detachErr = errors.New("checkout: your local changes would be overwritten")
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.verifyPass()
	want := "[obor] RV-5 verify of T-a,T-b failed: checkout: your local changes would be overwritten (serve tries again" +
		" on its next pass)"
	if l, tries := r.verifyLines(), r.git.headReads; len(l) != 1 || l[0] != want || tries != 2 {
		t.Fatalf("lines %q after %d tries; want the one line %q, two tries", l, tries, want)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || r.pending() != "T-a,T-b" {
		t.Fatalf("%d verify briefs, pending %q; want none, T-a,T-b", n, r.pending())
	}
	r.git.detachErr = nil
	r.verifyPass()
	if n, l := len(verifyBriefs(t, r.p.root)), r.verifyLines(); n != 1 || len(l) != 1 {
		t.Errorf("the move works: %d verify briefs, lines %q; want one brief, still one line", n, l)
	}
}

// A linked task dropped: one line (not again on later passes or after a restart), not pending; the rest merged, the
// verify names only it; close is not refused by the dropped one.
func TestVerifyLinkDropped(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.dropped("T-b")
	r.verifyPass()
	r.verifyPass()
	r.reload()
	r.verifyPass()
	want := "[obor] RV-5 fix T-b ended unmerged (dropped): not counted, the verify waits for T-a"
	if l := r.verifyLines(); len(l) != 1 || l[0] != want {
		t.Fatalf("lines %q, want the one line %q", l, want)
	}
	if got := r.pending(); got != "T-a" {
		t.Fatalf("pending %q, want T-a (T-b dropped)", got)
	}
	r.merged("T-a")
	r.verifyPass()
	if b := verifyBriefs(t, r.p.root); len(b) != 1 || b[0].Task != "T-a" {
		t.Fatalf("verify briefs %+v, want one naming T-a", b)
	}
	r.finish()
	if c := r.cycle(); len(c.pending) != 0 || strings.Join(c.covered, ",") != "T-start,T-a" {
		t.Errorf("cycle %+v; want nothing pending, T-a covered", c)
	}
}

// The reviewer's finished line covers the verify's tasks: close's pending set empties, its head is the verify's, and no
// verify is briefed again.
func TestVerifyFinishedCovers(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.finish()
	r.verifyPass()
	c := r.cycle()
	if len(c.pending) != 0 || strings.Join(c.covered, ",") != "T-start,T-a,T-b" || c.reviewed != headSHA ||
		c.start != startHead {
		t.Errorf("cycle %+v; want nothing pending, T-a,T-b covered, the verify's head, the start's start", c)
	}
	if n := len(verifyBriefs(t, r.p.root)); n != 1 {
		t.Errorf("%d verify briefs after the finished verify, want one", n)
	}
	r.verifyPass()
	if got := r.a.Status().Project.ReviewOpen.Fixes; len(got) != 2 || got[0].State != "verified" || got[1].State != "verified" {
		t.Errorf("the review card's fixes %+v, want both verified", got)
	}
}

// A crash between the detach and the brief's record (the append refused): after a restart the verify is briefed once
// (the rule's failure mode: a doubled brief is allowed, a lost one is not; the move is idempotent).
func TestVerifyCrashBetweenDetachAndBrief(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.refuseStep(event.StepBriefed)
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 0 || len(r.git.detached) != 1 {
		t.Fatalf("precondition: %d verify briefs, detached %q; want moved, nothing briefed", n, r.git.detached)
	}
	r.reload()
	r.verifyPass()
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || len(r.git.detached) != 2 {
		t.Errorf("after the restart: %d verify briefs, detached %q; want one brief, moved again", n, r.git.detached)
	}
}

// The reviewer's finished line names its review file and the file's Result line; other roles keep
// "[obor] <agent> finished (briefed <age> ago)" (messages_test.go TestBriefFinishedLineToTheLead and the advisor's).

// finishedLines are the log's finished lines (messages that end an agent) of agent, in order.
func finishedLines(t *testing.T, root, agent string) []string {
	t.Helper()
	var out []string
	for _, rec := range readLog(t, root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Ends == agent {
			out = append(out, m.Line)
		}
	}
	return out
}

// writeReview writes reviews/name.md in the rig's main checkout.
func writeReview(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reviews", name+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewerFinishedLineNamesReviewAndResult(t *testing.T) {
	t.Parallel()
	t.Run("a review file with a Result line", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		writeReview(t, r.p.root, "RV-5", "# RV-5\n\nResult: **changes requested** · 2b3c4d5 · T-a,T-b\n\n## Findings\n")
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5.md — changes requested · 2b3c4d5 · T-a,T-b"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("a verify file", func(t *testing.T) {
		t.Parallel()
		r := fixRig(t)
		r.merged("T-a")
		r.merged("T-b")
		r.verifyPass()
		if n := len(verifyBriefs(t, r.p.root)); n != 1 {
			t.Fatalf("precondition: %d verify briefs, want 1", n)
		}
		writeReview(t, r.p.root, "RV-5-verify", "# RV-5 verify\n\n**Result:** approved · all fixed\n")
		r.finish()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5-verify.md — approved · all fixed"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("a verify file with Result mid-sentence", func(t *testing.T) {
		t.Parallel()
		r := fixRig(t)
		r.merged("T-a")
		r.merged("T-b")
		r.verifyPass()
		if n := len(verifyBriefs(t, r.p.root)); n != 1 {
			t.Fatalf("precondition: %d verify briefs, want 1", n)
		}
		writeReview(t, r.p.root, "RV-5-verify", "# RV-5 verify\n\nReviewed `c579437`: T-944 fixed both found"+
			" issues; Result: **changes requested**; #1 is incomplete, #2 is fixed.\n")
		r.finish()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5-verify.md — changes requested; #1 is incomplete, #2 is fixed."
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("no file", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5.md (no Result line)"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("an unrelated brief", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		r.started()       // the review's own brief: its finished line names RV-5
		r.leadBrief("b1") // an unrelated brief to the reviewer, after the review's own
		r.finish()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished (briefed 0s ago)"
		if len(got) != 2 || !strings.HasPrefix(got[0], "[obor] pj-rev finished RV-5:") || got[1] != want {
			t.Errorf("finished lines %q, want the review's, then the unrelated brief's %q", got, want)
		}
	})
	t.Run("a file with no Result line", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		writeReview(t, r.p.root, "RV-5", "# RV-5\n\nno result yet\n")
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5.md (no Result line)"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("a long result is cut", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		writeReview(t, r.p.root, "RV-5", "Result: **approved** "+strings.Repeat("x", 300)+"\n")
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		if len(got) != 1 {
			t.Fatalf("finished lines %q, want one", got)
		}
		_, res, ok := strings.Cut(got[0], " — ")
		if !ok || !strings.HasPrefix(res, "approved xxx") || len([]rune(res)) > resultWidth+1 {
			t.Errorf("result %q (%d runes), want it cut to about %d", res, len([]rune(res)), resultWidth)
		}
	})
}
