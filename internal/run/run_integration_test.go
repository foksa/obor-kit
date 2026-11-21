//go:build integration

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/testkit"
)

// setupGit is setup over a real repository: lane L-01 a git worktree, the run's git the real one.
func setupGit(t *testing.T, commands string) *fx {
	t.Helper()
	p := testkit.New(t)
	f := &fx{p: p, lane: p.Lane("L-01", "task/T-01"), slots: filepath.Join(p.Base, "slots")}
	p.Write(filepath.Join(f.lane, ".gitignore"), ".task-done.md\n.task-run\n")
	p.Config(testkit.Set("commands", commands), testkit.Set("checks", map[string]any{"slot_dir": f.slots, "heavy_at_once": 2}))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	return f
}

// laneIndex is the lane's index file, its HEAD and its staged files: what computing a tree must not change.
func laneIndex(t *testing.T, f *fx) string {
	t.Helper()
	idx := strings.TrimSpace(f.p.Git(f.lane, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	b, _ := os.ReadFile(idx)
	return f.p.Git(f.lane, "rev-parse", "HEAD") + "|" + f.p.Git(f.lane, "diff", "--cached", "--name-only") + "|" + string(b)
}

// obor run exports OBOR_CHANGED_FILES in a lane: committed and uncommitted (also untracked) changes against the merge
// base with the target, the done file not; the lane's index and HEAD stay as they were.
func TestRunExportsChangedFiles(t *testing.T) {
	t.Parallel()
	f := setupGit(t, `{"files": {"cmd": "cat \"$OBOR_CHANGED_FILES\"", "heavy": true}, "light": "echo \"[$OBOR_CHANGED_FILES]\""}`)
	f.p.Write(filepath.Join(f.lane, ".gitignore"), ".task-run\n") // the done file is not ignored: only the tree leaves it out
	f.p.Commit(f.lane)                                            // work.txt, committed
	f.p.Write(filepath.Join(f.lane, "shared.txt"), "tracked edit\n")
	f.p.Git(f.lane, "add", "shared.txt") // staged
	f.p.Write(filepath.Join(f.lane, "new.txt"), "untracked\n")
	f.p.Submit("L-01")
	before := laneIndex(t, f)
	if !strings.Contains(before, "shared.txt") {
		t.Fatalf("precondition: shared.txt staged: %q", before)
	}

	r := f.run(t, f.lane, nil, nil, "files")
	if r.rc != 0 {
		t.Fatalf("rc: %+v", r)
	}
	got := strings.Fields(r.out)
	want := []string{".gitignore", "new.txt", "shared.txt", "work.txt"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("changed files %v, want %v (committed + staged + untracked, no done file)", got, want)
	}
	if after := laneIndex(t, f); after != before {
		t.Errorf("the lane's index or HEAD changed while computing the tree:\nbefore %q\nafter  %q", before, after)
	}
	if r := f.run(t, f.lane, nil, nil, "light"); r.out != "[]\n" {
		t.Errorf("a light command gets no changed-file list: %q", r.out)
	}
	if r := f.run(t, f.p.Root, nil, nil, "files"); r.out != "" {
		t.Errorf("outside a lane there is no list: %+v", r)
	}
}
