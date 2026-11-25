// Package launch starts, stops and restarts an obor agent in its labelled herdr pane (`obor restart`, serve's
// start of a worker and obor setup's --start): the one launch path.
//
//	Who      a lane agent, a role agent (lead, integrator, reviewer, triage, deep_dive, advisor)
//	         (config.AgentLookup: the roles table, no second copy of the name rules), an agent whose pane has a
//	         lane worktree as cwd (that lane's agent), or any agent with a session recorded in serve's store. Env
//	         OBOR_ROLE (the role's env value, triage's too; no role: unset), OBOR_LANE (no lane:
//	         unset), the kind's vars and the cwd as obor setup gives them, every other var of the registry unset (agents.Kinds.StartVars,
//	         a stale OBOR_AGENT or another kind's OBOR_OPENCODE_MODEL);
//	         an unknown agent keeps its OBOR_ROLE, in the pane's cwd. Kind and args from the dispatch's model (the
//	         launch's, else a lane agent's lane row model), else the role / lead's model (config.Model: base args, model id, kinds args, then --settings for
//	         claude); no model: the kind herdr reports for the pane, else claude, no args.
//	Where    Locate (locate.go), under the agent's restart lock, before anything is stopped, labelled, typed or
//	         recorded: the pane serve's identity table proves the agent runs in (a restart in place, its label put
//	         back), else its home (the pane labelled <agent> at its shell, in this project), else --pane, else a pane
//	         the caller places (port.Launch.Place, once the start is built: it takes the start's env). Refused: a
//	         target other than the pane the agent runs in, an ambiguous agent, a pane serve gives to another agent or
//	         labelled for another agent the config names, a second pane labelled <agent>, and with serve down a start
//	         beside a live agent of the project no label accounts for. Never a known agent's pane found by its label
//	         or identity whose cwd is outside this project; a NoStop start into the --pane the caller named takes it at its
//	         shell whatever its cwd (it runs no agent and carries no other agent's label).
//	Session  the recorded sid, else the one herdr reports for the pane (never herdr's for a kind whose sessions obor
//	         names itself: Claude); --fresh: a new one. None: a role agent starts fresh, said, only at a pane that runs
//	         no agent; a role agent's pane that runs one, and a lane's agent, FAIL (pass --fresh), checked before
//	         anything is stopped. A fresh start of a kind whose sessions obor names records the id obor chose before
//	         the agent runs. A recorded Claude sid resumes from the transcript the session itself reported (the
//	         SessionStart hook's path); none anywhere (never prompted): starts fresh, said. A session of another kind
//	         than the dispatch's type starts fresh, said.
//	Stop     see Stop; port.Launch.NoStop refuses instead (the agent already running, another agent in its lane
//	         worktree, another program in its pane), nothing stopped. A child the pane's shell stopped (Ctrl+Z: herdr
//	         then sees an idle shell) → FAIL with a hint, before anything is stopped or typed: no fg resume.
//	Shell    a restart whose pane ran an agent waits after the stop for the pane to be a shell again (ShellWait:
//	         the terminal refuses a start, PaneBusy, until the old agent's exit is reaped), and a start the terminal
//	         refuses as PaneBusy is retried like that within the same bound; past it, no start and a FAIL naming the
//	         pane. RESTARTED only when the start succeeded and its agent is seen.
//	Start    the env line typed at the shell (never into another program), then Start (the one start, shared with
//	         obor setup's --start): the pane labelled <agent>, the terminal's agent start (port.AgentStarter), a look for a
//	         startup dialog. The start ends when the terminal says its agent runs in the pane.
//	Self     the caller's own pane (project.PaneOf): scheduled, see self.go.
//
// Every operation registers one op record (kind restart) for its whole run.
package launch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/store"
)

// Launcher runs launches in one project (Cfg nil: none, every agent is unknown). Zero durations mean the defaults.
type Launcher struct {
	Cfg  *config.Config
	Env  project.Env
	Kit  string // the obor kit (Codex's MCP registration points at <Kit>/bin/obor); "": project.KitRoot(Env)
	H    port.PaneControl
	Out  io.Writer     // progress lines
	Wait time.Duration // for an agent to exit after /exit, the shell to run the env line (default OBOR_RESTART_WAIT, else 20 s)
	Up   time.Duration // for a started agent to come up in its pane (the terminal's start's bound; default 30 s)
	Idle time.Duration // Stop with force: for a working agent to go idle after Escape (default port.StopIdle)
	Poll time.Duration // how often it looks (default 1 s)
	// Shell is how long a restart waits after its stop for the pane to be a shell again, and retries a start the
	// terminal refused as PaneBusy: it keeps the pane no available shell until the old agent's exit is reaped (default
	// ShellWait).
	Shell time.Duration
	// Bound is the most a stop may take: its idle wait, its quit wait and the terminate together (default StopBound,
	// port.StopBound). The quit wait is capped to it, whatever OBOR_RESTART_WAIT says.
	Bound time.Duration
	// TermGrace and KillGrace are how long a terminate waits for the process group to be done after SIGTERM and after
	// SIGKILL (defaults 3 s and 2 s). A test sets them small: it asserts the ladder's signals, not its timing.
	TermGrace time.Duration
	KillGrace time.Duration
	// Alive says whether process pid runs (an agent's exit is its leader gone); nil: project.PIDAlive.
	Alive func(pid int) bool
	// Kill sends sig to process group pgid (an exited agent's leftover MCP servers, or with force an agent that ignored
	// /exit); nil: kill(2) on -pgid.
	Kill func(pgid int, sig syscall.Signal) error
	// PS runs ps with args (a suspended agent: the shell's stopped child); nil: the ps on PATH.
	PS func(args ...string) ([]byte, error)
	// Sched is how long a scheduled restart of the caller's own pane waits for the agent to go idle (default 10 min).
	Sched time.Duration
	// Spawn starts `obor <args>` detached (own session) in dir, its output appended to the file out, with the env
	// marking it the scheduled run; nil: this binary.
	Spawn func(dir string, args []string, out string) error
	// Sessions is agent's current recorded session; nil: serve's store at Cfg.Root (store.CurrentAt).
	Sessions func(agent string) (store.Session, bool)
	// Report records a session a start found (a Codex rollout) or the session a start resumed; nil: the session
	// request to Cfg.Root's serve.
	Report func(ctx context.Context, s store.Session) error
	// Started reports the confirmed start of an agent (a start's one success path, done): serve ends the agent's skew
	// entry for it, whatever session the start reports. nil: the started request to Cfg.Root's serve.
	Started func(ctx context.Context, agent string) error
	// Rollout is how long a fresh start looks for its new session's transcript (default 10 s).
	Rollout time.Duration
	// Background runs the search for a fresh start's new session transcript after the start returned (obor serve: a
	// goroutine, as its starts run one at a time and must not wait for it); nil: before the start returns (a CLI
	// restart, which exits after).
	Background func(search func())
	// Log is where a background search says what it found (none, or several open); nil: nowhere.
	Log io.Writer
	// Diag is where a start's terminal-adapter lines go (port.AgentStart.Say: a name the terminal kept, cleared and
	// retried, a failed clear's warning, a start it counts though its own answer failed): the diagnostics a serve
	// start's discarded Out would swallow. nil: Out.
	Diag io.Writer
	// Open is the files processes pids hold open (a fresh start's session: the transcript its agent has open);
	// nil: /proc/<pid>/fd on linux, lsof elsewhere.
	Open func(pids []int) []string
	// Workspace prepares the workspace a start runs in (roleWorktree): the role worktree a role agent's cwd is, added
	// when missing and given the files it lacks. nil: project.AddWorktree and project.LinkInto. A test fakes it to
	// assert that the workspace is prepared before the kind's start reads it, without git.
	Workspace func(ctx context.Context, dir string) error
	// Job is the open job (<task>/<attempt>/<step>) obor serve holds for role's agent on lane ("": none): a lane
	// worker's work or fix job of the task its lane holds, the integrator's review or resolve job of a task;
	// obor restart sends its wake, never an idle agent with no job. An error refuses the restart before the old
	// agent is stopped: the agent's lane holds a task and serve cannot answer, or the integrator's serve is not
	// running and may hold one of its jobs (that error is already restart's whole FAIL line). nil: none asked
	// (obor serve's own starts carry their job's wake).
	Job func(ctx context.Context, role, lane string) (string, error)
	// Install is the machine a kind's start check reads (agents.StartChecker); nil: project.InstallHost of
	// this launcher's Env.
	Install func() agents.InstallHost
	// Where asks serve's identity table which pane runs an agent, and which agent each pane of the request runs
	// (Locate); nil: the where request to Cfg.Root's serve. An error is serve that cannot tell (wire.Absent: not
	// running).
	Where func(ctx context.Context, req wire.WhereReq) (wire.WhereReply, error)
}

// Request is one `obor restart` (the core's port.Launch: Launcher is a port.Launcher).
type Request = port.Launch

var _ port.Launcher = (*Launcher)(nil)

// Args writes r into argv (after the "restart" subcommand): schedule puts it into the detached run's argv and
// ParseRequest reads that back, so a flag cannot be carried on one side only. The Prompt, Effort, Type, Place, NoStop
// and Again are not restart's (a brief's, a dispatch's or obor setup's start): a scheduled run is the CLI's restart, which
// carries none of them.
// Handoff is carried: the detached run asks for the lead's handoff form only if its argv names --handoff.
func Args(r Request) []string {
	args := []string{r.Agent}
	if r.Pane != "" {
		args = append(args, "--pane", r.Pane)
	}
	if r.Fresh {
		args = append(args, "--fresh")
	}
	if r.Handoff {
		args = append(args, "--handoff")
	}
	if r.Force {
		args = append(args, "--force")
	}
	return args
}

// ParseRequest reads an `obor restart` argument list (after the subcommand) back into the Request Args wrote; the
// other half of that pair. Flags restart owns but a Request does not carry (--stop) are not its business and
// are an error, as is every flag restart gave up (--prompt, --type, --effort, --sid). --handoff is restart's flag for
// the lead's handoff form (its Handoff), and a scheduled run reads it back from the argv Args wrote.
func ParseRequest(args []string) (Request, error) {
	var r Request
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", false
			}
			i++
			return args[i], true
		}
		switch a {
		case "--pane":
			v, ok := value()
			if !ok {
				return Request{}, fmt.Errorf("--pane needs a value")
			}
			r.Pane = v
		case "--fresh":
			r.Fresh = true
		case "--handoff":
			r.Handoff = true
		case "--force":
			r.Force = true
		default:
			if strings.HasPrefix(a, "-") || r.Agent != "" {
				return Request{}, fmt.Errorf("unexpected argument %q", a)
			}
			r.Agent = a
		}
	}
	return r, nil
}

func (l *Launcher) say(format string, a ...any) { fmt.Fprintf(l.Out, format+"\n", a...) }

func (l *Launcher) wait() time.Duration {
	if l.Wait > 0 {
		return l.Wait
	}
	var s int
	if _, err := fmt.Sscan(l.Env.Get("OBOR_RESTART_WAIT"), &s); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return port.StopQuit
}

// idle is how long a stop waits for a working agent to go idle after Escape (default port.StopIdle).
func (l *Launcher) idle() time.Duration {
	if l.Idle > 0 {
		return l.Idle
	}
	return port.StopIdle
}

// bound is the most a stop may take (default StopBound).
func (l *Launcher) bound() time.Duration {
	if l.Bound > 0 {
		return l.Bound
	}
	return StopBound
}

// termGrace and killGrace are the ladder's waits after SIGTERM and SIGKILL (defaults 3 s and 2 s).
func (l *Launcher) termGrace() time.Duration {
	if l.TermGrace > 0 {
		return l.TermGrace
	}
	return 3 * time.Second
}

func (l *Launcher) killGrace() time.Duration {
	if l.KillGrace > 0 {
		return l.KillGrace
	}
	return 2 * time.Second
}

func (l *Launcher) upWait() time.Duration {
	if l.Up > 0 {
		return l.Up
	}
	return 30 * time.Second
}

// shellWait is how long a restart waits for the pane's shell (default ShellWait).
func (l *Launcher) shellWait() time.Duration {
	if l.Shell > 0 {
		return l.Shell
	}
	return ShellWait
}

func (l *Launcher) poll() time.Duration {
	if l.Poll > 0 {
		return l.Poll
	}
	return time.Second
}

// until polls cond every Poll for at most d; true once cond holds.
func (l *Launcher) until(ctx context.Context, d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(min(l.poll(), time.Until(deadline)))
	}
}

// target is the agent being launched: who it is and where it runs.
type target struct {
	agent string
	known bool // the config knows it (lane, role, lane cwd)
	info  config.AgentInfo
	sid   string // recorded session id
	// sidKind is the recorded session's kind ("": not recorded).
	sidKind string
	// transcript is the recorded session's transcript, as the session reported it (Claude's SessionStart hook);
	// "" when the record names none (a start obor named, before its hook ran).
	transcript string
	pane       string
	entry      port.Pane // the pane as pane list shows it
	panes      []port.Pane
	kind       string
	env        map[string]string // the start's own env (agents.Kind.StartEnv; "" unsets the variable), set by the env line
	session    string            // the session id obor chose for the start (build; "": none)
	placed     bool              // the pane was just placed for the start: no old agent ever ran in it
}

// resolve is who the agent is and its pane's facts (shared by Restart and Stop), pane the one Locate chose ("": none,
// no pane is the agent's) for the caller's asked (--pane; "": none), with typ the launch's model type ("": none;
// port.Launch.Type, a dispatch's); nothing is typed. An agent no config entry, lane cwd or recorded session knows is
// started only in the pane the caller named.
func (l *Launcher) resolve(ctx context.Context, agent, asked, pane, typ string, noStop bool) (*target, error) {
	t, err := l.who(ctx, agent, asked, pane)
	if err != nil {
		return nil, err
	}
	if pane == "" {
		return nil, project.Failf("no pane labelled %s (pass --pane)", agent)
	}
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return nil, project.Failf("terminal pane list failed (is the terminal running?)")
	}
	t.panes = panes
	t.pane = pane
	for _, p := range panes {
		if p.ID == pane {
			t.entry = p
		}
	}
	// A NoStop start into the pane the caller named (--pane) takes it at its shell whatever its cwd: Locate already
	// refused a pane that runs an agent or carries another agent's label. A pane found by its label keeps inProject.
	if !(noStop && asked != "" && pane == asked) {
		if err := l.inProject(ctx, t); err != nil {
			return nil, err
		}
	}
	if err := l.model(t, typ); err != nil {
		return nil, err
	}
	return t, l.suspended(ctx, t)
}

// who is the agent's config entry and recorded session, pane its pane ("": none; an agent the config does not name is
// its pane's lane agent when that pane's cwd is a lane worktree). An agent neither the config nor a recorded session
// knows, with no pane asked (--pane), is a Fail.
func (l *Launcher) who(ctx context.Context, agent, asked, pane string) (*target, error) {
	t := &target{agent: agent}
	if l.Cfg != nil {
		t.info, t.known = l.Cfg.AgentLookup(agent, "")
		if !t.known {
			// An agent whose pane has a lane worktree as cwd: that lane's agent.
			if cwd := l.paneCwd(ctx, pane); cwd != "" {
				if info, ok := l.Cfg.AgentLookup(agent, cwd); ok {
					t.info, t.known = info, true
					l.say("lane %s (pane %s cwd)", info.Lane, pane)
				}
			}
		}
	}
	rec, _ := l.session(agent)
	t.sid, t.sidKind, t.transcript = rec.ID, rec.Kind, rec.Transcript
	if !t.known && t.sid == "" && asked == "" {
		return nil, project.Failf("unknown agent %s (no lane, role or recorded session)", agent)
	}
	return t, nil
}

// model is t's model and kind, their one resolution: the dispatch's type (typ, else a lane agent's lane row:
// dispatchType), else the configured one; the kind is that model's, else (no model) the kind the terminal reports
// for t's pane, else agents.Default. A model the config does not have is a Fail.
func (l *Launcher) model(t *target, typ string) error {
	l.dispatchType(t, typ)
	if t.info.Type != "" {
		lt, ok := l.Cfg.Model(t.info.Type)
		if !ok {
			return project.Failf("%v", l.Cfg.UnknownModel(t.info.Type))
		}
		t.kind = lt.Kind
	}
	if t.kind == "" {
		t.kind = t.entry.Agent
	}
	if t.kind == "" {
		t.kind = agents.Default
	}
	return nil
}

// suspended refuses t when its pane's shell holds a stopped child (Ctrl+Z: the terminal then sees an idle shell),
// before anything is stopped or typed.
func (l *Launcher) suspended(ctx context.Context, t *target) error {
	pi, err := l.H.ProcessInfo(ctx, t.pane)
	if err != nil || !pi.AtShell() {
		return nil
	}
	// herdr's process info lists only the foreground group: a stopped child of the shell (Ctrl+Z) is not in it.
	out, err := l.ps("-A", "-o", "ppid=,stat=,pid=,comm=")
	if err != nil {
		l.say("warning: cannot check pane %s for a suspended agent (ps: %v)", t.pane, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 4 && f[0] == strconv.Itoa(pi.ShellPID) && strings.HasPrefix(f[1], "T") {
			l.say("pane %s: %s (pid %s) is stopped", t.pane, strings.Join(f[3:], " "), f[2])
			return project.Failf("%s is suspended in pane %s (Ctrl+Z): fg it and /exit, or close the pane", t.agent, t.pane)
		}
	}
	return nil
}

// dispatchType puts the type a dispatch chose into t.info (a lane is a pool, the task's type picks the
// kind): typ, the launch's (port.Launch.Type and Effort, its effort the launch's own; no CLI flag sets it), else
// the ref project.LaneModel answers for a lane agent (a configured lane's row always names its dispatch's
// type, so there is no fallback to a lane type); neither: the configured type and effort stay.
func (l *Launcher) dispatchType(t *target, typ string) {
	effort := ""
	if typ == "" && t.info.Lane != "" && l.Cfg != nil {
		if ref, ok := project.LaneModel(l.Cfg, t.info.Lane); ok {
			typ, effort = config.ParseModelRef(ref)
			l.say("model %s (lane %s's dispatch)", ref, t.info.Lane)
		}
	}
	if typ != "" {
		t.info.Type, t.info.Effort = typ, effort
	}
}

func (l *Launcher) ps(args ...string) ([]byte, error) {
	if l.PS != nil {
		return l.PS(args...)
	}
	return exec.Command("ps", args...).Output()
}

// inProject: the pane of an agent this config knows (it is started with a cd into this project) is this project's
// (labels and agent names are machine-wide, so a run that loaded another project's config took axis's
// integrator pane for the kit's lane of the same name and started it in the kit's lane worktree). Its herdr cwd is in
// the main checkout, a lanes.dir worktree, the agent's configured cwd or another worktree of this repository. No cwd
// known, or an unknown agent (started where its pane is): not checked. Else FAIL with evidence, before anything is
// stopped or typed.
func (l *Launcher) inProject(ctx context.Context, t *target) error {
	cwd := t.entry.Cwd
	if !t.known || cwd == "" || project.LabelOurs(l.Cfg, t.agent, cwd, "") ||
		t.info.Cwd != "" && project.LabelOurs(l.Cfg, t.agent, cwd, t.info.Cwd) {
		return nil
	}
	if r, err := project.Find(cwd); err == nil && r.Root == project.Physical(l.Cfg.Root) {
		return nil
	}
	return l.giveUp(ctx, t,
		"pane %s is not this project's: its cwd %s is outside %s (another project's agent? run obor restart %s in its own project)",
		t.pane, cwd, l.Cfg.Root, t.agent)
}

func (l *Launcher) paneCwd(ctx context.Context, pane string) string {
	if pane == "" {
		return ""
	}
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return ""
	}
	for _, p := range panes {
		if p.ID == pane {
			return p.Cwd
		}
	}
	return ""
}

// begin takes the lane's restart lock (project.RestartLock; no lane: the agent's) for the whole stop and start; a
// restart of the lane already running is waited for. No project: nothing to lock.
func (l *Launcher) begin(ctx context.Context, agent, lane string) (func(), error) {
	if l.Cfg == nil {
		return func() {}, nil
	}
	lock := RestartLock(l.Cfg.Root, lane, agent)
	lock.Output = l.Out
	ok, err := lock.Try()
	if err == nil && !ok {
		l.say("waiting for the restart of %s (pid %s)", restartOf(lane, agent), lock.Holder())
		err = lock.Wait(ctx, -1)
	}
	if err != nil {
		return nil, project.Failf("cannot take the restart lock of %s: %v", restartOf(lane, agent), err)
	}
	return func() { _ = lock.Unlock() }, nil
}

// restartOf names whose restart lock it is: the lane, else the agent (on no lane).
func restartOf(lane, agent string) string {
	if lane == "" {
		return agent
	}
	return "lane " + lane
}

// Restart brings req.Agent (back) up in its pane; the last line it means is RESTARTED (the caller prints it). A
// fresh start held by a trust dialog is a restart: its line names the dialog and what to do (a resumed one failed).
func (l *Launcher) Restart(ctx context.Context, req Request) (string, error) {
	s, err := l.Launch(ctx, req)
	if td := port.TrustHeld(err); td != nil {
		return s.Line + ": " + td.Hint, nil
	}
	return s.Line, err
}

// Launch is Restart (port.Launcher): its line, and the agent it started there with its session id: herdr's for it, else
// the id obor named it (the identity serve records for a later rename; none for a scheduled restart of the caller's
// own pane). A fresh start held by a trust dialog returns both too, with a *port.TrustDialog (its pane: Started's):
// started, its first prompt taken once a person answers the dialog. A resumed one is a failed start (NotReady).
// req.Place places the pane of an agent that has none (called once, the start goes there). req.NoStop refuses rather
// than stop: its agent already running (a *port.Running), another agent in its lane (LaneFree), another program in
// its pane (otherProgram). req.NoAgent labels the pane and types its env and starts no agent (panesOnly).
func (l *Launcher) Launch(ctx context.Context, req Request) (port.Started, error) {
	if !config.AgentNameRE.MatchString(req.Agent) {
		return port.Started{}, project.Failf("%s", config.BadAgentName(l.Cfg, req.Agent))
	}
	if req.NoAgent {
		return l.panesOnly(ctx, req)
	}
	waited, err := l.waitScheduled(ctx, req.Agent, req.Pane)
	if err != nil {
		return port.Started{}, err
	}
	// The decision before anything is touched, under the agent's restart lock held to the start's last step.
	loc, end, err := l.Locate(ctx, req.Agent, req.Pane, true)
	if err != nil {
		return port.Started{}, err
	}
	defer end()
	if req.NoStop && loc.Runs {
		return port.Started{}, &port.Running{Agent: req.Agent, Pane: loc.Pane}
	}
	// No pane is the agent's and the caller places one: who it is and its model now, its pane once the start is built
	// (the placement takes the start's env).
	placing := loc.Pane == "" && req.Place != nil
	var t *target
	if placing {
		if t, err = l.who(ctx, req.Agent, req.Pane, ""); err == nil {
			err = l.model(t, req.Type)
		}
	} else {
		t, err = l.resolve(ctx, req.Agent, req.Pane, loc.Pane, req.Type, req.NoStop)
	}
	if err != nil {
		return port.Started{}, err
	}
	if why, lead := l.quarantined(t); why != "" {
		if lead { // the owner is at the lead's (or its drive's) screen: the Gatekeeper dialog can be answered there
			l.say("warning: " + why)
		} else {
			return port.Started{}, port.Refused{Line: why}
		}
	}
	if waited {
		if err := l.sameAgent(ctx, t); err != nil {
			return port.Started{}, err
		}
	}
	if req.NoStop {
		if err := l.laneFree(ctx, t.agent, t.info.Lane, t.info.Cwd); err != nil {
			return port.Started{}, err
		}
	}
	resumed, err := l.sessionOf(ctx, t, req)
	if err != nil {
		return port.Started{}, err
	}
	sid, src := resumed.sid, resumed.src
	// The caller's own pane: schedule a detached run; that run waited for the agent to go idle (waitScheduled). A
	// NoStop start stops nothing, so it has nothing to schedule (otherProgram refuses the pane its caller holds).
	if !placing && !req.NoStop && l.ownPane(t) && !waited {
		line, err := l.schedule(t, req, false)
		return port.Started{Line: line}, err
	}

	// The effort: the request's (a dispatch's type reference), else the role's (roles.<role>.type), else the
	// type's args; it applies to a resume too, so a role's session keeps the effort it was started with.
	effort := req.Effort
	if effort == "" {
		effort = t.info.Effort
	}
	first, err := l.firstPrompt(ctx, t, req, sid)
	if err != nil {
		return port.Started{}, err
	}
	// The agent's workspace, prepared before its start is built: a missing role worktree added (roleWorktree), so the
	// kind's start reads the tree the agent starts in (agy's hooks check reads that tree's hooks file). Before the
	// stop: a start the kind refuses leaves a running agent running.
	if err := l.roleWorktree(ctx, t); err != nil {
		return port.Started{}, err
	}
	// The start, built once before anything is stopped: its argv and its env from one complete start.
	args, err := l.build(t, effort, sid, first.prompt)
	if err != nil {
		return port.Started{}, err
	}
	if t.info.Lane != "" { // a lane's env: the generic facts and the project's lanes.env
		t.env = config.WithLaneEnv(t.env, l.Cfg)
	}
	switch {
	case placing:
		if err := l.place(ctx, t, req.Place); err != nil {
			return port.Started{}, err
		}
	case req.NoStop: // in place of the stop: a pane running another program than its shell is refused
		if err := l.otherProgram(ctx, t.pane); err != nil {
			return port.Started{}, err
		}
	default:
		if err := l.stop(ctx, t, req.Force, false, "--force to restart anyway"); err != nil {
			return port.Started{}, err
		}
	}
	if sid != "" {
		l.say("resuming %s (from %s)", sid, src)
	}
	// The start runs only once the pane is a shell again: until the old agent's exit is reaped, the terminal refuses
	// the start (PaneBusy), and the env line would fail in a pane that still runs it. One bound covers this wait and the
	// start's retries; past it, no start and a FAIL naming the pane. A pane that ran an agent is waited on (Await), and a
	// pane just placed (its shell may still be coming up): a pane at a shell, or one running a person's program, is
	// setEnv's to refuse.
	st := Start{Agent: t.agent, Kind: t.kind, Pane: t.pane, Label: t.entry.Label, Args: args,
		Resume: sid, Record: resumed.record, Session: t.session, Prompt: first.prompt, Again: req.Again, Fresh: req.Fresh,
		Lane: t.info.Lane, Shell: l.shellWait(),
		Await: t.entry.Agent != "" || placing,
		Env:   func(ctx context.Context) error { return l.startEnv(ctx, t) },
		Whose: AgentEnv{Agent: t.agent, Role: t.info.Role, Lane: t.info.Lane, Cwd: t.info.Cwd, Vars: t.env}}
	seen, err := l.start(ctx, st)
	if seen.ID != "" && seen.ID != t.pane { // the terminal ran the agent in another pane: it runs there
		t.pane, t.entry.Terminal = seen.ID, seen.Terminal
	}
	trust, err := l.outcome(ctx, t, st, err)
	if err != nil {
		return port.Started{}, err
	}
	switch {
	case first.wake != "" && sid != "":
		if _, err := l.H.Prompt(ctx, t.pane, first.wake, false); err != nil {
			l.say("warning: %s's wake not typed into pane %s: %v (type it there: %s)", first.what, t.pane, err, first.wake)
		} else {
			l.say("%s: its wake typed", first.what)
		}
	case first.job != "":
		l.say("%s: its wake is the first prompt", first.what)
	}
	if sid == "" {
		sid = "new"
	}
	// The session the start row records: the one herdr shows for the started agent, else the id obor named it
	// (t.session: a fresh start of a kind whose sessions obor names).
	session := seen.Session
	if session == "" {
		session = t.session
	}
	started := port.Started{Line: fmt.Sprintf("RESTARTED %s (pane %s, session %s)", t.agent, t.pane, sid), Pane: t.pane,
		Terminal: t.entry.Terminal, Kind: t.kind, Session: session}
	if trust != nil {
		return started, trust
	}
	return started, nil
}

// panesOnly is a NoAgent launch (obor setup's pane without --start): the agent's pane labelled and its env typed, no
// agent started. Where, as every launch decides it: Locate (adds no agent: serve down does not refuse a label), under
// the agent's restart lock held to the last step. The agent running → a *port.Running, nothing done; another agent in
// its lane → refused (laneFree, the decision of a NoStop start too); its labelled pane there already with no agent,
// no --pane named (a rerun) → Started.Idle, nothing done; else the --pane target (an existing pane, its shell may hold
// the env of the agent that ran there: its env line typed) or a pane req.Place places with the env of the agent's
// role and lane (no start is built, so none of a kind's own; every registry var it does not set goes empty, so the
// pane inherits none of its placer's). The pane is labelled Agent last.
func (l *Launcher) panesOnly(ctx context.Context, req Request) (port.Started, error) {
	loc, end, err := l.Locate(ctx, req.Agent, req.Pane, false)
	if err != nil {
		return port.Started{}, err
	}
	defer end()
	if loc.Runs {
		return port.Started{}, &port.Running{Agent: req.Agent, Pane: loc.Pane}
	}
	t := &target{agent: req.Agent}
	if l.Cfg != nil {
		t.info, t.known = l.Cfg.AgentLookup(req.Agent, "")
	}
	if err := l.laneFree(ctx, t.agent, t.info.Lane, t.info.Cwd); err != nil {
		return port.Started{}, err
	}
	if req.Pane == "" && loc.Pane != "" {
		return port.Started{Pane: loc.Pane, Idle: true}, nil
	}
	if t.info.Lane != "" { // a lane's env: the generic facts and the project's lanes.env
		t.env = config.WithLaneEnv(nil, l.Cfg)
	}
	switch {
	case loc.Pane != "": // the --pane target
		t.pane = loc.Pane
		l.say("pane %s (existing)", t.pane)
		if err := l.setEnv(ctx, t); err != nil {
			return port.Started{}, err
		}
	case req.Place != nil:
		if err := l.place(ctx, t, req.Place); err != nil {
			return port.Started{}, err
		}
	default:
		return port.Started{}, project.Failf("no pane labelled %s (pass --pane)", req.Agent)
	}
	if loc.Label != req.Agent {
		if err := l.H.RenamePane(ctx, t.pane, req.Agent); err != nil {
			return port.Started{}, project.Failf("terminal pane rename %s %s failed", t.pane, req.Agent)
		}
		l.say("labelled pane %s %s", t.pane, req.Agent)
	}
	return port.Started{Pane: t.pane, Terminal: t.entry.Terminal}, nil
}

// carries: req's first prompt goes with a request that may resume (serve's: a lane worker started again with its
// outstanding job's wake, or the integrator with its job's): no session to resume starts fresh with it, a resumed
// session gets it typed once up, as a job's wake.
func carries(req Request) bool { return !req.Fresh && req.Prompt != "" }

// resumption is the start's session decision (sessionOf): the session the start resumes (sid; "": a fresh start),
// where it came from (src: "record" or "herdr"; "" for a fresh start), and record: the start records sid, with the
// start's kind, as the agent's session once it runs (Start.Record; done).
type resumption struct {
	sid, src string
	record   bool
}

// sessionOf is the start's session decision, made before anything is stopped: one rule over the agent's recorded
// session, herdr's live session for its pane and the request, its rows in order:
//   - a kind that resumes nothing (agents.Caps.Resume false): fresh, said when a session is recorded (serve records
//     the session, but there is nothing obor resumes);
//   - req.Fresh: fresh;
//   - a record of this kind (or of no kind, an older record): it resumes; herdr's live session for the pane is a
//     cross-check, said when it differs; a record whose transcript is gone (checkTranscript) is fresh, said;
//   - a record of another kind (the agent ran another kind before, a dispatch of another type): never resumed; herdr's
//     live session of this kind resumes (adopted), else a new session, said;
//   - no record: herdr's live session when it is this kind's (herdr names this kind, or none) resumes (adopted), else
//     none.
//
// A kind whose sessions obor names itself (agents.Caps.NamedSession: Claude) never adopts herdr's: obor chose the
// recorded id, and a session with no record is a new one. Nothing recorded and nothing to adopt, with neither req.Fresh
// nor a first prompt the request carries (carries): a role agent other than the lead (no lane row; the integrator
// counts) starts fresh, said, only when its pane runs no agent (at its shell, or no pane yet: the first start, a
// stopped role); a pane that runs one (herdr names it, or its shell does not hold the foreground) is a Fail before
// anything is stopped, as a lane's agent is (it has lost its context, and a person picks --fresh). The lead is a Fail
// too, its pane at its shell included: its fresh start is obor lead's or a handoff restart's (req.Fresh), so a plain
// restart of it with nothing to resume keeps the refusal. A session the rule puts aside (another
// kind's record, a transcript gone) starts fresh, said. Every session the start resumes, recorded or adopted, is
// recorded with the start's kind once the agent runs (record): an adopted session becomes the agent's current one
// whether or not its kind reports sessions or fires a start hook on resume (agy: neither), so the next restart, the
// keep-lane identity and the context readers read it, not a stale record.
func (l *Launcher) sessionOf(ctx context.Context, t *target, req Request) (resumption, error) {
	kinds := l.Cfg.Kinds()
	if !kinds.Resumes(t.kind) {
		if t.sid != "" {
			l.say("warning: no resume for kind %s: new session", t.kind)
		}
		return resumption{}, nil
	}
	if req.Fresh {
		return resumption{}, nil
	}
	live, adopts := t.entry.Session, !kinds.NamedSession(t.kind)
	var r resumption
	switch {
	case t.sid != "" && (t.sidKind == "" || t.sidKind == t.kind): // this kind's record
		if live != "" && !sameSid(live, t.sid) {
			l.say("warning: the terminal reports session %s for pane %s, the recorded session is %s: resuming %s", live,
				t.pane, t.sid, t.sid)
		}
		fresh, err := l.checkTranscript(t, t.sid)
		if err != nil {
			return resumption{}, err
		}
		if fresh {
			return resumption{}, nil
		}
		r = resumption{sid: t.sid, src: "record"}
	case t.sid != "": // another kind's record
		why := fmt.Sprintf("session %s is %s's, the type %s is %s's", t.sid, t.sidKind, t.info.Type, t.kind)
		if live == "" || t.entry.Agent != t.kind || !adopts {
			l.say("%s: new session", why)
			return resumption{}, nil
		}
		l.say("%s: resuming the terminal's %s", why, live)
		r = resumption{sid: live, src: "herdr"}
	case live != "" && adopts && (t.entry.Agent == t.kind || t.entry.Agent == ""): // no record: herdr's of this kind
		r = resumption{sid: live, src: "herdr"}
	case !carries(req) && t.info.Role != "" && t.info.Role != "lead" && t.info.Lane == "" && !l.runsAgent(ctx, t): // no agent in its pane
		l.say("no session recorded for %s: new session", t.agent)
		return resumption{}, nil
	case !carries(req):
		return resumption{}, project.Failf("no session to resume for %s (pass --fresh to start a new one)", t.agent)
	default:
		return resumption{}, nil
	}
	r.record = true
	return r, nil
}

// runsAgent: the pane t starts in runs an agent now, by either field the target has for it: herdr names an agent on the
// pane's entry, or (herdr naming none) the pane's shell does not hold the foreground. No pane yet (the caller places
// one: the first start, or a stopped role's pane is gone): nothing runs there.
func (l *Launcher) runsAgent(ctx context.Context, t *target) bool {
	if t.entry.Agent != "" {
		return true
	}
	return t.pane != "" && !l.atShell(ctx, t.pane)
}

// opening is a start's first prompt decision (firstPrompt): the prompt its argv carries ("": none, a resume's), the
// wake typed into a resumed session once it is up (what names it in the lines said), and the open job whose wake it
// is ("": none).
type opening struct {
	prompt, wake, what, job string
}

// firstPrompt is the start's first prompt, sid the session it resumes ("": fresh). An agent restarted with no first
// prompt of its own while serve holds a job for it: that job's wake, the first prompt of a fresh session, typed into a
// resumed one once it is up. A lane's worker asks for its lane's job (a work or fix job), the integrator for a task's
// open review or resolve job. Resolved before the stop: a lane with a task whose job serve cannot tell refuses the
// restart, the old agent untouched. A fresh start with no prompt and no job gets its role's (a handoff restart:
// the role's handoff form).
func (l *Launcher) firstPrompt(ctx context.Context, t *target, req Request, sid string) (opening, error) {
	o := opening{what: "the first prompt"}
	if req.Prompt == "" && l.Job != nil && (t.info.Role == "integrator" || t.info.Role == "worker" && t.info.Lane != "") {
		job, err := l.Job(ctx, t.info.Role, t.info.Lane)
		if err != nil {
			var fail project.Fail
			if errors.As(err, &fail) { // restart's own refusal: the whole FAIL line (serve down for the integrator)
				return opening{}, err
			}
			return opening{}, project.Failf("%s not restarted (nothing stopped): %v", t.agent, err)
		}
		o.job = job
	}
	switch {
	case o.job != "":
		o.wake, o.what = wire.WakeLine(o.job), "job "+o.job
	case carries(req):
		o.wake = req.Prompt
	}
	o.prompt = req.Prompt
	switch {
	case sid != "":
		o.prompt = "" // a resume carries no first prompt: the wake is typed once it is up
	case o.job != "":
		o.prompt = o.wake
	case o.prompt == "":
		// A fresh start without a first prompt gets its role's, e.g. the lead's /obor:lead (or, on a kind with no
		// plugin slash command, a prompt naming the skill file): a session the owner starts presents the state and
		// options. Only a handoff restart, which asks for it (Request.Handoff), continues from a Handoff
		// (`/obor:lead handoff`).
		if rc, ok := l.Cfg.Role(t.info.Role); ok {
			k, _ := l.Cfg.Kind(t.kind)
			kit := l.kitRoot()
			o.prompt = rc.Role.StartPrompt(k, kit)
			if req.Handoff {
				o.prompt = rc.Role.HandoffPrompt(k, kit)
			}
		}
	}
	return o, nil
}

// outcome is what start's error err means for the launch of t (st the start made): nil, nothing; a fresh start held by
// a trust dialog (start's Trust) is started, its success path run (done), and returned as the *port.TrustDialog a
// caller adopts or fails on; any other StartError is the launch's FAIL with evidence, marked project.RetryHint when
// its line names the command that starts the agent again (st.Again, else a restart); another error is returned as it
// is. A resumed start at the dialog is NotReady: its wake is not typed, so it failed.
func (l *Launcher) outcome(ctx context.Context, t *target, st Start, err error) (*port.TrustDialog, error) {
	var se *StartError
	switch {
	case err == nil:
		return nil, nil
	case errors.As(err, &se) && se.Trust:
		// The agent runs at the dialog and takes its first prompt (in its args) once a person answers it.
		l.say("started: %s", se.Hint)
		l.done(ctx, st, port.Pane{})
		return &port.TrustDialog{Hint: se.Hint}, nil
	case errors.As(err, &se) && se.NoShell:
		return nil, project.RetryHint(l.giveUp(ctx, t, "pane %s is not an available shell after %s: start the agent there with %s",
			t.pane, l.shellWait(), l.retry(t.agent, t.pane, st)))
	case errors.As(err, &se) && se.NotReady:
		fail := l.giveUp(ctx, t, "start failed in %s (%s)", t.pane, se.Hint)
		if se.Retry {
			return nil, project.RetryHint(fail)
		}
		return nil, fail
	case errors.As(err, &se) && port.PaneBusy(se.Err):
		why := "the pane is not an available shell: the old agent has not left it"
		if t.placed {
			why = "the pane is not an available shell"
		}
		return nil, project.RetryHint(l.giveUp(ctx, t, "start failed in %s (%s; run %s again or start it by hand)",
			t.pane, why, l.retry(t.agent, t.pane, st)))
	case errors.As(err, &se):
		return nil, project.RetryHint(l.giveUp(ctx, t, "start failed in %s (no agent runs there: the pane is at its shell, maybe mid-command: run %s again or start it by hand)",
			t.pane, l.retry(t.agent, t.pane, st)))
	}
	return nil, err
}

// place puts t's start in a pane p places (t's agent has none), the start's env for its shell (paneEnv), then reads
// that pane's facts (its terminal id) from the pane list. A pane just placed is the project's and runs nothing, so
// resolve's checks of a found pane (inProject, suspended) do not apply. An empty pane id is a Fail.
func (l *Launcher) place(ctx context.Context, t *target, p *port.Placer) error {
	pane, err := p.Place(ctx, paneEnv(l.Cfg.Kinds(), t))
	if err != nil {
		return err
	}
	if pane == "" {
		return project.Failf("no pane placed for %s (the placement named none)", t.agent)
	}
	l.say("pane %s (placed)", pane)
	t.pane, t.entry, t.placed = pane, port.Pane{ID: pane}, true
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return project.Failf("terminal pane list failed (is the terminal running?)")
	}
	t.panes = panes
	for _, q := range panes {
		if q.ID == pane {
			t.entry = q
		}
	}
	return nil
}

// paneEnv is t's start env for a new pane's shell, NAME=VALUE, by the one builder of a start's env
// (agents.Kinds.StartVars of the project's kinds): every registry var the start does not set goes empty ("NAME="), so
// the pane inherits none of its placer's (a stale OBOR_AGENT, the caller's OBOR_PANE: the new pane's id is exported by
// the env line), and so does a var the kind's start clears.
func paneEnv(kinds agents.Kinds, t *target) []string {
	set, unset := kinds.StartVars(roleEnv(t.info.Role), t.info.Lane, "", t.env)
	env := make([]string, 0, len(set)+len(unset))
	for _, v := range set {
		env = append(env, v.Name+"="+v.Value)
	}
	for _, v := range unset {
		env = append(env, v+"=")
	}
	return env
}

// laneFree refuses a start of agent in its lane (cwd the lane's worktree) while another agent runs there (one agent in
// a lane): the one decision of Launch's NoStop start and of a NoAgent launch, over one read of the
// terminal's panes. Called once Locate found agent not running, so every agent there is another. The refusal names the
// occupant to stop by its label when the config names that agent, else (a person's label, a pane nobody labelled) the
// pane to quit it in. A start with no lane or no cwd has nothing to refuse. Nothing is touched.
func (l *Launcher) laneFree(ctx context.Context, agent, lane, cwd string) error {
	if lane == "" || cwd == "" {
		return nil
	}
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return project.Failf("terminal agent list failed (is the terminal running?)")
	}
	for _, p := range panes {
		if p.Agent == "" || p.Cwd != cwd {
			continue
		}
		who := cmp.Or(p.Label, p.Agent) // its pane's label, else (a pane nobody labelled) its kind
		if l.Cfg.NamesOtherAgent(p.Label, agent) {
			return project.Failf("%s still runs in lane %s (pane %s): obor restart %s --stop first", who, lane, p.ID, p.Label)
		}
		return project.Failf("%s still runs in lane %s (pane %s): quit the agent in pane %s first", who, lane, p.ID, p.ID)
	}
	return nil
}

// quarantined is why t's kind may not start on this machine (agents.Kinds.StartRefusal), and whether the start may
// still be made with a warning: the lead or the owner's drive agent, whose owner is at its screen and can answer
// Gatekeeper's dialog. A worker's or role's start is refused (the line names the files and the fix), so no unattended
// pane waits on it.
func (l *Launcher) quarantined(t *target) (why string, owners bool) {
	why = l.Cfg.Kinds().StartRefusal(t.kind, l.installHost())
	return why, why != "" && (t.info.Role == "lead" || t.info.Role == roles.Driver)
}

// installHost is the machine the kind checks read (Install; else project.InstallHost of this launcher's env).
func (l *Launcher) installHost() agents.InstallHost {
	if l.Install != nil {
		return l.Install()
	}
	return project.InstallHost(l.envOrOS())
}

// checkTranscript: what a recorded session needs to resume. For a kind whose sessions obor names itself (
// agents.Caps.NamedSession) that is the transcript the session reported, as the SessionStart hook recorded it: the
// path is the session's own (Claude resolves symlinks and hashes a long cwd, so obor derives none), so a pane cwd
// that moved or a symlinked lane does not refuse it. A session with no transcript file was never prompted (Claude
// writes none before its first message): it is no session to resume, so the restart starts fresh, said; fresh
// is true then. A record that names no transcript yet (obor chose its id, and no SessionStart hook confirmed it)
// resumes when the kind's own file of that id is somewhere. Other kinds are not checked: their sessions are not
// obor's to check (Codex's rollout, which launch records itself).
func (l *Launcher) checkTranscript(t *target, sid string) (fresh bool, err error) {
	k, ok := l.Cfg.Kind(t.kind)
	if !ok || !k.Caps().NamedSession {
		l.say("recorded session %s not checked (%s's sessions are not obor's)", sid, t.kind)
		return false, nil
	}
	if path := t.transcript; path != "" {
		if _, err := os.Stat(path); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) { // a search that failed is no proof of "never prompted"
			return false, project.Failf("recorded session %s: cannot check its transcript %s: %v (pass --fresh)", sid, path, err)
		}
		l.say("recorded session %s has no transcript (%s): starting fresh", sid, path)
		return true, nil
	}
	// No recorded path: the kind's own file of that id, anywhere (the hook has not filled the path in yet).
	found, err := transcriptElsewhere(k.Transcript(l.Env.Get, sid))
	if err != nil {
		return false, project.Failf("recorded session %s: cannot search its transcript: %v (pass --fresh)", sid, err)
	}
	if found {
		return false, nil
	}
	l.say("recorded session %s has no transcript (never prompted): starting fresh", sid)
	return true, nil
}

// transcriptElsewhere: whether the transcript wild is "<projects>/*/<file>" names exists under any project dir. The
// projects dir is listed literally (no glob: a base dir with glob metacharacters) and a failure other than
// "no such dir" is an error, never "not found".
func transcriptElsewhere(wild string) (bool, error) {
	dir, file := filepath.Dir(filepath.Dir(wild)), filepath.Base(wild)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		_, err := os.Stat(filepath.Join(dir, e.Name(), file))
		switch {
		case err == nil:
			return true, nil
		case !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR):
			return false, err
		}
	}
	return false, nil
}

// AgentEnv is port.AgentEnv.
type AgentEnv = port.AgentEnv

// setEnv: env and cwd as obor setup gives them; the pane's shell keeps them for the agent. Typed only at the shell,
// into the role worktree a role agent's cwd is, prepared first (roleWorktree). A NoAgent launch's (panesOnly).
func (l *Launcher) setEnv(ctx context.Context, t *target) error {
	if err := l.otherProgram(ctx, t.pane); err != nil {
		return err
	}
	if err := l.roleWorktree(ctx, t); err != nil {
		return err
	}
	return l.typeEnv(ctx, t)
}

// startEnv is a start's Env step: setEnv, its workspace already prepared (Launch prepares it before it builds the
// start).
func (l *Launcher) startEnv(ctx context.Context, t *target) error {
	if err := l.otherProgram(ctx, t.pane); err != nil {
		return err
	}
	return l.typeEnv(ctx, t)
}

// typeEnv types t's env line at its pane's shell; nothing started when it does not finish.
func (l *Launcher) typeEnv(ctx context.Context, t *target) error {
	// The line's own success word or failure word says how it ended (project.RunEnvLine).
	res, err := project.RunEnvLine(ctx, l.H, t.pane, envLine(l.Cfg.Kinds(), t), l.poll(), l.wait())
	switch {
	case err != nil:
		return project.Failf("terminal pane run %s (env) failed", t.pane)
	case res == project.EnvLineNoMark:
		return l.giveUp(ctx, t, "the shell in %s did not finish the env line in %s (nothing started; the env line may have failed: check the pane)",
			t.pane, l.wait())
	case res == project.EnvLineFailed:
		return l.giveUp(ctx, t, "the env line in %s failed (nothing started; check the pane)", t.pane)
	}
	return nil
}

// otherProgram refuses pane when its foreground is another program than its shell (an agent, a person's program):
// nothing is typed there. A process info that cannot be read refuses nothing.
func (l *Launcher) otherProgram(ctx context.Context, pane string) error {
	pi, err := l.H.ProcessInfo(ctx, pane)
	if err != nil || pi.AtShell() {
		return nil
	}
	prog := pi.Foreground()
	if prog == "" {
		prog = "unknown"
	}
	return project.Failf("%s runs another program (%s)", pane, prog)
}

// envOrOS is the launcher's env, the process's own when it has none.
func (l *Launcher) envOrOS() project.Env {
	if l.Env == nil {
		return project.OSEnv
	}
	return l.Env
}

// roleWorktree prepares the workspace a role agent starts in: the role worktree its cwd is, added when missing
// (detached at merge.target_branch) and given the links it lacks (project.WorktreeLinks, project.LinkInto), so the
// kind's start reads the tree the agent starts in (agy's hooks check reads that tree's hooks file) and the env line's
// cd finds it: serve's starts and obor restart of a project whose role worktree is absent. Launch prepares it before
// the start is built and before the stop: a start the kind refuses leaves a running agent running. l.Workspace
// prepares it instead of git. Any other cwd: nothing.
func (l *Launcher) roleWorktree(ctx context.Context, t *target) error {
	r, ok := roles.Lookup(t.info.Role)
	if !ok || r.Cwd != roles.CwdRoleWorktree || l.Cfg == nil || t.info.Cwd != l.Cfg.RoleWorktree(r.Name) {
		return nil
	}
	prepare := l.Workspace
	if prepare == nil {
		prepare = l.addWorktree
	}
	if err := prepare(ctx, t.info.Cwd); err != nil {
		return project.Failf("%s: its worktree: %v", t.agent, err)
	}
	return nil
}

// addWorktree is roleWorktree's own preparation, by git: the worktree dir added when missing and every link it lacks
// made. A settings file its checkout lacks (git does not track it, or the worktree sits behind the commit that did)
// reaches it only as a link, listed in lanes.links or not, and with it obor's plugin.
func (l *Launcher) addWorktree(ctx context.Context, dir string) error {
	if _, err := project.AddWorktree(ctx, l.Cfg.Root, dir, l.Cfg.MainBranch()); err != nil {
		return err
	}
	_, err := project.LinkInto(l.Cfg.Root, dir, project.WorktreeLinks(l.Cfg, dir), project.Git)
	return err
}

// envLine is the shell line that sets the agent's env (and cwd) as obor setup does, from the one builder of a start's
// env (agents.Kinds.StartVars of the project's kinds) by the one builder of the line (project.EnvLine): OBOR_ROLE, OBOR_LANE, OBOR_PANE (the pane's id) and the kind's vars exported,
// every other var of the registry unset (a stale OBOR_AGENT; another kind's OBOR_OPENCODE_MODEL) and so is a var the
// kind's start clears (a lane's OPENCODE_CONFIG_CONTENT), so nothing of an earlier start in the pane's shell survives.
// An agent the config does not know keeps the shell's OBOR_ROLE (obor cannot tell its role).
func envLine(kinds agents.Kinds, t *target) string {
	role := roleEnv(t.info.Role)
	var keep []string
	if !t.known && role == "" {
		keep = []string{agents.RoleVar}
	}
	set, unset := kinds.StartVars(role, t.info.Lane, t.pane, t.env, keep...)
	env := make([]string, 0, len(unset)+len(set))
	for _, v := range unset {
		env = append(env, v+"=")
	}
	for _, v := range set { // StartVars sets no name empty: each is exported
		env = append(env, v.Name+"="+v.Value)
	}
	return project.EnvLine(t.info.Cwd, env)
}

// roleEnv is role's env value (OBOR_ROLE's) for a role obor knows, else role as it is ("": none).
func roleEnv(role string) string {
	if r, ok := roles.Lookup(role); ok {
		return r.EnvValue
	}
	return role
}

// build is t's one start, built before anything is stopped: the type's (a role without a type: the defaults
// of the resolved kind; a lead keeps Remote Control; a lane's agent starts strict) with effort in place of
// the type's ("": the type's), resume or the first prompt; its argv returned, t.env its own env (OpenCode's
// model and effort, its generated config) for the env line, t.session the session obor names for it. An agent with
// neither type nor role starts with no launch args: the kind's start over none, still with its env (OpenCode: its
// generated config). An effort the start cannot carry is a Fail (nothing stopped yet).
func (l *Launcher) build(t *target, effort, resume, prompt string) ([]string, error) {
	b := startSpec{Agent: t.agent, Kind: t.kind, Role: t.info.Role, Effort: effort, Dir: t.info.Cwd, Resume: resume,
		Prompt: prompt, Bare: t.info.Type == "" && t.info.Role == ""}
	if t.info.Type != "" {
		lt, _ := l.Cfg.Model(t.info.Type)
		b.Kind, b.Args = lt.Kind, lt.Args
	}
	if !b.Bare {
		if err := l.Cfg.Kinds().CheckEffort(b.Kind, effort); err != nil { // one the args cannot carry: the start's error
			return nil, project.Failf("%s: %v (obor config validate)", t.agent, err)
		}
		rc, _ := l.Cfg.Role(t.info.Role)
		b.WriteDirs = l.Cfg.RoleWriteDirs(rc.Role)
	}
	built, err := l.buildStart(b)
	if err != nil {
		return nil, project.Failf("%s: %v", t.agent, err)
	}
	t.env, t.session = built.Vars, built.Session
	return built.Args, nil
}

// startSpec is an agent's start to build: its type (the model's kind and args), its role ("": none), effort ("": the
// type's), cwd, the dirs outside it the agent writes, and the session to resume or the first prompt. Bare: the kind's
// start over no launch args (an agent with neither type nor role: no settings, not strict).
type startSpec struct {
	Agent, Kind       string
	Args              []string // the model's args (config.Model.Args)
	Role, Effort, Dir string
	WriteDirs         []string
	Resume, Prompt    string
	Bare              bool
}

// startBuilt is a start as its kind built it from the complete start, in one call of a plugin kind: the argv after the
// binary, the start's own env (Vars; "" unsets one) and the session id obor chose for it ("": none, or a resume).
type startBuilt struct {
	Args    []string
	Vars    map[string]string
	Session string
}

// kitRoot is the obor kit a start names: l.Kit, else project.KitRoot(Env).
func (l *Launcher) kitRoot() string {
	if l.Kit != "" {
		return l.Kit
	}
	return project.KitRoot(l.Env)
}

// buildStart is the agent's one start, built once at the launch boundary: the complete agents.Start
// (the kit, the model's args with the role's settings and strictness, the project's override files of the kind, the
// effort, the cwd, the dirs the agent writes,
// which it makes, the resume or the first prompt as one line, the session obor names for a kind that takes one, the
// env and state writer), handed to the kind once (config.Model.Launch, agents.StartOf): the argv, the env and the files
// a plugin kind writes come from one start call. The kind is the one of this moment (agents.AsNow: a plugin kind whose
// command changed is described again here, once), so the override files read and the start call are of one manifest.
// The model's start (config.Model.Launch), or b.Bare the kind's start over no launch args. An unknown kind: the
// args as given (start refuses it).
func (l *Launcher) buildStart(b startSpec) (startBuilt, error) {
	k, ok := l.Cfg.Kind(b.Kind)
	if !ok {
		return startBuilt{Args: append([]string(nil), b.Args...)}, nil
	}
	k, err := agents.AsNow(k)
	if err != nil {
		return startBuilt{}, err
	}
	for _, d := range b.WriteDirs { // a dir a start lets the agent write (Codex's --add-dir) must be there
		if err := os.MkdirAll(d, 0o755); err != nil {
			return startBuilt{}, err
		}
	}
	kit := l.kitRoot()
	// A kind whose sessions obor names: the id is obor's, recorded for the agent before it runs (Start), so the
	// store and a restart know it before its SessionStart hook reports it (the hook confirms it, with its transcript).
	session := ""
	if b.Resume == "" && k.Caps().NamedSession {
		session = l.newSession(b.Agent)
	}
	in := agents.Start{Kit: kit, Agent: b.Agent, Effort: b.Effort, Resume: b.Resume, Prompt: oneLine(b.Prompt), Session: session,
		WriteDirs: b.WriteDirs, Dir: b.Dir, Root: l.Cfg.Root, Read: os.ReadFile, Env: l.envOrOS().Get,
		Dirs: project.SubDirs, WriteState: project.StateWriter(l.Cfg.Root)}
	var r agents.Launch
	if b.Bare {
		r, err = agents.StartOf(k, in)
	} else {
		var overrides map[string]string
		if overrides, err = l.Cfg.OverridesOf(k); err == nil {
			r, err = config.Model{Kind: b.Kind, Args: b.Args}.LaunchOf(k, b.Role, overrides, in)
		}
	}
	if err != nil {
		return startBuilt{}, err
	}
	return startBuilt{Args: r.Args, Vars: r.Env, Session: session}, nil
}

func (l *Launcher) kill(pgid int, sig syscall.Signal) error {
	if l.Kill != nil {
		return l.Kill(pgid, sig)
	}
	return syscall.Kill(-pgid, sig)
}
