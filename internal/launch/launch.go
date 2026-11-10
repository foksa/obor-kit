// Package launch starts, stops and restarts an obor agent in its labelled herdr pane (`obor restart`, and serve's
// start of a worker): the one launch path.
//
//	Who      a lane agent, a role agent (lead, integrator, reviewer, triage, deep_dive, advisor), an agents.<name>
//	         entry (config.AgentLookup: the roles table, no second copy of the name rules), an agent whose pane has a
//	         lane worktree as cwd (that lane's agent), or any agent with a session recorded in serve's store. Env
//	         OBOR_ROLE (the role's env value, triage's too; an agents entry without a role: unset), OBOR_LANE (no lane:
//	         unset), the kind's vars and the cwd as obor setup gives them, every other var of the registry unset (agents.StartVars,
//	         a stale OBOR_AGENT or another kind's OBOR_OPENCODE_MODEL);
//	         an unknown agent keeps its OBOR_ROLE, in the pane's cwd. Kind and args from the dispatch's model (the
//	         launch's, else a lane agent's lane row model), else the role / lead / agents entry's model (config.Model: base args, model id, kinds args, then --settings for
//	         claude); no model: the kind herdr reports for the pane, else claude, no args.
//	Where    --pane, else the pane labelled <agent> (in this project). Never a pane labelled for another
//	         agent, never a second pane labelled <agent>, never a known agent's pane whose cwd is outside this project
//	         Another pane holding the agent's herdr name (moving between panes) → FAIL with a hint: no moves.
//	Session  the recorded sid, else the one herdr reports for the pane (never herdr's for a kind whose sessions obor
//	         names itself: Claude); --fresh: a new one. None → FAIL (pass --fresh), checked before anything is
//	         stopped. A fresh start of such a kind records the id obor chose before the agent runs. A recorded Claude
//	         sid resumes from the transcript the session itself reported (the SessionStart hook's path); none
//	         anywhere (never prompted): starts fresh, said. A session of another kind than the dispatch's type
//	         starts fresh, said.
//	Stop     see Stop. A child the pane's shell stopped (Ctrl+Z: herdr then sees an idle shell) → FAIL with a hint, before
//	         anything is stopped or typed: no fg resume.
//	Shell    a restart whose pane ran an agent waits after the stop for the pane to be a shell again (ShellWait:
//	         herdr answers agent_pane_busy until the old agent's exit is reaped), and a start herdr answers
//	         agent_pane_busy is retried like that within the same bound; past it, no start and a FAIL naming the pane.
//	         RESTARTED only when the start succeeded and its agent is seen.
//	Start    the env line typed at the shell (never into another program), then Start (the one start, shared with
//	         obor setup): the pane labelled <agent>, herdr agent start (agent_name_taken: cleared and retried once), a
//	         look for a startup dialog. The start ends when its agent runs in the pane, not at herdr's own bound for an
//	         agent ready for input. herdr's agent name is neither required nor renamed back.
//	Self     the caller's own pane (project.Pane): scheduled, see self.go.
//
// Every operation registers one op record (kind restart) for its whole run.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
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
	Up   time.Duration // for a started agent to come up in its pane (default 30 s: herdr's own startup bound)
	Idle time.Duration // Stop with force: for a working agent to go idle after Escape (default port.StopIdle)
	Poll time.Duration // how often it looks (default 1 s)
	// Shell is how long a restart waits after its stop for the pane to be a shell again, and retries a start herdr
	// answered agent_pane_busy: herdr keeps the pane no available shell until the old agent's exit is reaped (default
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
	// Report records a session a start found (a Codex rollout); nil: the session request to Cfg.Root's serve.
	Report func(ctx context.Context, s store.Session) error
	// Rollout is how long a fresh start looks for its new session's transcript (default 10 s).
	Rollout time.Duration
	// Background runs the search for a fresh start's new session transcript after the start returned (obor serve: a
	// goroutine, as its starts run one at a time and must not wait for it); nil: before the start returns (a CLI
	// restart, which exits after).
	Background func(search func())
	// Log is where a background search says what it found (none, or several open); nil: nowhere.
	Log io.Writer
	// Open is the files processes pids hold open (a fresh start's session: the transcript its agent has open);
	// nil: /proc/<pid>/fd on linux, lsof elsewhere.
	Open func(pids []int) []string
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
}

// Request is one `obor restart` (the core's port.Launch: Launcher is an port.Agents).
type Request = port.Launch

var _ port.Agents = (*Launcher)(nil)

// Args writes r into argv (after the "restart" subcommand): schedule puts it into the detached run's argv and
// ParseRequest reads that back, so a flag cannot be carried on one side only. The Prompt, Effort and Type are not
// restart's (a brief's or a dispatch's start): a scheduled run is the CLI's restart, which carries none of them.
func Args(r Request) []string {
	args := []string{r.Agent}
	if r.Pane != "" {
		args = append(args, "--pane", r.Pane)
	}
	if r.Fresh {
		args = append(args, "--fresh")
	}
	if r.Force {
		args = append(args, "--force")
	}
	return args
}

// ParseRequest reads an `obor restart` argument list (after the subcommand) back into the Request Args wrote; the
// other half of that pair. Flags restart owns but a Request does not carry (--stop) are not its business and
// are an error, as is every flag restart gave up (--prompt, --type, --effort, --sid).
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
	known bool // the config knows it (lane, role, agents entry, lane cwd)
	info  config.AgentInfo
	sid   string // recorded session id
	// sidKind is the recorded session's kind ("": not recorded); dispatched: the type is a dispatch's (the launch's or
	// the lane row's), not the configured one.
	sidKind    string
	dispatched bool
	// transcript is the recorded session's transcript, as the session reported it (Claude's SessionStart hook);
	// "" when the record names none (a start obor named, before its hook ran).
	transcript string
	pane       string
	entry      port.Pane // the pane as pane list shows it
	panes      []port.Pane
	kind       string
	env        map[string]string // the start's own env (agents.Kind.StartEnv; "" unsets the variable), set by the env line
	session    string            // the session id obor chose for the start (build; "": none)
}

// resolve is who and where (shared by Restart and Stop) with typ the launch's model type ("": none; port.Launch.Type, a
// dispatch's); nothing is typed.
func (l *Launcher) resolve(ctx context.Context, agent, pane, typ string) (*target, error) {
	t := &target{agent: agent}
	if l.Cfg != nil {
		t.info, t.known = l.Cfg.AgentLookup(agent, "")
		if !t.known {
			// An agent whose pane (--pane, else its labelled one) has a lane worktree as cwd: that lane's agent.
			p := pane
			if p == "" {
				p, _ = l.PaneOf(ctx, agent, "")
			}
			if cwd := l.paneCwd(ctx, p); cwd != "" {
				if info, ok := l.Cfg.AgentLookup(agent, cwd); ok {
					t.info, t.known = info, true
					l.say("lane %s (pane %s cwd)", info.Lane, p)
				}
			}
		}
	}
	rec, _ := l.session(agent)
	t.sid, t.sidKind, t.transcript = rec.ID, rec.Kind, rec.Transcript
	if !t.known && t.sid == "" && pane == "" {
		return nil, project.Failf("unknown agent %s (no lane, role, agents entry or recorded session)", agent)
	}
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return nil, project.Failf("herdr pane list failed (is herdr running?)")
	}
	t.panes = panes
	src := "--pane"
	if pane == "" {
		if pane, err = l.paneIn(panes, agent, ""); err != nil {
			return nil, project.Failf("no pane labelled %s (pass --pane)", agent)
		}
		src = "label"
	}
	t.pane = pane
	for _, p := range panes {
		if p.ID == pane {
			t.entry = p
		}
	}
	l.say("pane %s (from %s)", pane, src)
	// The label is the pane's identity (R-28 §2): never take a pane labelled for another agent, never label a second pane.
	if t.entry.Label != "" && t.entry.Label != agent {
		return nil, project.Failf("pane %s is labelled %s, not %s", pane, t.entry.Label, agent)
	}
	for _, p := range panes {
		if p.Label == agent && p.ID != pane {
			return nil, project.Failf("pane %s is already labelled %s (obor restart %s there)", p.ID, agent, agent)
		}
	}
	if err := l.inProject(ctx, t); err != nil {
		return nil, err
	}
	l.dispatchType(t, typ)
	if t.info.Type != "" {
		lt, ok := l.Cfg.Model(t.info.Type)
		if !ok || lt.Kind == "" {
			return nil, project.Failf("unknown model %s (models: %s)", t.info.Type, strings.Join(l.Cfg.ModelNames(), ", "))
		}
		t.kind = lt.Kind
	}
	if t.kind == "" {
		t.kind = t.entry.Agent
	}
	if t.kind == "" {
		t.kind = agents.Default
	}
	if pi, err := l.H.ProcessInfo(ctx, pane); err == nil && pi.AtShell() {
		// herdr's process info lists only the foreground group: a stopped child of the shell (Ctrl+Z) is not in it.
		out, err := l.ps("-A", "-o", "ppid=,stat=,pid=,comm=")
		if err != nil {
			l.say("warning: cannot check pane %s for a suspended agent (ps: %v)", pane, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 4 && f[0] == strconv.Itoa(pi.ShellPID) && strings.HasPrefix(f[1], "T") {
				l.say("pane %s: %s (pid %s) is stopped", pane, strings.Join(f[3:], " "), f[2])
				return nil, project.Failf("%s is suspended in pane %s (Ctrl+Z): fg it and /exit, or close the pane", agent, pane)
			}
		}
	}
	return t, nil
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
		t.info.Type, t.info.Effort, t.dispatched = typ, effort, true
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
// the main checkout, a lanes_dir worktree, the agent's configured cwd or another worktree of this repository. No cwd
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

// Restart brings req.Agent (back) up in its pane; the last line it means is RESTARTED (the caller prints it).
func (l *Launcher) Restart(ctx context.Context, req Request) (string, error) {
	s, err := l.Launch(ctx, req)
	return s.Line, err
}

// Launch is Restart (port.Agents): its line, and the agent it started there with the session id herdr reported for it
// (the identity serve records for a later rename; none for a scheduled restart of the caller's own pane).
func (l *Launcher) Launch(ctx context.Context, req Request) (port.Started, error) {
	if !config.AgentNameRE.MatchString(req.Agent) {
		return port.Started{}, project.Failf("bad agent name %q", req.Agent)
	}
	waited, err := l.waitScheduled(ctx, req.Agent, req.Pane)
	if err != nil {
		return port.Started{}, err
	}
	t, err := l.resolve(ctx, req.Agent, req.Pane, req.Type)
	if err != nil {
		return port.Started{}, err
	}
	if why, lead := l.quarantined(t); why != "" {
		if lead { // the owner is at the lead's screen: the Gatekeeper dialog can be answered there
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
	end, err := l.begin(ctx, t.agent, t.info.Lane)
	if err != nil {
		return port.Started{}, err
	}
	defer end()

	// Another pane holds the herdr name: herdr would refuse the start. No moves: FAIL before anything is stopped.
	if a, err := l.H.Agent(ctx, t.agent); err == nil && a.ID != "" && a.ID != t.pane {
		return port.Started{}, project.Failf("pane %s holds the name %s: restart it there (obor restart %s)",
			a.ID, t.agent, t.agent)
	}

	// The session, checked before anything is stopped: none and no --fresh → FAIL.
	// A kind that resumes nothing (agents.Caps.Resume false: OpenCode) always starts fresh: serve records its
	// session, but there is nothing obor resumes. The recorded session wins: herdr's live one for the pane is a
	// cross-check, said when it differs. A kind whose sessions obor names itself (agents.Caps.NamedSession: Claude)
	// never adopts herdr's: obor chose the recorded id, and a session with no record is a new one.
	sid, src := t.sid, "record"
	// A first prompt on a request that may resume (serve's: a lane worker started again with its outstanding job's
	// wake, or the integrator with its job's): no session to resume starts fresh with it, a resumed session
	// gets it typed once up, as a job's wake.
	carry := !req.Fresh && req.Prompt != ""
	if !agents.Resumes(t.kind) {
		sid, src = "", ""
		if t.sid != "" {
			l.say("warning: no resume for kind %s: new session", t.kind)
		}
	} else {
		if live := t.entry.Session; live != "" && !sameSid(live, sid) {
			switch {
			case sid != "" && !req.Fresh:
				l.say("warning: herdr reports session %s for pane %s, the recorded session is %s: resuming %s", live, t.pane,
					sid, sid)
			case sid == "" && !agents.NamedSession(t.kind):
				sid, src = live, "herdr"
			}
		}
		if req.Fresh {
			sid = ""
		}
		fresh := false
		// A dispatch's type of another kind than the session's (the lane's last task ran another kind): that
		// session is not this kind's to resume, so a new one.
		if was := map[string]string{"record": t.sidKind, "herdr": t.entry.Agent}[src]; sid != "" && t.dispatched &&
			was != "" && was != t.kind {
			l.say("session %s is %s's, the type %s is %s's: new session", sid, was, t.info.Type, t.kind)
			sid, fresh = "", true
		}
		if sid != "" && src == "record" {
			var err error
			if fresh, err = l.checkTranscript(t, sid); err != nil {
				return port.Started{}, err
			}
			if fresh {
				sid = ""
			}
		}
		if sid == "" && !req.Fresh && !fresh && !carry {
			return port.Started{}, project.Failf("no session to resume for %s (pass --fresh to start a new one)", t.agent)
		}
	}
	// The caller's own pane: schedule a detached run; that run waited for the agent to go idle (waitScheduled).
	if l.ownPane(t) && !waited {
		line, err := l.schedule(t, req, false)
		return port.Started{Line: line}, err
	}

	// The effort: the request's (a dispatch's type reference), else the role's (roles.<role>.type), else the
	// type's args; it applies to a resume too, so a role's session keeps the effort it was started with.
	effort := req.Effort
	if effort == "" {
		effort = t.info.Effort
	}
	// An agent restarted with no first prompt of its own while serve holds a job for it: that job's
	// wake, the first prompt of a fresh session, typed into a resumed one once it is up. A lane's worker asks for its
	// lane's job (a work or fix job), the integrator for a task's open review or resolve job. Resolved before the stop
	// a lane with a task whose job serve cannot tell refuses the restart, the old agent untouched.
	job := ""
	if req.Prompt == "" && l.Job != nil && (t.info.Role == "integrator" || t.info.Role == "worker" && t.info.Lane != "") {
		if job, err = l.Job(ctx, t.info.Role, t.info.Lane); err != nil {
			var fail project.Fail
			if errors.As(err, &fail) { // restart's own refusal: the whole FAIL line (serve down for the integrator)
				return port.Started{}, err
			}
			return port.Started{}, project.Failf("%s not restarted (nothing stopped): %v", t.agent, err)
		}
	}
	wake, what := "", "the first prompt"
	switch {
	case job != "":
		wake, what = wire.WakeLine(job), "job "+job
	case carry:
		wake = req.Prompt
	}
	prompt := req.Prompt
	switch {
	case sid != "":
		prompt = "" // a resume carries no first prompt: the wake is typed once it is up
	case job != "":
		prompt = wake
	case prompt == "":
		// A fresh start without a first prompt gets its role's, e.g. the lead's /obor:lead.
		if rc, ok := l.Cfg.Role(t.info.Role); ok {
			prompt = rc.Role.StartPrompt(t.kind)
		}
	}
	// The start, built once before anything is stopped: its argv and its env from one complete start.
	args, err := l.build(t, effort, sid, prompt)
	if err != nil {
		return port.Started{}, err
	}
	if t.info.Lane != "" { // a lane's env: the generic facts and the project's slots.lane_env
		t.env = config.WithLaneEnv(t.env, l.Cfg)
	}
	if err := l.stop(ctx, t, req.Force, false, "--force to restart anyway"); err != nil {
		return port.Started{}, err
	}
	if sid != "" {
		l.say("resuming %s (from %s)", sid, src)
	}
	// The start runs only once the pane is a shell again: until the old agent's exit is reaped, herdr answers
	// agent.start with agent_pane_busy, and the env line would fail in a pane that still runs it. One bound covers this
	// wait and the start's retries; past it, no start and a FAIL naming the pane. Only a pane that ran an agent is
	// waited on (Await): a pane at a shell, or one running a person's program, is setEnv's to refuse.
	seen, err := l.start(ctx, Start{Agent: t.agent, Kind: t.kind, Pane: t.pane, Label: t.entry.Label, Args: args,
		Resume: sid, Session: t.session, Prompt: prompt, Lane: t.info.Lane, Shell: l.shellWait(), Await: t.entry.Agent != "",
		Env: func(ctx context.Context) error { return l.setEnv(ctx, t) }})
	var se *StartError
	switch {
	case errors.As(err, &se) && se.NoShell:
		return port.Started{}, l.giveUp(ctx, t, "pane %s is not a shell after %s (the old agent has not left): nothing started",
			t.pane, l.shellWait())
	case errors.As(err, &se) && se.NotReady:
		return port.Started{}, l.giveUp(ctx, t, "start failed in %s (%s)", t.pane, se.Hint)
	case errors.As(err, &se) && port.ErrorCode(se.Err) == "agent_pane_busy":
		return port.Started{}, l.giveUp(ctx, t, "start failed in %s (herdr: the pane is not an available shell: the old "+
			"agent has not left it; run obor restart %s --pane %s again or start it by hand)", t.pane, t.agent, t.pane)
	case errors.As(err, &se):
		return port.Started{}, l.giveUp(ctx, t, "start failed in %s (no agent runs there: the pane is at its shell, maybe mid-command: run obor restart %s --pane %s again or start it by hand)",
			t.pane, t.agent, t.pane)
	case err != nil:
		return port.Started{}, err
	}
	switch {
	case wake != "" && sid != "":
		if err := l.H.Wake(ctx, t.pane, wake); err != nil {
			l.say("warning: %s's wake not typed into pane %s: %v (type it there: %s)", what, t.pane, err, wake)
		} else {
			l.say("%s: its wake typed", what)
		}
	case job != "":
		l.say("%s: its wake is the first prompt", what)
	}
	if sid == "" {
		sid = "new"
	}
	return port.Started{Line: fmt.Sprintf("RESTARTED %s (pane %s, session %s)", t.agent, t.pane, sid), Pane: t.pane,
		Terminal: t.entry.Terminal, Kind: t.kind, Session: seen.Session}, nil
}

// quarantined is why t's kind may not start on this machine (agents.StartRefusal), and whether the start may
// still be made with a warning: the lead, whose owner is at its screen and can answer Gatekeeper's dialog. A worker's
// or role's start is refused (the line names the files and the fix), so no unattended pane waits on it.
func (l *Launcher) quarantined(t *target) (why string, lead bool) {
	why = agents.StartRefusal(t.kind, l.installHost())
	return why, why != "" && t.info.Role == "lead"
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
	k, ok := agents.Lookup(t.kind)
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

// SetEnv types the env line of e at pane's shell, as a restart does (obor setup in a pane that already exists: its
// shell may hold the env of the agent that ran there before).
func (l *Launcher) SetEnv(ctx context.Context, pane string, e AgentEnv) error {
	return l.setEnv(ctx, &target{agent: e.Agent, known: true, pane: pane,
		info: config.AgentInfo{Role: e.Role, Lane: e.Lane, Cwd: e.Cwd}, env: e.Vars})
}

// setEnv: env and cwd as obor setup gives them; the pane's shell keeps them for the agent. Typed only at the shell.
func (l *Launcher) setEnv(ctx context.Context, t *target) error {
	if pi, err := l.H.ProcessInfo(ctx, t.pane); err == nil && !pi.AtShell() {
		prog := pi.Foreground()
		if prog == "" {
			prog = "unknown"
		}
		return project.Failf("%s runs another program (%s)", t.pane, prog)
	}
	if err := l.roleWorktree(ctx, t); err != nil {
		return err
	}
	// The line echoes one of two words: the success word only when the whole line (its cd, its exports)
	// succeeded — a `; echo` after a cd that failed still printed it, and the agent started in the shell's old cwd with
	// the old env — and its failure word when it failed, so a visible failure ends the wait at once instead
	// of at the timeout.
	okMark, badMark := envMark(), envMark()
	if err := l.H.Type(ctx, t.pane, envLine(t)+" && echo "+okMark.typed+" || echo "+badMark.typed); err != nil {
		return project.Failf("herdr pane run %s (env) failed", t.pane)
	}
	// One server-owned wait for either mark (the echoed words, which the typed line's quoted forms never match): its
	// timeout is the env line's bound, and the text read says which mark the shell printed.
	screen, err := l.H.WaitForOutput(ctx, t.pane, okMark.echoed+"|"+badMark.echoed, l.wait())
	if err != nil {
		return l.giveUp(ctx, t, "the shell in %s did not finish the env line in %s (nothing started; the env line may have failed: check the pane)",
			t.pane, l.wait())
	}
	if strings.Contains(screen, badMark.echoed) {
		return l.giveUp(ctx, t, "the env line in %s failed (nothing started; check the pane)", t.pane)
	}
	return nil
}

// envOrOS is the launcher's env, the process's own when it has none.
func (l *Launcher) envOrOS() project.Env {
	if l.Env == nil {
		return project.OSEnv
	}
	return l.Env
}

// roleWorktree adds the role worktree a role agent's cwd is when it is missing (the reviewer's, the deep
// dive's, the integrator's <lanes_dir>/<role>), detached at branches.main, so the env line's cd finds it: serve's
// starts and obor restart of a project whose role worktree is absent. Any other cwd: nothing.
func (l *Launcher) roleWorktree(ctx context.Context, t *target) error {
	r, ok := roles.Lookup(t.info.Role)
	if !ok || r.Cwd != roles.CwdRoleWorktree || l.Cfg == nil || t.info.Cwd != l.Cfg.RoleWorktree(r.Name) {
		return nil
	}
	if _, err := project.AddWorktree(ctx, l.Cfg.Root, t.info.Cwd, l.Cfg.MainBranch()); err != nil {
		return project.Failf("%s: its worktree: %v", t.agent, err)
	}
	return nil
}

// envLine is the shell line that sets the agent's env (and cwd) as obor setup does, built by the one builder
// (agents.StartVars): OBOR_ROLE, OBOR_LANE, OBOR_PANE (the pane's id) and the kind's vars exported,
// every other var of the registry unset (a stale OBOR_AGENT; another kind's OBOR_OPENCODE_MODEL), so
// nothing of an earlier start in the pane's shell survives. An agent the config does not know keeps the shell's OBOR_ROLE (obor cannot tell its role).
func envLine(t *target) string {
	role := t.info.Role
	if r, ok := roles.Lookup(role); ok {
		role = r.EnvValue
	}
	var keep []string
	if !t.known && role == "" {
		keep = []string{agents.RoleVar}
	}
	set, unset := agents.StartVars(role, t.info.Lane, t.pane, t.env, keep...)
	line := "unset " + strings.Join(unset, " ")
	if len(set) > 0 {
		words := make([]string, len(set))
		for i, v := range set {
			words[i] = v.Name + "=" + shellQuote(v.Value)
		}
		line += " && export " + strings.Join(words, " ")
	}
	if t.info.Cwd != "" {
		line = "cd " + shellQuote(t.info.Cwd) + " && " + line
	}
	return line
}

// mark is a word the env line echoes (its success word, or its failure word when the line failed): the
// shell's prompt drawn after it is the shell back at its line editor. Before that, zsh runs the line with the tty in
// cooked mode, where a start's command typed at once is echoed raw and cut at the tty's line limit (~1 KB on macOS: a
// long --prompt left the shell at an open quote, live 2026-10-29), while herdr already counts the pane an available
// shell. typed is the word as typed, quoted in its middle so no wrap of the typed line on the screen reads as the
// echoed word.
type mark struct{ typed, echoed string }

var markSeq atomic.Int64

func envMark() mark {
	n := strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(markSeq.Add(1), 36)
	return mark{typed: `obor-env-"` + n + `"`, echoed: "obor-env-" + n}
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// shellQuote is s as one shell word.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// build is t's one start, built before anything is stopped: the type's (a role without a type: the defaults
// of the resolved kind; a lead keeps Remote Control; a lane's agent starts strict) with effort in place of
// the type's ("": the type's), resume or the first prompt; its argv returned, t.env its own env (OpenCode's
// model and effort, its generated config) for the env line, t.session the session obor names for it. An agent with
// neither type nor role starts with no launch args: the kind's start over none, still with its env (OpenCode: its
// generated config). An effort the start cannot carry is a Fail (nothing stopped yet).
func (l *Launcher) build(t *target, effort, resume, prompt string) ([]string, error) {
	b := port.Build{Agent: t.agent, Kind: t.kind, Role: t.info.Role, Effort: effort, Dir: t.info.Cwd, Resume: resume,
		Prompt: prompt}
	bare := t.info.Type == "" && t.info.Role == ""
	if t.info.Type != "" {
		lt, _ := l.Cfg.Model(t.info.Type)
		b.Kind, b.Args = lt.Kind, lt.Args
	}
	if !bare {
		if err := agents.CheckEffort(b.Kind, effort); err != nil { // one the args cannot carry: the start's error
			return nil, project.Failf("%s: %v", t.agent, err)
		}
		rc, _ := l.Cfg.Role(t.info.Role)
		b.WriteDirs = l.Cfg.RoleWriteDirs(rc.Role)
	}
	built, err := l.startOf(b, bare)
	if err != nil {
		return nil, project.Failf("%s: %v", t.agent, err)
	}
	t.env, t.session = built.Vars, built.Session
	return built.Args, nil
}

// Build is the agent's one start (port.Starter), built once at the launch boundary: the complete agents.Start
// (the kit, the model's args with the role's settings and strictness, the effort, the cwd, the dirs the agent writes,
// which it makes, the resume or the first prompt as one line, the session obor names for a kind that takes one, the
// env and state writer), handed to the kind once (config.Model.Launch, agents.StartOf): the argv, the env and the files
// a plugin kind writes come from one start call. An unknown kind: the args as given (Start refuses it).
func (l *Launcher) Build(b port.Build) (port.Built, error) {
	return l.startOf(b, false)
}

// startOf is b's complete start: the model's (config.Model.Launch), or bare the kind's start over no launch args (no
// settings, not strict).
func (l *Launcher) startOf(b port.Build, bare bool) (port.Built, error) {
	k, ok := agents.Lookup(b.Kind)
	if !ok {
		return port.Built{Args: append([]string(nil), b.Args...)}, nil
	}
	for _, d := range b.WriteDirs { // a dir a start lets the agent write (Codex's --add-dir) must be there
		if err := os.MkdirAll(d, 0o755); err != nil {
			return port.Built{}, err
		}
	}
	kit := l.Kit
	if kit == "" {
		kit = project.KitRoot(l.Env)
	}
	// A kind whose sessions obor names: the id is obor's, recorded for the agent before it runs (Start), so the
	// store and a restart know it before its SessionStart hook reports it (the hook confirms it, with its transcript).
	session := ""
	if b.Resume == "" && k.Caps().NamedSession {
		session = l.newSession(b.Agent)
	}
	in := agents.Start{Kit: kit, Effort: b.Effort, Resume: b.Resume, Prompt: oneLine(b.Prompt), Session: session,
		WriteDirs: b.WriteDirs, Dir: b.Dir, Read: os.ReadFile, Env: l.envOrOS().Get,
		WriteState: project.StateWriter(l.Cfg.Root)}
	var r agents.Launch
	var err error
	if bare {
		r, err = agents.StartOf(k, in)
	} else {
		r, err = config.Model{Kind: b.Kind, Args: b.Args}.Launch(b.Role, in)
	}
	if err != nil {
		return port.Built{}, err
	}
	return port.Built{Args: r.Args, Vars: r.Env, Session: session}, nil
}

func (l *Launcher) kill(pgid int, sig syscall.Signal) error {
	if l.Kill != nil {
		return l.Kill(pgid, sig)
	}
	return syscall.Kill(-pgid, sig)
}
