//go:build integration

package checks

import (
	"encoding/json"
	"fmt"
	"os"
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
