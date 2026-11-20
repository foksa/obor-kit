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
	"syscall"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
)

// where is the placement of a new agent: at most one of --tab, --new-tab, --pane; none places it by its kind. The
// terminal adapter lays out all but --pane (port.Placement).
type where struct {
	tab, newTab, pane string
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

// agent is who to start: its name, cwd, role and lane ("" for a role).
type agent struct {
	name, cwd string
	// typ and effort are a drive's model and effort (its dispatch's); "": the launcher's (a lane's dispatch, a role's
	// roles.<role>).
	typ, effort string
	role        roles.Role
	lane        string
	noStart     bool   // label the pane and type its env, start no agent (a pane without --start)
	prompt      string // the first prompt ("": the role's StartPrompt); a drive agent's line
	// again is the command that starts this agent again as it was (a drive's `obor drive <T-xx>`); "": `obor restart
	// <name>`, with --fresh when no session is recorded.
	again string
}

// Drive is the owner's drive agent to start (obor drive): its task, name, lane, worktree, model and effort, and its
// one-line first prompt.
type Drive struct {
	Task, Agent, Lane, Cwd, Type, Effort, Prompt string
}

// StartDrive starts the owner's drive agent d (role driver) in a new pane placed as a lane's (a lanes tab of the
// project), labelled d.Agent like a lane pane, with OBOR_ROLE=driver, OBOR_LANE. Refused: another agent still runs in the lane's worktree.
func (s *Setup) StartDrive(ctx context.Context, d Drive) error {
	if err := s.load(); err != nil {
		return err
	}
	if d.Task == "" {
		return project.Failf("drive start of %s: no task to name in its recovery command", d.Agent)
	}
	driver, _ := roles.Lookup(roles.Driver)
	_, err := s.start(ctx, agent{name: d.Agent, typ: d.Type, effort: d.Effort, cwd: d.Cwd, role: driver, lane: d.Lane,
		prompt: d.Prompt, again: "obor drive " + d.Task}, where{})
	return err
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
	_, err = s.lanePane(ctx, args[0], w, startNow)
	return err
}

// lanePane places lane's worker pane where w says, starting the worker when startNow.
func (s *Setup) lanePane(ctx context.Context, lane string, w where, startNow bool) (placed, error) {
	if err := s.load(); err != nil {
		return placed{}, err
	}
	l, err := s.laneWorktree(lane)
	if err != nil {
		return placed{}, err
	}
	worker, _ := roles.Lookup("worker")
	return s.start(ctx, agent{name: s.cfg.AgentName("worker", lane), cwd: l.Dir, role: worker, lane: lane,
		noStart: !startNow}, w)
}

// laneWorktree is the configured lane lane and its worktree (which must be there): unknown lane, or no dir, is its
// refusal.
func (s *Setup) laneWorktree(lane string) (config.Lane, error) {
	for _, l := range s.cfg.Lanes() {
		if l.Name != lane {
			continue
		}
		if _, err := os.Stat(l.Dir); err != nil {
			return config.Lane{}, project.Failf("no lane dir %s (obor setup lane %s)", l.Dir, lane)
		}
		return l, nil
	}
	return config.Lane{}, project.Failf("unknown lane %s (lanes: %d; obor setup lane %s first)", lane, s.cfg.LaneCount(), lane)
}

// PlaceLane places a pane for lane's worker in the lane's worktree, labelled with the lane's agent and with no agent in
// it: obor setup pane <LANE>'s placement (a lanes tab of the project, the pane in the lane, its label the worker's
// name). serve calls it at a worker's start when its lane has no pane; the pane is in the lane's cwd as it returns
// (the terminal readies it: Placement.Starts is unset), and the start that wanted it runs there next, typing the
// worker's env line (launch). The lane must be configured and its worktree there; a terminal that
// refuses the placement is its error.
func (s *Setup) PlaceLane(ctx context.Context, lane string) (string, error) {
	if err := s.load(); err != nil {
		return "", err
	}
	l, err := s.laneWorktree(lane)
	if err != nil {
		return "", err
	}
	// The layout is chosen against the panes as they are now: a long-lived terminal adapter keeps its last read.
	if _, err := s.T.Panes(ctx); err != nil {
		return "", project.Failf("terminal pane list failed (is the terminal running?)")
	}
	a := agent{name: s.cfg.AgentName("worker", lane), cwd: l.Dir, lane: lane}
	p := s.placement(a, where{}, nil)
	pane, err := s.T.Place(ctx, p)
	if err != nil {
		return "", err
	}
	if err := s.T.RenamePane(ctx, pane, a.name); err != nil {
		return "", project.Failf("terminal pane rename %s %s failed", pane, a.name)
	}
	s.say("pane %s placed in lane %s for %s", pane, lane, a.name)
	return pane, nil
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
	_, err = s.rolePane(ctx, args[0], w, startNow)
	return err
}

// rolePane places the role name's pane where w says, starting its agent when startNow (its model the launcher's:
// roles.<role>).
func (s *Setup) rolePane(ctx context.Context, name string, w where, startNow bool) (placed, error) {
	if err := s.load(); err != nil {
		return placed{}, err
	}
	rc, ok := s.cfg.Role(strings.ReplaceAll(name, "-", "_")) // a table role or a custom one
	role := rc.Role
	if !ok || role.Name == "worker" || role.Name == roles.Driver { // a driver is obor drive's
		return placed{}, errUsage
	}
	if rc.Type == "" {
		return placed{}, project.Failf("roles.%s is not set (a model ref, e.g. <model>[:<effort>])", role.Name)
	}
	a := agent{name: s.cfg.AgentName(role.Name, ""), cwd: s.cfg.RoleDir(role), role: role, noStart: !startNow}
	if a.cwd != s.cfg.Root { // its role worktree
		if err := s.ensureWorktree(a.cwd); err != nil {
			return placed{}, err
		}
		if err := s.linkWorktree(a.cwd); err != nil { // an untracked settings file reaches a worktree only as a link
			return placed{}, err
		}
	}

	for _, rel := range slices.Sorted(maps.Keys(role.Seeds)) { // a custom role's seeded files (triage's docs/BUGS.md)
		if err := s.copyFile(filepath.Join(s.Kit, "templates", role.Seeds[rel]), rel); err != nil {
			return placed{}, err
		}
	}
	switch role.Name {
	case "reviewer", "deep_dive": // the reviews dir is gitignored, also in a project init made before it was
		if err := s.ignoreLines(s.reviewsIgnore()); err != nil {
			return placed{}, err
		}
	}
	return s.start(ctx, a, w)
}

// LeadHere offers the caller's pane to the lead as its home (obor lead when the lead has no pane): the caller is no
// agent (no OBOR_ROLE), sits in a pane of the project's terminal (project.PaneOf), and the terminal takes that pane
// (port.TerminalSetup.TakeLead: labelled the lead's, its tab named lead, its group the project's). It answers the
// pane when taken ("": the lead gets a new tab) and the line saying what was done or why the pane stays as it is.
func (s *Setup) LeadHere(ctx context.Context) (string, string, error) {
	if err := s.load(); err != nil {
		return "", "", err
	}
	p, why := s.leadPane()
	if why != "" {
		return "", why, nil
	}
	t, err := s.T.TakeLead(ctx, p)
	if err != nil {
		return "", "", err
	}
	if !t.Taken {
		return "", t.Line, nil
	}
	return p.Pane, t.Line, nil
}

// leadPane is the caller's pane as offered to the lead (port.LeadPane, Caller this process's group: the obor command
// holding the pane's foreground), or why it is not offered: the caller is an agent (OBOR_ROLE), or it runs in no pane
// of the project's terminal (project.PaneOf).
func (s *Setup) leadPane() (port.LeadPane, string) {
	if role := s.Env.Get(agents.RoleVar); role != "" {
		return port.LeadPane{}, "this pane is a " + role + "'s (" + agents.RoleVar + " is set)"
	}
	pane, err := project.PaneOf(s.cfg, s.Env)
	if err != nil {
		return port.LeadPane{}, "this shell runs in no pane of terminal " + s.cfg.TerminalKind()
	}
	return port.LeadPane{Pane: pane, Agent: s.cfg.AgentName("lead", ""), Project: PlaceProjectOf(s.cfg, s.root),
		Caller: syscall.Getpgrp()}, ""
}

// initLeadSpace is obor init's decision on the caller's pane, made before any pane is placed: when the pane qualifies
// as the lead's (leadPane, port.TerminalSetup.LeadSpace), the terminal readies its group for the project (hc: the
// caller's space named after it), so the lanes and roles are placed there and the lead's start takes the pane. What
// it did, "" when nothing.
func (s *Setup) initLeadSpace(ctx context.Context) (string, error) {
	p, why := s.leadPane()
	if why != "" {
		return "", nil
	}
	t, err := s.T.LeadSpace(ctx, p)
	if err != nil || !t.Taken {
		return "", err
	}
	return t.Line, nil
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

// placed is a start's outcome (obor init's summary reads it): the agent's pane, and what was there or done.
type placed struct {
	pane    string
	running bool // the agent ran already: nothing done
	idle    bool // a pane labelled for the agent was there already, no agent in it (a rerun without --start)
	started bool // the agent was started (a pane without --start starts none)
}

// start starts a's agent where w says through the launcher's one start (port.Launcher.Launch): a fresh session, its
// first prompt a's own (else the role's), a new pane placed by w and the agent's kind when it has none, refused rather
// than stopping what runs (NoStop): the agent itself running → OK, nothing done. A pane without --start is the same
// launch with NoAgent: the launcher labels the pane and types its env, no agent started; its labelled pane there
// already (a rerun) → OK, nothing done.
func (s *Setup) start(ctx context.Context, a agent, w where) (placed, error) {
	what := cmp.Or(a.lane, a.role.Name)
	l := s.Launcher(s.cfg)
	place := func(ctx context.Context, env []string) (string, error) {
		p := s.placement(a, w, env)
		p.Starts = !a.noStart // the start types the env line: the pane gets it once (no start: the terminal readies it)
		return s.T.Place(ctx, p)
	}
	st, err := l.Launch(ctx, port.Launch{Agent: a.name, Pane: w.pane, Fresh: true, Prompt: a.prompt, Type: a.typ,
		Effort: a.effort, Again: a.again, NoStop: true, NoAgent: a.noStart, Place: &port.Placer{Place: place}})
	var running *port.Running
	switch {
	case errors.As(err, &running):
		s.say("skip agent start: %s already running (pane %s)", a.name, running.Pane)
		s.say("OK %s: %s already running", what, a.name)
		return placed{pane: running.Pane, running: true}, nil
	case port.TrustHeld(err) != nil: // started, held by the dialog: a failed start to setup, as the launcher words one
		return placed{}, project.Failf("start failed in %s (%s)", st.Pane, port.TrustHeld(err).Hint)
	case err != nil:
		return placed{}, err
	case st.Idle: // a rerun: the pane is there, labelled, with no agent
		s.say("skip pane: %s already labelled %s (pane %s)", a.name, a.name, st.Pane)
		s.say("OK %s: pane %s", what, st.Pane)
		return placed{pane: st.Pane, idle: true}, nil
	case a.noStart:
		s.say("pane %s ready for %s, no agent started (started on first use; --start starts it now)", st.Pane, a.name)
		s.say("OK %s: pane %s", what, st.Pane)
		return placed{pane: st.Pane}, nil
	}
	s.say("agent %s started (%s)", a.name, st.Kind)
	s.say("OK %s: %s in pane %s", what, a.name, st.Pane)
	return placed{pane: st.Pane, started: true}, nil
}

// --- placement -------------------------------------------------------------------------------------------

// maxPanesPerTab is how many panes a tab holds: a constant, not a config key.
const maxPanesPerTab = 4

// placement is a's new pane as the terminal is asked for it: whose it is, its kind, its cwd and env, where w says,
// and the project it is for (where it goes is app/layout's decision). A tab holds maxPanesPerTab panes.
func (s *Setup) placement(a agent, w where, env []string) port.Placement {
	kind := port.KindRole
	switch {
	case a.lane != "":
		kind = port.KindLane
	case a.role.Name == "lead":
		kind = port.KindLead
	}
	return port.Placement{Kind: kind, Cwd: a.cwd, Env: env, Group: w.newTab, In: w.tab,
		Say:     func(line string) { s.say("%s", line) },
		Project: PlaceProjectOf(s.cfg, s.root)}
}

// PlaceProjectOf is the project at root as a terminal places its panes (serve's adoption of hidden terminals asks the
// same of the terminal as setup does).
func PlaceProjectOf(cfg *config.Config, root string) port.PlaceProject {
	s := &Setup{cfg: cfg, root: root}
	return port.PlaceProject{Name: cfg.ProjectName(), Root: root, MaxPanes: maxPanesPerTab, Has: s.inProject,
		KindOf: s.paneKind}
}

// paneKind is the kind of the pane labelled label: a lane's (its worker, its drive agent), the lead's, another role's;
// "" when label names no agent of the project.
func (s *Setup) paneKind(label string) port.PaneKind {
	info, ok := s.cfg.AgentLookup(label, "")
	switch {
	case !ok:
		return ""
	case info.Lane != "":
		return port.KindLane
	case info.Role == "lead":
		return port.KindLead
	}
	return port.KindRole
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
