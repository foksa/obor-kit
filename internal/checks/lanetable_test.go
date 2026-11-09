package checks

import (
	"context"
	"testing"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/testkit"
)

// The lane table's tests that need no git (T-1018); the ones over real git are lanetable_integration_test.go's.

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

// T-224: spec.Match with the patterns GlobMatch's tests used, with what it returned.
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
