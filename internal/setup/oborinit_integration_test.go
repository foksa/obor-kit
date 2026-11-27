//go:build integration

// The integration tier of obor init: init makes the repository (git init, the "obor init" commit), installs
// core.hooksPath and adds the lane worktrees with git, so these run on a git project (newGitInitFx).

package setup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// obor init in a plain directory (no repository) makes one on branch main, commits its own files (the config and
// .claude/settings.json with the edits init made to them) as the one "obor init" commit and branches the lanes from it:
// the tree is clean after init.
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
	files := strings.Fields(x.Git(dir, "show", "--name-only", "--format=", "HEAD"))
	for _, f := range []string{".obor/config.json", ".claude/settings.json", ".gitignore", "BACKLOG.md", "docs/decisions.md",
		"docs/agents/worker.md"} {
		if !slices.Contains(files, f) {
			t.Errorf("the commit lacks %s: %v", f, files)
		}
	}
	if got := x.Git(dir, "status", "--porcelain"); got != "" {
		t.Errorf("git status after init:\n%s", got)
	}
	if got := x.Git(dir, "show", "HEAD:.obor/config.json"); got != strings.TrimSpace(x.Read(filepath.Join(dir, ".obor", "config.json"))) ||
		!strings.Contains(got, `"count": 2`) {
		t.Errorf("the committed config is not the final one (lanes.count 2):\n%s", got)
	}
	has(t, out, "git init -b main in "+dir, "the git init line")
	has(t, out, "committed obor init: ", "the commit line")
	has(t, out, ".obor/config.json", "the commit's config")
	has(t, out, ".gitignore", "the commit's .gitignore")
	has(t, out, "docs/agents/", "the commit's agents dir")
	hasNot(t, out, "nothing committed", "no uncommitted note after init committed")
}

// A lane made while setup was deferred sits at init's commit, without the files the owner commits after it. The
// repair rerun moves such a lane (detached, clean, no task, behind) to the branch head before lane setup, so the
// setup sees the owner's package.json; every other lane is left as it is.
func TestOborInitRerunMovesADeferredLaneToTheOwnersCommit(t *testing.T) {
	// Not parallel: the lane's shell reads this process's PATH, where the fake npm is first.
	x := newGitInitFx(t, true)
	dir := x.unborn(t, "fresh")
	x.Write(filepath.Join(dir, "package.json"), `{"scripts":{"test":"vitest run"}}`)
	x.Write(filepath.Join(dir, "package-lock.json"), "{}\n")
	marker := filepath.Join(x.Base, "npm-ran")
	testkit.Script(t, x.bin, "npm", "#!/bin/sh\n[ -f package.json ] || exit 9\nmkdir -p node_modules\necho \"$PWD\" >> '"+marker+"'\n")
	t.Setenv("PATH", x.bin+":/usr/bin:/bin")
	if out, rc := x.runIn(t, dir, "obor-init"); rc != 0 {
		t.Fatalf("first init: exit %d\n%s", rc, out)
	}
	if x.Exists(marker) {
		t.Fatalf("precondition: the first init ran lane_setup: %q", x.Read(marker))
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	lanes := []string{filepath.Join(cfg.LanesDir(), "L-01"), filepath.Join(cfg.LanesDir(), "L-02")}
	for _, lane := range lanes {
		if x.Exists(filepath.Join(lane, "package.json")) {
			t.Fatalf("precondition: the deferred lane %s already holds package.json", lane)
		}
	}
	worktrees := worktreeDirs(x, dir)
	// the files question's Commit all: the owner's files join init's commit on main
	x.Git(dir, "add", "-A")
	x.Git(dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "Initial commit", "--no-verify")
	out, rc := x.runIn(t, dir, "obor-init")
	if rc != 0 {
		t.Fatalf("rerun: exit %d\n%s", rc, out)
	}
	owner := x.Git(dir, "rev-parse", "HEAD")
	for _, lane := range lanes {
		has(t, out, "worktree "+lane+" moved to main (a new lane's start)", "the move line")
		if !x.Exists(filepath.Join(lane, "package.json")) {
			t.Errorf("%s is not at the owner's commit: package.json is missing", lane)
		}
		if got := x.Git(lane, "rev-parse", "HEAD"); got != owner {
			t.Errorf("%s HEAD %s, want the owner's commit %s", lane, got, owner)
		}
		has(t, x.Read(marker), lane, "lane_setup ran in "+lane+" after the move")
	}
	if got := worktreeDirs(x, dir); !slices.Equal(got, worktrees) {
		t.Errorf("the rerun changed the worktrees: %v, were %v", got, worktrees)
	}
}

// worktreeDirs is the dirs `git worktree list` names under dir, without the shas.
func worktreeDirs(x *initFx, dir string) []string {
	var dirs []string
	for _, line := range strings.Split(x.Git(dir, "worktree", "list"), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			dirs = append(dirs, f[0])
		}
	}
	return dirs
}

// a rerun after an interrupted init (init's own files, no commit) commits them; a file at init's path the user
// edited goes in as it is.
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
		if out, rc := x.runIn(t, dir, "obor-init"); rc != 0 {
			t.Fatalf("rerun: exit %d\n%s", rc, out)
		}
		if got := x.Git(dir, "show", "HEAD:BACKLOG.md"); got+"\n" != edited {
			t.Errorf("the committed BACKLOG.md is %q, want the edited file", got)
		}
	})
}

// obor uninstall on a real repository after obor init: the main checkout is the only worktree left, the task branch
// serve lists as merged is deleted, core.hooksPath is unset, and git status is as clean as it was before (nothing tracked is
// touched); the config stays and the second run changes nothing.
func TestUninstallAfterInitLeavesAPlainRepository(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, true)
	dir := x.Root
	x.init(t)
	x.Git(dir, "add", "-A")
	x.Git(dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "after init", "--no-verify")
	x.Git(dir, "branch", "task/T-9")
	if got := strings.Count(x.Git(dir, "worktree", "list"), "\n"); got < 2 {
		t.Fatalf("precondition: %d worktrees after obor init, want the main checkout and the lanes", got+1)
	}
	if x.Git(dir, "status", "--porcelain") != "" || x.Git(dir, "config", "--local", "--get", "core.hooksPath") == "" {
		t.Fatalf("precondition: status %q, hooks path %q", x.Git(dir, "status", "--porcelain"),
			x.Git(dir, "config", "--local", "--get", "core.hooksPath"))
	}
	down := &fakeDown{known: true, merged: []string{"task/T-9"}}
	run := func() (string, int) {
		s, out := x.newSetup(t)
		s.Dir = dir
		td := down.teardown()
		td.Pruner = func(cfg *config.Config) port.Pruner { return checks.GitPruner{Cfg: cfg} }
		s.Down = td
		rc := s.Run(context.Background(), []string{"obor-uninstall"})
		return out.String(), rc
	}

	out, rc := run()
	if rc != 0 {
		t.Fatalf("uninstall: exit %d\n%s", rc, out)
	}
	if got := strings.TrimSpace(x.Git(dir, "worktree", "list")); strings.Count(got, "\n") != 0 || !strings.HasPrefix(got, project.Physical(dir)) {
		t.Errorf("worktrees after uninstall:\n%s\nwant the main checkout only", got)
	}
	if got := x.Git(dir, "status", "--porcelain"); got != "" {
		t.Errorf("git status after uninstall:\n%s", got)
	}
	if got, err := x.gitOut(dir, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Errorf("core.hooksPath still %q", got)
	}
	if got := x.Git(dir, "branch", "--list", "task/*"); got != "" {
		t.Errorf("task branches left: %q", got)
	}
	has(t, out, "deleted task/T-9 (was ", "the branch line")
	if !x.Exists(".obor/config.json") {
		t.Error("the config is gone")
	}
	out, rc = run()
	if rc != 0 {
		t.Fatalf("second uninstall: exit %d\n%s", rc, out)
	}
	for step, state := range states(out) {
		if state == "done" {
			t.Errorf("second run: %s is done\n%s", step, out)
		}
	}
}
