package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// T-878, T-932: the Review: on merge rule, one table (condition → action). A task whose spec asks for a review and
// merged after the last review start gets one review request (no --after, the note naming the newest such task); the
// spec asks for it with the line `Review: on merge` or with `Difficulty: hard`, and never when it has a `Fixes:` line;
// an open request (the lead's included) or a review started after the merge leaves nothing to do; a request recorded
// while a cycle is open waits for its close; two line tasks merged before the pass are one request; a request that
// ended after the merge (the lead's --rm, or a failed start's fire) holds the rule.
func TestReviewOnMergeRule(t *testing.T) {
	t.Parallel()
	const (
		medium     = "Difficulty: medium\n"
		mediumLine = "Difficulty: medium\nReview: on merge\n"
		hard       = "Difficulty: hard\n"
		hardFix    = "Difficulty: hard\nFixes: RV-12\n"
	)
	type step func(t *testing.T, r *reviewRig)
	merge := func(task, head string) step {
		return func(t *testing.T, r *reviewRig) {
			t.Helper()
			text := "# " + task + " — A\n\n" + head + "\n## Do\n"
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
	// The --after task T-b ending unmerged, the request with the lead's line: pass folds the failure end.
	ended := func(t *testing.T, r *reviewRig, line string) {
		t.Helper()
		r.pass()
		r.pass()
		if r.p.folded.review != nil {
			t.Fatal("precondition: the request is still open")
		}
		if lines := r.leadLines(); len(lines) != 1 || lines[0] != line {
			t.Fatalf("precondition: lead lines %q, want %q", lines, line)
		}
	}
	droppedB := func(t *testing.T, r *reviewRig) {
		r.dropped("T-b")
		ended(t, r, "[obor] review request ended: T-b dropped")
	}
	// rejectedB walks T-b to the integrator's rejection of its review job (T-906: the same failure end).
	rejectedB := func(t *testing.T, r *reviewRig) {
		t.Helper()
		r.a.record(r.p, event.Record{Task: "T-b", Attempt: 1, Step: event.StepGate, Kind: event.KindResult},
			gateOut{Outcome: outGreen, Candidate: port.Candidate{SHA: "cand", Patch: "patch"}})
		r.a.record(r.p, event.Record{Task: "T-b", Attempt: 1, Step: event.StepReview, Kind: event.KindIntent}, JobData{Patch: "patch"})
		if mt := r.p.folded.loop["T-b"]; mt == nil || mt.state != stAwaiting {
			t.Fatalf("precondition: T-b not awaiting its review: %+v", mt)
		}
		r.a.record(r.p, event.Record{Task: "T-b", Attempt: 1, Step: verdictStep(wire.MethodReject, "j1"), Kind: event.KindResult},
			verdictResult{Job: JobID(event.Key{Task: "T-b", Attempt: 1, Step: event.StepReview}), Note: "not this"})
		if mt := r.p.folded.loop["T-b"]; mt == nil || mt.state != stRejected {
			t.Fatalf("precondition: T-b not rejected: %+v", mt)
		}
		ended(t, r, "[obor] review request ended: T-b rejected")
	}
	started := func(t *testing.T, r *reviewRig) { r.started() }
	pass := func(_ *testing.T, r *reviewRig) { r.pass() }
	closeCommit := func(t *testing.T, r *reviewRig) { // a close commit alone: the request still waits (no git read)
		t.Helper()
		r.git.mu.Lock()
		r.git.last = "RV-5 closed · abcdef0 · approved · T-a"
		r.git.mu.Unlock()
	}
	closeFact := func(t *testing.T, r *reviewRig) {
		t.Helper()
		if q := r.p.folded.review; q == nil || q.held != "RV-5" || len(r.agents.launches) != 1 {
			t.Fatalf("precondition: the rule's request is not waiting on RV-5 after its close commit: %+v, %d launches",
				q, len(r.agents.launches))
		}
		r.closedBy("RV-5 closed · abcdef0 · approved · T-a")
	}
	startsRV6 := func(t *testing.T, r *reviewRig) {
		t.Helper()
		if l := r.agents.launches; len(l) != 2 || !strings.HasPrefix(l[1].Prompt, "Review RV-6. ") ||
			!strings.HasSuffix(l[1].Prompt, "\nLead's note: Review: on merge (T-a)") {
			t.Errorf("launches %+v; want RV-6 started on the rule's request", l)
		}
	}
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
		{"medium, no line: nothing", []step{merge("T-a", medium), rule, rule}, 0, "", "", false},
		{"medium, line, merged, no request: the request", []step{merge("T-a", mediumLine), rule, rule}, 1,
			"Review: on merge (T-a)", "", false},
		{"hard, no line: the request (T-932)", []step{merge("T-a", hard), rule, rule}, 1,
			"Review: on merge (T-a)", "", false},
		{"hard with Fixes: nothing (T-932)", []step{merge("T-a", hardFix), rule, rule}, 0, "", "", false},
		{"a request already open: nothing", []step{waiting, merge("T-a", mediumLine), rule}, 1, "", "", false},
		{"a review started after the merge: nothing", []step{merge("T-a", mediumLine), started, rule}, 1, "", "", false},
		{"a cycle still open: the request waits", []step{started, merge("T-a", mediumLine), rule, pass}, 2,
			"Review: on merge (T-a)", "RV-5", false},
		{"a cycle still open: the request starts after the close fact (T-926)",
			[]step{started, merge("T-a", mediumLine), rule, pass, closeCommit, pass, closeFact, pass, startsRV6}, 2, "", "", false},
		{"two line tasks merged: one request",
			[]step{merge("T-a", mediumLine), merge("T-b", mediumLine), rule}, 1,
			"Review: on merge (T-b)", "", false},
		{"the lead's --rm: no new request", []step{merge("T-a", mediumLine), rule, cancel, rule, rule}, 1, "", "", false},
		{"--after task dropped: the rule requests the merge",
			[]step{waiting, merge("T-a", mediumLine), rule, droppedB, rule, rule}, 2, "Review: on merge (T-a)", "", false},
		{"--after task rejected: the rule requests the merge",
			[]step{waiting, merge("T-a", mediumLine), rule, rejectedB, rule, rule}, 2, "Review: on merge (T-a)", "", false},
		{"a failed start: no retry while its needs-you is in force",
			[]step{merge("T-a", mediumLine), rule, failStart, rule, rule}, 1, "", "", true},
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
			if c.note != "" && q != nil {
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
