//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// obor plugin through the binary: usage (kick went with the heartbeat, T-465), the hook returning at once with the
// work detached, herdr reached over its socket alone (behaviour: internal/launch).
// bash restore.test.sh case_backgrounded_and_failure_logged
func TestPluginBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(lane)
	p.SubmitAgo("L-01", 900)
	p.Herdr().Agent("w9:l", "pj-lead", "idle", p.Root).Label = "pj-lead"
	p.Herdr().Shell("w9:o1", "pj-l-01", lane)
	srv := herdrtest.Serve(t, p.Herdr())
	// herdr is reached over HERDR_SOCKET_PATH, as herdr runs its plugins. OBOR_PANE and HERDR_PANE_ID name a pane a
	// plugin command can carry (T-301, T-1037): the restore below restarts that pane now, never as its own (SCHEDULED).
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "OBOR_PANE=w9:o1", "HERDR_PANE_ID=w9:o1",
		"OBOR_STATE_HOME=" + p.Base + "/state-home"}
	plugin := filepath.Join(p.Base, "plugin-dir") // herdr's cwd for plugin commands: no project
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"plugin"}, {"plugin", "start"}, {"plugin", "kick"}, {"plugin", "restore", "--later"}} {
		if rc, _, stderr := run(t, plugin, env, args...); rc != 2 || !strings.Contains(stderr, help.Usage("plugin")) {
			t.Errorf("obor %q: exit %d, %q; want 2, usage", args, rc, stderr)
		}
	}

	// restore: the lead's pane runs an agent (skipped); the in-flight lane's shell is restarted by the real obor
	// restart (fresh: no recorded session), its output and result logged.
	restoreLog := filepath.Join(p.Base, "state-home", "restore.log")
	if rc, stdout, stderr := run(t, plugin, env, "plugin", "restore"); rc != 0 || stdout+stderr != "" {
		t.Fatalf("obor plugin restore: exit %d, %q, %q; want 0, silent", rc, stdout, stderr)
	}
	if !waitFor(func() bool { return strings.Contains(p.Read(restoreLog), "RESTARTED pj-l-01 (pane w9:o1") }) {
		t.Fatalf("restore.log:\n%s", p.Read(restoreLog))
	}
	got := p.Read(restoreLog)
	if !strings.Contains(got, " skip pj-lead (pane w9:l): runs claude") ||
		!strings.Contains(got, " restore pj-l-01 (pane w9:o1) in "+p.Root+" (worker, no recorded session: --fresh)") {
		t.Errorf("restore.log: want the lead skipped and the worker restored:\n%s", got)
	}
}
