//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// TestExitCodes outside a git repository: exit 3 (hooks and callers treat it as "no obor project"). project.Find
// finds no .git there and asks git (rev-parse), so the case runs here.
func TestExitCodesOutsideARepo(t *testing.T) {
	cmd := exec.Command(build(t), "whoami")
	cmd.Dir = t.TempDir()
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 3 {
		t.Errorf("whoami outside a repo: %v, want exit 3", err)
	}
}

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

// TestAgyKindHook: obor kind agy hook runs obor's hook in process, its answer agy's: the guard's deny for an agy tool
// obor cannot guard, and the guard's project rule (project.InProject). A repository nested in an obor project is its
// own, so an event obor cannot read is asked there (agy's own check) and denied in the outer project.
func TestAgyKindHook(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	p.Write(".git/HEAD", "ref: refs/heads/main\n")
	p.Config()
	p.Write("nested/.git/HEAD", "ref: refs/heads/main\n")
	inner := p.Path("nested")
	env := []string{"OBOR_ROLE=worker"}

	event := fmt.Sprintf(`{"hookEventName":"PreToolUse","cwd":%q,"toolCall":{"name":"browser_click"}}`, p.Root)
	rc, stdout, stderr, _ := shimRun(t, p.Root, event, env, build(t), "kind", "agy", "hook")
	if want := `{"decision":"deny","reason":"obor: obor has no rule for agy tool browser_click: update obor"}`; rc != 0 ||
		stdout != want || stderr != "" {
		t.Errorf("agy's unguarded tool in the project: exit %d, stdout %q, stderr %q; want 0, %q, nothing",
			rc, stdout, stderr, want)
	}

	broken := `{"hookEventName":"PreToolUse","toolCall":`
	if rc, stdout, stderr, _ := shimRun(t, inner, broken, env, build(t), "kind", "agy", "hook"); rc != 0 ||
		stdout != `{"decision":"ask"}` || stderr != "" {
		t.Errorf("an unreadable event in a repository nested in the project: exit %d, stdout %q, stderr %q; want 0, "+
			"%q, nothing (the nested repository is its own)", rc, stdout, stderr, `{"decision":"ask"}`)
	}
	rc, stdout, stderr, _ = shimRun(t, p.Root, broken, env, build(t), "kind", "agy", "hook")
	if rc != 0 || !strings.HasPrefix(stdout, `{"decision":"deny","reason":"obor: cannot read agy's hook event: `) ||
		stderr != "" {
		t.Errorf("an unreadable event in the project: exit %d, stdout %q, stderr %q; want 0 and the transport's deny",
			rc, stdout, stderr)
	}
}
