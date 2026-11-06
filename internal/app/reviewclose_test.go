package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-926: obor review close is a call to serve: serve checks the open review against its fold, commits the close in the
// main checkout (ReviewGit.Close, admitted) and records fact review.closed, which every "is a review open" reads.

// closedFacts is the log's review.closed facts' data, in order.
func (r *reviewRig) closedFacts() []ReviewData {
	r.t.Helper()
	var out []ReviewData
	for _, rec := range readLog(r.t, r.p.root) {
		var d ReviewData
		if rec.Task == event.ReviewTask && rec.Step == event.StepReviewClosed && json.Unmarshal(rec.Data, &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

// commits is how many closes the main checkout committed.
func (r *reviewRig) commits() int {
	r.git.mu.Lock()
	defer r.git.mu.Unlock()
	return len(r.git.closes)
}

// The close's refusals, each before any change (no commit, no fact): no open review (no review file), no review start
// for it in the log, the main checkout's own refusal (not on the branch, dirty outside the allowed files: its message
// passed on), a linked fix pending; --force closes with the fix pending.
func TestReviewCloseRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setup func(r *reviewRig)
		want  string // the refusal's start
	}{
		{"no open review: no review file", func(r *reviewRig) { r.started() },
			"no open review: no reviews/RV-5.md in "},
		{"no review start in the log", func(r *reviewRig) { openReviewFile(r.t, r.p.root) },
			"no review start for RV-5 in the log: nothing changed"},
		{"not on the branch", func(r *reviewRig) {
			r.started()
			openReviewFile(r.t, r.p.root)
			r.git.closeErr = errors.New("the main checkout /x is not on main: nothing changed")
		}, "the main checkout /x is not on main: nothing changed"},
		{"dirty outside the allowed files", func(r *reviewRig) {
			r.started()
			openReviewFile(r.t, r.p.root)
			r.git.closeErr = errors.New("the main checkout is dirty outside BACKLOG.md and RV-5's files (?? x): nothing changed")
		}, "the main checkout is dirty outside BACKLOG.md and RV-5's files (?? x): nothing changed"},
		{"a linked fix pending", func(r *reviewRig) {
			openReviewFile(r.t, r.p.root)
			startedReview(r.t, r)
			r.fixTask("T-a", "Fixes: RV-5")
		}, "RV-5's fixes are not verified: T-a (open, or merged and its verify not finished; --force closes anyway): nothing changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReviewRig(t)
			c.setup(r)
			_, err := r.close("c1", "fixed", false)
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("close: %v; want %q", err, c.want)
			}
			if n, f := r.commits(), r.closedFacts(); n != 0 || len(f) != 0 {
				t.Errorf("a refused close: %d commits, facts %+v; want none", n, f)
			}
			if c.name != "a linked fix pending" {
				return
			}
			reply, err := r.close("c2", "forced", true)
			if err != nil || !strings.HasPrefix(reply, "CLOSED RV-5 closed · 1234567 · approved · forced") {
				t.Errorf("--force: %q, %v; want CLOSED", reply, err)
			}
			if r.commits() != 1 || len(r.closedFacts()) != 1 || r.p.folded.reviewed.open() != "" {
				t.Errorf("--force: %d commits, %d facts, open %q; want one, one, none", r.commits(), len(r.closedFacts()),
					r.p.folded.reviewed.open())
			}
		})
	}
}

// A close of a verified fix round: the main checkout commits the subject and body as before (the verify's head, the
// cycle's covered tasks and its start; accepted 1 for one --accept line), the review file and the accepted line handed
// to it; fact review.closed{id, sha, covered, start} recorded; a repeated call id gets the reply and commits nothing
// again. The next rule pass stops the reviewer (no git read) and obor status shows no fix line of the closed review
// (RV-125 #2).
func TestReviewCloseCommitsAndRecords(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.finish()
	r.verifyPass()
	if got := r.a.Status().Project.Review; !strings.Contains(got, "RV-5 fixes: T-a verified · T-b verified") {
		t.Fatalf("precondition: status %q, want the verified fix round", got)
	}
	reply, err := r.close("c1", "fixed", false, "rare case")
	subject := "RV-5 closed · abcdef0 · accepted 1 · fixed"
	if err != nil || reply != "CLOSED "+subject {
		t.Fatalf("close: %q, %v", reply, err)
	}
	want := ReviewCommit{Branch: "main", ID: "RV-5", File: r.p.root + "/reviews/RV-5.md", Reviewed: headSHA,
		Subject: subject, Body: "Covered: T-start, T-a, T-b\nStart: " + startHead, Accepts: []string{"rare case"}}
	if r.commits() != 1 || !commitEqual(r.git.closes[0], want) {
		t.Errorf("committed %+v, want %+v", r.git.closes, want)
	}
	f := r.closedFacts()
	if len(f) != 1 || f[0].ID != "RV-5" || f[0].Reply != "CLOSED "+subject {
		t.Errorf("review.closed facts %+v", f)
	}
	if r.p.folded.reviewed.open() != "" || r.p.folded.reviewed.closed == 0 {
		t.Errorf("the fold %+v, want RV-5 closed", r.p.folded.reviewed)
	}
	if again, err := r.close("c1", "fixed", false, "rare case"); err != nil || again != reply || r.commits() != 1 {
		t.Errorf("the repeated call: %q, %v, %d commits; want the stored reply, one commit", again, err, r.commits())
	}

	s := &reviewStopSpy{}
	reads := r.git.reads()
	r.a.rulePass(context.Background(), rules, &Loop{Review: r.git, Agents: s})
	if !slices.Equal(s.stops, []string{"pj-rev w:rev force=false"}) {
		t.Errorf("stops %q after the close, want the reviewer stopped on the next pass", s.stops)
	}
	if n := r.git.reads() - reads; n != 0 {
		t.Errorf("the rule pass read git %d times, want none", n)
	}
	if got := r.a.Status().Project.Review; strings.Contains(got, "fixes") {
		t.Errorf("status %q after the close, want no fix line (RV-125 #2)", got)
	}
}

// commitEqual: a and b are the same close.
func commitEqual(a, b ReviewCommit) bool {
	return a.Branch == b.Branch && a.ID == b.ID && a.File == b.File && a.Reviewed == b.Reviewed && a.Subject == b.Subject &&
		a.Body == b.Body && slices.Equal(a.Accepts, b.Accepts)
}

// A crash between the close's commit and its fact (here: the log refuses the fact): the call says so; after a restart
// the first pass's git read records the close once (a second read, and a second restart, add none), and the next
// obor review starts RV-6 with the scope the close commit names (its start, its covered tasks left out).
func TestReviewCloseCrashBeforeFact(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.started()
	openReviewFile(t, r.p.root)
	r.refuseStep(event.StepReviewClosed)
	if _, err := r.close("c1", "fixed", false); err == nil || !strings.Contains(err.Error(), "committed RV-5, but the event log") {
		t.Fatalf("close with the fact refused: %v", err)
	}
	if r.commits() != 1 || len(r.closedFacts()) != 0 {
		t.Fatalf("precondition: %d commits, %d facts; want the commit, no fact", r.commits(), len(r.closedFacts()))
	}
	read := func() { r.a.reviewCloseRead(context.Background(), r.p, &Loop{Review: r.git, Agents: r.agents}) }
	for i := range 2 {
		r.reload()
		if i == 0 && r.p.folded.reviewed.open() != "RV-5" {
			t.Fatalf("precondition: RV-5 not open after the restart: %+v", r.p.folded.reviewed)
		}
		read()
		read()
		if f := r.closedFacts(); len(f) != 1 || f[0].ID != "RV-5" {
			t.Fatalf("restart %d: review.closed facts %+v, want RV-5's once, from its commit", i+1, f)
		}
		if r.p.folded.reviewed.open() != "" {
			t.Fatalf("restart %d: RV-5 still open", i+1)
		}
	}
	if got := r.mustCall("r2", nil, "", false); got != "REVIEW REQUESTED (now)" {
		t.Fatalf("obor review after the close: %q", got)
	}
	r.pass()
	if l := r.agents.launches; len(l) != 2 || !strings.HasPrefix(l[1].Prompt, "Review RV-6. ") ||
		!strings.Contains(l[1].Prompt, "In scope: no merges;") || !strings.Contains(l[1].Prompt, "Previous review: RV-5 (") {
		t.Errorf("launches %+v; want RV-6, its covered tasks out of scope", l)
	}
	if from := r.git.from[len(r.git.from)-1]; from != headSHA {
		t.Errorf("RV-6's scope from %q, want RV-5's start %s", from, headSHA)
	}
}

// An old store (T-926's upgrade): RV-5 started and closed by an older obor's commit, no fact. After the restart the
// fold alone has it open; the merge loop's first pass reads the commit once and records the close, so no review
// counts open and obor review starts RV-6.
func TestReviewCloseOldStore(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.started()
	r.git.last, r.git.lastBody = "RV-5 closed · abcdef0 · approved · T-a", "Covered: T-a, T-b\nStart: "+headSHA
	r.reload()
	if r.p.folded.reviewed.open() != "RV-5" || len(r.closedFacts()) != 0 {
		t.Fatalf("precondition: the old store's fold has RV-5 open, no close fact: %+v", r.p.folded.reviewed)
	}
	if _, err := r.call("r2", nil, "", false); err == nil || !strings.Contains(err.Error(), "RV-5 is open") {
		t.Fatalf("precondition: before the first pass RV-5 counts open: %v", err)
	}
	r.a.advance(context.Background())
	if f := r.closedFacts(); len(f) != 1 || r.p.folded.reviewed.open() != "" {
		t.Fatalf("after the first pass: facts %+v, open %q; want RV-5 closed once", f, r.p.folded.reviewed.open())
	}
	r.a.advance(context.Background())
	if n := len(r.closedFacts()); n != 1 {
		t.Errorf("%d close facts after a second pass, want one", n)
	}
	if got := r.mustCall("r3", nil, "", false); got != "REVIEW REQUESTED (now)" {
		t.Fatalf("obor review after the upgrade: %q", got)
	}
	r.pass()
	if l := r.agents.launches; len(l) != 2 || !strings.HasPrefix(l[1].Prompt, "Review RV-6. ") {
		t.Errorf("launches %+v; want RV-6", l)
	}
}

// RV-126 #1: only the lead closes a review (the owner's call has no role): a reviewer or a worker is refused, plain and
// with --force, before any effect (no commit, no fact, the review still open); the lead's close goes through.
func TestReviewCloseLeadOnly(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.finish()
	r.verifyPass()
	if r.p.folded.reviewed.open() != "RV-5" {
		t.Fatalf("precondition: %+v, want RV-5 open", r.p.folded.reviewed)
	}
	for _, role := range []string{"reviewer", "worker"} {
		for _, force := range []bool{false, true} {
			_, err := r.a.reviewCloseCall(wire.Call{Root: r.p.root, CallID: "x-" + role, Role: role, Agent: "pj-" + role,
				Note: "fixed", Force: force})
			if want := "review close: only the lead closes a review (" + role + ")"; err == nil || err.Error() != want {
				t.Errorf("%s force=%v: %v, want %q", role, force, err, want)
			}
		}
	}
	if r.commits() != 0 || len(r.closedFacts()) != 0 || r.p.folded.reviewed.open() != "RV-5" {
		t.Fatalf("a refused close changed something: %d commits, %d facts, open %q", r.commits(), len(r.closedFacts()),
			r.p.folded.reviewed.open())
	}
	if reply, err := r.close("c1", "fixed", false); err != nil || !strings.HasPrefix(reply, "CLOSED RV-5 closed") {
		t.Fatalf("the lead's close: %q, %v", reply, err)
	}
}
