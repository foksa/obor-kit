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

// noHead is the git of a dir that is no repository: every query fails, so LinkInto links as without a HEAD.
func noHead(string, ...string) (string, error) { return "", errors.New("fatal: not a git repository") }

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
	linked, err := LinkInto(cfg.Root, dir, []string{"own.env", "link.env"}, func(d string, args ...string) (string, error) {
		t.Errorf("git %q in %s: nothing is missing, nothing to ask", args, d)
		return "", nil
	})
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
	links := WorktreeLinks(cfg, dir)
	if want := []string{"local.env"}; !reflect.DeepEqual(links, want) {
		t.Fatalf("WorktreeLinks = %q, want %q", links, want)
	}
	linked, err := LinkInto(cfg.Root, dir, links, noHead)
	if err != nil || !reflect.DeepEqual(linked, []string{"local.env"}) {
		t.Errorf("LinkInto = %q, %v; want local.env linked, no error", linked, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".claude")); err == nil {
		t.Error("the worktree got a .claude directory for a settings file the main checkout does not have")
	}
}

// The With forms answer from the git queries they are given: the main checkout's file untracked → the settings file
// linked to the main checkout's copy; tracked → linked only into a worktree whose HEAD lacks the path (one behind the
// commit that tracked it); listed in lanes.links → once, nothing asked. A worktree that lacks the path is not asked
// by WorktreeLinksWith (the list has the file): LinkInto's HEAD query decides, so a tracked file the worktree deleted
// is not linked.
func TestWorktreeLinksWithAsksTheQueriesItIsGiven(t *testing.T) {
	t.Parallel()
	const cfgJSON = `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":["local.env"]}}`
	lacks := "main: ls-files --error-unmatch -- " + PluginSettings
	at := "dir: ls-tree --name-only HEAD -- " + PluginSettings
	tests := []struct {
		name       string
		cfg        string
		tracked    bool // the main checkout tracks the settings file
		dirTracks  bool // the worktree's HEAD has it
		dirHas     bool // the worktree has a file at its path (git's checkout, or its own)
		want       []string
		linked     []string
		queries    []string // WorktreeLinksWith's
		noSettings bool
	}{
		{name: "untracked in main: linked", cfg: cfgJSON, want: []string{"local.env", PluginSettings},
			linked: []string{"local.env", PluginSettings}},
		{name: "untracked in main, the worktree's own file there: listed, kept", cfg: cfgJSON, dirHas: true,
			want: []string{"local.env", PluginSettings}, linked: []string{"local.env"}, queries: []string{lacks}},
		{name: "tracked, the worktree's HEAD has it: no settings link", cfg: cfgJSON, tracked: true, dirTracks: true, dirHas: true,
			want: []string{"local.env"}, linked: []string{"local.env"}, queries: []string{lacks, at}},
		{name: "tracked, the worktree's HEAD has it, deleted there: listed, not linked", cfg: cfgJSON, tracked: true, dirTracks: true,
			want: []string{"local.env", PluginSettings}, linked: []string{"local.env"}},
		{name: "tracked, the worktree is behind the commit that tracked it: linked", cfg: cfgJSON, tracked: true,
			want: []string{"local.env", PluginSettings}, linked: []string{"local.env", PluginSettings}},
		{name: "listed in lanes.links: once", cfg: `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"links":[".claude/settings.json"]}}`,
			tracked: true, want: []string{PluginSettings}, linked: []string{PluginSettings}},
		{name: "no settings file in main: none", cfg: cfgJSON, noSettings: true, tracked: true,
			want: []string{"local.env"}, linked: []string{"local.env"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir := plainProject(t, tt.cfg)
			put(t, filepath.Join(cfg.Root, "local.env"), "A=1\n")
			if !tt.noSettings {
				put(t, filepath.Join(cfg.Root, PluginSettings), "{}\n")
			}
			if tt.dirHas {
				put(t, filepath.Join(dir, PluginSettings), "the worktree's\n")
			}
			var asked []string
			git := func(gotDir string, args ...string) (string, error) {
				switch gotDir {
				case cfg.Root:
					asked = append(asked, "main: "+strings.Join(args, " "))
					if tt.tracked {
						return PluginSettings + "\n", nil
					}
					return "", errors.New("path not in the index")
				case dir:
					asked = append(asked, "dir: "+strings.Join(args, " "))
					if tt.dirTracks {
						return PluginSettings + "\n", nil
					}
					return "", nil // ls-tree names nothing for a path HEAD lacks
				}
				t.Fatalf("query in %s, want the main checkout %s or the worktree %s", gotDir, cfg.Root, dir)
				return "", nil
			}
			links := WorktreeLinksWith(cfg, dir, git)
			if !reflect.DeepEqual(links, tt.want) {
				t.Fatalf("WorktreeLinksWith = %q, want %q", links, tt.want)
			}
			if !reflect.DeepEqual(asked, tt.queries) {
				t.Errorf("queries = %q, want %q", asked, tt.queries)
			}
			if got, want := SettingsUntrackedWith(cfg.Root, git), !tt.tracked && !tt.noSettings; got != want {
				t.Errorf("SettingsUntrackedWith = %v, want %v", got, want)
			}
			linked, err := LinkInto(cfg.Root, dir, links, git)
			if err != nil || !reflect.DeepEqual(linked, tt.linked) {
				t.Fatalf("LinkInto = %q, %v; want %q", linked, err, tt.linked)
			}
			fi, err := os.Lstat(filepath.Join(dir, PluginSettings))
			switch {
			case slices.Contains(tt.linked, PluginSettings):
				linksTo(t, cfg.Root, dir, PluginSettings)
			case tt.dirHas:
				if err != nil || !fi.Mode().IsRegular() {
					t.Errorf("the worktree's file at %s: %v, %v; want it kept, a regular file", PluginSettings, fi, err)
				}
			case err == nil:
				t.Errorf("the worktree has the settings file, though LinkInto linked only %q", tt.linked)
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
	linked, err := LinkInto(cfg.Root, dir, []string{"ro/first.env", "second.env"}, noHead)
	if !reflect.DeepEqual(linked, []string{"second.env"}) {
		t.Errorf("linked = %q, want [second.env]", linked)
	}
	linksTo(t, cfg.Root, dir, "second.env")
	if first := filepath.Join(dir, "ro", "first.env"); err == nil || !strings.Contains(err.Error(), "cannot link "+first) {
		t.Errorf("err = %v, want it to name %s", err, first)
	}
}

// LinkInto asks git whether the worktree's HEAD tracks a path only when an entry is missing, once for all of them:
// a missing entry HEAD tracks (a file, a directory HEAD has a file under) stays missing, the others are linked; with
// every entry there, no query at all.
func TestLinkIntoAsksHEADOnceOnlyForMissingEntries(t *testing.T) {
	t.Parallel()
	t.Run("entries missing: one ls-tree of them, the tracked ones not linked", func(t *testing.T) {
		t.Parallel()
		cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
		for _, f := range []string{"tracked.env", "conf/a.env", "free.env", "there.env"} {
			put(t, filepath.Join(cfg.Root, f), "main\n")
		}
		put(t, filepath.Join(dir, "there.env"), "mine\n")
		var asked []string
		git := func(d string, args ...string) (string, error) {
			asked = append(asked, d+": "+strings.Join(args, " "))
			return "tracked.env\nconf/a.env\n", nil
		}
		linked, err := LinkInto(cfg.Root, dir, []string{"tracked.env", "conf/", "free.env", "there.env"}, git)
		if err != nil || !reflect.DeepEqual(linked, []string{"free.env"}) {
			t.Errorf("LinkInto = %q, %v; want only free.env linked", linked, err)
		}
		if want := []string{dir + ": ls-tree --name-only HEAD -- tracked.env conf/ free.env"}; !reflect.DeepEqual(asked, want) {
			t.Errorf("git calls %q, want %q", asked, want)
		}
		for _, f := range []string{"tracked.env", "conf"} {
			if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
				t.Errorf("%s: HEAD tracks it, yet the worktree has it", f)
			}
		}
		linksTo(t, cfg.Root, dir, "free.env")
	})
	t.Run("nothing missing: no query", func(t *testing.T) {
		t.Parallel()
		cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
		put(t, filepath.Join(cfg.Root, "a.env"), "main\n")
		put(t, filepath.Join(dir, "a.env"), "mine\n")
		calls := 0
		git := func(string, ...string) (string, error) { calls++; return "", nil }
		linked, err := LinkInto(cfg.Root, dir, []string{"a.env", "absent.env"}, git)
		if err != nil || len(linked) != 0 {
			t.Errorf("LinkInto = %q, %v; want nothing linked", linked, err)
		}
		if calls != 0 {
			t.Errorf("%d git calls, want none", calls)
		}
	})
	t.Run("no HEAD: every missing entry linked", func(t *testing.T) {
		t.Parallel()
		cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
		put(t, filepath.Join(cfg.Root, "a.env"), "main\n")
		calls := 0
		git := func(string, ...string) (string, error) {
			calls++
			return "", errors.New("fatal: Not a valid object name HEAD")
		}
		linked, err := LinkInto(cfg.Root, dir, []string{"a.env"}, git)
		if err != nil || !reflect.DeepEqual(linked, []string{"a.env"}) || calls != 1 {
			t.Errorf("LinkInto = %q, %v after %d git calls; want a.env linked after one", linked, err, calls)
		}
	})
}

// LaneDirt counts what is the lane's at a link path and elsewhere: a regular untracked file at the settings link's
// path, a tracked file modified there and another untracked file are each dirty, its path named. None of them is a
// symlink, so the link set's query in the main checkout is never asked.
func TestLaneDirtCountsTheLanesOwnFiles(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status string
		want   []string
	}{
		{"a regular untracked file at a link path", "?? .claude/settings.json\n", []string{PluginSettings}},
		{"a tracked file modified at a link path", " M .claude/settings.json\n", []string{PluginSettings}},
		{"another untracked file", "?? notes.txt\n", []string{"notes.txt"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir := plainProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
			put(t, filepath.Join(cfg.Root, PluginSettings), "{}\n")
			put(t, filepath.Join(dir, PluginSettings), "{}\n")
			put(t, filepath.Join(dir, "notes.txt"), "x\n")
			var asked []string
			git := func(gotDir string, args ...string) (string, error) {
				line := strings.Join(args, " ")
				asked = append(asked, line)
				if gotDir == dir && line == "status --porcelain --untracked-files=all" {
					return tt.status, nil
				}
				if gotDir == cfg.Root && line == "ls-files --error-unmatch -- "+PluginSettings {
					return "", errors.New("path not in the index")
				}
				t.Fatalf("git %s in %s: not a call LaneDirt makes", line, gotDir)
				return "", nil
			}
			if links := WorktreeLinksWith(cfg, dir, git); !slices.Contains(links, PluginSettings) {
				t.Fatalf("precondition: %s is not in the link set %q", PluginSettings, links)
			}
			asked = nil
			got, err := LaneDirt(cfg, dir, git)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LaneDirt = %q, %v; want %q", got, err, tt.want)
			}
			if want := []string{"status --porcelain --untracked-files=all"}; !reflect.DeepEqual(asked, want) {
				t.Errorf("git calls %q, want only %q", asked, want)
			}
		})
	}
}
