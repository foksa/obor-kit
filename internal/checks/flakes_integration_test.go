//go:build integration

package checks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/testkit"
)

// eqBool fails unless got is want.
func (x *fx) eqBool(got, want bool, what string) {
	x.t.Helper()
	if got != want {
		x.t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// flakes reads the fixture's flake log, failing the test on a missing or malformed line.
func (x *fx) flakes() []Flake {
	x.t.Helper()
	b, err := os.ReadFile(x.Path(".obor/state/flakes.jsonl"))
	if err != nil {
		x.t.Fatalf("flake log: %v", err)
	}
	var out []Flake
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l == "" {
			continue
		}
		var f Flake
		if err := json.Unmarshal([]byte(l), &f); err != nil {
			x.t.Fatalf("flake line %q: %v", l, err)
		}
		out = append(out, f)
	}
	return out
}

// T-350: the check runner appends one line per failing test to .obor/state/flakes.jsonl (a red run), and never on a
// green run; a log it cannot write is a warning, never a red.
func TestFlakeLog(t *testing.T) {
	t.Parallel()
	// Two test failures and the package summary line: the two test names, not the package.
	const twoFails = `echo "--- FAIL: TestAlpha (0.01s)"; echo "--- FAIL: TestBeta (0.02s)"; ` +
		`echo "FAIL example.com/pkg 0.03s"; exit 1`
	const failGrep = `^--- FAIL|^FAIL\s`

	t.Run("red run: one line per failing test with the run's task and sha", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", twoFails), testkit.Set("merge.fail_grep", failGrep))
		x.is(x.gate("L-01", "T-05", "red"), Red, `^RED check `)
		base := x.cand.SHA // T-961: the lane test runs on the candidate, the slot's merge commit
		got := x.flakes()
		if len(got) != 2 {
			t.Fatalf("flake lines %d, want 2: %+v", len(got), got)
		}
		for i, want := range []string{"TestAlpha", "TestBeta"} {
			f := got[i]
			x.eq(f.Test, want, "test")
			x.eq(f.Check, "check", "check")
			x.eq(f.Task, "T-05", "task")
			x.eq(f.SHA, base, "sha")
			x.eqBool(f.Retried, false, "retried")
			x.eqBool(f.PassedOnRetry, false, "passed_on_retry")
			if f.Time == 0 {
				t.Errorf("%s: no time", want)
			}
		}
	})

	t.Run("green run: no log", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", "echo 2 pass"))
		x.is(x.gate("L-01", "T-05", "green"), OK, `^GREEN T-05 `)
		x.gone(".obor/state/flakes.jsonl", "no flake log")
	})

	t.Run("unwritable flake log: a warning line, the check result unchanged", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", twoFails), testkit.Set("merge.fail_grep", failGrep))
		if err := os.MkdirAll(filepath.Join(x.Root, ".obor/state/flakes.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		x.is(x.gate("L-01", "T-05", "red"), Red, `^RED check `)
		x.contains(x.out.String(), "warning: flake log:", "warning line")
		if info, err := os.Stat(filepath.Join(x.Root, ".obor/state/flakes.jsonl")); err != nil || !info.IsDir() {
			t.Errorf("flakes.jsonl: %v, %v; want the directory that made the write fail", info, err)
		}
	})
}

// logged is the log's lines, with the checks and the retry marks as "test/check/retried/passed".
func (x *fx) logged() []string {
	x.t.Helper()
	var out []string
	for _, f := range x.flakes() {
		out = append(out, fmt.Sprintf("%s/%s/%v/%v", f.Test, f.Check, f.Retried, f.PassedOnRetry))
	}
	return out
}

// eqList fails unless got is want.
func (x *fx) eqList(got, want []string, what string) {
	x.t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		x.t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

// T-359 (RV-39 #3): every command of the full tier logs a red run, and the result is unchanged.
func TestFlakeLogNightly(t *testing.T) {
	t.Parallel()
	failing := func(name string) any { return `echo "--- FAIL: Test_` + name + ` (0.01s)"; exit 1` }
	grep := testkit.Set("merge.fail_grep", `^--- FAIL|^FAIL\s`)
	t.Run("NIGHTLY: check, check_milestone, baseline, gate and e2e", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, grep, cmd("check", failing("check")), cmd("check_milestone", failing("milestone")),
			cmd("baseline", map[string]any{"cmd": failing("baseline"), "dir": "base"}),
			cmd("gate", failing("gate")), cmd("e2e", failing("e2e")))
		os.Mkdir(x.Path("base"), 0o755)
		x.is(x.nightly(), Red, `^NIGHTLY RED .*failed: check, check_milestone, baseline, gate, e2e;`)
		got := x.flakes()
		if len(got) != 5 {
			t.Fatalf("flake lines %d, want 5: %+v", len(got), got)
		}
		for i, name := range []string{"check", "check_milestone", "baseline", "gate", "e2e"} {
			x.eq(got[i].Test, "Test_"+strings.TrimPrefix(name, "check_"), "test")
			x.eq(got[i].Check, name, "check")
			x.eq(got[i].Task, "nightly", "task")
			x.eqBool(got[i].Retried || got[i].PassedOnRetry, false, "no retry")
		}
	})

	t.Run("lane_setup timeout: logged, the rest not run", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, grep, cmd("lane_setup", map[string]any{"cmd": `echo "--- FAIL: Test_setup (0.01s)"; sleep 5`, "timeout": 1}),
			cmd("check", "echo 1 pass"))
		x.is(x.nightly(), Red, `^NIGHTLY RED .*failed: lane_setup;`)
		x.eqList(x.logged(), []string{"Test_setup/lane_setup/false/false"}, "log")
	})

	t.Run("green nightly: no log", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, grep, cmd("check", "echo 1 pass"))
		x.is(x.nightly(), OK, `^NIGHTLY GREEN `)
		x.gone(".obor/state/flakes.jsonl", "no flake log")
	})
}

// T-359 (RV-39 #4): an independent FAIL <pkg> line is logged unless a --- FAIL: name of that package covers it.
func TestFailingNames(t *testing.T) {
	t.Parallel()
	opt := &Options{}
	opt.Cfg = &config.Config{}
	opt.Cfg.Data.Merge.FailGrep = `^--- FAIL|^FAIL\s`
	for _, c := range []struct{ name, out, want string }{
		{"tests only: their names, not the package", "--- FAIL: TestA (0.01s)\n--- FAIL: TestB (0.01s)\nFAIL example.com/a 0.03s\nFAIL\n", "TestA TestB"},
		{"packages only: the packages", "FAIL example.com/a [build failed]\nFAIL example.com/b 1s\nFAIL\n", "example.com/a example.com/b"},
		{"a test and a build failure of another package: both", "--- FAIL: TestUnit (0.01s)\nFAIL example.com/unit 0.02s\nFAIL example.com/build [build failed]\n", "TestUnit example.com/build"},
		{"a test and a panic or timeout of another package: both", "--- FAIL: TestUnit (0.01s)\nFAIL example.com/unit 0.02s\nFAIL example.com/hang 60s\n", "TestUnit example.com/hang"},
		{"a package with no test line first: its package, then the next package's test", "FAIL example.com/build [build failed]\n--- FAIL: TestUnit (0.01s)\nFAIL example.com/unit 0.02s\n", "example.com/build TestUnit"},
		{"two packages with tests: no package", "--- FAIL: TestA (0s)\nFAIL example.com/a 1s\n--- FAIL: TestB (0s)\nFAIL example.com/b 1s\n", "TestA TestB"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := strings.Join(opt.failingNames(output{text: c.out}), " ")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}

	t.Run("a merge's mixed output: test names, a build failure", func(t *testing.T) {
		t.Parallel()
		const mixed = `echo "--- FAIL: TestUnit timed out (0.01s)"; echo "FAIL example.com/unit 0.02s"; ` +
			`echo "FAIL example.com/build [build failed]"; exit 1`
		x := newFx(t, testkit.Set("merge", map[string]any{"fail_grep": `^--- FAIL|^FAIL\s`}), cmd("check", mixed))
		x.is(x.gate("L-01", "T-05", "red"), Red, `^RED check `)
		x.eqList(x.logged(), []string{"TestUnit/check/false/false", "example.com/build/check/false/false"}, "red")
	})
}

// flakeFx is a project whose internal/x/x_test.go defines TestLedger — the test a red can be explained by — with the
// target branch and paths a flake rule reads.
type flakeFx struct {
	*testkit.Project
	t   *testing.T
	cfg *config.Config
}

func newFlakeFx(t *testing.T) *flakeFx {
	t.Helper()
	p := testkit.NewAt(t, testkit.Dir(t))
	p.Write("internal/x/x.go", "package x\n")
	p.Write("internal/x/x_test.go", "package x\n\nfunc TestLedger(t *testing.T) {}\n")
	p.Write("shared.txt", "one\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "fixture")
	cfg := &config.Config{Root: p.Root}
	cfg.Data.Branches.Main = "main"
	return &flakeFx{Project: p, t: t, cfg: cfg}
}

// task commits files on branch task/<task>, merges it onto main ("Merge <task>: change" as obor does) and returns the
// lane commit's sha: the sha a merge's checks ran on.
func (x *flakeFx) task(task string, files map[string]string) string {
	x.t.Helper()
	branch := "task/" + task
	x.Git(x.Root, "checkout", "-q", "-b", branch)
	for path, text := range files {
		x.Write(path, text)
	}
	x.Git(x.Root, "add", "-A")
	x.Git(x.Root, "commit", "-q", "-m", task+": change")
	sha := x.Git(x.Root, "rev-parse", "HEAD")
	x.Git(x.Root, "checkout", "-q", "main")
	x.Git(x.Root, "merge", "-q", "--no-ff", "-m", "Merge "+task+": change", branch)
	return sha
}

// spec stores task's spec with the given title (the store: the flakes rule's only source, T-521).
func (x *flakeFx) spec(task, title string) {
	x.Spec(task, "# "+task+" — "+title+"\n\nDifficulty: tiny.\n")
}

// mergedAt records task's merge at epoch at in the store, as obor serve does: fact merged (T-525).
func (x *flakeFx) mergedAt(task string, at int64) {
	x.MergedAt(task, x.Git(x.Root, "rev-parse", "main"), false, at)
}

// red is one red log line of test on task at epoch at, at sha.
func red(test, task, sha string, at int64) Flake {
	return Flake{Time: at, Test: test, Check: "check", Task: task, SHA: sha}
}

// flakyNames is the flaky tests' names, in order.
func flakyNames(got []Flaky) string {
	var out []string
	for _, f := range got {
		out = append(out, f.Test)
	}
	return strings.Join(out, " ")
}

// T-399: a red does not count when the red task's diff touched the file that defines the test, or when it came before
// the merge of a task whose spec title names the test; a red on an untouched file, after that merge, or a package name
// still counts.
func TestFlakyRule(t *testing.T) {
	t.Parallel()
	const test = "TestLedger"
	now := time.Now()
	at := func(offset int64) int64 { return now.Unix() + offset }
	const body = "package x\n\nfunc TestLedger(t *testing.T) { %s }\n"

	t.Run("the red task changed the file that defines the test: no alert (T-386, T-390)", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		t1 := x.task("T-01", map[string]string{"internal/x/x_test.go": fmt.Sprintf(body, "t.Log(1)")})
		t2 := x.task("T-02", map[string]string{"internal/x/x_test.go": fmt.Sprintf(body, "t.Log(2)")})
		got := FlakyTests(x.cfg, []Flake{red(test, "T-01", t1, at(-200)), red(test, "T-02", t2, at(-100))}, now)
		if len(got) != 0 {
			t.Errorf("flaky %q, want none: the red task's own change explains it", flakyNames(got))
		}
	})

	t.Run("the red sha is a merge's target: the task's merged diff explains it (T-386, T-390)", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		before1 := x.Git(x.Root, "rev-parse", "main")
		x.task("T-01", map[string]string{"internal/x/x_test.go": fmt.Sprintf(body, "t.Log(1)")})
		before2 := x.Git(x.Root, "rev-parse", "main")
		x.task("T-02", map[string]string{"internal/x/x_test.go": fmt.Sprintf(body, "t.Log(2)")})
		got := FlakyTests(x.cfg, []Flake{red(test, "T-01", before1, at(-200)), red(test, "T-02", before2, at(-100))}, now)
		if len(got) != 0 {
			t.Errorf("flaky %q, want none: the checks ran on the merge's target", flakyNames(got))
		}
	})

	t.Run("two reds before the fix merge: no alert (T-373, T-380, T-382)", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		t1 := x.task("T-01", map[string]string{"shared.txt": "two\n"})
		t2 := x.task("T-02", map[string]string{"shared.txt": "three\n"})
		x.spec("T-05", "Git lock contention after a crash: "+test+" (flaky ×2)")
		x.mergedAt("T-05", at(-50))
		got := FlakyTests(x.cfg, []Flake{red(test, "T-01", t1, at(-300)), red(test, "T-02", t2, at(-200))}, now)
		if len(got) != 0 {
			t.Errorf("flaky %q, want none: the fix merged after both reds", flakyNames(got))
		}
	})

	t.Run("a red after the fix merge and another red: an alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		t3 := x.task("T-03", map[string]string{"shared.txt": "two\n"})
		t4 := x.task("T-04", map[string]string{"shared.txt": "three\n"})
		t6 := x.task("T-06", map[string]string{"shared.txt": "four\n"})
		x.spec("T-05", "Fix: "+test+" is flaky")
		x.mergedAt("T-05", at(-100))
		got := FlakyTests(x.cfg, []Flake{red(test, "T-03", t3, at(-200)), red(test, "T-04", t4, at(-50)),
			red(test, "T-06", t6, at(0))}, now)
		if flakyNames(got) != test {
			t.Fatalf("flaky %q, want %q", flakyNames(got), test)
		}
		if got[0].N != 2 || strings.Join(got[0].Tasks, ",") != "T-04,T-06" {
			t.Errorf("×%d tasks %q, want ×2 tasks T-04,T-06: only the reds after the fix count", got[0].N, got[0].Tasks)
		}
	})

	t.Run("two unrelated tasks red, the test file untouched: an alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		t1 := x.task("T-01", map[string]string{"shared.txt": "two\n"})
		t2 := x.task("T-02", map[string]string{"shared.txt": "three\n"})
		got := FlakyTests(x.cfg, []Flake{red(test, "T-01", t1, at(-200)), red(test, "T-02", t2, at(-100))}, now)
		if flakyNames(got) != test {
			t.Fatalf("flaky %q, want %q", flakyNames(got), test)
		}
		if got[0].N != 2 || strings.Join(got[0].Tasks, ",") != "T-01,T-02" {
			t.Errorf("×%d tasks %q, want ×2 tasks T-01,T-02", got[0].N, got[0].Tasks)
		}
	})

	t.Run("a package name red: it counts (no test line)", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		const pkg = "example.com/build"
		t1 := x.task("T-01", map[string]string{"shared.txt": "two\n"})
		t2 := x.task("T-02", map[string]string{"shared.txt": "three\n"})
		got := FlakyTests(x.cfg, []Flake{red(pkg, "T-01", t1, at(-200)), red(pkg, "T-02", t2, at(-100))}, now)
		if flakyNames(got) != pkg {
			t.Fatalf("flaky %q, want %q", flakyNames(got), pkg)
		}
	})

	t.Run("a title names the test only at identifier boundaries", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			title, test string
			want        bool
		}{
			{"crash: TestLedger (flaky ×2)", "TestLedger", true},
			{"TestLedger", "TestLedger", true},
			{"crash: TestLedger2 (flaky ×2)", "TestLedger", false},
			{"TestLedgerX", "TestLedger", false},
			{"Test", "TestLedger", false},
		} {
			if got := mentions(c.title, c.test); got != c.want {
				t.Errorf("%q names %q = %v, want %v", c.title, c.test, got, c.want)
			}
		}
	})
}

// A check run removes the logs older than 14 days from .obor/state/logs and keeps a younger one and its own.
func TestRunPrunesOldLogs(t *testing.T) {
	t.Parallel()
	x := newFx(t, cmd("check", "echo 2 pass"))
	dir := x.Path(".obor/state/logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	age := func(name string, days int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old, young := age("T-01-check.log", 15), age("T-02-check.log", 1)
	for _, p := range []string{old, young} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("precondition: %v", err)
		}
	}
	x.is(x.gate("L-01", "T-05", "logs"), OK, `^GREEN T-05 `)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the 15-day-old log stays: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("the 1-day-old log is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "T-05-check.log")); err != nil {
		t.Errorf("the run's own log is gone: %v", err)
	}
}
