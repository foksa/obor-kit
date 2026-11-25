package rex

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// probeEnv is the env probe block's text from the recorded format answer (testdata/env-probe.format.json: a scratch
// server started with an obor pane's, herdr's, hc's and a Claude session's names in its environment).
func probeEnv(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/env-probe.format.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return strings.Split(r.Content, "\n")
}

// A block obor makes unsets every leaked identity name the server's environment holds (OBOR_ROLE, HERDR_PANE_ID,
// CLAUDE_CODE_SESSION_ID among them) before its own variables, then runs the server's shell as a login shell: none of
// the names reaches the block's shell unless the block's own env sets it. Rex's own names, PATH, HOME and the rest
// stay; the probe's other lines (login's banner) are no variables.
func TestEnvScrub(t *testing.T) {
	environ := probeEnv(t)
	for _, n := range []string{"OBOR_ROLE", "HERDR_PANE_ID", "CLAUDE_CODE_SESSION_ID"} {
		if !slices.ContainsFunc(environ, func(l string) bool { return strings.HasPrefix(l, n+"=") }) {
			t.Fatalf("precondition: the recorded server environment has no %s", n)
		}
	}
	e, err := readEnv(environ)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "HC_TERMINAL_ID", "HERDR_ENV",
		"HERDR_PANE_ID", "OBOR_PANE", "OBOR_ROLE"}
	if !slices.Equal(e.Unset, want) || e.Shell != "/bin/zsh" {
		t.Errorf("unset %q, shell %q; want %q and /bin/zsh", e.Unset, e.Shell, want)
	}
	cmd := e.command([]string{"A=1", "B=", "OBOR_ROLE=worker"})
	wantCmd := []string{"/usr/bin/env"}
	for _, n := range want {
		wantCmd = append(wantCmd, "-u", n)
	}
	wantCmd = append(wantCmd, "A=1", "B=", "OBOR_ROLE=worker", "/bin/zsh", "-l")
	if !slices.Equal(cmd, wantCmd) {
		t.Errorf("command %q, want %q", cmd, wantCmd)
	}
}

// An environment that names no SHELL is an error: no shell is guessed.
func TestEnvWithoutShell(t *testing.T) {
	if _, err := readEnv([]string{"PATH=/bin", "OBOR_ROLE=x"}); err == nil || !strings.Contains(err.Error(), "names no SHELL") {
		t.Errorf("readEnv = %v, want the no-SHELL error", err)
	}
}
