package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/testkit"
)

// hangBound: the watchdog fires at 2 s; the rest is headroom for a loaded merge machine.
const hangBound = 15 * time.Second

// The plugin wiring: the JSON manifests, hooks/hooks.json, `claude plugin validate`, and the five hooks run as
// `bin/obor hook <name>` without a usable Go binary (T-195, RV-22 #1, T-201). What the hooks do is the Go binary's
// `obor hook <name>` (internal/guard). These tests start plain processes (no process groups), so they
// run inside a Codex sandbox too; every run is bounded by a context timeout.

// checkoutRoot is this checkout: the kit the tests read and copy (never changed).
func checkoutRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("invalid JSON: %s: %v", path, err)
	}
}

type hooksFile struct {
	Modules []string `json:"modules"`
	Hooks   map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// shimRun runs name args… in dir with env added and stdin, bounded by 20 s: exit code, stdout, stderr (one trailing
// newline cut, like $(…)), and how long it took.
func shimRun(t *testing.T, dir, stdin string, env []string, name string, args ...string) (int, string, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, strings.NewReader(stdin), &stdout, &stderr
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Run()
	took := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("%s %v: timed out after %s", name, args, took)
	}
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return rc, strings.TrimSuffix(stdout.String(), "\n"), strings.TrimSuffix(stderr.String(), "\n"), took
}

// bash plugin.test.sh case_json_valid
func TestPluginManifests(t *testing.T) {
	root := checkoutRoot(t)
	var plugin struct{ Name, Version string }
	var market struct {
		Name    string
		Plugins []struct{ Name, Source string }
	}
	readJSON(t, filepath.Join(root, ".claude-plugin/plugin.json"), &plugin)
	readJSON(t, filepath.Join(root, ".claude-plugin/marketplace.json"), &market)
	readJSON(t, filepath.Join(root, "hooks/hooks.json"), &hooksFile{})
	if plugin.Name != "obor" || plugin.Version != "0.1.0" {
		t.Errorf("plugin.json: name %q version %q; want obor 0.1.0", plugin.Name, plugin.Version)
	}
	// T-592: main is the dev checkout, so its marketplace is obor-kit-dev; a release tag renames it to obor-kit.
	if market.Name != "obor-kit-dev" || len(market.Plugins) == 0 || market.Plugins[0].Name != "obor" ||
		market.Plugins[0].Source != "./" {
		t.Errorf("marketplace.json: %+v; want obor-kit-dev listing obor with source ./", market)
	}
}

// bashParseError is what `/bin/bash -n` says about the script at path ("" when it parses).
func bashParseError(t *testing.T, path string) string {
	t.Helper()
	rc, _, stderr, _ := shimRun(t, filepath.Dir(path), "", nil, "/bin/bash", "-n", path)
	if rc == 0 {
		return ""
	}
	return fmt.Sprintf("exit %d: %s", rc, stderr)
}

// The bash files the kit ships (bin/obor and the git hooks) parse: since T-281 no code merge runs them, so an
// unreached syntax error would merge green (RV-31 #5).
func TestScriptsParse(t *testing.T) {
	root := checkoutRoot(t)
	// githooks/ holds the runner and the two rules, githooks/chain/ every other hook (T-623): all of them parse.
	var hooks []string
	for _, glob := range []string{"githooks/*", "githooks/chain/*"} {
		files, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v, %d files", glob, err, len(files))
		}
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil && !fi.IsDir() {
				hooks = append(hooks, f)
			}
		}
	}
	for _, path := range append([]string{filepath.Join(root, "bin/obor")}, hooks...) {
		if msg := bashParseError(t, path); msg != "" {
			t.Errorf("%s does not parse: %s", path, msg)
		}
	}
	// The check can fail: a trailing `if then` (a temp copy) is a syntax error.
	bad := filepath.Join(t.TempDir(), "pre-commit")
	if err := os.WriteFile(bad, []byte("#!/bin/bash\necho ok\nif then\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bashParseError(t, bad) == "" {
		t.Error("bash -n accepted a script ending in `if then`")
	}
}

// bash plugin.test.sh case_hooks_wiring, case_modules_entry, case_hook_commands_run_the_shim
func TestHooksJSONWiring(t *testing.T) {
	root := checkoutRoot(t)
	var h hooksFile
	readJSON(t, filepath.Join(root, "hooks/hooks.json"), &h)
	const prefix = `OBOR_NO_SELF_HOST=1 bash "${CLAUDE_PLUGIN_ROOT}/bin/obor" hook `
	want := []struct {
		event, hook, matcher string
		timeout              int
	}{
		{"PreToolUse", "pre-tool-use", "Bash|Edit|Write|NotebookEdit", 10},
		{"SessionStart", "session-start", "", 10},
		{"Stop", "stop", "", 15},
		{"StopFailure", "stop-failure", "", 15},
		{"UserPromptSubmit", "user-prompt-submit", "", 10},
	}
	if len(h.Hooks) != len(want) {
		t.Errorf("hooks.json has %d events, want exactly the five: %v", len(h.Hooks), h.Hooks)
	}
	for _, w := range want {
		entries := h.Hooks[w.event]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Errorf("%s: %d entries, want one entry with one hook", w.event, len(entries))
			continue
		}
		e, hk := entries[0], entries[0].Hooks[0]
		if e.Matcher != w.matcher || hk.Type != "command" || hk.Timeout != w.timeout {
			t.Errorf("%s: matcher %q type %q timeout %d; want %q command %d", w.event, e.Matcher, hk.Type, hk.Timeout,
				w.matcher, w.timeout)
		}
		// T-221: the shim, never self-hosted into the cwd's checkout, running a hook the binary has.
		if hk.Command != prefix+w.hook {
			t.Errorf("%s: command %q, want %q", w.event, hk.Command, prefix+w.hook)
		}
		if rc, _, _, _ := shimRun(t, root, "", nil, oborBin, "has", "hook", w.hook); rc != 0 {
			t.Errorf("%s: the binary has no hook %s (exit %d)", w.event, w.hook, rc)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "hooks")); len(entries) != 1 || entries[0].Name() != "hooks.json" {
		t.Errorf("hooks/ holds %v, want only hooks.json", entries)
	}
	// T-466: the obor mod is gone; the plugin loads no hooks module.
	if len(h.Modules) != 0 {
		t.Errorf("modules %v, want none (the obor mod went in T-466)", h.Modules)
	}
}

// bash plugin.test.sh case_claude_validate
func TestClaudePlugin(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH: claude plugin validate / test not run")
	}
	root := checkoutRoot(t)
	if rc, out, stderr, _ := shimRun(t, root, "", nil, "claude", "plugin", "validate", root); rc != 0 ||
		!strings.Contains(out, "Validation passed") {
		t.Errorf("claude plugin validate: exit %d\n%s\n%s", rc, out, stderr)
	}
}

// --- hooks without a usable Go binary (T-195, RV-22 #1, T-201) -------------------------------------------------------

// noBinKit is a copy of this kit (bin/, VERSION) with no dist/obor, inside an obor project with lane O-01, and an empty
// HOME (no installed release).
type noBinKit struct {
	p          *testkit.Project
	dir, home  string
	lane, hint string
}

func newNoBinKit(t *testing.T) *noBinKit {
	t.Helper()
	p := testkit.New(t)
	lane := p.Lane("O-01", "task/T-01")
	p.Config()
	// Inside the project's main checkout (untracked), so "make build" with cwd = the kit is in an obor project.
	k := &noBinKit{p: p, dir: filepath.Join(p.Root, "kit"), home: filepath.Join(p.Base, "home"), lane: lane}
	root := checkoutRoot(t)
	if err := os.CopyFS(filepath.Join(k.dir, "bin"), os.DirFS(filepath.Join(root, "bin"))); err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	p.Write("kit/VERSION", string(version))
	if err := os.MkdirAll(k.home, 0o755); err != nil {
		t.Fatal(err)
	}
	k.hint = fmt.Sprintf("obor: no usable Go binary in %s — run: make -C %s build", k.dir, k.dir)
	return k
}

// hook is the kit's `bin/obor hook <name>` with the event on stdin, run from the kit (no version pin).
func (k *noBinKit) hook(t *testing.T, name, event string, env ...string) (int, string, string, time.Duration) {
	t.Helper()
	env = append([]string{"HOME=" + k.home, "OBOR_NO_SELF_HOST=1"}, env...)
	return shimRun(t, k.dir, event, env, "bash", filepath.Join(k.dir, "bin/obor"), "hook", name)
}

// release is where the shim looks for the installed release binary of this platform.
func (k *noBinKit) release(t *testing.T) string {
	t.Helper()
	return filepath.Join(k.home, ".obor/bin", fmt.Sprintf("obor-%s-%s-%s",
		strings.TrimSpace(k.p.Read("kit/VERSION")), runtime.GOOS, runtime.GOARCH))
}

// stub writes a Go binary stand-in to path that logs its args to <Base>/stub.log. stale: built before `has` (knows only
// hook noop; anything else → usage, exit 2: the dist/obor of the T-193 outage); no: `has` exits 1; deny: `has` → 0, else
// "obor: go stub" on stderr, exit 2 (the Go guard); good: `has` → 0, else "go stub", exit 0; hang: `has` never returns
// (its pid in <Base>/stub.pids, then exec sleep: no child, RV-22 #1, T-279).
func (k *noBinKit) stub(t *testing.T, path, kind string) {
	t.Helper()
	body := map[string]string{
		"stale": `[ "$1 $2" = "hook noop" ] && exit 0` + "\necho \"usage: obor hook noop\" >&2\nexit 2\n",
		"no":    "[ \"$1\" = has ] && exit 1\necho \"obor: go stub\" >&2\nexit 2\n",
		"deny":  "[ \"$1\" = has ] && exit 0\necho \"obor: go stub\" >&2\nexit 2\n",
		"good":  "[ \"$1\" = has ] && exit 0\necho \"go stub\"\n",
		"hang": fmt.Sprintf("[ \"$1\" = has ] && { echo \"$$\" > '%s'; exec sleep 30; }\necho \"go stub\"\n",
			filepath.Join(k.p.Base, "stub.pids")),
	}[kind]
	if body == "" {
		t.Fatalf("stub kind %q", kind)
	}
	script := fmt.Sprintf("#!/bin/bash\necho \"$*\" >> '%s'\n%s", filepath.Join(k.p.Base, "stub.log"), body)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	k.p.Write(filepath.Join(k.p.Base, "stub.log"), "")
}

func (k *noBinKit) stubLog() string {
	return strings.TrimSpace(k.p.Read(filepath.Join(k.p.Base, "stub.log")))
}

// reaped: the hanging stub (its pid in stub.pids) is gone within 2 s; if left over it is killed.
func (k *noBinKit) reaped(t *testing.T) {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(k.p.Read(filepath.Join(k.p.Base, "stub.pids"))))
	if err != nil {
		t.Fatalf("stub.pids: %v, want <pid>", err)
	}
	alive := func() bool { return syscall.Kill(pid, 0) == nil }
	for deadline := time.Now().Add(2 * time.Second); alive() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if alive() {
		t.Errorf("hanging binary %d still alive", pid)
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

func bashEvent(cwd, command string) string {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": cwd,
		"tool_input": map[string]any{"command": command}})
	return string(b)
}

func fileEvent(cwd, tool, path string) string {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "cwd": cwd,
		"tool_input": map[string]any{"file_path": path}})
	return string(b)
}

// deniesAll: Bash, Edit, Write and NotebookEdit in the lane are denied with exactly the hint line.
func (k *noBinKit) deniesAll(t *testing.T, what string) {
	t.Helper()
	events := []string{bashEvent(k.lane, "git commit -m x"), bashEvent(k.lane, "ls")}
	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		events = append(events, fileEvent(k.lane, tool, filepath.Join(k.lane, "src/a/x.ts")))
	}
	for _, e := range events {
		if rc, _, stderr, _ := k.hook(t, "pre-tool-use", e); rc != 2 || stderr != k.hint {
			t.Errorf("%s: %s → exit %d, %q; want 2 with the hint", what, e, rc, stderr)
		}
	}
}

// bash plugin.test.sh case_pre_tool_use_missing_binary_denies: no Go binary (no dist/obor, nothing in ~/.obor/bin) in a
// obor project → every Bash / Edit / Write / NotebookEdit denied with the hint, except the repair command; other tools
// and a cwd outside an obor project allowed.
func TestPreToolUseMissingBinaryDenies(t *testing.T) {
	t.Parallel()
	k := newNoBinKit(t)
	if k.p.Exists("kit/dist/obor") || k.p.Exists(filepath.Join(k.home, ".obor/bin")) {
		t.Fatal("precondition: the kit has no dev binary, HOME no installed one")
	}
	k.deniesAll(t, "missing binary")
	other := testkit.Dir(t)
	k.p.Git(other, "-c", "init.defaultBranch=main", "init", "-q")
	read, _ := json.Marshal(map[string]any{"tool_name": "Read", "cwd": k.lane, "tool_input": map[string]any{"file_path": "x"}})
	for _, c := range []struct {
		name, event string
		rc          int
		denied      bool
	}{
		{"make -C <kit> build allowed", bashEvent(k.lane, "make -C "+k.dir+" build"), 0, false},
		{"make -C <kit> build, blanks around, allowed", bashEvent(k.lane, "  make -C "+k.dir+" build "), 0, false},
		{"precondition: the kit is in an obor project", bashEvent(k.dir, "ls"), 2, true},
		{"make build in the kit allowed", bashEvent(k.dir, "make build"), 0, false},
		{"make build outside the kit denied", bashEvent(k.lane, "make build"), 2, true},
		{"make -C <kit> build plus more denied", bashEvent(k.lane, "make -C "+k.dir+" build && git commit -m x"), 2, true},
		{"make -C <another dir> build denied", bashEvent(k.lane, "make -C "+k.p.Base+" build"), 2, true},
		{"other tools allowed", string(read), 0, false},
		{"outside an obor project: Bash allowed", bashEvent(other, "git commit -m x"), 0, false},
		{"outside an obor project: Edit allowed", fileEvent(other, "Edit", filepath.Join(other, "x")), 0, false},
	} {
		want := ""
		if c.denied {
			want = k.hint
		}
		if rc, _, stderr, _ := k.hook(t, "pre-tool-use", c.event); rc != c.rc || stderr != want {
			t.Errorf("%s: exit %d, %q; want %d, %q", c.name, rc, stderr, c.rc, want)
		}
	}
}

// bash plugin.test.sh case_pre_tool_use_stale_binary_denies: a stale dist/obor (no `has`: the T-193 outage) or a
// binary whose `has hook pre-tool-use` exits 1 is not run: the hint denies; a binary that has it runs.
func TestPreToolUseStaleBinaryDenies(t *testing.T) {
	t.Parallel()
	k := newNoBinKit(t)
	dist := filepath.Join(k.dir, "dist/obor")
	k.stub(t, dist, "stale")
	if rc, _, _, _ := shimRun(t, k.dir, "", nil, dist, "hook", "noop"); rc != 0 {
		t.Fatalf("precondition: the stale binary runs hook noop (exit %d)", rc)
	}
	if rc, _, stderr, _ := shimRun(t, k.dir, "", nil, dist, "has", "hook", "pre-tool-use"); rc != 2 ||
		!strings.Contains(stderr, "usage: obor hook noop") {
		t.Fatalf("precondition: the stale binary has no has: exit %d, %q", rc, stderr)
	}
	k.deniesAll(t, "stale dist/obor")
	event := bashEvent(k.lane, "ls")
	for _, c := range []struct {
		name, release, dist, stderr string
	}{
		{"release whose has → 1: hint, binary not run", "no", "stale", k.hint},
		{"release with has → Go guard", "deny", "stale", "obor: go stub"},
		{"dist/obor with has → Go guard", "", "deny", "obor: go stub"},
	} {
		os.Remove(k.release(t))
		if c.release != "" {
			k.stub(t, k.release(t), c.release)
		}
		k.stub(t, dist, c.dist)
		if rc, _, stderr, _ := k.hook(t, "pre-tool-use", event); rc != 2 || stderr != c.stderr {
			t.Errorf("%s: exit %d, %q; want 2, %q", c.name, rc, stderr, c.stderr)
		}
	}
}

// bash plugin.test.sh case_pre_tool_use_hanging_binary_denies (RV-22 #1, T-201): a dist/obor whose `has` never returns
// → denied with the hint within ~2 s, the binary and its child reaped; the repair command allowed; the binary rebuilt
// runs.
func TestPreToolUseHangingBinaryDenies(t *testing.T) {
	t.Parallel()
	k := newNoBinKit(t)
	dist := filepath.Join(k.dir, "dist/obor")
	event := bashEvent(k.lane, "git commit -m x")
	k.stub(t, dist, "hang")
	rc, _, stderr, took := k.hook(t, "pre-tool-use", event)
	if rc != 2 || stderr != k.hint {
		t.Errorf("hanging dist/obor: exit %d, %q; want 2 with the hint", rc, stderr)
	}
	if took > hangBound {
		t.Errorf("a hanging has is bounded (2 s): took %s", took)
	}
	if got := k.stubLog(); got != "has hook pre-tool-use" {
		t.Errorf("stub log %q: want has asked once, never run", got)
	}
	k.reaped(t)
	if rc, _, stderr, _ := k.hook(t, "pre-tool-use", bashEvent(k.lane, "make -C "+k.dir+" build")); rc != 0 || stderr != "" {
		t.Errorf("make -C <kit> build: exit %d, %q; want allowed", rc, stderr)
	}
	k.stub(t, dist, "deny")
	if rc, _, stderr, _ := k.hook(t, "pre-tool-use", event); rc != 2 || stderr != "obor: go stub" {
		t.Errorf("rebuilt dist/obor: exit %d, %q; want the Go guard", rc, stderr)
	}
}

// bash plugin.test.sh case_stop_no_binary_allows, case_user_prompt_submit_no_binary_allows,
// case_session_start_no_binary_allows (tests/lib/gostub.sh no_binary_allows, no_binary_outside): with no usable Go
// binary (missing, stale, hanging) the other hooks allow (no output, exit 0) with the hint once on stderr; stale
// and hanging binaries are never run, the hang is bounded and reaped; a good binary runs; no obor project → silent.
func TestOtherHooksNoBinaryAllow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		hook  string
		event func(cwd string) map[string]any
	}{
		{"stop", func(cwd string) map[string]any {
			return map[string]any{"session_id": "s1", "hook_event_name": "Stop", "cwd": cwd, "stop_hook_active": false}
		}},
		{"user-prompt-submit", func(cwd string) map[string]any { return map[string]any{"cwd": cwd, "prompt": "hi"} }},
		{"stop-failure", func(cwd string) map[string]any {
			return map[string]any{"hook_event_name": "StopFailure", "cwd": cwd, "error": "rate_limit"}
		}},
		{"session-start", func(cwd string) map[string]any {
			return map[string]any{"hook_event_name": "SessionStart", "cwd": cwd}
		}},
	} {
		t.Run(c.hook, func(t *testing.T) {
			t.Parallel()
			k := newNoBinKit(t)
			event, _ := json.Marshal(c.event(k.lane))
			dist := filepath.Join(k.dir, "dist/obor")
			allows := func(what string) time.Duration {
				t.Helper()
				rc, out, stderr, took := k.hook(t, c.hook, string(event))
				if rc != 0 || out != "" || stderr != k.hint {
					t.Errorf("%s: exit %d, stdout %q, stderr %q; want 0, nothing, the hint", what, rc, out, stderr)
				}
				return took
			}
			runs := func(what string) {
				t.Helper()
				if rc, out, _, _ := k.hook(t, c.hook, string(event)); rc != 0 || out != "go stub" {
					t.Errorf("%s: exit %d, %q; want hook %s run by the binary", what, rc, out, c.hook)
				}
				if log := k.stubLog(); !strings.HasPrefix(log, "has hook "+c.hook+"\n") {
					t.Errorf("%s: stub log %q, want has asked first", what, log)
				}
			}

			allows("missing binary")
			k.stub(t, dist, "stale")
			if rc, _, stderr, _ := shimRun(t, k.dir, "", nil, dist, "has", "hook", c.hook); rc != 2 ||
				!strings.Contains(stderr, "usage: obor hook noop") {
				t.Fatalf("precondition: the stale binary has no has: exit %d, %q", rc, stderr)
			}
			k.stub(t, dist, "stale")
			allows("stale binary")
			if log := k.stubLog(); log != "has hook "+c.hook {
				t.Errorf("stale binary: stub log %q, want has asked, never run", log)
			}
			k.stub(t, dist, "good")
			runs("good binary")

			k.stub(t, dist, "hang")
			if took := allows("hanging binary"); took > hangBound {
				t.Errorf("a hanging has is bounded (2 s): took %s", took)
			}
			if log := k.stubLog(); log != "has hook "+c.hook {
				t.Errorf("hanging binary: stub log %q, want has asked once, never run", log)
			}
			k.reaped(t)
			k.stub(t, dist, "good")
			runs("rebuilt binary")

			os.Remove(dist)
			outside, _ := json.Marshal(c.event(k.p.Base))
			if rc, out, stderr, _ := k.hook(t, c.hook, string(outside)); rc != 0 || out != "" || stderr != "" {
				t.Errorf("outside an obor project: exit %d, %q, %q; want silent", rc, out, stderr)
			}
		})
	}
}

// bash plugin.test.sh case_hooks_pin_no_self_host (T-221): the hooks are kit-owned callers: whatever obor they run must
// not self-host into the cwd's checkout. Each hooks.json command, run as Claude Code runs it (sh -c,
// CLAUDE_PLUGIN_ROOT = the kit) from a lane whose main checkout is an obor checkout with a broken bin/obor, reaches the
// kit's binary, which sees OBOR_NO_SELF_HOST=1.
func TestHooksPinNoSelfHost(t *testing.T) {
	t.Parallel()
	k := newNoBinKit(t)
	k.p.Write(".claude-plugin/plugin.json", `{"name":"obor"}`+"\n")
	broken := k.p.Write("bin/obor", "#!/bin/bash\necho broken project obor >&2\nexit 9\n")
	if err := os.Chmod(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if rc, _, _, _ := shimRun(t, k.p.Root, "", nil, "bash", broken); rc != 9 {
		t.Fatalf("precondition: the project's bin/obor is broken (exit %d)", rc)
	}
	pins := testkit.Dir(t)
	dist := k.p.Write("kit/dist/obor", fmt.Sprintf(
		"#!/bin/bash\n[ \"$1\" = has ] && exit 0\necho \"${OBOR_NO_SELF_HOST-unset}\" > '%s/pin.'\"$2\"\nexit 0\n", pins))
	if err := os.Chmod(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	var h hooksFile
	readJSON(t, filepath.Join(checkoutRoot(t), "hooks/hooks.json"), &h)
	event, _ := json.Marshal(map[string]any{"hook_event_name": "x", "tool_name": "Read", "cwd": k.lane, "prompt": "hi",
		"tool_input": map[string]any{"file_path": "x"}})
	for _, name := range []string{"SessionStart", "Stop", "StopFailure", "UserPromptSubmit", "PreToolUse"} {
		if len(h.Hooks[name]) == 0 || len(h.Hooks[name][0].Hooks) == 0 {
			t.Errorf("%s: no hook in hooks.json", name)
			continue
		}
		cmd := h.Hooks[name][0].Hooks[0].Command
		// testkit.Isolate already cleared OBOR_NO_SELF_HOST from this environment.
		shimRun(t, k.lane, string(event), []string{"HOME=" + k.home, "CLAUDE_PLUGIN_ROOT=" + k.dir}, "sh", "-c", cmd)
		hook := cmd[strings.LastIndex(cmd, " ")+1:]
		got, err := os.ReadFile(filepath.Join(pins, "pin."+hook))
		if err != nil || strings.TrimSpace(string(got)) != "1" {
			t.Errorf("%s: the kit's binary saw OBOR_NO_SELF_HOST=%q (%v), want 1", name, got, err)
		}
	}
}
