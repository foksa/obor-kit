//go:build integration

package main

import (
	"regexp"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// The small commands through the binary: usage, help and exit codes (behaviour: internal/run and cmd/obor).
func TestSmallCommandsBinary(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "")
	p.Config(testkit.Set("commands", `{"t": {"cmd": "echo ran", "timeout": 5, "heavy": true}}`),
		testkit.Set("checks", map[string]any{"slot_dir": p.Base + "/slots"}))
	p.Herdr().Agent("w1:p1", "l-01", "idle", p.Root)
	env := []string{"HERDR_SOCKET_PATH=" + herdrtest.Serve(t, p.Herdr()).Path}
	for _, c := range []struct {
		dir  string
		args []string
		rc   int
		out  string // a regexp the whole stdout matches
		err  string // a substring of stderr
	}{
		// bash run.test.sh case_help, case_rc_zero_and_unknown
		{lane, []string{"run"}, 2, `FAIL usage: obor run .*\n`, ""},
		{lane, []string{"run", "--help"}, 0, `usage: obor run .*\n`, ""},
		{lane, []string{"run", "t", "-h"}, 0, `usage: obor run .*\ncommands\.t: echo ran  \(timeout 5 s, heavy\)\n`, ""},
		{lane, []string{"run", "nope"}, 2, `FAIL no commands\.nope in the config\n`, ""},
		{p.Base, []string{"run", "t"}, 2, `FAIL no usable \.obor/config\.json \(obor config validate\)\n`, ""},
		// an unknown subcommand in a project (TestExitCodes: the exit code outside one; TestMonitorIsUnknown: the same line
		// for another name; pause is a request to serve, pause_test.go)
		{p.Root, []string{"ack", "T-06", "x"}, 2, ``, "unknown subcommand: ack"},
	} {
		rc, stdout, stderr := run(t, c.dir, env, c.args...)
		if rc != c.rc || !regexp.MustCompile(`^(?s:`+c.out+`)$`).MatchString(stdout) || !strings.Contains(stderr, c.err) {
			t.Errorf("obor %q: exit %d, %q, %q; want %d, /%s/, %q", c.args, rc, stdout, stderr, c.rc, c.out, c.err)
		}
	}
}

// obor run passes what follows -- to the command, -h included: the one row that runs the configured command, so the
// project is a git worktree (the run checks it).
func TestRunPassesArgsAfterDashDash(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config(testkit.Set("commands", `{"t": {"cmd": "echo ran", "timeout": 5}}`),
		testkit.Set("checks", map[string]any{"slot_dir": p.Base + "/slots"}))
	if rc, stdout, stderr := run(t, p.Root, nil, "run", "t", "--", "-h"); rc != 0 || stdout != "ran -- -h\n" {
		t.Errorf("obor run t -- -h: exit %d, %q, %q; want 0 and ran -- -h", rc, stdout, stderr)
	}
}
