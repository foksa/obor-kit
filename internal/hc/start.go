package hc

import (
	"cmp"
	"context"
	"errors"
	"strconv"

	"obor/internal/app/port"
	"obor/internal/app/shellpane"
)

// Start is port.AgentStarter's start on hc: shellpane's (the agent's command line typed at the terminal's shell, up by
// the status rule) over the terminal record: the shell is up when a foreground holds the terminal, a program other
// than its own runs there (Record.programRuns), and a status is the program's report (hc keeps `reported` true after a
// program exits, so a status, not `reported`, is the proof; a status clears when the agent exits to the shell). The
// mark is the obor.agent and obor.kind labels. hc types a command line whole (a 16 KB line arrives as it is): it
// needs no split.
func (cl *Client) Start(ctx context.Context, s port.AgentStart) (port.Pane, error) {
	return shellpane.Start(ctx, starter{cl}, s)
}

// starter is the client as shellpane's Terminal.
type starter struct{ cl *Client }

func (starter) Name() string { return "hc" }

func (t starter) Look(ctx context.Context, pane string) (shellpane.Look, error) {
	r, err := t.cl.live(ctx, pane)
	if err != nil {
		return shellpane.Look{}, err
	}
	p := r.pane(t.cl.kinds())
	p.Group = ""
	l := shellpane.Look{Pane: p, Up: r.Foreground != nil, Reported: r.Status != nil}
	if r.programRuns() {
		l.Program = cmp.Or(r.Foreground.Name, strconv.Itoa(r.Foreground.PID))
	}
	return l, nil
}

func (t starter) Mark(ctx context.Context, pane, label, kind string) error {
	labels := map[string]string{LabelKind: kind}
	if label != "" {
		labels[LabelAgent] = label
	}
	return t.cl.label(ctx, pane, labels)
}

// Type is Write with Enter; began unless hc refused the send or it never went out whole.
func (t starter) Type(ctx context.Context, pane, line string) (bool, error) {
	err := t.cl.Write(ctx, pane, line, true)
	var e *Error
	return err == nil || errors.As(err, &e) && e.sent && e.Kind == "", err
}
