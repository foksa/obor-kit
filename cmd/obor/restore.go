package main

// restore.go: serve's restore wiring: the launch.Restorer serve runs once its terminal connection is back,
// with obor restart's launch and stop and, for a role agent, serve's open-work read.

import (
	"context"
	"fmt"
	"io"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
)

// serveApp is what serve's launchers ask serve: the App's own answers, in process (app.App).
type serveApp interface {
	Where(ctx context.Context, req wire.WhereReq) (wire.WhereReply, error)
	Sessions(root, agent string) (store.Session, bool)
	Report(root string, s store.Session) error
	Started(root, agent string) error
	RestartJob(root, agent, role, lane string) (string, error)
	OpenWork(agent, role string) (bool, error)
}

var _ serveApp = (*app.App)(nil)

// serveLauncher is the launcher serve runs for cfg's project over h: where, sessions, reports and started come from
// s, so a start of serve's dials no socket of serve's own and reads no store from disk.
func serveLauncher(cfg *config.Config, env project.Env, h port.PaneControl, out io.Writer, s serveApp) *launch.Launcher {
	root := cfg.Root
	return &launch.Launcher{Cfg: cfg, Env: env, H: h, Out: out,
		Where:    s.Where,
		Sessions: func(agent string) (store.Session, bool) { return s.Sessions(root, agent) },
		Report:   func(_ context.Context, ss store.Session) error { return s.Report(root, ss) },
		Started:  func(_ context.Context, agent string) error { return s.Started(root, agent) }}
}

// restorer is the restore over the project's terminal term (serve's) with obor restart's launch and s's state: serve's,
// after its terminal connection came back (its own project). env must name no pane: the restore is no process of one.
func restorer(term port.Terminal, env project.Env, s serveApp) *launch.Restorer {
	return &launch.Restorer{Env: env, H: term.Control,
		LaneState: func(cfg *config.Config, l config.Lane) string {
			return checks.NewLaneTable(context.Background(), cfg).LaneState(l).State
		},
		OpenWork: s.OpenWork,
		Sessions: s.Sessions,
		Restart: func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
			l := serveLauncher(cfg, env, term.Control, w, s)
			l.Job = func(_ context.Context, role, lane string) (string, error) {
				job, err := s.RestartJob(cfg.Root, req.Agent, role, lane)
				if err != nil { // the restore's rule: an error restores the pane, with no wake (obor restart refuses)
					fmt.Fprintf(w, "job read for %s failed, restored with no wake: %v\n", req.Agent, err)
					return "", nil
				}
				return job, nil
			}
			return l.Restart(ctx, req)
		},
		Stop: func(ctx context.Context, cfg *config.Config, agent, pane string, w io.Writer) (string, error) {
			return serveLauncher(cfg, env, term.Control, w, s).Stop(ctx, agent, pane, false)
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
