package project

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
)

// plainProject is a main checkout that is no git repository (git tracks nothing in it) with cfg as its config, and
// an empty worktree dir beside it.
func plainProject(t *testing.T, cfg string) (c *config.Config, dir string) {
	t.Helper()
	base := Physical(t.TempDir())
	root := filepath.Join(base, "proj")
	put(t, filepath.Join(root, ".obor", "config.json"), cfg)
	dir = filepath.Join(base, "wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linksTo fails unless dir/link is a symlink to root/link.
func linksTo(t *testing.T, root, dir, link string) {
	t.Helper()
	got, err := os.Readlink(filepath.Join(dir, link))
	if want := filepath.Join(root, link); err != nil || got != want {
		t.Errorf("%s: link to %q (%v), want a link to %s", link, got, err, want)
	}
}

// Whatever is at an entry's path in the worktree stays: the worktree's own file, and a link to somewhere else.
func TestLinkIntoLeavesAnExistingDestinationAsItIs(t *testing.T) {
	t.Parallel()
	cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
	put(t, filepath.Join(cfg.Root, "own.env"), "main\n")
	put(t, filepath.Join(cfg.Root, "link.env"), "main\n")
	put(t, filepath.Join(dir, "own.env"), "mine\n")
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Symlink(elsewhere, filepath.Join(dir, "link.env")); err != nil {
		t.Fatal(err)
	}
	linked, err := LinkInto(cfg.Root, dir, []string{"own.env", "link.env"})
	if err != nil || len(linked) != 0 {
		t.Fatalf("LinkInto = %q, %v; want nothing linked, no error", linked, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "own.env"))
	if fi, lerr := os.Lstat(filepath.Join(dir, "own.env")); err != nil || lerr != nil || !fi.Mode().IsRegular() || string(b) != "mine\n" {
		t.Errorf("the worktree's own file changed: %q (%v, %v), want the regular file \"mine\\n\"", b, err, lerr)
	}
	if got, err := os.Readlink(filepath.Join(dir, "link.env")); err != nil || got != elsewhere {
		t.Errorf("the worktree's link changed: %q (%v), want %s", got, err, elsewhere)
	}
}

// A main checkout without a settings file: nothing is added to lanes.links, and linking is no error.
func TestWorktreeLinksAddsNothingWithoutASettingsFile(t *testing.T) {
	t.Parallel()
	cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":["local.env"]}}`)
	put(t, filepath.Join(cfg.Root, "local.env"), "A=1\n")
	if _, err := os.Stat(filepath.Join(cfg.Root, PluginSettings)); err == nil {
		t.Fatal("precondition: the main checkout has no settings file")
	}
	links := WorktreeLinks(cfg)
	if want := []string{"local.env"}; !reflect.DeepEqual(links, want) {
		t.Fatalf("WorktreeLinks = %q, want %q", links, want)
	}
	linked, err := LinkInto(cfg.Root, dir, links)
	if err != nil || !reflect.DeepEqual(linked, []string{"local.env"}) {
		t.Errorf("LinkInto = %q, %v; want local.env linked, no error", linked, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".claude")); err == nil {
		t.Error("the worktree got a .claude directory for a settings file the main checkout does not have")
	}
}

// The With forms answer from the git query they are given: tracked → no settings link; untracked and present → the
// settings file linked to the main checkout's copy; listed in lanes.links → once.
func TestWorktreeLinksWithAsksTheQueryItIsGiven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     string
		tracked bool
		want    []string
		queries []string
	}{
		{"tracked: no settings link", `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":["local.env"]}}`,
			true, []string{"local.env"}, []string{"ls-files --error-unmatch -- " + PluginSettings}},
		{"untracked and present: linked", `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":["local.env"]}}`,
			false, []string{"local.env", PluginSettings}, []string{"ls-files --error-unmatch -- " + PluginSettings}},
		{"listed in lanes.links: once", `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":[".claude/settings.json"]}}`,
			false, []string{PluginSettings}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir := plainProject(t, tt.cfg)
			put(t, filepath.Join(cfg.Root, "local.env"), "A=1\n")
			put(t, filepath.Join(cfg.Root, PluginSettings), "{}\n")
			untracked := errors.New("path not in the index")
			var asked []string
			git := func(gotDir string, args ...string) (string, error) {
				if gotDir != cfg.Root {
					t.Errorf("query in %s, want the main checkout %s", gotDir, cfg.Root)
				}
				asked = append(asked, strings.Join(args, " "))
				if tt.tracked {
					return PluginSettings + "\n", nil
				}
				return "", untracked
			}
			links := WorktreeLinksWith(cfg, git)
			if !reflect.DeepEqual(links, tt.want) {
				t.Fatalf("WorktreeLinksWith = %q, want %q", links, tt.want)
			}
			if !reflect.DeepEqual(asked, tt.queries) {
				t.Errorf("queries = %q, want %q", asked, tt.queries)
			}
			if got, want := SettingsUntrackedWith(cfg.Root, git), !tt.tracked; got != want {
				t.Errorf("SettingsUntrackedWith = %v, want %v", got, want)
			}
			linked, err := LinkInto(cfg.Root, dir, links)
			if err != nil || !reflect.DeepEqual(linked, tt.want) {
				t.Fatalf("LinkInto = %q, %v; want %q", linked, err, tt.want)
			}
			if slices.Contains(tt.want, PluginSettings) {
				linksTo(t, cfg.Root, dir, PluginSettings)
			} else if _, err := os.Lstat(filepath.Join(dir, PluginSettings)); err == nil {
				t.Errorf("the worktree has the settings file, though the links %q do not list it", tt.want)
			}
		})
	}
}

// An entry that cannot be linked (its parent in the worktree is not writable) does not stop the next one: the second
// is linked and the error names the first.
func TestLinkIntoLinksTheSecondEntryWhenTheFirstFailsAndNamesTheFirst(t *testing.T) {
	t.Parallel()
	cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
	put(t, filepath.Join(cfg.Root, "ro", "first.env"), "1\n")
	put(t, filepath.Join(cfg.Root, "second.env"), "2\n")
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	if err := os.Symlink("x", filepath.Join(ro, "probe")); err == nil {
		t.Fatal("precondition: the first entry's parent is not writable")
	}
	linked, err := LinkInto(cfg.Root, dir, []string{"ro/first.env", "second.env"})
	if !reflect.DeepEqual(linked, []string{"second.env"}) {
		t.Errorf("linked = %q, want [second.env]", linked)
	}
	linksTo(t, cfg.Root, dir, "second.env")
	if first := filepath.Join(dir, "ro", "first.env"); err == nil || !strings.Contains(err.Error(), "cannot link "+first) {
		t.Errorf("err = %v, want it to name %s", err, first)
	}
}

// ExcludeLinks adds each link's anchored line under obor's comment line once: a rerun leaves the file byte for byte
// as it was, and the lines an existing info/exclude has (git's own comments, the owner's patterns) are kept.
func TestExcludeLinksAddsOnceAndKeepsTheFile(t *testing.T) {
	t.Parallel()
	common := t.TempDir()
	path := filepath.Join(common, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const before = "# git ls-files --others --exclude-from=.git/info/exclude\n*.swp" // no trailing newline
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	links := []string{PluginSettings, "out/", ""}
	if err := ExcludeLinks(common, links); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := before + "\n" + excludeMark + "\n/.claude/settings.json\n/out\n"
	if string(b) != want {
		t.Fatalf("info/exclude:\n%s\nwant:\n%s", b, want)
	}
	if err := ExcludeLinks(common, links); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != want {
		t.Errorf("rerun changed info/exclude:\n%s\nwant:\n%s", again, want)
	}
	// a new link later joins the file once, obor's comment line not repeated
	if err := ExcludeLinks(common, append(links, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if more, _ := os.ReadFile(path); string(more) != want+"/node_modules\n" {
		t.Errorf("a new link:\n%s\nwant:\n%s", more, want+"/node_modules\n")
	}
}

// With no info/exclude yet (and no info dir), ExcludeLinks makes it.
func TestExcludeLinksMakesTheFile(t *testing.T) {
	t.Parallel()
	common := t.TempDir()
	if err := ExcludeLinks(common, []string{PluginSettings}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	if want := excludeMark + "\n/.claude/settings.json\n"; err != nil || string(b) != want {
		t.Errorf("info/exclude %q, %v; want %q", b, err, want)
	}
}
