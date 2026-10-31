package launch

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app"
	"obor/internal/project"
	"obor/internal/store"
)

// Start, StartError and AgentEnv are the core's types (app.Starter), so setup starts agents without importing launch.
type (
	Start      = app.Start
	StartError = app.StartError
)

// Start labels the pane, clears the agent's herdr name from it if herdr still holds it there, prints the launch line
// (T-145: a wrong model or permission mode is visible before the agent starts) and runs herdr agent start
// (agent_name_taken: the name cleared and retried once, unless a live agent elsewhere holds it). A start with a first
// prompt that herdr gave up on while the agent works on it is a start (busyStart); a start ends with the agent named
// (named: renamed when herdr has no name for it; a refused rename is a warning), and it ends as soon as its agent runs in the pane and is
// named (started), never at a fixed wait (T-404: herdr's own answer waits up to 30 s for an agent ready for input,
// which an agent busy on its first prompt never is). A failed start is a *StartError, herdr's error printed: NotReady
// with a hint when the pane shows a trust dialog or a Codex self-update, or herdr says agent_not_ready. A failed
// pane label is a Fail (nothing started). A started fresh start of a kind that reports its session records it (done).
func (l *Launcher) Start(ctx context.Context, s Start) error {
	_, err := l.start(ctx, s)
	return err
}

// start is Start; it returns the agent the start ran in s.Pane as herdr showed it (its session id the identity a later
// rename compares, T-418; none when herdr named no pane).
func (l *Launcher) start(ctx context.Context, s Start) (app.AgentInfo, error) {
	if s.Label != s.Agent {
		if err := l.H.RenamePane(ctx, s.Pane, s.Agent); err != nil {
			return app.AgentInfo{}, project.Failf("herdr pane rename %s %s failed", s.Pane, s.Agent)
		}
		l.say("labelled pane %s %s", s.Pane, s.Agent)
	}
	if l.holds(ctx, s.Agent, s.Pane) {
		l.release(ctx, s.Agent, s.Pane, true)
	}
	k, ok := agents.Lookup(s.Kind)
	if !ok { // dispatch and obor config validate refuse such a lane type (DD-v0.13 D1)
		return app.AgentInfo{}, project.Failf("%s: kind %s has no bridge (obor config validate)", s.Agent, s.Kind)
	}
	kit := l.Kit
	if kit == "" {
		kit = project.KitRoot(l.Env)
	}
	s.Args = startArgs(k, s, kit)
	shown := strings.Join(s.Args, " ")
	if shown == "" {
		shown = "none"
	}
	l.say("args (%s): %s", s.Kind, shown)
	a, err := l.started(ctx, s)
	if app.ErrorCode(err) == "agent_name_taken" {
		holder := ""
		if a, gerr := l.H.Agent(ctx, s.Agent); gerr == nil {
			holder = a.Pane
		}
		if holder == "" || holder == s.Pane {
			at := ""
			if holder != "" {
				at = " (pane " + holder + ")"
			}
			l.say("agent start: name %s taken%s: clearing it and retrying", s.Agent, at)
			if holder != "" {
				l.release(ctx, s.Agent, holder, false)
			} else {
				_ = l.H.RenameAgent(ctx, s.Agent, "")
			}
			a, err = l.started(ctx, s)
		}
	}
	if err == nil {
		return l.done(ctx, s, a)
	}
	l.say("%v", err)
	if s.Prompt != "" && app.ErrorCode(err) != "agent_not_ready" {
		if a, ok := l.busyStart(ctx, s); ok {
			return l.done(ctx, s, a)
		}
	}
	// Only a failed start looks at the screen (a started agent's prompt delivery is judged by herdr alone).
	screen, _ := l.H.Screen(ctx, s.Pane, 60)
	if hint := notReady(screen, s.Kind, s.Agent, s.Pane); hint != "" {
		return app.AgentInfo{}, &StartError{Err: err, NotReady: true, Hint: hint}
	}
	if app.ErrorCode(err) == "agent_not_ready" {
		// Started, but waiting at a startup dialog the screen does not name (live 2026-10-13): the agent runs.
		return app.AgentInfo{}, &StartError{Err: err, NotReady: true, Hint: s.Agent + " waits at a startup dialog in pane " + s.Pane + ": answer it there"}
	}
	return app.AgentInfo{}, &StartError{Err: err}
}

// started runs herdr's agent start and returns once the agent runs in s.Pane and is named (the start's done condition,
// T-404), or herdr's answer: its error, or the agent it detected. A pending answer is no proof: herdr answers before
// the agent runs (launch_pending, T-544: a command cut off at the shell left no agent, and the start said RESTARTED),
// so it keeps the poll going, and no agent up within Up is an error. The call runs alongside the poll (T-387: an
// agent busy on its first prompt is up although herdr may give up on it). started owns that call (T-405): before it
// returns, the call is cancelled and joined, and its late answer is dropped.
func (l *Launcher) started(ctx context.Context, s Start) (app.AgentInfo, error) {
	type answer struct {
		a   app.AgentInfo
		err error
	}
	pre, _ := l.H.Agent(ctx, s.Pane) // what the pane ran before the start: none, after the stop
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
			return app.AgentInfo{}, ctx.Err()
		case r := <-res:
			answered = true
			if r.err != nil || detected(r.a, s) {
				cancel()
				return r.a, r.err
			}
		case <-bound.C:
			end()
			return app.AgentInfo{}, fmt.Errorf("herdr agent start %s: no %s agent came up in %s within %s", s.Agent, s.Kind, s.Pane, l.upWait())
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
func detected(a app.AgentInfo, s Start) bool {
	return a.Pane == s.Pane && a.Kind == s.Kind && a.Status != "" && a.Status != "unknown"
}

// up: a is the agent s's start put in its pane, ready for obor. Its kind, and idle or done, or working on its first
// prompt (T-387); and named s.Agent, or unnamed in a pane that ran no agent of the kind before (the caller names it:
// an old agent herdr lost the name of is never taken for the new one).
func up(pre, a app.AgentInfo, s Start) bool {
	switch {
	case a.Pane != s.Pane || a.Kind != s.Kind:
		return false
	case a.Status == "working" && s.Prompt == "":
		return false
	case a.Status != "idle" && a.Status != "done" && a.Status != "working":
		return false
	}
	return a.Name == s.Agent || a.Name == "" && pre.Kind != s.Kind
}

// busyStart: a start that carried a first prompt failed, yet the agent of s.Kind runs in s.Pane and took it (working,
// or idle/done after a short turn): started. herdr's start succeeds only once the agent is ready for input within its
// 30 s bound, and an agent that works on its first prompt from the outset is not (T-387, live: Sonnet and Codex
// workers busy on their wake at 30 s); herdr then keeps no name for it (named).
func (l *Launcher) busyStart(ctx context.Context, s Start) (app.AgentInfo, bool) {
	a, err := l.H.Agent(ctx, s.Pane)
	if err != nil || a.Kind != s.Kind || (a.Status != "working" && a.Status != "idle" && a.Status != "done") {
		return a, false
	}
	l.say("%s runs in %s (%s on its first prompt): started", s.Agent, s.Pane, a.Status)
	return a, true
}

// done is a start's success path, the one for Start and restart (T-582): the agent named (named), then a fresh start
// of a kind that reports its session searches for it (searchRollout: the transcript its process holds open, T-588; a
// resume reports none).
func (l *Launcher) done(ctx context.Context, s Start, a app.AgentInfo) (app.AgentInfo, error) {
	a, err := l.named(ctx, s, a)
	if k, ok := agents.Lookup(s.Kind); err != nil || s.Resume != "" || !ok || !k.Caps().Sessions {
		return a, err
	}
	l.searchRollout(ctx, s.Agent, s.Pane, s.Kind, s.Lane, s.Prompt != "")
	return a, nil
}

// named ends a start with herdr knowing the agent in s.Pane as s.Agent: a start can leave herdr's agent unnamed (T-387,
// live: every prompt to the name was agent_not_found until a rename by hand). a is the agent herdr showed at the
// start (no Pane: it named none, nothing to tell); unnamed or misnamed, it is renamed, best-effort (DD-v0.13 A7: serve
// finds agents by its identity table, not herdr's name): a refused rename is a warning line, the start stands. herdr
// renames by pane, so the pane must still hold that agent (same: T-405): a pane that holds another
// one now is renamed nothing, a Fail. It returns a, the agent started.
func (l *Launcher) named(ctx context.Context, s Start, a app.AgentInfo) (app.AgentInfo, error) {
	if a.Pane == "" || a.Name == s.Agent {
		return a, nil
	}
	now, err := l.H.Agent(ctx, s.Pane)
	if err != nil || !same(a, now, s.Agent) {
		return app.AgentInfo{}, project.Failf("%s started in %s, but the pane holds %s now, not the agent started (session %s): renamed nothing",
			s.Agent, s.Pane, held(now, err), orNone(a.Session))
	}
	if now.Name == s.Agent {
		return a, nil
	}
	if err := l.H.RenameAgent(ctx, s.Pane, s.Agent); err != nil {
		l.say("warning: %s started in %s, but herdr has no agent of that name there (herdr agent rename %s %s: %v)",
			s.Agent, s.Pane, s.Pane, s.Agent, err)
		return a, nil
	}
	l.say("named the agent in %s %s (herdr had no name for it)", s.Pane, s.Agent)
	return a, nil
}

// same: now, the agent the pane holds, is a, the agent the start saw there: its kind, its session (when herdr knew it
// at the start), and no name since but a's or name, the start's.
// Accepted residual risk (owner, 2026-10-26, RV-44-verify-2): with no session in herdr's answer, an unnamed same-kind
// agent that replaced this start's agent in the pane during the start (only by an action outside obor) is renamed;
// obor's own drop / reuse wait for the start (T-405).
func same(a, now app.AgentInfo, name string) bool {
	switch {
	case now.Kind != a.Kind:
		return false
	case a.Session != "" && now.Session != a.Session:
		return false
	}
	return now.Name == "" || now.Name == a.Name || now.Name == name
}

// recorded is agent's recorded session id ("": none): its current session in serve's store (T-577), nothing else.
func (l *Launcher) recorded(agent string) string {
	s, _ := l.session(agent)
	return s.ID
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

// held is what a pane holds as herdr answered for it (now, err): its agent, or none.
func held(now app.AgentInfo, err error) string {
	switch {
	case app.NotFound(err):
		return "no agent herdr knows"
	case err != nil:
		return "an agent herdr cannot tell (" + err.Error() + ")"
	}
	return "a " + now.Kind + " agent (session " + orNone(now.Session) + ", name " + orNone(now.Name) + ")"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
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
