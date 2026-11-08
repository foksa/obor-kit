//go:build integration

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

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

// TestExitCodes outside a git repository: exit 3 (hooks and callers treat it as "no obor project"). project.Find
// finds no .git there and asks git (rev-parse), so the case runs here.
func TestExitCodesOutsideARepo(t *testing.T) {
	cmd := exec.Command(build(t), "whoami")
	cmd.Dir = t.TempDir()
	if err := cmd.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 3 {
		t.Errorf("whoami outside a repo: %v, want exit 3", err)
	}
}
