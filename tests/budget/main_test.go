package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// env is one check call's inputs: the budget file, the packages with tests, their runs and the private TMPDIR's
// leftovers.
type env struct {
	budget File
	pkgs   []string
	runs   map[string]Run
	tmp    []string
}

// base is a green run: two packages, every metric under its budget.
func base() env {
	return env{
		budget: File{
			Packages: map[string]Budget{
				"a": {FullS: 4, GitProcs: 10},
				"b": {FullS: 9, GitProcs: 100},
			},
		},
		pkgs: []string{"a", "b"},
		runs: map[string]Run{
			"a": {Passed: true, Elapsed: 3.9, Git: 10},
			"b": {Passed: true, Elapsed: 8, Git: 50},
		},
	}
}

// T-1002: the green run has no line (the precondition the breach cases rest on) and counts every package.
func TestCheckGreen(t *testing.T) {
	e := base()
	lines, n := check(e.budget, e.pkgs, e.runs, e.tmp)
	if len(lines) != 0 {
		t.Fatalf("a run under every budget printed %q; want none", lines)
	}
	if n != 2 {
		t.Errorf("check compared %d packages; want 2", n)
	}
}

// T-1002, T-1004: one line per breach, in the shape `FAIL budget: <pkg> <metric> <got> > <max>` for a metric, and a
// line of its own for a missing budget, a failed run and the TMPDIR's leftovers.
func TestCheckBreaches(t *testing.T) {
	if lines, _ := check(base().budget, base().pkgs, base().runs, nil); len(lines) != 0 {
		t.Fatalf("precondition: the base run is green, got %q", lines)
	}
	for _, c := range []struct {
		name string
		edit func(*env)
		want []string
	}{
		{"full over budget", func(e *env) {
			r := e.runs["a"]
			r.Elapsed = 4.5
			e.runs["a"] = r
		}, []string{"FAIL budget: a full_s 4.5s > 4.0s"}},
		{"git over budget", func(e *env) {
			r := e.runs["a"]
			r.Git = 11
			e.runs["a"] = r
		}, []string{"FAIL budget: a git_procs 11 > 10"}},
		// T-1024: a cap under a second is the size of go test's own noise, so the floor answers, not the cap.
		{"a wall under the floor over a tiny cap is green", func(e *env) {
			e.budget.Packages["a"] = Budget{FullS: 0.6, GitProcs: 10}
			e.runs["a"] = Run{Passed: true, Elapsed: 0.9, Git: 10}
		}, nil},
		{"a wall over the floor over a tiny cap", func(e *env) {
			e.budget.Packages["a"] = Budget{FullS: 0.6, GitProcs: 10}
			e.runs["a"] = Run{Passed: true, Elapsed: 1.1, Git: 10}
		}, []string{"FAIL budget: a full_s 1.1s > 1.0s"}},
		{"a cap over the floor stays the cap", func(e *env) {
			e.budget.Packages["a"] = Budget{FullS: 1.5, GitProcs: 10}
			e.runs["a"] = Run{Passed: true, Elapsed: 1.6, Git: 10}
		}, []string{"FAIL budget: a full_s 1.6s > 1.5s"}},
		{"no budget for a package with tests", func(e *env) {
			delete(e.budget.Packages, "b")
		}, []string{"FAIL budget: b missing from tests/budget.json"}},
		{"a failed run", func(e *env) {
			r := e.runs["a"]
			r.Passed, r.Failed = false, []string{"TestX", "TestY"}
			e.runs["a"] = r
		}, []string{"FAIL budget: a run failed: TestX, TestY"}},
		{"a failed run with no failed tests", func(e *env) {
			r := e.runs["a"]
			r.Passed, r.note = false, "build constraints exclude all Go files"
			e.runs["a"] = r
		}, []string{"FAIL budget: a run failed: build constraints exclude all Go files"}},
		{"temp entries left", func(e *env) {
			e.tmp = []string{"obor-leftover-1", "obor-leftover-2"}
		}, []string{"FAIL budget: tmp 2 entries left in the private TMPDIR: obor-leftover-1, obor-leftover-2"}},
		{"several at once", func(e *env) {
			r := e.runs["b"]
			r.Elapsed, r.Git = 9.5, 101
			e.runs["b"] = r
		}, []string{"FAIL budget: b full_s 9.5s > 9.0s", "FAIL budget: b git_procs 101 > 100"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := base()
			c.edit(&e)
			lines, n := check(e.budget, e.pkgs, e.runs, e.tmp)
			if !slices.Equal(lines, c.want) {
				t.Errorf("check printed %q; want %q", lines, c.want)
			}
			if n != len(e.pkgs) {
				t.Errorf("check compared %d packages; want %d", n, len(e.pkgs))
			}
		})
	}
}

// T-1073: -missing names every package with tests and no entry, with the seed command that writes it, and every entry
// whose package is gone, and says nothing when the two match.
func TestMissing(t *testing.T) {
	matched := File{Packages: map[string]Budget{"a": {}, "b": {}}}
	if lines := missing([]string{"a", "b"}, matched); len(lines) != 0 {
		t.Errorf("a matched file printed %q; want nothing", lines)
	}

	got := missing([]string{"a", "c", "d"}, File{Packages: map[string]Budget{"a": {}, "gone": {}}})
	want := []string{
		"FAIL budget: c missing from tests/budget.json — run: go run ./tests/budget -seed c",
		"FAIL budget: d missing from tests/budget.json — run: go run ./tests/budget -seed d",
		"FAIL budget: gone in tests/budget.json has no package with tests — delete its entry",
	}
	if !slices.Equal(got, want) {
		t.Errorf("missing printed %q; want %q", got, want)
	}
}

// T-1073: -missing exits 1 when a package with tests has no entry, and 0 when the file matches: the gate reads the
// status, not the text.
func TestMissingExitRed(t *testing.T) {
	got := runMissing(t,
		[]string{"example.com/x", "example.com/y"},
		`{"packages":{"example.com/x":{"full_s":1,"git_procs":0}}}`)
	if got != 1 {
		t.Errorf("-missing exited %d with example.com/y unmeasured; want 1", got)
	}
}

func TestMissingExitGreen(t *testing.T) {
	got := runMissing(t,
		[]string{"example.com/x"},
		`{"packages":{"example.com/x":{"full_s":1,"git_procs":0}}}`)
	if got != 0 {
		t.Errorf("-missing exited %d with every package measured; want 0", got)
	}
}

// runMissing calls the tool's -missing once: in a scratch module whose `go list` (a stub named by GO) names pkgs, against
// a tests/budget.json holding budget. It returns the exit status. One call per test: run registers its flags on
// flag.CommandLine.
func runMissing(t *testing.T, pkgs []string, budget string) int {
	t.Helper()
	dir := t.TempDir()
	writeModule(t, dir, budget)
	var list strings.Builder
	list.WriteString("#!/bin/sh\nprintf '")
	for _, pkg := range pkgs {
		list.WriteString(pkg + " 1 0\\n")
	}
	list.WriteString("'\n")
	goBin := filepath.Join(dir, "go")
	if err := os.WriteFile(goBin, []byte(list.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO", goBin)
	t.Chdir(dir)

	savedFlags, savedArgs := flag.CommandLine, os.Args
	flag.CommandLine = flag.NewFlagSet("budget", flag.ContinueOnError)
	os.Args = []string{"budget", "-missing"}
	t.Cleanup(func() { flag.CommandLine, os.Args = savedFlags, savedArgs })
	return run()
}

// T-1002: the package's own verdict is the run's wall time, and its failed top-level tests are named.
func TestParseRun(t *testing.T) {
	const passing = `{"Action":"start","Package":"p"}
{"Action":"run","Test":"TestA"}
{"Action":"output","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"run","Test":"TestA/sub"}
{"Action":"pass","Test":"TestA/sub","Elapsed":0.5}
{"Action":"pass","Test":"TestA","Elapsed":1.3}
{"Action":"run","Test":"TestSkipped"}
{"Action":"output","Test":"TestSkipped","Output":"    p_test.go:12: needs the network\n"}
{"Action":"skip","Test":"TestSkipped","Elapsed":0}
{"Action":"pass","Package":"p","Elapsed":4.2}
`
	r := parseRun([]byte(passing))
	if !r.Passed || r.Elapsed != 4.2 {
		t.Errorf("parseRun: passed %v elapsed %v; want true 4.2", r.Passed, r.Elapsed)
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

// T-1002: the seed is each measured wall time x1.5 and each git count x1.5, rounded up (a tenth of a second; one
// process).
func TestSeed(t *testing.T) {
	runs := map[string]Run{
		"a": {Elapsed: 2.0, Git: 3},
		"b": {Elapsed: 1.01, Git: 0},
	}
	f := seed(runs)
	want := map[string]Budget{
		"a": {FullS: 3.0, GitProcs: 5},
		"b": {FullS: 1.6, GitProcs: 0},
	}
	for pkg, w := range want {
		if got := f.Packages[pkg]; got != w {
			t.Errorf("seed(%s) = %+v; want %+v", pkg, got, w)
		}
	}
}

// T-1007: -seed re-measures the packages of the file it finds; an older file's per-test section (the -short tier's)
// is not carried over, and no file seeds the same measurement.
func TestSeededKeepsCaps(t *testing.T) {
	runs := map[string]Run{"a": {Elapsed: 2.0, Git: 2}}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := `{"untagged_s":2.5,"tests":{"a TestSlow":4},"packages":{"a":{"short_s":1,"full_s":1,"git_procs":0}}}`
	if err := os.WriteFile(filepath.Join(root, "tests", "budget.json"), []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := seeded(root, nil, runs)
	if err != nil {
		t.Fatal(err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("seeded: %v", err)
	}
	if got, want := f.Packages["a"], (Budget{FullS: 3.0, GitProcs: 3}); got != want {
		t.Errorf("seeded package a = %+v; want the measurement %+v", got, want)
	}

	g, err := seeded(t.TempDir(), nil, runs)
	if err != nil {
		t.Fatal(err)
	}
	var noFile File
	if err := json.Unmarshal(g, &noFile); err != nil {
		t.Fatalf("seeded with no file: %v", err)
	}
	if got, want := noFile.Packages["a"], (Budget{FullS: 3.0, GitProcs: 3}); got != want {
		t.Errorf("seed with no file: package a = %+v; want the measurement %+v", got, want)
	}
}

// T-1008 (RV-139 #2): -seed writes tests/budget.json itself, over the caps of the file it finds and complete for every
// measured package; a run that failed writes nothing, so the old file stays byte for byte.
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
	runs := map[string]Run{"a": {Passed: true, Elapsed: 2.0, Git: 2}, "b": {Passed: true, Elapsed: 1.0}}
	if err := seedFile(root, nil, runs); err != nil {
		t.Fatalf("seedFile: %v", err)
	}
	got, err := load(root)
	if err != nil {
		t.Fatalf("the seeded file does not load: %v", err)
	}
	if len(got.Packages) != 2 {
		t.Errorf("seeded packages = %v; want both measured packages", got.Packages)
	}
	if b, want := got.Packages["b"], (Budget{FullS: 1.5, GitProcs: 0}); b != want {
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
	if err := seedFile(root, nil, bad); err == nil {
		t.Fatal("seedFile seeded a failed run; want an error")
	}
	if after := readFile(t, path); after != before {
		t.Errorf("a failed measurement changed tests/budget.json:\n%s", after)
	}
}

// T-1073: -seed with package arguments merges: the named packages get their new measurement, every other entry stays
// byte for byte (even a hand-formatted 4.0, which re-marshalling the whole file would rewrite as 4).
func TestSeedMerges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := `{
  "packages": {
    "a": {
      "full_s": 4.0,
      "git_procs": 1
    },
    "b": {
      "full_s": 1.0,
      "git_procs": 1
    }
  }
}
`
	path := filepath.Join(root, "tests", "budget.json")
	if err := os.WriteFile(path, []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedFile(root, []string{"b"}, map[string]Run{"b": {Passed: true, Elapsed: 4.0, Git: 3}}); err != nil {
		t.Fatalf("seedFile (b only): %v", err)
	}
	after := readFile(t, path)
	f, err := load(root)
	if err != nil {
		t.Fatalf("the merged file does not load: %v", err)
	}
	if got, want := f.Packages["b"], (Budget{FullS: 6.0, GitProcs: 5}); got != want {
		t.Errorf("seeded package b = %+v; want the new measurement %+v", got, want)
	}
	if got, want := f.Packages["a"], (Budget{FullS: 4.0, GitProcs: 1}); got != want {
		t.Errorf("seeding b changed package a to %+v; want %+v", got, want)
	}
	if got, want := entry(t, after, "a"), entry(t, prev, "a"); got != want {
		t.Errorf("seeding b rewrote package a: %s; want %s (byte for byte)", got, want)
	}
}

// T-1073: -seed with no arguments is today's behaviour, the whole measurement: an entry no run measured (its package
// moved or was deleted) is dropped rather than kept.
func TestSeedWholeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := seedFile(root, nil, map[string]Run{
		"a": {Passed: true, Elapsed: 2.0, Git: 2},
		"b": {Passed: true, Elapsed: 1.0, Git: 1},
	}); err != nil {
		t.Fatalf("seedFile: %v", err)
	}
	if err := seedFile(root, nil, map[string]Run{"a": {Passed: true, Elapsed: 2.0, Git: 2}}); err != nil {
		t.Fatalf("seedFile: %v", err)
	}
	f, err := load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Packages["b"]; ok {
		t.Errorf("a whole-file seed kept package b, which no run measured: %v", f.Packages)
	}
	if _, ok := f.Packages["a"]; !ok {
		t.Errorf("a whole-file seed dropped the measured package a: %v", f.Packages)
	}
}

// entry is one package's entry in a tests/budget.json file, raw: a merge must leave the entries it did not measure
// byte for byte.
func entry(t *testing.T, file, pkg string) string {
	t.Helper()
	var f struct {
		Packages map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(file), &f); err != nil {
		t.Fatalf("tests/budget.json: %v", err)
	}
	return string(f.Packages[pkg])
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// T-1014: a package's run passes -tags integration, so a test behind `//go:build integration` is measured instead of
// silently skipped; T-1056: nothing passes -short.
func TestTestArgsIntegration(t *testing.T) {
	args := testArgs("obor/internal/checks")
	if !slices.Contains(args, "integration") {
		t.Errorf("testArgs() = %q; want -tags integration", args)
	}
	if slices.Contains(args, "-short") {
		t.Errorf("testArgs() = %q; want no -short", args)
	}
	if args[len(args)-1] != "obor/internal/checks" {
		t.Errorf("testArgs() = %q; the package is not last", args)
	}
}

// T-1014: go list passes the tag too, so a package whose only tests are tagged is measured rather than dropped.
func TestListPackagesTagsIntegration(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "go.log")
	goBin := filepath.Join(dir, "go")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quote(log) + "\n" +
		"printf 'example.com/tagged 0 1\\nexample.com/none 0 0\\n'\n"
	if err := os.WriteFile(goBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pkgs, err := listPackages(dir, goBin, os.Environ())
	if err != nil {
		t.Fatalf("listPackages: %v", err)
	}
	if want := []string{"example.com/tagged"}; !slices.Equal(pkgs, want) {
		t.Errorf("listPackages = %v; want %v (a package with only tagged tests counts)", pkgs, want)
	}
	if call := readFile(t, log); !strings.Contains(call, "-tags integration") {
		t.Errorf("go list ran without -tags integration: %q", call)
	}
}

// T-1077 (RV-156 #1): -seed ./rel/pkg measures the package and writes its entry under the import path the listing and
// -missing key by, not under the raw argument.
func TestSeedWritesImportPathKey(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, `{"packages":{}}`)
	goBin := goStub(t, map[string]string{
		"./internal/newpkg": "example.com/x/internal/newpkg 1 0\\n",
	})
	if status := runSeed(t, root, goBin, "-seed", "./internal/newpkg"); status != 0 {
		t.Fatalf("-seed ./internal/newpkg exited %d; want 0", status)
	}
	f, err := load(root)
	if err != nil {
		t.Fatalf("the seeded file does not load: %v", err)
	}
	if _, ok := f.Packages["./internal/newpkg"]; ok {
		t.Errorf("the seed wrote the raw argument as a key: %v", f.Packages)
	}
	if got, ok := f.Packages["example.com/x/internal/newpkg"]; !ok || got != (Budget{FullS: 1.5, GitProcs: 0}) {
		t.Errorf("seeded packages = %v; want example.com/x/internal/newpkg measured at 1.5s", f.Packages)
	}
}

// T-1077: -seed resolves each argument through go list: a pattern expands to its packages with tests, two arguments
// keep their order, and an import path is kept as given.
func TestResolvePackages(t *testing.T) {
	goBin := goStub(t, map[string]string{
		"./internal/...":             "example.com/x/internal/a 1 0\\nexample.com/x/internal/b 0 1\\nexample.com/x/internal/notests 0 0\\n",
		"./internal/newpkg":          "example.com/x/internal/newpkg 1 0\\n",
		"example.com/x/internal/foo": "example.com/x/internal/foo 2 0\\n",
	})
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"a pattern is its packages with tests", []string{"./internal/..."}, []string{"example.com/x/internal/a", "example.com/x/internal/b"}},
		{"two arguments keep their order", []string{"./internal/newpkg", "example.com/x/internal/foo"}, []string{"example.com/x/internal/newpkg", "example.com/x/internal/foo"}},
		{"an import path is kept as given", []string{"example.com/x/internal/foo"}, []string{"example.com/x/internal/foo"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolvePackages(t.TempDir(), goBin, os.Environ(), c.args)
			if err != nil {
				t.Fatalf("resolvePackages(%q): %v", c.args, err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("resolvePackages(%q) = %q; want %q", c.args, got, c.want)
			}
		})
	}
}

// T-1077: an argument that resolves to no package with tests is an error naming it (a path that matches nothing, and a
// package whose tests are all gone), so the seed can be corrected.
func TestResolvePackagesNoTests(t *testing.T) {
	goBin := goStub(t, map[string]string{
		"./internal/newpkg":  "example.com/x/internal/newpkg 1 0\\n",
		"./internal/notests": "example.com/x/internal/notests 0 0\\n",
	})
	for _, arg := range []string{"./internal/nope", "./internal/notests"} {
		got, err := resolvePackages(t.TempDir(), goBin, os.Environ(), []string{"./internal/newpkg", arg})
		if err == nil {
			t.Fatalf("resolvePackages(%q) = %q; want an error", arg, got)
		}
		if !strings.Contains(err.Error(), arg) {
			t.Errorf("resolvePackages(%q) error %q; want it to name the argument", arg, err)
		}
	}
}

// T-1077: -seed with an argument that resolves to no package with tests fails before measuring: tests/budget.json stays
// byte for byte.
func TestSeedUnresolvedArgWritesNothing(t *testing.T) {
	root := t.TempDir()
	prev := `{"packages":{"example.com/x/old":{"full_s":1,"git_procs":0}}}`
	writeModule(t, root, prev)
	goBin := goStub(t, map[string]string{
		"./internal/newpkg": "example.com/x/internal/newpkg 1 0\\n",
	})
	if status := runSeed(t, root, goBin, "-seed", "./internal/nope"); status != 2 {
		t.Fatalf("-seed ./internal/nope exited %d; want 2", status)
	}
	if after := readFile(t, filepath.Join(root, "tests", "budget.json")); after != prev {
		t.Errorf("-seed with an unresolved argument rewrote tests/budget.json:\n%s", after)
	}
}

// runSeed calls the tool once with args in root, GO as goBin: one call per test (run registers its flags on
// flag.CommandLine).
func runSeed(t *testing.T, root, goBin string, args ...string) int {
	t.Helper()
	t.Setenv("GO", goBin)
	t.Chdir(root)
	savedFlags, savedArgs := flag.CommandLine, os.Args
	flag.CommandLine = flag.NewFlagSet("budget", flag.ContinueOnError)
	os.Args = append([]string{"budget"}, args...)
	t.Cleanup(func() { flag.CommandLine, os.Args = savedFlags, savedArgs })
	return run()
}

// writeModule makes dir a module with tests/budget.json holding budget.
func writeModule(t *testing.T, dir, budget string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "budget.json"), []byte(budget), 0o644); err != nil {
		t.Fatal(err)
	}
}

// goStub writes a `go` for one test: a `test` call passes with Elapsed 1.0, and a `list` call answers from cases, keyed
// by its last argument (the package or pattern) with what go list would print for it. An argument that is no key prints
// nothing, like a pattern that matches no packages.
func goStub(t *testing.T, cases map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(cases))
	for k := range cases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("#!/bin/sh\nif [ \"$1\" = test ]; then\n  printf '{\"Action\":\"pass\",\"Package\":\"p\",\"Elapsed\":1.0}\\n'\n  exit 0\nfi\n")
	b.WriteString("for last; do :; done\ncase \"$last\" in\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s) printf '%s' ;;\n", quote(k), cases[k])
	}
	b.WriteString("esac\n")
	goBin := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(goBin, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return goBin
}
