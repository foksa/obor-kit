package setup

import (
	"os"
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
	x := newInitFx(t, false)
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
	for _, lane := range []string{"O-01", "S-01"} {
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

// A lane that existed before the plugin was installed (the bug's shape: init made the lanes, the file came after) gets
// the link on a rerun of init.
func TestOborInitRerunLinksIntoAnExistingLane(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, false)
	x.init(t)
	cfg, _ := config.Load(x.Root)
	link := filepath.Join(cfg.LanesDir(), "S-01", ".claude", "settings.json")
	if err := os.Remove(link); err != nil {
		t.Fatalf("precondition: S-01 holds the link: %v", err)
	}
	if config.PluginEnabledIn(filepath.Dir(filepath.Dir(link))) {
		t.Fatal("precondition: S-01 no longer sees the plugin")
	}
	out := x.init(t)
	if !config.PluginEnabledIn(filepath.Dir(filepath.Dir(link))) || stateOf(out, "plugin-links") != "done" {
		t.Errorf("S-01 sees the plugin %v, plugin-links %q",
			config.PluginEnabledIn(filepath.Dir(filepath.Dir(link))), summary(out, "plugin-links"))
	}
}

// A settings file git tracks reaches every worktree by itself: init adds no link for it.
func TestOborInitLeavesATrackedSettingsFileToGit(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, false)
	x.Write(filepath.Join(x.Root, ".claude", "settings.json"), `{"enabledPlugins":{"obor@obor-kit":true}}`+"\n")
	x.Git(x.Root, "add", ".claude/settings.json")
	x.Git(x.Root, "commit", "-q", "-m", "settings")
	out := x.init(t)
	cfg, _ := config.Load(x.Root)
	if len(cfg.Data.LaneLinks) != 0 || stateOf(out, "plugin-links") != "already" {
		t.Errorf("lane_links %q, plugin-links %q; want none, already", cfg.Data.LaneLinks, summary(out, "plugin-links"))
	}
	if !config.PluginEnabledIn(filepath.Join(cfg.LanesDir(), "O-01")) {
		t.Error("O-01 does not get the tracked file from git")
	}
}

// obor setup lane links an untracked .claude/settings.json even when lane_links does not list it (a project set up
// before this).
func TestSetupLaneLinksAnUntrackedPluginSettings(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.Write(filepath.Join(x.Root, ".claude", "settings.json"), `{"enabledPlugins":{"obor@obor-kit":true}}`+"\n")
	x.ok(t, "lane", "O-01", "opus")
	if !config.PluginEnabledIn(x.LaneDir("O-01")) {
		t.Error("the new lane does not see the enabled plugin")
	}
}

// T-705 item 2: obor setup pane without --start labels the pane, types the env and starts no agent; --start starts it.
func TestPaneWithoutStartStartsNoAgent(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "O-01", "opus")
	x.reset()
	out := x.ok(t, "pane", "O-01", "--tab", "w1:t1")
	calls := strings.Join(x.reqs(), "\n")
	if !strings.Contains(calls, "pane.split") || !strings.Contains(calls, "env={OBOR_AGENT=pj-o-01,OBOR_LANE=O-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker}") ||
		!strings.Contains(calls, "pane.rename label=pj-o-01 pane_id=w1:n1") {
		t.Errorf("the pane is split with the env and labelled:\n%s", calls)
	}
	if strings.Contains(calls, "agent.start") {
		t.Errorf("an agent was started without --start:\n%s", calls)
	}
	if p := x.h.Pane("w1:n1"); p == nil || p.Label != "pj-o-01" || p.Name != "" || p.Kind != "" {
		t.Errorf("pane: %+v, want labelled pj-o-01 and no agent", p)
	}
	if !strings.Contains(out, "no agent started") {
		t.Errorf("output does not say no agent was started:\n%s", out)
	}
	// a second run reuses the labelled pane
	x.reset()
	x.ok(t, "pane", "O-01", "--tab", "w1:t1")
	if strings.Contains(strings.Join(x.reqs(), "\n"), "pane.split") {
		t.Errorf("a rerun split another pane:\n%s", strings.Join(x.reqs(), "\n"))
	}
	// --start starts the agent in the labelled pane
	x.reset()
	x.ok(t, "pane", "O-01", "--pane", "w1:n1", "--start")
	if p := x.h.Pane("w1:n1"); p == nil || p.Name != "pj-o-01" || p.Kind != "claude" {
		t.Errorf("with --start the pane holds %+v", p)
	}
}
