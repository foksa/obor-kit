package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/affected"
	"obor/internal/app/port"
)

// goAffected is serve's port.Affected (cmd/obor wires the same): affected.Go.
func goAffected(root string, files, fullGlobs []string) ([]string, string, error) {
	res, err := affected.Go(root, files, fullGlobs)
	return res.Pkgs, res.Full, err
}

// T-900: a moved target keeps a green result when the new commits overlap nothing of the candidate.

// goA is package a's file: two functions far apart, so a change to each merges cleanly.
const goA = "package a\n\nfunc A() int { return 1 }\n\n// one\n// two\n// three\n// four\n// five\n\nfunc A2() int { return 2 }\n"

// newGoSx is the scratch fixture on a Go module (m: packages a, b, d alone; c imports a) whose check counts its runs
// in $TMP_DIR/check-runs; lane L-01 on task/T-05 holds the module, nothing else changed.
func newGoSx(t *testing.T) *sx {
	t.Helper()
	y := newSx(t, cmd("check", `echo run >> "$TMP_DIR/check-runs"; echo 1 pass`))
	for name, text := range map[string]string{
		"go.mod":  "module m\n\ngo 1.22\n",
		"a/a.go":  goA,
		"b/b.go":  "package b\n\nfunc B() int { return 1 }\n",
		"c/c.go":  "package c\n\nimport \"m/a\"\n\nfunc C() int { return a.A() }\n",
		"d/d.go":  "package d\n\nfunc D() int { return 1 }\n",
		"e/e.go":  "package e\n\nfunc E() int { return 1 }\n",
		"f/f.go":  "package f\n\nfunc F() int { return 1 }\n",
		"g/g.go":  "package g\n\nfunc G() int { return 1 }\n",
		"h/h.txt": "not go\n",
	} {
		y.Write(name, text)
	}
	y.Git(y.Root, "add", "-A")
	y.Git(y.Root, "commit", "-qm", "go module")
	if err := os.Remove(filepath.Join(y.lane, "feature.txt")); err != nil {
		t.Fatal(err)
	}
	y.Git(y.lane, "merge", "-q", "--ff-only", "main")
	y.s.Affected = goAffected
	return y
}

// runs is how often the fixture's check ran.
func (y *sx) runs() int {
	b, err := os.ReadFile(filepath.Join(y.Base, "check-runs"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		y.t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

// edit replaces old with new in file (under dir) and commits it there.
func (y *sx) edit(dir, file, old, new string) {
	y.t.Helper()
	path := filepath.Join(dir, file)
	b, err := os.ReadFile(path)
	if err != nil {
		y.t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		y.t.Fatalf("precondition: %s holds no %q", file, old)
	}
	y.Write(path, strings.Replace(string(b), old, new, 1))
	y.Git(dir, "commit", "-qam", "edit "+file)
}

func moved(sha, green string) port.GateInput {
	return port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Summary: "feature", Green: green}
}

// One table, condition → gate or keep: the lane changes lane's file, main then changes main's, the moved gate runs
// with the green gate's base (or green's own).
func TestMovedGateKeepsWhenNothingOverlaps(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		lane, main [3]string                       // file, old, new
		green      func(y *sx, base string) string // the moved gate's Green (nil: the green gate's base)
		want       string                          // kept, gate, conflict
	}{
		{name: "disjoint packages", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"b/b.go", "return 1", "return 20"}, want: "kept"},
		{name: "disjoint, a non-Go file under no package", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"h/h.txt", "not go", "still not go"}, want: "kept"},
		{name: "shared package (c imports a)", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"c/c.go", "return a.A()", "return a.A() + 1"}, want: "gate"},
		{name: "same file, merged cleanly", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"a/a.go", "return 2", "return 20"}, want: "gate"},
		{name: "main's side full (go.mod)", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"go.mod", "go 1.22\n", "go 1.22\n\n// moved\n"}, want: "gate"},
		{name: "candidate's side full (go.mod)", lane: [3]string{"go.mod", "go 1.22\n", "go 1.22\n\n// lane\n"},
			main: [3]string{"b/b.go", "return 1", "return 20"}, want: "gate"},
		{name: "last gate red (no green base)", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main:  [3]string{"b/b.go", "return 1", "return 20"},
			green: func(*sx, string) string { return "" }, want: "gate"},
		{name: "green base not an ancestor of the target", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"b/b.go", "return 1", "return 20"},
			green: func(y *sx, base string) string {
				return y.Git(y.Root, "commit-tree", "-p", base, "-m", "elsewhere", base+"^{tree}")
			}, want: "gate"},
		{name: "rebase conflict", lane: [3]string{"a/a.go", "return 1", "return 10"},
			main: [3]string{"a/a.go", "return 1", "return 30"}, want: "conflict"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			y := newGoSx(t)
			y.edit(y.lane, c.lane[0], c.lane[1], c.lane[2])
			sha := y.submit()
			old := y.green(lane(sha))
			if y.runs() != 1 {
				t.Fatalf("precondition: the lane gate ran the check once, ran %d", y.runs())
			}
			y.edit(y.Root, c.main[0], c.main[1], c.main[2])
			target := y.head(y.Root)
			green := old.Target
			if c.green != nil {
				green = c.green(y, old.Target)
			}
			o := y.gate(moved(sha, green))
			got := map[bool]string{true: "kept", false: "gate"}[o.Kept != ""]
			if o.Kind == port.OutcomeConflict {
				got = "conflict"
			} else if o.Kind != port.OutcomeGreen {
				t.Fatalf("moved gate: %s %q; want green or a conflict", o.Kind, o.Line)
			}
			if got != c.want {
				t.Fatalf("moved gate %s (%q); want %s", got, o.Line, c.want)
			}
			switch c.want {
			case "kept":
				y.eq(fmt.Sprint(y.runs()), "1", "kept: no check ran on the moved target")
				y.eq(o.Kept, old.Target, "kept: the result names the base its checks ran on")
				y.eq(o.Candidate.Checks, "kept "+old.Target[:7], "kept: the candidate's checks")
				if o.Candidate.Full {
					t.Error("kept: the candidate is full; want partial")
				}
				y.eq(o.Candidate.Target, target, "kept: the candidate merges onto the new target")
				y.eq(y.Git(y.Root, "rev-parse", o.Candidate.SHA+"^1"), target, "kept: the merge's first parent is the target")
				y.eq(y.Git(y.Root, "rev-parse", o.Candidate.SHA+"^2"), sha, "kept: the merge's second parent is the submitted sha")
				y.eq(y.pin("T-05"), o.Candidate.SHA, "kept: the candidate is pinned")
				y.eq(o.Candidate.Patch, old.Patch, "kept: the patch is the green gate's")
			case "gate":
				y.eq(fmt.Sprint(y.runs()), "2", "gated: the check ran on the moved target")
				y.eq(o.Kept, "", "gated: nothing kept")
			case "conflict":
				y.eq(fmt.Sprint(y.runs()), "1", "conflict: no check ran")
				y.eq(strings.Join(o.Files, " "), "a/a.go", "conflict: the conflicting file (a resolve job)")
			}
		})
	}
}

// A crash between the keep decision and its record (the gate's result) runs the gate again: the same decision, the same
// candidate, still no check run.
func TestMovedGateKeepReplays(t *testing.T) {
	t.Parallel()
	y := newGoSx(t)
	y.edit(y.lane, "a/a.go", "return 1", "return 10")
	sha := y.submit()
	old := y.green(lane(sha))
	y.edit(y.Root, "b/b.go", "return 1", "return 20")
	first := y.gate(moved(sha, old.Target))
	if first.Kept == "" || y.runs() != 1 {
		t.Fatalf("precondition: kept with one check run; got kept %q, %d runs (%q)", first.Kept, y.runs(), first.Line)
	}
	again := y.gate(moved(sha, old.Target))
	y.eq(again.Kept, first.Kept, "replayed: kept the same base")
	y.eq(again.Candidate.SHA, first.Candidate.SHA, "replayed: the same candidate commit")
	y.eq(again.Line, first.Line, "replayed: the same line")
	y.eq(fmt.Sprint(y.runs()), "1", "replayed: no check ran")
}

// No Go module (the fixture's plain files): nothing can say which packages the commits affect, so the gate runs.
func TestMovedGateWithoutGoModuleGates(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", `echo run >> "$TMP_DIR/check-runs"; echo 1 pass`))
	y.s.Affected = goAffected
	sha := y.submit()
	old := y.green(lane(sha))
	y.Write("other.txt", "other\n")
	y.Git(y.Root, "add", "other.txt")
	y.Git(y.Root, "commit", "-qm", "unrelated")
	o := y.gate(moved(sha, old.Target))
	if o.Kind != port.OutcomeGreen || o.Kept != "" {
		t.Fatalf("moved gate without go.mod: %s kept %q (%q); want green, gated", o.Kind, o.Kept, o.Line)
	}
	y.eq(fmt.Sprint(y.runs()), "2", "the check ran on the moved target")
}

// Four candidates on disjoint packages, all green on one base, merge in a row as the merge loop does: publish, a
// moved target gates again, publish again. Before T-900 (no green base passed) every candidate after the first is gated
// twice: 7 gates for 4 merges. After: 4.
func TestMovedGateCountFourInARow(t *testing.T) {
	t.Parallel()
	count := func(t *testing.T, keep bool) (gates, runs int) {
		y := newGoSx(t)
		base := y.head(y.Root)
		type cand struct {
			task, sha, green string
			c                port.Candidate
		}
		var cs []*cand
		for i, pkg := range []string{"d", "e", "f", "g"} {
			task := fmt.Sprintf("T-%d", 10+i)
			y.Git(y.lane, "checkout", "-q", "-B", "work/"+task, base)
			y.edit(y.lane, pkg+"/"+pkg+".go", "return 1", "return 2")
			cs = append(cs, &cand{task: task, sha: y.head(y.lane)})
		}
		for _, c := range cs {
			gi := lane(c.sha)
			gi.Task = c.task
			c.c = y.green(gi)
			c.green = c.c.Target
			gates++
		}
		for _, c := range cs {
			for range 3 {
				o, err := y.s.Publish(ctx, c.c)
				if err != nil {
					t.Fatalf("publish %s: %v", c.task, err)
				}
				if o.Kind == port.PublishMerged {
					break
				}
				if o.Kind != port.PublishMoved {
					t.Fatalf("publish %s: %+v", c.task, o)
				}
				gi := moved(c.sha, "")
				gi.Task = c.task
				if keep {
					gi.Green = c.green
				}
				g := y.gate(gi)
				if g.Kind != port.OutcomeGreen {
					t.Fatalf("moved gate %s: %s %q", c.task, g.Kind, g.Line)
				}
				c.c, c.green = g.Candidate, g.Candidate.Target
				if g.Kept != "" {
					c.green = g.Kept
				}
				gates++
			}
		}
		y.eq(y.Git(y.Root, "rev-list", "--count", "--merges", base+"..main"), "4", "four merges on main")
		return gates, y.runs()
	}
	var before, after [2]int
	t.Run("before", func(t *testing.T) { before[0], before[1] = count(t, false) })
	t.Run("after", func(t *testing.T) { after[0], after[1] = count(t, true) })
	t.Logf("4 merges in a row: before %d gate calls, %d check runs (%.2f per merge); after %d gate calls, %d check runs (%.2f per merge)",
		before[0], before[1], float64(before[1])/4, after[0], after[1], float64(after[1])/4)
	if before[1] != 7 || after[1] != 4 {
		t.Errorf("check runs: before %d, after %d; want 7 and 4", before[1], after[1])
	}
}
