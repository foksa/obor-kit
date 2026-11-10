package checks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"obor/internal/app/port"
	"obor/internal/project"
)

// ScratchFF is port.Workflow "scratch-ff" (workflow.kind, the default): the merge slot in serve's own worktree
// (Scratch: submit, gate, the lane test, the fast-forward publish), the lanes as worktrees on task branches (Lane), the
// target merge.target_branch, and the review's git on the main checkout (Review: cmd/obor's, obor's own commits there).
type ScratchFF struct {
	Scratch
	Lane
	port.Review
}

var _ port.Workflow = ScratchFF{}

// NewScratchFF is the scratch-ff workflow over s, serve's merge slot: the lanes take s's config, so a workflow holds one
// *config.Config.
func NewScratchFF(s Scratch, review port.Review) ScratchFF {
	return ScratchFF{Scratch: s, Lane: Lane{Cfg: s.Cfg}, Review: review}
}

// Detach is port.Review.Detach on the worktree a role reads (its role worktree, or the reviewer's at a review's start)
// and then the lane setup a lane gets there: lanes.ready, lanes.setup when it is red, lanes.ready again (LaneSetup), so
// a role that runs targeted tests reads a worktree that would pass a lane's own, not a bare checkout. A setup that
// stays red, or a ready still red after it, is the move's error with LaneSetup's message (the role is named by its
// dir's base name, <lanes.dir>/<role>): the role's start or brief is then refused and says why.
func (w ScratchFF) Detach(ctx context.Context, dir, sha string) error {
	if err := w.Review.Detach(ctx, dir, sha); err != nil {
		return err
	}
	return LaneSetup(ctx, w.Scratch.Cfg, filepath.Base(dir), dir, w.Scratch.Out)
}

// Target is the sha of merge.target_branch, and whether task's branch has commits it lacks.
func (w ScratchFF) Target(ctx context.Context, task string) (string, bool, error) {
	cfg := w.Scratch.Cfg
	base := cfg.MainBranch() // whatever the main checkout has checked out
	sha, err := project.GitCtx(ctx, cfg.Root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve %s: %w", base, err)
	}
	sha = strings.TrimSpace(sha)
	branch := cfg.TaskBranch(task)
	if _, err := project.GitCtx(ctx, cfg.Root, "show-ref", "--verify", "-q", "refs/heads/"+branch); err != nil {
		return sha, false, nil // no such branch (show-ref's exit 1)
	}
	_, err = project.GitCtx(ctx, cfg.Root, "merge-base", "--is-ancestor", "refs/heads/"+branch, base)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return sha, false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return sha, true, nil
	}
	return "", false, fmt.Errorf("cannot compare %s with %s: %w", branch, base, err)
}
