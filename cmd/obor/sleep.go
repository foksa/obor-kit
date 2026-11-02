package main

// obor stop / obor start (T-709): put a project to sleep and wake it. A stopped project runs no serve and no agents,
// is not in the machine's project list, and its hooks stand down (project.Stopped); its files stay.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/service"
)

const (
	stopUsage  = `usage: obor stop ["<why>"] [--force]`
	startUsage = "usage: obor start"
)

// busyTask is a task in progress: a working, gating or merging lane, or a waiting dispatch (no lane).
type busyTask struct{ Task, Lane, State string }

// sleeper is one obor stop or obor start in the project of Cfg; the funcs are its outside systems (tests fake them).
type sleeper struct {
	Cfg *config.Config
	Env project.Env
	Out io.Writer
	H   port.PaneControl // herdr: the project's agent panes
	// Ask is serve's status, Drop one drop request to serve (nil: the project's socket); Stop stops one agent (nil:
	// launch.Launcher.Stop).
	Ask   func(ctx context.Context) (wire.Status, error)
	Drop  func(ctx context.Context, c wire.Call) (string, error)
	Stop  func(ctx context.Context, agent, pane string, force bool) (string, error)
	Now   func() time.Time
	Force bool
	Why   string
}

func (s *sleeper) say(format string, a ...any) { fmt.Fprintf(s.Out, format+"\n", a...) }

// stopCmd is `obor stop ["<why>"] [--force]` in a project: its steps, one line each, then STOPPED <name> (or FAIL,
// exit 1).
func stopCmd(args []string) int {
	s := &sleeper{Env: project.OSEnv, Out: out, Why: "obor stop"}
	why := ""
	for _, a := range args {
		switch {
		case a == "--force":
			s.Force = true
		case strings.HasPrefix(a, "-") || why != "":
			fmt.Fprintln(out, "FAIL "+stopUsage)
			return 1
		default:
			why = a
		}
	}
	if why != "" {
		s.Why = why
	}
	if !s.load() {
		return 1
	}
	s.H = herdrSocket(s.Env)
	return s.stop(context.Background())
}

// startCmd is `obor start` in a project: its steps, then STARTED <name> (or FAIL, exit 1).
func startCmd(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(out, "FAIL "+startUsage)
		return 1
	}
	s := &sleeper{Env: project.OSEnv, Out: out}
	if !s.load() {
		return 1
	}
	return s.start(context.Background())
}

// load reads the project's config as the readers do (an unknown key is a warning: stop must work on a config newer
// than this binary).
func (s *sleeper) load() bool {
	cfg, warn, rc := loadReader()
	if rc != 0 {
		fmt.Fprintln(s.Out, "FAIL not in an obor project (no loadable .obor/config.json)")
		return false
	}
	if warn != "" {
		s.say("%s", warn)
	}
	s.Cfg = cfg
	return true
}

func (s *sleeper) name() string { return s.Cfg.ProjectName() }

// stop is obor stop's steps, in order: the tasks in progress (refused, or with --force dropped), every agent obor
// started in the project but the caller's own, serve's launchd agent, the machine's project list, the marker. Each is
// idempotent, so a second stop says each is done already.
func (s *sleeper) stop(ctx context.Context) int {
	root := s.Cfg.Root
	busy, serveUp := s.busy(ctx)
	if len(busy) > 0 && !s.Force {
		fmt.Fprintf(s.Out, "FAIL tasks in progress: %s: finish or drop them, or obor stop --force (drops each)\n",
			busyList(busy))
		return 1
	}
	for _, b := range busy {
		if !serveUp {
			s.say("serve is down: %s not dropped (its row stays; obor drop it once serve runs)", busyList([]busyTask{b}))
			continue
		}
		c := caller(s.Env, root, s.Cfg) // as obor drop calls it
		c.Note, c.Force = "obor stop --force: "+s.Why, true
		if b.Lane != "" {
			c.Lane = b.Lane
		} else {
			c.Task = b.Task
		}
		reply, err := s.drop(ctx, c)
		if err != nil {
			fmt.Fprintf(s.Out, "FAIL drop %s: %v\n", busyList([]busyTask{b}), err)
			return 1
		}
		s.say("%s", reply)
	}
	if !s.stopAgents(ctx) {
		return 1
	}
	svc := serviceOf(s.Cfg, s.Env)
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	line, err := svc.Uninstall(sctx)
	cancel()
	if err != nil {
		fmt.Fprintf(s.Out, "FAIL obor serve uninstall: %v\n", err)
		return 1
	}
	s.say("serve: %s", line)
	switch changed, err := project.Unregister(root, s.Env); {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL the project list: %v\n", err)
		return 1
	case changed:
		s.say("projects: %s removed from %s", s.name(), project.ProjectsFile(s.Env))
	default:
		s.say("projects: %s not in %s (already)", s.name(), project.ProjectsFile(s.Env))
	}
	if project.Stopped(root) {
		s.say("marker: %s (already)", project.StoppedFile(root))
	} else if err := project.MarkStopped(root, s.Why, s.now()); err != nil {
		fmt.Fprintf(s.Out, "FAIL the marker: %v\n", err)
		return 1
	} else {
		s.say("marker: %s written", project.StoppedFile(root))
	}
	s.say("STOPPED %s (files stay; obor start wakes it)", s.name())
	return 0
}

// start is obor start's steps: the marker removed, serve's launchd agent installed, the project registered. Agents
// start on first use, as usual.
func (s *sleeper) start(ctx context.Context) int {
	root := s.Cfg.Root
	switch removed, err := project.ClearStopped(root); {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL the marker: %v\n", err)
		return 1
	case removed:
		s.say("marker: %s removed", project.StoppedFile(root))
	default:
		s.say("marker: none (already)")
	}
	svc := serviceOf(s.Cfg, s.Env)
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch {
	case !svc.Supported():
		s.say("serve: %s", service.Elsewhere)
	case svc.Current(sctx):
		s.say("serve: %s installed and loaded (already)", svc.Label())
	default:
		line, err := svc.Install(sctx)
		if err != nil {
			fmt.Fprintf(s.Out, "FAIL obor serve install: %v\n", err)
			return 1
		}
		s.say("serve: %s", line)
	}
	switch changed, err := project.Register(root, s.Env); {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL the project list: %v\n", err)
		return 1
	case changed:
		s.say("projects: %s added to %s", s.name(), project.ProjectsFile(s.Env))
	default:
		s.say("projects: %s in %s (already)", s.name(), project.ProjectsFile(s.Env))
	}
	s.say("STARTED %s (agents start on first use)", s.name())
	return 0
}

// busy is the project's tasks in progress and whether serve answered: serve's open tasks (every state but merged,
// rejected and dropped: working, gating, merging, waiting …); serve down, the lanes git and the store call working or
// done (a waiting dispatch is serve's alone then).
func (s *sleeper) busy(ctx context.Context) (out []busyTask, serveUp bool) {
	actx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ss, err := s.ask(actx)
	cancel()
	if p := ss.Project; err == nil && p != nil && filepath.Clean(p.Root) == filepath.Clean(s.Cfg.Root) {
		for _, t := range p.Tasks {
			if t.State != "merged" && t.State != "rejected" && t.State != "dropped" {
				out = append(out, busyTask{Task: t.Task, Lane: t.Lane, State: t.State})
			}
		}
		return out, true
	}
	for _, l := range checks.NewLaneTable(s.Cfg).States() {
		if l.State == "working" || l.State == "done" {
			out = append(out, busyTask{Task: l.Task, Lane: l.Lane, State: l.State})
		}
	}
	return out, false
}

func busyList(bs []busyTask) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		where := b.Lane
		if where == "" {
			where = "no lane"
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", b.Task, where, b.State))
	}
	return strings.Join(parts, ", ")
}

// stopAgents stops every agent of the project that runs in a labelled pane (a lane's worker, a role agent, an agents
// entry), except the caller's own pane and agent: each pane stays labelled, at its shell (launch.Launcher.Stop, with
// --force a working one too). herdr not reachable: no agent runs, said. false: an agent did not stop (FAIL said).
func (s *sleeper) stopAgents(ctx context.Context) bool {
	panes, err := s.H.Panes(ctx)
	if err != nil {
		s.say("agents: herdr not reachable (%v): none to stop", err)
		return true
	}
	ownPane, ownAgent := s.Env.Get("HERDR_PANE_ID"), s.Env.Get("OBOR_AGENT")
	stopped := 0
	for _, p := range panes {
		if p.Label == "" || p.Kind == "" || p.ID == ownPane || p.Label == ownAgent {
			continue
		}
		info, ok := project.AgentLookup(s.Cfg, p.Label, "")
		if !ok || !project.LabelOurs(s.Cfg, p.Label, p.Cwd, "") &&
			(info.Cwd == "" || !project.LabelOurs(s.Cfg, p.Label, p.Cwd, info.Cwd)) {
			continue
		}
		line, err := s.stopOne(ctx, p.Label, p.ID)
		if err != nil {
			if ev := launch.Evidence(err); ev != "" {
				s.say("%s", ev)
			}
			fmt.Fprintf(s.Out, "FAIL agent %s (pane %s): %v\n", p.Label, p.ID, err)
			return false
		}
		s.say("agents: %s", line)
		stopped++
	}
	if stopped == 0 {
		s.say("agents: none running (already)")
	}
	return true
}

func (s *sleeper) stopOne(ctx context.Context, agent, pane string) (string, error) {
	if s.Stop != nil {
		return s.Stop(ctx, agent, pane, s.Force)
	}
	l := &launch.Launcher{Cfg: s.Cfg, Env: s.Env, H: s.H, Out: io.Discard}
	return l.Stop(ctx, agent, pane, s.Force)
}

func (s *sleeper) ask(ctx context.Context) (wire.Status, error) {
	if s.Ask != nil {
		return s.Ask(ctx)
	}
	return wire.Ask(ctx, wire.SocketPath(s.Cfg.Root))
}

func (s *sleeper) drop(ctx context.Context, c wire.Call) (string, error) {
	if s.Drop != nil {
		return s.Drop(ctx, c)
	}
	return wire.Verdict(ctx, wire.SocketPath(s.Cfg.Root), wire.MethodDrop, c)
}

func (s *sleeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
