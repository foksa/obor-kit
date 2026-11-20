package launch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/project"
	"obor/internal/store"
)

// StartError is the core's type (port.StartError): serve reads a failed start's NotReady and Trust.
type StartError = port.StartError

// Start is one agent start in its pane (Launch's): the agent's env and cwd are the pane shell's
// already (or Env types them), its argv the one its buildStart built.
type Start struct {
	Agent, Kind, Pane string
	Label             string   // the pane's label now: renamed to Agent when it differs
	Args              []string // the argv after the binary (startBuilt.Args): started as it is
	Resume            string   // the session id to resume; "" for a new one
	// Session is the session id obor chose for a fresh start (startBuilt.Session; Args name it): recorded for the agent
	// before it runs; "" for none.
	Session string
	Prompt  string // the CLI's first prompt (a job's wake), in Args already; "" for none
	// Again is the command that starts this agent again as it was started (port.Launch.Again: a drive's
	// `obor drive <T-xx>`, which keeps its prompt); "": `obor restart <Agent>`. A hint that names the start again uses it.
	Again string
	// Fresh is the request's intent (port.Launch.Fresh): a new session, whatever is recorded. The retry a failed start
	// names (retry) is a --fresh restart then; a resume's is one only when no session is recorded.
	Fresh bool
	Lane  string // the agent's lane: a session a fresh start reports is recorded with it
	// Shell bounds the wait for the pane's shell (Await) and the retries of a start the terminal refused as PaneBusy:
	// one bound for both, from the start's beginning; 0: neither, a pane the caller knows is its shell.
	Shell time.Duration
	// Await: the start waits for the pane's shell before its env line and start (a pane a new tab or split just made,
	// a pane whose agent was just stopped); past Shell, a StartError with NoShell and nothing typed. false: a pane not at
	// its shell is the env line's to refuse (a person's program in a pane that ran no agent).
	Await bool
	// Env types the agent's env line at the pane's shell once it is up, before the start (nil: the caller set it).
	Env func(ctx context.Context) error
	// Whose is whose env and cwd that line sets (the AgentEnv the caller types it from): the terminal's AgentStart
	// carries them as its Env and Cwd. Zero: none given (a caller that set the env itself).
	Whose AgentEnv
}

// Start is the one start of an agent in its pane, Launch's (a restart, serve's starts, a restore, obor setup's
// --start) and the live conformance's probe of it: the pane's shell waited for (s.Await, within s.Shell), the env line
// (s.Env), the pane labelled, the launch line printed (a wrong model or permission mode is visible before the
// agent starts; s.Args as its buildStart built them: no kind is asked again), the session obor chose recorded
// (s.Session), then the terminal's agent start (port.AgentStarter, asked of the PaneControl): a PaneBusy answer
// retried once the pane is its shell again, within s.Shell. The terminal's start returns once its agent runs in the
// pane, within Up; its own lines (a name it kept and cleared, a start it counts though its answer failed) are said
// through Diag (else Out). A live agent in another pane that took the name after the start's decision (Locate) is a
// port.Refused, Locate's line. The pane label put back over another label is said. A failed start is a *StartError,
// the terminal's error printed: NoShell when the pane never became its shell, NotReady with a hint when the pane shows
// a trust dialog (Trust too when the terminal accepted a fresh start: the agent runs and holds its first prompt, in
// its argv; the hint is agents.TrustHint) or a Codex self-update, or the terminal says NotReady. A failed env line is
// its own error; a failed pane label is a Fail (nothing started); a terminal with no AgentStarter is a Fail (nothing
// started). A start reports the confirmed start (serve ends the agent's skew entry) and, for a kind whose sessions
// its transcript names, its session (done).
func (l *Launcher) Start(ctx context.Context, s Start) error {
	_, err := l.start(ctx, s)
	return err
}

// start is Start; it returns the pane the terminal's start ran the agent in, as the terminal showed it then (none when
// it showed none). A pane id other than s.Pane is where the agent runs: the rest of the start is done there.
func (l *Launcher) start(ctx context.Context, s Start) (port.Pane, error) {
	by := time.Now().Add(s.Shell)
	if s.Await && !l.atShell(ctx, s.Pane) {
		l.say("waiting for the shell of pane %s (≤ %s)", s.Pane, s.Shell)
		if !l.waitShell(ctx, s.Pane, by) {
			return port.Pane{}, &StartError{NoShell: true, Err: fmt.Errorf("pane %s is not a shell after %s", s.Pane, s.Shell)}
		}
	}
	if s.Env != nil {
		if err := s.Env(ctx); err != nil {
			return port.Pane{}, err
		}
	}
	if s.Label != s.Agent {
		if err := l.H.RenamePane(ctx, s.Pane, s.Agent); err != nil {
			return port.Pane{}, project.Failf("terminal pane rename %s %s failed", s.Pane, s.Agent)
		}
		if s.Label != "" { // a restart in place in a pane a person relabelled
			l.say("label of pane %s put back: %s (it was %s)", s.Pane, s.Agent, s.Label)
		} else {
			l.say("labelled pane %s %s", s.Pane, s.Agent)
		}
	}
	if _, ok := l.Cfg.Kind(s.Kind); !ok { // dispatch and obor config validate refuse such a model's kind
		return port.Pane{}, project.Failf("%s: kind %s has no bridge (obor config validate)", s.Agent, s.Kind)
	}
	starter, ok := l.H.(port.AgentStarter)
	if !ok {
		return port.Pane{}, project.Failf("%s: the terminal starts no agent in pane %s (no agent start of its own)", s.Agent, s.Pane)
	}
	// A quarantined install that reaches the start is the lead's or a drive's (Launch refuses every other one): the
	// owner is at its screen, so it warns — the Gatekeeper dialog can be answered there.
	if why := l.Cfg.Kinds().StartRefusal(s.Kind, l.installHost()); why != "" {
		l.say("warning: " + why)
	}
	// The session obor chose for a kind whose sessions it names (startBuilt.Session): recorded before the agent runs.
	if s.Session != "" {
		l.recordSession(ctx, s)
	}
	shown := strings.Join(s.Args, " ")
	if shown == "" {
		shown = "none"
	}
	l.say("args (%s): %s", s.Kind, shown)
	as := l.agentStart(s)
	a, err := starter.Start(ctx, as)
	said := false
	for s.Shell > 0 && port.PaneBusy(err) {
		if !said {
			l.say("pane %s is not an available shell yet: waiting for it", s.Pane)
			said = true
		}
		if !l.waitShell(ctx, s.Pane, by) {
			break
		}
		a, err = starter.Start(ctx, as)
	}
	if err == nil {
		if a.ID != "" {
			s.Pane = a.ID
		}
		return l.done(ctx, s, a), nil
	}
	// A start that lost a race (a live agent in another pane took the name after Locate) is refused for good with
	// Locate's line: the holder, its proof and the command that works. The holder's label is read here: the terminal
	// reports the pane, never a label.
	if pane, held := port.NameHeld(err); held {
		label := ""
		if p, err := l.H.Pane(ctx, pane); err == nil {
			label = p.Label
		}
		return port.Pane{}, port.Refused{Line: runsLine(s.Agent, pane, proofName, label)}
	}
	l.say("%v", err)
	// Only a failed start looks at the screen (a started agent's prompt delivery is judged by the terminal alone).
	screen, _ := l.H.Screen(ctx, s.Pane, 60)
	if agents.TrustDialog(screen) {
		// Trust only for a fresh start the terminal accepted: a refused one ran no agent, and a resumed one carries no
		// first prompt in its argv (its wake is typed once it is up), so the dialog holds nothing it would deliver.
		return port.Pane{}, &StartError{Err: err, NotReady: true, Trust: port.Accepted(err) && s.Resume == "",
			Hint: agents.TrustHint(s.Kind, s.Agent, s.Pane)}
	}
	k, _ := l.Cfg.Kind(s.Kind)
	if hint, retry := notReady(screen, k, s.Pane, l.retry(s.Agent, s.Pane, s)); hint != "" {
		return port.Pane{}, &StartError{Err: err, NotReady: true, Hint: hint, Retry: retry}
	}
	if port.NotReady(err) {
		// Started, but waiting at a startup dialog the screen does not name (live 2026-10-13): the agent runs.
		return port.Pane{}, &StartError{Err: err, NotReady: true, Hint: s.Agent + " waits at a startup dialog in pane " + s.Pane + ": answer it there"}
	}
	return port.Pane{}, &StartError{Err: err}
}

// agentStart is s as the terminal's agent start takes it: its pane, the agent's name, kind, the kind's program (of
// l.Cfg, the pass's config; "" when it names no such kind), argv and first prompt,
// the env and cwd its env line set (s.Whose), Up and Poll, and the terminal's lines through Diag (else Out).
func (l *Launcher) agentStart(s Start) port.AgentStart {
	say := l.say
	if l.Diag != nil {
		say = func(format string, args ...any) { fmt.Fprintf(l.Diag, format+"\n", args...) }
	}
	var env []string
	if s.Whose.Agent != "" {
		set, _ := l.Cfg.Kinds().StartVars(roleEnv(s.Whose.Role), s.Whose.Lane, s.Pane, s.Whose.Vars)
		for _, v := range set {
			env = append(env, v.Name+"="+v.Value)
		}
	}
	program := ""
	if k, ok := l.Cfg.Kind(s.Kind); ok {
		program = agents.Programs(k)[0]
	}
	return port.AgentStart{Pane: s.Pane, Label: s.Agent, Kind: s.Kind, Program: program, Argv: s.Args, Env: env,
		Cwd: s.Whose.Cwd, Prompt: s.Prompt, Bound: l.upWait(), Poll: l.poll(), Say: say}
}

// done is a start's success path, the one for Start and restart: it records the start (recordStart: the restore's
// proof that obor started the agent in the pane), reports the confirmed start (serve ends the agent's skew entry),
// then, for a start of a kind whose sessions its transcript names, the session the agent runs (report): a resume
// reports the session it resumed, a fresh start searches for its own (searchRollout: the transcript its process holds
// open).
func (l *Launcher) done(ctx context.Context, s Start, a port.Pane) port.Pane {
	l.recordStart(ctx, s, s.Pane)
	l.reportStarted(ctx, s)
	if k, ok := l.Cfg.Kind(s.Kind); !ok || !k.Caps().Sessions {
		return a
	}
	if s.Resume != "" {
		l.reportResume(ctx, s)
		return a
	}
	l.searchRollout(ctx, s.Agent, s.Pane, s.Kind, s.Lane, s.Prompt != "")
	return a
}

// reportStarted reports the confirmed start of s.Agent to serve: the process that runs now is this start's, whatever
// session it reports (a fresh Codex start with no prompt has none before its first prompt). It is one request, not
// retried: serve's entry it ends is serve's memory, and a serve that is not listening is no warning; any other failure
// is one line.
func (l *Launcher) reportStarted(ctx context.Context, s Start) {
	var err error
	if l.Started != nil {
		err = l.Started(ctx, s.Agent)
	} else if l.Cfg != nil {
		ctx, cancel := context.WithTimeout(ctx, reportWait)
		defer cancel()
		err = wire.Notify(ctx, wire.SocketPath(l.Cfg.Root), wire.StartedRequest(l.Cfg.Root, s.Agent))
	}
	if err != nil && !wire.Absent(err) {
		l.say("warning: start of %s not reported: %v (serve may still name its last process's release)", s.Agent, err)
	}
}

// session is agent's current recorded session (Sessions, else serve's store).
func (l *Launcher) session(agent string) (store.Session, bool) {
	switch {
	case l.Sessions != nil:
		return l.Sessions(agent)
	case l.Cfg != nil:
		return store.CurrentAt(l.Cfg.Root, agent)
	}
	return store.Session{}, false
}

// hasSession is whether a session is recorded for agent.
func (l *Launcher) hasSession(agent string) bool {
	_, ok := l.session(agent)
	return ok
}

// retry is the command that starts agent again in pane, for every hint of a failed start s (generic, busy, no shell, a
// Codex self-update): s.Again when the start has one (a drive's), else a restart on its pane, --fresh when the request
// was fresh (s.Fresh) or no session is recorded to resume.
func (l *Launcher) retry(agent, pane string, s Start) string {
	return project.RestartCmd(s.Again, agent, s.Fresh || !l.hasSession(agent), pane)
}

// sameSid: herdr's session value live (an id, or its transcript's path) names session sid.
func sameSid(live, sid string) bool {
	return live == sid || sid != "" && strings.TrimSuffix(filepath.Base(live), filepath.Ext(live)) == sid
}

// notReady is why the agent cannot take a prompt yet, from its pane's screen after the start, a screen with no trust
// dialog (start's own case): what the kind k sees on it (agents.Kind.NotReady: Codex's self-update); "" when none
// shows (or k is nil, no kind). The hint names again, the command that starts the agent again (retry).
func notReady(screen string, k agents.Kind, pane, again string) (hint string, retry bool) {
	if k != nil {
		if why := k.NotReady(screen); why != "" {
			return k.Name() + " " + why + " in pane " + pane + ": start it again with " + again, true
		}
	}
	return "", false
}
