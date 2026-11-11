package project

import "testing"

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
