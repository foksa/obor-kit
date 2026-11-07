package app_test

import (
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// T-973 (RV-134 verify #1): a conflict resolved before any accept is not the task's review: the resolve job carries
// only the conflict, so its resolved candidate is gated, tested once in the lane, and then reviewed before it
// publishes. A red lane test sends back; a moved conflict keeps its earlier accept (row 20).

// resolve is the integrator's resolution of T-1's first-gate conflict (r.conflicting) committed in the scratch, and
// its resolved verdict: the resolution's sha.
func resolve(t *testing.T, r *loopRig, callID string) string {
	t.Helper()
	r.waitJob("pj-int", "T-1/1/resolve")
	sha := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, callID, "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	return sha
}

// A first-gate conflict's resolved candidate is tested in the lane and then reviewed once (T-973): the review job
// carries the stored spec, and its accept publishes the resolution.
func TestLoopResolvedFirstConflictReviewsThenMerges(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.conflicting()
	r.start()
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	r.submit()
	resolution := resolve(t, r, "c1")
	job := r.waitJob("pj-int", "T-1/1/review")
	if !strings.Contains(job, "Do the thing.") {
		t.Errorf("the review job carries no rev 1 spec:\n%s", job)
	}
	for _, s := range []string{"1 result gate.2", "1 intent test", "1 result test"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish", "1 fact merged"} {
		if r.has(s) {
			t.Errorf("the resolution published before its review (%q): %q", s, r.steps())
		}
	}
	r.accept("c2", r.patch(job))
	r.merged(1)
	if n := r.count("1 intent review"); n != 1 || r.mainMerges() != 1 || r.published().SHA != resolution {
		t.Errorf("review jobs %d, merges %d; want one review and one merge of the resolution: %q", n, r.mainMerges(), r.steps())
	}
	r.noLead()
}

// The resolved candidate's lane test is red: the task goes back to its worker with the red line, as any red test; no
// review job and no publish.
func TestLoopResolvedFirstConflictRedSendsBack(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.conflicting()
	r.scratch.Script("Test", "T-1", port.OutcomeRed) // the first lane test is the resolution's
	r.start()
	r.submit()
	resolve(t, r, "c1")
	job := r.waitJob("pj-l-01", "T-1/2/fix")
	if !strings.Contains(job, "RED check") {
		t.Errorf("the fix job names no red lane test:\n%s", job)
	}
	for _, s := range []string{"1 result gate.2", "1 intent test", "1 result test", "1 fact sendback"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish", "1 fact merged", "1 intent review"} {
		if r.has(s) {
			t.Errorf("a red lane test after the resolution ran %q: %q", s, r.steps())
		}
	}
	if n, p := r.mainMerges(), r.scratch.Publishes.Load(); n != 0 || p != 0 {
		t.Errorf("merges %d, publishes %d; want none", n, p)
	}
	r.noLead()
}

// The lead sets a new spec rev after the resolution: the review job that follows the resolved candidate's lane test
// carries that rev, and its accept publishes the task. A low disk holds the resolved gate off the scratch until the rev
// is set, so the case is not a race.
func TestLoopResolvedFirstConflictNewSpecRevReviews(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	var free atomic.Uint64
	free.Store(50 << 30)
	r.disk = &app.Disk{Volume: "/vol/pj", Free: func() (uint64, error) { return free.Load(), nil }}
	r.conflicting()
	r.start()
	r.submit()
	r.waitJob("pj-int", "T-1/1/resolve")
	resolution := r.resolveInScratch()
	free.Store(1 << 30) // the resolved gate would only fail on a full disk: it does not start
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	if n := r.scratch.Gates.Load(); n != 1 {
		t.Fatalf("precondition: %d gates before the spec rev, want the conflict's one", n)
	}
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	free.Store(50 << 30)
	job := r.waitJob("pj-int", "T-1/1/review")
	if !strings.Contains(job, "Do the thing.") {
		t.Errorf("the review job carries no rev 1 text:\n%s", job)
	}
	for _, s := range []string{"1 result gate.2", "1 intent test", "1 result test"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish", "1 fact merged"} {
		if r.has(s) {
			t.Errorf("the resolution published before its review (%q): %q", s, r.steps())
		}
	}
	r.accept("c2", r.patch(job))
	r.merged(1)
	if got := r.published().SHA; got != resolution || r.mainMerges() != 1 {
		t.Errorf("published %s, merges %d; want the resolution %s merged once", got, r.mainMerges(), resolution)
	}
	r.noLead()
}

// A conflict after an accepted move is resolved by a rev change's review, as before: the accept was of rev 0, the lead
// set rev 1, so the resolved gate ends the accept and the review job (review.2) comes before the publish.
func TestLoopMovedConflictNewSpecRevReviews(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	first := r.waitJob("pj-int", "T-1/1/review")
	r.movedConflict() // main edits the lane's line: the moved merge conflicts
	r.accept("c1", r.patch(first))
	job := r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	if !strings.Contains(job, "CONFLICT in "+scratch) {
		t.Fatalf("the resolve job does not name the moved gate's conflict:\n%s", job)
	}
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	resolution := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, "c2", "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	second := r.waitJob("pj-int", "T-1/1/review.2")
	if r.patch(second) != r.patch(first) {
		t.Errorf("review.2's patch %s, want the task's own %s", r.patch(second), r.patch(first))
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish.2", "1 fact merged"} {
		if r.has(s) {
			t.Errorf("the resolution of a rev-changed accept published (%q): %q", s, r.steps())
		}
	}
	r.accept("c3", r.patch(second))
	r.merged(1)
	if got := r.published().SHA; got != resolution || r.mainMerges() != 1 {
		t.Errorf("published %s, merges %d; want the resolution %s merged once", got, r.mainMerges(), resolution)
	}
}
