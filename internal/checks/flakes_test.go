package checks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
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
// lines (the gate tests), and nothing for the race check (a race report is no flake). No git: the sha is "" when git
// cannot name one.
func TestLogFlakes(t *testing.T) {
	t.Parallel()
	const mixed = "--- FAIL: TestUnit timed out (0.01s)\nFAIL example.com/unit 0.02s\nFAIL example.com/build [build failed]\n"
	for _, c := range []struct {
		name  string
		check string
		o     *Options
		out   string
		want  string // the log's tests, joined; "" none
	}{
		{"a test and the package that did not build", "check", flakeOptions(goFailGrep, goTestLine, goPkgLine), mixed,
			"TestUnit/check example.com/build/check"},
		{"a race run: no log", "race", flakeOptions(goFailGrep, goTestLine, goPkgLine), mixed, ""},
		{"no failed_test: no names, no log", "check", flakeOptions(goFailGrep), mixed, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var warn strings.Builder
			c.o.Git, c.o.Out = "false", &warn // git cannot name a sha
			r := newRunner(t.Context(), c.o, realGit{Scratch{Options: *c.o}}, root, "T-05")
			r.root, r.state = root, filepath.Join(root, "state")
			r.logFlakes(c.check, false, false, output{text: c.out})
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

// FlakyTests: a test is flaky only when it failed and then passed on a retry. Red lines, even on several tasks, are
// reds the gate caught, not flakes; the race check's lines are no evidence at all; a passed-on-retry line counts, and
// its reds do not.
func TestFlakyTests(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const hour = int64(3600)
	at := func(h int64) int64 { return now.Unix() - h*hour }
	for _, c := range []struct {
		name    string
		entries []Flake
		want    string // the names, joined
		wantN   int
	}{
		{name: "red on two tasks, no retry pass: no flake", entries: []Flake{
			{Time: at(2), Test: "TestRed", Check: "check", Task: "T-01"},
			{Time: at(1), Test: "TestRed", Check: "check_affected", Task: "T-02"},
		}},
		{name: "a passed-on-retry line and its red: a flake, ×1", want: "TestSaved", wantN: 1, entries: []Flake{
			{Time: at(2), Test: "TestSaved", Check: "check", Task: "T-01"},
			{Time: at(1), Test: "TestSaved", Check: "check", Task: "T-01", Retried: true, PassedOnRetry: true},
		}},
		{name: "the race check's lines: no flake", entries: []Flake{
			{Time: at(1), Test: "TestRace", Check: "race", Task: "nightly", Retried: true, PassedOnRetry: true},
		}},
		{name: "older than the window: no flake", entries: []Flake{
			{Time: at(8 * 24), Test: "TestOld", Check: "check", Task: "T-01", Retried: true, PassedOnRetry: true},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := FlakyTests(&config.Config{}, c.entries, now)
			var names []string
			for _, f := range got {
				names = append(names, f.Test)
			}
			if strings.Join(names, " ") != c.want {
				t.Fatalf("flaky %q, want %q", strings.Join(names, " "), c.want)
			}
			if c.wantN != 0 && got[0].N != c.wantN {
				t.Errorf("×%d, want ×%d", got[0].N, c.wantN)
			}
		})
	}
}

// flakeLaneTest is the lane test of k's green candidate, which changes feature.txt.
func flakeLaneTest(t *testing.T, k *fk) (port.Candidate, port.TestOutcome) {
	t.Helper()
	c := k.green(lane(k.submit()))
	k.g.answers["Changed "+c.Target+" "+c.SHA] = "feature.txt"
	o, err := k.s.Test(ctx, port.TestInput{Task: "T-05", Lane: "L-01", Branch: "task/T-05", SHA: c.SHA, Target: c.Target})
	if err != nil {
		t.Fatalf("lane test: %v\noutput:\n%s", err, k.out.String())
	}
	return c, o
}

// the check runner appends one line per failing test to .obor/state/flakes.jsonl (a red run), and never on a
// green run; a log it cannot write is a warning, never a red.
func TestFlakeLog(t *testing.T) {
	t.Parallel()
	// Two test failures and the package summary line: the two test names, not the package.
	const twoFails = `echo "--- FAIL: TestAlpha (0.01s)"; echo "--- FAIL: TestBeta (0.02s)"; ` +
		`echo "FAIL example.com/pkg 0.03s"; exit 1`
	t.Run("red run: one line per failing test with the run's task and sha", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, cmd("check", twoFails), goLines())
		c, o := flakeLaneTest(t, k)
		if o.Kind != port.OutcomeRed || !strings.HasPrefix(o.Line, "RED check ") {
			t.Fatalf("lane test: %s %q; want red check", o.Kind, o.Line)
		}
		got, err := Flakes(k.Root)
		if err != nil || len(got) != 2 {
			t.Fatalf("flake lines %+v (%v), want 2", got, err)
		}
		for i, want := range []string{"TestAlpha", "TestBeta"} {
			f := got[i]
			k.eq(f.Test, want, "test")
			k.eq(f.Check, "check", "check")
			k.eq(f.Task, "T-05", "task")
			k.eq(f.SHA, c.SHA, "sha: the candidate the lane test ran on")
			k.eq(fmt.Sprint(f.Retried, f.PassedOnRetry), "false false", "retried and passed_on_retry")
			if f.Time == 0 {
				t.Errorf("%s: no time", want)
			}
		}
	})

	t.Run("green run: no log", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, cmd("check", "echo 2 pass"))
		if _, o := flakeLaneTest(t, k); o.Kind != port.OutcomeGreen {
			t.Fatalf("lane test: %s %q; want green", o.Kind, o.Line)
		}
		k.gone(k.Path(".obor/state/flakes.jsonl"), "no flake log")
	})

	t.Run("unwritable flake log: a warning line, the check result unchanged", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, cmd("check", twoFails), goLines())
		if err := os.MkdirAll(k.Path(".obor/state/flakes.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, o := flakeLaneTest(t, k)
		if o.Kind != port.OutcomeRed || !strings.HasPrefix(o.Line, "RED check ") {
			t.Fatalf("lane test: %s %q; want red check", o.Kind, o.Line)
		}
		k.contains(k.out.String(), "warning: flake log:", "warning line")
		if info, err := os.Stat(k.Path(".obor/state/flakes.jsonl")); err != nil || !info.IsDir() {
			t.Errorf("flakes.jsonl: %v, %v; want the directory that made the write fail", info, err)
		}
	})
}

// the nightly's runner (task "nightly", it retests): every gating command of the full tier logs a red run, and the
// race step's red logs nothing (its WARN line is nonGatingLine's).
func TestFlakeLogNightly(t *testing.T) {
	t.Parallel()
	failing := func(name string) any { return `echo "--- FAIL: Test_` + name + ` (0.01s)"; exit 1` }
	nightlyRunner := func(t *testing.T, edits ...func(map[string]any)) (*fk, *runner) {
		k := newFk(t, append([]func(map[string]any){goLines()}, edits...)...)
		o := k.s.Options
		r := newRunner(ctx, &o, k.g, k.Root, "nightly")
		r.retest = true
		t.Cleanup(r.releaseSlot)
		return k, &r
	}
	t.Run("check and check_milestone", func(t *testing.T) {
		t.Parallel()
		k, r := nightlyRunner(t, cmd("check", failing("check")), cmd("check_milestone", failing("milestone")))
		for _, name := range []string{"check", "check_milestone"} {
			if o, ok, h, _ := r.retested(name); !ok || h != nil || o.rc != 1 {
				t.Fatalf("precondition: %s is red (rc 1), got ok %v, rc %d, halt %+v", name, ok, o.rc, h)
			}
		}
		got, err := Flakes(k.Root)
		if err != nil || len(got) != 2 {
			t.Fatalf("flake lines %+v (%v), want 2", got, err)
		}
		for i, name := range []string{"check", "check_milestone"} {
			k.eq(got[i].Test, "Test_"+strings.TrimPrefix(name, "check_"), "test")
			k.eq(got[i].Check, name, "check")
			k.eq(got[i].Task, "nightly", "task")
			k.eq(got[i].SHA, k.main(), "sha: the nightly's head")
			k.eq(fmt.Sprint(got[i].Retried, got[i].PassedOnRetry), "false false", "no retry: no commands.retest")
		}
	})

	t.Run("lane_setup timeout: logged", func(t *testing.T) {
		t.Parallel()
		k, r := nightlyRunner(t, cmd("lane_setup", map[string]any{"cmd": `echo "--- FAIL: Test_setup (0.01s)"; sleep 5`, "timeout": 1}))
		if h := r.laneSetup(); h == nil || h.red == nil || h.red.rc != 124 {
			t.Fatalf("precondition: lane_setup timed out (red, rc 124), got %+v", h)
		}
		got, err := Flakes(k.Root)
		if err != nil || len(got) != 1 {
			t.Fatalf("flake lines %+v (%v), want 1", got, err)
		}
		k.eq(fmt.Sprint(got[0].Test, "/", got[0].Check, "/", got[0].Retried, "/", got[0].PassedOnRetry),
			"Test_setup/lane_setup/false/false", "log")
	})

	t.Run("green run: no log", func(t *testing.T) {
		t.Parallel()
		k, r := nightlyRunner(t, cmd("check", "echo 1 pass"))
		if o, ok, h, _ := r.retested("check"); !ok || h != nil || o.rc != 0 {
			t.Fatalf("precondition: check is green, got ok %v, rc %d, halt %+v", ok, o.rc, h)
		}
		k.gone(k.Path(".obor/state/flakes.jsonl"), "no flake log")
	})

	t.Run("a race step's red: no flake line", func(t *testing.T) {
		t.Parallel()
		k, r := nightlyRunner(t, cmd("race", `echo "--- FAIL: TestRace (0.01s)"; exit 1`))
		if o, ok, h := r.run("race"); !ok || h != nil || o.rc != 1 {
			t.Fatalf("precondition: race is red (rc 1), got ok %v, rc %d, halt %+v", ok, o.rc, h)
		}
		k.gone(k.Path(".obor/state/flakes.jsonl"), "no flake log")
	})
}
