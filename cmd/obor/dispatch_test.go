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

// T-408, removed by v0.10.0 and unknown since v0.12: a config that still sets dispatch.serve does not load, so obor
// config validate FAILs on it and obor dispatch asks serve nothing.
func TestDispatchObsoleteSwitch(t *testing.T) {
	t.Parallel()
	p := servedProject(t, testkit.Set("dispatch.serve", false))
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → L-01 (queued)", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	if rc, _, stderr := run(t, p.Root, env, "config", "validate"); rc != 1 || !strings.Contains(stderr, `unknown field "dispatch"`) {
		t.Errorf("validate: exit %d, stderr %q; want an unknown field FAIL", rc, stderr)
	}
	if rc, _, _ := run(t, p.Root, env, "dispatch", "T-07"); rc == 0 {
		t.Errorf("dispatch: exit 0 with an unloadable config")
	}
	if calls, _ := serve.requests(); len(calls) != 0 {
		t.Errorf("serve's requests %+v, want none", calls)
	}
}
