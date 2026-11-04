package setup

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/store"
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

// agent is who to start: its herdr name, model, cwd, role and lane ("" for a role).
type agent struct {
	name, typ, cwd string
	effort         string // the role's effort (roles.<role>.type's `:<effort>`, T-695); "": the type's args
	role           roles.Role
	lane           string
	noStart        bool   // label the pane and type its env, start no agent (T-705/T-708: a pane without --start)
	prompt         string // the first prompt ("": the role's StartPrompt); a drive agent's line (T-751)
}

// Drive is the owner's drive agent to start (obor drive, T-751): its name, lane, worktree, model and effort, and its
// one-line first prompt.
type Drive struct {
	Agent, Lane, Cwd, Type, Effort, Prompt string
}

// StartDrive starts the owner's drive agent d (role driver) in a new pane split in the caller's tab (the project's
// busiest tab when the caller's is none of the project's), labelled d.Agent like a lane pane, with OBOR_ROLE=driver,
// OBOR_LANE. Refused: another agent still runs in the lane's worktree.
func (s *Setup) StartDrive(ctx context.Context, d Drive) error {
	if err := s.load(); err != nil {
		return err
	}
	driver, _ := roles.Lookup(roles.Driver)
	return s.start(ctx, agent{name: d.Agent, typ: d.Type, effort: d.Effort, cwd: d.Cwd, role: driver, lane: d.Lane,
		prompt: d.Prompt}, where{})
}

func (s *Setup) pane(ctx context.Context, args []string) error {
	args, startNow := withoutFlag(args, "--start")
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
			return project.Failf("no lane dir %s (obor setup lane %s)", l.Dir, lane)
		}
		worker, _ := roles.Lookup("worker")
		return s.start(ctx, agent{name: s.cfg.AgentName("worker", lane), cwd: l.Dir, role: worker, lane: lane,
			noStart: !startNow}, w)
	}
	return project.Failf("unknown lane %s (lanes: %d; obor setup lane %s first)", lane, s.cfg.Data.Lanes, lane)
}

func (s *Setup) role(ctx context.Context, args []string) error {
	args, startNow := withoutFlag(args, "--start")
	if len(args) == 0 {
		return errUsage
	}
	w, err := parseWhere(args[1:])
	if err != nil {
		return err
	}
	if err := s.load(); err != nil {
		return err
	}
	rc, ok := s.cfg.Role(strings.ReplaceAll(args[0], "-", "_")) // a table role or a custom one (T-801)
	role := rc.Role
	if !ok || role.Name == "worker" || role.Name == roles.Driver { // a driver is obor drive's (T-751)
		return errUsage
	}
	if rc.Type == "" {
		return project.Failf("roles.%s is not set (a model ref, e.g. \"opus:high\")", role.Name)
	}
	a := agent{name: s.cfg.AgentName(role.Name, ""), typ: rc.Type, effort: rc.Effort, cwd: s.root, role: role, noStart: !startNow}
	for _, rel := range slices.Sorted(maps.Keys(role.Seeds)) { // a custom role's seeded files (triage's docs/BUGS.md)
		if err := s.copyFile(filepath.Join(s.Kit, "templates", role.Seeds[rel]), rel); err != nil {
			return err
		}
	}
	switch role.Name {
	case "reviewer", "deep_dive": // T-731: the reviews dir is gitignored, also in a project init made before it was
		if err := s.ignoreLines(s.reviewsIgnore()); err != nil {
			return err
		}
	}
	return s.start(ctx, a, w)
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
	// A lane's model comes with its dispatch (T-765: every lane is pooled): --start runs the model the lane row's open
	// dispatch names, else the default kind with no model, as obor restart does (launch.dispatchType).
	if a.lane != "" && a.typ == "" {
		if row, _, _ := store.ReadLaneAt(s.root, a.lane); row.Task != "" {
			a.typ, a.effort = config.ParseModelRef(row.Type)
		}
	}
	lt, ok := s.cfg.Model(a.typ)
	switch {
	case a.typ == "" && a.lane != "":
		lt = config.Model{Kind: agents.Default}
	case !ok || lt.Kind == "":
		return project.Failf("unknown model %s (models: %s)", a.typ, strings.Join(s.cfg.ModelNames(), ", "))
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
	s.placed, s.running, s.started, s.idle = "", false, false, false
	for _, r := range running {
		if a.role.Name == roles.Driver && r.Name != a.name && r.Cwd == a.cwd { // T-751: one agent in a lane
			return project.Failf("%s still runs in lane %s (pane %s): obor restart %s --stop first", r.Name, a.lane, r.Pane,
				r.Name)
		}
		if r.Name == a.name || (a.lane != "" && r.Cwd == a.cwd) {
			s.placed, s.running = r.Pane, true
			s.say("skip agent start: %s already running (pane %s)", r.Name, r.Pane)
			s.say("OK %s: %s already running", what, r.Name)
			return nil
		}
	}
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return project.Failf("herdr pane list failed")
	}
	label := ""
	for _, p := range panes {
		if a.noStart && w.pane == "" && p.Label == a.name { // a rerun: the pane is there, labelled, with no agent (T-705)
			s.placed, s.idle = p.ID, true
			s.say("skip pane: %s already labelled %s (pane %s)", a.name, a.name, p.ID)
			s.say("OK %s: pane %s", what, p.ID)
			return nil
		}
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

	if err := agents.CheckEffortArgs(lt.Kind, lt.Args, a.effort); err != nil {
		return project.Failf("%s: %v (obor config validate)", a.name, err)
	}
	vars, err := lt.StartEnv(agents.Start{Effort: a.effort, Read: os.ReadFile, Env: s.Env.Get})
	if err != nil {
		return project.Failf("%s: %v", a.name, err)
	}
	var env []string
	// An empty value goes to herdr too: the pane may inherit setup's own env (an OpenCode agent running setup), and an
	// empty OBOR_OPENCODE_MODEL is none to obor's plugin (T-703).
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		env = append(env, k+"="+vars[k])
	}
	if a.role.EnvValue != "" {
		env = append(env, "OBOR_ROLE="+a.role.EnvValue)
	}
	if a.lane != "" {
		env = append(env, "OBOR_LANE="+a.lane)
	}
	pane := w.pane
	fresh := false
	switch {
	case pane != "":
		s.say("pane %s (existing)", pane)
	case w.newTab != "":
		if pane, err = s.newTab(ctx, panes, w.newTab, a.cwd, env); err != nil {
			return err
		}
		fresh = true
	default:
		tab := w.tab
		if tab == "" {
			tab = s.defaultTab(panes)
		}
		if tab == "" { // no tab of the project to split: a new one in its workspace (T-632)
			label := a.role.Name
			if a.lane != "" {
				label = "workers"
			}
			pane, err = s.newTab(ctx, panes, label, a.cwd, env)
		} else {
			pane, err = s.split(ctx, panes, tab, a.cwd, env)
		}
		if err != nil {
			return err
		}
		fresh = true
	}

	s.placed = pane
	args := lt.LaunchArgs(a.role.Name, a.effort)
	l := s.Starter(s.cfg)
	if w.pane != "" { // a reused pane: its shell may hold the env of the agent that ran there before
		if err := l.SetEnv(ctx, pane, port.AgentEnv{Agent: a.name, Role: a.role.Name, Lane: a.lane, Cwd: a.cwd, Vars: vars}); err != nil {
			return err
		}
	}
	if a.noStart {
		if label != a.name {
			if err := s.H.RenamePane(ctx, pane, a.name); err != nil {
				return project.Failf("herdr pane rename %s %s failed", pane, a.name)
			}
			s.say("labelled pane %s %s", pane, a.name)
		}
		s.say("pane %s ready for %s, no agent started (started on first use; --start starts it now)", pane, a.name)
		s.say("OK %s: pane %s", what, pane)
		return nil
	}
	s.started = true
	run := func() error {
		prompt := a.prompt
		if prompt == "" {
			prompt = a.role.StartPrompt(lt.Kind)
		}
		return l.Start(ctx, port.Start{Agent: a.name, Kind: lt.Kind, Pane: pane, Label: label, Args: args, Lane: a.lane,
			Prompt: prompt})
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
	var se *port.StartError
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
	var se *port.StartError
	if errors.As(err, &se) {
		return port.ErrorCode(se.Err)
	}
	return port.ErrorCode(err)
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

// projectWorkspace is the herdr workspace of this project ("" when none): one with a pane whose cwd is the project
// root or under its lanes dir and no pane with a known cwd elsewhere (T-634: a shell of another project's workspace
// that cd's into this one does not make that workspace the project's); an unknown cwd counts neither way. Several:
// the one with most project panes, the first of equals in herdr's order. Placement follows it, never the caller's
// workspace when that is another project's (T-628, live: obor init from obor-kit's lead pane put axis-maps' panes in
// obor-kit's w5).
func (s *Setup) projectWorkspace(panes []port.PaneInfo) string {
	count, foreign := map[string]int{}, map[string]bool{}
	for _, p := range panes {
		switch {
		case p.Workspace == "" || p.Cwd == "":
		case s.inProject(p.Cwd):
			count[p.Workspace]++
		default:
			foreign[p.Workspace] = true
		}
	}
	ws, most := "", 0
	for _, p := range panes { // in herdr's order: the first of equals
		if c := count[p.Workspace]; c > most && !foreign[p.Workspace] {
			ws, most = p.Workspace, c
		}
	}
	return ws
}

// splittable: a split of a named tab (--tab) may use p. Its cwd is unknown, or in this project: never another
// project's pane (T-628).
func (s *Setup) splittable(p port.PaneInfo) bool { return p.Cwd == "" || s.inProject(p.Cwd) }

// projectTab: every pane of tab has a known cwd in this project, so placement may pick it on its own (T-632). A pane
// with an unknown cwd may be another project's.
func (s *Setup) projectTab(panes []port.PaneInfo, tab string) bool {
	n := 0
	for _, p := range panes {
		if p.Tab != tab {
			continue
		}
		if !s.inProject(p.Cwd) {
			return false
		}
		n++
	}
	return n > 0
}

// workspace is where this run's new tabs go: the project's workspace; none (no herdr workspace holds a pane of the
// project) → a new workspace named after the project, its first pane a shell in the root, made once a run (T-632).
// The caller's or herdr's active workspace is never a fallback: it may be another project's.
func (s *Setup) workspace(ctx context.Context, panes []port.PaneInfo) (string, error) {
	if ws := s.projectWorkspace(panes); ws != "" {
		return ws, nil
	}
	if s.madeWS != "" {
		return s.madeWS, nil
	}
	name := s.cfg.Data.Name
	if name == "" {
		name = filepath.Base(s.root)
	}
	ws, err := s.H.NewWorkspace(ctx, name, s.root)
	if err != nil {
		return "", project.Failf("herdr workspace create failed: %v", err)
	}
	s.madeWS = ws
	s.say("workspace %s created for %s", ws, name)
	return ws, nil
}

// newTab makes a tab labelled label in the project's workspace (made first when there is none), its pane in cwd with
// env; the pane's id.
func (s *Setup) newTab(ctx context.Context, panes []port.PaneInfo, label, cwd string, env []string) (string, error) {
	ws, err := s.workspace(ctx, panes)
	if err != nil {
		return "", err
	}
	pane, err := s.H.NewTab(ctx, ws, label, cwd, env...)
	if err != nil {
		return "", project.Failf("herdr tab create failed: %v", err)
	}
	s.say("tab %s: pane %s", label, pane)
	return pane, nil
}

// defaultTab is the tab a split without --tab goes to: the caller's when it is a project tab in the project's
// workspace, else that workspace's project tab with most panes; "" when none (a new tab then, T-632).
func (s *Setup) defaultTab(panes []port.PaneInfo) string {
	proj := s.projectWorkspace(panes)
	if proj == "" {
		return ""
	}
	if self := s.Env.Get("HERDR_PANE_ID"); self != "" {
		for _, p := range panes {
			if p.ID == self && p.Workspace == proj && s.projectTab(panes, p.Tab) {
				return p.Tab
			}
		}
	}
	return s.busiestTab(panes, proj)
}

// busiestTab is the project tab of ws with the most panes ("" when none).
func (s *Setup) busiestTab(panes []port.PaneInfo, ws string) string {
	count := map[string]int{}
	for _, p := range panes {
		if p.Workspace == ws {
			count[p.Tab]++
		}
	}
	tab, most := "", 0
	for _, p := range panes { // in herdr's order: the first of equals
		if c := count[p.Tab]; p.Workspace == ws && c > most && s.projectTab(panes, p.Tab) {
			tab, most = p.Tab, c
		}
	}
	return tab
}

// split splits the largest pane of tab a split may use for a new pane with cwd and env: to the right when it is wider
// than tall on screen (a cell is about twice as tall as wide), else down. Another project's pane is never split.
func (s *Setup) split(ctx context.Context, panes []port.PaneInfo, tab, cwd string, env []string) (string, error) {
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

// withoutFlag is args without the bare flag, and whether it was there.
func withoutFlag(args []string, flag string) ([]string, bool) {
	var rest []string
	found := false
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, found
}
