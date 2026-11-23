package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// oborBin is the binary under test, built once by TestMain.
var oborBin string

// afterRun are the cleanups of fixtures a test file builds once per binary (set before the tests run or under their
// own sync.Once, run by TestMain after the last test).
var afterRun []func()

func TestMain(m *testing.M) {
	testkit.Isolate()
	dir, err := os.MkdirTemp("", "obor-bin")
	if err != nil {
		panic(err)
	}
	oborBin = filepath.Join(dir, "obor")
	// -buildvcs=false: VCS stamping runs git, and the unit tier starts none.
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", oborBin, ".").CombinedOutput(); err != nil {
		panic("go build: " + err.Error() + "\n" + string(out))
	}
	code := testkit.Run(m) // removes the project template testkit.New copies
	for _, f := range afterRun {
		f()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

func build(t *testing.T) string { return oborBin }

// waitFor polls cond for up to a minute. Detached commands and herdr over a socket are slow on a loaded machine: the
// gate's parallel packages took one past 15 s (a budget breach).
func waitFor(cond func() bool) bool {
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// callDown is the environment of a call to a serve that is not running: every deadline is 0.2 s, so the call's
// retries end at once, not after the deadline table's 15 s.
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
	// Outside a git repository: TestExitCodesOutsideARepo (main_integration_test.go: project.Find asks git there).
}

// `obor kind agy hook` runs agy's hook transport in this obor, no kind binary beside it: agy's event of a tool obor
// cannot guard reaches obor hook pre-tool-use in process, whose guard denies it for an agent with a role, with the
// transport's reason. An unknown kind exits 2.
func TestKindRunsTheKitKindInProcess(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	if _, err := os.Stat(filepath.Join(filepath.Dir(oborBin), "obor-kind-agy")); !os.IsNotExist(err) {
		t.Fatalf("precondition: no kind binary of that name beside obor (%v)", err)
	}
	event := `{"hookEventName": "PreToolUse", "conversationId": "c1", "cwd": "` + p.Root + `", ` +
		`"toolCall": {"name": "run_workflow", "args": {}}}` + "\n"
	rc, stdout, stderr := runIn(t, p.Root, []string{"OBOR_ROLE=lead"}, event, "kind", "agy", "hook")
	var got struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || rc != 0 {
		t.Fatalf("obor kind agy hook: exit %d, %q (%v), stderr %q", rc, stdout, err, stderr)
	}
	if got.Decision != "deny" || !strings.Contains(got.Reason, "obor cannot guard a workflow script") {
		t.Errorf("the answer = %+v; want obor hook's deny with the transport's reason (run on this obor)", got)
	}
	if rc, _, stderr := run(t, p.Root, nil, "kind", "nope", "hook"); rc != 2 || !strings.Contains(stderr, `"nope" is no kit kind (agy, opencode)`) {
		t.Errorf("obor kind nope: exit %d, %q; want 2 naming the kit kinds", rc, stderr)
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
	// a kit's own dist/obor is self-describing (no "dev build" word); a fetched release names the kit and source.
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

// `obor has` is the capability check hooks and the shim run before trusting a binary: 0 implemented, 1 not,
// 2 a usage error, never any output. The table calls has; the binary runs one row per exit code (main's `has` case
// passes the arguments and the code through).
func TestHas(t *testing.T) {
	for _, c := range []struct {
		args []string
		rc   int
	}{
		{[]string{"hook", "pre-tool-use"}, 0}, {[]string{"hook", "noop"}, 0}, {[]string{"hook"}, 0},
		{[]string{"hook", "stop"}, 0}, {[]string{"hook", "session-start"}, 0},
		{[]string{"hook", "user-prompt-submit"}, 0}, {[]string{"hook", "stop-failure"}, 0},
		{[]string{"dispatch"}, 0}, {[]string{"drop"}, 0}, {[]string{"sendback"}, 1},
		{[]string{"status"}, 0}, {[]string{"restart"}, 0}, {[]string{"version"}, 0}, {[]string{"config", "get"}, 0},
		{[]string{"send"}, 0}, {[]string{"brief"}, 0}, {[]string{"tell"}, 1}, {[]string{"inbox"}, 1},
		{[]string{"flakes"}, 0},
		{[]string{"monitor"}, 1}, {[]string{"setup", "monitor"}, 1}, // obor monitor is no subcommand; the setup step is unknown too
		{[]string{"plugin"}, 1}, {[]string{"plugin", "restore"}, 1}, // the herdr plugin's command is no subcommand
		{[]string{"setup", "migrate"}, 1}, // no migration step
		{[]string{"merge"}, 1},            // obor serve merges
		{[]string{"hook", "no-such"}, 1}, {[]string{"no-such"}, 1}, {[]string{"status", "x"}, 1},
		{nil, 2}, {[]string{"hook", "noop", "x"}, 2},
	} {
		if rc := has(c.args); rc != c.rc {
			t.Errorf("has %v = %d, want %d", c.args, rc, c.rc)
		}
	}
	for _, c := range []struct {
		args []string
		rc   int
	}{{[]string{"has", "hook", "noop"}, 0}, {[]string{"has", "sendback"}, 1}, {[]string{"has"}, 2}} {
		rc, stdout, stderr := run(t, t.TempDir(), nil, c.args...)
		if rc != c.rc || stdout != "" || stderr != "" {
			t.Errorf("obor %v: exit %d, stdout %q, stderr %q; want exit %d, no output", c.args, rc, stdout, stderr, c.rc)
		}
	}
}

// callCmd runs a command's function in this process with out captured: its exit code and what it wrote. For the rows
// of a command's argument check (usage, a refused flag), which end before the command reads its directory or
// environment. It swaps the package's one writer, so the test calling it is not parallel (a parallel test resumes
// only after the sequential ones end).
func callCmd(t *testing.T, cmd func([]string) int, args ...string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	outMu.Lock()
	prev := out
	out = bufio.NewWriter(&buf)
	outMu.Unlock()
	defer func() {
		outMu.Lock()
		out = prev
		outMu.Unlock()
	}()
	rc := cmd(args)
	outMu.Lock()
	defer outMu.Unlock()
	if err := out.Flush(); err != nil {
		t.Fatal(err)
	}
	return rc, buf.String()
}

// obor merge is no subcommand (obor serve's merge loop is the only merger): main does not dispatch it and the capability
// check says so; the unknown-subcommand line and its exit code are TestExitCodes' and TestMonitorIsUnknown's, so there
// is nothing here to merge.
func TestMergeIsUnknown(t *testing.T) {
	if slices.Contains(dispatched(t), "merge") {
		t.Error("obor merge is dispatched; want no subcommand")
	}
	if rc := has([]string{"merge"}); rc != 1 {
		t.Errorf("obor has merge: exit %d, want 1", rc)
	}
}

// obor monitor is an unknown subcommand (obor status and obor web remain): exit 2 and the same error every
// command this binary lacks gets.
func TestMonitorIsUnknown(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"monitor"}, {"monitor", "--json"}} {
		rc, stdout, stderr := run(t, t.TempDir(), nil, args...)
		if rc != 2 || stdout != "" || stderr != "obor: unknown subcommand: monitor\n" {
			t.Errorf("obor %q: exit %d, stdout %q, stderr %q; want exit 2, no stdout and the unknown subcommand there",
				args, rc, stdout, stderr)
		}
	}
}

// The PreToolUse hook's deny: exit 2 and one line "obor: <reason>" on stderr, nothing on stdout; allow: exit 0, silent.
func TestPreToolUseDenyIsOneOborLine(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
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

// runIn is run with stdin, bounded the same way.
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

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// dispatched is the subcommands main's switch on sub dispatches (its case strings; -h and --help are help's aliases).
func dispatched(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "sub" {
			return true
		}
		for _, st := range sw.Body.List {
			for _, e := range st.(*ast.CaseClause).List {
				lit, ok := e.(*ast.BasicLit)
				if !ok {
					t.Fatalf("main.go: a case of the switch on sub is not a string literal: %T", e)
				}
				s, _ := strconv.Unquote(lit.Value)
				if !strings.HasPrefix(s, "-") {
					names = append(names, s)
				}
			}
		}
		return false
	})
	return names
}

// every subcommand main dispatches has a row in help.Commands and every row is dispatched: a removed command
// cannot stay listed, a new one cannot be missing. Each row's group is one of help.Groups and its usage names it.
func TestHelpTableMatchesDispatch(t *testing.T) {
	t.Parallel()
	got := dispatched(t)
	for _, want := range []string{"help", "whoami", "hook", "version"} {
		if !slices.Contains(got, want) {
			t.Fatalf("precondition: main.go's dispatch %v lacks %q", got, want)
		}
	}
	rows := map[string]bool{}
	for _, c := range help.Commands {
		if rows[c.Name] {
			t.Errorf("help.Commands lists %q twice", c.Name)
		}
		rows[c.Name] = true
		if !slices.Contains(got, c.Name) {
			t.Errorf("help.Commands lists %q, which main.go does not dispatch", c.Name)
		}
		if !slices.Contains(help.Groups, c.Group) {
			t.Errorf("%s: group %q is not one of %v", c.Name, c.Group, help.Groups)
		}
		if !strings.HasPrefix(c.Usage, "obor "+c.Name) || c.Desc == "" {
			t.Errorf("%s: usage %q (want it to start with obor %s), desc %q", c.Name, c.Usage, c.Name, c.Desc)
		}
	}
	for _, name := range got {
		if !rows[name] {
			t.Errorf("main.go dispatches %q, which help.Commands does not list", name)
		}
	}
}

// one usage source. Every command row and step resolves through help.Usage to a line that names it, and no
// cmd/obor file spells a usage line out: a new one would drift from the table obor help prints.
func TestUsageLinesComeFromHelp(t *testing.T) {
	t.Parallel()
	for _, c := range help.Commands {
		got := help.Usage(c.Name)
		if got != "usage: "+c.Usage || !strings.HasPrefix(got, "usage: obor "+c.Name) {
			t.Errorf("%s: help.Usage = %q, want usage: %s", c.Name, got, c.Usage)
		}
	}
	for name := range help.Steps {
		cmd, _, _ := strings.Cut(name, " ")
		if !slices.ContainsFunc(help.Commands, func(c help.Command) bool { return c.Name == cmd }) {
			t.Errorf("help.Steps names %q, whose command is not in help.Commands", name)
		}
		if got := help.Usage(name); !strings.HasPrefix(got, "usage: obor "+name) {
			t.Errorf("%s: help.Usage = %q", name, got)
		}
	}
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, n)
		}
	}
	if len(files) < 10 {
		t.Fatalf("precondition: %d cmd/obor files scanned, want its source files", len(files))
	}
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(s, "usage: obor") {
				t.Errorf("%s: a usage line is typed here, not help.Usage: %q", name, s)
			}
			return true
		})
	}
}

// `obor help` (the binary) prints every group and every row, its usage line and its description, exit 0.
func TestHelpPrintsEveryGroupAndRow(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"help", "--help"} {
		rc, stdout, _ := run(t, t.TempDir(), nil, arg)
		if rc != 0 || !strings.HasPrefix(stdout, "usage: obor <subcommand>") {
			t.Fatalf("obor %s: exit %d, %q", arg, rc, stdout)
		}
		for _, g := range help.Groups {
			if !strings.Contains(stdout, "\n"+g+":\n") {
				t.Errorf("obor %s: no group %q:\n%s", arg, g, stdout)
			}
		}
		for _, c := range help.Commands {
			if !strings.Contains(stdout, c.Usage+"\n") || !strings.Contains(stdout, "  "+c.Name) ||
				!strings.Contains(stdout, c.Desc+"\n") {
				t.Errorf("obor %s: no row %s (%q, %q)", arg, c.Name, c.Usage, c.Desc)
			}
		}
	}
	// gone commands stay gone (they were in the shim's old list)
	_, stdout, _ := run(t, t.TempDir(), nil, "help")
	for _, gone := range []string{"answer", "sendback"} {
		if strings.Contains(stdout, "  "+gone+" ") {
			t.Errorf("obor help lists %q, a removed command", gone)
		}
	}
}

// bashHook is `obor hook pre-tool-use` (the binary) on a Bash call of cmd in dir: its exit code and stderr.
func bashHook(t *testing.T, dir, cmd string) (int, string) {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": dir, "tool_input": map[string]string{"command": cmd}})
	c := exec.Command(oborBin, "hook", "pre-tool-use")
	c.Dir, c.Stdin = dir, bytes.NewReader(in)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	err := c.Run()
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, stderr.String()
}

// The kit's OpenCode is answered in process: with nothing cached the guard applies its manifest's rule (`opencode run`
// denied as headless, another opencode command let through). A project's own plugin kind whose manifest is not
// cached has no headless forms, so the guard denies every command of its CLI (also wrapped) naming obor setup agents;
// after obor serve's start, which caches its manifest (and none for OpenCode), the manifest's rule applies.
func TestGuardDeniesAnUncachedPluginKindUntilServeCachesIt(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "obor-kind-hermes")
	manifest := `{"version":1,"name":"hermes","status":"terminal","headless":{"subcommands":["run"]}}`
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\necho '"+manifest+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("agents.kinds.hermes.plugin", bin))
	kinds := filepath.Join(p.Root, ".obor", "state", "kinds")
	if _, err := os.Stat(kinds); !os.IsNotExist(err) {
		t.Fatalf("precondition: no manifest cached (%v)", err)
	}
	for _, cmd := range []string{"opencode run hello", "env opencode run hello"} {
		if rc, stderr := bashHook(t, p.Root, cmd); rc != 2 || !strings.Contains(stderr, "opencode run runs an agent headless") {
			t.Errorf("in process, %q: exit %d, %q; want the manifest's headless deny", cmd, rc, stderr)
		}
	}
	if rc, stderr := bashHook(t, p.Root, "opencode --version"); rc != 0 {
		t.Errorf("in process, opencode --version: exit %d, %q; want allowed", rc, stderr)
	}
	for _, cmd := range []string{"hermes run hello", "env hermes run hello", "hermes --version"} {
		if rc, stderr := bashHook(t, p.Root, cmd); rc != 2 ||
			!strings.Contains(stderr, "hermes is the CLI of plugin kind hermes, whose manifest is not cached") ||
			!strings.Contains(stderr, "run obor setup agents") {
			t.Errorf("no cache, %q: exit %d, %q; want the uncached deny", cmd, rc, stderr)
		}
	}

	srv := herdrtest.Serve(t, herdrtest.New())
	cmd, out := startServe(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(out.String(), "obor serve: running") {
			break
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	b, err := os.ReadFile(filepath.Join(kinds, "hermes.json"))
	if err != nil || !strings.Contains(out.String(), "obor serve: kind hermes: manifest of "+bin+" cached") {
		t.Fatalf("serve's start cached no manifest (%v):\n%s", err, out.String())
	}
	var c struct{ Command string }
	if json.Unmarshal(b, &c) != nil || c.Command != bin {
		t.Errorf("the cache describes %q, want %s", c.Command, bin)
	}
	if _, err := os.Stat(filepath.Join(kinds, "opencode.json")); !os.IsNotExist(err) {
		t.Errorf("serve cached OpenCode, a kind answered in process (%v)", err)
	}
	for _, cmd := range []string{"hermes run hello", "env hermes run hello"} {
		if rc, stderr := bashHook(t, p.Root, cmd); rc != 2 || !strings.Contains(stderr, "hermes run runs an agent headless") {
			t.Errorf("cached, %q: exit %d, %q; want the manifest's headless deny", cmd, rc, stderr)
		}
	}
	if rc, stderr := bashHook(t, p.Root, "hermes --version"); rc != 0 {
		t.Errorf("cached, hermes --version: exit %d, %q; want allowed", rc, stderr)
	}
}

// obor hook session-start sends serve one session request with the agent, its kind, the session id and its
// transcript_path, the pane, lane and task; serve down: nothing is recorded (no .sid file), the hook says so on stderr
// and still exits 0.
func TestSessionStartHookReports(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	lane := p.LaneDir("L-01")
	state := t.TempDir()
	env := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-l-01", "OBOR_LANE=L-01", "OBOR_STATE_HOME=" + state,
		"OBOR_PANE=w1:p3"}
	// The first call goes to a serve that is not there: with a short deadline it ends at once.
	down := append(append([]string(nil), env...), callDown)
	event, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": lane, "session_id": "sid-77",
		"transcript_path": "/claude/projects/x/sid-77.jsonl", "source": "startup"})

	if _, err := os.Stat(wire.SocketPath(p.Root)); err == nil {
		t.Fatal("precondition: no serve socket")
	}
	rc, _, stderr, _ := shimRun(t, lane, string(event), down, oborBin, "hook", "session-start")
	if rc != 0 {
		t.Errorf("serve down: exit %d, want 0", rc)
	}
	if !strings.Contains(stderr, "session sid-77 of pj-l-01 not recorded") {
		t.Errorf("serve down: stderr %q, want it to say the session is not recorded", stderr)
	}
	if !strings.Contains(stderr, "a restart resumes pj-l-01's last recorded session; obor restart pj-l-01 --fresh starts a new one") {
		t.Errorf("serve down: stderr %q, want what a restart does then", stderr)
	}
	if _, err := os.Stat(filepath.Join(state, "agents", "pj-l-01.sid")); err == nil {
		t.Error("serve down: a .sid file was written")
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "SESSION pj-l-01 sid-77", "")
	if rc, _, _, _ := shimRun(t, lane, string(event), env, oborBin, "hook", "session-start"); rc != 0 {
		t.Errorf("serve up: exit %d, want 0", rc)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 {
		t.Fatalf("serve got %d requests, want one", len(calls))
	}
	c := calls[0]
	if c.Session == nil {
		t.Fatalf("serve got %s %+v: no session", serve.sentMethods()[0], c)
	}
	s := *c.Session
	if serve.sentMethods()[0] != wire.MethodSession || c.Agent != "pj-l-01" || s.Agent != "pj-l-01" || s.Kind != "claude" ||
		s.ID != "sid-77" || s.Transcript != "/claude/projects/x/sid-77.jsonl" || s.Pane != "w1:p3" || s.Lane != "L-01" ||
		c.Root != p.Root || s.Started == 0 || s.Source != "SessionStart hook" {
		t.Errorf("serve got %s %+v (session %+v)", serve.sentMethods()[0], c, s)
	}
}

// obor hook stop-failure (Claude Code's StopFailure: a turn ended on an API error) appends the payload's raw
// error and error_details with a timestamp to .obor/state/stop-failure.log and sends serve one stopped request with
// the agent, its role and lane, the error and its details. Serve down or refusing: exit 0, nothing on stdout, the log
// line written all the same. Outside an obor project: nothing.
func TestStopFailureHook(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	lane := p.LaneDir("L-01")
	logPath := filepath.Join(p.Root, ".obor/state/stop-failure.log")
	env := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-l-01"}
	down := append(append([]string(nil), env...), callDown) // the first call: no serve yet
	event, _ := json.Marshal(map[string]any{"hook_event_name": "StopFailure", "cwd": lane, "error": "rate_limit",
		"error_details": "429 Too Many Requests"})
	logLines := func() []map[string]any {
		t.Helper()
		b, _ := os.ReadFile(logPath)
		var out []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("log line %q: %v", l, err)
			}
			out = append(out, m)
		}
		return out
	}

	if _, err := os.Stat(wire.SocketPath(p.Root)); err == nil {
		t.Fatal("precondition: no serve socket")
	}
	if rc, stdout, _, _ := shimRun(t, lane, string(event), down, oborBin, "hook", "stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("serve down: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	ls := logLines()
	if len(ls) != 1 || ls[0]["error"] != "rate_limit" || ls[0]["error_details"] != "429 Too Many Requests" ||
		ls[0]["agent"] != "pj-l-01" || ls[0]["at"] == nil {
		t.Fatalf("serve down: log %v, want one line with the error, its details, the agent and a time", ls)
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "STOPPED pj-l-01 (needs-you T-01)", "")
	if rc, stdout, _, _ := shimRun(t, lane, string(event), env, oborBin, "hook", "stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("serve up: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 {
		t.Fatalf("serve got %d requests, want one", len(calls))
	}
	c := calls[0]
	if serve.sentMethods()[0] != wire.MethodStopped || c.Agent != "pj-l-01" || c.Role != "worker" || c.Lane != "L-01" ||
		c.Error != "rate_limit" || c.Details != "429 Too Many Requests" || c.CallID == "" || c.Root != p.Root {
		t.Errorf("serve got %s %+v", serve.sentMethods()[0], c)
	}
	if n := len(logLines()); n != 2 {
		t.Errorf("log lines %d, want 2", n)
	}

	// A refusal: still exit 0, nothing on stdout.
	startFakeServe(t, p.Root, wire.Status{}, "", "stopped: no served project")
	if rc, stdout, _, _ := shimRun(t, lane, string(event), env, oborBin, "hook", "stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("serve refusing: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	// Outside an obor project: TestStopFailureHookOutsideAProject (stopfailure_integration_test.go: project.Find asks git).
}

// a worker's UserPromptSubmit and Stop hooks each tell serve its next turn began or ended (one turn request
// with the agent, its role and lane), which ends a stop in force; the hooks' own output is unchanged (the Stop hook
// still blocks with no task_done). A role agent's hooks send the same (its brief's stop ends). Each says its
// phase: the Stop hook's is the turn's end, UserPromptSubmit's its start. SessionStart and the hooks of a lead with a
// live inbox send none; serve down: exit 0 all the same.
func TestTurnHooks(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	lane := p.LaneDir("L-01")
	worker := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-l-01"}
	down := append(append([]string(nil), worker...), callDown) // the first turn: no serve yet
	event := func(name string, cwd ...string) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": name, "cwd": append(cwd, lane)[0],
			"prompt": "[obor job T-01/1/work] go"})
		return string(b)
	}

	if rc, stdout, _, _ := shimRun(t, lane, event("UserPromptSubmit"), down, oborBin, "hook", "user-prompt-submit"); rc != 0 ||
		stdout != "" {
		t.Errorf("serve down: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "TURN pj-l-01 (no stop)", "")
	if rc, stdout, _, _ := shimRun(t, lane, event("UserPromptSubmit"), worker, oborBin, "hook", "user-prompt-submit"); rc != 0 ||
		stdout != "" {
		t.Errorf("user-prompt-submit: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.sentMethods()[0] != wire.MethodTurn || calls[0].Agent != "pj-l-01" || calls[0].Role != "worker" ||
		calls[0].Lane != "L-01" || calls[0].Root != p.Root || calls[0].Ended {
		t.Fatalf("after user-prompt-submit serve got %v %+v, want one turn request", serve.sentMethods(), calls)
	}

	p.Dispatched("L-01") // a task without task_done: the Stop hook blocks, and tells serve all the same
	rc, stdout, _, _ := shimRun(t, lane, event("Stop"), worker, oborBin, "hook", "stop")
	if rc != 0 || !strings.Contains(stdout, `"decision":"block"`) {
		t.Errorf("stop: exit %d, stdout %q; want the block verdict", rc, stdout)
	}
	if calls, _ = serve.requests(); len(calls) != 2 || serve.sentMethods()[1] != wire.MethodTurn || calls[1].Agent != "pj-l-01" ||
		!calls[1].Ended {
		t.Fatalf("after stop serve got %v %+v, want a second turn request, its end", serve.sentMethods(), calls)
	}

	shimRun(t, lane, event("SessionStart"), worker, oborBin, "hook", "session-start")
	sock := filepath.Join(t.TempDir(), "s.sock") // the lead's live inbox (its existence is the check): no lead_lines
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := project.WriteInbox(p.Cfg(), "lead", project.Inbox{Socket: sock}); err != nil {
		t.Fatal(err)
	}
	lead := []string{"OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	shimRun(t, p.Root, event("UserPromptSubmit", p.Root), lead, oborBin, "hook", "user-prompt-submit")
	shimRun(t, p.Root, event("Stop", p.Root), lead, oborBin, "hook", "stop")
	if calls, _ = serve.requests(); len(calls) != 2 {
		t.Fatalf("session-start and the lead's hooks sent %v, want none", serve.sentMethods()[2:])
	}

	reviewer := []string{"OBOR_ROLE=reviewer", "OBOR_AGENT=pj-rev"}
	shimRun(t, p.Root, event("UserPromptSubmit", p.Root), reviewer, oborBin, "hook", "user-prompt-submit")
	shimRun(t, p.Root, event("Stop", p.Root), reviewer, oborBin, "hook", "stop")
	calls, _ = serve.requests()
	if len(calls) != 4 {
		t.Fatalf("the reviewer's hooks sent %v, want two turn requests", serve.sentMethods()[2:])
	}
	for i, c := range calls[2:] {
		if serve.sentMethods()[2+i] != wire.MethodTurn || c.Agent != "pj-rev" || c.Role != "reviewer" || c.Root != p.Root ||
			c.Ended != (i == 1) {
			t.Errorf("the reviewer's hook %d sent %s %+v, want a turn request (the second its end)", i, serve.sentMethods()[2+i], c)
		}
	}
}

// hookKindManifest is the describe reply of the plugin kind hookk: no inbox, its hooks act on the answers.
const hookKindManifest = `{"version":1,"name":"hookk","status":"hooks","caps":{"hooks":"plugin","hook_answers":true}}`

// A lead's UserPromptSubmit and Stop hooks, its kind (a project plugin kind, its manifest cached) with no inbox and
// acting on the hooks' answers, each ask serve for its lines (one lead_lines request, the lead's agent and role) and
// hand what serve answers: as the prompt's context, as the stop's block. Serve down: exit 0, nothing. An OpenCode
// lead's with no inbox recorded (its kind has one, and hook_answers) ask the same, with nothing left to hand.
func TestLeadLinesHooks(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "obor-kind-hookk")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\necho '"+hookKindManifest+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := servedProject(t, testkit.Set("agents.kinds.hookk", map[string]any{"plugin": bin}),
		testkit.Set("agents.models.hooky", map[string]any{"kind": "hookk", "id": "hooky"}),
		testkit.Set("agents.roles.lead", "hooky"))
	if _, err := plugin.Cache("hookk", bin, filepath.Join(p.Root, ".obor", "state")); err != nil {
		t.Fatalf("precondition: the kind's manifest cached: %v", err)
	}
	lead := []string{"OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	event := func(name string) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": name, "cwd": p.Root, "prompt": "what next?"})
		return string(b)
	}
	if rc, stdout, _, _ := shimRun(t, p.Root, event("UserPromptSubmit"), append(lead, callDown), oborBin, "hook",
		"user-prompt-submit"); rc != 0 || stdout != "" {
		t.Errorf("serve down: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	_, serve := startFakeServe(t, p.Root, wire.Status{}, "[obor other] hello", "")
	rc, stdout, _, _ := shimRun(t, p.Root, event("UserPromptSubmit"), lead, oborBin, "hook", "user-prompt-submit")
	if rc != 0 || !strings.Contains(stdout, `"additionalContext":"obor (lead): lines for you`) ||
		!strings.Contains(stdout, `[obor other] hello`) {
		t.Errorf("user-prompt-submit: exit %d, stdout %q; want the lines as context", rc, stdout)
	}
	rc, stdout, _, _ = shimRun(t, p.Root, event("Stop"), lead, oborBin, "hook", "stop")
	if rc != 0 || !strings.Contains(stdout, `"decision":"block"`) || !strings.Contains(stdout, `[obor other] hello`) {
		t.Errorf("stop: exit %d, stdout %q; want the block with the lines", rc, stdout)
	}
	calls, _ := serve.requests()
	if m := serve.sentMethods(); len(calls) != 2 || m[0] != wire.MethodLeadLines || m[1] != wire.MethodLeadLines {
		t.Fatalf("serve got %v, want two lead_lines requests", m)
	}
	for _, c := range calls {
		if c.Agent != "pj-lead" || c.Role != "lead" || c.Root != p.Root || c.CallID == "" {
			t.Errorf("request %+v, want the lead's with a call id", c)
		}
		until := time.UnixMilli(c.UntilMS)
		if now := time.Now(); c.UntilMS == 0 || until.Before(now) || until.After(now.Add(5*time.Second)) {
			t.Errorf("request %+v: until_ms %d, want the instant its call ends, ahead of now", c, c.UntilMS)
		}
	}
	p.Config(testkit.Set("agents.roles.integrator", ""), testkit.Set("agents.roles.lead", "deepseek"))
	// OpenCode, a kit kind, is answered in process: no manifest cached
	shimRun(t, p.Root, event("UserPromptSubmit"), lead, oborBin, "hook", "user-prompt-submit")
	shimRun(t, p.Root, event("Stop"), lead, oborBin, "hook", "stop")
	if m := serve.sentMethods(); len(m) != 4 {
		t.Errorf("an OpenCode lead's hooks sent %v, want two lead_lines requests", m[2:])
	}
}

// SessionStart of a triage session records its session (serve gets pj-triage's) and its project inbox (slot
// triage), so a brief to pj-triage is posted to the session, not typed.
func TestTriageSessionStartRecordsItsInbox(t *testing.T) {
	t.Parallel()
	p := triageProject(t)
	dir, err := os.MkdirTemp("", "tri") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		c.Close()
		got <- string(b)
	}()
	if _, ok := inboxed(p.Root)("pj-triage"); ok {
		t.Fatal("precondition: no inbox record of pj-triage")
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "SESSION pj-triage sid-t1", "")
	env := []string{"OBOR_ROLE=triage", "OBOR_STATE_HOME=" + t.TempDir(), "OBOR_PANE=w1:p9",
		"CLAUDE_CODE_MESSAGING_SOCKET=" + sock, "CLAUDE_CODE_MESSAGING_TOKEN=tok-t"}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": p.Root, "session_id": "sid-t1",
		"source": "startup"})
	rc, stdout, stderr, _ := shimRun(t, p.Root, string(event), env, oborBin, "hook", "session-start")
	if rc != 0 {
		t.Fatalf("session-start: exit %d, %s", rc, stderr)
	}
	if !strings.Contains(stdout, "obor: role triage,") {
		t.Errorf("session-start output names no triage role:\n%s", stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.sentMethods()[0] != wire.MethodSession || calls[0].Agent != "pj-triage" ||
		calls[0].Session == nil || calls[0].Session.ID != "sid-t1" || calls[0].Root != p.Root {
		t.Fatalf("serve got %v %+v; want pj-triage's session sid-t1", serve.sentMethods(), calls)
	}
	if s, ok := inboxed(p.Root)("pj-triage"); !ok || s != "sid-t1" {
		t.Fatalf("inboxed(pj-triage) = %q, %v; want the session's record", s, ok)
	}

	const line = "[obor pj-lead] bug B-03: retest"
	if began, err := (poster{p.Root, testkit.Kinds()}).Post(context.Background(), agents.Default, "pj-triage", line); !began || err != nil {
		t.Fatalf("brief to pj-triage: began %v, %v; want posted to its inbox", began, err)
	}
	select {
	case b := <-got:
		if !strings.Contains(b, `"token":"tok-t"`) || !strings.Contains(b, "bug B-03: retest") {
			t.Errorf("the triage session read %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the triage session read nothing")
	}
	if _, err := project.ReadInbox(p.Cfg(), "triage"); err != nil {
		t.Errorf("inbox slot triage: %v", err)
	}
}

// the stop-failure hook of a triage session names the triage agent and its role to serve.
func TestTriageStopFailureNamesIt(t *testing.T) {
	t.Parallel()
	p := triageProject(t)
	_, serve := startFakeServe(t, p.Root, wire.Status{}, "STOPPED pj-triage", "")
	event, _ := json.Marshal(map[string]any{"hook_event_name": "StopFailure", "cwd": p.Root, "error": "rate_limit"})
	if rc, stdout, _, _ := shimRun(t, p.Root, string(event), []string{"OBOR_ROLE=triage"}, oborBin, "hook",
		"stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.sentMethods()[0] != wire.MethodStopped || calls[0].Agent != "pj-triage" ||
		calls[0].Role != "triage" || calls[0].Error != "rate_limit" {
		t.Errorf("serve got %v %+v; want one stopped request of pj-triage (role triage)", serve.sentMethods(), calls)
	}
}
