package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
)

// flakeOptions is an Options whose checks' failing lines are read as failGrep's matches and named by
// checks.output.failed_test and failed_package, the pair.
func flakeOptions(failGrep string, pair ...string) *Options {
	cfg := &config.Config{}
	o := &cfg.Data.Checks.Output
	o.Failure = failGrep
	if len(pair) == 2 {
		o.FailedTest, o.FailedPackage = pair[0], pair[1]
	}
	return &Options{Cfg: cfg}
}

// the flake log's names and the retest's tests and packages come from merge.flake_regex, not from patterns in
// the code: obor-kit's Go pair names the same, a project's own pair reads its lines, and a
// project with no pair parses nothing, with no error.
func TestFlakeParse(t *testing.T) {
	t.Parallel()
	goPair := []string{goTestLine, goPkgLine}
	pytestPair := []string{`^FAILED (?P<test>\S+)`, `^ERROR (?P<package>\S+)`}
	for _, c := range []struct {
		name, grep, out    string
		pair               []string
		names, tests, pkgs string
	}{
		{name: "tests only: their names, not the package",
			grep: goFailGrep, pair: goPair,
			out:   "--- FAIL: TestA (0.01s)\n--- FAIL: TestB (0.01s)\nFAIL example.com/a 0.03s\nFAIL\n",
			names: "TestA TestB", tests: "TestA TestB", pkgs: "example.com/a"},
		{name: "packages only: the packages",
			grep: goFailGrep, pair: goPair,
			out:   "FAIL example.com/a [build failed]\nFAIL example.com/b 1s\nFAIL\n",
			names: "example.com/a example.com/b", pkgs: "example.com/a example.com/b"},
		{name: "a test and a build failure of another package: both",
			grep: goFailGrep, pair: goPair,
			out:   "--- FAIL: TestUnit (0.01s)\nFAIL example.com/unit 0.02s\nFAIL example.com/build [build failed]\n",
			names: "TestUnit example.com/build", tests: "TestUnit", pkgs: "example.com/unit example.com/build"},
		{name: "a test and a panic or timeout of another package: both",
			grep: goFailGrep, pair: goPair,
			out:   "--- FAIL: TestUnit (0.01s)\nFAIL example.com/unit 0.02s\nFAIL example.com/hang 60s\n",
			names: "TestUnit example.com/hang", tests: "TestUnit", pkgs: "example.com/unit example.com/hang"},
		{name: "a package with no test line first: its package, then the next package's test",
			grep: goFailGrep, pair: goPair,
			out:   "FAIL example.com/build [build failed]\n--- FAIL: TestUnit (0.01s)\nFAIL example.com/unit 0.02s\n",
			names: "example.com/build TestUnit", tests: "TestUnit", pkgs: "example.com/build example.com/unit"},
		{name: "two packages with tests: no package name beside the tests",
			grep: goFailGrep, pair: goPair,
			out:   "--- FAIL: TestA (0s)\nFAIL example.com/a 1s\n--- FAIL: TestB (0s)\nFAIL example.com/b 1s\n",
			names: "TestA TestB", tests: "TestA TestB", pkgs: "example.com/a example.com/b"},
		{name: "a check's own FAIL line (no module path) is no package",
			grep: goFailGrep, pair: goPair,
			out: "FAIL make build\n", names: "", tests: "", pkgs: ""},
		{name: "a project's own lines: pytest's FAILED and ERROR",
			grep: `^FAILED|^ERROR`, pair: pytestPair,
			out:   "ERROR tests/test_db.py - ImportError\nFAILED tests/test_api.py::test_login - assert 1 == 2\n",
			names: "tests/test_db.py tests/test_api.py::test_login", tests: "tests/test_api.py::test_login",
			pkgs: "tests/test_db.py"},
		{name: "no merge.flake_regex: nothing, no error",
			grep: goFailGrep,
			out:  "--- FAIL: TestA (0.01s)\nFAIL example.com/a 0.03s\n", names: "", tests: "", pkgs: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			names, tests, pkgs := flakeOptions(c.grep, c.pair...).flakeParse(output{text: c.out})
			for _, got := range []struct{ what, got, want string }{
				{"names", strings.Join(names, " "), c.names},
				{"tests", strings.Join(tests, " "), c.tests},
				{"pkgs", strings.Join(pkgs, " "), c.pkgs},
			} {
				if got.got != got.want {
					t.Errorf("%s = %q, want %q", got.what, got.got, got.want)
				}
			}
		})
	}
}

// commands.retest's {tests} is the failing tests joined by merge.retest_join, in the run's order; a template
// with no placeholder is its own command.
func TestRetestCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		join  string
		tests []string
		want  string
	}{
		{"one test", "", []string{"TestAlpha"}, "make -s check-retest TESTS='^(TestAlpha)$'"},
		{"three tests", "", []string{"TestA", "TestB", "TestC"}, "make -s check-retest TESTS='^(TestA|TestB|TestC)$'"},
		{"a project's join", ",", []string{"TestA", "TestB"}, "make -s check-retest TESTS='^(TestA,TestB)$'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{}
			cfg.Data.Merge.Retest.Join = c.join
			r := runner{Options: &Options{Cfg: cfg}}
			got := r.retestCommand(config.Command{Cmd: "make -s check-retest TESTS='^({tests})$'"}, c.tests)
			if got.Cmd != c.want {
				t.Errorf("command = %q, want %q", got.Cmd, c.want)
			}
		})
	}
	if got := (&runner{Options: &Options{Cfg: &config.Config{}}}).
		retestCommand(config.Command{Cmd: "make -s check-retest"}, []string{"TestA"}); got.Cmd != "make -s check-retest" {
		t.Errorf("a template with no placeholder = %q, want it unchanged", got.Cmd)
	}
}

// logFlakes writes what flakeParse names, one line per name (a test and the package that did not build, both), and
// nothing, with no warning, for a project with no checks.output.failed_test: the red's report still names the failing
// lines (the gate tests). No git: the sha is "" when git cannot name one.
func TestLogFlakes(t *testing.T) {
	t.Parallel()
	const mixed = "--- FAIL: TestUnit timed out (0.01s)\nFAIL example.com/unit 0.02s\nFAIL example.com/build [build failed]\n"
	for _, c := range []struct {
		name string
		o    *Options
		out  string
		want string // the log's tests, joined; "" none
	}{
		{"a test and the package that did not build", flakeOptions(goFailGrep, goTestLine, goPkgLine), mixed,
			"TestUnit/check example.com/build/check"},
		{"no failed_test: no names, no log", flakeOptions(goFailGrep), mixed, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var warn strings.Builder
			c.o.Git, c.o.Out = "false", &warn // git cannot name a sha
			r := newRunner(t.Context(), c.o, root, "T-05")
			r.root, r.state = root, filepath.Join(root, "state")
			r.logFlakes("check", false, false, output{text: c.out})
			if warn.Len() != 0 {
				t.Errorf("warning %q, want none", warn.String())
			}
			b, err := os.ReadFile(filepath.Join(r.state, "flakes.jsonl"))
			if c.want == "" {
				if err == nil {
					t.Errorf("flake log written: %q", b)
				}
				return
			}
			if err != nil {
				t.Fatalf("flake log: %v", err)
			}
			var got []string
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				var f Flake
				if err := json.Unmarshal([]byte(l), &f); err != nil {
					t.Fatalf("flake line %q: %v", l, err)
				}
				if f.Task != "T-05" || f.SHA != "" || f.Retried || f.PassedOnRetry {
					t.Errorf("flake %+v: want task T-05, no sha, no retry", f)
				}
				got = append(got, f.Test+"/"+f.Check)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("logged %q, want %q", got, c.want)
			}
		})
	}
}
