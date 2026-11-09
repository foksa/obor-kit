package main

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// obor dispatch / drop (obor serve has drop's and dispatch's behaviour):
// argument parsing, no config, the result line last with its exit code. bash dispatch.test.sh
// case_unknown_lane_and_bad_id (usage); bash drop.test.sh case_unknown_lane (usage).
func TestLaneCommands(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	o1 := p.PlainLane("L-01", "task/T-01")
	// The installed fake is a snapshot: the agent stays in group 2 with no process listed, and whether pid 2 runs is
	// the host's (none on macOS: the exit is proven at once; else drop's worker stop waits OBOR_RESTART_WAIT).
	env := []string{"HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state-home", "OBOR_RESTART_WAIT=1"}
	for _, c := range []struct {
		args []string
		last string
	}{
		{[]string{"dispatch", "T-05"}, "FAIL no usable .obor/config.json (obor config validate)"},
		{[]string{"drop", "T-01", "why"}, "FAIL no usable .obor/config.json (obor config validate)"},
	} {
		if rc, stdout, _ := run(t, p.Root, env, c.args...); rc != 1 || strings.TrimSpace(stdout) != c.last {
			t.Errorf("no config: obor %v: exit %d, %q", c.args, rc, stdout)
		}
	}
	p.Config()
	a := p.Herdr().Agent("w1:p1", "pj-l1", "idle", o1)
	a.Label = "pj-l1"
	env = append(env, "HERDR_SOCKET_PATH="+herdrtest.Serve(t, p.Herdr()).Path)
	for _, c := range []struct {
		prep func()
		args []string
		rc   int
		last string
	}{
		{nil, []string{"dispatch"}, 1, "FAIL " + help.Usage("dispatch")},
		{nil, []string{"dispatch", "L-01", "T-05"}, 1, "FAIL " + help.Usage("dispatch")},
		{nil, []string{"dispatch", "T-05", "x"}, 1, "FAIL " + help.Usage("dispatch")},
		{nil, []string{"dispatch", "T-05", "--agent"}, 1, "FAIL " + help.Usage("dispatch")},
		{nil, []string{"dispatch", "T-05", "--bogus"}, 1, "FAIL " + help.Usage("dispatch")},
		{nil, []string{"drop", "T-01"}, 1, `FAIL usage: obor drop <T-xx> "<why>" [--discard] [--force]`},
		{nil, []string{"drop", "T-01", "why", "--bogus"}, 1, `FAIL usage: obor drop <T-xx> "<why>" [--discard] [--force]`},
		{nil, []string{"drop", "L-01", "why"}, 1, "FAIL L-01 is no task id (task_id_pattern T-[0-9]+[a-z]?)"}, // the lane form is gone (T-775)
		// A drop that passes these checks is a request to obor serve: TestDropServeRequest.
	} {
		if c.prep != nil {
			c.prep()
		}
		rc, stdout, stderr := run(t, p.Root, env, c.args...)
		if rc != c.rc || lastLine(stdout) != c.last || stderr != "" {
			t.Errorf("obor %v: exit %d (want %d)\n%s---\n%s", c.args, rc, c.rc, stdout, stderr)
		}
	}
}

// T-1081 (DD-v0.32 #1): a key this obor does not know never stops a command. A config still naming the promotion's
// keys (T-1076), a v0.29 removed key and T-408's dispatch.serve loads for obor dispatch and whoami (the load() path),
// with one warning per key on stderr; obor config validate alone FAILs, naming each, and exits 1.
func TestUnknownKeysStopNoCommand(t *testing.T) {
	t.Parallel()
	keys := []string{"branches.promote", "merge.promote_weight", "merge.weights", "merge.receipt_max_age", "dispatch"}
	p := servedProject(t, testkit.Set("branches.promote", "release"), testkit.Set("merge.promote_weight", 2),
		testkit.Set("merge.weights", map[string]any{"core": 3}), testkit.Set("merge.receipt_max_age", 60),
		testkit.Set("dispatch.serve", false))
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → L-01 (queued)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	warned := func(cmd, stderr string) {
		t.Helper()
		if n := strings.Count(stderr, "ignored: this obor does not know it"); n != len(keys) {
			t.Errorf("%s: %d warnings, want one per key (%d):\n%s", cmd, n, len(keys), stderr)
		}
		for _, k := range keys {
			if !strings.Contains(stderr, `unknown key "`+k+`" ignored`) {
				t.Errorf("%s: no warning naming %s in:\n%s", cmd, k, stderr)
			}
		}
	}
	rc, stdout, stderr := run(t, p.Root, env, "config", "validate")
	if rc != 1 || stderr != "" {
		t.Errorf("validate: exit %d, stderr %q; want exit 1, nothing on stderr", rc, stderr)
	}
	for _, k := range keys {
		if want := "FAIL ." + k + ": unknown to this obor (retired, or newer than this binary): delete it (.obor/config.json)\n"; !strings.Contains(stdout, want) {
			t.Errorf("validate: no %q in:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "OK") {
		t.Errorf("validate printed OK:\n%s", stdout)
	}
	rc, stdout, stderr = run(t, p.Root, env, "dispatch", "T-07")
	if rc != 0 || stdout != "DISPATCHED T-07 → L-01 (queued)\n" {
		t.Errorf("dispatch: exit %d, %q (%s); want serve's reply", rc, stdout, stderr)
	}
	warned("dispatch", stderr)
	if calls, _ := serve.requests(); len(calls) != 1 || calls[0].Task != "T-07" {
		t.Errorf("serve's requests %+v, want the dispatch of T-07", calls)
	}
	rc, stdout, stderr = run(t, p.Root, env, "whoami")
	if rc != 0 || !strings.Contains(stdout, "role: lead\n") {
		t.Errorf("whoami: exit %d, %q (%s)", rc, stdout, stderr)
	}
	warned("whoami", stderr)
}
