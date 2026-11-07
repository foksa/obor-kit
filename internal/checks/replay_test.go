package checks

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// T-915 (RV-123 #1): git rerere replays conflict hunks, not the whole resolution commit. A moved gate whose merge
// needed a replay is clean only when its patch equals the attempt's checked resolution's (GateInput.Resolved); else,
// or with none, a conflict naming the files whose patch differs, nothing compiled.

// resolvedGreen brings newGoSx to a checked resolution: the lane and main both change a/a.go's A, the lane gate
// conflicts, the integrator resolves it (return 40) — extra, when set, also changes b/b.go outside the conflict — and
// the resolved gate is green. The submitted sha, the resolution's sha, its green candidate.
func resolvedGreen(y *sx, extra bool) (string, string, port.Candidate) {
	y.t.Helper()
	y.edit(y.lane, "a/a.go", "return 1", "return 10")
	sha := y.submit()
	y.edit(y.Root, "a/a.go", "return 1", "return 30")
	if o := y.gate(lane(sha)); o.Kind != port.OutcomeConflict || strings.Join(o.Files, " ") != "a/a.go" {
		y.t.Fatalf("precondition: the lane gate conflicts in a/a.go, got %s %q", o.Kind, o.Line)
	}
	y.Write(filepath.Join(y.serve, "a/a.go"), strings.Replace(goA, "return 1", "return 40", 1))
	if extra {
		y.Write(filepath.Join(y.serve, "b/b.go"), "package b\n\nfunc B() int { return 40 }\n")
	}
	y.Git(y.serve, "add", "-A")
	y.Git(y.serve, "commit", "-qm", "resolved")
	res, err := y.s.Resolved(ctx, "T-05", sha, nil)
	if err != nil {
		y.t.Fatal(err)
	}
	c := y.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res})
	if y.runs() != 1 {
		y.t.Fatalf("precondition: the resolved gate compiled once, %d runs", y.runs())
	}
	return sha, res, c
}

// The review's fixture: the resolution also changed b/b.go, main then moves in disjoint d/d.go; the replay drops the
// b/b.go edit. Before T-915 the moved gate went green with that edit lost.
func TestRV123RerereDropsOtherResolvedEdits(t *testing.T) {
	t.Parallel()
	y := newGoSx(t)
	sha, res, old := resolvedGreen(y, true)
	y.eq(y.Git(y.Root, "show", old.SHA+":b/b.go"), "package b\n\nfunc B() int { return 40 }", "precondition: the checked resolution holds b's edit")
	y.edit(y.Root, "d/d.go", "return 1", "return 20")
	in := moved(sha)
	in.Resolved = res
	o := y.gate(in)
	if o.Kind != port.OutcomeConflict {
		t.Fatalf("moved gate: %s %q; want a conflict", o.Kind, o.Line)
	}
	y.eq(strings.Join(o.Files, " "), "b/b.go", "the conflict names the file whose patch differs")
	y.eq(fmt.Sprint(y.runs()), "1", "nothing compiled")
	if !strings.Contains(o.Line, "differs from the checked one") {
		t.Errorf("the line says why: %q", o.Line)
	}
	y.eq(o.Dir, y.serve, "the merge stays open in serve's worktree")
}

// Whatever the move touches (a disjoint package, an importer of the resolved one, go.mod), a replay that equals the
// checked resolution is clean: compiled and green, its message saying the resolution was reused; one that differs (the
// resolution's extra b/b.go edit lost) is a conflict, nothing compiled.
func TestReplayedResolutionCleanOnlyWhenEqual(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		main [3]string // the move: file, old, new
	}{
		{"a disjoint package", [3]string{"d/d.go", "return 1", "return 20"}},
		{"an importer", [3]string{"c/c.go", "return a.A()", "return a.A() + 1"}},
		{"go.mod", [3]string{"go.mod", "go 1.22\n", "go 1.22\n\n// moved\n"}},
	} {
		for _, extra := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, differs=%v", c.name, extra), func(t *testing.T) {
				t.Parallel()
				y := newGoSx(t)
				sha, res, _ := resolvedGreen(y, extra)
				y.edit(y.Root, c.main[0], c.main[1], c.main[2])
				in := moved(sha)
				in.Resolved = res
				o := y.gate(in)
				if extra {
					if o.Kind != port.OutcomeConflict {
						t.Fatalf("a replay that lost b/b.go: %s %q; want a conflict", o.Kind, o.Line)
					}
					y.eq(strings.Join(o.Files, " "), "b/b.go", "the conflict names the differing file")
					y.eq(fmt.Sprint(y.runs()), "1", "differs: nothing compiled")
					return
				}
				if o.Kind != port.OutcomeGreen {
					t.Fatalf("an equal replay: %s %q; want green", o.Kind, o.Line)
				}
				y.eq(y.Git(y.Root, "show", o.Candidate.SHA+":a/a.go"), strings.TrimSpace(strings.Replace(goA, "return 1", "return 40", 1)),
					"the candidate holds the replayed resolution")
				y.contains(y.Git(y.Root, "log", "-1", "--format=%B", o.Candidate.SHA), "(conflict resolution reused)", "the merge's message")
				y.eq(fmt.Sprint(y.runs()), "2", "the equal replay compiled")
			})
		}
	}
}

// A replay with no checked resolution of this attempt (the cache entry is another task's, or another attempt's) is a
// conflict, even when it would equal a resolution.
func TestReplayWithoutCheckedResolutionConflicts(t *testing.T) {
	t.Parallel()
	y := newGoSx(t)
	sha, _, _ := resolvedGreen(y, false)
	y.edit(y.Root, "d/d.go", "return 1", "return 20")
	o := y.gate(moved(sha)) // no Resolved
	if o.Kind != port.OutcomeConflict {
		t.Fatalf("moved gate with no checked resolution: %s %q; want a conflict", o.Kind, o.Line)
	}
	y.eq(strings.Join(o.Files, " "), "a/a.go", "the conflict names the replayed file")
	y.eq(fmt.Sprint(y.runs()), "1", "nothing compiled")
	// A lane gate's replay has none either (the integrator resolves, rerere pre-fills the hunk).
	o = y.gate(lane(sha))
	if o.Kind != port.OutcomeConflict {
		t.Fatalf("lane gate with a replay: %s %q; want a conflict", o.Kind, o.Line)
	}
}
