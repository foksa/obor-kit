package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
)

// dispatchLoop sets l up for dispatch as state: the lane adapter, the spec reader and the obor kit. The merge loop no
// longer pauses while the main checkout is off branches.main (T-1023: the review close, its last reader, refuses on
// its own).
func dispatchLoop(l *app.Loop, env project.Env, cfg *config.Config) {
	l.Lane = checks.Lane{Cfg: cfg, Out: io.Discard}
	l.Specs = specs{cfg: cfg, env: env}
	l.Kit = project.KitRoot(env)
}

// specs is app.Specs for one project: its task ids and task branches in git.
type specs struct {
	cfg *config.Config
	env project.Env
}

var _ app.Specs = specs{}

// Check is why task cannot be dispatched ("" when it can): a bad task id or no stored spec (text "").
func (s specs) Check(_ context.Context, task, text string) (string, error) {
	if !s.cfg.TaskIDOK(task) {
		return fmt.Sprintf("%s is no task id (task_id_pattern %s)", task, s.cfg.Data.TaskIDPattern), nil
	}
	if text == "" {
		return fmt.Sprintf("no spec for %s in obor serve's store (obor spec set %s <path>)", task, task), nil
	}
	return "", nil
}

// Target is the sha of branches.main, and whether task's branch has commits it lacks.
func (s specs) Target(ctx context.Context, task string) (string, bool, error) {
	base, err := s.target(ctx)
	if err != nil {
		return "", false, err
	}
	sha, err := project.GitCtx(ctx, s.cfg.Root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve %s: %w", base, err)
	}
	branch := s.cfg.TaskBranch(task)
	if _, err := project.GitCtx(ctx, s.cfg.Root, "show-ref", "--verify", "-q", "refs/heads/"+branch); err != nil {
		return strings.TrimSpace(sha), false, nil // no such branch (show-ref's exit 1)
	}
	_, err = project.GitCtx(ctx, s.cfg.Root, "merge-base", "--is-ancestor", "refs/heads/"+branch, base)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(sha), false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return strings.TrimSpace(sha), true, nil
	}
	return "", false, fmt.Errorf("cannot compare %s with %s: %w", branch, base, err)
}

// target is branches.main, whatever the main checkout has checked out (T-425).
func (s specs) target(context.Context) (string, error) {
	return s.cfg.Data.Branches.Main, nil
}
