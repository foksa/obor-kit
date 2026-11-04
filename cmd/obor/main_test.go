package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// oborBin is the binary under test, built once by TestMain.
var oborBin string

func TestMain(m *testing.M) {
	testkit.Isolate()
	dir, err := os.MkdirTemp("", "obor-bin")
	if err != nil {
		panic(err)
	}
	oborBin = filepath.Join(dir, "obor")
	if out, err := exec.Command("go", "build", "-o", oborBin, ".").CombinedOutput(); err != nil {
		panic("go build: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func build(t *testing.T) string { return oborBin }

// callDown is the environment of a call to a serve that is not running: every deadline is 0.2 s, so the call's
// retries end at once (T-685), not after the deadline table's 15 s.
const callDown = "OBOR_CALL_DEADLINE=0.2"

// run is the binary in dir with env added: its exit code, stdout and stderr. The command is killed after 60 s (and
// WaitDelay, in case a grandchild holds the pipes) so a hang names the command instead of costing go test's timeout.
func run(t *testing.T, dir string, env []string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, oborBin, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("obor %q hung > 60s in %s; stderr so far: %q", args, dir, stderr.String())
	}
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, stdout.String(), stderr.String()
}

func TestExitCodes(t *testing.T) {
	bin := build(t)
	for _, c := range []struct {
		args []string
		rc   int
	}{{nil, 2}, {[]string{"no-such"}, 2}, {[]string{"help"}, 0}, {[]string{"hook", "noop"}, 0}, {[]string{"hook"}, 2}} {
		err := exec.Command(bin, c.args...).Run()
		rc := 0
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		}
		if rc != c.rc {
			t.Errorf("obor %v: exit %d, want %d", c.args, rc, c.rc)
		}
	}
	// Outside a git repository: exit 3 (hooks and callers treat it as "no obor project").
	cmd := exec.Command(bin, "whoami")
	cmd.Dir = t.TempDir()
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 3 {
		t.Errorf("whoami outside a repo: %v, want exit 3", err)
	}
}

// `obor … | head` with the reader gone: the buffered writer's flush gets EPIPE → exit 0, nothing on stderr.
func TestClosedStdoutExitsZeroQuietly(t *testing.T) {
	bin := build(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	var stderr bytes.Buffer
	cmd := exec.Command(bin, "help")
	cmd.Stdout, cmd.Stderr = w, &stderr
	err = cmd.Run()
	w.Close()
	if err != nil || stderr.Len() != 0 {
		t.Errorf("closed stdout: %v, stderr %q; want exit 0 and no stderr", err, stderr.String())
	}
}

func TestVersionPrintsBuildAndPaths(t *testing.T) {
	bin := build(t)
	// T-554: a kit's own dist/obor is self-describing (no "dev build" word); a fetched release names the kit and source.
	for _, c := range []struct{ source, want string }{
		{"pinned", "obor dev (" + bin + ", kit /kit, pinned)\n"},
		{"dev build", "obor dev (" + bin + ")\n"},
	} {
		cmd := exec.Command(bin, "version")
		cmd.Env = append(os.Environ(), "OBOR_PLUGIN_ROOT=/kit", "OBOR_VERSION_SOURCE="+c.source)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("obor version (source %q): %v", c.source, err)
		}
		if string(out) != c.want {
			t.Errorf("obor version (source %q) = %q, want %q", c.source, out, c.want)
		}
	}
}

// T-195: `obor has` is the capability check hooks and the shim run before trusting a binary: 0 implemented, 1 not,
// never any output.
func TestHas(t *testing.T) {
	bin := build(t)
	for _, c := range []struct {
		args []string
		rc   int
	}{
		{[]string{"has", "hook", "pre-tool-use"}, 0}, {[]string{"has", "hook", "noop"}, 0}, {[]string{"has", "hook"}, 0},
		{[]string{"has", "hook", "stop"}, 0}, {[]string{"has", "hook", "session-start"}, 0},
		{[]string{"has", "hook", "user-prompt-submit"}, 0}, {[]string{"has", "hook", "stop-failure"}, 0},
		{[]string{"has", "dispatch"}, 0}, {[]string{"has", "drop"}, 0}, {[]string{"has", "sendback"}, 1},
		{[]string{"has", "status"}, 0}, {[]string{"has", "restart"}, 0}, {[]string{"has", "version"}, 0}, {[]string{"has", "config", "get"}, 0},
		{[]string{"has", "send"}, 0}, {[]string{"has", "brief"}, 0}, {[]string{"has", "tell"}, 1}, {[]string{"has", "inbox"}, 1},
		{[]string{"has", "flakes"}, 0},
		{[]string{"has", "monitor"}, 0}, {[]string{"has", "setup", "monitor"}, 1}, // T-776: the command stays, the setup step is gone
		{[]string{"has", "setup", "migrate"}, 1}, // T-789: the one-time migration code lived one release
		{[]string{"has", "merge"}, 1},            // T-409: obor serve merges
		{[]string{"has", "hook", "no-such"}, 1}, {[]string{"has", "no-such"}, 1}, {[]string{"has", "status", "x"}, 1},
		{[]string{"has"}, 2}, {[]string{"has", "hook", "noop", "x"}, 2},
	} {
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, c.args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		rc := 0
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		}
		if rc != c.rc || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Errorf("obor %v: exit %d, stdout %q, stderr %q; want exit %d, no output", c.args, rc, stdout.String(),
				stderr.String(), c.rc)
		}
	}
}

// T-409: obor merge is gone (obor serve's merge loop is the only merger): an unknown subcommand, exit 2, nothing
// merged, the lane's work left as it was.
func TestMergeIsUnknown(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Write(filepath.Join(lane, "feature.txt"), "feature\n")
	head := p.Git(p.Root, "rev-parse", "HEAD")
	for _, args := range [][]string{{"merge", "L-01", "T-01", "summary"}, {"merge", "--ff-retry", "T-01"}} {
		rc, stdout, stderr := run(t, p.Root, nil, args...)
		if rc != 2 || stdout != "" || !strings.Contains(stderr, "obor: unknown subcommand: merge") {
			t.Errorf("obor %q: exit %d, %q, %q; want 2, unknown subcommand", args, rc, stdout, stderr)
		}
	}
	if got := p.Git(p.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("main moved: %s, want %s", got, head)
	}
	if st := p.Git(lane, "status", "--porcelain"); st != "?? feature.txt" {
		t.Errorf("lane status %q, want the uncommitted feature only", st)
	}
}

// T-776: obor monitor is removed (obor status and obor web remain): one line naming them, exit 1, and nothing else
// (it reads no project, so it answers outside one too).
func TestMonitorIsRemoved(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"monitor"}, {"monitor", "--json"}} {
		rc, stdout, stderr := run(t, t.TempDir(), nil, args...)
		if rc != 1 || stdout != "" || strings.Count(stderr, "\n") != 1 ||
			!strings.Contains(stderr, "removed, use `obor web` or `obor status`") {
			t.Errorf("obor %q: exit %d, stdout %q, stderr %q; want exit 1 and one line naming obor web or obor status",
				args, rc, stdout, stderr)
		}
	}
}

// bash status case_no_config_and_usage, the --lead flag of case_two_leads_filter and case_one_lead_unchanged.
func TestStatusExitCodesAndLeadFlag(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	if rc, _, stderr := run(t, p.Root, nil, "status"); rc != 3 || !strings.Contains(stderr, "not an obor project") {
		t.Errorf("no config: exit %d, %q; want 3, not an obor project", rc, stderr)
	}
	p.Config()
	if rc, _, stderr := run(t, p.Root, nil, "status", "--bogus"); rc != 2 || !strings.Contains(stderr, "usage: obor status [--facts] [--json] [--short]") {
		t.Errorf("obor status --bogus: exit %d, %q; want 2, the usage", rc, stderr)
	}
	for _, gone := range []string{"--herdr", "--lead"} {
		if rc, _, _ := run(t, p.Root, nil, "status", gone); rc != 2 {
			t.Errorf("obor status %s: exit %d; want 2 (removed, T-463)", gone, rc)
		}
	}
}

// The PreToolUse hook's deny: exit 2 and one line "obor: <reason>" on stderr, nothing on stdout; allow: exit 0, silent.
func TestPreToolUseDenyIsOneOborLine(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	hook := func(cmd string) (int, string, string) {
		in, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": p.Root, "tool_input": map[string]string{"command": cmd}})
		c := exec.Command(oborBin, "hook", "pre-tool-use")
		c.Stdin = bytes.NewReader(in)
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		rc := 0
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		}
		return rc, stdout.String(), stderr.String()
	}
	if rc, stdout, stderr := hook("git push"); rc != 2 || stdout != "" || !strings.HasPrefix(stderr, "obor: git push is blocked") ||
		strings.Count(stderr, "\n") != 1 {
		t.Errorf("deny: exit %d, %q %q", rc, stdout, stderr)
	}
	if rc, stdout, stderr := hook("git status"); rc != 0 || stdout+stderr != "" {
		t.Errorf("allow: exit %d, %q %q", rc, stdout, stderr)
	}
}

// obor restart (internal/launch has the behaviour): the result line last, FAIL → exit 1 (a --stop that gives up
// prints its evidence first).
func TestRestartCommand(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "")
	p.Config()
	f := p.Herdr()
	f.Shell("w1:p2", "pj-int", p.Root)
	a := f.Agent("w1:p1", "pj-l-01", "idle", o1)
	a.Label = "pj-l-01"
	// The agent's group leader is a live pid (this test's own), and herdr lists no process in its group (RV-24 #1:
	// an empty list proves no exit), so the stop still times out.
	a.Foreground, a.OnQuit = os.Getpid(), "stay" // the live fake would quit on /exit: this agent ignores it
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + p.Base + "/state", "OBOR_RESTART_WAIT=1"}
	for _, c := range []struct {
		args []string
		rc   int
		last string
	}{
		{[]string{"restart"}, 1, "FAIL " + restartUsage},
		{[]string{"restart", "a", "b"}, 1, "FAIL " + restartUsage},
		{[]string{"restart", "pj-l-01", "--stop", "--fresh"}, 1, "FAIL --stop takes no --fresh"},
		// T-466: --keep-inbox went with the inbox: an unknown flag.
		{[]string{"restart", "pj-l-01", "--fresh", "--keep-inbox"}, 1, "FAIL " + restartUsage},
		// T-785: the flags restart gave up are refused by name, with where their choice went, before anything is read.
		{[]string{"restart", "pj-l-01", "--prompt", "go"}, 1, `FAIL --prompt is gone: obor brief <agent> --fresh "<text>"`},
		{[]string{"restart", "pj-l-01", "--type", "luna"}, 1, "FAIL " + restartGone["--type"]},
		{[]string{"restart", "pj-l-01", "--effort", "high"}, 1, "FAIL " + restartGone["--effort"]},
		{[]string{"restart", "pj-l-01", "--sid", "sid-9"}, 1, "FAIL " + restartGone["--sid"]},
		{[]string{"restart", "pj-l-01", "--type=luna"}, 1, "FAIL " + restartGone["--type"]},
		{[]string{"restart", "nobody"}, 1, "FAIL unknown agent nobody (no lane, role, agents entry or recorded session)"},
		{[]string{"restart", "pj-int", "--stop"}, 0, "STOPPED pj-int (pane w1:p2, already at a shell)"},
	} {
		rc, stdout, stderr := run(t, p.Root, env, c.args...)
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if rc != c.rc || lines[len(lines)-1] != c.last || stderr != "" {
			t.Errorf("obor %v: exit %d (want %d)\n%s---\n%s", c.args, rc, c.rc, stdout, stderr)
		}
	}
	// A direct stop that gives up prints its evidence, then the one-line FAIL last (T-213).
	rc, stdout, _ := run(t, p.Root, env, "restart", "pj-l-01", "--stop")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 1 || lines[len(lines)-1] != "FAIL pj-l-01 did not exit in 1s (pane w1:p1; the input was cleared; --force terminates it)" ||
		!strings.Contains(stdout, "agent status:") || !strings.Contains(stdout, "pane processes:") ||
		strings.Index(stdout, "agent status:") > strings.Index(stdout, "FAIL ") {
		t.Errorf("obor restart --stop that cannot stop: evidence, then FAIL last: exit %d\n%s", rc, stdout)
	}
	var typed, started bool
	for _, r := range srv.Requests() {
		typed = typed || r.Method == "pane.send_input" && strings.Contains(string(r.Params), `"pane_id":"w1:p1"`) &&
			strings.Contains(string(r.Params), `"text":"/exit"`)
		started = started || r.Method == "agent.start"
	}
	if !typed || started {
		t.Errorf("--stop typed /exit in the worker's pane, started nothing: typed %v, started %v: %+v", typed, started, srv.Requests())
	}
}

// runIn is run with stdin, bounded the same way (T-291).
func runIn(t *testing.T, dir string, env []string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, oborBin, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = dir, &stdout, &stderr, strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("obor %q hung > 60s in %s; stderr so far: %q", args, dir, stderr.String())
	}
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, stdout.String(), stderr.String()
}

// obor dispatch / drop (obor serve has drop's and dispatch's behaviour):
// argument parsing, no config, the result line last with its exit code. bash dispatch.test.sh
// case_unknown_lane_and_bad_id (usage); bash drop.test.sh case_unknown_lane (usage).
func TestLaneCommands(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
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
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "T-01: merged")
	a := p.Herdr().Agent("w1:p1", "pj-l1", "idle", o1)
	a.Label = "pj-l1"
	env = append(env, "HERDR_SOCKET_PATH="+herdrtest.Serve(t, p.Herdr()).Path)
	for _, c := range []struct {
		prep func()
		args []string
		rc   int
		last string
	}{
		{nil, []string{"dispatch"}, 1, "FAIL " + dispatchUsage},
		{nil, []string{"dispatch", "L-01", "T-05"}, 1, "FAIL " + dispatchUsage},
		{nil, []string{"dispatch", "T-05", "x"}, 1, "FAIL " + dispatchUsage},
		{nil, []string{"dispatch", "T-05", "--agent"}, 1, "FAIL " + dispatchUsage},
		{nil, []string{"dispatch", "T-05", "--bogus"}, 1, "FAIL " + dispatchUsage},
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

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
