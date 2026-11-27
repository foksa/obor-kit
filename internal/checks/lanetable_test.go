package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// A lane whose serve add has not finished (its add marker there) is missing whatever its worktree holds, with no git
// read; with the marker gone the same clean worktree is free. Git is the table's fake: rev-parse names the lane dir,
// status nothing.
func TestLanePendingAddIsMissing(t *testing.T) {
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("lanes.count", 1))
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	dir := c.Lanes()[0].Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reads := 0
	state := func() (string, string) {
		lt := NewLaneTable(context.Background(), c)
		lt.Git = func(_ context.Context, _ string, args ...string) (string, error) {
			reads++
			switch args[0] {
			case "rev-parse":
				return dir + "\nabc123\nHEAD\n", nil
			case "status":
				return "", nil
			}
			return "", errors.New("unexpected git " + strings.Join(args, " "))
		}
		l := lt.LaneState(c.Lanes()[0])
		return l.State, l.Reason
	}
	if st, _ := state(); st != "free" || reads == 0 {
		t.Fatalf("precondition: the clean worktree with no marker is %s after %d git reads, want free", st, reads)
	}
	marker := project.LaneAddMarker(p.Root, "L-01")
	if filepath.Dir(marker) != filepath.Join(project.StateDir(p.Root), "lanes") {
		t.Errorf("marker %s is not beside the lane's .setup marker", marker)
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reads = 0
	if st, why := state(); st != "missing" || why != "serve's add of the lane has not finished (obor setup lane L-01)" {
		t.Errorf("with the add marker: %s (%q), want missing with its reason", st, why)
	}
	if reads != 0 {
		t.Errorf("with the add marker: %d git reads, want none", reads)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if st, _ := state(); st != "free" {
		t.Errorf("the marker removed: %s, want free", st)
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
