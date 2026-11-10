package main

// restore.go: serve's restore wiring: the launch.Restorer serve runs once its terminal connection is back,
// with obor restart's launch.

import (
	"context"
	"io"

	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
)

// restorer is the restore over env's herdr with obor restart's launch: serve's, after its terminal connection came
// back (its own project). env must name no pane: the restore is no process of one.
func restorer(env project.Env) *launch.Restorer {
	return &launch.Restorer{Env: env, H: herdrSocket(env),
		LaneState: func(cfg *config.Config, l config.Lane) string {
			return checks.NewLaneTable(context.Background(), cfg).LaneState(l).State
		},
		Restart: func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
			l := &launch.Launcher{Cfg: cfg, Env: env, H: herdrSocket(env), Out: w}
			return l.Restart(ctx, req)
		}}
}

// paneless is env without the variables project.Pane reads: serve may run in a pane, but its restore restarts other
// agents' panes, and a restart there would pass for the agent's own (launch: scheduled).
func paneless(env project.Env) project.Env {
	return func(key string) (string, bool) {
		for _, v := range project.PaneVars {
			if key == v {
				return "", false
			}
		}
		return env(key)
	}
}
