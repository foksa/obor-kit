package main

import (
	"context"
	"fmt"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/project"
)

// dispatchLoop sets l up for dispatch as state: the spec reader and the obor kit (the lanes and the target are the
// workflow's, T-1029). The merge loop no longer pauses while the main checkout is off branches.main (T-1023: the
// review close, its last reader, refuses on its own).
func dispatchLoop(l *app.Loop, env project.Env, cfg *config.Config) {
	l.Specs = specs{cfg: cfg, env: env}
	l.Kit = project.KitRoot(env)
}

// specs is app.Specs for one project: its task ids.
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
