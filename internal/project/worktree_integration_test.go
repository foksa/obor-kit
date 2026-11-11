//go:build integration

package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"obor/internal/config"
)

// An untracked settings file that lanes.links lists is in the list once, and is linked.
func TestWorktreeLinksListsAListedUntrackedSettingsFileOnceAndItIsLinked(t *testing.T) {
	t.Parallel()
	cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":["local.env",".claude/settings.json"]}}`)
	put(t, filepath.Join(cfg.Root, PluginSettings), "{}\n")
	if !SettingsUntrackedWith(cfg.Root, Git) {
		t.Fatal("precondition: the main checkout has an untracked settings file")
	}
	links := WorktreeLinks(cfg)
	if want := []string{"local.env", PluginSettings}; !reflect.DeepEqual(links, want) {
		t.Fatalf("WorktreeLinks = %q, want %q (the settings file once)", links, want)
	}
	linked, err := LinkInto(cfg.Root, dir, links)
	if err != nil || !reflect.DeepEqual(linked, []string{PluginSettings}) {
		t.Fatalf("LinkInto = %q, %v; want the settings file linked", linked, err)
	}
	linksTo(t, cfg.Root, dir, PluginSettings)
}

// A settings file git tracks is not added to the links: a worktree has it from git, as a regular file.
func TestWorktreeLinksLeavesATrackedSettingsFileToGit(t *testing.T) {
	root, wt := fixture(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":["local.env"]}}`)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, PluginSettings), "{}\n")
	if got, want := WorktreeLinks(cfg), []string{"local.env", PluginSettings}; !reflect.DeepEqual(got, want) {
		t.Fatalf("precondition: untracked, the settings file is added: %q, want %q", got, want)
	}
	run(t, root, "git", "add", PluginSettings)
	run(t, root, "git", "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "settings")
	links := WorktreeLinks(cfg)
	if want := []string{"local.env"}; !reflect.DeepEqual(links, want) {
		t.Fatalf("WorktreeLinks = %q, want %q (a tracked settings file is not added)", links, want)
	}
	dir := filepath.Join(wt, "new")
	if added, err := AddWorktree(context.Background(), root, dir, "main"); err != nil || !added {
		t.Fatalf("AddWorktree: %v, %v", added, err)
	}
	if linked, err := LinkInto(root, dir, links); err != nil || len(linked) != 0 {
		t.Errorf("LinkInto = %q, %v; want nothing linked", linked, err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, PluginSettings)); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("the worktree's settings file: %v, %v; want the regular file git checked out", fi, err)
	}
}
