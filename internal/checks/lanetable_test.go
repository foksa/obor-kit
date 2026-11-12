package checks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/spec"
	"obor/internal/testkit"
)

// The lane table's tests that need no git; the ones over real git are lanetable_integration_test.go's.

// A configured lane with no worktree is missing: no git read, no branch, no type. A plain project (no git): the lane
// table never asks git about a lane that is not there.
func TestMissingLane(t *testing.T) {
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("lanes.count", 1))
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if c.LaneCount() != 1 {
		t.Fatalf("precondition: lanes %d, want 1", c.LaneCount())
	}
	s := NewLaneTable(context.Background(), c).States()
	if len(s) != 1 || s[0].Name != "L-01" || s[0].State != "missing" || s[0].Branch != "" || s[0].Type != "" {
		t.Errorf("States = %+v", s)
	}
}

// spec.Match with the patterns GlobMatch's tests used, with what it returned.
func TestSpecMatchGlobs(t *testing.T) {
	for _, c := range []struct {
		path, glob string
		want       bool // GlobMatch's answer
	}{
		{"src/ui/a/b.ts", "src/ui/**", true}, {"a/b", "a/**/b", true}, {"a/x/y/b", "a/**/b", true},
		{"x", "**/x", true}, {"d/x", "**/x", true}, {"src/net/x", "src/ui/**", false}, {"a.go", "?.go", true},
		{"b.go", "[ab].go", true}, {"c.go", "[!ab].go", true}, {"a.go", "[!ab].go", false},
		{"a/b/c", "a/*", true}, {"ab", "a/**/b", false}, {"src/ui", "src/ui/**", false}, {"axb", "a.b", false},
	} {
		if got := spec.Match(c.path, c.glob); got != c.want {
			t.Errorf("spec.Match(%q, %q) = %v, GlobMatch said %v", c.path, c.glob, got, c.want)
		}
	}
}

// laneGit is a lane table's git for the dirt check's unit tests: dir is its own worktree at sha on branch and status
// is what git status --porcelain --untracked-files=all says. The main checkout tracks nothing: its
// .claude/settings.json is untracked, so WorktreeLinks links it. Any other call fails the test.
type laneGit struct {
	t      testing.TB
	dir    string
	sha    string
	branch string
	status string
}

func (g *laneGit) git(_ context.Context, dir string, args ...string) (string, error) {
	g.t.Helper()
	line := strings.Join(args, " ")
	switch {
	case line == "rev-parse --show-toplevel HEAD --symbolic-full-name HEAD":
		if dir != g.dir {
			g.t.Fatalf("rev-parse ran in %s, want the lane %s", dir, g.dir)
		}
		return g.dir + "\n" + g.sha + "\nrefs/heads/" + g.branch + "\n", nil
	case line == "status --porcelain --untracked-files=all":
		return g.status, nil
	case len(args) == 4 && args[0] == "ls-files" && args[1] == "--error-unmatch" && args[2] == "--":
		return "", errors.New("error: pathspec '" + args[3] + "' did not match any file(s) known to git")
	}
	g.t.Fatalf("lane table read git %s in %s; the fixture answers only the dirt check's calls", line, dir)
	return "", nil
}

// The lane dirt check keeps obor's own links out of a lane's state: an untracked entry exactly at a path obor links
// into the lane (lanes.links, or the .claude/settings.json project.WorktreeLinks adds while the main checkout does
// not track it) is obor's link, so it leaves the lane free; a tracked entry there is the lane's work, and so is any
// other untracked entry.
func TestLaneDirtSkipsOborLinks(t *testing.T) {
	for _, c := range []struct {
		name     string
		links    []any // lanes.links
		settings bool  // the main checkout's .claude/settings.json, which git does not track
		status   string
		want     string
		paths    []string
	}{
		{name: "the untracked settings link", settings: true, status: "?? .claude/settings.json\n", want: port.LaneFree},
		{name: "an untracked lanes.links entry", links: []any{"out/"}, status: "?? out/\n", want: port.LaneFree},
		{name: "a modified tracked file at a linked path", settings: true, status: " M .claude/settings.json\n",
			want: port.LaneDirtyFree, paths: []string{".claude/settings.json"}},
		{name: "a tracked-deleted file at a linked path", links: []any{"out/"}, status: " D out/\n",
			want: port.LaneDirtyFree, paths: []string{"out/"}},
		{name: "another untracked file", settings: true, status: "?? notes.txt\n", want: port.LaneDirtyFree,
			paths: []string{"notes.txt"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := testkit.NewPlain(t)
			dir := p.PlainLane("L-01", "task/T-01")
			if c.settings {
				p.Write(project.PluginSettings, "{}\n")
			}
			p.Config(testkit.Set("lanes.links", c.links))
			cfg, err := config.Load(p.Root)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Lanes()) != 1 || cfg.Lanes()[0].Dir != dir {
				t.Fatalf("precondition: lanes %+v, want L-01 at %s", cfg.Lanes(), dir)
			}
			g := &laneGit{t: t, dir: dir, sha: "abc123", branch: "task/T-01", status: c.status}
			tbl := NewLaneTable(context.Background(), cfg)
			tbl.Git = g.git
			if c.settings && !slices.Contains(tbl.linkedPaths(), project.PluginSettings) {
				t.Fatalf("precondition: %s is not a link obor makes (%v)", project.PluginSettings, tbl.linkedPaths())
			}
			s := tbl.LaneState(cfg.Lanes()[0])
			if s.State != c.want || !slices.Equal(s.Paths, c.paths) {
				t.Errorf("lane state %q, paths %v; want %q, %v", s.State, s.Paths, c.want, c.paths)
			}
		})
	}
}
