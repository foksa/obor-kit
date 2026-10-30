package checks

import (
	"fmt"
	"path/filepath"
	"testing"

	"obor/internal/app"
)

// after_merge runs in the main checkout (the directory publish fast-forwards), never in serve's worktree: the marker
// it touches lands in the root only.
func TestScratchAfterMergeRunsInMainCheckout(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"),
		cmd("after_merge", map[string]any{"cmd": "touch after.marker"}))
	cand := y.green(lane(y.submit()))
	y.there(y.serve, "precondition: serve's worktree")
	if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != app.PublishMerged {
		t.Fatalf("precondition: publish %+v %v", o, err)
	}
	line, err := y.s.AfterMerge(ctx, cand)
	y.eq(fmt.Sprintf("%s %v", line, err), "ran <nil>", "after_merge ran")
	y.there(filepath.Join(y.Root, "after.marker"), "marker in the main checkout")
	y.gone(filepath.Join(y.serve, "after.marker"), "marker in serve's worktree")
}

// A failed after_merge's WARN line names its log, as a failing check's line does (T-414).
func TestScratchAfterMergeFailedNamesItsLog(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"),
		cmd("after_merge", map[string]any{"cmd": "echo broken; exit 2"}))
	cand := y.green(lane(y.submit()))
	if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != app.PublishMerged {
		t.Fatalf("precondition: publish %+v %v", o, err)
	}
	line, err := y.s.AfterMerge(ctx, cand)
	log := filepath.Join(y.Root, ".obor/state/logs", cand.Task+"-after_merge.log")
	y.eq(fmt.Sprintf("%s %v", line, err), "WARN after_merge failed: broken; log "+log+" <nil>",
		"the WARN line names the log")
	y.there(log, "the failed run's log")
	y.eq(y.Read(log), "broken\n", "the failed run's output is in the log")
}

// after_merge's run keeps its full output in .obor/state/logs/<task>-after_merge.log: a green run too (T-414; on
// 2026-10-25 a stale dist/obor after a merge had no trace).
func TestScratchAfterMergeGreenLog(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"),
		cmd("after_merge", map[string]any{"cmd": "echo after-merge-ran"}))
	cand := y.green(lane(y.submit()))
	if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != app.PublishMerged {
		t.Fatalf("precondition: publish %+v %v", o, err)
	}
	line, err := y.s.AfterMerge(ctx, cand)
	y.eq(fmt.Sprintf("%s %v", line, err), "ran <nil>", "after_merge ran")
	log := filepath.Join(".obor/state/logs", cand.Task+"-after_merge.log")
	y.there(log, "the green run's log")
	y.eq(y.Read(log), "after-merge-ran\n", "the green run's output is in the log")
}

// The main checkout must be on a branch at the published merge: otherwise after_merge does not run and the result is
// a WARN line, again on a second call (no merges.log keeps a record, T-525).
func TestScratchAfterMergeMainNotAtMerge(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		moved func(y *sx)                   // the main checkout leaves the merge
		pre   func(y *sx, cand string) bool // the precondition: the checkout is not at the merge
	}{
		{"main moved past the merge",
			func(y *sx) { y.Git(y.Root, "commit", "-q", "--allow-empty", "-m", "later") },
			func(y *sx, cand string) bool { return y.head(y.Root) != cand }},
		{"main detached at the merge",
			func(y *sx) { y.Git(y.Root, "checkout", "-q", "--detach") },
			func(y *sx, _ string) bool { return y.Git(y.Root, "branch", "--show-current") == "" }},
	} {
		y := newSx(t, cmd("check", "echo 1 pass"),
			cmd("after_merge", map[string]any{"cmd": "touch after.marker"}))
		cand := y.green(lane(y.submit()))
		if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != app.PublishMerged {
			t.Fatalf("%s: precondition: publish %+v %v", c.name, o, err)
		}
		c.moved(y)
		if !c.pre(y, cand.SHA) {
			t.Fatalf("%s: precondition: the main checkout still at the merge", c.name)
		}
		line, err := y.s.AfterMerge(ctx, cand)
		y.eq(fmt.Sprintf("%s %v", line, err),
			"WARN after_merge not run: the main checkout is not at the published merge "+cand.SHA+" <nil>",
			c.name+": WARN, not run")
		y.gone(filepath.Join(y.Root, "after.marker"), c.name+": after_merge not run in the main checkout")
		y.gone(filepath.Join(y.serve, "after.marker"), c.name+": not run in serve's worktree")
		again, err := y.s.AfterMerge(ctx, cand)
		y.eq(fmt.Sprintf("%s %v", again, err), fmt.Sprintf("%s %v", line, nil), c.name+": the same WARN again")
		y.gone(y.Path(".obor/state/merges.log"), c.name+": no merges.log")
	}
}
