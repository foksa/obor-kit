package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
)

// where is the placement of a new agent: at most one of --tab, --new-tab, --pane.
type where struct{ tab, newTab, pane string }

func parseWhere(args []string) (where, error) {
	var w where
	n := 0
	for len(args) > 0 {
		if len(args) < 2 || args[1] == "" {
			return w, errUsage
		}
		switch args[0] {
		case "--tab":
			w.tab = args[1]
		case "--new-tab":
			w.newTab = args[1]
		case "--pane":
			w.pane = args[1]
		default:
			return w, errUsage
		}
		n++
		args = args[2:]
	}
	if n > 1 {
		return w, project.Failf("pass at most one of --tab, --new-tab, --pane")
	}
	return w, nil
}

// agent is who to start: its herdr name, lane type, cwd, role and lane ("" for a role).
type agent struct {
	name, typ, cwd string
	role           roles.Role
	lane           string
}

func (s *Setup) pane(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "" {
		return errUsage
	}
	lane := args[0]
	w, err := parseWhere(args[1:])
	if err != nil {
		return err
	}
	if err := s.load(); err != nil {
		return err
	}
	for _, l := range s.cfg.Lanes() {
		if l.Name != lane {
			continue
		}
		if _, err := os.Stat(l.Dir); err != nil {
			return project.Failf("no lane dir %s (obor setup lane %s %s)", l.Dir, lane, l.Type)
		}
		name := s.cfg.Data.Lanes.M[lane].Agent
		if name == "" {
			name = s.cfg.AgentName("worker", lane)
		}
		worker, _ := roles.Lookup("worker")
		return s.start(ctx, agent{name: name, typ: l.Type, cwd: l.Dir, role: worker, lane: lane}, w)
	}
	return project.Failf("unknown lane %s (obor setup lane %s <type> first)", lane, lane)
}

func (s *Setup) role(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	role, ok := roles.Lookup(strings.ReplaceAll(args[0], "-", "_"))
	if !ok || role.Name == "worker" {
		return errUsage
	}
	w, err := parseWhere(args[1:])
	if err != nil {
		return err
	}
	if err := s.load(); err != nil {
		return err
	}
	rc, _ := s.cfg.Role(role.Name)
	if rc.Type == "" {
		return project.Failf("roles.%s.type is not set (a lane_types key)", role.Name)
	}
	a := agent{name: s.cfg.AgentName(role.Name, ""), typ: rc.Type, cwd: s.root, role: role}
	worktree := ""
	if role.Cwd == roles.CwdRoleWorktree {
		worktree = rc.Worktree
	}
	if err := refuseRoleCollisions(s.cfg, role.Name, worktree, a.name); err != nil {
		return err
	}
	if err := refuseLaneName(s.cfg, role.Name, a.name); err != nil {
		return err
	}
	if worktree != "" {
		// Reviewer and deep dive (T-179): a detached worktree of their own, never a lane.
		a.cwd = filepath.Join(s.cfg.LanesDir(), worktree)
		if err := s.ensureWorktree(a.cwd); err != nil {
			return err
		}
	}
	if role.Name == "triage" {
		if err := s.copyFile(filepath.Join(s.Kit, "templates", "BUGS.md"), "docs/BUGS.md"); err != nil {
			return err
		}
	}
	return s.start(ctx, a, w)
}

// refuseLaneName keeps a role's agent from a lane's bare lowercase name (o-01): no name of the lane since T-302, but
// it reads like one.
func refuseLaneName(cfg *config.Config, role, agent string) error {
	for _, l := range cfg.Lanes() {
		if strings.ToLower(l.Name) == agent {
			return project.Failf("role %s agent %s is named like lane %s", role, agent, l.Name)
		}
	}
	return nil
}

// copyFile copies src to rel under the root unless rel exists.
func (s *Setup) copyFile(src, rel string) error {
	dest := filepath.Join(s.root, rel)
	if _, err := os.Stat(dest); err == nil {
		s.say("skip %s (exists)", rel)
		return nil
	}
	b, err := os.ReadFile(src)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(dest), 0o755); err == nil {
			err = os.WriteFile(dest, b, 0o644)
		}
	}
	if err != nil {
		return project.Failf("cannot write %s", rel)
	}
	s.say("created %s", rel)
	return nil
}

// start places a's pane, labels it and starts the agent (see the package doc); a running agent → OK, nothing done.
func (s *Setup) start(ctx context.Context, a agent, w where) error {
	lt, _ := s.cfg.LaneType(a.typ)
	if lt.Kind == "" {
		return project.Failf("unknown lane type %s (not in lane_types with a kind)", a.typ)
	}
	if !config.AgentNameRE.MatchString(a.name) {
		return project.Failf("bad herdr agent name %s", a.name)
	}
	what := a.lane
	if what == "" {
		what = a.role.Name
	}
	running, err := s.H.Agents(ctx)
	if err != nil {
		return project.Failf("herdr agent list failed (is herdr running?)")
	}
	s.placed, s.running = "", false
	for _, r := range running {
		if r.Name == a.name || (a.lane != "" && r.Cwd == a.cwd) {
			s.placed, s.running = r.Pane, true
			s.say("skip agent start: %s already running (pane %s)", r.Name, r.Pane)
			s.say("OK %s: %s already running", what, r.Name)
			return nil
		}
	}
	s.warnPlugin()
	if lt.Max > 0 {
		n := 0
		names := map[string]bool{}
		for _, r := range roles.All() {
			if r.Name == "lead" {
				continue // the lead is counted below, by its own type
			}
			if rc, _ := s.cfg.Role(r.Name); rc.Configured && rc.Type == a.typ {
				names[s.cfg.AgentName(r.Name, "")] = true
			}
		}
		if rc, _ := s.cfg.Role("lead"); rc.Type == a.typ {
			names[s.cfg.AgentName("lead", "")] = true
		}
		dirs := map[string]bool{}
		for _, l := range s.cfg.Lanes() {
			if l.Type == a.typ {
				dirs[l.Dir] = true
			}
		}
		for _, r := range running {
			if dirs[r.Cwd] || names[r.Name] {
				n++
			}
		}
		if n >= lt.Max {
			return project.Failf("lane type %s is at its max (%d of %d agents running)", a.typ, n, lt.Max)
		}
	}
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return project.Failf("herdr pane list failed")
	}
	label := ""
	for _, p := range panes {
		if p.Label == a.name && p.ID != w.pane {
			return project.Failf("pane %s is already labelled %s (obor restart %s, or herdr pane rename %s --clear)", p.ID,
				a.name, a.name, p.ID)
		}
		if p.ID == w.pane && p.Label != "" && p.Label != a.name {
			return project.Failf("pane %s is labelled %s, not %s (herdr pane rename %s --clear to reuse it)", p.ID, p.Label,
				a.name, p.ID)
		}
		if p.ID == w.pane {
			label = p.Label
		}
	}

	var env []string
	if a.role.EnvValue != "" {
		env = append(env, "OBOR_ROLE="+a.role.EnvValue)
	}
	env = append(env, "OBOR_AGENT="+a.name)
	if a.lane != "" {
		env = append(env, "OBOR_LANE="+a.lane)
	}
	pane := w.pane
	switch {
	case pane != "":
		s.say("pane %s (existing)", pane)
	case w.newTab != "":
		// In the caller's workspace (T-137): without --workspace herdr uses its active one, maybe another project's.
		if pane, err = s.H.NewTab(ctx, s.Env.Get("HERDR_WORKSPACE_ID"), w.newTab, a.cwd, env...); err != nil {
			return project.Failf("herdr tab create failed: %v", err)
		}
		s.say("tab %s: pane %s", w.newTab, pane)
	default:
		if pane, err = s.split(ctx, panes, w.tab, a.cwd, env); err != nil {
			return err
		}
	}

	s.placed = pane
	args := lt.LaunchArgs(a.role.Name == "lead")
	l := s.Starter(s.cfg)
	if w.pane != "" { // a reused pane: its shell may hold the env of the agent that ran there before
		if err := l.SetEnv(ctx, pane, app.AgentEnv{Agent: a.name, Role: a.role.Name, Lane: a.lane, Cwd: a.cwd}); err != nil {
			return err
		}
	}
	err = l.Start(ctx, app.Start{Agent: a.name, Kind: lt.Kind, Pane: pane, Label: label, Args: args, Lane: a.lane,
		Prompt: a.role.StartPrompt(lt.Kind)})
	var se *app.StartError
	switch {
	case errors.As(err, &se) && se.NotReady:
		return project.Failf("agent not ready: %s", se.Hint)
	case errors.As(err, &se):
		return project.Failf("herdr agent start %s failed (pane %s)", a.name, pane)
	case err != nil:
		return err
	}
	s.say("agent %s started (%s, %s)", a.name, lt.Kind, a.typ)
	s.say("OK %s: %s in pane %s", what, a.name, pane)
	return nil
}

// split splits the largest pane of tab (default: the caller's) for a new pane with cwd and env: to the right when it
// is wider than tall on screen (a cell is about twice as tall as wide), else down.
func (s *Setup) split(ctx context.Context, panes []app.PaneInfo, tab, cwd string, env []string) (string, error) {
	if tab == "" {
		self := s.Env.Get("HERDR_PANE_ID")
		if self == "" {
			return "", project.Failf("not in a herdr pane: pass --tab <tab-id>, --new-tab <label> or --pane <id>")
		}
		for _, p := range panes {
			if p.ID == self {
				tab = p.Tab
			}
		}
		if tab == "" {
			return "", project.Failf("pane %s not in herdr pane list: pass --tab or --new-tab", self)
		}
	}
	var in []string
	for _, p := range panes {
		if p.Tab == tab {
			in = append(in, p.ID)
		}
	}
	if len(in) == 0 {
		return "", project.Failf("no panes in tab %s", tab)
	}
	limit := 4
	if l := s.cfg.Data.Limits; l != nil && l.MaxPanesPerTab != nil {
		limit = *l.MaxPanesPerTab
	}
	if len(in) >= limit {
		return "", project.Failf("tab %s is full (%d of %d panes; --new-tab <label>)", tab, len(in), limit)
	}
	layout, err := s.H.Layout(ctx, in[0])
	if err != nil {
		return "", project.Failf("herdr pane layout failed: %v", err)
	}
	target, w, h := "", 0, 0
	for _, p := range layout {
		if p.W*p.H >= w*h { // the last of equals: the newest
			target, w, h = p.Pane, p.W, p.H
		}
	}
	if target == "" {
		return "", project.Failf("herdr pane layout of tab %s lists no panes", tab)
	}
	dir := "down"
	if w > 2*h {
		dir = "right"
	}
	pane, err := s.H.Split(ctx, target, dir, cwd, env...)
	if err != nil {
		return "", project.Failf("herdr pane split %s failed: %v", target, err)
	}
	s.say("split %s in tab %s: pane %s", target, tab, pane)
	return pane, nil
}
