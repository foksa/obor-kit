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
// are worktrees, so init links it (lane_links, .gitignore) and each lane sees the enabled plugin. A rerun changes
// nothing.
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
	}
	if got := cfg.Data.Lanes.Links; len(got) != 1 || got[0] != ".claude/settings.json" {
		t.Errorf("lane_links = %q, want [.claude/settings.json]", got)
	}
	if !strings.Contains(x.Read(".gitignore"), "\n.claude/settings.json\n") {
		t.Errorf(".gitignore lacks the link:\n%s", x.Read(".gitignore"))
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
		t.Errorf("lane_links %q, plugin-links %q; want none, already", cfg.Data.Lanes.Links, summary(out, "plugin-links"))
	}
	if !config.PluginEnabledIn(filepath.Join(cfg.LanesDir(), "L-01")) {
		t.Error("L-01 does not get the tracked file from git")
	}
}
