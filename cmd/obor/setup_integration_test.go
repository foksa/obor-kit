//go:build integration

package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// T-536: setup talks to herdr over its socket (HERDR_SOCKET_PATH). The run's PATH has no herdr on it: only the fake
// socket answers, and every call is its answer.
func TestSetupPaneUsesTheHerdrSocket(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	x := herdrtest.New()
	x.Tab("w1:t1", 200, 50, "w1:p1")
	srv := herdrtest.Serve(t, x)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "PATH=/usr/bin:/bin"}
	if rc, stdout, stderr := run(t, p.Root, env, "setup", "lane", "L-01"); rc != 0 {
		t.Fatalf("setup lane: exit %d\n%s%s", rc, stdout, stderr)
	}
	rc, stdout, stderr := run(t, p.Root, env, "setup", "pane", "--start", "L-01", "--tab", "w1:t1")
	if rc != 0 || !strings.Contains(stdout, "OK L-01: pj-l-01 in pane w1:n1") {
		t.Fatalf("setup pane: exit %d\n%s%s", rc, stdout, stderr)
	}
	pane := x.Pane("w1:n1")
	if pane == nil || pane.Kind != "claude" || pane.Name != "pj-l-01" {
		t.Errorf("the socket's herdr started the agent: %+v", pane)
	}
	var methods []string
	for _, req := range srv.Requests() {
		methods = append(methods, req.Method)
	}
	for _, want := range []string{"agent.list", "pane.list", "pane.layout", "pane.split", "agent.start"} {
		if !strings.Contains(strings.Join(methods, " "), want) {
			t.Errorf("no %s on the socket: %v", want, methods)
		}
	}
}

// obor init end to end (the binary): stub herdr on its socket, a fake claude and a fake launchctl on PATH, HOME a
// temp dir. It sets the project up, installs serve (bootstrap of obor.serve.proj's plist under HOME) and says next:
// obor lead; a rerun leaves the loaded agent alone (no second bootstrap). Off darwin the serve step is skipped with
// the run-it-yourself line and init still succeeds.
func TestInitEndToEnd(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	env, log, home := initEnv(t, nil)
	rc, stdout, stderr := run(t, p.Root, env, "init")
	if rc != 0 || !strings.Contains(stdout, "OK init proj (prefix proj); next: first task for the lead: set up the stack "+
		"and `commands.check` (lane_setup, lane_ready); until then every gate stops on \"missing command\"\n") ||
		!strings.HasSuffix(stdout, "starting point: the lead goes through it with you at its first session\n") {
		t.Fatalf("obor init: exit %d\n%s%s", rc, stdout, stderr)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "claude plugin install obor@") {
		t.Errorf("no plugin install:\n%s", calls)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "obor.serve.proj.plist")
	if runtime.GOOS != "darwin" {
		if !strings.Contains(stdout, "serve      skipped  run `obor serve` yourself") || strings.Contains(string(calls), "launchctl") {
			t.Errorf("off darwin:\n%s\ncalls:\n%s", stdout, calls)
		}
		return
	}
	if _, err := os.Stat(plist); err != nil || !strings.Contains(string(calls), "launchctl bootstrap gui/") {
		t.Fatalf("serve not installed (%v):\n%s\ncalls:\n%s", err, stdout, calls)
	}
	if !strings.Contains(stdout, "  serve      done     INSTALLED obor.serve.proj (") {
		t.Errorf("serve step:\n%s", stdout)
	}
	rc, stdout, stderr = run(t, p.Root, env, "init")
	if rc != 0 || !strings.Contains(stdout, "  serve      already  obor.serve.proj installed and loaded") {
		t.Fatalf("rerun: exit %d\n%s%s", rc, stdout, stderr)
	}
	again, _ := os.ReadFile(log)
	if strings.Count(string(again), "launchctl bootstrap") != 1 || strings.Count(string(again), "claude plugin install") != 1 {
		t.Errorf("rerun bootstrapped or installed again:\n%s", again)
	}
}

// initEnv is obor init's environment for a test (TestInitEndToEnd's doc): stub herdr on its socket, a fake claude and
// a fake launchctl on PATH (their calls logged to log) with more, the scripts by name, HOME a temp dir.
func initEnv(t *testing.T, more map[string]string) (env []string, log, home string) {
	t.Helper()
	x := herdrtest.New()
	srv := herdrtest.Serve(t, x)
	bin := t.TempDir()
	log = filepath.Join(bin, "calls")
	claude := "#!/bin/sh\necho \"claude $*\" >> " + log + "\ncase \"$*\" in \"plugin install \"*) mkdir -p .claude; " +
		"printf '{\"enabledPlugins\":{\"%s\":true}}' \"$3\" > .claude/settings.json ;; esac\n"
	launchctl := "#!/bin/sh\necho \"launchctl $*\" >> " + log + "\ncase \"$1\" in print) [ -f " + bin + "/loaded ] || exit 113; " +
		"echo '\tpid = 999999' ;; bootstrap) : > " + bin + "/loaded ;; bootout) rm -f " + bin + "/loaded ;; esac\n"
	scripts := map[string]string{"claude": claude, "launchctl": launchctl}
	maps.Copy(scripts, more)
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kit, _ := filepath.Abs(filepath.Join("..", ".."))
	home = t.TempDir()
	env = []string{"HERDR_SOCKET_PATH=" + srv.Path, "PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home,
		"OBOR_PLUGIN_ROOT=" + kit, "OBOR_STATE_HOME=" + t.TempDir(), "HERDR_PANE_ID=", "OBOR_PANE=", "HERDR_CONFIG_PATH="}
	return env, log, home
}

// T-1054 (RV-150 #2): obor init in a clean project with the kit binary (the real obor-kind-opencode beside obor)
// caches OpenCode's manifest before its panes, so obor setup mcp --model deepseek works with no hand step; init in a
// project whose integrator is OpenCode's (its pane's start built from the manifest) succeeds, and so does its rerun.
func TestInitCachesThePluginKinds(t *testing.T) {
	t.Parallel()
	cacheOf := func(p *testkit.Project) string {
		return filepath.Join(p.Root, ".obor", "state", "kinds", "opencode.json")
	}
	initIn := func(p *testkit.Project, env []string, what string) {
		t.Helper()
		if _, err := os.Stat(cacheOf(p)); !os.IsNotExist(err) {
			t.Fatalf("%s: precondition: no manifest cached (%v)", what, err)
		}
		rc, stdout, stderr := run(t, p.Root, env, "init")
		if rc != 0 || !strings.Contains(stdout, "  kinds      done     opencode: manifest cached") {
			t.Fatalf("%s: exit %d\n%s%s", what, rc, stdout, stderr)
		}
		b, err := os.ReadFile(cacheOf(p))
		var c struct{ Command string }
		if err != nil || json.Unmarshal(b, &c) != nil || filepath.Base(c.Command) != "obor-kind-opencode" {
			t.Fatalf("%s: cached %q (%v), want the kit's obor-kind-opencode", what, b, err)
		}
	}

	clean := testkit.New(t)
	env, _, _ := initEnv(t, nil)
	initIn(clean, env, "obor init in a clean project")
	if rc, stdout, stderr := run(t, clean.Root, env, "setup", "mcp", "--model", "deepseek"); rc != 0 {
		t.Errorf("obor setup mcp --model deepseek: exit %d\n%s%s", rc, stdout, stderr)
	}

	oc := testkit.New(t)
	oc.Config(testkit.Set("agents.roles.integrator", "deepseek"))
	// an opencode on PATH: else init sets the integrator to opus (no CLI to run it)
	env, _, _ = initEnv(t, map[string]string{"opencode": "#!/bin/sh\necho 1.14.0\n"})
	initIn(oc, env, "obor init with an OpenCode integrator")
	if b, _ := os.ReadFile(filepath.Join(oc.Root, ".obor", "config.json")); !strings.Contains(string(b), `"integrator": "deepseek"`) {
		t.Fatalf("precondition: the integrator stays OpenCode's deepseek:\n%s", b)
	}
	if err := os.Remove(cacheOf(oc)); err != nil { // the rerun caches it again itself
		t.Fatal(err)
	}
	initIn(oc, env, "its rerun")
}
