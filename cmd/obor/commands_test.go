package main

import (
	"regexp"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// The T-211 commands through the binary: usage, help and exit codes (behaviour: internal/run and cmd/obor).
func TestSmallCommandsBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("O-01", "task/T-01")
	p.Config(testkit.Set("commands", `{"t": {"cmd": "echo ran", "timeout": 5, "heavy": true}}`),
		testkit.Set("slots", map[string]any{"dir": p.Base + "/slots"}))
	p.Herdr().Agent("w1:p1", "o-01", "idle", p.Root)
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
		{lane, []string{"run", "t", "--", "-h"}, 0, `ran -- -h\n`, ""},
		{lane, []string{"run", "nope"}, 2, `FAIL no commands\.nope in the config\n`, ""},
		{p.Base, []string{"run", "t"}, 2, `FAIL no usable \.obor/config\.json \(obor config validate\)\n`, ""},
		// T-465: gone with the heartbeat (pause: a request to serve, pause_test.go)
		{p.Root, []string{"ack", "T-06", "x"}, 2, ``, "unknown subcommand: ack"},
		{p.Root, []string{"notify", "Build complete"}, 2, ``, "unknown subcommand: notify"},
		{p.Root, []string{"wait-agent", "o-01"}, 2, ``, "unknown subcommand: wait-agent"},
		{p.Root, []string{"handoff", "ready"}, 2, ``, "unknown subcommand: handoff"},
		{p.Root, []string{"heartbeat", "--once"}, 2, ``, "unknown subcommand: heartbeat"},
	} {
		rc, stdout, stderr := run(t, c.dir, env, c.args...)
		if rc != c.rc || !regexp.MustCompile(`^(?s:`+c.out+`)$`).MatchString(stdout) || !strings.Contains(stderr, c.err) {
			t.Errorf("obor %q: exit %d, %q, %q; want %d, /%s/, %q", c.args, rc, stdout, stderr, c.rc, c.out, c.err)
		}
	}
}
