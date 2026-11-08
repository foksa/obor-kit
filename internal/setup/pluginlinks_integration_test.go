//go:build integration

// T-705 on real git (T-1021): whether git tracks .claude/settings.json decides the lane link.

package setup

import (
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
)

// T-705 item 1: obor init in a fresh project leaves .claude/settings.json untracked in the main checkout; the lanes
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
	if got := cfg.Data.LaneLinks; len(got) != 1 || got[0] != ".claude/settings.json" {
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

// A settings file git tracks reaches every worktree by itself: init adds no link for it.
func TestOborInitLeavesATrackedSettingsFileToGit(t *testing.T) {
	t.Parallel()
	x := newGitInitFx(t, false)
	x.Write(filepath.Join(x.Root, ".claude", "settings.json"), `{"enabledPlugins":{"obor@obor-kit":true}}`+"\n")
	x.Git(x.Root, "add", ".claude/settings.json")
	x.Git(x.Root, "commit", "-q", "-m", "settings")
	out := x.init(t)
	cfg, _ := config.Load(x.Root)
	if len(cfg.Data.LaneLinks) != 0 || stateOf(out, "plugin-links") != "already" {
		t.Errorf("lane_links %q, plugin-links %q; want none, already", cfg.Data.LaneLinks, summary(out, "plugin-links"))
	}
	if !config.PluginEnabledIn(filepath.Join(cfg.LanesDir(), "L-01")) {
		t.Error("L-01 does not get the tracked file from git")
	}
}

// obor setup lane links an untracked .claude/settings.json even when lane_links does not list it (a project set up
// before this).
func TestSetupLaneLinksAnUntrackedPluginSettings(t *testing.T) {
	t.Parallel()
	x := newGitFx(t)
	x.Write(filepath.Join(x.Root, ".claude", "settings.json"), `{"enabledPlugins":{"obor@obor-kit":true}}`+"\n")
	x.ok(t, "lane", "L-01")
	if !config.PluginEnabledIn(x.LaneDir("L-01")) {
		t.Error("the new lane does not see the enabled plugin")
	}
}
