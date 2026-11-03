package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/run"
	"obor/internal/testkit"
)

// worker is the lane's `obor run <name>`: the receipt a green heavy run leaves.
func (x *fx) worker(name string) int {
	x.t.Helper()
	var out strings.Builder
	o := x.opts()
	return run.Run(run.Options{Cfg: o.Cfg, Env: o.Env, Environ: o.Environ, Dir: x.lane, Name: name, Stdout: &out, Stderr: &out,
		PID: os.Getpid(), Interrupted: make(chan int), Slots: o.Slots})
}

// ran is how often the check commands ran (one line each in $TMP_DIR/ran).
func (x *fx) ran() int {
	b, _ := os.ReadFile(filepath.Join(x.Base, "ran"))
	return strings.Count(string(b), "\n")
}

func receiptFx(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	count := `echo 4 pass; echo x >> "$TMP_DIR/ran"`
	x := newFx(t, append([]func(map[string]any){
		cmd("check", map[string]any{"cmd": count, "heavy": true, "timeout": 20}),
		cmd("other", map[string]any{"cmd": count, "heavy": true, "timeout": 20}),
		testkit.Set("merge.receipt_max_age", 3600)}, edits...)...)
	x.Git(x.lane, "merge", "-q", "--ff-only", "main") // a lane is based on the current target, as right after a submit
	return x
}

// T-349: the gate reuses a worker's green run on the identical tree, and runs the check in every other case.
func TestReceiptReuse(t *testing.T) {
	t.Parallel()
	reused := `^GREEN T-05 \(reused L-01 [0-9a-f]{7}, full:no-guard\)$`
	ranAgain := `^GREEN T-05 \(4 pass, full:no-guard\)$`
	for _, tc := range []struct {
		name string
		edit func(x *fx) // between the worker's run and the merge
		want string
		runs int // check runs in total: the worker's, and the gate's when it ran
	}{
		{"same tree, check and command: reused", func(x *fx) {}, reused, 1},
		{"the target moved: the merge tree is not the lane's", func(x *fx) {
			x.Write("other.txt", "moved\n")
			x.Git(x.Root, "add", "other.txt")
			x.Git(x.Root, "commit", "-q", "-m", "target moves")
		}, ranAgain, 2},
		{"an uncommitted change after the run", func(x *fx) { x.change("later.txt", "later") }, ranAgain, 2},
		{"the command text changed in the config", func(x *fx) {
			x.Write(".obor/local.json", `{"commands": {"check": {"cmd": "echo 4 pass; echo y >> \"$TMP_DIR/ran\"; true", "heavy": true}}}`)
		}, ranAgain, 2},
		{"a receipt older than the max age", func(x *fx) {
			es, _ := os.ReadDir(x.Path(".obor/state/receipts"))
			path := x.Path(".obor/state/receipts/" + es[0].Name())
			f := strings.Fields(x.Read(path))
			f[3] = "1"
			x.Write(path, strings.Join(f, " ")+"\n")
		}, ranAgain, 2},
		{"the max age 0 reuses nothing", func(x *fx) { x.Write(".obor/local.json", `{"merge": {"receipt_max_age": 0}}`) }, ranAgain, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := receiptFx(t)
			if rc := x.worker("check"); rc != 0 {
				t.Fatalf("the worker's run: %d", rc)
			}
			es, _ := os.ReadDir(x.Path(".obor/state/receipts"))
			if x.ran() != 1 || len(es) != 1 {
				t.Fatalf("precondition: the worker's green run ran the check once and left a receipt: %d runs, %d receipts", x.ran(), len(es))
			}
			tc.edit(x)
			x.is(x.gate("L-01", "T-05", "reuse"), OK, tc.want)
			x.eq(strings.Repeat("r", x.ran()), strings.Repeat("r", tc.runs), "check runs")
			if strings.Contains(tc.want, "reused") {
				if x.cand.Full {
					t.Error("a reused merge stays partial: the candidate is full")
				}
			}
		})
	}

	t.Run("a receipt for another check name", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t)
		if rc := x.worker("other"); rc != 0 {
			t.Fatalf("the worker's run: %d", rc)
		}
		x.is(x.gate("L-01", "T-05", "reuse"), OK, ranAgain)
		x.eq(strings.Repeat("r", x.ran()), "rr", "check runs")
	})

	t.Run("a red worker run leaves no receipt", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t, cmd("check", map[string]any{"cmd": `echo 1 fail; echo x >> "$TMP_DIR/ran"; exit 1`, "heavy": true}))
		if rc := x.worker("check"); rc != 1 {
			t.Fatalf("the worker's run: %d, want 1", rc)
		}
		x.eq(strings.Repeat("r", x.ran()), "r", "precondition: the worker ran it")
		x.gone(".obor/state/receipts", "no receipt")
		x.is(x.gate("L-01", "T-05", "red"), Red, `^RED check `)
		x.eq(strings.Repeat("r", x.ran()), "rr", "the gate ran it")
	})
}

// The guard always runs, and the class's other checks are still reused one by one.
func TestReceiptGuardAlwaysRuns(t *testing.T) {
	t.Parallel()
	x := receiptFx(t,
		testkit.Set("merge", map[string]any{"trailer": trailer, "summary_regex": "[0-9]+ pass", "guard": "guard", "receipt_max_age": 3600,
			"classes": []any{map[string]any{"name": "any", "paths": []any{"**"}, "checks": []any{"check"}}}}),
		cmd("guard", map[string]any{"cmd": `echo 1 pass; echo g >> "$TMP_DIR/guard.ran"`, "heavy": true}))
	if rc := x.worker("check"); rc != 0 {
		t.Fatalf("the worker's run: %d", rc)
	}
	x.is(x.gate("L-01", "T-05", "partial"), OK, `^GREEN T-05 \(guard 1 pass, check reused L-01 [0-9a-f]{7}, partial:any\)$`)
	x.eq(strings.Repeat("r", x.ran()), "r", "check not run again")
	x.eq(x.Read(filepath.Join(x.Base, "guard.ran")), "g\n", "the guard ran")
}

// obor merge exports CHECK_CPUS to its commands.
func TestGateExportsCheckCPUs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ slots, want string }{{`{"cpus": 3, "max": 2}`, "3"}, {`{"max": 1000}`, "1"}} {
		x := newFx(t, testkit.Set("slots", tc.slots), cmd("check", `echo "$CHECK_CPUS" > "$TMP_DIR/cpus"`))
		x.is(x.gate("L-01", "T-05", "cpus"), OK, `^GREEN T-05 `)
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "cpus"))), tc.want, "CHECK_CPUS for slots "+tc.slots)
	}
}

// mutate is a command that changes the tree being merged: it stages a generated file, like a formatter or codegen.
const mutate = `echo "$MUT" > gen.txt; git add gen.txt; echo m >> "$TMP_DIR/mut.ran"; echo 1 pass`

// RV-40 #1: a receipt is reused only when the tree at that moment, and at the end, is the tree it proved.
func TestReceiptTreeChangedByAnEarlierStep(t *testing.T) {
	t.Parallel()
	classes := func(checks ...any) func(map[string]any) {
		return testkit.Set("merge", map[string]any{"trailer": trailer, "summary_regex": "[0-9]+ pass", "guard": "guard", "receipt_max_age": 3600,
			"classes": []any{map[string]any{"name": "any", "paths": []any{"**"}, "checks": checks}}})
	}
	mutator := func(name string) func(map[string]any) {
		return cmd(name, map[string]any{"cmd": strings.ReplaceAll(mutate, "$MUT", "generated"), "heavy": true})
	}
	// verify is the lane's merge: the check ran again on the merged tree, which holds the generated file.
	verify := func(t *testing.T, x *fx, wantRuns int) {
		t.Helper()
		x.eq(strings.Repeat("r", x.ran()), strings.Repeat("r", wantRuns), "check runs")
		if x.Read(filepath.Join(x.Base, "mut.ran")) == "" {
			t.Fatal("precondition: the mutating step ran")
		}
		x.eq(x.Git(x.Root, "show", x.cand.SHA+":gen.txt"), "generated", "the merged tree holds the generated file")
	}

	t.Run("the guard stages a change: the check runs", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t, classes("check"), mutator("guard"))
		if rc := x.worker("check"); rc != 0 {
			t.Fatalf("the worker's run: %d", rc)
		}
		x.is(x.gate("L-01", "T-05", "guard"), OK, `^GREEN T-05 \(guard 1 pass, check 4 pass, partial:any\)$`)
		verify(t, x, 2)
	})

	t.Run("an earlier class check stages a change: the later check is not reused", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t, classes("other", "check"), cmd("guard", map[string]any{"cmd": "echo 1 pass", "heavy": true}),
			cmd("other", map[string]any{"cmd": strings.ReplaceAll(mutate, "$MUT", "generated") + `; echo x >> "$TMP_DIR/ran"`, "heavy": true}))
		if rc := x.worker("check"); rc != 0 {
			t.Fatalf("the worker's run: %d", rc)
		}
		x.is(x.gate("L-01", "T-05", "earlier"), OK, `^GREEN T-05 \(guard 1 pass, other 1 pass, check 4 pass, partial:any\)$`)
		verify(t, x, 3) // the worker's, other's, and check's own
	})

	t.Run("a later class check stages a change after a reuse: nothing stays reused", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t, classes("check", "mutator"), cmd("guard", map[string]any{"cmd": "echo 1 pass", "heavy": true}), mutator("mutator"))
		if rc := x.worker("check"); rc != 0 {
			t.Fatalf("the worker's run: %d", rc)
		}
		x.is(x.gate("L-01", "T-05", "later"), OK, `^GREEN T-05 \(guard 1 pass, check 4 pass, mutator 1 pass, partial:any\)$`)
		verify(t, x, 2)
	})

	t.Run("the baseline stages a change after the check was reused (full class)", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t, cmd("baseline", map[string]any{"at": "merge", "dir": ".", "timeout": 10,
			"cmd": strings.ReplaceAll(mutate, "$MUT", "generated")}))
		if rc := x.worker("check"); rc != 0 {
			t.Fatalf("the worker's run: %d", rc)
		}
		x.is(x.gate("L-01", "T-05", "baseline"), OK, `^GREEN T-05 \(4 pass, full:no-guard\)$`)
		verify(t, x, 2)
	})

	t.Run("nothing changes the tree: the receipt is still reused", func(t *testing.T) {
		t.Parallel()
		x := receiptFx(t, classes("check"), cmd("guard", map[string]any{"cmd": "echo 1 pass", "heavy": true}))
		if rc := x.worker("check"); rc != 0 {
			t.Fatalf("the worker's run: %d", rc)
		}
		x.is(x.gate("L-01", "T-05", "same"), OK, `^GREEN T-05 \(guard 1 pass, check reused L-01 [0-9a-f]{7}, partial:any\)$`)
		x.eq(strings.Repeat("r", x.ran()), "r", "check runs")
	})
}
