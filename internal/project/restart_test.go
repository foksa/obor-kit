package project

import (
	"errors"
	"testing"
)

// RestartCmd is the one formatter of the command that starts an agent again (setup's failed shell, launch's startup
// hint): a recorded session resumes, none starts fresh, pane names where, and a start with its own command (a drive's)
// uses it as it is.
func TestRestartCmd(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		own        string
		agent      string
		hasSession bool
		pane       string
		want       string
	}{
		{"a session: no --fresh", "", "pj-l-01", true, "w1:n1", "obor restart pj-l-01 --pane w1:n1"},
		{"no session: --fresh", "", "pj-l-01", false, "w1:n1", "obor restart pj-l-01 --fresh --pane w1:n1"},
		{"no pane: none named", "", "pj-l-01", true, "", "obor restart pj-l-01"},
		{"a drive's own command, its prompt kept", "obor drive T-07", "pj-l-01-drive", false, "w1:n1", "obor drive T-07"},
	} {
		if got := RestartCmd(c.own, c.agent, c.hasSession, c.pane); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// RetryHint marks a failure whose message already says how to run its start again: its text and its Fail are kept,
// and a plain error does not read as one.
func TestRetryHint(t *testing.T) {
	t.Parallel()
	fail := Fail("pane w1:n1 is not an available shell: start the agent there with obor drive T-07")
	hinted := RetryHint(fail)
	if !RetryHinted(hinted) {
		t.Error("a wrapped failure is not RetryHinted")
	}
	if hinted.Error() != fail.Error() {
		t.Errorf("text %q, want %q", hinted, fail)
	}
	var got Fail
	if !errors.As(hinted, &got) || got != fail {
		t.Errorf("the wrapped failure is not reachable as its Fail: %v", hinted)
	}
	if RetryHinted(errors.New("lane L-01 is not prepared for T-07 after 1m0s")) {
		t.Error("a plain failure reads as carrying a retry")
	}
}
