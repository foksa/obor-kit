// Package shellpane is the shell pane model of the terminals that run an agent by typing its command line at a pane's
// login shell (hc's terminals, Rex's blocks): the agent start (Start: the command line typed, then up by the status
// rule) and the wait for an agent's status (WaitStatus, over the terminal's port.Panes). Each adapter gives its own
// facts (Terminal: a pane as the start reads it, its label, the typing); the rules are here, once. It imports
// app/port and progstatus only.
package shellpane

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/project"
)

// Look is a pane as a start reads it: the pane as the terminal reports it (Group not set), whether its shell is up
// (something holds its foreground), the program other than the pane's shell that holds the foreground ("" at the
// shell), and whether the pane's status is a program's report that counts (hc: a status; Rex: a root record whose
// owner holds the foreground).
type Look struct {
	Pane     port.Pane
	Up       bool
	Program  string
	Reported bool
}

// Terminal is what a start needs of a terminal: its name in the start's messages ("hc", "rex"), a pane read now
// (NotFound: no such pane), the pane marked as the agent's (its name, label "" leaves the name, and its kind, where
// the terminal keeps one), and a command line typed at the pane with Enter after it. Type's began says the line may
// have reached the pane (false only when nothing was typed).
type Terminal interface {
	Name() string
	Look(ctx context.Context, pane string) (Look, error)
	Mark(ctx context.Context, pane, label, kind string) error
	Type(ctx context.Context, pane, line string) (began bool, err error)
}

// Start is port.AgentStarter's start on a shell pane: s.Program and s.Argv as one command line (CommandLine) typed at
// the pane's shell, which has the agent's env and cwd already (launch's env line; s.Env and s.Cwd are not read), after
// the pane is marked with the agent's name and kind. It returns once the agent is up (started): a program other than
// the shell holds the foreground and reports a status naming s.Kind, idle or done (or working, for a start with a
// first prompt). The error matches port.PaneBusy when the pane is no available shell (its shell is not up, or a
// program holds its foreground), and, once the command line was typed, port.Accepted and one of: port.NotReady when
// the agent reports blocked (a startup dialog: Claude's trust dialog) or a program holds the foreground with no
// status at the bound (a dialog that reports nothing: Codex's trust dialog; launch reads the screen for it),
// port.TimedOut when the shell still holds the foreground at the bound. A shell pane holds no agent names, so no start
// is refused as port.NameHeld.
func Start(ctx context.Context, t Terminal, s port.AgentStart) (port.Pane, error) {
	if s.Program == "" {
		return port.Pane{}, fmt.Errorf("%s: no program for agent kind %s", t.Name(), s.Kind)
	}
	l, err := t.Look(ctx, s.Pane)
	if err != nil {
		return port.Pane{}, err
	}
	if !l.Up || l.Program != "" {
		why := "its shell is not up"
		if l.Up {
			why = l.Program + " runs there"
		}
		return port.Pane{}, &paneBusy{fmt.Sprintf("%s: %s is not an available shell (%s)", t.Name(), s.Pane, why)}
	}
	if err := t.Mark(ctx, s.Pane, s.Label, s.Kind); err != nil {
		return port.Pane{}, err
	}
	if began, err := t.Type(ctx, s.Pane, CommandLine(s.Program, s.Argv)); err != nil {
		if began {
			return port.Pane{}, &accepted{err}
		}
		return port.Pane{}, err
	}
	p, err := up(ctx, t, s)
	if err != nil {
		return port.Pane{}, &accepted{err}
	}
	return p, nil
}

// CommandLine is program and argv as one shell command line, each word quoted.
func CommandLine(program string, argv []string) string {
	words := []string{project.ShellQuote(program)}
	for _, a := range argv {
		words = append(words, project.ShellQuote(a))
	}
	return strings.Join(words, " ")
}

// up reads s.Pane at once and then every s.Poll (default 1 s) until its agent is up, within s.Bound (default 30 s).
func up(ctx context.Context, t Terminal, s port.AgentStart) (port.Pane, error) {
	bound := cmp.Or(s.Bound, 30*time.Second)
	poll := cmp.Or(s.Poll, time.Second)
	tick := time.NewTicker(poll)
	defer tick.Stop()
	end := time.NewTimer(bound)
	defer end.Stop()
	var last Look
	for {
		l, err := t.Look(ctx, s.Pane)
		if port.NotFound(err) {
			return port.Pane{}, err
		}
		if err == nil {
			last = l
			if p, done, err := started(t.Name(), l, s); done {
				return p, err
			}
		}
		select {
		case <-ctx.Done():
			return port.Pane{}, ctx.Err()
		case <-end.C:
			if last.Program != "" {
				return port.Pane{}, &notReady{fmt.Sprintf("%s: %s runs in %s and reported no status within %s: it may wait at "+
					"a startup dialog", t.Name(), last.Program, s.Pane, bound)}
			}
			return port.Pane{}, &timedOut{fmt.Sprintf("%s: no %s agent came up in %s within %s", t.Name(), s.Kind, s.Pane, bound)}
		case <-tick.C:
		}
	}
}

// started is what l says of s's start: done with the agent's pane once it is up (a program holds the foreground and
// reports a status naming s.Kind, ready, or working on a first prompt), done with a NotReady error when it reports
// blocked; not done while no program reports a status there.
func started(name string, l Look, s port.AgentStart) (port.Pane, bool, error) {
	if l.Program == "" || !l.Reported {
		return port.Pane{}, false, nil
	}
	p := l.Pane
	switch {
	case p.Status == "blocked":
		return port.Pane{}, true, &notReady{fmt.Sprintf("%s: the %s agent in %s is blocked at start (%s)", name, s.Kind, s.Pane, p.Note)}
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

// accepted is a failed start whose command line was typed (port.Accepted): the agent may run.
type accepted struct{ err error }

func (e *accepted) Error() string  { return e.err.Error() }
func (e *accepted) Unwrap() error  { return e.err }
func (e *accepted) Accepted() bool { return true }

// timedOut is a wait that ended at its own bound (port.TimedOut).
type timedOut struct{ msg string }

func (e *timedOut) Error() string  { return e.msg }
func (e *timedOut) TimedOut() bool { return true }
