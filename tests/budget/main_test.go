package main

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// env is one check call's inputs: the budget file, the packages with tests, their two tiers and the private TMPDIR's
// leftovers.
type env struct {
	budget      File
	pkgs        []string
	full, short map[string]Run
	tmp         []string
}

// base is a green run: two packages, every metric under its budget, one 1.5 s test and its Slow mark.
func base() env {
	return env{
		budget: File{
			UntaggedS: 1.3,
			Packages: map[string]Budget{
				"a": {ShortS: 2, FullS: 4, GitProcs: 10},
				"b": {ShortS: 5, FullS: 9, GitProcs: 100},
			},
		},
		pkgs: []string{"a", "b"},
		full: map[string]Run{
			"a": {Passed: true, Elapsed: 3.9, Git: 10, Tests: []Test{{Name: "TestSlow", Elapsed: 1.5}}},
			"b": {Passed: true, Elapsed: 8, Git: 50},
		},
		short: map[string]Run{
			"a": {Passed: true, Elapsed: 1.9, Slow: map[string]bool{"TestSlow": true}},
			"b": {Passed: true, Elapsed: 4},
		},
	}
}

// T-1002: the green run has no line (the precondition the breach cases rest on) and counts every package.
func TestCheckGreen(t *testing.T) {
	e := base()
	lines, n := check(e.budget, e.pkgs, e.full, e.short, e.tmp)
	if len(lines) != 0 {
		t.Fatalf("a run under every budget printed %q; want none", lines)
	}
	if n != 2 {
		t.Errorf("check compared %d packages; want 2", n)
	}
}

// T-1002, T-1004: one line per breach, in the shape `FAIL budget: <pkg> <metric> <got> > <max>` for a metric, and a
// line of its own for a missing budget, a failed tier, a test over its cap, an uncapped untagged test and the TMPDIR's
// leftovers.
func TestCheckBreaches(t *testing.T) {
	if lines, _ := check(base().budget, base().pkgs, base().full, base().short, nil); len(lines) != 0 {
		t.Fatalf("precondition: the base run is green, got %q", lines)
	}
	for _, c := range []struct {
		name string
		edit func(*env)
		want []string
	}{
		{"full over budget", func(e *env) {
			r := e.full["a"]
			r.Elapsed = 4.5
			e.full["a"] = r
		}, []string{"FAIL budget: a full_s 4.5s > 4.0s"}},
		{"short over budget", func(e *env) {
			r := e.short["a"]
			r.Elapsed = 2.5
			e.short["a"] = r
		}, []string{"FAIL budget: a short_s 2.5s > 2.0s"}},
		{"git over budget", func(e *env) {
			r := e.full["a"]
			r.Git = 11
			e.full["a"] = r
		}, []string{"FAIL budget: a git_procs 11 > 10"}},
		{"a capped test under its cap", func(e *env) {
			r := e.full["a"]
			r.Tests = []Test{{Name: "TestCapped", Elapsed: 1.5}}
			e.full["a"] = r
			e.budget.Tests = map[string]float64{"a TestCapped": 2}
		}, nil},
		{"a capped test over its cap", func(e *env) {
			r := e.full["a"]
			r.Tests = []Test{{Name: "TestCapped", Elapsed: 2.5}}
			e.full["a"] = r
			e.budget.Tests = map[string]float64{"a TestCapped": 2}
		}, []string{"FAIL budget: a test/TestCapped 2.50s > 2.0s"}},
		{"an uncapped test at 1.2 s is under the untagged threshold", func(e *env) {
			r := e.full["a"]
			r.Tests = []Test{{Name: "TestUntagged", Elapsed: 1.2}}
			e.full["a"] = r
		}, nil},
		{"an uncapped test at the untagged threshold", func(e *env) {
			r := e.full["a"]
			r.Tests = []Test{{Name: "TestUntagged", Elapsed: 1.3}}
			e.full["a"] = r
		}, []string{"FAIL budget: a test/TestUntagged 1.30s >= 1.3s without testkit.Slow"}},
		{"no budget for a package with tests", func(e *env) {
			delete(e.budget.Packages, "b")
		}, []string{"FAIL budget: b missing from tests/budget.json"}},
		{"a failed full tier", func(e *env) {
			r := e.full["a"]
			r.Passed, r.Failed = false, []string{"TestX", "TestY"}
			e.full["a"] = r
		}, []string{"FAIL budget: a full run failed: TestX, TestY"}},
		{"a failed short tier", func(e *env) {
			r := e.short["a"]
			r.Passed, r.note = false, "build constraints exclude all Go files"
			e.short["a"] = r
		}, []string{"FAIL budget: a short run failed: build constraints exclude all Go files"}},
		{"temp entries left", func(e *env) {
			e.tmp = []string{"obor-leftover-1", "obor-leftover-2"}
		}, []string{"FAIL budget: tmp 2 entries left in the private TMPDIR: obor-leftover-1, obor-leftover-2"}},
		{"several at once", func(e *env) {
			r := e.full["b"]
			r.Elapsed, r.Git = 9.5, 101
			e.full["b"] = r
		}, []string{"FAIL budget: b full_s 9.5s > 9.0s", "FAIL budget: b git_procs 101 > 100"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := base()
			c.edit(&e)
			lines, n := check(e.budget, e.pkgs, e.full, e.short, e.tmp)
			if !slices.Equal(lines, c.want) {
				t.Errorf("check printed %q; want %q", lines, c.want)
			}
			if n != len(e.pkgs) {
				t.Errorf("check compared %d packages; want %d", n, len(e.pkgs))
			}
		})
	}
}

// T-1002, T-1004: a Slow-marked test is the short tier's skipped test whose output carries testkit.Slow's message; a
// top-level test's cost is its own Elapsed plus the Elapsed of every subtest that paused (rev 4: Go's Elapsed excludes
// a parallel test's wait between pause and cont, and a non-paused subtest is inside its parent's Elapsed); the
// package's own verdict is the tier's wall time.
func TestParseRun(t *testing.T) {
	for _, c := range []struct {
		name, stream string
		want         []Test
	}{
		{"a plain test keeps its time", `{"Action":"run","Test":"TestPlain"}
{"Action":"pass","Test":"TestPlain","Elapsed":1.2}
{"Action":"pass","Package":"p","Elapsed":1.3}
`, []Test{{Name: "TestPlain", Elapsed: 1.2}}},
		{"a parent's own time plus its two paused subtests", `{"Action":"run","Test":"TestP"}
{"Action":"run","Test":"TestP/a"}
{"Action":"pause","Test":"TestP/a"}
{"Action":"run","Test":"TestP/b"}
{"Action":"pause","Test":"TestP/b"}
{"Action":"cont","Test":"TestP/a"}
{"Action":"pass","Test":"TestP/a","Elapsed":0.7}
{"Action":"cont","Test":"TestP/b"}
{"Action":"pass","Test":"TestP/b","Elapsed":0.7}
{"Action":"pass","Test":"TestP","Elapsed":0}
{"Action":"pass","Package":"p","Elapsed":1.5}
`, []Test{{Name: "TestP", Elapsed: 1.4}}},
		{"a parallel top-level test that waited 30 s in the queue", `{"Action":"run","Test":"TestQueued"}
{"Action":"pause","Test":"TestQueued"}
{"Action":"cont","Test":"TestQueued"}
{"Action":"pass","Test":"TestQueued","Elapsed":0.2}
{"Action":"pass","Package":"p","Elapsed":30.3}
`, []Test{{Name: "TestQueued", Elapsed: 0.2}}},
		{"a sequential subtest is already in its parent", `{"Action":"run","Test":"TestSeq"}
{"Action":"run","Test":"TestSeq/s"}
{"Action":"pass","Test":"TestSeq/s","Elapsed":0.4}
{"Action":"pass","Test":"TestSeq","Elapsed":0.5}
{"Action":"pass","Package":"p","Elapsed":0.6}
`, []Test{{Name: "TestSeq", Elapsed: 0.5}}},
		{"a paused subtest and its paused grandchild are added", `{"Action":"run","Test":"TestDeep"}
{"Action":"run","Test":"TestDeep/s"}
{"Action":"pause","Test":"TestDeep/s"}
{"Action":"run","Test":"TestDeep/s/g"}
{"Action":"pause","Test":"TestDeep/s/g"}
{"Action":"pass","Test":"TestDeep/s/g","Elapsed":0.6}
{"Action":"pass","Test":"TestDeep/s","Elapsed":0.3}
{"Action":"pass","Test":"TestDeep","Elapsed":0.1}
{"Action":"pass","Package":"p","Elapsed":1.1}
`, []Test{{Name: "TestDeep", Elapsed: 1.0}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := parseRun([]byte(c.stream))
			if !sameTests(r.Tests, c.want) {
				t.Errorf("parseRun's top-level tests %v; want %v", r.Tests, c.want)
			}
		})
	}

	const passing = `{"Action":"start","Package":"p"}
{"Action":"run","Test":"TestA"}
{"Action":"output","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"run","Test":"TestA/sub"}
{"Action":"pass","Test":"TestA/sub","Elapsed":0.5}
{"Action":"pass","Test":"TestA","Elapsed":1.3}
{"Action":"run","Test":"TestSlow"}
{"Action":"output","Test":"TestSlow","Output":"    p_test.go:10: slow: runs in make check\n"}
{"Action":"skip","Test":"TestSlow","Elapsed":0}
{"Action":"run","Test":"TestOtherSkip"}
{"Action":"output","Test":"TestOtherSkip","Output":"    p_test.go:12: needs the network\n"}
{"Action":"skip","Test":"TestOtherSkip","Elapsed":0}
{"Action":"pass","Package":"p","Elapsed":4.2}
`
	r := parseRun([]byte(passing))
	if !r.Passed || r.Elapsed != 4.2 {
		t.Errorf("parseRun: passed %v elapsed %v; want true 4.2", r.Passed, r.Elapsed)
	}
	if want := []Test{{Name: "TestA", Elapsed: 1.3}}; !sameTests(r.Tests, want) {
		t.Errorf("parseRun's top-level tests %v; want %v (a sequential subtest adds nothing)", r.Tests, want)
	}
	if want := map[string]bool{"TestSlow": true}; len(r.Slow) != len(want) || !r.Slow["TestSlow"] {
		t.Errorf("parseRun's Slow marks %v; want only TestSlow (its skip message)", r.Slow)
	}
	if len(r.Failed) != 0 {
		t.Errorf("parseRun reported failed tests %q on a passing run", r.Failed)
	}

	const buildFail = `{"Action":"build-output","Output":"# p\n./p_test.go:1:1: undefined: y\n"}
{"Action":"build-fail"}
`
	r = parseRun([]byte(buildFail))
	if r.Passed {
		t.Error("parseRun: a build-fail is not a pass")
	}
	if r.note != "./p_test.go:1:1: undefined: y" {
		t.Errorf("parseRun's note %q; want the compiler's error line", r.note)
	}

	const testFail = `{"Action":"run","Test":"TestB"}
{"Action":"fail","Test":"TestB","Elapsed":0.1}
{"Action":"fail","Package":"p","Elapsed":0.5}
`
	r = parseRun([]byte(testFail))
	if r.Passed {
		t.Error("parseRun: a package fail is not a pass")
	}
	if want := []string{"TestB"}; !slices.Equal(r.Failed, want) {
		t.Errorf("parseRun's failed tests %v; want %v", r.Failed, want)
	}
}

// T-1002: the wrapper counts every call and still runs the real git with the caller's arguments (the budget's whole
// measurement rests on it).
func TestGitWrapper(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "real.log")
	realGit := filepath.Join(dir, "real-git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quote(log) + "\n"
	if err := os.WriteFile(realGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(dir, "count")
	if err := writeGitWrapper(filepath.Join(dir, "bin"), realGit, count); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "bin", "git")
	for _, args := range [][]string{{"status"}, {"-C", "a b", "log"}, {}} {
		if out, err := exec.Command(wrapper, args...).CombinedOutput(); err != nil {
			t.Fatalf("wrapper %q: %v\n%s", args, err, out)
		}
	}
	b, err := os.ReadFile(count)
	if err != nil {
		t.Fatalf("the wrapper wrote no counter: %v", err)
	}
	if len(b) != 3 {
		t.Errorf("the wrapper counted %d calls; want 3", len(b))
	}
	calls := strings.Split(strings.TrimSuffix(string(readFile(t, log)), "\n"), "\n")
	if want := []string{"status", "-C a b log", ""}; !slices.Equal(calls, want) {
		t.Errorf("the real git got %q; want %q (the arguments, unchanged)", calls, want)
	}
}

// T-1002, T-1004: the seed is each measured tier x1.5 and each git count x1.5, rounded up (a tenth of a second; one
// process), with the untagged threshold and no per-test caps (they are hand-set).
func TestSeed(t *testing.T) {
	full := map[string]Run{
		"a": {Elapsed: 2.0, Git: 3},
		"b": {Elapsed: 1.01, Git: 0},
	}
	short := map[string]Run{
		"a": {Elapsed: 1.0},
		"b": {Elapsed: 0.21},
	}
	f := seed(full, short, File{})
	want := map[string]Budget{
		"a": {ShortS: 1.5, FullS: 3.0, GitProcs: 5},
		"b": {ShortS: 0.4, FullS: 1.6, GitProcs: 0},
	}
	for pkg, w := range want {
		if got := f.Packages[pkg]; got != w {
			t.Errorf("seed(%s) = %+v; want %+v", pkg, got, w)
		}
	}
	if f.UntaggedS != untaggedDefault {
		t.Errorf("seed's untagged_s = %v; want %v", f.UntaggedS, untaggedDefault)
	}
	if len(f.Tests) != 0 {
		t.Errorf("seed set per-test caps %v; want none (they are hand-set)", f.Tests)
	}
}

// T-1007: -seed carries the hand-set per-test caps and untagged_s of the tests/budget.json it finds over unchanged,
// re-measuring only the packages; no file seeds the defaults.
func TestSeededKeepsCaps(t *testing.T) {
	full := map[string]Run{"a": {Elapsed: 2.0, Git: 2}}
	short := map[string]Run{"a": {Elapsed: 1.0}}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := `{"untagged_s":2.5,"tests":{"a TestSlow":4},"packages":{"a":{"short_s":1,"full_s":1,"git_procs":0}}}`
	if err := os.WriteFile(filepath.Join(root, "tests", "budget.json"), []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := seeded(root, full, short)
	if err != nil {
		t.Fatal(err)
	}
	if f.UntaggedS != 2.5 {
		t.Errorf("seeded untagged_s = %v; want the file's 2.5", f.UntaggedS)
	}
	if len(f.Tests) != 1 || f.Tests["a TestSlow"] != 4 {
		t.Errorf("seeded tests = %v; want the file's a TestSlow 4", f.Tests)
	}
	if got, want := f.Packages["a"], (Budget{ShortS: 1.5, FullS: 3.0, GitProcs: 3}); got != want {
		t.Errorf("seeded package a = %+v; want the measurement %+v", got, want)
	}

	g, err := seeded(t.TempDir(), full, short)
	if err != nil {
		t.Fatal(err)
	}
	if g.UntaggedS != untaggedDefault || len(g.Tests) != 0 {
		t.Errorf("seed with no file: untagged_s %v, tests %v; want the defaults",
			g.UntaggedS, g.Tests)
	}
}

// T-1008 (RV-139 #2): -seed writes tests/budget.json itself, over the caps of the file it finds and complete for every
// measured package; a tier that failed writes nothing, so the old file stays byte for byte.
func TestSeedWritesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tests", "budget.json")
	prev := `{"untagged_s":2.5,"tests":{"a TestSlow":4},"packages":{"a":{"short_s":1,"full_s":1,"git_procs":0}}}`
	if err := os.WriteFile(path, []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}
	full := map[string]Run{"a": {Passed: true, Elapsed: 2.0, Git: 2}, "b": {Passed: true, Elapsed: 1.0}}
	short := map[string]Run{"a": {Passed: true, Elapsed: 1.0}, "b": {Passed: true, Elapsed: 0.5}}
	if err := seedFile(root, []string{"a", "b"}, full, short); err != nil {
		t.Fatalf("seedFile: %v", err)
	}
	got, err := load(root)
	if err != nil {
		t.Fatalf("the seeded file does not load: %v", err)
	}
	if got.UntaggedS != 2.5 {
		t.Errorf("seeded untagged_s = %v; want the old file's 2.5", got.UntaggedS)
	}
	if len(got.Tests) != 1 || got.Tests["a TestSlow"] != 4 {
		t.Errorf("seeded tests = %v; want the old file's a TestSlow 4", got.Tests)
	}
	if len(got.Packages) != 2 {
		t.Errorf("seeded packages = %v; want both measured packages", got.Packages)
	}
	if b, want := got.Packages["b"], (Budget{ShortS: 0.8, FullS: 1.5, GitProcs: 0}); b != want {
		t.Errorf("seeded package b = %+v; want the measurement %+v", b, want)
	}
	ents, err := os.ReadDir(filepath.Join(root, "tests"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "budget.json" {
		t.Errorf("the seed left %d entries in tests/; want only budget.json (no temp file)", len(ents))
	}

	before := readFile(t, path)
	bad := map[string]Run{"a": {Passed: true, Elapsed: 2.0, Git: 2}, "b": {Passed: false, Elapsed: 1.0}}
	if err := seedFile(root, []string{"a", "b"}, full, bad); err == nil {
		t.Fatal("seedFile seeded a failed tier; want an error")
	}
	if after := readFile(t, path); after != before {
		t.Errorf("a failed measurement changed tests/budget.json:\n%s", after)
	}
}

// T-1004: a budget file without untagged_s is an error, not a zero threshold (which would report every test).
func TestLoadNeedsUntaggedS(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"packages":{"a":{"short_s":1,"full_s":1,"git_procs":0}}}`
	if err := os.WriteFile(filepath.Join(root, "tests", "budget.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := load(root); err == nil {
		t.Error("load accepted a budget file without untagged_s; want an error")
	}
}

// sameTests compares two test lists, allowing the float noise of summed Elapsed values.
func sameTests(got, want []Test) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Name != want[i].Name || math.Abs(got[i].Elapsed-want[i].Elapsed) > 1e-6 {
			return false
		}
	}
	return true
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
