//go:build integration

package checks

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/project"
)

// An upgraded project's lane holds a symlink to the main checkout's untracked, non-ignored file of the same path (the
// settings link, or a lanes.links entry) from before the upgrade, and no linker has run on the new binary: no
// info/exclude line, no LinkInto. The lane is free in the lane table, the worker's submission commits its work but
// never the link, its lane test runs green and the test's cleanup (reset --hard, clean -fd) keeps the link, and
// Lane.Free lets a drop through with the link still there. The main checkout's own file stays untracked and not
// ignored: git status shows it and git add of it succeeds.
func TestPreexistingLinkKeepsTheLaneClean(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		path  string
		links []any // lanes.links
	}{
		{name: "the settings link", path: project.PluginSettings},
		{name: "a lanes.links path", path: "local.env", links: []any{"local.env"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			y := newSx(t, cmd("check", probe), func(m map[string]any) {
				if c.links != nil {
					lanes, _ := m["lanes"].(map[string]any)
					if lanes == nil {
						lanes = map[string]any{}
						m["lanes"] = lanes
					}
					lanes["links"] = c.links
				}
			})
			// The upgraded project: the fixture's lane copy of the settings and its .claude/ exclude lines go, so the
			// main checkout's file is untracked and ignored nowhere, and the lane has none of its own.
			exclude := filepath.Join(y.Root, ".git", "info", "exclude")
			var kept []string
			for _, line := range strings.Split(y.Read(".git/info/exclude"), "\n") {
				if !strings.Contains(line, ".claude") {
					kept = append(kept, line)
				}
			}
			y.Write(exclude, strings.Join(kept, "\n"))
			if err := os.Remove(filepath.Join(y.lane, project.PluginSettings)); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			y.Write(c.path, "main's own\n")
			if _, err := project.Git(y.Root, "ls-files", "--error-unmatch", "--", c.path); err == nil {
				t.Fatalf("precondition: git tracks %s", c.path)
			}
			if out, err := project.Git(y.Root, "check-ignore", "-v", c.path); err == nil {
				t.Fatalf("precondition: %s is ignored: %s", c.path, out)
			}
			link := filepath.Join(y.lane, c.path)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(y.Root, c.path), link); err != nil {
				t.Fatal(err)
			}
			if st := y.Git(y.lane, "status", "--porcelain", "--untracked-files=all", "--", c.path); st != "?? "+c.path {
				t.Fatalf("precondition: raw git status of the link %q, want it untracked", st)
			}
			isLink := func(when string) {
				t.Helper()
				if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("%s: %s is no link (%v)", when, link, err)
				}
			}
			cfg := y.s.Cfg

			sha := y.submit()
			if out := y.Git(y.lane, "ls-tree", "-r", "--name-only", sha); strings.Contains(out, c.path) {
				t.Errorf("the submission committed the link: %q", out)
			}
			if out := y.Git(y.lane, "ls-tree", "-r", "--name-only", sha); !strings.Contains(out, "feature.txt") {
				t.Errorf("the submission lacks the lane's work feature.txt: %q", out)
			}
			s := stateOf(NewLaneTable(context.Background(), cfg), "L-01")
			if s.State != port.LaneFree || s.Dirty() || len(s.Paths) != 0 {
				t.Fatalf("lane table: %+v; want free with no dirty paths", s)
			}

			cand := y.green(lane(sha))
			o, err := y.test(ctx, cand)
			if err != nil || o.Kind != port.OutcomeGreen {
				t.Fatalf("lane test: %+v, %v; want green", o, err)
			}
			isLink("after the lane test's cleanup")

			l := Lane{Cfg: cfg, Out: io.Discard}
			line, err := l.Free(ctx, port.FreeInput{Lane: "L-01", Dir: y.lane, Branch: "task/T-05", Target: cand.SHA})
			if err != nil {
				t.Fatalf("free: %v; want the drop let through", err)
			}
			if line != "" {
				t.Errorf("free discarded %q; want nothing (the link is obor's)", line)
			}
			isLink("after the drop")

			if b, _ := os.ReadFile(exclude); strings.Contains(string(b), c.path) {
				t.Errorf("info/exclude names %s:\n%s", c.path, b)
			}
			if st := y.Git(y.Root, "status", "--porcelain", "--untracked-files=all", "--", c.path); st != "?? "+c.path {
				t.Errorf("main checkout's git status of %s: %q, want it untracked and shown", c.path, st)
			}
			if out, err := project.Git(y.Root, "add", "--", c.path); err != nil {
				t.Errorf("git add %s in the main checkout: %v %s", c.path, err, out)
			}
		})
	}
}

// A lane with three links, two gitignored (one named with a non-ASCII character, which git quotes in its answers) and
// one untracked and not ignored: the submission's git add stages the worker's work and no link. An exclude pathspec
// that names an ignored path fails git add, so an ignored link needs no exclude.
func TestAddAllLeavesIgnoredAndPlainLinksOut(t *testing.T) {
	t.Parallel()
	const (
		linked   = "local.env" // untracked and ignored nowhere
		nonASCII = "lïnk"      // gitignored, and quoted in git's answer about it
	)
	y := newSx(t, func(m map[string]any) {
		lanes, _ := m["lanes"].(map[string]any)
		lanes["links"] = []any{linked, nonASCII}
	})
	y.Write(filepath.Join(".git", "info", "exclude"), y.Read(".git/info/exclude")+"/"+nonASCII+"\n")
	links := []string{linked, nonASCII, project.PluginSettings} // project.LinksIn's order: lanes.links, then the settings
	for _, path := range links {
		y.Write(path, "main's own\n")
		if err := os.Remove(filepath.Join(y.lane, path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(y.Root, path), filepath.Join(y.lane, path)); err != nil {
			t.Fatal(err)
		}
	}
	if got := project.LinksIn(y.s.Cfg, y.lane, y.s.call); strings.Join(got, " ") != strings.Join(links, " ") {
		t.Fatalf("precondition: AddAll's links %q; want %q", got, links)
	}
	for _, c := range []struct {
		path    string
		ignored bool
	}{{project.PluginSettings, true}, {nonASCII, true}, {linked, false}} {
		if _, err := project.Git(y.lane, "check-ignore", "-q", c.path); (err == nil) != c.ignored {
			t.Fatalf("precondition: git ignoring %s = %v; want %v", c.path, err == nil, c.ignored)
		}
	}

	if err := y.s.gio().AddAll(y.lane); err != nil {
		t.Fatalf("AddAll: %v; want every link left out, the ignored ones without an exclude", err)
	}
	if staged := y.Git(y.lane, "diff", "--cached", "--name-only"); staged != "feature.txt" {
		t.Errorf("staged %q; want only the worker's feature.txt, no link", staged)
	}
}
