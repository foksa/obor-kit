package main

// obor uninstall (internal/setup/uninstall.go) is setup's; this file is what it asks of the running project: serve,
// the agents and the main repo's git, built from the same pieces obor stop and obor prune use.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/service"
	"obor/internal/setup"
)

// uninstallCmd is `obor uninstall [--dry-run] [--discard] [--purge] [--force]`.
func uninstallCmd(args []string) int { return setupCmd(append([]string{"obor-uninstall"}, args...)) }

// teardownOf is setup's Teardown for the caller in cwd: t is the terminal's pane control (the agents' stop).
func teardownOf(env project.Env, cwd string, t port.PaneControl) *setup.Teardown {
	sleeperOf := func(cfg *config.Config, force bool, w *bytes.Buffer) *sleeper {
		return &sleeper{Cfg: cfg, Env: env, Cwd: cwd, Out: w, H: t, Force: force, Why: "obor uninstall"}
	}
	return &setup.Teardown{
		Open: func(ctx context.Context, cfg *config.Config) (string, bool) {
			ps, err := sleeperOf(cfg, false, &bytes.Buffer{}).status(ctx)
			if err != nil {
				return "", false
			}
			return busyList(openTasks(ps)), true
		},
		Merged: func(ctx context.Context, cfg *config.Config) ([]string, error) {
			req := &wire.PruneReq{DryRun: true}
			req.Envelope = caller(env, cwd, cfg)
			reply, err := wire.Verdict(ctx, wire.SocketPath(cfg.Root), req)
			if err != nil {
				return nil, err
			}
			return prunedBranches(reply), nil
		},
		Agents: func(ctx context.Context, cfg *config.Config, force, dry bool) (string, string, error) {
			var w bytes.Buffer
			s := sleeperOf(cfg, force, &w)
			if dry {
				return s.agentsWould(ctx)
			}
			if err := s.stopAgents(ctx); err != nil {
				return "", "", err
			}
			state := "done"
			if strings.Contains(w.String(), "none running") {
				state = "already"
			}
			return state, joinLines(w.String(), "agents: "), nil
		},
		Serve: func(ctx context.Context, cfg *config.Config, dry bool) (string, string, error) {
			return serveDown(ctx, sleeperOf(cfg, false, &bytes.Buffer{}), dry)
		},
		Pruner: func(cfg *config.Config) port.Pruner { return checks.GitPruner{Cfg: cfg} },
	}
}

// prunedBranches is the task branches a prune reply's lines name ("would delete task/T-1 (was abc1234)").
func prunedBranches(reply string) []string {
	var out []string
	for _, l := range strings.Split(reply, "\n") {
		if name, ok := strings.CutPrefix(l, "would delete task/"); ok {
			name, _, _ = strings.Cut(name, " ")
			out = append(out, "task/"+name)
		}
	}
	return out
}

// oneLine is out's lines joined with "; ", each without prefix.
func joinLines(out, prefix string) string {
	var parts []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimPrefix(l, prefix); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, "; ")
}

// agentsWould says which agents obor uninstall would stop: the labelled agent panes of the project but the caller's.
func (s *sleeper) agentsWould(ctx context.Context) (string, string, error) {
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return "", "", fmt.Errorf("the terminal's panes not listed (%v)", err)
	}
	repo, _ := project.Find(s.Cwd)
	ownPane, _ := project.Pane(s.Env)
	ownAgent := project.Self(repo, s.Cfg, s.Env)
	var names []string
	for _, p := range panes {
		if p.Label == "" || p.Agent == "" || p.ID == ownPane || ownAgent != "" && p.Label == ownAgent {
			continue
		}
		info, ok := s.Cfg.AgentLookup(p.Label, "")
		if ok && (project.LabelOurs(s.Cfg, p.Label, p.Cwd, "") ||
			info.Cwd != "" && project.LabelOurs(s.Cfg, p.Label, p.Cwd, info.Cwd)) {
			names = append(names, p.Label)
		}
	}
	if len(names) == 0 {
		return "already", "none running", nil
	}
	return "done", "would stop " + strings.Join(names, ", "), nil
}

// serveDown ends the project's serve (obor stop's path) and uninstalls its launchd agent, and the machine's obor.web
// with the last one. Serve not running and no agent installed: already.
func serveDown(ctx context.Context, s *sleeper, dry bool) (string, string, error) {
	cfg := s.Cfg
	svc := serviceOf(cfg, s.Env)
	web := webOf(cfg, s.Env)
	_, askErr := s.ask(ctx)
	running := askErr == nil || servePID(cfg.Root) != 0
	installed := false
	if svc.Supported() {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, loaded := svc.PID(pctx)
		cancel()
		_, statErr := os.Stat(svc.PlistPath())
		installed = loaded || statErr == nil
	}
	if dry {
		var parts []string
		if running {
			parts = append(parts, fmt.Sprintf("would stop serve (pid %d)", servePID(cfg.Root)))
		}
		if installed {
			parts = append(parts, "would uninstall "+svc.Label())
		}
		if len(parts) == 0 {
			return "already", "serve not running, " + svc.Label() + " not installed", nil
		}
		return "done", strings.Join(parts, "; ") + " (obor.web with the machine's last serve agent)", nil
	}
	var lines []string
	switch {
	case running:
		var w bytes.Buffer
		s.Out = &w
		c := caller(s.Env, s.Cwd, cfg)
		if err := s.stopServe(ctx, c); err != nil {
			return "", "", err
		}
		lines = append(lines, joinLines(w.String(), "serve: "))
	case installed:
		l, err := svc.Uninstall(ctx)
		if err != nil {
			return "", "", fmt.Errorf("obor serve uninstall: %v", err)
		}
		lines = append(lines, l)
	}
	if web.Supported() {
		wl, err := web.UninstallLast(ctx)
		if err != nil {
			return "", "", fmt.Errorf("obor.web: %v", err)
		}
		if wl != "" && wl != service.Elsewhere && !strings.HasSuffix(wl, " is not installed") {
			lines = append(lines, wl)
		}
	}
	if len(lines) == 0 {
		return "already", "serve not running, " + svc.Label() + " not installed", nil
	}
	return "done", strings.Join(lines, "; "), nil
}
