package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// obor init end to end (the binary): stub herdr on its socket, a fake claude and a fake launchctl on PATH, HOME a
// temp dir. It sets the project up, installs serve (bootstrap of obor.serve.proj's plist under HOME) and says next:
// obor lead; a rerun leaves the loaded agent alone (no second bootstrap). Off darwin the serve step is skipped with
// the run-it-yourself line and init still succeeds.
func TestInitEndToEnd(t *testing.T) {
	testkit.Slow(t)
	t.Parallel()
	p := testkit.New(t)
	x := herdrtest.New()
	srv := herdrtest.Serve(t, x)
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	claude := "#!/bin/sh\necho \"claude $*\" >> " + log + "\ncase \"$*\" in \"plugin install \"*) mkdir -p .claude; " +
		"printf '{\"enabledPlugins\":{\"%s\":true}}' \"$3\" > .claude/settings.json ;; esac\n"
	launchctl := "#!/bin/sh\necho \"launchctl $*\" >> " + log + "\ncase \"$1\" in print) [ -f " + bin + "/loaded ] || exit 113; " +
		"echo '\tpid = 999999' ;; bootstrap) : > " + bin + "/loaded ;; bootout) rm -f " + bin + "/loaded ;; esac\n"
	for name, script := range map[string]string{"claude": claude, "launchctl": launchctl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kit, _ := filepath.Abs(filepath.Join("..", ".."))
	home := t.TempDir()
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home,
		"OBOR_PLUGIN_ROOT=" + kit, "OBOR_STATE_HOME=" + t.TempDir(), "HERDR_PANE_ID=", "HERDR_CONFIG_PATH="}
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
