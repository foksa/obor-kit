package main

// obor stop / obor start (T-709): put a project to sleep and wake it. A stopped project runs no serve and no agents,
// is not in the machine's project list, and its hooks stand down (project.Stopped); its files stay.
//
// obor stop is one guarded transition (T-710, RV-84): serve must answer, it is paused first so nothing new starts,
// and STOPPED is said only once serve is confirmed down. A refusal after the pause clears the pause it set.

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/service"
)

const (
	stopUsage  = `usage: obor stop ["<why>"] [--force]`
	startUsage = "usage: obor start"
	// stopPause is the why of the pause obor stop sets: obor start clears a pause in force with this why only.
	stopPause = "stopping (obor stop)"
)

// busyTask is a task in progress: a working, gating or merging lane, or a waiting dispatch (no lane).
type busyTask struct{ Task, Lane, State string }

// sleeper is one obor stop or obor start in the project of Cfg, run from Cwd; the funcs are its outside systems
// (tests fake them).
type sleeper struct {
	Cfg *config.Config
	Env project.Env
	Cwd string // the process's cwd: a lane's is a worker's (project.DetectRole)
	Out io.Writer
	H   port.PaneControl // herdr: the project's agent panes
	// Ask is serve's status, Call one request to serve (a drop, a pause; nil: the project's socket); Stop stops one
	// agent (nil: launch.Launcher.Stop).
	Ask  func(ctx context.Context) (wire.Status, error)
	Call func(ctx context.Context, method string, c wire.Call) (string, error)
	Stop func(ctx context.Context, agent, pane string, force bool) (string, error)
	Now  func() time.Time
	// Wait bounds the wait for serve to go down (stop) or to answer (start); 0: 30 s.
	Wait  time.Duration
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
// than this binary) and the process's cwd.
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
	s.Cwd, _ = os.Getwd()
	return true
}

func (s *sleeper) name() string { return s.Cfg.ProjectName() }

// stop is obor stop's one transition, in order: (0) not from a worker (OBOR_ROLE worker or a lane's cwd); (1) serve
// answers for the project (else: start serve first); (2) serve paused, so nothing new is dispatched (a pause in force
// already is kept); (3) the tasks in progress, asked of serve: refused, or with --force each dropped through serve
// (any drop failing refuses); (4) herdr's panes listed (an error refuses) and every agent obor started in the project
// stopped but the caller's own; (5) serve ended (launchd's: uninstalled; one in a pane or run by hand: refused, it has
// no exit call) and confirmed down (its socket and lock gone); (6) the project unregistered and the marker written.
// A refusal after (2) clears the pause (2) set. A stopped project's stop says so and does nothing.
func (s *sleeper) stop(ctx context.Context) int {
	root := s.Cfg.Root
	repo, _ := project.Find(s.Cwd)
	if r := project.DetectRole(repo, s.Cfg, s.Env); r.Role == "worker" {
		where := "OBOR_ROLE=worker"
		if r.Lane != "" {
			where = "lane " + r.Lane
		}
		fmt.Fprintf(s.Out, "FAIL obor stop is the owner's or the lead's, not a worker's (%s): run it in %s\n", where, root)
		return 1
	}
	if project.Stopped(root) {
		s.say("STOPPED %s (already: %s; obor start wakes it)", s.name(), project.StoppedFile(root))
		return 0
	}
	ps, err := s.status(ctx)
	if err != nil {
		fmt.Fprintf(s.Out, "FAIL obor serve does not answer for %s (%v): start serve first (obor serve, or obor serve "+
			"install), then obor stop\n", root, err)
		return 1
	}
	c := caller(s.Env, s.Cwd, s.Cfg)
	paused := false // the pause in force is this stop's: a refusal clears it
	if ps.Paused != nil {
		s.say("pause: %q in force (kept)", ps.Paused.Why)
	} else {
		p := c
		p.CallID, p.Note = wire.NewCallID(), stopPause
		reply, err := s.call(ctx, wire.MethodPause, p)
		if err != nil {
			fmt.Fprintf(s.Out, "FAIL pause: %v\n", err)
			return 1
		}
		paused = true
		s.say("pause: %s", reply)
	}
	refuse := func(format string, a ...any) int {
		fmt.Fprintf(s.Out, "FAIL "+format+"\n", a...)
		if paused {
			p := c
			p.CallID, p.Clear = wire.NewCallID(), true
			if reply, err := s.call(ctx, wire.MethodPause, p); err != nil {
				s.say("pause: not cleared (%v): obor pause --clear", err)
			} else {
				s.say("pause: %s", reply)
			}
		}
		return 1
	}

	if ps, err = s.status(ctx); err != nil { // paused now: no dispatch starts after this answer
		return refuse("obor serve stopped answering (%v)", err)
	}
	busy := openTasks(ps)
	if len(busy) > 0 && !s.Force {
		return refuse("tasks in progress: %s: finish or drop them, or obor stop --force (drops each)", busyList(busy))
	}
	for _, b := range busy {
		d := c // as obor drop calls it
		d.CallID, d.Note, d.Force = wire.NewCallID(), "obor stop --force: "+s.Why, true
		if b.Lane != "" {
			d.Lane = b.Lane
		} else {
			d.Task = b.Task
		}
		reply, err := s.call(ctx, wire.MethodDrop, d)
		if err != nil {
			return refuse("drop %s: %v (nothing stopped)", busyList([]busyTask{b}), err)
		}
		s.say("%s", reply)
	}

	if err := s.stopAgents(ctx); err != nil {
		return refuse("%v", err)
	}
	if err := s.stopServe(ctx); err != nil {
		return refuse("%v", err)
	}

	switch changed, err := project.Unregister(root, s.Env); {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL the project list: %v\n", err)
		return 1
	case changed:
		s.say("projects: %s removed from %s", s.name(), project.ProjectsFile(s.Env))
	default:
		s.say("projects: %s not in %s (already)", s.name(), project.ProjectsFile(s.Env))
	}
	if err := project.MarkStopped(root, s.Why, s.now()); err != nil {
		fmt.Fprintf(s.Out, "FAIL the marker: %v\n", err)
		return 1
	}
	s.say("marker: %s written", project.StoppedFile(root))
	s.say("STOPPED %s (files stay; obor start wakes it)", s.name())
	return 0
}

// start is obor start's steps: the marker removed, serve's launchd agent installed, the project registered, and
// obor stop's pause cleared once serve answers (a pause with another why stays). Agents start on first use.
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
	if !s.clearStopPause(ctx) {
		return 1
	}
	s.say("STARTED %s (agents start on first use)", s.name())
	return 0
}

// clearStopPause clears the pause obor stop set, once serve answers (within Wait): a pause with another why is not
// stop's and stays. Serve not answering by then is a line naming the command, not a failure (off darwin serve is the
// user's to run). false: serve refused the clear (FAIL said).
func (s *sleeper) clearStopPause(ctx context.Context) bool {
	var ps *wire.ProjectStatus
	var err error
	for deadline := time.Now().Add(s.wait()); ; time.Sleep(200 * time.Millisecond) {
		if ps, err = s.status(ctx); err == nil || time.Now().After(deadline) {
			break
		}
	}
	switch {
	case err != nil:
		s.say("pause: serve does not answer yet (%v): once it runs, obor pause --clear ends obor stop's pause", err)
		return true
	case ps.Paused == nil || ps.Paused.Why != stopPause:
		s.say("pause: none of obor stop's (already)")
		return true
	}
	c := caller(s.Env, s.Cwd, s.Cfg)
	c.Clear = true
	reply, err := s.call(ctx, wire.MethodPause, c)
	if err != nil {
		fmt.Fprintf(s.Out, "FAIL pause clear: %v\n", err)
		return false
	}
	s.say("pause: %s", reply)
	return true
}

// status is serve's status of this project (2 s): an error when serve does not answer, or answers for another root
// or before its project is loaded.
func (s *sleeper) status(ctx context.Context) (*wire.ProjectStatus, error) {
	actx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ss, err := s.ask(actx)
	cancel()
	switch p := ss.Project; {
	case err != nil:
		return nil, err
	case p == nil:
		return nil, fmt.Errorf("serve has not loaded the project yet")
	case filepath.Clean(p.Root) != filepath.Clean(s.Cfg.Root):
		return nil, fmt.Errorf("the socket's serve serves %s", p.Root)
	default:
		return p, nil
	}
}

// openTasks is serve's open tasks: every state but merged, rejected and dropped (working, gating, merging, waiting …).
func openTasks(p *wire.ProjectStatus) (out []busyTask) {
	for _, t := range p.Tasks {
		if t.State != "merged" && t.State != "rejected" && t.State != "dropped" {
			out = append(out, busyTask{Task: t.Task, Lane: t.Lane, State: t.State})
		}
	}
	return out
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
// --force a working one too). An error: herdr's panes not listed (no proof that no agent runs), or an agent that did
// not stop.
func (s *sleeper) stopAgents(ctx context.Context) error {
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return fmt.Errorf("herdr's panes not listed (%v): its agents may still run; obor stop again once herdr answers", err)
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
			return fmt.Errorf("agent %s (pane %s): %v", p.Label, p.ID, err)
		}
		s.say("agents: %s", line)
		stopped++
	}
	if stopped == 0 {
		s.say("agents: none running (already)")
	}
	return nil
}

// stopServe ends the project's running serve and confirms it down: launchd's (its agent's pid is the one serve.flock
// names) is uninstalled; any other (in a pane, or run by hand, on any OS) is refused, since serve has no exit call
// to ask it over its socket. Down is serve.flock no longer held and serve.sock not answering, within Wait.
func (s *sleeper) stopServe(ctx context.Context) error {
	root := s.Cfg.Root
	svc := serviceOf(s.Cfg, s.Env)
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pid, launchd := servePID(root), false
	if svc.Supported() && pid != 0 {
		lpid, loaded := svc.PID(sctx)
		launchd = loaded && lpid == pid
	}
	if !launchd {
		return fmt.Errorf("obor serve (pid %d) is not launchd's: it runs in a pane or by hand and has no exit call: "+
			"stop it there yourself (Ctrl-C)", pid)
	}
	line, err := svc.Uninstall(sctx)
	if err != nil {
		return fmt.Errorf("obor serve uninstall: %v", err)
	}
	s.say("serve: %s", line)
	lock, sock := project.NewLock(app.FlockFile(root)), wire.SocketPath(root)
	for deadline := time.Now().Add(s.wait()); lock.Held() || answers(sock); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("obor serve (pid %d) still holds %s or %s %s after its uninstall", pid, lock.Path, sock,
				s.wait())
		}
	}
	s.say("serve: down (pid %d: %s and %s released)", pid, filepath.Base(lock.Path), filepath.Base(sock))
	return nil
}

// answers: something accepts connections on the unix socket at path.
func answers(path string) bool {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
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

func (s *sleeper) call(ctx context.Context, method string, c wire.Call) (string, error) {
	if s.Call != nil {
		return s.Call(ctx, method, c)
	}
	return wire.Verdict(ctx, wire.SocketPath(s.Cfg.Root), method, c)
}

func (s *sleeper) wait() time.Duration {
	if s.Wait > 0 {
		return s.Wait
	}
	return 30 * time.Second
}

func (s *sleeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
