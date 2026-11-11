package main

// obor stop / obor start: put a project to sleep and wake it. A stopped project runs no serve and no agents,
// is not in the machine's project list, and its hooks stand down (project.Stopped); its files stay.
//
// obor stop is one guarded transition: serve must answer, it is paused first so nothing new starts,
// and STOPPED is said only once serve is confirmed down. The pause is always stop's own, untimed: a pause in
// force before it is replaced and recorded in the marker; a refusal after the pause, and obor start, restore that
// pause while it has not expired, else clear. obor start fails until stop's pause is ended, and its rerun completes it.

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
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/service"
)

const (
	// stopPause is the why of the pause obor stop sets: obor start ends a pause in force with this why only.
	stopPause = wire.StopPause
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
	// Ask is serve's status, Call one request to serve (a pause; nil: the project's socket); Stop stops one agent
	// (nil: launch.Launcher.Stop).
	Ask  func(ctx context.Context) (wire.Status, error)
	Call func(ctx context.Context, r wire.Req) (string, error)
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
			fmt.Fprintln(out, "FAIL "+help.Usage("stop"))
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
	t, err := terminal(s.Cfg, s.Env)
	if err != nil {
		s.say("FAIL %v", err)
		return 1
	}
	s.H = t.Control
	return s.stop(context.Background())
}

// startCmd is `obor start` in a project: its steps, then STARTED <name> (or FAIL, exit 1).
func startCmd(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(out, "FAIL "+help.Usage("start"))
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
// answers for the project (else: start serve first); (2) serve paused by stop's own untimed pause, so nothing new is
// dispatched (a pause in force already is replaced, and recorded); (3) the tasks in progress, asked of serve: refused,
// or with --force kept open (the agents stop; the tasks continue at obor start) — never the lane whose agent runs in
// the caller's own pane (project.Pane); (4) herdr's panes listed (an error refuses) and every agent obor
// started in the project stopped but the caller's own;
// (5) serve ended (launchd's agent uninstalled; one in a pane or run by hand asked to exit over its socket) and
// confirmed down (its socket and lock gone); (6) the project unregistered and the marker written, with the replaced
// pause. A refusal after (2) restores the replaced pause while it has not expired, else clears stop's. A stopped
// project's stop says so and does nothing.
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
	m := project.StopMarker{Why: s.Why} // the pause stop replaces: a refusal and obor start restore it
	if ps.Paused != nil && ps.Paused.Why != stopPause {
		m.PauseWhy, m.PauseUntil = ps.Paused.Why, ps.Paused.Until
		s.say("pause: %q in force (until %s): replaced by obor stop's, restored by obor start while it holds",
			m.PauseWhy, untilLine(m.PauseUntil))
	}
	p := &wire.PauseReq{Envelope: c, Why: stopPause}
	p.CallID = wire.NewCallID()
	reply, err := s.call(ctx, p)
	if err != nil {
		fmt.Fprintf(s.Out, "FAIL pause: %v\n", err)
		return 1
	}
	s.say("pause: %s", reply)
	refuse := func(format string, a ...any) int {
		fmt.Fprintf(s.Out, "FAIL "+format+"\n", a...)
		if err := s.endStopPause(ctx, c, m); err != nil {
			s.say("pause: obor stop's not ended (%v): obor pause --clear", err)
		}
		return 1
	}

	if ps, err = s.status(ctx); err != nil { // paused now: no dispatch starts after this answer
		return refuse("obor serve stopped answering (%v)", err)
	}
	busy := openTasks(ps)
	if len(busy) > 0 && !s.Force {
		return refuse("tasks in progress: %s: finish or drop them, or obor stop --force (stops the agents; the tasks "+
			"stay open and continue at obor start)", busyList(busy))
	}
	switch own, err := s.ownLane(ctx, busy); {
	case err != nil:
		return refuse("%v", err)
	case own != "":
		pane, _ := project.Pane(s.Env) // ownLane found the lane by it
		return refuse("lane %s's agent runs in this pane (%s): obor stop would stop its own caller: run it from the "+
			"owner's or the lead's pane", own, pane)
	}

	if err := s.stopAgents(ctx); err != nil {
		return refuse("%v", err)
	}
	if err := s.stopServe(ctx, c); err != nil {
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
	m.At = s.now()
	if err := project.WriteStopMarker(root, m); err != nil {
		fmt.Fprintf(s.Out, "FAIL the marker: %v\n", err)
		return 1
	}
	s.say("marker: %s written", project.StoppedFile(root))
	s.say("STOPPED %s (files stay; obor start wakes it)", s.name())
	return 0
}

// start is obor start's steps: the marker moved aside (serve may start now; a rerun finds it there), serve's launchd
// agent installed, the project registered, and obor stop's pause ended once serve answers — the pause it replaced
// restored while that has not expired, else cleared (a pause with another why stays) — then the marker removed.
// Serve not answering, or the pause not ended, fails with a retry line: obor start again completes it. Agents start
// on first use.
func (s *sleeper) start(ctx context.Context) int {
	root := s.Cfg.Root
	m, moved, pending, err := project.BeginStart(root)
	switch {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL the marker: %v\n", err)
		return 1
	case moved:
		s.say("marker: %s moved to %s", filepath.Base(project.StoppedFile(root)), project.StartingFile(root))
	case pending:
		s.say("marker: %s (an earlier obor start's)", project.StartingFile(root))
	default:
		s.say("marker: none (already)")
	}
	svc := serviceOf(s.Cfg, s.Env)
	sctx, cancel := context.WithTimeout(ctx, service.Deadline)
	defer cancel()
	switch {
	case !svc.Supported():
		s.say("serve: %s", service.Elsewhere)
	case svc.Current(sctx):
		s.say("serve: %s installed and loaded (already)", svc.Label())
	default:
		line, err := svc.Install(sctx)
		if err != nil {
			fmt.Fprintf(s.Out, "FAIL obor serve install: %v (obor start again)\n", err)
			return 1
		}
		s.say("serve: %s", line)
	}
	switch changed, err := project.Register(root, s.Env); {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL the project list: %v (obor start again)\n", err)
		return 1
	case changed:
		s.say("projects: %s added to %s", s.name(), project.ProjectsFile(s.Env))
	default:
		s.say("projects: %s in %s (already)", s.name(), project.ProjectsFile(s.Env))
	}
	if !s.startPause(ctx, m) {
		return 1
	}
	if err := project.EndStart(root); err != nil {
		fmt.Fprintf(s.Out, "FAIL the marker: %v (obor start again)\n", err)
		return 1
	}
	s.say("STARTED %s (agents start on first use)", s.name())
	return 0
}

// startPause ends obor stop's pause once serve answers (within Wait): m's pause restored while it holds, else
// cleared; a pause in force with another why is not stop's and stays. false: serve did not answer, or refused (FAIL
// said, with the retry).
func (s *sleeper) startPause(ctx context.Context, m project.StopMarker) bool {
	var ps *wire.ProjectStatus
	var err error
	for deadline := time.Now().Add(s.wait()); ; time.Sleep(200 * time.Millisecond) {
		if ps, err = s.status(ctx); err == nil || time.Now().After(deadline) {
			break
		}
	}
	switch {
	case err != nil:
		fmt.Fprintf(s.Out, "FAIL obor serve does not answer for %s (%v): obor stop's pause holds; once serve runs "+
			"(obor serve, or obor serve install), obor start again\n", s.Cfg.Root, err)
		return false
	case ps.Paused == nil || ps.Paused.Why != stopPause:
		s.say("pause: none of obor stop's (already)")
		return true
	}
	if err := s.endStopPause(ctx, caller(s.Env, s.Cwd, s.Cfg), m); err != nil {
		fmt.Fprintf(s.Out, "FAIL pause: obor stop's not ended (%v): obor start again\n", err)
		return false
	}
	return true
}

// endStopPause ends obor stop's pause in force: the pause m records restored (its why and end) while it has not
// expired, else the pause cleared.
func (s *sleeper) endStopPause(ctx context.Context, id wire.Envelope, m project.StopMarker) error {
	c := &wire.PauseReq{Envelope: id}
	c.CallID = wire.NewCallID()
	if m.PauseWhy != "" && (m.PauseUntil == 0 || m.PauseUntil > s.now().Unix()) {
		c.Why, c.Until = m.PauseWhy, m.PauseUntil
		reply, err := s.call(ctx, c)
		if err != nil {
			return err
		}
		s.say("pause: %q restored: %s", m.PauseWhy, reply)
		return nil
	}
	c.Clear = true
	reply, err := s.call(ctx, c)
	if err != nil {
		return err
	}
	if m.PauseWhy != "" {
		s.say("pause: %q expired (until %s): not restored", m.PauseWhy, untilLine(m.PauseUntil))
	}
	s.say("pause: %s", reply)
	return nil
}

// untilLine is a pause's end as local time, or "cleared".
func untilLine(until int64) string {
	if until == 0 {
		return "cleared"
	}
	return time.Unix(until, 0).Format("2006-01-02 15:04")
}

// ownLane is the lane among busy whose agent runs in the caller's own pane (project.Pane): its label is that
// lane's agent or its cwd in that lane. "": none, or no pane of the caller's. An error: herdr's panes not listed, so
// the caller's pane is not known.
func (s *sleeper) ownLane(ctx context.Context, busy []busyTask) (string, error) {
	own, _ := project.Pane(s.Env) // none: the caller runs in no pane, so no lane's agent is its own
	lanes := map[string]bool{}
	for _, b := range busy {
		if b.Lane != "" {
			lanes[b.Lane] = true
		}
	}
	if own == "" || len(lanes) == 0 {
		return "", nil
	}
	panes, err := s.H.Panes(ctx)
	if err != nil {
		return "", fmt.Errorf("the terminal's panes not listed (%v): the caller's pane %s is not known; obor stop again "+
			"once the terminal answers", err, own)
	}
	for _, p := range panes {
		if p.ID != own {
			continue
		}
		if info, ok := s.Cfg.AgentLookup(p.Label, ""); p.Label != "" && ok && lanes[info.Lane] {
			return info.Lane, nil
		}
		for _, l := range s.Cfg.Lanes() {
			if lanes[l.Name] && p.Cwd != "" && project.LabelOurs(s.Cfg, p.Label, p.Cwd, l.Dir) {
				return l.Name, nil
			}
		}
	}
	return "", nil
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

// openTasks is serve's open tasks: every task it still has in its merge loop (working, gating, merging, waiting …),
// which is every task that does not say it ended: an answer that names no ended field keeps its tasks open.
func openTasks(p *wire.ProjectStatus) (out []busyTask) {
	for _, t := range p.Tasks {
		if !t.Ended {
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
		return fmt.Errorf("the terminal's panes not listed (%v): its agents may still run; obor stop again once "+
			"the terminal answers", err)
	}
	repo, _ := project.Find(s.Cwd)
	ownPane, _ := project.Pane(s.Env)            // none: no pane of the caller's to spare
	ownAgent := project.Self(repo, s.Cfg, s.Env) // derived
	stopped := 0
	for _, p := range panes {
		if p.Label == "" || p.Agent == "" || p.ID == ownPane || ownAgent != "" && p.Label == ownAgent {
			continue
		}
		info, ok := s.Cfg.AgentLookup(p.Label, "")
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

// stopServe ends the project's running serve and confirms it down: launchd's agent, when loaded, is uninstalled
// first (it ends launchd's serve, and no launchd serve takes over the lock); a serve that is not launchd's (in a pane,
// or run by hand, on any OS) is then asked to exit over its socket (wire.MethodExit: its graceful exit).
// Down is serve.flock released and serve.sock not answering, within Wait.
func (s *sleeper) stopServe(ctx context.Context, id wire.Envelope) error {
	root := s.Cfg.Root
	svc := serviceOf(s.Cfg, s.Env)
	sctx, cancel := context.WithTimeout(ctx, service.Deadline)
	defer cancel()
	pid, launchd, how := servePID(root), false, "its exit"
	if svc.Supported() {
		if lpid, loaded := svc.PID(sctx); loaded {
			launchd = pid != 0 && lpid == pid
			line, err := svc.Uninstall(sctx)
			if err != nil {
				return fmt.Errorf("obor serve uninstall: %v", err)
			}
			s.say("serve: %s", line)
			how = "its uninstall"
		}
	}
	if !launchd {
		c := &wire.ExitReq{Envelope: id, Why: "obor stop: " + s.Why}
		c.CallID = wire.NewCallID()
		reply, err := s.call(ctx, c)
		if err != nil {
			return fmt.Errorf("obor serve (pid %d) not ended: its exit: %v", pid, err)
		}
		s.say("serve: %s (pid %d)", reply, pid)
		how = "its exit"
	}
	lock, sock := project.NewLock(app.FlockFile(root)), wire.SocketPath(root)
	for deadline := time.Now().Add(s.wait()); lock.Held() || answers(sock); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("obor serve (pid %d) still holds %s or %s %s after %s", pid, lock.Path, sock,
				s.wait(), how)
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

func (s *sleeper) call(ctx context.Context, r wire.Req) (string, error) {
	if s.Call != nil {
		return s.Call(ctx, r)
	}
	return wire.Verdict(ctx, wire.SocketPath(s.Cfg.Root), r)
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
