package app

import (
	"strings"
	"testing"

	"obor/internal/app/port"
)

// T-917 (RV-123 V1): the replay mismatch reaches the resolve job with the reason, the checked resolution and the file.
func TestResolveJobNamesReplayMismatch(t *testing.T) {
	m := &port.Mismatch{Checked: "0123456789abcdef", Tree: "t", Files: []string{"b/b.go"}, Dir: "/scratch"}
	o := port.GateOutcome{Kind: port.OutcomeConflict, Files: m.Files, Dir: "/scratch", Mismatch: m,
		Line: "CONFLICT in /scratch: b/b.go (the replayed resolution differs from the checked one)"}
	g := gateResult("T-05", o)
	if g.Outcome != outConflict || g.Mismatch != m {
		t.Fatalf("gateResult: %+v; want a conflict carrying the mismatch", g)
	}
	mt := mtask{task: "T-05", lane: "L-01", attempt: 1, state: stConflict}
	mt.line, mt.mismatch = g.Line, g.Mismatch
	text := resolveText(mt)
	for _, want := range []string{"lost edits of the checked one 0123456", "b/b.go", "git diff 0123456 -- b/b.go",
		"the target's new changes stay"} {
		if !strings.Contains(text, want) {
			t.Errorf("resolve job lacks %q:\n%s", want, text)
		}
	}
}

// Real unmerged paths keep today's conflict line and job.
func TestResolveJobForUnmergedPathsHasNoMismatchWording(t *testing.T) {
	o := port.GateOutcome{Kind: port.OutcomeConflict, Files: []string{"a/a.go"}, Dir: "/scratch"}
	g := gateResult("T-05", o)
	if g.Mismatch != nil || g.Line != "CONFLICT in /scratch: a/a.go" {
		t.Fatalf("gateResult: %+v", g)
	}
	mt := mtask{task: "T-05", lane: "L-01", attempt: 1, state: stConflict, line: g.Line}
	text := resolveText(mt)
	if !strings.Contains(text, "CONFLICT in /scratch: a/a.go") || strings.Contains(text, "lost edits") {
		t.Errorf("resolve job: %s", text)
	}
}
