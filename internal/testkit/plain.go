package testkit

// The git-less project layout: a project and its worktrees laid out as files, in the shape project.Find
// reads. The root's .git is a directory; a worktree's .git is a file naming a gitdir: under
// <root>/.git/worktrees/<name>, and that gitdir holds the commondir (../..) and gitdir (the worktree's .git) files a
// git worktree has. So project.Find, project.LaneOf and project.DetectRole answer as they do in a real project, with
// no git process: for a test whose code under test reads the repository but runs no git. A test of a git call keeps
// testkit.New and Lane.

import (
	"os"
	"path/filepath"
	"testing"

	"obor/internal/roles"
)

// NewPlain is NewDir plus the shape of a repository project.Find reads as files: <root>/.git is the common dir, and
// every built-in role worktree (<lanes_dir>/<role>) is a linked worktree, so a launcher that would add one has
// nothing to run. Use Worktree (or PlainLane) for the lanes.
func NewPlain(t testing.TB) *Project {
	t.Helper()
	p := NewDir(t)
	if err := os.MkdirAll(filepath.Join(p.Root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, r := range roles.All() {
		if r.Cwd == roles.CwdRoleWorktree {
			p.Worktree(p.LaneDir(r.Name), "")
		}
	}
	return p
}

// PlainLane is Lane for a plain project (NewPlain): <Base>/proj-wt/<name> as a linked worktree, its branch ("" detached)
// noted for laneTask.
func (p *Project) PlainLane(name, branch string) string {
	p.t.Helper()
	return p.Worktree(p.LaneDir(name), branch)
}

// Worktree lays dir out as a linked worktree of p's repository, as files: dir/.git names
// <root>/.git/worktrees/<name>, which holds the commondir (../..) and gitdir (dir/.git) files git and the guard's
// worktree list read. branch ("" detached) is what laneTask reads for a lane. A plain project only: a git project
// makes worktrees with git (testkit.New and Lane).
func (p *Project) Worktree(dir, branch string) string {
	p.t.Helper()
	if !p.plain {
		p.t.Fatalf("worktree %s in a git project (testkit.New and Lane make worktrees)", dir)
	}
	name := filepath.Base(dir)
	gitDir := filepath.Join(p.Root, ".git", "worktrees", name)
	p.Write(filepath.Join(dir, ".git"), "gitdir: "+gitDir+"\n")
	p.Write(filepath.Join(gitDir, "commondir"), "../..\n")
	p.Write(filepath.Join(gitDir, "gitdir"), filepath.Join(dir, ".git")+"\n")
	p.branches[name] = branch
	p.enablePlugin(dir)
	return dir
}
