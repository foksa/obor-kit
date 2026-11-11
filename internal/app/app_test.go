package app

import (
	"errors"
	"testing"
	"time"
)

func TestReady(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{"idle": true, "done": true, "working": false, "blocked": false, "unknown": false, "": false} {
		if Ready(status) != want {
			t.Errorf("Ready(%q) = %v, want %v", status, !want, want)
		}
	}
}

// notFoundErr is a terminal's not-found answer (port.NotFound).
type notFoundErr struct{}

func (notFoundErr) Error() string  { return "agent.get: not found" }
func (notFoundErr) NotFound() bool { return true }

// The step that fails with the terminal's not-found just after its agent's start is uncounted (herdr may not know a
// new agent's name yet); every other error, and the same one later, is a failure.
func TestUnsettled(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		err     error
		since   time.Duration // now - start
		started bool          // startAt was recorded
		want    bool
	}{
		{"not found right after the start", notFoundErr{}, 20 * time.Second, true, true},
		{"not found at the settle bound", notFoundErr{}, startSettle, true, false},
		{"not found with no start recorded", notFoundErr{}, 20 * time.Second, false, false},
		{"another error right after the start", errors.New("pane busy"), 20 * time.Second, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			startAt := start
			if !c.started {
				startAt = time.Time{}
			}
			if got := unsettled(c.err, startAt, start.Add(c.since)); got != c.want {
				t.Errorf("unsettled(%v, %v, %v) = %t, want %t", c.err, startAt, start.Add(c.since), got, c.want)
			}
		})
	}
}
