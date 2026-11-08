//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStopFailureHook outside an obor project: nothing (exit 0, silent, no .obor dir written). project.Find finds no
// .git there and asks git (rev-parse), so the case runs here.
func TestStopFailureHookOutsideAProject(t *testing.T) {
	t.Parallel()
	env := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-l-01"}
	outside := t.TempDir()
	event, _ := json.Marshal(map[string]any{"cwd": outside, "error": "rate_limit"})
	if rc, stdout, stderr, _ := shimRun(t, outside, string(event), env, oborBin, "hook", "stop-failure"); rc != 0 ||
		stdout != "" || stderr != "" {
		t.Errorf("outside an obor project: exit %d, %q, %q; want silent", rc, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(outside, ".obor")); err == nil {
		t.Error("outside an obor project: a .obor dir was written")
	}
}
