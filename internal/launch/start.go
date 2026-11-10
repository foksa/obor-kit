package launch

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/project"
	"obor/internal/store"
)

// Start, StartError and AgentEnv are the core's types (port.Starter), so setup starts agents without importing launch.
type (
	Start      = port.Start
	StartError = port.StartError
)

// Start is the one start of an agent in its pane, for obor setup (port.Starter), a restart and serve's starts
// (Launch), and restore's restart: the pane's shell waited for (s.Await, within s.Shell), the env line
// (s.Env), the pane labelled, the launch line printed (a wrong model or permission mode is visible before the
// agent starts; s.Args as its Build built them: no kind is asked again), the session obor chose recorded
// (s.Session), then herdr agent start: agent_pane_busy retried once the pane is its shell again, within s.Shell
// agent_name_taken: the stale name cleared and retried once, unless a live agent elsewhere holds it.
// A start ends as soon as its agent runs in the pane (started), never at a fixed wait (herdr's own answer waits
// up to 30 s for an agent ready for input, which an agent busy on its first prompt never is); a start with a first
// prompt whose agent works on it is a start, its launch pending or herdr's call given up on (attempt.counts,
// busyStart). herdr's agent name is neither required nor renamed back (herdr drops it at times and obor
// addresses the pane by id). A failed start is a *StartError, herdr's error printed: NoShell when the pane never
// became its shell, NotReady with a hint when the pane shows a trust dialog or a Codex self-update, or herdr says
// agent_not_ready. A failed env line is its own error; a failed pane label is a Fail (nothing started). A start reports
// the confirmed start (serve ends the agent's skew entry) and, for a kind whose sessions its transcript names, its
// session (done).
func (l *Launcher) Start(ctx context.Context, s Start) error {
	_, err := l.start(ctx, s)
	return err
}

// start is Start; it returns the agent the start ran in s.Pane as herdr showed it (none when herdr named no agent).
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
			return port.Pane{}, project.Failf("herdr pane rename %s %s failed", s.Pane, s.Agent)
		}
		l.say("labelled pane %s %s", s.Pane, s.Agent)
	}
	if _, ok := agents.Lookup(s.Kind); !ok { // dispatch and obor config validate refuse such a model's kind
		return port.Pane{}, project.Failf("%s: kind %s has no bridge (obor config validate)", s.Agent, s.Kind)
	}
	// A start the owner makes at a screen (obor lead, obor setup role): a quarantined install warns, never refuses
	// — the Gatekeeper dialog can be answered there.
	if why := agents.StartRefusal(s.Kind, l.installHost()); why != "" {
		l.say("warning: " + why)
	}
	// The session obor chose for a kind whose sessions it names (Built.Session): recorded before the agent runs.
	if s.Session != "" {
		l.recordSession(ctx, s)
	}
	shown := strings.Join(s.Args, " ")
	if shown == "" {
		shown = "none"
	}
	l.say("args (%s): %s", s.Kind, shown)
	t := l.started(ctx, s)
	said := false
	for s.Shell > 0 && port.ErrorCode(t.err) == "agent_pane_busy" {
		if !said {
			l.say("pane %s is not an available shell yet (herdr: agent_pane_busy): waiting for it", s.Pane)
			said = true
		}
		if !l.waitShell(ctx, s.Pane, by) {
			break
		}
		t = l.started(ctx, s)
	}
	if port.ErrorCode(t.err) == "agent_name_taken" {
		holder := ""
		if a, gerr := l.H.Agent(ctx, s.Agent); gerr == nil {
			holder = a.ID
		}
		if holder == "" || holder == s.Pane {
			at := ""
			if holder != "" {
				at = " (pane " + holder + ")"
			}
			l.say("agent start: name %s taken%s: clearing it and retrying", s.Agent, at)
			// herdr lags in noticing an exit: the name may still belong to a pane that ran the agent before.
			if cleared := holder != "" && l.H.RenameAgent(ctx, holder, "") == nil; !cleared &&
				l.H.RenameAgent(ctx, s.Agent, "") != nil {
				l.say("warning: herdr kept the name %s", s.Agent)
			}
			t = l.started(ctx, s)
		}
	}
	if t.err == nil {
		return l.done(ctx, s, t.a), nil
	}
	err := t.err
	l.say("%v", err)
	if s.Prompt != "" && port.ErrorCode(err) != "agent_not_ready" {
		if a, ok := l.busyStart(ctx, s, t); ok {
			return l.done(ctx, s, a), nil
		}
	}
	// Only a failed start looks at the screen (a started agent's prompt delivery is judged by herdr alone).
	screen, _ := l.H.Screen(ctx, s.Pane, 60)
	if hint := notReady(screen, s.Kind, s.Agent, s.Pane); hint != "" {
		return port.Pane{}, &StartError{Err: err, NotReady: true, Hint: hint}
	}
	if port.ErrorCode(err) == "agent_not_ready" {
		// Started, but waiting at a startup dialog the screen does not name (live 2026-10-13): the agent runs.
		return port.Pane{}, &StartError{Err: err, NotReady: true, Hint: s.Agent + " waits at a startup dialog in pane " + s.Pane + ": answer it there"}
	}
	return port.Pane{}, &StartError{Err: err}
}

// attempt is one agent start as started saw it: the agent it ended with, the terminal's error, and whether the
// terminal accepted it: it answered a start it made (success, a pending launch included, or one it gave up
// waiting on: timeout, agent_not_ready), or, before its answer, listed the record it resets the pane's agent to when
// it accepts a start (launching); never a refusal (agent_pane_busy, agent_name_taken, pane_not_available).
type attempt struct {
	a        port.Pane
	err      error
	accepted bool
}

// counts is the evidence rule: a listing a is the start's own agent only when it was read
// after the terminal accepted this attempt, is up, and carries the terminal's word that the launch is over
// (a.Launched, herdr's launch_pending gone or interactive_ready) or, for a start with a first prompt, works: herdr
// keeps such a launch pending while its agent works on the prompt, until its own ~30 s bound (live H-L14: claude and
// codex working with launch_pending from ~4 s), and an agent that exited, or the old agent's stale idle, is never
// working. A listing alone, kind and status, is never the proof: before the acceptance herdr may list the old agent,
// and a launch whose CLI exits leaves no agent at all.
func (t attempt) counts(a port.Pane, s Start) bool {
	return t.accepted && up(a, s) && (a.Launched || s.Prompt != "" && a.Status == "working")
}

// launching: a is the record herdr resets s.Pane's agent to when it accepts a start, which no listing of an agent
// that ran there shows: the launch pending with no status yet (live H-L12, H-L14: none/unknown, then the kind with
// unknown, from the call's acceptance until the agent shows a status). Read after the start's call began and before
// its answer, it is the acceptance. An old agent's stale listing carries a status (idle, blocked, working).
func launching(a port.Pane, s Start) bool {
	return a.ID == s.Pane && (a.Agent == "" || a.Agent == s.Kind) && !a.Launched && a.Status == "unknown"
}

// started runs the terminal's agent start once and returns once the agent runs in s.Pane (the start's done
// condition), or the terminal's answer: its error, or the agent it detected. A pending answer is no proof: herdr
// answers before the agent runs (launch_pending: a command cut off at the shell left no agent, and the start said
// RESTARTED), so a poll reads s.Pane until a listing counts by the evidence rule (attempt.counts), and none within Up is
// an error. The poll runs alongside the call, from its start (herdr's answer may come after an agent
// busy on its first prompt is up); until the answer, only the terminal's reset record (launching) accepts the start,
// never a listing of an agent. started owns that call: it runs under Up, and before started returns it is
// cancelled and joined, its late answer dropped.
func (l *Launcher) started(ctx context.Context, s Start) attempt {
	var t attempt
	type answer struct {
		a   port.Pane
		err error
	}
	call, cancel := context.WithCancel(ctx)
	res := make(chan answer, 1)
	go func() {
		a, err := l.H.Start(call, s.Agent, s.Kind, s.Pane, s.Args...)
		res <- answer{a, err}
	}()
	answered := false
	end := func() {
		cancel()
		if !answered {
			<-res
		}
	}
	tick := time.NewTicker(l.poll())
	defer tick.Stop()
	bound := time.NewTimer(l.upWait())
	defer bound.Stop()
	for {
		select {
		case <-ctx.Done():
			end()
			t.err = ctx.Err()
			return t
		case r := <-res:
			answered = true
			switch port.ErrorCode(r.err) {
			case "":
				t.accepted = t.accepted || r.err == nil
			case "timeout", "agent_not_ready":
				t.accepted = true
			case "agent_pane_busy", "agent_name_taken", "pane_not_available":
				t.accepted = false // refused: what the poll took for its launch was not this start's
			}
			if r.err != nil || detected(r.a, s) {
				cancel()
				t.a, t.err = r.a, r.err
				return t
			}
		case <-bound.C:
			end()
			t.err = fmt.Errorf("herdr agent start %s: no %s agent came up in %s within %s", s.Agent, s.Kind, s.Pane, l.upWait())
			return t
		case <-tick.C:
			a, err := l.H.Agent(ctx, s.Pane)
			if err != nil {
				continue
			}
			if !answered && launching(a, s) {
				t.accepted = true
			}
			if t.counts(a, s) {
				end()
				t.a = a
				return t
			}
		}
	}
}

// detected: the terminal's answer a to s's start shows the agent running: s.Kind in s.Pane, launched (not pending),
// with a status.
func detected(a port.Pane, s Start) bool {
	return a.ID == s.Pane && a.Agent == s.Kind && a.Launched && a.Status != "" && a.Status != "unknown"
}

// up: a is an agent of s's kind in its pane, ready for obor: idle or done, or working on its first prompt.
// Whether it is the start's own agent is the evidence rule's (attempt.counts). herdr's name is no part of it.
func up(a port.Pane, s Start) bool {
	switch {
	case a.ID != s.Pane || a.Agent != s.Kind:
		return false
	case a.Busy() && s.Prompt == "":
		return false
	}
	return a.Ready() || a.Status == "working"
}

// busyStart: a start that carried a first prompt failed, yet the agent of s.Kind runs in s.Pane and took it (working,
// or idle/done after a short turn): started. herdr's start succeeds only once the agent is ready for input within its
// 30 s bound, and an agent that works on its first prompt from the outset is not (live: Sonnet and Codex
// workers busy on their wake at 30 s); herdr then keeps no name for it. The listing counts by the evidence rule of the
// last attempt t (attempt.counts): a start herdr refused while the old agent was still listed (agent_pane_busy)
// is no start, however ready that old agent looks; one herdr accepted and gave up on is, once herdr lists its
// agent launched, or working on the prompt.
func (l *Launcher) busyStart(ctx context.Context, s Start, t attempt) (port.Pane, bool) {
	a, err := l.H.Agent(ctx, s.Pane)
	if err != nil || !t.counts(a, s) {
		return a, false
	}
	l.say("%s runs in %s (%s on its first prompt): started", s.Agent, s.Pane, a.Status)
	return a, true
}

// done is a start's success path, the one for Start and restart: it reports the confirmed start (serve ends the agent's
// skew entry), then, for a start of a kind whose sessions its transcript names, the session the agent runs (report):
// a resume reports the session it resumed, a fresh start searches for its own (searchRollout: the transcript its process
// holds open).
func (l *Launcher) done(ctx context.Context, s Start, a port.Pane) port.Pane {
	l.reportStarted(ctx, s)
	if k, ok := agents.Lookup(s.Kind); !ok || !k.Caps().Sessions {
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

// sameSid: herdr's session value live (an id, or its transcript's path) names session sid.
func sameSid(live, sid string) bool {
	return live == sid || sid != "" && strings.TrimSuffix(filepath.Base(live), filepath.Ext(live)) == sid
}

var trustRE = regexp.MustCompile(`(?i)trust (this|the) (folder|directory)|do you trust`)

// notReady is why the agent cannot take a prompt yet, from its pane's screen after the start: every kind's trust
// dialog, else what the kind sees on it (agents.Kind.NotReady: Codex's self-update); "" when none shows.
func notReady(screen, kind, name, pane string) string {
	if trustRE.MatchString(screen) {
		return "a 'Trust this folder?' dialog in pane " + pane + ": answer it there (Enter = trust; once per repo)"
	}
	if k, ok := agents.Lookup(kind); ok {
		if why := k.NotReady(screen); why != "" {
			return kind + " " + why + " in pane " + pane + ": run herdr agent start " + name + " --kind " + kind +
				" --pane " + pane + " again"
		}
	}
	return ""
}
