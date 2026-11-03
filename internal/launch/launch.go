// Package launch starts, stops and restarts an obor agent in its labelled herdr pane (`obor restart`, and serve's
// start of a worker): the one launch path.
//
//	Who      a lane agent, a role agent (lead, integrator, reviewer, triage, deep_dive, advisor), an agents.<name>
//	         entry (project.AgentLookup: the roles table, no second copy of the name rules), an agent whose pane has a
//	         lane worktree as cwd (that lane's agent), or any agent with a session recorded in serve's store. Env
//	         OBOR_AGENT, OBOR_ROLE (the role's env value; triage and an agents entry without a role: unset), OBOR_LANE
//	         (no lane: unset) and the cwd as obor setup gives them; an unknown agent gets OBOR_AGENT only, in the pane's
//	         cwd. Kind and args from the dispatch's model (the launch's, else a lane agent's lane row model, T-722), else
//	         the role / lead / agents entry's model (config.Model: base args, model id, kinds args, then --settings for
//	         claude); no model: the kind herdr reports for the pane, else claude, no args.
//	Where    --pane, else the pane labelled <agent> (in this project: RV-19 #3). Never a pane labelled for another
//	         agent, never a second pane labelled <agent>, never a known agent's pane whose cwd is outside this project
//	         (T-301). Another pane holding the agent's herdr name (moving between panes) → FAIL with a hint: no moves.
//	Session  the recorded sid, else the one herdr reports for the pane; --sid <id>; --fresh: a new one. None → FAIL (pass --fresh), checked before anything is stopped. A
//	         recorded Claude sid must have its transcript under the start cwd; none anywhere (never prompted, T-627):
//	         starts fresh, said. A session of another kind than the dispatch's type (T-722): starts fresh, said.
//	Stop     see Stop. A child the pane's shell stopped (Ctrl+Z: herdr then sees an idle shell) → FAIL with a hint, before
//	         anything is stopped or typed: no fg resume.
//	Start    the env line typed at the shell (never into another program), then Start (the one start, shared with
//	         obor setup): the pane labelled <agent>, the name cleared from the pane if herdr still holds it, herdr agent
//	         start (agent_name_taken: cleared and retried once), a look for a startup dialog. The start ends when its
//	         agent runs in the pane and is named, not at herdr's own bound for an agent ready for input (T-404).
//	Self     the caller's own pane (HERDR_PANE_ID): scheduled, see self.go.
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
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/opencode"
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
	Idle time.Duration // Stop with force: for a working agent to go idle after Escape (default 30 s)
	Poll time.Duration // how often it looks (default 1 s)
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
	// Sessions is agent's current recorded session (T-577); nil: serve's store at Cfg.Root (store.CurrentAt).
	Sessions func(agent string) (store.Session, bool)
	// Report records a session a start found (a Codex rollout, T-577); nil: the session request to Cfg.Root's serve.
	Report func(ctx context.Context, s store.Session) error
	// Rollout is how long a fresh start looks for its new session's transcript (default 10 s).
	Rollout time.Duration
	// Background runs the search for a fresh start's new session transcript after the start returned (obor serve: a
	// goroutine, as its starts run one at a time and must not wait for it); nil: before the start returns (a CLI
	// restart, which exits after).
	Background func(search func())
	// Log is where a background search says what it found (none, or several open, T-582); nil: nowhere.
	Log io.Writer
	// Open is the files processes pids hold open (a fresh start's session: the transcript its agent has open, T-588);
	// nil: /proc/<pid>/fd on linux, lsof elsewhere.
	Open func(pids []int) []string
	// Job is the open worker job (<task>/<attempt>/<step>) of the task lane holds in obor serve ("": none, or serve does
	// not answer): obor restart of a lane's worker with no --prompt sends its wake (T-773), never an idle agent with no
	// job. nil: none asked (obor serve's own starts carry their job's wake).
	Job func(ctx context.Context, lane string) string
}

// Request is one `obor restart` (the core's port.Launch: Launcher is an port.Agents).
type Request = port.Launch

var _ port.Agents = (*Launcher)(nil)

// Args writes r into argv (after the "restart" subcommand): schedule puts it into the detached run's argv and
// ParseRequest reads that back. The pair owns the whole mapping, so a new Request field cannot be carried on one
// side only (T-303: a handoff flag once was, and the scheduled run lost it).
func Args(r Request) []string {
	args := []string{r.Agent}
	if r.Pane != "" {
		args = append(args, "--pane", r.Pane)
	}
	switch {
	case r.Fresh:
		args = append(args, "--fresh")
	case r.Sid != "":
		args = append(args, "--sid", r.Sid)
	}
	if r.Force {
		args = append(args, "--force")
	}
	if r.Prompt != "" {
		args = append(args, "--prompt", r.Prompt)
	}
	if r.Effort != "" {
		args = append(args, "--effort", r.Effort)
	}
	if r.Type != "" {
		args = append(args, "--type", r.Type)
	}
	return args
}

// ParseRequest reads an `obor restart` argument list (after the subcommand) back into the Request Args wrote; the
// other half of that pair. Flags restart owns but a Request does not carry (--stop) are not its business and
// are an error.
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
		case "--sid":
			v, ok := value()
			if !ok {
				return Request{}, fmt.Errorf("--sid needs a value")
			}
			r.Sid = v
		case "--fresh":
			r.Fresh = true
		case "--force":
			r.Force = true
		case "--prompt":
			v, ok := value()
			if !ok {
				return Request{}, fmt.Errorf("--prompt needs a value")
			}
			r.Prompt = v
		case "--effort":
			v, ok := value()
			if !ok {
				return Request{}, fmt.Errorf("--effort needs a value")
			}
			r.Effort = v
		case "--type":
			v, ok := value()
			if !ok {
				return Request{}, fmt.Errorf("--type needs a value")
			}
			r.Type = v
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
	return 20 * time.Second
}

func (l *Launcher) upWait() time.Duration {
	if l.Up > 0 {
		return l.Up
	}
	return 30 * time.Second
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
	// the lane row's, T-722), not the configured one.
	sidKind    string
	dispatched bool
	pane       string
	entry      port.PaneInfo // the pane as pane list shows it
	panes      []port.PaneInfo
	kind       string
	env        map[string]string // the start's own env (agents.Kind.StartEnv; "" unsets the variable), set by the env line
}

// resolve is who and where (shared by Restart and Stop) with typ the launch's type ("": none); nothing is typed.
func (l *Launcher) resolve(ctx context.Context, agent, pane, typ string) (*target, error) {
	t := &target{agent: agent}
	if l.Cfg != nil {
		t.info, t.known = project.AgentLookup(l.Cfg, agent, "")
		if !t.known {
			// An agent whose pane (--pane, else its labelled one) has a lane worktree as cwd: that lane's agent.
			p := pane
			if p == "" {
				p, _ = l.PaneOf(ctx, agent, "")
			}
			if cwd := l.paneCwd(ctx, p); cwd != "" {
				if info, ok := project.AgentLookup(l.Cfg, agent, cwd); ok {
					t.info, t.known = info, true
					l.say("lane %s (pane %s cwd)", info.Lane, p)
				}
			}
		}
	}
	rec, _ := l.session(agent)
	t.sid, t.sidKind = rec.ID, rec.Kind
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
		t.kind = t.entry.Kind
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

// dispatchType puts the type a dispatch chose into t.info (T-722: a lane is a pool, the task's type picks the
// kind): typ, the launch's (its effort is the launch's own), else the type a lane agent's row's dispatch runs with its
// effort (store.LaneTypeRef: a row from before T-721 records none, the lane's configured type); neither: the
// configured type and effort stay.
func (l *Launcher) dispatchType(t *target, typ string) {
	effort := ""
	if typ == "" && t.info.Lane != "" && l.Cfg != nil {
		row, _, _ := store.ReadLaneAt(l.Cfg.Root, t.info.Lane)
		if row.Task != "" {
			typ, effort = config.ParseModelRef(row.Type)
		}
		if row.Type != "" {
			l.say("model %s (lane %s's dispatch)", row.Type, t.info.Lane)
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
// (T-301: labels and agent names are machine-wide, so a run that loaded another project's config took axis's
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
// (the identity serve records for a later rename, T-418; none for a scheduled restart of the caller's own pane).
func (l *Launcher) Launch(ctx context.Context, req Request) (port.Started, error) {
	if !config.AgentNameRE.MatchString(req.Agent) {
		return port.Started{}, project.Failf("bad agent name %q", req.Agent)
	}
	if req.Sid != "" && req.Fresh {
		return port.Started{}, project.Failf("pass --sid or --fresh, not both")
	}
	waited, err := l.waitScheduled(ctx, req.Agent, req.Pane)
	if err != nil {
		return port.Started{}, err
	}
	t, err := l.resolve(ctx, req.Agent, req.Pane, req.Type)
	if err != nil {
		return port.Started{}, err
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
	if a, err := l.H.Agent(ctx, t.agent); err == nil && a.Pane != "" && a.Pane != t.pane {
		return port.Started{}, project.Failf("pane %s holds the name %s: restart it there (obor restart %s)",
			a.Pane, t.agent, t.agent)
	}

	// The session, checked before anything is stopped (T-124): none and no --fresh → FAIL.
	// A kind that resumes nothing (agents.Caps.Resume false: OpenCode, T-581) always starts fresh: serve records its
	// session, but there is nothing obor resumes. The recorded session wins (T-577): herdr's live one for the pane is a
	// cross-check, said when it differs, and resumed only when none is recorded.
	sid, src := t.sid, "record"
	if !agents.Resumes(t.kind) {
		sid, src = "", ""
		if t.sid != "" || req.Sid != "" {
			l.say("warning: no resume for kind %s: new session", t.kind)
		}
	} else {
		if live := t.entry.Session; live != "" && !sameSid(live, sid) {
			switch {
			case sid == "":
				sid, src = live, "herdr"
			case req.Sid == "" && !req.Fresh:
				l.say("warning: herdr reports session %s for pane %s, the recorded session is %s: resuming %s", live, t.pane,
					sid, sid)
			}
		}
		if req.Sid != "" {
			sid, src = req.Sid, "--sid"
		}
		if req.Fresh {
			sid = ""
		}
		fresh := false
		// A dispatch's type of another kind than the session's (T-722: the lane's last task ran another kind): that
		// session is not this kind's to resume, so a new one.
		if was := map[string]string{"record": t.sidKind, "herdr": t.entry.Kind}[src]; sid != "" && t.dispatched &&
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
		if sid == "" && !req.Fresh && !fresh {
			return port.Started{}, project.Failf("no session to resume for %s (pass --fresh to start a new one)", t.agent)
		}
	}
	// The caller's own pane (T-264): schedule a detached run; that run waited for the agent to go idle (waitScheduled).
	if l.ownPane(t) && !waited {
		line, err := l.schedule(t, req, false)
		return port.Started{Line: line}, err
	}

	// The effort (T-695): the request's (a dispatch's type reference), else the role's (roles.<role>.type), else the
	// type's args; it applies to a resume too, so a role's session keeps the effort it was started with.
	effort := req.Effort
	if effort == "" {
		effort = t.info.Effort
	}
	args, err := l.args(t, effort)
	if err != nil {
		return port.Started{}, err
	}
	if err := l.stop(ctx, t, req.Force, false, "--force to restart anyway"); err != nil {
		return port.Started{}, err
	}
	if err := l.setEnv(ctx, t); err != nil {
		return port.Started{}, err
	}
	if sid != "" {
		l.say("resuming %s (from %s)", sid, src)
	}
	// A lane's worker restarted with no --prompt while its lane's task has a job open (T-773): that job's wake, the first
	// prompt of a fresh session, typed into a resumed one once it is up.
	job := ""
	if req.Prompt == "" && l.Job != nil && t.info.Role == "worker" && t.info.Lane != "" {
		job = l.Job(ctx, t.info.Lane)
	}
	prompt := req.Prompt
	switch {
	case sid != "":
		prompt = "" // a resume carries no first prompt
	case job != "":
		prompt = wire.WakeLine(job)
	case prompt == "":
		// A fresh start without --prompt gets its role's first prompt (T-729), e.g. the lead's /obor:lead.
		if r, ok := roles.Lookup(t.info.Role); ok {
			prompt = r.StartPrompt(t.kind)
		}
	}
	seen, err := l.start(ctx, Start{Agent: t.agent, Kind: t.kind, Pane: t.pane, Label: t.entry.Label, Args: args,
		Resume: sid, Prompt: prompt, Lane: t.info.Lane})
	var se *StartError
	switch {
	case errors.As(err, &se) && se.NotReady:
		return port.Started{}, l.giveUp(ctx, t, "start failed in %s (%s)", t.pane, se.Hint)
	case errors.As(err, &se):
		return port.Started{}, l.giveUp(ctx, t, "start failed in %s (no agent runs there: the pane is at its shell, maybe mid-command: run obor restart %s --pane %s again or start it by hand)",
			t.pane, t.agent, t.pane)
	case err != nil:
		return port.Started{}, err
	}
	switch {
	case job != "" && sid != "":
		if err := l.H.Type(ctx, t.pane, wire.WakeLine(job)); err != nil {
			l.say("warning: job %s's wake not typed into pane %s: %v (type it there: %s)", job, t.pane, err, wire.WakeLine(job))
		} else {
			l.say("job %s: its wake typed", job)
		}
	case job != "":
		l.say("job %s: its wake is the first prompt", job)
	}
	if sid == "" {
		sid = "new"
	}
	return port.Started{Line: fmt.Sprintf("RESTARTED %s (pane %s, session %s)", t.agent, t.pane, sid), Pane: t.pane,
		Terminal: t.entry.Terminal, Kind: t.kind, Session: seen.Session}, nil
}

// checkTranscript: a recorded sid must be from the start cwd (T-137: another project's run may have left it), for a
// kind whose transcripts record their cwd (agents.Kind.Transcript: Claude's projects/<cwd as a slug>). A session with
// no transcript anywhere was never prompted (Claude writes none before its first message, T-627): it is no session to
// resume, so the restart starts fresh, said; fresh is true then.
func (l *Launcher) checkTranscript(t *target, sid string) (fresh bool, err error) {
	cwd := t.info.Cwd
	if cwd == "" {
		cwd = t.entry.Cwd
	}
	k, ok := agents.Lookup(t.kind)
	switch {
	case !ok || k.Transcript(l.Env.Get, "/", sid) == "": // any cwd tells whether its transcripts record one
		l.say("recorded session %s not checked (%s keeps no transcript per cwd)", sid, t.kind)
	case cwd == "":
		l.say("warning: pane %s has no known cwd: recorded session %s not checked", t.pane, sid)
	default:
		_, err := os.Stat(k.Transcript(l.Env.Get, cwd, sid))
		if err == nil {
			return false, nil
		}
		if !os.IsNotExist(err) { // a search that failed is no proof of "never prompted" (RV-67 #4)
			return false, project.Failf("recorded session %s: cannot check its transcript under %s: %v (pass --fresh, or --sid <id>)", sid, cwd, err)
		}
		// Under any cwd ("" asks for the kind's whole transcript dir): another project's, refuse as before.
		found, err := transcriptElsewhere(k.Transcript(l.Env.Get, "", sid))
		if err != nil {
			return false, project.Failf("recorded session %s: cannot search its transcript: %v (pass --fresh, or --sid <id>)", sid, err)
		}
		if found {
			return false, project.Failf("recorded session %s is not from %s (pass --fresh, or --sid <id>)", sid, cwd)
		}
		l.say("recorded session %s has no transcript (never prompted): starting fresh", sid)
		return true, nil
	}
	return false, nil
}

// transcriptElsewhere: whether the transcript wild is "<projects>/*/<file>" names exists under any project dir. The
// projects dir is listed literally (no glob: a base dir with glob metacharacters, T-633) and a failure other than
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
	// The line echoes one of two words (T-601): the success word only when the whole line (its cd, its exports)
	// succeeded — a `; echo` after a cd that failed still printed it, and the agent started in the shell's old cwd with
	// the old env (RV-64 #3) — and its failure word when it failed, so a visible failure ends the wait at once instead
	// of at the timeout.
	okMark, badMark := envMark(), envMark()
	if err := l.H.Type(ctx, t.pane, envLine(t)+" && echo "+okMark.typed+" || echo "+badMark.typed); err != nil {
		return project.Failf("herdr pane run %s (env) failed", t.pane)
	}
	failed := false
	if !l.until(ctx, l.wait(), func() bool {
		screen, err := l.H.Screen(ctx, t.pane, 30)
		if err != nil {
			return false
		}
		switch {
		case badMark.done(screen):
			failed = true
			return true
		case okMark.done(screen):
			return true
		}
		return false
	}) {
		return l.giveUp(ctx, t, "the shell in %s did not finish the env line in %s (nothing started; the env line may have failed: check the pane)",
			t.pane, l.wait())
	}
	if failed {
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

// envLine is the shell line that sets the agent's env (and cwd) as obor setup does.
func envLine(t *target) string {
	line := "export OBOR_AGENT=" + shellQuote(t.agent)
	role := t.info.Role
	if r, ok := roles.Lookup(role); ok {
		role = r.EnvValue
	}
	if role != "" {
		line += " OBOR_ROLE=" + shellQuote(role)
	}
	if t.info.Lane != "" {
		line += " OBOR_LANE=" + shellQuote(t.info.Lane)
	} else {
		line += " && unset OBOR_LANE"
	}
	if t.known && role == "" {
		line += " OBOR_ROLE"
	}
	if t.info.Cwd != "" {
		line = "cd " + shellQuote(t.info.Cwd) + " && " + line
	}
	keys := make([]string, 0, len(t.env))
	for k := range t.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := t.env[k]; v != "" {
			line += " && export " + k + "=" + shellQuote(v)
		} else {
			line += " && unset " + k
		}
	}
	return line
}

// mark is a word the env line echoes (T-544, T-601: its success word, or its failure word when the line failed): the
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

// done: screen shows the echoed word on a line of its own with a non-empty line (the shell's next prompt) below it.
func (m mark) done(screen string) bool {
	var lines []string
	for _, ln := range strings.Split(screen, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	for i := len(lines) - 2; i >= 0; i-- {
		if lines[i] == m.echoed {
			return true
		}
	}
	return false
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// shellQuote is s as one shell word.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// args are the type's launch args (config.Model.LaunchArgs; a role without a type: the defaults of the resolved kind; a
// lead keeps Remote Control, T-280; a lane's agent starts strict, T-770) with effort in place of the type's ("": the
// type's, T-695), and t.env the start's own env (agents.Kind.StartEnv: OpenCode's model and effort) for the env line. An
// effort the start cannot carry is a Fail (nothing stopped yet).
func (l *Launcher) args(t *target, effort string) ([]string, error) {
	lt := config.Model{Kind: t.kind}
	switch {
	case t.info.Type != "":
		lt, _ = l.Cfg.Model(t.info.Type)
	case t.info.Role == "":
		if t.kind != opencode.Name { // no type, no args; still not an earlier OpenCode start's model (T-707)
			t.env = map[string]string{opencode.ModelVar: ""}
		}
		return nil, nil
	}
	if err := agents.CheckEffortArgs(lt.Kind, lt.Args, effort); err != nil {
		return nil, project.Failf("%s: %v", t.agent, err)
	}
	vars, err := lt.StartEnv(agents.Start{Effort: effort, Read: os.ReadFile, Env: l.envOrOS().Get})
	if err != nil {
		return nil, project.Failf("%s: %v", t.agent, err)
	}
	t.env = vars
	return lt.LaunchArgs(t.info.Role, effort), nil
}

// holds: pane holds agent's herdr name.
func (l *Launcher) holds(ctx context.Context, agent, pane string) bool {
	if a, err := l.H.Agent(ctx, agent); err == nil && a.Pane == pane {
		return true
	}
	a, err := l.H.Agent(ctx, pane)
	return err == nil && a.Name == agent
}

// release clears agent's name from pane (T-124, T-137): right after a stop herdr may not have noticed yet (a clear
// then failed, Codex): with wait, wait (≤ 10 s) until herdr sees no agent in the pane; then clear unless herdr
// already did, retrying once by name.
func (l *Launcher) release(ctx context.Context, agent, pane string, wait bool) {
	if wait {
		l.until(ctx, min(l.wait(), 10*time.Second), func() bool {
			_, err := l.H.Agent(ctx, pane)
			return err != nil
		})
	}
	if !l.holds(ctx, agent, pane) {
		return
	}
	if l.H.RenameAgent(ctx, pane, "") == nil {
		return
	}
	time.Sleep(l.poll())
	if l.H.RenameAgent(ctx, agent, "") == nil {
		return
	}
	l.say("warning: herdr kept the name %s in pane %s", agent, pane)
}

func (l *Launcher) kill(pgid int, sig syscall.Signal) error {
	if l.Kill != nil {
		return l.Kill(pgid, sig)
	}
	return syscall.Kill(-pgid, sig)
}
