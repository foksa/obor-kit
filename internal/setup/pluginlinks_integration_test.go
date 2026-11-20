//go:build integration

// Whether git tracks .claude/settings.json decides the lane link.

package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
)

// obor init in a fresh project leaves .claude/settings.json untracked in the main checkout; the lanes
// are worktrees, so init links it (lanes.links; never .gitignore or git's info/exclude) and each lane sees the
// enabled plugin with no dirt (project.LaneDirt: the link is obor's). A rerun changes nothing.
func TestOborInitLinksThePluginIntoTheLanes(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, false)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if out := x.Git(x.Root, "ls-files", "--", ".claude/settings.json"); strings.TrimSpace(out) != "" {
		t.Fatalf("precondition: .claude/settings.json is untracked in the main checkout, git lists %q", out)
	}
	if !config.PluginEnabledIn(x.Root) {
		t.Fatal("precondition: the plugin is enabled in the main checkout")
	}
	for _, lane := range []string{"L-01", "L-02"} {
		dir := filepath.Join(cfg.LanesDir(), lane)
		if !config.PluginEnabledIn(dir) {
			t.Errorf("lane %s's worktree does not see the enabled plugin", lane)
		}
		if dirt, err := project.LaneDirt(cfg, dir, project.Git); err != nil || len(dirt) != 0 {
			t.Errorf("lane %s's dirt %q, %v; want none: the link is obor's", lane, dirt, err)
		}
	}
	common := strings.TrimSpace(x.Git(x.Root, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if b, _ := os.ReadFile(filepath.Join(common, "info", "exclude")); strings.Contains(string(b), ".claude/settings.json") {
		t.Errorf("info/exclude names the link:\n%s", b)
	}
	if got := cfg.Data.Lanes.Links; len(got) != 1 || got[0] != ".claude/settings.json" {
		t.Errorf("lanes.links = %q, want [.claude/settings.json]", got)
	}
	if strings.Contains(x.Read(".gitignore"), ".claude/settings.json") {
		t.Errorf(".gitignore names the link:\n%s", x.Read(".gitignore"))
	}
	if stateOf(out, "plugin-links") != "done" {
		t.Errorf("plugin-links step %q, want done", summary(out, "plugin-links"))
	}
	// the rerun is the repair path and changes nothing
	before := x.Read(".obor/config.json")
	out = x.init(t)
	if stateOf(out, "plugin-links") != "already" || x.Read(".obor/config.json") != before {
		t.Errorf("rerun: plugin-links %q, config changed %v", summary(out, "plugin-links"), x.Read(".obor/config.json") != before)
	}
}

// The role worktrees init makes (the integrator's, the reviewer's) get the untracked settings file as a link too: a
// role agent reads the project's settings only, so without the link it starts without obor's plugin.
func TestOborInitLinksThePluginIntoTheRoleWorktrees(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, false)
	x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if out := x.Git(x.Root, "ls-files", "--", ".claude/settings.json"); strings.TrimSpace(out) != "" {
		t.Fatalf("precondition: .claude/settings.json is untracked in the main checkout, git lists %q", out)
	}
	for _, role := range []string{"integrator", "reviewer"} {
		dir := cfg.RoleWorktree(role)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Fatalf("precondition: init made %s's worktree %s: %v", role, dir, err)
		}
		got, err := os.Readlink(filepath.Join(dir, ".claude", "settings.json"))
		if want := filepath.Join(x.Root, ".claude", "settings.json"); err != nil || project.Physical(got) != project.Physical(want) {
			t.Errorf("%s's worktree: .claude/settings.json links to %q (%v), want %s", role, got, err, want)
		}
		if !config.PluginEnabledIn(dir) {
			t.Errorf("%s's worktree does not see the enabled plugin", role)
		}
	}
}

// A settings file git tracks reaches every worktree by itself: init adds no link for it.
func TestOborInitLeavesATrackedSettingsFileToGit(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, false)
	x.Write(filepath.Join(x.Root, ".claude", "settings.json"), `{"enabledPlugins":{"obor@obor-kit":true}}`+"\n")
	x.Git(x.Root, "add", ".claude/settings.json")
	x.Git(x.Root, "commit", "-q", "-m", "settings")
	out := x.init(t)
	cfg, _ := config.Load(x.Root)
	if len(cfg.Data.Lanes.Links) != 0 || stateOf(out, "plugin-links") != "already" {
		t.Errorf("lanes.links %q, plugin-links %q; want none, already", cfg.Data.Lanes.Links, summary(out, "plugin-links"))
	}
	if !config.PluginEnabledIn(filepath.Join(cfg.LanesDir(), "L-01")) {
		t.Error("L-01 does not get the tracked file from git")
	}
}

// lanes.links keeps .claude/settings.json after the project commits it: init says the link is no longer needed
// (one WARN line) and leaves the config as it is; before the commit it says nothing.
func TestOborInitNotesALinkTheMainCheckoutTracks(t *testing.T) {
	t.Parallel()
	const warn = "WARN lanes.links .claude/settings.json is tracked: the link is no longer needed"
	x := newGitInitFx(t, false)
	if out := x.init(t); strings.Contains(out, "WARN lanes.links") {
		t.Fatalf("untracked settings file: init warns of its link:\n%s", out)
	}
	cfg, err := config.Load(x.Root)
	if err != nil || len(cfg.Data.Lanes.Links) != 1 {
		t.Fatalf("precondition: lanes.links = %q (%v), want the settings link", cfg.Data.Lanes.Links, err)
	}
	x.Git(x.Root, "add", "-f", ".claude/settings.json")
	x.Git(x.Root, "commit", "-q", "-m", "settings")
	before := x.Read(".obor/config.json")
	out := x.init(t)
	if strings.Count(out, warn) != 1 {
		t.Errorf("tracked settings file: want the line %q once, init printed:\n%s", warn, out)
	}
	if x.Read(".obor/config.json") != before {
		t.Error("init edited the config for the owner")
	}
}
