package herdr

import (
	"context"
	"fmt"
	"time"

	"obor/internal/app/port"
)

var _ port.AgentStarter = (*Socket)(nil)

// Start is port.AgentStarter's start on herdr: herdr's agent start of s.Kind in s.Pane under the name s.Label
// (startAgent: a name herdr kept with no live agent behind it is cleared and the start retried once), returning once
// the agent runs in the pane (started), never at a fixed wait (herdr's own answer waits up to 30 s for an agent ready
// for input, which an agent busy on its first prompt never is); a start with a first prompt whose agent works on it is
// a start, its launch pending or herdr's call given up on (attempt.counts, busyStart). herdr's agent name is neither
// required nor renamed back (herdr drops it at times and obor addresses the pane by id). s.Env and s.Cwd are not
// read: the pane's shell has them already (launch's env line). The error matches port.PaneBusy when herdr refused the
// start because the pane is no available shell (agent_pane_busy: the caller may wait for the shell and start again),
// port.NotReady when herdr gave up on an agent at a startup dialog, port.TimedOut when no agent came up within
// s.Bound or herdr gave up waiting, port.NameHeld when a live agent in another pane holds the name, and
// port.Accepted when herdr accepted the start before it failed (its command line ran: the agent may run).
func (s *Socket) Start(ctx context.Context, a port.AgentStart) (port.Pane, error) {
	return start(ctx, s, a)
}

// agentCalls is what a start asks of herdr: its agent start (Socket.startAgent) and a read of the pane (Socket.read).
type agentCalls interface {
	startAgent(ctx context.Context, name, kind, pane string, say func(format string, args ...any), args ...string) (Pane, error)
	read(ctx context.Context, pane string) (Pane, error)
}

// start is Socket.Start over h.
func start(ctx context.Context, h agentCalls, a port.AgentStart) (port.Pane, error) {
	t := started(ctx, h, a)
	if t.err == nil {
		return t.a.Pane, nil
	}
	if _, held := port.NameHeld(t.err); !held && a.Prompt != "" && !port.NotReady(t.err) {
		if p, ok := busyStart(ctx, h, a, t); ok {
			return p.Pane, nil
		}
	}
	if t.accepted {
		return port.Pane{}, &accepted{t.err}
	}
	return port.Pane{}, t.err
}

// accepted is a failed start herdr accepted (port.Accepted): its answer to a start it made, or the record it resets
// the pane's agent to when it accepts one, was seen before the start failed.
type accepted struct{ err error }

func (e *accepted) Error() string  { return e.err.Error() }
func (e *accepted) Unwrap() error  { return e.err }
func (e *accepted) Accepted() bool { return true }

// notUp is a start whose agent did not come up within its bound (port.TimedOut).
type notUp struct{ msg string }

func (e *notUp) Error() string  { return e.msg }
func (e *notUp) TimedOut() bool { return true }

// attempt is one agent start as started saw it: the agent it ended with, herdr's error, and whether herdr accepted
// it: it answered a start it made (success, a pending launch included, or one it gave up waiting on: TimedOut,
// NotReady), or, before its answer, listed the record it resets the pane's agent to when it accepts a start
// (launching); never a PaneBusy refusal.
type attempt struct {
	a        Pane
	err      error
	accepted bool
}

// counts is the evidence rule: a listing a is the start's own agent only when it was read
// after herdr accepted this attempt, is up, and carries herdr's word that the launch is over
// (a.Launched, herdr's launch_pending gone or interactive_ready) or, for a start with a first prompt, works: herdr
// keeps such a launch pending while its agent works on the prompt, until its own ~30 s bound (live H-L14: claude and
// codex working with launch_pending from ~4 s), and an agent that exited, or the old agent's stale idle, is never
// working. A listing alone, kind and status, is never the proof: before the acceptance herdr may list the old agent,
// and a launch whose CLI exits leaves no agent at all.
func (t attempt) counts(a Pane, s port.AgentStart) bool {
	return t.accepted && up(a, s) && (a.Launched || s.Prompt != "" && a.Status == "working")
}

// launching: a is the record herdr resets s.Pane's agent to when it accepts a start, which no listing of an agent
// that ran there shows: the launch pending with no status yet (live H-L12, H-L14: none/unknown, then the kind with
// unknown, from the call's acceptance until the agent shows a status). Read after the start's call began and before
// its answer, it is the acceptance. An old agent's stale listing carries a status (idle, blocked, working).
func launching(a Pane, s port.AgentStart) bool {
	return a.ID == s.Pane && (a.Agent == "" || a.Agent == s.Kind) && !a.Launched && a.Status == "unknown"
}

// started runs herdr's agent start once and returns once the agent runs in s.Pane (the start's done condition), or
// herdr's answer: its error, or the agent it detected. A pending answer is no proof: herdr answers before the agent
// runs (launch_pending: a command cut off at the shell left no agent, and the start said RESTARTED), so a poll reads
// s.Pane until a listing counts by the evidence rule (attempt.counts), and none within s.Bound is an error. The poll
// runs alongside the call, from its start (herdr's answer may come after an agent busy on its first prompt is up);
// until the answer, only herdr's reset record (launching) accepts the start, never a listing of an agent. started
// owns that call: it runs under the bound, and before started returns it is cancelled and joined, its late answer
// dropped.
func started(ctx context.Context, h agentCalls, a port.AgentStart) attempt {
	var t attempt
	type answer struct {
		a   Pane
		err error
	}
	call, cancel := context.WithCancel(ctx)
	res := make(chan answer, 1)
	go func() {
		p, err := h.startAgent(call, a.Label, a.Kind, a.Pane, a.Say, a.Argv...)
		res <- answer{p, err}
	}()
	answered := false
	end := func() {
		cancel()
		if !answered {
			<-res
		}
	}
	tick := time.NewTicker(startPoll(a))
	defer tick.Stop()
	bound := time.NewTimer(startBound(a))
	defer bound.Stop()
	for {
		select {
		case <-ctx.Done():
			end()
			t.err = ctx.Err()
			return t
		case r := <-res:
			answered = true
			switch {
			case r.err == nil:
				t.accepted = true
			case port.TimedOut(r.err), port.NotReady(r.err):
				t.accepted = true // herdr stopped waiting on a start it accepted: the agent may still come up
			case port.PaneBusy(r.err):
				t.accepted = false // refused: what the poll took for its launch was not this start's
			}
			if r.err != nil || detected(r.a, a) {
				cancel()
				t.a, t.err = r.a, r.err
				return t
			}
		case <-bound.C:
			end()
			t.err = &notUp{fmt.Sprintf("terminal agent start %s: no %s agent came up in %s within %s", a.Label, a.Kind,
				a.Pane, startBound(a))}
			return t
		case <-tick.C:
			p, err := h.read(ctx, a.Pane)
			if err != nil {
				continue
			}
			if !answered && launching(p, a) {
				t.accepted = true
			}
			if t.counts(p, a) {
				end()
				t.a = p
				return t
			}
		}
	}
}

// startBound is how long a start waits for its agent to come up (s.Bound; default 30 s: herdr's own startup bound).
func startBound(s port.AgentStart) time.Duration {
	if s.Bound > 0 {
		return s.Bound
	}
	return 30 * time.Second
}

// startPoll is how often a start reads its pane (s.Poll; default 1 s).
func startPoll(s port.AgentStart) time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return time.Second
}

// detected: herdr's answer a to s's start shows the agent running: s.Kind in s.Pane, launched (not pending), with a
// status.
func detected(a Pane, s port.AgentStart) bool {
	return a.ID == s.Pane && a.Agent == s.Kind && a.Launched && a.Status != "" && a.Status != "unknown"
}

// up: a is an agent of s's kind in its pane, ready for obor: idle or done, or working on its first prompt.
// Whether it is the start's own agent is the evidence rule's (attempt.counts). herdr's name is no part of it.
func up(a Pane, s port.AgentStart) bool {
	switch {
	case a.ID != s.Pane || a.Agent != s.Kind:
		return false
	case a.Busy() && s.Prompt == "":
		return false
	}
	return a.Ready() || a.Status == "working"
}

// busyStart: a start that carried a first prompt failed, yet the agent of s.Kind runs in s.Pane and took it (working,
// or idle/done after a short turn): started, herdr's error and the start said on s.Say. herdr's start succeeds only
// once the agent is ready for input within its 30 s bound, and an agent that works on its first prompt from the
// outset is not (live: Sonnet and Codex workers busy on their wake at 30 s); herdr then keeps no name for it. The
// listing counts by the evidence rule of the last attempt t (attempt.counts): a start herdr refused while the old
// agent was still listed (agent_pane_busy) is no start, however ready that old agent looks; one herdr accepted and
// gave up on is, once herdr lists its agent launched, or working on the prompt.
func busyStart(ctx context.Context, h agentCalls, a port.AgentStart, t attempt) (Pane, bool) {
	p, err := h.read(ctx, a.Pane)
	if err != nil || !t.counts(p, a) {
		return p, false
	}
	sayf(a.Say, "%v", t.err)
	sayf(a.Say, "%s runs in %s (%s on its first prompt): started", a.Label, a.Pane, p.Status)
	return p, true
}
