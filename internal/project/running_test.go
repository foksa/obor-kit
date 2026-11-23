package project

import (
	"os"
	"path/filepath"
	"testing"
)

// write lays a file out, its directories made: a fixture repo is a .git marker alone, so no git process runs.
func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Running, and InProject, are dir in a running obor project: a directory in a checkout whose main checkout holds
// .obor/config.json, a linked worktree (its .git file's gitdir, that dir's commondir) though it has no config of its
// own, and a worktree tracking a config of its own (the marker read is the main checkout's); not a repository without
// one, nor a stopped project; a repository nested in an obor project is its own, not the outer project's.
func TestRunning(t *testing.T) {
	base := Physical(t.TempDir())
	main, lane, other := filepath.Join(base, "main"), filepath.Join(base, "wt", "L-01"), filepath.Join(base, "other")
	outer, nested := filepath.Join(base, "outer"), filepath.Join(base, "outer", "nested")
	write(t, filepath.Join(main, ".obor", "config.json"), "{}")
	write(t, filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(main, ".git", "worktrees", "L-01", "commondir"), "../..\n")
	write(t, filepath.Join(lane, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "L-01")+"\n")
	write(t, filepath.Join(lane, "pkg", "a.go"), "package a\n")
	write(t, filepath.Join(main, ".agents", "hooks.json"), "{}\n")
	write(t, filepath.Join(other, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(other, "sub", "b.go"), "package b\n")
	write(t, filepath.Join(outer, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(outer, ".obor", "config.json"), "{}")
	write(t, filepath.Join(nested, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(nested, "pkg", "a.go"), "package a\n")
	if _, err := os.Stat(filepath.Join(lane, ".obor")); err == nil {
		t.Fatal("precondition: the lane has no config of its own")
	}

	for dir, want := range map[string]bool{
		main: true, filepath.Join(main, ".agents"): true, lane: true, filepath.Join(lane, "pkg"): true,
		outer: true, nested: false, filepath.Join(nested, "pkg"): false,
		filepath.Join(other, "sub"): false, "": false,
	} {
		if got := InProject(dir); got != want {
			t.Errorf("InProject(%q) = %v, want %v", dir, got, want)
		}
	}
	for dir, want := range map[string]string{main: main, lane: main, nested: ""} {
		got := ""
		if r := Running(dir); r != nil {
			got = r.Root
		}
		if got != want {
			t.Errorf("Running(%q).Root = %q, want %q", dir, got, want)
		}
	}

	// A stopped project (obor stop's marker in the main checkout) is no project, also for its worktree, also when the
	// worktree tracks a config of its own: the marker read is the main checkout's.
	write(t, filepath.Join(main, ".obor", "state", "stopped"), "stopped\n")
	write(t, filepath.Join(lane, ".obor", "config.json"), "{}")
	for _, dir := range []string{main, filepath.Join(main, ".agents"), lane, filepath.Join(lane, "pkg")} {
		if InProject(dir) {
			t.Errorf("InProject(%q) in a stopped project = true, want false", dir)
		}
	}
	if !InProject(outer) {
		t.Error("InProject(outer) in a stopped project beside it = false, want true: each project reads its own marker")
	}
	if err := os.Remove(filepath.Join(main, ".obor", "state", "stopped")); err != nil {
		t.Fatal(err)
	}
	if !InProject(lane) {
		t.Error("InProject(lane) once the marker is gone = false, want true: the worktree is in the project")
	}
}
