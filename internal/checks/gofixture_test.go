package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// The scratch fixture on a Go module, for the rerere replay tests (T-913, T-915, T-917).

// goA is package a's file: two functions far apart, so a change to each merges cleanly.
const goA = "package a\n\nfunc A() int { return 1 }\n\n// one\n// two\n// three\n// four\n// five\n\nfunc A2() int { return 2 }\n"

// newGoSx is the scratch fixture on a Go module (m: packages a, b, d alone; c imports a) whose commands.compile (the
// gate's one command, T-961) counts its runs in $TMP_DIR/compile-runs; lane L-01 on task/T-05 holds the module,
// nothing else changed.
func newGoSx(t *testing.T, edits ...func(map[string]any)) *sx {
	t.Helper()
	y := newSx(t, append([]func(map[string]any){
		cmd("compile", `echo run >> "$TMP_DIR/compile-runs"; echo 1 pass`),
	}, edits...)...)
	for name, text := range map[string]string{
		"go.mod":  "module m\n\ngo 1.22\n",
		"a/a.go":  goA,
		"b/b.go":  "package b\n\nfunc B() int { return 1 }\n",
		"c/c.go":  "package c\n\nimport \"m/a\"\n\nfunc C() int { return a.A() }\n",
		"d/d.go":  "package d\n\nfunc D() int { return 1 }\n",
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
	return y
}

// runs is how often the fixture's compile ran.
func (y *sx) runs() int {
	b, err := os.ReadFile(filepath.Join(y.Base, "compile-runs"))
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

// moved is T-05's gate of sha onto the moved target.
func moved(sha string) port.GateInput {
	return port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Summary: "feature"}
}
