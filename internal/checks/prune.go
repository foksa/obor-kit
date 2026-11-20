package checks

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
)

// GitPruner is port.Pruner on the project's main repo: git -C <root>. It decides nothing (serve's gitprune.go does);
// the refusals it makes are git's own (a branch checked out in a worktree, a locked worktree) and the clean check.
type GitPruner struct{ Cfg *config.Config }

var _ port.Pruner = GitPruner{}

func (g GitPruner) git(ctx context.Context, args ...string) (string, error) {
	return project.GitCtx(ctx, g.Cfg.Root, args...)
}

const headsPrefix = "refs/heads/"

// Branches is the local branches under task/ with their tips.
func (g GitPruner) Branches(ctx context.Context) ([]port.PruneBranch, error) {
	out, err := g.git(ctx, "for-each-ref", "--format=%(refname) %(objectname)", headsPrefix+"task/")
	if err != nil {
		return nil, err
	}
	var bs []port.PruneBranch
	for _, line := range strings.Split(out, "\n") {
		if ref, sha, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			bs = append(bs, port.PruneBranch{Name: strings.TrimPrefix(ref, headsPrefix), SHA: sha})
		}
	}
	return bs, nil
}

// worktrees is git worktree list --porcelain, the main checkout (first) included.
func (g GitPruner) worktrees(ctx context.Context) ([]port.PruneTree, error) {
	out, err := g.git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var ts []port.PruneTree
	for _, block := range strings.Split(out, "\n\n") {
		var t port.PruneTree
		for _, line := range strings.Split(block, "\n") {
			key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
			switch key {
			case "worktree":
				t.Dir = val
			case "HEAD":
				t.SHA = val
			case "branch":
				t.Branch = strings.TrimPrefix(val, headsPrefix)
			case "prunable":
				t.Gone = true
			}
		}
		if t.Dir != "" {
			ts = append(ts, t)
		}
	}
	return ts, nil
}

// CheckedOut is the branches checked out in any worktree, the main checkout's too.
func (g GitPruner) CheckedOut(ctx context.Context) (map[string]bool, error) {
	ts, err := g.worktrees(ctx)
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, t := range ts {
		if t.Branch != "" {
			in[t.Branch] = true
		}
	}
	return in, nil
}

// DeleteBranch is git branch -D (the gate merges a candidate commit, so git never finds a merged branch merged) of a
// branch whose tip is still b.SHA; git refuses one checked out in a worktree.
func (g GitPruner) DeleteBranch(ctx context.Context, b port.PruneBranch) error {
	tip, err := g.git(ctx, "rev-parse", "--verify", headsPrefix+b.Name)
	if err != nil {
		return err
	}
	if tip = strings.TrimSpace(tip); tip != b.SHA {
		return fmt.Errorf("its tip moved to %s since it was listed", short(tip))
	}
	_, err = g.git(ctx, "branch", "-D", b.Name)
	return err
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// Trees is the registered worktrees but the main checkout, whose dir is gone marked.
func (g GitPruner) Trees(ctx context.Context) ([]port.PruneTree, error) {
	ts, err := g.worktrees(ctx)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return ts[1:], nil
}

// Dirt is why the worktree at dir must stay: uncommitted work (obor's own links are not work: project.LaneDirt), or a
// HEAD that no branch, tag or remote branch holds (removing the worktree would lose those commits).
func (g GitPruner) Dirt(ctx context.Context, dir string) (string, error) {
	in := func(dir string, args ...string) (string, error) { return project.GitCtx(ctx, dir, args...) }
	paths, err := project.LaneDirt(g.Cfg, dir, in)
	if err != nil {
		return "", err
	}
	if len(paths) > 0 {
		return fmt.Sprintf("dirty (%d paths, as %s)", len(paths), paths[0]), nil
	}
	out, err := in(dir, "rev-list", "-n", "1", "HEAD", "--not", "--branches", "--tags", "--remotes")
	if err != nil {
		return "", err
	}
	if sha := strings.TrimSpace(out); sha != "" {
		return "commits no branch holds (up to " + short(sha) + ")", nil
	}
	return "", nil
}

// RemoveTree is git worktree remove --force of a worktree Dirt found clean (the force is for obor's own links, which
// git counts as untracked files); a locked worktree stays (it takes two).
func (g GitPruner) RemoveTree(ctx context.Context, dir string) error {
	_, err := g.git(ctx, "worktree", "remove", "--force", dir)
	return err
}

// PruneTrees is git worktree prune.
func (g GitPruner) PruneTrees(ctx context.Context) error {
	_, err := g.git(ctx, "worktree", "prune")
	return err
}
