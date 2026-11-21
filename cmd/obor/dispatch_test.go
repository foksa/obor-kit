package main

import (
	"reflect"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
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
		// dispatch's arguments: TestDispatchArguments; drop's usage rows: TestDropUsage
		{nil, []string{"drop", "L-01", "why"}, 1, "FAIL L-01 is no task id (task_id_pattern T-[0-9]+[a-z]?)"}, // a lane is not a task id
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

// obor dispatch's arguments as the request's fields: the level form, --model, --after and --force in any order; every
// other shape is the usage, a task id outside task_id_pattern is its own line. (The binary's row: TestDispatchServeRequest.)
func TestDispatchArguments(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Data: config.Data{Project: config.Project{TaskIDPattern: "T-[0-9]+[a-z]?"}}}
	for _, c := range []struct {
		name string
		args []string
		want wire.DispatchReq // the fields the arguments set
	}{
		{"level", []string{"T-07"}, wire.DispatchReq{Task: "T-07"}},
		{"level, force", []string{"T-07", "--force"}, wire.DispatchReq{Task: "T-07", Force: true}},
		{"models", []string{"T-07", "--model", "sonnet,opus"}, wire.DispatchReq{Task: "T-07", Types: []string{"sonnet", "opus"}}},
		{"models, force first", []string{"--force", "--model", "opus", "T-07"},
			wire.DispatchReq{Task: "T-07", Types: []string{"opus"}, Force: true}},
		{"level, after", []string{"T-07", "--after", "T-05,T-06"}, wire.DispatchReq{Task: "T-07", After: []string{"T-05", "T-06"}}},
		{"models, after first", []string{"--after", "T-05", "T-07", "--model", "opus"},
			wire.DispatchReq{Task: "T-07", Types: []string{"opus"}, After: []string{"T-05"}}},
	} {
		var got wire.DispatchReq
		if err := dispatchArgs(c.args, cfg, &got); err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	usage := help.Usage("dispatch")
	for _, c := range []struct {
		name string
		args []string
		err  string
	}{
		{"no task", nil, usage},
		{"agent", []string{"T-07", "--agent", "x"}, usage},
		{"agent without a value", []string{"T-05", "--agent"}, usage},
		{"no-prompt", []string{"T-07", "--no-prompt"}, usage},
		{"strict", []string{"T-07", "--strict"}, usage},
		{"fresh", []string{"T-07", "--fresh"}, usage},
		{"lane form (gone)", []string{"L-01", "T-07"}, usage},
		{"lane form with models", []string{"L-01", "T-07", "--model", "opus"}, usage},
		{"models without a task", []string{"--model", "opus"}, usage},
		{"empty model", []string{"T-07", "--model", "opus,"}, usage},
		{"models without a value", []string{"T-07", "--model"}, usage},
		{"two tasks", []string{"T-07", "T-08"}, usage},
		{"a word after the task", []string{"T-05", "x"}, usage},
		{"unknown flag", []string{"T-07", "--bogus"}, usage},
		{"after without a value", []string{"T-07", "--after"}, usage},
		{"empty after", []string{"T-07", "--after", "T-05,"}, usage},
		{"after twice", []string{"T-07", "--after", "T-05", "--after", "T-06"}, usage},
		{"bad task id", []string{"X-1"}, "X-1 is no task id (task_id_pattern T-[0-9]+[a-z]?)"},
		{"bad task id in --after", []string{"T-07", "--after", "T-05,X-2"}, "X-2 is no task id (task_id_pattern T-[0-9]+[a-z]?)"},
	} {
		if err := dispatchArgs(c.args, cfg, &wire.DispatchReq{}); err == nil || err.Error() != c.err {
			t.Errorf("%s: %v; want %q", c.name, err, c.err)
		}
	}
}

// obor drop's usage rows end before the command reads its directory: the rows call dropCmd.
func TestDropUsage(t *testing.T) {
	usage := "FAIL usage: obor drop <T-xx> \"<why>\" [--discard] [--force]\n"
	for _, args := range [][]string{{"T-01"}, {"T-01", "why", "--bogus"}} {
		if rc, stdout := callCmd(t, dropCmd, args...); rc != 1 || stdout != usage {
			t.Errorf("drop %q: exit %d, %q; want 1 and %q", args, rc, stdout, usage)
		}
	}
}

// a key this obor does not know never stops a command. A config still naming the promotion's
// keys, a v0.29 removed key and the dispatch.serve key load for obor dispatch and whoami (the load() path),
// with one warning per key on stderr; obor config validate alone FAILs, naming each, and exits 1.
func TestUnknownKeysStopNoCommand(t *testing.T) {
	t.Parallel()
	keys := []string{"branches", // the shape-1 branches left with promote alone
		"merge.promote_weight", "merge.weights", "merge.receipt_max_age", "dispatch"}
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
