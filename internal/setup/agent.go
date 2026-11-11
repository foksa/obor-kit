package setup

import (
	"cmp"
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

// where is the placement of a new agent: at most one of --tab, --new-tab, --pane (the user's), or obor init's own
// (a group of the role's own, beside a pane). The terminal adapter lays out all but --pane (port.Placement).
type where struct {
	tab, newTab, pane string
	own               bool   // in a group of the role's own
	beside            string // beside this pane when its group is the project's
}

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
	effort         string // the role's effort (roles.<role>.type's `:<effort>`); "": the type's args
	role           roles.Role
	lane           string
	noStart        bool   // label the pane and type its env, start no agent (a pane without --start)
	prompt         string // the first prompt ("": the role's StartPrompt); a drive agent's line
	// again is the command that starts this agent again as it was (a drive's `obor drive <T-xx>`); "": `obor restart
	// <name>`, with --fresh when no session is recorded.
	again string
}

// Drive is the owner's drive agent to start (obor drive): its task, name, lane, worktree, model and effort, and its
// one-line first prompt.
type Drive struct {
	Task, Agent, Lane, Cwd, Type, Effort, Prompt string
}

// StartDrive starts the owner's drive agent d (role driver) in a new pane split in the caller's tab (the project's
// busiest tab when the caller's is none of the project's), labelled d.Agent like a lane pane, with OBOR_ROLE=driver,
// OBOR_LANE. Refused: another agent still runs in the lane's worktree.
func (s *Setup) StartDrive(ctx context.Context, d Drive) error {
	if err := s.load(); err != nil {
		return err
	}
	if d.Task == "" {
		return project.Failf("drive start of %s: no task to name in its recovery command", d.Agent)
	}
	driver, _ := roles.Lookup(roles.Driver)
	return s.start(ctx, agent{name: d.Agent, typ: d.Type, effort: d.Effort, cwd: d.Cwd, role: driver, lane: d.Lane,
		prompt: d.Prompt, again: "obor drive " + d.Task}, where{})
}

func (s *Setup) pane(ctx context.Context, args []string) error {
	args, startNow := withoutFlag(args, "--start")
	if len(args) == 0 || args[0] == "" {
		return errUsage
	}
	w, err := parseWhere(args[1:])
	if err != nil {
		return err
	}
	return s.lanePane(ctx, args[0], w, startNow)
}

// lanePane places lane's worker pane where w says, starting the worker when startNow.
func (s *Setup) lanePane(ctx context.Context, lane string, w where, startNow bool) error {
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
	return project.Failf("unknown lane %s (lanes: %d; obor setup lane %s first)", lane, s.cfg.LaneCount(), lane)
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
	return s.rolePane(ctx, args[0], w, startNow)
}

// rolePane places the role name's pane where w says, starting its agent when startNow.
func (s *Setup) rolePane(ctx context.Context, name string, w where, startNow bool) error {
	if err := s.load(); err != nil {
		return err
	}
	rc, ok := s.cfg.Role(strings.ReplaceAll(name, "-", "_")) // a table role or a custom one
	role := rc.Role
	if !ok || role.Name == "worker" || role.Name == roles.Driver { // a driver is obor drive's
		return errUsage
	}
	if rc.Type == "" {
		return project.Failf("roles.%s is not set (a model ref, e.g. <model>[:<effort>])", role.Name)
	}
	a := agent{name: s.cfg.AgentName(role.Name, ""), typ: rc.Type, effort: rc.Effort, cwd: s.cfg.RoleDir(role), role: role,
		noStart: !startNow}
	if a.cwd != s.cfg.Root { // its role worktree
		if err := s.ensureWorktree(a.cwd); err != nil {
			return err
		}
		if err := s.linkWorktree(a.cwd); err != nil { // an untracked settings file reaches a worktree only as a link
			return err
		}
	}

	for _, rel := range slices.Sorted(maps.Keys(role.Seeds)) { // a custom role's seeded files (triage's docs/BUGS.md)
		if err := s.copyFile(filepath.Join(s.Kit, "templates", role.Seeds[rel]), rel); err != nil {
			return err
		}
	}
	switch role.Name {
	case "reviewer", "deep_dive": // the reviews dir is gitignored, also in a project init made before it was
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

// laneFree refuses a start of lane agent a while another agent runs in a's lane worktree (one agent in a lane): called
// under the lane's restart lock once Locate found a not running, so every agent there is another. The refusal names
// the occupant to stop by its label when the config names that agent, else (a person's label, a pane nobody labelled)
// the pane to quit it in. Nothing is touched.
func (s *Setup) laneFree(ctx context.Context, a agent) error {
	running, err := s.T.Agents(ctx)
	if err != nil {
		return project.Failf("terminal agent list failed (is the terminal running?)")
	}
	for _, r := range running {
		if r.Cwd != a.cwd {
			continue
		}
		who := cmp.Or(r.Label, r.Agent) // its pane's label, else (a pane nobody labelled) its kind
		if s.cfg.NamesOtherAgent(r.Label, a.name) {
			return project.Failf("%s still runs in lane %s (pane %s): obor restart %s --stop first", who, a.lane, r.ID, r.Label)
		}
		return project.Failf("%s still runs in lane %s (pane %s): quit the agent in pane %s first", who, a.lane, r.ID, r.ID)
	}
	return nil
}

// start places a's pane, labels it and starts the agent (see the package doc); a running agent → OK, nothing done.
func (s *Setup) start(ctx context.Context, a agent, w where) error {
	// A lane's model comes with its dispatch (every lane is pooled): --start runs the ref project.LaneModel
	// answers, else the default kind with no model, as obor restart does (launch.dispatchType).
	if a.lane != "" && a.typ == "" {
		if ref, ok := project.LaneModel(s.cfg, a.lane); ok {
			a.typ, a.effort = config.ParseModelRef(ref)
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
		return project.Failf("bad agent name %s", a.name)
	}
	what := a.lane
	if what == "" {
		what = a.role.Name
	}
	s.placed, s.running, s.started, s.idle = "", false, false, false
	// The decision before a pane is placed or labelled (launch's Locate, under the agent's restart lock, its lane's for
	// a lane's agent, held to the start's last step): where the agent runs, else its home or the --pane target. A pane
	// without --start adds no agent: serve down does not refuse its label.
	l := s.Starter(s.cfg)
	loc, end, err := l.Locate(ctx, a.name, w.pane, !a.noStart)
	if err != nil {
		return err
	}
	defer end()
	if loc.Runs {
		s.placed, s.running = loc.Pane, true
		s.say("skip agent start: %s already running (pane %s)", a.name, loc.Pane)
		s.say("OK %s: %s already running", what, a.name)
		return nil
	}
	if a.lane != "" {
		if err := s.laneFree(ctx, a); err != nil {
			return err
		}
	}
	label := loc.Label
	if a.noStart && w.pane == "" && loc.Pane != "" { // a rerun: the pane is there, labelled, with no agent
		s.placed, s.idle = loc.Pane, true
		s.say("skip pane: %s already labelled %s (pane %s)", a.name, a.name, loc.Pane)
		s.say("OK %s: pane %s", what, loc.Pane)
		return nil
	}

	if err := agents.CheckEffort(lt.Kind, a.effort); err != nil { // one the args cannot carry: the start's error
		return project.Failf("%s: %v (obor config validate)", a.name, err)
	}
	// The start, built once: its argv for the start and its env for the new pane or the env line, from one
	// complete start (a plugin kind's one start call).
	prompt := a.prompt
	if prompt == "" {
		prompt = a.role.StartPrompt(lt.Kind)
	}
	built, err := l.Build(port.Build{Agent: a.name, Kind: lt.Kind, Args: lt.Args, Role: a.role.Name, Effort: a.effort,
		Dir: a.cwd, WriteDirs: s.cfg.RoleWriteDirs(a.role), Prompt: prompt})
	if err != nil {
		return project.Failf("%s: %v", a.name, err)
	}
	vars := built.Vars
	// The one builder of a start's env (agents.StartVars), as the env line of a reused pane: herdr's pane env
	// cannot unset, so every registry var the start does not set goes to herdr empty, none to obor (the pane may inherit
	// setup's own env: an OpenCode agent running setup; a stale OBOR_AGENT; the caller's OBOR_PANE: the
	// new pane's id is herdr's answer, exported by the env line before a start).
	if a.lane != "" { // a lane's env: the generic facts and the project's lanes.env
		vars = config.WithLaneEnv(vars, s.cfg)
	}
	set, unset := agents.StartVars(a.role.EnvValue, a.lane, "", vars)
	var env []string
	for _, v := range set {
		env = append(env, v.Name+"="+v.Value)
	}
	for _, v := range unset {
		env = append(env, v+"=")
	}
	pane := loc.Pane // the --pane target, else the agent's home (the pane labelled with its name at its shell)
	fresh := pane == ""
	if fresh {
		if pane, err = s.T.Place(ctx, s.placement(a, w, env)); err != nil {
			return err
		}
	} else {
		s.say("pane %s (existing)", pane)
	}

	s.placed = pane
	setEnv := func() error {
		return l.SetEnv(ctx, pane, port.AgentEnv{Agent: a.name, Role: a.role.Name, Lane: a.lane, Cwd: a.cwd, Vars: vars})
	}
	if !fresh { // a reused pane: its shell may hold the env of the agent that ran there before
		if err := setEnv(); err != nil {
			return err
		}
	}
	if a.noStart {
		if label != a.name {
			if err := s.T.RenamePane(ctx, pane, a.name); err != nil {
				return project.Failf("terminal pane rename %s %s failed", pane, a.name)
			}
			s.say("labelled pane %s %s", pane, a.name)
		}
		s.say("pane %s ready for %s, no agent started (started on first use; --start starts it now)", pane, a.name)
		s.say("OK %s: pane %s", what, pane)
		return nil
	}
	s.started = true
	// A pane a new tab or a split made is a shell that may still be coming up: herdr answers agent_pane_busy until it
	// is one (live 2026-10-31). The start waits for it within the bound, then types the env line (its id is
	// known now: the env line exports OBOR_PANE before the start) and retries a busy answer within the same
	// bound (launch's one start). A reused pane's env line is typed above, and its start is not retried.
	st := port.Start{Agent: a.name, Kind: lt.Kind, Pane: pane, Label: label, Args: built.Args, Session: built.Session,
		Lane: a.lane, Prompt: prompt, Again: a.again}
	if fresh {
		st.Shell, st.Await, st.Env = s.shellWait(), true, func(context.Context) error { return setEnv() }
	}
	err = l.Start(ctx, st)
	var se *port.StartError
	switch {
	case errors.As(err, &se) && se.NoShell, fresh && startBusy(err):
		return s.notShell(pane, a)
	case errors.As(err, &se) && se.NotReady:
		return project.Failf("agent not ready: %s", se.Hint)
	case errors.As(err, &se):
		return project.Failf("terminal agent start %s failed (pane %s)", a.name, pane)
	case err != nil:
		return err
	}
	s.say("agent %s started (%s, %s)", a.name, lt.Kind, a.typ)
	s.say("OK %s: %s in pane %s", what, a.name, pane)
	return nil
}

// startBusy: err is a start the terminal refused because the pane is not an available shell (launch's start wraps the
// terminal's own error in a StartError).
func startBusy(err error) bool {
	var se *port.StartError
	if errors.As(err, &se) {
		return port.PaneBusy(se.Err)
	}
	return port.PaneBusy(err)
}

// notShell is the refusal for a new pane whose shell did not come up within the bound: the command it prints starts
// the agent as it was — a drive's own command, or a restart with --fresh when no session is recorded (the plain form
// refuses with none).
func (s *Setup) notShell(pane string, a agent) error {
	_, hasSession := store.CurrentAt(s.cfg.Root, a.name)
	return project.Failf("pane %s is not an available shell after %s: start the agent there with %s",
		pane, s.shellWait(), project.RestartCmd(a.again, a.name, hasSession, pane))
}

// --- placement -------------------------------------------------------------------------------------------

// shellWait bounds the wait for a new pane's shell and the start's agent_pane_busy retries (port.Start.Shell): a new
// pane's shell is up in moments, unlike a restart's, which waits for the old agent's exit to be reaped.
const shellWait = 10 * time.Second

// shellWait is the bound (Setup.ShellWait when set: tests shorten it).
func (s *Setup) shellWait() time.Duration {
	if s.ShellWait > 0 {
		return s.ShellWait
	}
	return shellWait
}

// maxPanesPerTab is how many panes a tab holds: a constant, not a config key.
const maxPanesPerTab = 4

// placement is a's new pane as the terminal is asked for it: whose it is, its cwd and env, where w says, and the
// project it is for (the layout is the adapter's). A tab holds maxPanesPerTab panes.
func (s *Setup) placement(a agent, w where, env []string) port.Placement {
	name := s.cfg.ProjectName()
	self, _ := project.Pane(s.Env) // "" when the caller has no pane
	return port.Placement{Role: a.role.Name, Lane: a.lane, Cwd: a.cwd, Env: env, Own: w.own, Group: w.newTab, In: w.tab,
		Beside: w.beside, Say: func(line string) { s.say("%s", line) },
		Project: port.PlaceProject{Name: name, Root: s.root, Self: self, MaxPanes: maxPanesPerTab, Has: s.inProject}}
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
