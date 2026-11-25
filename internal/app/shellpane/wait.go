package shellpane

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/progstatus"
)

// notFound is a wait with no agent to wait for (port.NotFound): the pane is gone, or no agent runs there.
type notFound struct{ msg string }

func (e *notFound) Error() string  { return e.msg }
func (e *notFound) NotFound() bool { return true }

// WaitStatus waits up to timeout for the agent in pane to be in one of until (progstatus.TurnEnded), on its own
// subscription to the terminal's panes (name: the terminal in its messages): the subscription's snapshot, then its
// events; a stream that ends as a refresh is subscribed again. A turn that ends on an error (Word idle, Note
// "error: …") is logged. The error matches port.TimedOut at timeout; it is not found when the terminal has no such
// pane or it went (exited, closed), or when no agent runs there (no status, its shell in the foreground: none ran, or
// it exited); any other end of the stream (a lost connection: port.NotRunning) is its error.
func WaitStatus(ctx context.Context, name string, panes port.Panes, pane string, until []string, timeout time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failed := func(err error) error {
		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
			return &timedOut{fmt.Sprintf("%s: the agent in %s is not %s after %s", name, pane, strings.Join(until, " or "), timeout)}
		}
		return err
	}
	logged := ""
	for {
		st, err := panes.Subscribe(wctx)
		if err != nil {
			return failed(err)
		}
		err = follow(wctx, name, st, pane, until, &logged)
		st.Close()
		if !port.Refreshed(err) {
			return failed(err)
		}
	}
}

// follow reads st until pane's agent is in one of until (nil), it is gone or runs no agent (notFound), or st ends
// (its error; ctx ending is ctx's error).
func follow(ctx context.Context, name string, st *port.Stream, pane string, until []string, logged *string) error {
	var p port.Pane
	known := false
	for _, q := range st.Snapshot.Panes {
		if q.ID == pane {
			p, known = q, true
		}
	}
	for {
		switch {
		case !known:
			return &notFound{fmt.Sprintf("%s: no pane %s", name, pane)}
		case p.Status == "":
			return &notFound{fmt.Sprintf("%s: %s runs no agent (no status, its shell in the foreground)", name, pane)}
		case p.Status != "unknown":
			if isError(p) && p.Note != *logged {
				*logged = p.Note
				log.Printf("%s: the agent in %s ended its turn on an %s", name, pane, p.Note)
			}
			if progstatus.TurnEnded(p.Status, until) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, open := <-st.C:
			if !open {
				return st.Err()
			}
			if ev.Pane != pane {
				continue
			}
			switch ev.Kind {
			case port.EventExited, port.EventClosed:
				return &notFound{fmt.Sprintf("%s: %s has gone (%s)", name, pane, ev.Kind)}
			case port.EventCreated, port.EventStatus:
				p, known = ev.New, true
			}
		}
	}
}

// isError: p's agent ended its turn on an error (progstatus: an error report is idle, its note "error" or
// "error: msg").
func isError(p port.Pane) bool {
	return p.Status == "idle" && (p.Note == "error" || strings.HasPrefix(p.Note, "error: "))
}
