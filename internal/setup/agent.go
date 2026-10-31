package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	fresh := false
	switch {
	case pane != "":
		s.say("pane %s (existing)", pane)
	case w.newTab != "":
		// In the project's workspace (T-628): without one, the caller's; herdr's active one only when the caller has none.
		if pane, err = s.H.NewTab(ctx, s.workspace(panes), w.newTab, a.cwd, env...); err != nil {
			return project.Failf("herdr tab create failed: %v", err)
		}
		s.say("tab %s: pane %s", w.newTab, pane)
		fresh = true
	default:
		tab := w.tab
		if tab == "" {
			if tab, err = s.defaultTab(panes); err != nil {
				return err
			}
		}
		if pane, err = s.split(ctx, panes, tab, a.cwd, env); err != nil {
			return err
		}
		fresh = true
	}

	s.placed = pane
	args := lt.LaunchArgs(a.role.Name == "lead")
	l := s.Starter(s.cfg)
	if w.pane != "" { // a reused pane: its shell may hold the env of the agent that ran there before
		if err := l.SetEnv(ctx, pane, app.AgentEnv{Agent: a.name, Role: a.role.Name, Lane: a.lane, Cwd: a.cwd}); err != nil {
			return err
		}
	}
	run := func() error {
		return l.Start(ctx, app.Start{Agent: a.name, Kind: lt.Kind, Pane: pane, Label: label, Args: args, Lane: a.lane,
			Prompt: a.role.StartPrompt(lt.Kind)})
	}
	// A pane a new tab or a split made is a shell that may still be coming up: herdr answers agent_pane_busy until it
	// is one (T-628, live 2026-10-31). It is used again only once herdr reports a shell there, within the bound.
	deadline := time.Now().Add(s.shellWait())
	err = run()
	for fresh && startCode(err) == "agent_pane_busy" && time.Now().Before(deadline) {
		if !s.waitShell(ctx, pane, deadline) {
			break
		}
		err = run()
	}
	var se *app.StartError
	switch {
	case fresh && startCode(err) == "agent_pane_busy":
		return project.Failf("pane %s is not an available shell after %s: start the agent there by hand "+
			"(herdr agent start %s --kind %s --pane %s)", pane, s.shellWait(), a.name, lt.Kind, pane)
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

// startCode is herdr's error code of a failed start (a StartError wraps herdr's own error), "" when none.
func startCode(err error) string {
	var se *app.StartError
	if errors.As(err, &se) {
		return app.ErrorCode(se.Err)
	}
	return app.ErrorCode(err)
}

// waitShell waits until herdr reports pane an available shell (its shell in the foreground), no longer than until;
// false when the bound came first. It waits one poll before asking: a start herdr answered agent_pane_busy for is
// retried no faster than that.
func (s *Setup) waitShell(ctx context.Context, pane string, until time.Time) bool {
	for {
		left := time.Until(until)
		if left <= 0 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(min(shellPoll, left)):
		}
		if pi, err := s.H.ProcessInfo(ctx, pane); err == nil && pi.AtShell() {
			return true
		}
	}
}

// --- placement (T-628) -------------------------------------------------------------------------------------------

// shellWait bounds the wait for a new pane's shell, and shellPoll is how often herdr is asked meanwhile.
const (
	shellWait = 10 * time.Second
	shellPoll = 100 * time.Millisecond
)

// shellWait is the bound (Setup.ShellWait when set: tests shorten it).
func (s *Setup) shellWait() time.Duration {
	if s.ShellWait > 0 {
		return s.ShellWait
	}
	return shellWait
}

// projectWorkspace is the herdr workspace of this project: the one with most panes whose cwd is the project root or
// under its lanes dir ("" when none). Placement follows it, never the caller's workspace when that is another
// project's (T-628, live: obor init from obor-kit's lead pane put axis-maps' panes in obor-kit's w5).
func (s *Setup) projectWorkspace(panes []app.PaneInfo) string {
	count := map[string]int{}
	for _, p := range panes {
		if p.Workspace != "" && s.inProject(p.Cwd) {
			count[p.Workspace]++
		}
	}
	ws, most := "", 0
	for _, p := range panes { // in herdr's order: the first of equals
		if c := count[p.Workspace]; p.Workspace != "" && s.inProject(p.Cwd) && c > most {
			ws, most = p.Workspace, c
		}
	}
	return ws
}

// splittable: a split may use p. Its cwd is unknown, or in this project: never another project's pane (T-628).
func (s *Setup) splittable(p app.PaneInfo) bool { return p.Cwd == "" || s.inProject(p.Cwd) }

// workspace is where this run's new tabs go: the project's workspace, else the caller's, with one warning line.
func (s *Setup) workspace(panes []app.PaneInfo) string {
	if ws := s.projectWorkspace(panes); ws != "" {
		return ws
	}
	caller := s.Env.Get("HERDR_WORKSPACE_ID")
	if !s.warned {
		s.warned = true
		id := caller
		if id == "" {
			id = "herdr's active workspace"
		}
		s.say("warning: no herdr workspace has a pane in %s: using the caller's (%s)", s.root, id)
	}
	return caller
}

// defaultTab is the tab a split without --tab goes to: the caller's when it is the project's, else the project's tab
// with most panes of it; no pane of the project anywhere: the caller's, with the warning.
func (s *Setup) defaultTab(panes []app.PaneInfo) (string, error) {
	self := s.Env.Get("HERDR_PANE_ID")
	var mine *app.PaneInfo
	for i, p := range panes {
		if self != "" && p.ID == self {
			mine = &panes[i]
		}
	}
	if proj := s.projectWorkspace(panes); proj != "" {
		if mine != nil && mine.Workspace == proj && s.splittable(*mine) && s.hasSplittable(panes, mine.Tab) {
			return mine.Tab, nil
		}
		if tab := s.busiestTab(panes, proj); tab != "" {
			return tab, nil
		}
		return "", project.Failf("no pane of this project to split in workspace %s: pass --new-tab <label> (or --pane <id>)", proj)
	}
	s.workspace(panes) // the no-project-workspace warning, once
	switch {
	case self == "":
		return "", project.Failf("not in a herdr pane: pass --tab <tab-id>, --new-tab <label> or --pane <id>")
	case mine == nil:
		return "", project.Failf("pane %s not in herdr pane list: pass --tab or --new-tab", self)
	case !s.splittable(*mine):
		return "", project.Failf("the caller's pane %s is another project's (cwd %s): pass --new-tab <label> (or --pane <id>)",
			self, mine.Cwd)
	}
	return mine.Tab, nil
}

// busiestTab is the tab of ws with the most panes a split may use ("" when none).
func (s *Setup) busiestTab(panes []app.PaneInfo, ws string) string {
	count := map[string]int{}
	for _, p := range panes {
		if p.Workspace == ws && s.splittable(p) {
			count[p.Tab]++
		}
	}
	tab, most := "", 0
	for _, p := range panes { // in herdr's order: the first of equals
		if c := count[p.Tab]; p.Workspace == ws && s.splittable(p) && c > most {
			tab, most = p.Tab, c
		}
	}
	return tab
}

// hasSplittable: tab holds a pane a split may use.
func (s *Setup) hasSplittable(panes []app.PaneInfo, tab string) bool {
	for _, p := range panes {
		if p.Tab == tab && s.splittable(p) {
			return true
		}
	}
	return false
}

// split splits the largest pane of tab a split may use for a new pane with cwd and env: to the right when it is wider
// than tall on screen (a cell is about twice as tall as wide), else down. Another project's pane is never split.
func (s *Setup) split(ctx context.Context, panes []app.PaneInfo, tab, cwd string, env []string) (string, error) {
	var in []string
	n := 0
	for _, p := range panes {
		if p.Tab != tab {
			continue
		}
		n++
		if s.splittable(p) {
			in = append(in, p.ID)
		}
	}
	if n == 0 {
		return "", project.Failf("no panes in tab %s", tab)
	}
	if len(in) == 0 {
		return "", project.Failf("tab %s holds no pane of this project: pass --new-tab <label> (or --pane <id>)", tab)
	}
	limit := 4
	if l := s.cfg.Data.Limits; l != nil && l.MaxPanesPerTab != nil {
		limit = *l.MaxPanesPerTab
	}
	if n >= limit {
		return "", project.Failf("tab %s is full (%d of %d panes; --new-tab <label>)", tab, n, limit)
	}
	layout, err := s.H.Layout(ctx, in[0])
	if err != nil {
		return "", project.Failf("herdr pane layout failed: %v", err)
	}
	may := map[string]bool{}
	for _, id := range in {
		may[id] = true
	}
	target, w, h := "", 0, 0
	for _, p := range layout {
		if !may[p.Pane] {
			continue
		}
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
