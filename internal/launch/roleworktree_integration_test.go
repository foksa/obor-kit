//go:build integration

package launch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/project"
	"obor/internal/testkit"
)

// setupGit is a real git project for the test of a git call (project.AddWorktree): the launcher adds the role
// worktree, which is absent. No lane: a lane no test reads would be a worktree add.
func setupGit(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	return newFx(t, testkit.New(t), "", edits...)
}

// The launcher's start of a role agent whose worktree is missing adds it with git (detached at the trunk) and links
// lanes.links into it: the env line cds into a worktree git knows, with the links its checkout lacks.
func TestRestartAddsTheMissingRoleWorktreeWithTheLinks(t *testing.T) {
	t.Parallel()
	x := setupGit(t, testkit.Set("lanes.links", `["local.env", ".claude/settings.json"]`))
	wt := x.p.LaneDir("integrator")
	if _, err := os.Stat(wt); err == nil {
		t.Fatalf("precondition: no role worktree %s", wt)
	}
	restartIntegratorWithLinks(t, x)
	if top := strings.TrimSpace(x.p.Git(wt, "rev-parse", "--show-toplevel")); project.Physical(top) != project.Physical(wt) {
		t.Fatalf("the role worktree %s was not added (toplevel %q)", wt, top)
	}
	linkedInto(t, x, wt, "local.env")
	linkedInto(t, x, wt, ".claude/settings.json")
}

// The main checkout tracks .claude/settings.json (lanes.links does not list it): a role worktree at a commit without
// the file is linked at the role's start, so its agent has obor's plugin; one at a commit that has it gets nothing
// linked, the tracked file is its own.
func TestRestartLinksTrackedSettingsOnlyIntoARoleWorktreeBehindTheCommit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		behind bool
	}{
		{"behind the commit that tracked it: linked", true},
		{"at a commit that has it: nothing linked", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setupGit(t)
			root, wt := x.p.Root, x.p.LaneDir("integrator")
			x.p.Git(root, "worktree", "add", "-q", "--detach", wt, "main") // before the commit below
			x.p.Write(filepath.Join(root, ".claude", "settings.json"), `{"main":true}`+"\n")
			x.p.Git(root, "add", "-f", ".claude/settings.json")
			x.p.Git(root, "commit", "-q", "-m", "track the settings")
			if !c.behind {
				x.p.Git(wt, "checkout", "-q", "--detach", "main")
			}
			if project.SettingsUntrackedWith(root, project.Git) {
				t.Fatal("precondition: the main checkout tracks the settings file")
			}
			tracked := x.p.Git(wt, "ls-tree", "--name-only", "HEAD", "--", ".claude/settings.json")
			if (tracked != "") != !c.behind {
				t.Fatalf("precondition: the role worktree's HEAD lists %q (behind %v)", tracked, c.behind)
			}
			restartIntegratorAsIs(t, x)
			fi, err := os.Lstat(filepath.Join(wt, project.PluginSettings))
			switch {
			case c.behind:
				settingsLinked(t, x, wt)
			case err != nil || !fi.Mode().IsRegular():
				t.Errorf("the role worktree's settings file: %v, %v; want git's regular file, no link", fi, err)
			}
		})
	}
}
