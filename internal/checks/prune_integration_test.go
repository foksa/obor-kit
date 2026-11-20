//go:build integration

package checks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
)

// GitPruner on a real repo: the branches under task/ with their tips, git's refusal of a branch checked out in a
// worktree, a deleted branch whose tip moved, the clean check of a worktree (dirty, commits no branch holds), the
// remove of a clean one and git worktree prune of one whose dir is gone.
func TestGitPrunerOnARealRepo(t *testing.T) {
	x := newFx(t) // L-01 on task/T-05, dirty (feature.txt)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	g, ctx := GitPruner{Cfg: cfg}, context.Background()
	x.Git(x.Root, "branch", "task/T-30")
	x.Git(x.Root, "branch", "keep/T-30")
	names := func() []string {
		bs, err := g.Branches(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, b := range bs {
			if len(b.SHA) != 40 {
				t.Errorf("branch %+v has no full sha", b)
			}
			out = append(out, b.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"task/T-05", "task/T-30"}) {
		t.Fatalf("branches %v, want the task/ ones only (keep/T-30 is not listed)", got)
	}
	in, err := g.CheckedOut(ctx)
	if err != nil || !in["task/T-05"] || in["task/T-30"] {
		t.Fatalf("checked out %v, %v: want task/T-05 (lane L-01) and not task/T-30", in, err)
	}
	bs, _ := g.Branches(ctx)
	checkedOut := bs[0]
	if err := g.DeleteBranch(ctx, checkedOut); err == nil || !slices.Contains(names(), "task/T-05") {
		t.Errorf("deleting the checked-out %s: %v; branches %v: want git's refusal and the branch kept", checkedOut.Name, err, names())
	}
	if err := g.DeleteBranch(ctx, port.PruneBranch{Name: "task/T-30", SHA: strings.Repeat("0", 40)}); err == nil ||
		!strings.Contains(err.Error(), "moved") || !slices.Contains(names(), "task/T-30") {
		t.Errorf("deleting task/T-30 with a stale tip: %v; branches %v: want a refusal and the branch kept", err, names())
	}
	if err := g.DeleteBranch(ctx, bs[1]); err != nil || slices.Contains(names(), "task/T-30") {
		t.Errorf("deleting task/T-30: %v; branches %v: want it gone", err, names())
	}

	// Worktrees: L-01 is dirty; L-02 is clean; a detached one with a commit no branch holds stays; one whose dir is gone.
	lane2 := x.lane2()
	detached := filepath.Join(filepath.Dir(lane2), "L-03")
	x.Git(x.Root, "worktree", "add", "--detach", detached)
	if err := os.WriteFile(filepath.Join(detached, "only.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	x.Git(detached, "add", "only.txt")
	x.Git(detached, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "only here")
	gone := filepath.Join(filepath.Dir(lane2), "L-04")
	x.Git(x.Root, "worktree", "add", "--detach", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	trees, err := g.Trees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]port.PruneTree{}
	for _, tr := range trees {
		by[filepath.Base(tr.Dir)] = tr
	}
	if len(by) != 4 || by["L-01"].Branch != "task/T-05" || by["L-03"].Branch != "" || !by["L-04"].Gone || by["L-02"].Gone ||
		by["L-02"].SHA == "" {
		t.Fatalf("trees %+v: want L-01..L-04 (the main checkout left out), L-03 detached, only L-04 gone", by)
	}
	for _, c := range []struct{ lane, want string }{
		{"L-01", "dirty"}, {"L-02", ""}, {"L-03", "commits no branch holds"},
	} {
		why, err := g.Dirt(ctx, by[c.lane].Dir)
		if ok := why == "" && c.want == "" || why != "" && c.want != "" && strings.HasPrefix(why, c.want); err != nil || !ok {
			t.Errorf("Dirt(%s) = %q, %v; want %q", c.lane, why, err, c.want)
		}
	}
	if err := g.RemoveTree(ctx, by["L-02"].Dir); err != nil {
		t.Fatalf("removing the clean L-02: %v", err)
	}
	if _, err := os.Stat(lane2); !os.IsNotExist(err) {
		t.Errorf("L-02's dir is still there: %v", err)
	}
	if err := g.PruneTrees(ctx); err != nil {
		t.Fatal(err)
	}
	trees, _ = g.Trees(ctx)
	var left []string
	for _, tr := range trees {
		left = append(left, filepath.Base(tr.Dir))
	}
	slices.Sort(left)
	if !slices.Equal(left, []string{"L-01", "L-03"}) {
		t.Errorf("worktrees left %v, want L-01 and L-03 (L-02 removed, L-04's registration pruned)", left)
	}
}
