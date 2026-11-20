package hc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/project"
)

// Start is port.AgentStarter's start on hc: the agent's command line (the kind's program and s.Argv, each word
// quoted) typed at the pane's shell, which has the agent's env and cwd already (launch's env line; s.Env and s.Cwd are
// not read), after the pane is labelled with the agent's name and its kind (obor.agent, obor.kind). It returns once
// the agent is up: a program other than the shell holds the foreground and reports a status naming s.Kind, idle or
// done (or working, for a start with a first prompt). hc keeps `reported` true after a program exits, so a status,
// not `reported`, is the proof; a status clears when the agent exits to the shell. The error matches port.PaneBusy
// when the pane is no available shell (a program holds its foreground, or its shell is not up), and, once the command
// line was typed, port.Accepted and one of: port.NotReady when the agent reports blocked (a startup dialog: Claude's
// trust dialog) or a program holds the foreground with no status at the bound (a dialog that reports nothing:
// Codex's trust dialog; launch reads the screen for it), port.TimedOut when the shell still holds the foreground at
// the bound. hc holds no agent names, so no start is refused as port.NameHeld.
func (cl *Client) Start(ctx context.Context, s port.AgentStart) (port.Pane, error) {
	k, ok := cl.Kinds.Lookup(s.Kind)
	if !ok {
		return port.Pane{}, &Error{Op: "terminal.send", Msg: "no agent kind " + s.Kind}
	}
	r, err := cl.live(ctx, s.Pane)
	if err != nil {
		return port.Pane{}, err
	}
	if r.Foreground == nil || r.programRuns() {
		prog := "its shell is not up"
		if r.Foreground != nil {
			prog = r.Foreground.Name + " runs there"
		}
		return port.Pane{}, &paneBusy{fmt.Sprintf("hc: %s is not an available shell (%s)", s.Pane, prog)}
	}
	labels := map[string]string{LabelKind: s.Kind}
	if s.Label != "" {
		labels[LabelAgent] = s.Label
	}
	if err := cl.label(ctx, s.Pane, labels); err != nil {
		return port.Pane{}, err
	}
	if err := cl.Write(ctx, s.Pane, commandLine(k, s.Argv), true); err != nil {
		var e *Error
		if errors.As(err, &e) && e.sent && e.Kind == "" {
			return port.Pane{}, &accepted{err}
		}
		return port.Pane{}, err
	}
	p, err := cl.up(ctx, s)
	if err != nil {
		return port.Pane{}, &accepted{err}
	}
	return p, nil
}

// commandLine is k's program and argv as one shell command line. hc types it whole (a 16 KB line arrives as it is):
// it needs no split.
func commandLine(k agents.Kind, argv []string) string {
	words := []string{project.ShellQuote(agents.Programs(k)[0])}
	for _, a := range argv {
		words = append(words, project.ShellQuote(a))
	}
	return strings.Join(words, " ")
}

// up reads s.Pane at once and then every s.Poll (default 1 s) until its agent is up, within s.Bound (default 30 s).
func (cl *Client) up(ctx context.Context, s port.AgentStart) (port.Pane, error) {
	bound, poll := s.Bound, s.Poll
	if bound <= 0 {
		bound = 30 * time.Second
	}
	if poll <= 0 {
		poll = time.Second
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	end := time.NewTimer(bound)
	defer end.Stop()
	var last Record
	for {
		r, err := cl.live(ctx, s.Pane)
		if port.NotFound(err) {
			return port.Pane{}, err
		}
		if err == nil {
			last = r
			if p, done, err := started(cl.Kinds, r, s); done {
				return p, err
			}
		}
		select {
		case <-ctx.Done():
			return port.Pane{}, ctx.Err()
		case <-end.C:
			if last.programRuns() {
				return port.Pane{}, &notReady{fmt.Sprintf("hc: %s runs in %s and reported no status within %s: it may wait at "+
					"a startup dialog", last.Foreground.Name, s.Pane, bound)}
			}
			return port.Pane{}, &timedOut{fmt.Sprintf("hc: no %s agent came up in %s within %s", s.Kind, s.Pane, bound)}
		case <-tick.C:
		}
	}
}

// started is what r says of s's start: done with the agent's pane once it is up (a program holds the foreground and
// reports a status naming s.Kind, ready, or working on a first prompt), done with a NotReady error when it reports
// blocked; not done while no program reports a status there.
func started(kinds agents.Kinds, r Record, s port.AgentStart) (port.Pane, bool, error) {
	if !r.programRuns() || r.Status == nil {
		return port.Pane{}, false, nil
	}
	p := r.pane(kinds)
	p.Group = ""
	switch {
	case r.Status.State == "blocked":
		return port.Pane{}, true, &notReady{fmt.Sprintf("hc: the %s agent in %s is blocked at start (%s)", s.Kind, s.Pane,
			joined(r.Status.Kind, r.Status.Msg))}
	case p.Agent != s.Kind: // a status naming another kind: not this start's agent
		return port.Pane{}, false, nil
	case p.Ready(), p.Status == "working" && s.Prompt != "":
		return p, true, nil
	}
	return port.Pane{}, false, nil
}

// paneBusy is a start refused because the pane is no available shell (port.PaneBusy).
type paneBusy struct{ msg string }

func (e *paneBusy) Error() string  { return e.msg }
func (e *paneBusy) PaneBusy() bool { return true }

// notReady is a started agent that waits at a startup dialog (port.NotReady).
type notReady struct{ msg string }

func (e *notReady) Error() string  { return e.msg }
func (e *notReady) NotReady() bool { return true }

// accepted is a failed start whose command line hc typed (port.Accepted): the agent may run.
type accepted struct{ err error }

func (e *accepted) Error() string  { return e.err.Error() }
func (e *accepted) Unwrap() error  { return e.err }
func (e *accepted) Accepted() bool { return true }
