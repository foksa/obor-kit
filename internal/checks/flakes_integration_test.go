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

// the check runner appends one line per failing test to .obor/state/flakes.jsonl (a red run), and never on a
// green run; a log it cannot write is a warning, never a red.
func TestFlakeLog(t *testing.T) {
	t.Parallel()
	// Two test failures and the package summary line: the two test names, not the package.
	const twoFails = `echo "--- FAIL: TestAlpha (0.01s)"; echo "--- FAIL: TestBeta (0.02s)"; ` +
		`echo "FAIL example.com/pkg 0.03s"; exit 1`
	t.Run("red run: one line per failing test with the run's task and sha", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", twoFails), goLines())
		x.is(x.gate("L-01", "T-05", "red"), Red, `^RED check `)
		base := x.cand.SHA // the lane test runs on the candidate, the slot's merge commit
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
		x := newFx(t, cmd("check", twoFails), goLines())
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

// every gating command of the full tier logs a red run, and the result is unchanged; the race step's red is a WARN
// line and logs nothing.
func TestFlakeLogNightly(t *testing.T) {
	t.Parallel()
	failing := func(name string) any { return `echo "--- FAIL: Test_` + name + ` (0.01s)"; exit 1` }
	t.Run("NIGHTLY: check and check_milestone", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", failing("check")), cmd("check_milestone", failing("milestone")))
		x.is(x.nightly(), Red, `^NIGHTLY RED .*failed: check, check_milestone;`)
		got := x.flakes()
		if len(got) != 2 {
			t.Fatalf("flake lines %d, want 2: %+v", len(got), got)
		}
		for i, name := range []string{"check", "check_milestone"} {
			x.eq(got[i].Test, "Test_"+strings.TrimPrefix(name, "check_"), "test")
			x.eq(got[i].Check, name, "check")
			x.eq(got[i].Task, "nightly", "task")
			x.eqBool(got[i].Retried || got[i].PassedOnRetry, false, "no retry")
		}
	})

	t.Run("lane_setup timeout: logged, the rest not run", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("lane_setup", map[string]any{"cmd": `echo "--- FAIL: Test_setup (0.01s)"; sleep 5`, "timeout": 1}),
			cmd("check", "echo 1 pass"))
		x.is(x.nightly(), Red, `^NIGHTLY RED .*failed: lane_setup;`)
		x.eqList(x.logged(), []string{"Test_setup/lane_setup/false/false"}, "log")
	})

	t.Run("green nightly: no log", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", "echo 1 pass"))
		x.is(x.nightly(), OK, `^NIGHTLY GREEN `)
		x.gone(".obor/state/flakes.jsonl", "no flake log")
	})

	t.Run("a race step's red: its WARN line, no flake line", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", "echo 1 pass"),
			cmd("race", `echo "--- FAIL: TestRace (0.01s)"; exit 1`), nightly("race"))
		x.is(x.nightly(), OK, `WARN race \(rc 1, non-gating\): --- FAIL: TestRace`)
		x.gone(".obor/state/flakes.jsonl", "no flake log")
	})
}

// flakeFx is a project whose internal/x/x_test.go defines TestLedger — the test a later merge can touch — and whose
// store holds the specs and the merges the flakes rule reads.
type flakeFx struct {
	*testkit.Project
	t   *testing.T
	cfg *config.Config
}

func newFlakeFx(t *testing.T) *flakeFx {
	t.Helper()
	base := snapshot(t, builtBase("flakes", func(base string) string {
		p := testkit.NewAt(mainTB{}, base)
		p.Write("internal/x/x_test.go", "package x\n\nfunc TestLedger(t *testing.T) {}\n")
		p.Git(p.Root, "add", "-A")
		p.Git(p.Root, "commit", "-q", "-m", "fixture")
		return base
	}))
	p := testkit.Adopt(t, base)
	return &flakeFx{Project: p, t: t, cfg: &config.Config{Root: p.Root}}
}

// task commits files on branch task/<task> and merges it onto main ("Merge <task>: change" as obor does).
func (x *flakeFx) task(task string, files map[string]string) {
	x.t.Helper()
	branch := "task/" + task
	x.Git(x.Root, "checkout", "-q", "-b", branch)
	for path, text := range files {
		x.Write(path, text)
	}
	x.Git(x.Root, "add", "-A")
	x.Git(x.Root, "commit", "-q", "-m", task+": change")
	x.Git(x.Root, "checkout", "-q", "main")
	x.Git(x.Root, "merge", "-q", "--no-ff", "-m", "Merge "+task+": change", branch)
}

// spec stores task's spec with the given title (the store: the flakes rule's only source).
func (x *flakeFx) spec(task, title string) {
	x.Spec(task, "# "+task+" — "+title+"\n\nDifficulty: tiny.\n")
}

// mergedAt records task's merge at epoch at in the store, as obor serve does: fact merged.
func (x *flakeFx) mergedAt(task string, at int64) {
	x.MergedAt(task, x.Git(x.Root, "rev-parse", "main"), false, at)
}

// red is one red log line of test on task at epoch at, at sha.
func red(test, task, sha string, at int64) Flake {
	return Flake{Time: at, Test: test, Check: "check", Task: task, SHA: sha}
}

// saved is one passed-on-retry log line of test on task at epoch at, at sha: the test failed and then passed on the
// retry.
func saved(test, task, sha string, at int64) Flake {
	return Flake{Time: at, Test: test, Check: "check", Task: task, SHA: sha, Retried: true, PassedOnRetry: true}
}

// flakyNames is the flaky tests' names, in order.
func flakyNames(got []Flaky) string {
	var out []string
	for _, f := range got {
		out = append(out, f.Test)
	}
	return strings.Join(out, " ")
}

// a passed-on-retry line counts as a flake unless a merge after it has a spec title naming the test. A merge that
// changed the test's file is no fix on its own: no git history is read. Reds alone are no flake evidence.
func TestFlakyRule(t *testing.T) {
	t.Parallel()
	const test = "TestLedger"
	now := time.Now()
	at := func(offset int64) int64 { return now.Unix() + offset }
	const body = "package x\n\nfunc TestLedger(t *testing.T) { %s }\n"

	t.Run("a merge after the line whose spec title names the test: no alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		tested := x.Git(x.Root, "rev-parse", "main")
		x.spec("T-05", "Git lock contention after a crash: "+test+" (flaky ×2)")
		x.mergedAt("T-05", at(-50))
		got := FlakyTests(x.cfg, []Flake{saved(test, "T-01", tested, at(-300))}, now)
		if len(got) != 0 {
			t.Errorf("flaky %q, want none: the fix's spec title names the test", flakyNames(got))
		}
	})

	t.Run("a later merge that changed the test's file, its title not naming it: an alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		tested := x.Git(x.Root, "rev-parse", "main") // the line ran on this: the merge below is not in its history
		x.spec("T-05", "x calls the ledger twice")
		x.task("T-05", map[string]string{"internal/x/x_test.go": fmt.Sprintf(body, "t.Log(1)")})
		x.mergedAt("T-05", at(-50))
		got := FlakyTests(x.cfg, []Flake{saved(test, "T-01", tested, at(-200))}, now)
		if flakyNames(got) != test {
			t.Fatalf("flaky %q, want %q: only a title naming the test ends a flake", flakyNames(got), test)
		}
		if got[0].N != 1 || strings.Join(got[0].Tasks, ",") != "T-01" {
			t.Errorf("×%d tasks %q, want ×1 tasks T-01", got[0].N, got[0].Tasks)
		}
	})

	t.Run("a passed-on-retry line with no fix after it: an alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		got := FlakyTests(x.cfg, []Flake{saved(test, "T-01", "", at(-100))}, now)
		if flakyNames(got) != test {
			t.Fatalf("flaky %q, want %q", flakyNames(got), test)
		}
	})

	t.Run("reds on two tasks, no retry pass: no alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		got := FlakyTests(x.cfg, []Flake{red(test, "T-01", "", at(-200)), red(test, "T-02", "", at(-100))}, now)
		if len(got) != 0 {
			t.Errorf("flaky %q, want none: two reds with no retry pass are no flake", flakyNames(got))
		}
	})

	t.Run("a package name passed on retry: an alert", func(t *testing.T) {
		t.Parallel()
		x := newFlakeFx(t)
		const pkg = "example.com/build"
		got := FlakyTests(x.cfg, []Flake{saved(pkg, "T-01", "", at(-100))}, now)
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
