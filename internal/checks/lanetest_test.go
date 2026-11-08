package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// T-961: the merge slot merges and compiles (Gate); the class's checks run in the task's lane (Test).

// probe is a check that records where and how it ran: its dir, the HEAD it saw and its environment, under $TMP_DIR.
const probe = `pwd -P > "$TMP_DIR/check-pwd"; git rev-parse HEAD > "$TMP_DIR/check-head"; env > "$TMP_DIR/check-env"; ` +
	`cat "$OBOR_CHANGED_FILES" > "$TMP_DIR/check-files"; echo 1 pass`

// probed is the probe's record name, "" when the probe did not run.
func (y *sx) probed(name string) string {
	b, err := os.ReadFile(filepath.Join(y.Base, "check-"+name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// test runs the lane test of c in L-01 (its dispatch's branch task/T-05) under ctx.
func (y *sx) test(c context.Context, cand port.Candidate) (port.TestOutcome, error) {
	return y.s.Test(c, port.TestInput{Task: "T-05", Lane: "L-01", Branch: "task/T-05", SHA: cand.SHA, Target: cand.Target})
}

// The gate runs commands.compile and never the class's checks: green names compile's counts; a compile that fails is
// red; a lane_setup that fails before it is a fail, its rc named (T-996: internal/app's scripted "FAIL lane_setup"
// gate holds on this); with no compile configured nothing runs (lane_setup neither) and the merge is green.
func TestGateCompilesOnly(t *testing.T) {
	testkit.Slow(t) // 1.13 s: its cases through the git-cut shim (T-1004: the 2-core budget)
	t.Parallel()
	for _, c := range []struct {
		name, compile, want string
		kind                string
		setupRC             int // lane_setup's exit code
	}{
		{"compile green", "echo 1 pass", `^GREEN T-05 \(compile 1 pass\)$`, port.OutcomeGreen, 0},
		{"compile red", "echo '0 pass, 1 fail'; exit 1", `^RED compile \(0 pass, 1 fail\)`, port.OutcomeRed, 0},
		{"lane_setup fails", "echo 1 pass", `^FAIL lane_setup \(rc 3\)$`, port.OutcomeFail, 3},
		{"no compile", "", `^GREEN T-05 \(merged, no commands.compile\)$`, port.OutcomeGreen, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			edits := []func(map[string]any){cmd("check", probe),
				cmd("lane_setup", fmt.Sprintf(`touch "$TMP_DIR/setup-ran"; exit %d`, c.setupRC))}
			if c.compile != "" {
				edits = append(edits, cmd("compile", c.compile))
			}
			y := newSx(t, edits...)
			o := y.gate(lane(y.submit()))
			if o.Kind != c.kind {
				t.Fatalf("gate: %s %q; want %s", o.Kind, o.Line, c.kind)
			}
			y.match(o.Line, c.want, "the gate's line")
			y.eq(y.probed("pwd"), "", "the class's check never runs in the gate")
			_, err := os.Stat(filepath.Join(y.Base, "setup-ran"))
			if got := err == nil; got != (c.compile != "") {
				t.Errorf("lane_setup ran %v; want %v (only before a compile)", got, c.compile != "")
			}
		})
	}
}

// The candidate's patch is the task's own diff Base..S (Base: merge-base(S, target)): a move in the patch's context
// gives a new merge onto the new target with the same Base, Patch, Stat and Diff, so the accept stands (row 20).
func TestGateCandidateIsTheTasksOwnDiff(t *testing.T) {
	t.Parallel()
	y := newSx(t)
	y.Write(filepath.Join(y.lane, "shared.txt"), "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	sha := y.submit()
	fork := y.Git(y.Root, "merge-base", sha, "main") // the lane forked before main's last fixture commit
	if fork == y.head(y.Root) {
		t.Fatal("precondition: main is past the lane's fork point (Base differs from Target)")
	}
	first := y.green(lane(sha))
	y.eq(first.Base, fork, "Base is the lane's fork point")
	y.eq(first.Diff, y.Git(y.Root, "diff", fork, sha), "Diff is git diff Base S")
	y.Write("shared.txt", "1\n2\nmain\n4\n5\n6\n7\n8\n9\n10\n") // within the patch's context
	y.Git(y.Root, "commit", "-qam", "main edit near the lane's")
	again := y.green(moved(sha))
	if again.Target == first.Target || again.SHA == first.SHA {
		t.Fatalf("precondition: the moved gate merged onto the new target: %+v", again)
	}
	y.eq(again.Base, first.Base, "the move keeps Base")
	y.eq(again.Patch, first.Patch, "the move keeps the patch the accept binds to")
	y.eq(again.Stat, first.Stat, "and the stat")
	y.eq(again.Diff, first.Diff, "and the diff the review job carries")
	if strings.Contains(again.Diff, "main") {
		t.Errorf("the task's own diff carries the target's edit:\n%s", again.Diff)
	}
}

// The lane test's green paths: the class's check runs in the lane at the candidate with the lane worker's environment
// and the task's files, and the lane is back on its branch; the CPU budget is the lane's; a lane test while
// branches.promote is set runs the short tier and asks for the changed packages only; what the checks changed is discarded.
func TestLaneTestGreenRestores(t *testing.T) {
	testkit.Slow(t) // 2.42 s: four lane-test paths through the git-cut shim (T-1004: the 2-core budget)
	for _, c := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"the class's check runs in the lane and the lane is restored", laneTestInTheLane},
		{"the lane's CPU budget", laneTestLaneEnv},
		{"CHECK_SHORT and CHECK_DIRECT follow branches.promote", laneTestPromotionEnv},
		{"what the checks changed is discarded", laneTestDiscards},
	} {
		t.Run(c.name, c.run)
	}
}

// The lane test's red paths: a red check is red with its failing test named and the lane is back on its branch; a cut
// test is an error and restores the branch all the same, and a lane a crash left detached is tested again; a red test
// reruns its failing tests once (commands.retest).
func TestLaneTestRedRestores(t *testing.T) {
	testkit.Slow(t) // 3.83 s: three lane-test paths through the git-cut shim (T-1004: the 2-core budget)
	for _, c := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"a red check", laneTestRed},
		{"a cut test", laneTestCut},
		{"a red test reruns once", laneTestRetests},
	} {
		t.Run(c.name, c.run)
	}
}

// The lane test checks the candidate out in the lane, runs the class's check there on a pool slot (worker priority,
// not the merge marker) with the lane's CPU budget and the task's files, and checks the lane's branch out again: green
// with the checks and the tree they ran on.
func laneTestInTheLane(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", probe))
	sha := y.submit()
	c := y.green(lane(sha))
	y.eq(y.probed("pwd"), "", "precondition: the gate ran no check")
	o, err := y.test(ctx, c)
	if err != nil || o.Kind != port.OutcomeGreen {
		t.Fatalf("test: %+v %v\noutput:\n%s", o, err, y.out.String())
	}
	y.match(o.Line, `^GREEN T-05 \(1 pass, full:`, "the test's line")
	y.eq(o.Checks, "1 pass", "its checks")
	lane, _ := filepath.EvalSymlinks(y.lane)
	y.eq(y.probed("pwd"), lane, "the check ran in the lane")
	y.eq(y.probed("head"), c.SHA, "on the candidate")
	y.eq(y.probed("files"), "feature.txt", "OBOR_CHANGED_FILES: the task's files (target..candidate)")
	env := y.probed("env")
	for _, kv := range []string{"CHECK_PRIORITY=worker", "GOMAXPROCS=2", "GOFLAGS=-p=2", "CHECK_CPUS=2"} {
		if !strings.Contains("\n"+env+"\n", "\n"+kv+"\n") {
			t.Errorf("the check's environment has no %s", kv)
		}
	}
	for _, l := range strings.Split(env, "\n") {
		if held, ok := strings.CutPrefix(l, "CHECK_SLOT_HELD="); ok && !strings.HasPrefix(filepath.Base(held), "slot-") {
			t.Errorf("the check held %s, want a pool slot (slot-<i>), never the merge marker", held)
		}
	}
	y.eq(y.Git(y.lane, "branch", "--show-current"), "task/T-05", "the lane is back on its branch")
	y.eq(y.head(y.lane), sha, "at the submitted sha")
	entries, _ := filepath.Glob(filepath.Join(y.slots, "slot-*"))
	if len(entries) != 0 {
		t.Errorf("slots left held: %v", entries)
	}
}

// T-967: the lane test's env is the lane workers' (config.LaneEnv): slots.lane_cpus and the inherited GOFLAGS' other
// flags kept, CHECK_CPUS the same budget.
func laneTestLaneEnv(t *testing.T) {
	t.Parallel()
	y := newSxEnv(t, []string{"GOFLAGS=-mod=mod"}, testkit.Set("slots.lane_cpus", 1), cmd("check", probe))
	if got := y.s.Cfg.LaneCPUs(); got != 1 {
		t.Fatalf("precondition: slots.lane_cpus 1, got %d", got)
	}
	if got := y.s.Env.Get("GOFLAGS"); got != "-mod=mod" {
		t.Fatalf("precondition: the inherited GOFLAGS is -mod=mod, got %q", got)
	}
	sha := y.submit()
	o, err := y.test(ctx, y.green(lane(sha)))
	if err != nil || o.Kind != port.OutcomeGreen {
		t.Fatalf("test: %+v %v\noutput:\n%s", o, err, y.out.String())
	}
	env := y.probed("env")
	for _, kv := range []string{"GOMAXPROCS=1", "GOFLAGS=-mod=mod -p=1", "CHECK_CPUS=1"} {
		if !strings.Contains("\n"+env+"\n", "\n"+kv+"\n") {
			t.Errorf("the check's environment has no %q:\n%s", kv, env)
		}
	}
}

// T-991/T-997: the lane test runs the short tier and only the changed packages' tests when branches.promote is set
// (CHECK_SHORT=1, CHECK_DIRECT=1: the promotion's full suite and the nightly cover the slow tests and the importers,
// the slot's compile an importer's build break); without a promotion it sets neither, so the full/mixed class runs the
// full check as before T-989.
func laneTestPromotionEnv(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		promote bool
	}{
		{"promotion on", true},
		{"promotion off", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			edits := []func(map[string]any){cmd("check", probe)}
			if c.promote {
				edits = append(edits, testkit.Set("branches.promote", "prod"))
			}
			y := newSx(t, edits...)
			if got := y.s.Cfg.Data.Branches.Promote != ""; got != c.promote {
				t.Fatalf("precondition: branches.promote set %v, want %v", got, c.promote)
			}
			sha := y.submit()
			o, err := y.test(ctx, y.green(lane(sha)))
			if err != nil || o.Kind != port.OutcomeGreen {
				t.Fatalf("test: %+v %v\noutput:\n%s", o, err, y.out.String())
			}
			env := y.probed("env")
			for _, kv := range []string{"CHECK_SHORT=1", "CHECK_DIRECT=1"} {
				got := strings.Contains("\n"+env+"\n", "\n"+kv+"\n")
				if got != c.promote {
					t.Errorf("the check's environment carries %s %v, want %v:\n%s", kv, got, c.promote, env)
				}
			}
		})
	}
}

// A lane with uncommitted changes is refused before anything runs (an error: the step runs again on a later pass):
// never checked out, the change kept, no check run.
func TestLaneTestRefusesDirtyLane(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", probe))
	sha := y.submit()
	c := y.green(lane(sha))
	y.Write(filepath.Join(y.lane, "wip.txt"), "still writing\n")
	o, err := y.test(ctx, c)
	if err == nil || !strings.Contains(err.Error(), "lane L-01 dirty") {
		t.Fatalf("test of a dirty lane: %+v %v; want the dirty error", o, err)
	}
	y.eq(y.probed("pwd"), "", "no check ran")
	y.eq(y.Git(y.lane, "branch", "--show-current"), "task/T-05", "the lane stays on its branch")
	y.eq(y.head(y.lane), sha, "never checked out")
	y.eq(y.Read(filepath.Join(y.lane, "wip.txt")), "still writing\n", "the worker's change kept")
}

// A red check is red, its failing test named; the lane is back on its branch.
func laneTestRed(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 'Expected 1, got 2'; echo '0 pass, 1 fail'; exit 1"))
	sha := y.submit()
	o, err := y.test(ctx, y.green(lane(sha)))
	if err != nil || o.Kind != port.OutcomeRed {
		t.Fatalf("test: %+v %v; want red", o, err)
	}
	y.match(o.Line, `^RED check \(0 pass, 1 fail, full:.*\) — failing: Expected 1, got 2; log `, "the red line")
	y.eq(y.Git(y.lane, "branch", "--show-current"), "task/T-05", "the lane is back on its branch")
}

// A test cut by its ctx (serve stops) is an error, not an outcome, and the lane goes back to its branch all the same;
// a lane left detached (a test cut before its restore) is tested again and restored to the dispatch's branch.
func laneTestCut(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", `touch "$TMP_DIR/check-began"; sleep 3`))
	sha := y.submit()
	c := y.green(lane(sha))
	cut, cancel := context.WithCancel(ctx)
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(y.Base, "check-began")); err == nil {
				break
			}
		}
		cancel()
	}()
	o, err := y.test(cut, c)
	if err == nil {
		t.Fatalf("a cut test: %+v; want an error", o)
	}
	y.eq(y.Git(y.lane, "branch", "--show-current"), "task/T-05", "the cut test restored the branch")

	y.Git(y.lane, "checkout", "-q", "--detach", c.SHA) // as a crash before the restore leaves it
	y.Write(filepath.Join(y.Root, ".obor", "config.json"), strings.Replace(y.Read(filepath.Join(y.Root, ".obor", "config.json")),
		`sleep 3`, `echo 1 pass`, 1))
	opts := y.opts()
	opts.Git = y.s.Git
	y.s = Scratch{opts}
	if o, err := y.test(ctx, c); err != nil || o.Kind != port.OutcomeGreen {
		t.Fatalf("the test again on the detached lane: %+v %v", o, err)
	}
	y.eq(y.Git(y.lane, "branch", "--show-current"), "task/T-05", "restored to the dispatch's branch")
}

// T-964: a red lane test reruns the tests and packages it named once (commands.retest, OBOR_FAILED_TESTS/OBOR_FAILED_PKGS)
// in the lane at the candidate. Green then is the step's green and the flake is logged retried and passed; still red is
// the original red; a build failure and a project without commands.retest rerun nothing.
func laneTestRetests(t *testing.T) {
	t.Parallel()
	const grep = `^--- FAIL|^FAIL\s`
	const redOne = `echo "--- FAIL: TestAlpha (0.01s)"; echo "FAIL example.com/pkg 0.03s"; exit 1`
	// retestCmd is commands.retest: it counts its runs and records where and how it ran under $TMP_DIR, then exits rc.
	retestCmd := func(rc string) string {
		return `n=$(cat "$TMP_DIR/retests" 2>/dev/null || echo 0); echo $((n + 1)) > "$TMP_DIR/retests"; ` +
			`pwd -P > "$TMP_DIR/retest-pwd"; git rev-parse HEAD > "$TMP_DIR/retest-head"; ` +
			`echo "$OBOR_FAILED_TESTS" > "$TMP_DIR/retest-tests"; echo "$OBOR_FAILED_PKGS" > "$TMP_DIR/retest-pkgs"; ` +
			`echo "1 pass"; exit ` + rc
	}
	t.Run("passes on the retest: green, the flake retried and passed", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne), cmd("retest", retestCmd("0")))
		c := y.green(lane(y.submit()))
		o, err := y.test(ctx, c)
		if err != nil || o.Kind != port.OutcomeGreen {
			t.Fatalf("test: %+v %v; want green on the retest\noutput:\n%s", o, err, y.out.String())
		}
		y.match(o.Line, `^GREEN T-05 \(1 pass, full:no-guard\)$`, "the step is green on the retest's counts")
		y.eq(y.Read(filepath.Join(y.Base, "retests")), "1\n", "one retest, never more")
		y.eq(strings.TrimSpace(y.Read(filepath.Join(y.Base, "retest-tests"))), "^TestAlpha$",
			"OBOR_FAILED_TESTS: the failing test's -run regexp")
		y.eq(strings.TrimSpace(y.Read(filepath.Join(y.Base, "retest-pkgs"))), "example.com/pkg", "OBOR_FAILED_PKGS: its package")
		laneDir, _ := filepath.EvalSymlinks(y.lane)
		y.eq(strings.TrimSpace(y.Read(filepath.Join(y.Base, "retest-pwd"))), laneDir, "the retest ran in the lane")
		y.eq(strings.TrimSpace(y.Read(filepath.Join(y.Base, "retest-head"))), c.SHA, "at the candidate")
		got := y.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		y.eq(got[0].Test, "TestAlpha", "the flake's test")
		y.eq(got[0].SHA, c.SHA, "the flake's sha")
		y.eqBool(got[0].Retried, true, "retried")
		y.eqBool(got[0].PassedOnRetry, true, "passed_on_retry")
	})

	t.Run("fails again: the original red, one retest", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne), cmd("retest", retestCmd("1")))
		o, err := y.test(ctx, y.green(lane(y.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red", o, err)
		}
		y.match(o.Line, `^RED check \(rc=1, full:no-guard\) — failing: --- FAIL: TestAlpha \(0\.01s\), FAIL example\.com/pkg 0\.03s; log `,
			"the red is the check's own line")
		y.eq(y.Read(filepath.Join(y.Base, "retests")), "1\n", "one retest, never more")
		got := y.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		y.eqBool(got[0].Retried, true, "retried")
		y.eqBool(got[0].PassedOnRetry, false, "passed_on_retry")
	})

	t.Run("a retest that ran no tests: red", func(t *testing.T) {
		t.Parallel()
		// T-976: the retry's summary says it tested nothing ("go test: 0 pass, 0 fail") yet exits 0: the red stands.
		y := newSx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne),
			cmd("retest", `echo "go test: 0 pass, 0 fail"; echo ran > "$TMP_DIR/retest-ran"; exit 0`))
		o, err := y.test(ctx, y.green(lane(y.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red (a retry that ran no tests is not green)\noutput:\n%s", o, err, y.out.String())
		}
		y.match(o.Line, `^RED check \(rc=1, full:no-guard\) — failing: --- FAIL: TestAlpha`, "the original red")
		y.eq(y.Read(filepath.Join(y.Base, "retest-ran")), "ran\n", "the retest ran")
		got := y.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		y.eqBool(got[0].Retried, true, "retried")
		y.eqBool(got[0].PassedOnRetry, false, "passed_on_retry: a retest that ran no tests proves nothing")
	})

	t.Run("a retest with no summary: rc decides", func(t *testing.T) {
		t.Parallel()
		// No summary line to read says nothing about the tests: the retest's rc 0 is green, as before T-976.
		y := newSx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne), cmd("retest", `exit 0`))
		o, err := y.test(ctx, y.green(lane(y.submit())))
		if err != nil || o.Kind != port.OutcomeGreen {
			t.Fatalf("test: %+v %v; want green (nothing to count: rc 0)\noutput:\n%s", o, err, y.out.String())
		}
		y.match(o.Line, `^GREEN T-05 \(rc=0, full:no-guard\)$`, "the retest's rc is the step's counts")
	})

	t.Run("a build failure: red, no retest", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, testkit.Set("merge.fail_grep", grep),
			cmd("check", `echo "FAIL make build"; echo "FAIL example.com/pkg [build failed]"; exit 1`),
			cmd("retest", retestCmd("0")))
		o, err := y.test(ctx, y.green(lane(y.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red", o, err)
		}
		if _, err := os.Stat(filepath.Join(y.Base, "retests")); err == nil {
			t.Error("a build failure was retested; want no retest")
		}
	})

	t.Run("no commands.retest: red as today", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne))
		o, err := y.test(ctx, y.green(lane(y.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red", o, err)
		}
		y.match(o.Line, `^RED check \(rc=1, full:no-guard\) — failing: --- FAIL: TestAlpha`, "the red line")
		got := y.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		y.eqBool(got[0].Retried || got[0].PassedOnRetry, false, "no retry")
	})
}

// A check that changes the lane (a tracked file, an untracked one) does not leave it so: the lane was clean when the
// test began, so the test discards those changes and the lane is on its branch, clean; an ignored file (a build
// output) stays.
func laneTestDiscards(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", `echo changed >> shared.txt; echo new > untracked.txt; mkdir -p .obor/state; `+
		`echo out > .obor/state/build.out; echo 1 pass`))
	sha := y.submit()
	o, err := y.test(ctx, y.green(lane(sha)))
	if err != nil || o.Kind != port.OutcomeGreen {
		t.Fatalf("test: %+v %v", o, err)
	}
	y.eq(y.Git(y.lane, "status", "--porcelain"), "", "the lane is clean")
	y.eq(y.Git(y.lane, "branch", "--show-current"), "task/T-05", "on its branch")
	y.eq(y.head(y.lane), sha, "at the submitted sha")
	y.eq(y.Read(filepath.Join(y.lane, ".obor/state/build.out")), "out\n", "the ignored build output stays")
}
