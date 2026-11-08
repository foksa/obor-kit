package checks

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// T-917 (RV-123 V1): a replay mismatch has no unmerged path, so the gate outcome carries the checked resolution and the
// files, and Scratch.Resolved refuses the staged merge committed as it stands.

// The review's fixture and each kind of move (a disjoint package, an importer, go.mod) reach the same mismatch.
func TestReplayMismatchOutcomeNamesCheckedResolution(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, file, old, new string }{
		{"a disjoint package", "d/d.go", "return 1", "return 20"},
		{"an importer", "c/c.go", "return a.A()", "return a.A() + 1"},
		{"go.mod", "go.mod", "go 1.22\n", "go 1.22\n\n// moved\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			y, sha, res, _ := resolvedTmpl(true).resolved(t, true)
			y.edit(y.Root, c.file, c.old, c.new)
			in := moved(sha)
			in.Resolved = res
			o := y.gate(in)
			if o.Kind != port.OutcomeConflict || o.Mismatch == nil {
				t.Fatalf("moved gate: %s %q mismatch %v; want a conflict with a mismatch", o.Kind, o.Line, o.Mismatch)
			}
			y.eq(o.Mismatch.Checked, res, "the mismatch names the checked resolution")
			y.eq(strings.Join(o.Mismatch.Files, " "), "b/b.go", "and the lost file")
			y.eq(o.Mismatch.Dir, y.serve, "and where the merge is open")
			y.contains(o.Line, "the replayed resolution differs from the checked one", "the reason")
		})
	}
}

// Real unmerged paths stay today's conflict: no mismatch.
func TestUnmergedConflictHasNoMismatch(t *testing.T) {
	t.Parallel()
	y := newGoSx(t)
	y.edit(y.lane, "a/a.go", "return 1", "return 10")
	sha := y.submit()
	y.edit(y.Root, "a/a.go", "return 1", "return 30")
	in := lane(sha)
	in.Resolved = "ignored" // only a replay is compared with it
	o := y.gate(in)
	if o.Kind != port.OutcomeConflict || strings.Join(o.Files, " ") != "a/a.go" {
		t.Fatalf("precondition: a real conflict in a/a.go, got %s %q", o.Kind, o.Line)
	}
	if o.Mismatch != nil {
		t.Errorf("unmerged paths are no mismatch: %+v", o.Mismatch)
	}
	y.contains(o.Line, "CONFLICT in "+y.serve+": a/a.go", "today's line")
}

// Committing the staged merge unchanged is refused; restoring the lost edit is accepted and gated.
func TestResolvedRefusesMismatchUntilEditRestored(t *testing.T) {
	t.Parallel()
	y, sha, res, _ := resolvedTmpl(true).resolved(t, true)
	y.edit(y.Root, "d/d.go", "return 1", "return 20")
	in := moved(sha)
	in.Resolved = res
	o := y.gate(in)
	if o.Mismatch == nil {
		t.Fatalf("precondition: a mismatch, got %s %q", o.Kind, o.Line)
	}
	y.Git(y.serve, "commit", "-qm", "as it stands")
	_, err := y.s.Resolved(ctx, "T-05", sha, o.Mismatch)
	var ref port.Refused
	if !errors.As(err, &ref) {
		t.Fatalf("the unchanged staged merge: err %v; want a refusal", err)
	}
	y.contains(ref.Line, "b/b.go", "the refusal names the file")
	y.contains(ref.Line, res[:7], "and the checked resolution")
	if _, err := y.s.Resolved(ctx, "T-05", sha, nil); err != nil {
		t.Fatalf("precondition: without the mismatch the same commit is accepted (the old behaviour): %v", err)
	}

	y.Write(filepath.Join(y.serve, "b/b.go"), "package b\n\nfunc B() int { return 40 }\n")
	y.Git(y.serve, "commit", "-qa", "--amend", "-m", "restored")
	fixed, err := y.s.Resolved(ctx, "T-05", sha, o.Mismatch)
	if err != nil {
		t.Fatalf("the restored edit: %v", err)
	}
	before := y.runs()
	c := y.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: fixed})
	y.eq(y.Git(y.Root, "show", c.SHA+":b/b.go"), "package b\n\nfunc B() int { return 40 }", "the candidate holds the edit")
	y.eq(fmt.Sprint(y.runs()), fmt.Sprint(before+1), "the resolved gate compiled it")
}

// An integrator who changed the file otherwise (not the replay's content) is not refused.
func TestResolvedAcceptsOtherwiseChangedMismatchFile(t *testing.T) {
	t.Parallel()
	y, sha, res, _ := resolvedTmpl(true).resolved(t, true)
	y.edit(y.Root, "d/d.go", "return 1", "return 20")
	in := moved(sha)
	in.Resolved = res
	o := y.gate(in)
	if o.Mismatch == nil {
		t.Fatalf("precondition: a mismatch, got %s %q", o.Kind, o.Line)
	}
	y.Write(filepath.Join(y.serve, "b/b.go"), "package b\n\nfunc B() int { return 41 }\n")
	y.Git(y.serve, "commit", "-qam", "own resolution")
	if _, err := y.s.Resolved(ctx, "T-05", sha, o.Mismatch); err != nil {
		t.Fatalf("a changed file is the integrator's decision: %v", err)
	}
}
