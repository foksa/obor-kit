package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// gitEnvShim is an executable git that answers with the GIT_OPTIONAL_LOCKS a runner gives it.
func gitEnvShim(t *testing.T) string {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "git-env")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf '%s' \"$GIT_OPTIONAL_LOCKS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

// The two git runners of this package carry GIT_OPTIONAL_LOCKS=0 (project.GitEnv): a read takes no index.lock. Lane is
// lane.prepare's and a lane test's git; Options is the scratch's (Scratch.gitEnv) and a nightly's.
func TestGitRunnersTakeNoOptionalLock(t *testing.T) {
	shim := gitEnvShim(t)
	t.Setenv("GIT_OPTIONAL_LOCKS", "1") // Lane reads this process's environment: the rule must still win
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		git  func() (string, error)
	}{
		{"Lane", func() (string, error) { return Lane{Git: shim}.git(context.Background(), dir, "status", "--porcelain") }},
		{"Options", func() (string, error) {
			return (&Options{Git: shim, Environ: []string{"GIT_OPTIONAL_LOCKS=1"}}).git(dir, "status", "--porcelain")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.git()
			if err != nil {
				t.Fatalf("git: %v", err)
			}
			if out != "0" {
				t.Errorf("git got GIT_OPTIONAL_LOCKS=%q, want 0", out)
			}
		})
	}
}
