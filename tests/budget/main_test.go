package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// env is one check call's inputs: the budgets, the packages with tests, their two tiers and the private TMPDIR's
// leftovers.
type env struct {
	budget      map[string]Budget
	pkgs        []string
	full, short map[string]Run
	tmp         []string
}

// base is a green run: two packages, every metric under its budget, one 1.5 s test and its Slow mark.
func base() env {
	return env{
		budget: map[string]Budget{
			"a": {ShortS: 2, FullS: 4, GitProcs: 10},
			"b": {ShortS: 5, FullS: 9, GitProcs: 100},
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

// T-1002: one line per breach, in the shape `FAIL budget: <pkg> <metric> <got> > <max>` for a metric, and a line of its
// own for a missing budget, a failed tier, an unmarked 1 s test and the TMPDIR's leftovers.
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
		{"a test over 1 s without Slow", func(e *env) {
			r := e.full["a"]
			r.Tests = []Test{{Name: "TestQuick", Elapsed: 1.2}}
			e.full["a"] = r
		}, []string{"FAIL budget: a test/TestQuick 1.20s >= 1s without testkit.Slow"}},
		{"no budget for a package with tests", func(e *env) {
			delete(e.budget, "b")
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

// T-1002: a Slow-marked test is the short tier's skipped test whose output carries testkit.Slow's message; a top-level
// test's time is the subtest-free pass event, and the package's own verdict is the tier's wall time.
func TestParseRun(t *testing.T) {
	const passing = `{"Action":"start","Package":"p"}
{"Action":"run","Test":"TestA"}
{"Action":"output","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"pass","Test":"TestA","Elapsed":1.3}
{"Action":"run","Test":"TestA/sub"}
{"Action":"pass","Test":"TestA/sub","Elapsed":2.5}
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
	if want := []Test{{Name: "TestA", Elapsed: 1.3}}; !slices.Equal(r.Tests, want) {
		t.Errorf("parseRun's top-level tests %v; want %v (a subtest is its parent's)", r.Tests, want)
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

// T-1002: the seed is each measured tier x1.5 and each git count x1.5, rounded up (a tenth of a second; one process).
func TestSeed(t *testing.T) {
	full := map[string]Run{
		"a": {Elapsed: 2.0, Git: 3},
		"b": {Elapsed: 1.01, Git: 0},
	}
	short := map[string]Run{
		"a": {Elapsed: 1.0},
		"b": {Elapsed: 0.21},
	}
	f := seed(full, short)
	want := map[string]Budget{
		"a": {ShortS: 1.5, FullS: 3.0, GitProcs: 5},
		"b": {ShortS: 0.4, FullS: 1.6, GitProcs: 0},
	}
	for pkg, w := range want {
		if got := f.Packages[pkg]; got != w {
			t.Errorf("seed(%s) = %+v; want %+v", pkg, got, w)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
