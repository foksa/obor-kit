package checks

import (
	"fmt"
	"path/filepath"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// afterMergeFx is a fresh fixture with after_merge v (commands.after_merge) whose candidate is published: the fixture
// and the candidate.
func afterMergeFx(t *testing.T, v map[string]any) (*sx, port.Candidate) {
	t.Helper()
	y := newSx(t, cmd("check", "echo 1 pass"), cmd("after_merge", v))
	cand := y.green(lane(y.submit()))
	if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != port.PublishMerged {
		t.Fatalf("precondition: publish %+v %v", o, err)
	}
	return y, cand
}

// afterMergeLog is the successful run's log, .obor/state/logs/<task>-after_merge.log (T-414).
func afterMergeLog(cand port.Candidate) string {
	return filepath.Join(".obor/state/logs", cand.Task+"-after_merge.log")
}

// after_merge runs commands.after_merge in the main checkout (the directory publish fast-forwards), never in serve's
// worktree; its when globs decide; its full output goes to .obor/state/logs/<task>-after_merge.log, a green run too
// (T-414: on 2026-10-25 a stale dist/obor after a merge had no trace). It keeps no record of its own (T-525: its intent
// in the store is the once), so a second call runs it again.
func TestScratchAfterMerge(t *testing.T) {
	testkit.Slow(t) // 1.54 s: three after-merge runs over fixture repos (T-1004: the 2-core budget)
	t.Parallel()
	t.Run("runs in the main checkout, its output in the log, again on a second call", func(t *testing.T) {
		t.Parallel()
		y, cand := afterMergeFx(t, map[string]any{"cmd": `echo after-merge-ran; echo ran >> after.ran; touch after.marker`})
		for i := 1; i <= 2; i++ {
			line, err := y.s.AfterMerge(ctx, cand)
			y.eq(fmt.Sprintf("%s %v", line, err), "ran <nil>", fmt.Sprintf("call %d", i))
		}
		y.eq(y.Read("after.ran"), "ran\nran\n", "both calls ran it")
		y.there(filepath.Join(y.Root, "after.marker"), "marker in the main checkout")
		y.gone(filepath.Join(y.serve, "after.marker"), "marker in serve's worktree")
		y.there(afterMergeLog(cand), "the green run's log")
		y.eq(y.Read(afterMergeLog(cand)), "after-merge-ran\n", "the run's output in the log")
		y.gone(y.Path(".obor/state/merges.log"), "no merges.log")
	})
	t.Run("a when glob that does not match skips it", func(t *testing.T) {
		t.Parallel()
		y, cand := afterMergeFx(t, map[string]any{"cmd": "touch after.marker", "when": []any{"cmd/**"}})
		line, err := y.s.AfterMerge(ctx, cand)
		y.eq(fmt.Sprintf("%s %v", line, err), "skipped <nil>", "after_merge skipped")
		y.gone(filepath.Join(y.Root, "after.marker"), "not run in the main checkout")
		y.gone(y.Path(afterMergeLog(cand)), "no log for a run that did not happen")
	})
	t.Run("a failed run's WARN line names its log", func(t *testing.T) {
		t.Parallel()
		y, cand := afterMergeFx(t, map[string]any{"cmd": "echo broken; exit 2"})
		line, err := y.s.AfterMerge(ctx, cand)
		log := y.Path(filepath.Join(".obor/state/logs", cand.Task+"-after_merge.log"))
		y.eq(fmt.Sprintf("%s %v", line, err), "WARN after_merge failed: broken; log "+log+" <nil>",
			"the WARN line names the log")
		y.there(log, "the failed run's log")
		y.eq(y.Read(log), "broken\n", "the failed run's output is in the log")
	})
}

// The main checkout must be on a branch at the published merge: otherwise after_merge does not run and the result is
// a WARN line, again on a second call (no merges.log keeps a record, T-525).
func TestScratchAfterMergeMainNotAtMerge(t *testing.T) {
	t.Parallel()
	y, cand := afterMergeFx(t, map[string]any{"cmd": "touch after.marker"})
	for _, c := range []struct {
		name  string
		moved func() // the main checkout leaves the merge
	}{
		{"main moved past the merge", func() { y.Git(y.Root, "commit", "-q", "--allow-empty", "-m", "later") }},
		{"main detached at the merge", func() { y.Git(y.Root, "checkout", "-q", "--detach", cand.SHA) }},
	} {
		c.moved()
		// Precondition: the checkout is not a branch at the merge.
		if b, at := y.Git(y.Root, "branch", "--show-current"), y.head(y.Root); b != "" && at == cand.SHA {
			t.Fatalf("%s: precondition: the main checkout is still on a branch at the merge", c.name)
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
