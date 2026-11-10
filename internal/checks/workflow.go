package checks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
