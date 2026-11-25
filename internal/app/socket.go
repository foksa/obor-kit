package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"obor/internal/app/wire"
	"obor/internal/version"
	"sort"
	"strings"
	"time"
)

// requestTimeout bounds a socket request's effect (a resolved's Git). It outlasts the connection's 10 s: an effect
// the caller stopped waiting for still completes, its reply stored for the repeated call id.
const requestTimeout = time.Minute

// Listen answers the app socket's requests on ln until it is closed: one request line per connection. A request's
// effect runs under the daemon's lifetime and requestTimeout; once Serve has ended, a connection is closed unanswered.
// Serve binds the socket only once its project is loaded (cmd/obor/serve.go, Loaded): a request never waits
// for the replay, a client retries the socket that is not there yet inside its own deadline.
func (a *App) Listen(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		stopped := a.stopped
		if !stopped {
			a.reqs.Add(1)
		}
		a.mu.Unlock()
		if stopped {
			c.Close()
			continue
		}
		go func() {
			defer a.reqs.Done()
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second)) // the request line
			b, err := bufio.NewReader(c).ReadBytes('\n')
			if err != nil {
				return
			}
			var req wire.Request
			var ans wire.Answer
			var env *wire.Envelope // the caller, read first from any request that carries a call
			unreadable := json.Unmarshal(b, &req) != nil
			if !unreadable && len(req.Call) > 0 && string(req.Call) != "null" { // null: no call, as before the split
				env = &wire.Envelope{}
				if unreadable = json.Unmarshal(req.Call, env) != nil; unreadable {
					env = nil
				} else {
					a.noteSkew(req.Method, *env)
				}
			}
			do, known := a.handlers()[req.Method]
			switch {
			case unreadable:
				ans.Error = "unreadable request"
			case req.Method == "status":
				st := a.Status()
				ans.Status = &st
			case req.Method == wire.MethodEvents: // a stream of lines, its own writes
				var r *wire.EventsReq
				if env != nil {
					r = &wire.EventsReq{}
					_ = json.Unmarshal(req.Call, r) // its envelope read: its fields are numbers and a bool
				}
				a.feed(c, r)
				return
			case !known:
				ans.Error = "unknown method " + req.Method
			case env == nil:
				ans.Error = req.Method + ": no call"
			default:
				var err error
				if ans.Reply, err = do(req.Method, req.Call); err != nil {
					ans.Error = err.Error()
				}
			}
			if ans.Error != "" && env != nil {
				if skew := versionSkew(*env); skew != "" {
					ans.Error += fmt.Sprintf(" (caller %s runs %s, serve %s)", callerOf(*env), skew, version.Version)
				}
			}
			out, _ := json.Marshal(ans)
			// The answer is written once its effect is done, which may wait for a stop: its own bound, not the
			// request line's.
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, _ = c.Write(append(out, '\n'))
		}()
	}
}

// handler answers one method's request: its call's JSON decoded into the method's request type (handle).
type handler func(method string, call json.RawMessage) (string, error)

// handlers is the socket's methods but status and events (Listen's own): each decodes the call into its request type
// and answers it; dispatch, drive and the integrator's verdicts run under the daemon's lifetime and
// requestTimeout.
func (a *App) handlers() map[string]handler {
	verdict := handle(func(c wire.VerdictReq) (string, error) {
		ctx, cancel := context.WithTimeout(a.life, requestTimeout)
		defer cancel()
		return a.verdict(ctx, c)
	})
	return map[string]handler{
		wire.MethodAccept:       verdict,
		wire.MethodSendback:     verdict,
		wire.MethodReject:       verdict,
		wire.MethodResolved:     verdict,
		wire.MethodHold:         verdict,
		wire.MethodResume:       handle(a.resume),
		wire.MethodDrop:         handle(a.drop),
		wire.MethodJobNext:      handle(a.jobNext),
		wire.MethodTaskDone:     handle(a.taskDone),
		wire.MethodMergePreview: handle(a.mergePreview),
		wire.MethodDispatch:     handle(timed(a, a.dispatchCall)),
		wire.MethodDrive:        handle(timed(a, a.driveCall)),
		wire.MethodLand:         handle(a.landCall),
		wire.MethodRestartJob:   handle(a.restartJob),
		wire.MethodSpecSet:      handle(a.specSet),
		wire.MethodPause:        handle(a.pauseCall),
		wire.MethodSend:         handle(a.sendCall),
		wire.MethodBrief:        handle(a.briefCall),
		wire.MethodWatch:        handle(a.watchCall),
		wire.MethodReview:       handle(a.reviewCall),
		wire.MethodStopped:      handle(a.stoppedCall),
		wire.MethodTurn:         handle(a.turnCall),
		wire.MethodLeadLines:    handle(a.leadLinesCall),
		wire.MethodSession:      handle(a.sessionCall),
		wire.MethodStarted:      handle(a.startedCall),
		wire.MethodExit:         handle(a.exitCall),
		wire.MethodConfig:       handle(a.configCall),
		wire.MethodTellLead:     handle(a.tellLeadCall),
		wire.MethodIdle:         handle(a.idleCall),
		wire.MethodReviewClose:  handle(a.reviewCloseCall),
		wire.MethodBackup:       handle(a.backup),
		wire.MethodPrune:        handle(timed(a, a.pruneCall)),
		wire.MethodWhere:        handle(a.whereCall),
	}
}

// handle is f as a handler: the call decoded into f's request type R; a VerdictReq's Verb is the method it came under.
func handle[R any](f func(R) (string, error)) handler {
	return func(method string, call json.RawMessage) (string, error) {
		var r R
		if err := json.Unmarshal(call, &r); err != nil {
			return "", errors.New("unreadable request")
		}
		if v, ok := any(&r).(*wire.VerdictReq); ok {
			v.Verb = method
		}
		return f(r)
	}
}

// timed is f under the daemon's lifetime and requestTimeout: a dispatch's and a drive's effect outlives a caller that
// stopped waiting.
func timed[R any](a *App, f func(context.Context, R) (string, error)) func(R) (string, error) {
	return func(r R) (string, error) {
		ctx, cancel := context.WithTimeout(a.life, requestTimeout)
		defer cancel()
		return f(ctx, r)
	}
}

// versionSkew is c's caller's obor as text when it differs from this serve's: its version, or "an older obor"
// for a call that carried none (a binary before the field); "" when they match. A difference alone refuses nothing:
// obor-kit's dev builds differ after every merge, so only a refusal names it and serve's log says it once.
func versionSkew(c wire.Envelope) string {
	switch {
	case c.Version == version.Version:
		return ""
	case c.Version == "":
		return "an older obor"
	}
	return "obor " + c.Version
}

// noteSkew writes serve's one log line for a call whose caller runs another obor: the first per (agent, version), not
// each call. It also keeps, per agent, the version of the other release its calls carry (behind): a session request
// clears it — a hook's, a plugin's, or a start's of the session the agent runs, a repeat of one already recorded
// included — and so does a started request, a launcher's confirmed start (startedCall). A call of serve's own release
// never clears it: one agent calls through several processes, and the line must not flicker between them. A started
// request names the agent a launcher started, not a caller beside it: its reporter's release is not that agent's, so it
// holds nothing here and logs nothing.
func (a *App) noteSkew(method string, c wire.Envelope) {
	if method == wire.MethodStarted {
		return
	}
	if c.Agent != "" { // the owner's own CLI is not an agent: nothing to restart
		other := version.Release(c.Version) != version.Release(version.Version)
		a.mu.Lock()
		if method == wire.MethodSession {
			delete(a.behind, c.Agent) // a session began: the process before it is not the one running now
		}
		if other {
			if a.behind == nil {
				a.behind = map[string]string{}
			}
			a.behind[c.Agent] = c.Version
		}
		a.mu.Unlock()
	}
	skew := versionSkew(c)
	if skew == "" {
		return
	}
	agent := callerOf(c)
	key := agent + "\x00" + c.Version
	a.mu.Lock()
	if a.skews == nil {
		a.skews = map[string]bool{}
	}
	seen := a.skews[key]
	a.skews[key] = true
	a.mu.Unlock()
	if !seen {
		a.logf("caller %s runs %s, serve %s", agent, skew, version.Version)
	}
}

// skewNote is the status note for the agents in a pane whose calls carry another obor release than serve's (behind):
// "pj-l-01 runs obor v0.33.0, pj-rev runs an older obor (serve v0.34.0)", agents in name order; "" when none. seen is
// the agents serve observes in a pane now: one that left is not named. The caller holds a.mu.
func (a *App) skewNote(seen map[string]bool) string {
	var names []string
	for name := range a.behind {
		if seen[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" runs "+versionSkew(wire.Envelope{Version: a.behind[name]}))
	}
	return strings.Join(parts, ", ") + " (serve " + version.Version + ")"
}

// started ends agent's skew entry: a start (fresh or resumed) runs a new process for it, so the release its earlier
// calls carried is not the one serving its calls now, and the line naming it asks for a restart that happened. A start
// that names no agent ends nothing. Under mu.
func (a *App) started(agent string) {
	if agent == "" {
		return
	}
	a.mu.Lock()
	delete(a.behind, agent)
	a.mu.Unlock()
}

// startedCall answers the socket's started request: a launcher's start of the envelope's agent is confirmed — the process
// that runs now is that start's, whatever session it reports (a fresh Codex start with no prompt has none yet) — so
// serve ends the agent's skew entry. It records no session: a session is the session request's.
func (a *App) startedCall(c wire.StartedReq) (string, error) {
	if c.Agent == "" {
		return "", errors.New(wire.MethodStarted + ": the agent is required")
	}
	if a.projAt(c.Root) == nil {
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodStarted, c.Root)
	}
	a.started(c.Agent)
	return "STARTED " + c.Agent, nil
}

// Started is the started request in process, for the launchers serve itself runs (Launcher.Started): agent's start in
// the served project at root is confirmed.
func (a *App) Started(root, agent string) error {
	_, err := a.startedCall(*wire.StartedRequest(root, agent))
	return err
}

// exitCall answers the socket's exit request (obor stop): for the project this serve serves, from the lead or
// the owner, serve ends gracefully (Options.Exit: its context cancelled, as a signal and the self-restart end it).
// The reply goes out before serve is down: the caller confirms that by serve.flock and the socket.
func (a *App) exitCall(c wire.ExitReq) (string, error) {
	a.mu.Lock()
	p := a.projAt(c.Root)
	a.mu.Unlock()
	refused := leadOrOwner(wire.MethodExit, "ends serve", c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodExit, c.Root)
	case refused != nil:
		return "", refused
	case a.opt.Exit == nil:
		return "", errors.New(wire.MethodExit + ": this serve cannot end itself")
	}
	a.opt.Exit()
	return "EXITING", nil
}
