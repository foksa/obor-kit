package setup

// The project's monitor pane (T-658): `obor monitor` in a tab labelled "system" of the project's workspace, the pane
// labelled <prefix>-monitor (config.MonitorLabel; not an agent name, so nothing dispatches to it). `obor init` makes
// it with its other panes, `obor setup monitor` adds it to a project set up before, and herdr's restore starts the
// command again in a pane left at its shell (internal/launch). Both are idempotent: a monitor pane that is there is
// reused, and its "system" tab is never made twice.

import (
	"context"
	"time"

	"obor/internal/app"
	"obor/internal/project"
)

// monitor is `obor setup monitor`: the project's monitor pane, created or started again.
func (s *Setup) monitor(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	state, what, err := s.monitorSetup(ctx)
	if err != nil {
		return err
	}
	s.ok("monitor", state == "already", "%s", what)
	return nil
}

// monitorSetup is the monitor step of obor init and obor setup monitor: done (the pane was made, or the command typed
// again in a pane left at its shell) or already (a monitor runs there); what says which pane.
func (s *Setup) monitorSetup(ctx context.Context) (state, what string, err error) {
	if err := s.load(); err != nil {
		return "", "", err
	}
	label := s.cfg.MonitorLabel()
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return "", "", project.Failf("herdr pane list failed")
	}
	// Only the project's own workspace is obor's to touch (T-628, T-634).
	ws := s.projectWorkspace(panes)
	for _, p := range panes {
		if p.Label != label || !s.inProject(p.Cwd) || p.Workspace != ws {
			continue
		}
		if p.Kind != "" || s.monitorRuns(ctx, p.ID) {
			return "already", label + " runs in pane " + p.ID, nil
		}
		if err := s.runCommand(ctx, p.ID, project.MonitorCommand); err != nil {
			return "", "", err
		}
		return "done", label + " started in pane " + p.ID, nil
	}
	pane, err := s.monitorPane(ctx, panes)
	if err != nil {
		return "", "", err
	}
	if err := s.H.RenamePane(ctx, pane, label); err != nil {
		return "", "", project.Failf("herdr pane rename %s failed: %v", pane, err)
	}
	if err := s.runCommand(ctx, pane, project.MonitorCommand); err != nil {
		return "", "", err
	}
	return "done", label + " started in pane " + pane, nil
}

// monitorPane is the monitor's new pane in the project's workspace: a pane of its "system" tab (split beside that
// tab's largest project pane), or a new "system" tab when the project has none. Its cwd is the project root and it
// runs no agent.
func (s *Setup) monitorPane(ctx context.Context, panes []app.PaneInfo) (string, error) {
	tabs, err := s.H.Tabs(ctx)
	if err != nil {
		return "", project.Failf("herdr tab list failed: %v", err)
	}
	ws := s.projectWorkspace(panes)
	for _, t := range tabs {
		if t.Label == "system" && ws != "" && t.Workspace == ws {
			return s.split(ctx, panes, t.ID, s.root, nil)
		}
	}
	return s.newTab(ctx, panes, "system", s.root, nil)
}

// monitorRuns: something runs in pane beyond its shell (the monitor already), so nothing is typed there. An unreadable
// process info counts as running: a duplicate command in a live monitor's pane would echo over its screen.
func (s *Setup) monitorRuns(ctx context.Context, pane string) bool {
	pi, err := s.H.ProcessInfo(ctx, pane)
	return err != nil || !pi.AtShell()
}

// runCommand types cmd at pane's shell once herdr reports an available shell there (a pane a new tab or a split made
// may still be coming up, T-628), then Enter.
func (s *Setup) runCommand(ctx context.Context, pane, cmd string) error {
	if !s.waitShell(ctx, pane, time.Now().Add(s.shellWait())) {
		return project.Failf("pane %s is not an available shell after %s: run %q there by hand", pane, s.shellWait(),
			cmd)
	}
	if err := s.H.Type(ctx, pane, cmd); err != nil {
		return project.Failf("pane %s: %v", pane, err)
	}
	return nil
}
