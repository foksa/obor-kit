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
	"obor/internal/project"
	"obor/internal/store"
)

// Start, StartError and AgentEnv are the core's types (port.Starter), so setup starts agents without importing launch.
type (
	Start      = port.Start
	StartError = port.StartError
)

// Start labels the pane, prints the launch line (T-145: a wrong model or permission mode is visible before the agent
// starts; s.Args as its Build built them, T-1042: no kind is asked again), records the session obor chose for it
// (s.Session) and runs herdr agent start (agent_name_taken: the stale name cleared and retried once, unless a live agent
// elsewhere holds it). A start with a first prompt that herdr gave up on while the agent works on it is a start
// (busyStart); a start ends as soon as its agent runs in the pane (started), never at a fixed wait (T-404: herdr's own
// answer waits up to 30 s for an agent ready for input, which an agent busy on its first prompt never is). herdr's
// agent name is neither required nor renamed back (T-952: herdr drops it at times and obor addresses the pane by id).
// A failed start is a *StartError, herdr's error printed: NotReady with a hint when the pane shows a trust dialog or a
// Codex self-update, or herdr says agent_not_ready. A failed pane label is a Fail (nothing started). A started fresh
// start of a kind that reports its session records it (done).
func (l *Launcher) Start(ctx context.Context, s Start) error {
	_, err := l.start(ctx, s, time.Time{})
	return err
}

// start is Start; it returns the agent the start ran in s.Pane as herdr showed it (none when herdr named no agent).
// shellBy (a restart's, T-1053; zero: none) bounds a start herdr answers agent_pane_busy for: the start waits for the
// pane's shell and tries again until then, so a pane still reaping the old agent's exit is no failed start.
func (l *Launcher) start(ctx context.Context, s Start, shellBy time.Time) (port.Pane, error) {
	if s.Label != s.Agent {
		if err := l.H.RenamePane(ctx, s.Pane, s.Agent); err != nil {
			return port.Pane{}, project.Failf("herdr pane rename %s %s failed", s.Pane, s.Agent)
		}
		l.say("labelled pane %s %s", s.Pane, s.Agent)
	}
	if _, ok := agents.Lookup(s.Kind); !ok { // dispatch and obor config validate refuse such a model's kind (DD-v0.13 D1)
		return port.Pane{}, project.Failf("%s: kind %s has no bridge (obor config validate)", s.Agent, s.Kind)
	}
	// A start the owner makes at a screen (obor lead, obor setup role): a quarantined install warns, never refuses (T-1052)
	// — the Gatekeeper dialog can be answered there.
	if why := agents.StartRefusal(s.Kind, l.installHost()); why != "" {
		l.say("warning: " + why)
	}
	// The session obor chose for a kind whose sessions it names (Built.Session, T-813): recorded before the agent runs.
	if s.Session != "" {
		l.recordSession(ctx, s)
	}
	shown := strings.Join(s.Args, " ")
	if shown == "" {
		shown = "none"
	}
	l.say("args (%s): %s", s.Kind, shown)
	// What the pane ran before the start: an agent already there is never taken for the new one, whatever herdr answers.
	pre, _ := l.H.Agent(ctx, s.Pane)
	a, err := l.started(ctx, s, pre)
	said := false
	for !shellBy.IsZero() && port.ErrorCode(err) == "agent_pane_busy" {
		if !said {
			l.say("pane %s is not an available shell yet (herdr: agent_pane_busy): waiting for it", s.Pane)
			said = true
		}
		if !l.waitShell(ctx, s.Pane, shellBy) {
			break
		}
		a, err = l.started(ctx, s, pre)
	}
	if port.ErrorCode(err) == "agent_name_taken" {
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
			a, err = l.started(ctx, s, pre)
		}
	}
	if err == nil {
		return l.done(ctx, s, a), nil
	}
	l.say("%v", err)
	if s.Prompt != "" && port.ErrorCode(err) != "agent_not_ready" {
		if a, ok := l.busyStart(ctx, s, pre); ok {
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

// started runs herdr's agent start and returns once the agent runs in s.Pane (the start's done condition, T-404), or
// herdr's answer: its error, or the agent it detected. pre is what the pane ran before the start. A pending answer is
// no proof: herdr answers before the agent runs (launch_pending, T-544: a command cut off at the shell left no agent,
// and the start said RESTARTED), so it keeps the poll going, and no agent up within Up is an error. The call runs
// alongside the poll (T-387: an agent busy on its first prompt is up although herdr may give up on it). started owns
// that call (T-405): before it returns, the call is cancelled and joined, and its late answer is dropped.
func (l *Launcher) started(ctx context.Context, s Start, pre port.Pane) (port.Pane, error) {
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
			return port.Pane{}, ctx.Err()
		case r := <-res:
			answered = true
			if r.err != nil || detected(r.a, s) {
				cancel()
				return r.a, r.err
			}
		case <-bound.C:
			end()
			return port.Pane{}, fmt.Errorf("herdr agent start %s: no %s agent came up in %s within %s", s.Agent, s.Kind, s.Pane, l.upWait())
		case <-tick.C:
			if a, err := l.H.Agent(ctx, s.Pane); err == nil && up(pre, a, s) {
				end()
				return a, nil
			}
		}
	}
}

// detected: herdr's answer a to s's start shows the agent running: s.Kind in s.Pane with a status (a pending launch
// has no kind and status unknown).
func detected(a port.Pane, s Start) bool {
	return a.ID == s.Pane && a.Agent == s.Kind && a.Status != "" && a.Status != "unknown"
}

// up: a is the agent s's start put in its pane, ready for obor: its kind, and idle or done, or working on its first
// prompt (T-387); in a pane that ran no agent of the kind before the start (an old agent herdr lost is never taken for
// the new one). herdr's name is no part of it (T-952).
func up(pre, a port.Pane, s Start) bool {
	switch {
	case a.ID != s.Pane || a.Agent != s.Kind:
		return false
	case a.Busy() && s.Prompt == "":
		return false
	case !a.Ready() && a.Status != "working":
		return false
	}
	return pre.Agent != s.Kind
}

// busyStart: a start that carried a first prompt failed, yet the agent of s.Kind runs in s.Pane and took it (working,
// or idle/done after a short turn): started. herdr's start succeeds only once the agent is ready for input within its
// 30 s bound, and an agent that works on its first prompt from the outset is not (T-387, live: Sonnet and Codex
// workers busy on their wake at 30 s); herdr then keeps no name for it. The agent the pane ran before the start (pre)
// is not the new one (up): a start herdr refused while the old agent was still listed (agent_pane_busy, T-1053) is no
// start, however ready that old agent looks.
func (l *Launcher) busyStart(ctx context.Context, s Start, pre port.Pane) (port.Pane, bool) {
	a, err := l.H.Agent(ctx, s.Pane)
	if err != nil || !up(pre, a, s) {
		return a, false
	}
	l.say("%s runs in %s (%s on its first prompt): started", s.Agent, s.Pane, a.Status)
	return a, true
}

// done is a start's success path, the one for Start and restart (T-582): a fresh start of a kind that reports its
// session searches for it (searchRollout: the transcript its process holds open, T-588; a resume reports none).
func (l *Launcher) done(ctx context.Context, s Start, a port.Pane) port.Pane {
	if k, ok := agents.Lookup(s.Kind); s.Resume != "" || !ok || !k.Caps().Sessions {
		return a
	}
	l.searchRollout(ctx, s.Agent, s.Pane, s.Kind, s.Lane, s.Prompt != "")
	return a
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
