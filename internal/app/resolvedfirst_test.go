package app_test

import (
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/app"
	"obor/internal/app/wire"
)

// T-968 (RV-134 #2): a conflict resolved before any accept is reviewed by the resolution itself: its resolved
// candidate is gated and tested once in the lane, and a green test publishes it with no second integrator start. What
// still reviews is a red lane test (it sends back) and a spec rev the lead set since the resolution.

// conflict has the lane's edit and main's collide in shared.txt: T-1's first gate conflicts and the integrator
// resolves it in the scratch, where resolve writes and commits the resolution (the resolved gate's input).
func conflict(t *testing.T, r *loopRig) {
	t.Helper()
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Write("shared.txt", "main\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Git(r.p.Root, "commit", "-qam", "main edit")
}

// resolve writes the resolution in the scratch (add runs on it first) and gives the resolved verdict.
func resolve(t *testing.T, r *loopRig, callID string, add ...func(dir string)) {
	t.Helper()
	r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	r.p.Write(filepath.Join(scratch, "shared.txt"), "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	for _, fn := range add {
		fn(scratch)
	}
	r.p.Git(scratch, "add", "-A")
	r.p.Git(scratch, "commit", "-qm", "Merge T-1: resolved")
	if reply, err := r.verdict(wire.MethodResolved, callID, "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
}

// A task with a stored spec: the resolve verdict records the rev it was made under (the store's, read at the verdict),
// so the resolved gate at that same rev binds the accept too, and the task publishes.
func TestLoopResolvedFirstConflictAtStoredRevMerged(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	conflict(t, r)
	r.start()
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	r.submit()
	resolve(t, r, "c1")
	r.merged(1)
	if !r.has("1 fact accept.kept") || r.mainMerges() != 1 || r.has("1 intent review") {
		t.Errorf("rev 1: no kept accept, a merge, no review: %q", r.steps())
	}
	r.noLead()
}

// The resolved candidate's lane test is red: the task goes back to its worker with the red line, as any red test; the
// resolve verdict bound no publish.
func TestLoopResolvedFirstConflictRedSendsBack(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, `if [ -f resolved.txt ]; then echo "0 pass, 1 fail"; exit 1; fi; echo 1 pass`)
	conflict(t, r)
	r.start()
	r.submit()
	resolve(t, r, "c1", func(dir string) {
		r.p.Write(filepath.Join(dir, "resolved.txt"), "the resolution\n")
	})
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
	if n, p := r.mainMerges(), r.scratch.publishes.Load(); n != 0 || p != 0 {
		t.Errorf("merges %d, publishes %d; want none", n, p)
	}
	r.noLead()
}

// The lead sets a new spec rev after the resolution: the resolved gate's rev is not the verdict's, so the accept does
// not bind and the task is reviewed (the attempt's first review job) before it publishes. A low disk holds the
// resolved gate off the scratch until the rev is set, so the case is not a race.
func TestLoopResolvedFirstConflictNewSpecRevReviews(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	var free atomic.Uint64
	free.Store(50 << 30)
	r.disk = &app.Disk{Volume: "/vol/pj", Free: func() (uint64, error) { return free.Load(), nil }}
	conflict(t, r)
	r.start()
	r.submit()
	r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	r.p.Write(filepath.Join(scratch, "shared.txt"), "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Git(scratch, "add", "shared.txt")
	r.p.Git(scratch, "commit", "-qm", "Merge T-1: resolved")
	free.Store(1 << 30) // the resolved gate would only fail on a full disk: it does not start
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	if n := r.scratch.gates.Load(); n != 1 {
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
			t.Errorf("a spec rev set since the resolution published (%q): %q", s, r.steps())
		}
	}
	r.accept("c2", r.patch(job))
	r.merged(1)
	if got := r.p.Read("shared.txt"); got != "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n" || r.mainMerges() != 1 {
		t.Errorf("main's shared.txt %q, merges %d; want the resolution merged once", got, r.mainMerges())
	}
	r.noLead()
}

// A conflict after an accepted move is resolved by a rev change's review, as before: the accept was of rev 0, the lead
// set rev 1, so the resolved gate ends the accept and the review job (review.2) comes before the publish.
func TestLoopMovedConflictNewSpecRevReviews(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	first := r.waitJob("pj-int", "T-1/1/review")
	r.p.Write("shared.txt", "main\n2\n3\n4\n5\n6\n7\n8\n9\n10\n") // the same line: the moved merge conflicts
	r.p.Git(r.p.Root, "commit", "-qam", "main edit")
	r.accept("c1", r.patch(first))
	job := r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	if !strings.Contains(job, "CONFLICT in "+scratch) {
		t.Fatalf("the resolve job does not name the moved gate's conflict:\n%s", job)
	}
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	r.p.Write(filepath.Join(scratch, "shared.txt"), "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Git(scratch, "add", "shared.txt")
	r.p.Git(scratch, "commit", "-qm", "Merge T-1: resolved")
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
	if got := r.p.Read("shared.txt"); got != "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n" || r.mainMerges() != 1 {
		t.Errorf("main's shared.txt %q, merges %d; want the resolution merged once", got, r.mainMerges())
	}
}
