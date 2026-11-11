//go:build integration

// The integration tier of obor init: init makes the repository (git init, the "obor init" commit), installs
// core.hooksPath and adds the lane worktrees with git, so these run on a git project (newGitInitFx).

package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
)

// obor init in a plain directory (no repository) makes one on branch main, commits its own files as "obor init"
// and branches the lanes from that commit; a directory the user already made a repository of, with no commit, gets the
// same first commit; a user's untracked file is a FAIL, never committed; a repository with a commit is left as it is.
func TestOborInitInAnEmptyDirectory(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	dir := filepath.Join(x.Base, "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, rc := x.runIn(t, dir, "obor-init")
	if rc != 0 {
		t.Fatalf("obor init in an empty dir: exit %d\n%s", rc, out)
	}
	if got := x.Git(dir, "branch", "--show-current"); got != "main" {
		t.Errorf("branch %q, want main", got)
	}
	if got := x.Git(dir, "log", "--format=%s"); got != "obor init" {
		t.Errorf("commits %q, want the one obor_init commit", got)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"L-01", "L-02"} {
		if _, err := os.Stat(filepath.Join(cfg.LanesDir(), lane, ".git")); err != nil {
			t.Errorf("no worktree for %s: %v", lane, err)
		}
	}
	has(t, out, "git init -b main in "+dir, "the git init line")
	has(t, out, "committed obor init: ", "the commit line")
	has(t, out, ".obor/config.json", "the commit's config")
	has(t, out, ".gitignore", "the commit's .gitignore")
	has(t, out, "docs/agents/", "the commit's agents dir")
	hasNot(t, out, "nothing committed", "no uncommitted note after init committed")
}

// a rerun after an interrupted init (init's own files, no commit) commits them when each is init's text; one
// the user edited is a FAIL, left as it is.
func TestOborInitRerunAfterAnInterruptedInit(t *testing.T) {
	t.Parallel()
	t.Run("untouched", func(t *testing.T) {
		x := newGitInitFx(t, true)
		dir := x.unborn(t, "fresh")
		x.interrupted(t, dir)
		if !x.Exists(filepath.Join(dir, ".obor", "config.json")) || !x.Exists(filepath.Join(dir, "BACKLOG.md")) {
			t.Fatal("the interrupted init wrote no files")
		}
		out, rc := x.runIn(t, dir, "obor-init")
		if rc != 0 {
			t.Fatalf("rerun: exit %d\n%s", rc, out)
		}
		if got := x.Git(dir, "log", "--format=%s"); got != "obor init" {
			t.Errorf("commits %q, want the one obor init commit", got)
		}
		files := x.Git(dir, "show", "--name-only", "--format=", "HEAD")
		for _, f := range []string{".obor/config.json", ".gitignore", "BACKLOG.md", "docs/decisions.md"} {
			if !strings.Contains("\n"+files+"\n", "\n"+f+"\n") {
				t.Errorf("the commit lacks %s:\n%s", f, files)
			}
		}
	})
	t.Run("edited", func(t *testing.T) {
		x := newGitInitFx(t, true)
		dir := x.unborn(t, "fresh")
		x.interrupted(t, dir)
		path := filepath.Join(dir, "BACKLOG.md")
		edited := x.Read(path) + "- my own item\n"
		x.Write(path, edited)
		out, rc := x.runIn(t, dir, "obor-init")
		wantFail(t, x, out, rc, dir)
		if x.Read(path) != edited {
			t.Error("the edited BACKLOG.md was touched")
		}
	})
}
