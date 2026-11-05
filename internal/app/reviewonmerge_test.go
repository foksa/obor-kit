package app

import (
	"context"
	"errors"
	"testing"

	"obor/internal/app/event"
	"obor/internal/store"
)

// T-878: the Review: on merge rule, one table (condition → action). A task whose spec has the line and that merged
// after the last review start gets one review request (no --after, the note naming the newest such task); an open
// request (the lead's included) or a review started after the merge leaves nothing to do; a request recorded while a
// cycle is open waits for its close; two line tasks merged before the pass are one request; a request that ended after
// the merge (the lead's --rm, or a failed start's fire) holds the rule.
func TestReviewOnMergeRule(t *testing.T) {
	t.Parallel()
	type step func(t *testing.T, r *reviewRig)
	merge := func(task string, onMerge bool) step {
		return func(t *testing.T, r *reviewRig) {
			t.Helper()
			text := "# " + task + " — A\n\nDifficulty: medium\n\n## Do\n"
			if onMerge {
				text = "# " + task + " — A\n\nDifficulty: medium\nReview: on merge\n\n## Do\n"
			}
			if _, _, err := store.SetSpec(r.p.log.DB(), task, text, "", "lead", r.now); err != nil {
				t.Fatal(err)
			}
			r.submitted(task)
			r.merged(task)
			if mt := r.p.folded.loop[task]; mt == nil || mt.state != stMerged || mt.mergedSeq == 0 {
				t.Fatalf("precondition: %s not merged: %+v", task, mt)
			}
		}
	}
	rule := func(t *testing.T, r *reviewRig) {
		t.Helper()
		if !r.p.merging {
			t.Fatal("precondition: the project does not merge")
		}
		r.a.rulePass(context.Background(), []rule{{name: ruleReviewOnMerge, when: reviewOnMergeRule}}, &Loop{})
	}
	waiting := func(t *testing.T, r *reviewRig) { // the lead's request: its --after task has not merged
		r.submitted("T-b")
		r.mustCall("r1", []string{"T-b"}, "", false)
		if q := r.p.folded.review; q == nil || len(q.after) != 1 {
			t.Fatalf("precondition: no waiting request: %+v", q)
		}
	}
	started := func(t *testing.T, r *reviewRig) { r.started() }
	pass := func(_ *testing.T, r *reviewRig) { r.pass() }
	cancel := func(t *testing.T, r *reviewRig) { // the lead's obor review --rm ends the rule's request
		t.Helper()
		if r.p.folded.review == nil {
			t.Fatal("precondition: no request to remove")
		}
		r.mustCall("rm1", nil, "", true)
		if r.p.folded.review != nil {
			t.Fatal("precondition: the request is still open")
		}
	}
	failStart := func(t *testing.T, r *reviewRig) { // a pass fires the rule's request, then the start fails at the detach
		t.Helper()
		if r.p.folded.review == nil {
			t.Fatal("precondition: no request to fire")
		}
		r.git.mu.Lock()
		r.git.detachErr = errors.New("worktree busy")
		r.git.mu.Unlock()
		r.pass()
		if r.p.folded.review != nil {
			t.Fatal("precondition: the request is still open")
		}
		if got := len(r.needs()); got != 1 {
			t.Fatalf("precondition: %d review needs-you after the failed start, want 1 (%v)", got, r.needs())
		}
	}
	cases := []struct {
		name     string
		steps    []step
		requests int    // review.request facts in the log
		note     string // the open request's note ("" when none)
		held     string // the open request's held review ("" when none)
		needs    bool   // a review needs-you is in force
	}{
		{"no line: nothing", []step{merge("T-a", false), rule, rule}, 0, "", "", false},
		{"line, merged, no request: the request", []step{merge("T-a", true), rule, rule}, 1,
			"Review: on merge (T-a)", "", false},
		{"a request already open: nothing", []step{waiting, merge("T-a", true), rule}, 1, "", "", false},
		{"a review started after the merge: nothing", []step{merge("T-a", true), started, rule}, 1, "", "", false},
		{"a cycle still open: the request waits", []step{started, merge("T-a", true), rule, pass}, 2,
			"Review: on merge (T-a)", "RV-5", false},
		{"two line tasks merged: one request", []step{merge("T-a", true), merge("T-b", true), rule}, 1,
			"Review: on merge (T-b)", "", false},
		{"the lead's --rm: no new request", []step{merge("T-a", true), rule, cancel, rule, rule}, 1, "", "", false},
		{"a failed start: no retry while its needs-you is in force",
			[]step{merge("T-a", true), rule, failStart, rule, rule}, 1, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReviewRig(t)
			for _, s := range c.steps {
				s(t, r)
			}
			if got := r.facts(event.StepReviewRequest); got != c.requests {
				t.Errorf("review.request facts %d, want %d", got, c.requests)
			}
			q := r.p.folded.review
			switch {
			case c.note == "" && q != nil && q.note != "":
				t.Errorf("the rule recorded a request (%q), want none", q.note)
			case c.note != "" && q == nil:
				t.Errorf("no open request, want the rule's %q", c.note)
			case c.note != "" && q.note != c.note:
				t.Errorf("request note %q, want %q", q.note, c.note)
			}
			if c.note != "" {
				if q.by != "obor" || len(q.after) != 0 {
					t.Errorf("request by %q, after %q; want obor, no --after", q.by, q.after)
				}
				if q.held != c.held {
					t.Errorf("request held %q, want %q", q.held, c.held)
				}
			}
			if got := len(r.needs()); (got > 0) != c.needs {
				t.Errorf("review needs-you in force %d (%v), want %v", got, r.needs(), c.needs)
			}
		})
	}
}
