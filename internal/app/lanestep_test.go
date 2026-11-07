package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// T-961: the lane test of a candidate (row 18b) at the app boundary: the fake Scratch's Test refuses a dirty lane and
// "checks the candidate out" there while its hook runs, as checks.Scratch does over git (internal/checks'
// TestLaneTest* hold the git side: the checkout, the restore, the dirty refusal).

// A lane with uncommitted changes when its test is due: the test is refused before it touches the lane (never checked
// out, the change kept), counted as a failed step and tried again on later passes, then a needs-you; once the lane is
// clean, obor resume runs it again and the task goes on to its review.
func TestLoopDirtyLaneTestRetried(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	var checkouts atomic.Int32
	r.scratch.OnTest(func(context.Context, string) { checkouts.Add(1) })
	dirty := filepath.Join(r.lane, "scratch-note.txt")
	r.scratch.Arm("Gate", func(context.Context) error { // the worker writes on after its task_done
		return os.WriteFile(dirty, []byte("still writing\n"), 0o644)
	})
	r.start()
	r.submit()
	r.waitPrefix("1 fact needs-you{test failed: lane L-01 dirty")
	if !r.has("1 result gate") || r.has("1 result test") {
		t.Fatalf("precondition: the gate green, no test result: %q", r.steps())
	}
	if n := r.count("1 intent test"); n != 1 {
		t.Errorf("test intents %d, want 1: each try reuses the pending one", n)
	}
	failed := 0
	for _, s := range r.steps() {
		if strings.HasPrefix(s, "1 fact failed{test: lane L-01 dirty") {
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("counted failures %d, want 3 (tried again on the later passes): %q", failed, r.steps())
	}
	if n := checkouts.Load(); n != 0 {
		t.Errorf("the dirty lane was checked out %d times, want never", n)
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the dirty lane is at %s, want its branch", got)
	}
	if got := r.p.Read(dirty); got != "still writing\n" {
		t.Errorf("the worker's uncommitted file %q, want it kept", got)
	}

	r.scratch.Arm("Gate", nil)
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.resume("r1", ""); err != nil || !strings.Contains(reply, "T-1") {
		t.Fatalf("resume: %q, %v", reply, err)
	}
	r.waitJob("pj-int", "T-1/1/review")
	if n := checkouts.Load(); n != 1 {
		t.Errorf("checkouts %d after the resume, want the one test", n)
	}
	if !r.has("1 result test") || r.count("1 intent test") != 1 {
		t.Errorf("the test ran under its pending key once the lane was clean: %q", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane is at %s after its test, want its branch", got)
	}
}

// serve stops while a lane test runs: the cut test is no failure and leaves its intent pending, the lane back on its
// branch; the next serve's replay runs the test again under the same key, and the task goes on to its review.
func TestLoopRestartMidTestRunsItAgain(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	started := make(chan struct{})
	var runs atomic.Int32
	r.scratch.OnTest(func(ctx context.Context, lane string) {
		if runs.Add(1) == 1 {
			close(started)
			<-ctx.Done() // the first test runs until serve stops
		}
	})
	r.start()
	r.submit()
	select {
	case <-started:
	case <-time.After(r.patience()):
		t.Fatalf("no lane test began: %q", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got == "" || got != r.scratch.Pin("candidate/T-1") {
		t.Fatalf("precondition: the lane is at the candidate while its test runs, got %q", got)
	}
	r.stop()
	if !r.has("1 intent test") || r.has("1 result test") || r.hasPrefix("1 fact failed{test") {
		t.Fatalf("the cut test: %q; want its intent pending, no result, no failure", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane after the cut test is at %s, want its branch back", got)
	}

	r.start()
	r.waitJob("pj-int", "T-1/1/review")
	if n := runs.Load(); n != 2 {
		t.Errorf("test runs %d, want 2: the cut one and its replay", n)
	}
	if r.count("1 intent test") != 1 || r.count("1 result test") != 1 {
		t.Errorf("the replay ran under the pending key: %q", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane after the replayed test is at %s, want its branch", got)
	}
}

// After a move the review job carries the task's own diff Base..S, not the merge onto the moved target: here a spec
// rev the lead set after the first review ends the accept (the one rule that reviews again, row 20), and review.2's
// diff is git diff <fork point> <submitted sha>, the main commit's file absent from it.
func TestLoopReviewDiffIsTheTasksOwnAfterAMove(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	first := r.waitJob("pj-int", "T-1/1/review")
	submitted := r.scratch.LaneHead("L-01")
	base := r.scratch.LaneBase("L-01") // the lane's fork point
	want := "git diff " + base + " " + submitted
	if !strings.Contains(first, want) {
		t.Fatalf("precondition: the first review job names %q:\n%s", want, first)
	}
	r.spec("sp1", specOne)
	r.scratch.Move() // an unrelated commit on main
	r.accept("c1", r.patch(first))
	second := r.waitJob("pj-int", "T-1/1/review.2")
	diff, _, _ := strings.Cut(second, "Patch hash:")
	if !strings.Contains(diff, want) || !strings.Contains(diff, "feature.txt") || strings.Contains(diff, "other.txt") {
		t.Errorf("review.2's diff part, want the task's own %q (feature.txt, not other.txt):\n%s", want, diff)
	}
	if r.patch(second) != r.patch(first) {
		t.Errorf("review.2's patch %s, want the first's %s (the move keeps it)", r.patch(second), r.patch(first))
	}
}
