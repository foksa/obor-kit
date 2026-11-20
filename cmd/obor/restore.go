package main

// restore.go: serve's restore wiring: the launch.Restorer serve runs once its terminal connection is back,
// with obor restart's launch and, for a role agent, serve's open-work read.

import (
	"context"
	"io"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
)

// restorer is the restore over the project's terminal term (serve's) with obor restart's launch and a's state: serve's,
// after its terminal connection came back (its own project). env must name no pane: the restore is no process of one.
func restorer(term port.Terminal, env project.Env, a *app.App) *launch.Restorer {
	return &launch.Restorer{Env: env, H: term.Control,
		LaneState: func(cfg *config.Config, l config.Lane) string {
			return checks.NewLaneTable(context.Background(), cfg).LaneState(l).State
		},
		OpenWork: a.OpenWork,
		Job:      a.RestartJob,
		Restart: func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
			l := &launch.Launcher{Cfg: cfg, Env: env, H: term.Control, Out: w}
			return l.Restart(ctx, req)
		}}
}

// paneless is env without the variables project.PaneVars lists: serve may run in a pane, but its restore restarts other
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
