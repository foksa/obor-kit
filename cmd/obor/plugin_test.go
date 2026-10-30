package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// waitFor polls cond for up to 15 s.
func waitFor(cond func() bool) bool {
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// obor plugin through the binary: usage (kick went with the heartbeat, T-465), the hook returning at once with the
// work detached, herdr reached over its socket alone (behaviour: internal/launch).
// bash restore.test.sh case_backgrounded_and_failure_logged
func TestPluginBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(lane)
	p.SubmitAgo("O-01", 900)
	p.Herdr().Agent("w9:l", "pj-lead", "idle", p.Root).Label = "pj-lead"
	p.Herdr().Shell("w9:o1", "pj-o-01", lane)
	srv := herdrtest.Serve(t, p.Herdr())
	// herdr is reached over HERDR_SOCKET_PATH, as herdr runs its plugins. HERDR_PANE_ID is a pane a plugin command can
	// carry (T-301): the restore below restarts that pane now, never as its own (SCHEDULED).
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HERDR_PANE_ID=w9:o1",
		"OBOR_STATE_HOME=" + p.Base + "/state-home"}
	plugin := filepath.Join(p.Base, "plugin-dir") // herdr's cwd for plugin commands: no project
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"plugin"}, {"plugin", "start"}, {"plugin", "kick"}, {"plugin", "restore", "--later"}} {
		if rc, _, stderr := run(t, plugin, env, args...); rc != 2 || !strings.Contains(stderr, pluginUsage) {
			t.Errorf("obor %q: exit %d, %q; want 2, usage", args, rc, stderr)
		}
	}

	// restore: the lead's pane runs an agent (skipped); the in-flight lane's shell is restarted by the real obor
	// restart (fresh: no recorded session), its output and result logged.
	restoreLog := filepath.Join(p.Base, "state-home", "restore.log")
	if rc, stdout, stderr := run(t, plugin, env, "plugin", "restore"); rc != 0 || stdout+stderr != "" {
		t.Fatalf("obor plugin restore: exit %d, %q, %q; want 0, silent", rc, stdout, stderr)
	}
	if !waitFor(func() bool { return strings.Contains(p.Read(restoreLog), "RESTARTED pj-o-01 (pane w9:o1") }) {
		t.Fatalf("restore.log:\n%s", p.Read(restoreLog))
	}
	got := p.Read(restoreLog)
	if !strings.Contains(got, " skip pj-lead (pane w9:l): runs claude") ||
		!strings.Contains(got, " restore pj-o-01 (pane w9:o1) in "+p.Root+" (worker, no recorded session: --fresh)") {
		t.Errorf("restore.log: want the lead skipped and the worker restored:\n%s", got)
	}
}

// The herdr manifest (herdr-plugin/herdr-plugin.toml): obor.events, the startup hook → obor plugin restore and no
// event hooks (the kick went with the heartbeat, T-465), through the kit's bin/obor relative to the plugin directory
// (herdr's cwd for plugin commands); this binary has restore, not kick. No TOML parser: each line is a `key = value`, a table header, a comment or blank.
// bash plugin.test.sh case_herdr_manifest
func TestHerdrManifest(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../herdr-plugin/herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`^(\s*(#.*)?|\[\[(events|startup)\]\]|[a-z_]+ = ("[^"]*"|\[("[^"]*"(, )?)+\]))$`)
	var top, tables []string
	table := ""
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		switch {
		case !line.MatchString(l):
			t.Errorf("not key = value / [[events]] / [[startup]] / comment: %q", l)
		case strings.HasPrefix(l, "[["):
			table = l
		case strings.HasPrefix(l, "#") || l == "":
		case table == "":
			top = append(top, l)
		default:
			tables = append(tables, table+" "+l)
		}
	}
	for _, want := range []string{`id = "obor.events"`, `name = "obor events"`, `min_herdr_version = "0.9.0"`,
		`platforms = ["macos", "linux"]`} {
		if !slices.Contains(top, want) {
			t.Errorf("top-level %s missing: %q", want, top)
		}
	}
	const restore = `command = ["/bin/bash", "../bin/obor", "plugin", "restore"]`
	want := []string{"[[startup]] " + restore}
	if !slices.Equal(tables, want) {
		t.Errorf("tables:\n%s\nwant:\n%s", strings.Join(tables, "\n"), strings.Join(want, "\n"))
	}
	if fi, err := os.Stat("../../herdr-plugin/../bin/obor"); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("../bin/obor from the plugin directory: %v", err)
	}
	if rc, _, _ := run(t, ".", nil, "has", "plugin", "restore"); rc != 0 {
		t.Errorf("obor has plugin restore: exit %d", rc)
	}
	if rc, _, _ := run(t, ".", nil, "has", "plugin", "kick"); rc != 1 {
		t.Errorf("obor has plugin kick: exit %d, want 1 (gone)", rc)
	}
}
