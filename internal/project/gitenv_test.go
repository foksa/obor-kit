package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// GitEnv is the environment of every git command obor runs: GIT_OPTIONAL_LOCKS=0 (a read takes no index.lock) over
// env, this process's environment when nil, an entry env has for it replaced, its argument untouched.
func TestGitEnv(t *testing.T) {
	t.Parallel()
	env := []string{"A=B", "GIT_OPTIONAL_LOCKS=1", "C=D"}
	if got, want := GitEnv(env), []string{"A=B", "C=D", GitOptionalLocksOff}; !slices.Equal(got, want) {
		t.Errorf("GitEnv(%q) = %q, want %q", env, got, want)
	}
	if want := []string{"A=B", "GIT_OPTIONAL_LOCKS=1", "C=D"}; !slices.Equal(env, want) {
		t.Errorf("GitEnv changed its argument: %q, want %q", env, want)
	}
	got := GitEnv(nil)
	if n := len(slices.DeleteFunc(slices.Clone(got), func(kv string) bool { return kv != GitOptionalLocksOff })); n != 1 {
		t.Errorf("GitEnv(nil) carries %s %d times, want once: %q", GitOptionalLocksOff, n, got)
	}
	if !slices.Contains(got, "PATH="+os.Getenv("PATH")) {
		t.Errorf("GitEnv(nil) drops this process's environment: %q", got)
	}
}

// A git command obor runs carries GIT_OPTIONAL_LOCKS=0: git skips the opportunistic index refresh, so the read takes no
// index.lock (a write's locks are as always). A value this process's environment has for it does not win.
func TestGitRunsTakeNoOptionalLock(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nprintf '%s' \"$GIT_OPTIONAL_LOCKS\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	out, err := Git(filepath.Join(base, "repo"), "status", "--porcelain")
	if err != nil {
		t.Fatalf("Git: %v", err)
	}
	if out != "0" {
		t.Errorf("git got GIT_OPTIONAL_LOCKS=%q, want 0", strings.TrimSpace(out))
	}
}
