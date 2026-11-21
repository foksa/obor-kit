package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// The lane test's decisions over git's answers, on fakeGit: the outcome a check's result gives, what the lane's env
// holds, what a dirty lane does, when a red reruns. What real git does in the lane (the check's cwd and the files it
// is given, discards, a cut by ctx) is TestLaneTest*'s in lanetest_integration_test.go.

// laneTest runs the lane test of c in L-01 (its dispatch's branch task/T-05), the fake's call log cleared first.
func (k *fk) laneTest(c port.Candidate) (port.TestOutcome, error) {
	k.t.Helper()
	k.g.calls = nil
	return k.s.Test(ctx, port.TestInput{Task: "T-05", Lane: "L-01", Branch: "task/T-05", SHA: c.SHA, Target: c.Target})
}

// restored asserts the lane test's writes: the lane detached at sha, then discarded and back on its branch.
func (k *fk) restored(sha string) {
	k.t.Helper()
	k.eq(strings.Join(k.g.writeCalls(), "; "), fmt.Sprintf("Detach %[1]s %[2]s; Reset %[1]s; Clean %[1]s; Switch %[1]s task/T-05",
		k.lane, sha), "the lane is detached at the candidate, then restored to its branch")
	k.eq(k.g.branch[k.lane], "task/T-05", "the lane is back on its branch")
}

// ran is whether the check or retest that wrote name under $TMP_DIR ran.
func (k *fk) ran(name string) bool {
	_, err := os.Stat(filepath.Join(k.Base, name))
	return err == nil
}

// the lane test's env is the lane workers' (config.LaneEnv): the fact OBOR_LANE_CPUS (slots.lane_cpus) and the
// project's lanes.env with {cpus} replaced — a var the inherited env also had takes the config's value, no merge;
// CHECK_CPUS the same budget.
func TestLaneTestLaneEnv(t *testing.T) {
	t.Parallel()
	k := newFk(t, testkit.Set("lanes.cpus", 1),
		testkit.Set("lanes.env", `{"GOMAXPROCS": "{cpus}", "GOFLAGS": "-p={cpus} -trimpath"}`),
		cmd("check", `env > "$TMP_DIR/check-env"; echo 1 pass`))
	k.env = []string{"GOFLAGS=-mod=mod"}
	k.s.Options = k.opts()
	if got := k.s.Cfg.LaneCPUs(); got != 1 {
		t.Fatalf("precondition: slots.lane_cpus 1, got %d", got)
	}
	if got := k.s.Env.Get("GOFLAGS"); got != "-mod=mod" {
		t.Fatalf("precondition: the inherited GOFLAGS is -mod=mod, got %q", got)
	}
	c := k.green(lane(k.submit()))
	o, err := k.laneTest(c)
	if err != nil || o.Kind != port.OutcomeGreen {
		t.Fatalf("test: %+v %v\noutput:\n%s", o, err, k.out.String())
	}
	env := k.Read(filepath.Join(k.Base, "check-env"))
	for _, kv := range []string{"OBOR_LANE_CPUS=1", "GOMAXPROCS=1", "GOFLAGS=-p=1 -trimpath", "CHECK_CPUS=1"} {
		if !strings.Contains("\n"+env+"\n", "\n"+kv+"\n") {
			t.Errorf("the check's environment has no %q:\n%s", kv, env)
		}
	}
	if strings.Contains(env, "GOFLAGS=-mod=mod") {
		t.Errorf("the inherited GOFLAGS stayed beside the config's:\n%s", env)
	}
}

// A lane with uncommitted changes is refused before anything runs (an error: the step runs again on a later pass):
// never checked out, the change kept, no check run.
func TestLaneTestRefusesDirtyLane(t *testing.T) {
	t.Parallel()
	k := newFk(t, cmd("check", `touch "$TMP_DIR/check-ran"; echo 1 pass`))
	c := k.green(lane(k.submit()))
	k.g.status[k.lane] = []string{"?? wip.txt"}
	o, err := k.laneTest(c)
	if err == nil || !strings.Contains(err.Error(), "lane L-01 dirty") {
		t.Fatalf("test of a dirty lane: %+v %v; want the dirty error", o, err)
	}
	if k.ran("check-ran") {
		t.Error("a check ran in a dirty lane")
	}
	k.eq(strings.Join(k.g.writeCalls(), "; "), "", "the lane is never checked out, reset or cleaned")
	k.eq(k.g.branch[k.lane], "task/T-05", "the lane stays on its branch")
	k.eq(strings.Join(k.g.status[k.lane], "; "), "?? wip.txt", "the worker's change kept")
}

// A red check is red, its failing test named; the lane is back on its branch.
func TestLaneTestRed(t *testing.T) {
	t.Parallel()
	k := newFk(t, cmd("check", "echo 'Expected 1, got 2'; echo '0 pass, 1 fail'; exit 1"))
	c := k.green(lane(k.submit()))
	o, err := k.laneTest(c)
	if err != nil || o.Kind != port.OutcomeRed {
		t.Fatalf("test: %+v %v; want red", o, err)
	}
	k.match(o.Line, `^RED check \(0 pass, 1 fail, full:.*\) — failing: Expected 1, got 2; log `, "the red line")
	k.restored(c.SHA)
}

// a red lane test reruns the tests and packages it named once (commands.retest, its {tests} joined by
// merge.retest_join and OBOR_FAILED_PKGS) in the lane at the candidate. Green then is the step's green and the flake
// is logged retried and passed; still red is the original red; a red that named no test (a build failure) and a
// project without commands.retest rerun nothing.
func TestLaneTestRetests(t *testing.T) {
	t.Parallel()
	const redOne = `echo "--- FAIL: TestAlpha (0.01s)"; echo "FAIL example.com/pkg 0.03s"; exit 1`
	// retestCmd is commands.retest: it counts its runs and records where it ran and what it was given under
	// $TMP_DIR, then exits rc.
	retestCmd := func(rc string) string {
		return `n=$(cat "$TMP_DIR/retests" 2>/dev/null || echo 0); echo $((n + 1)) > "$TMP_DIR/retests"; ` +
			`pwd -P > "$TMP_DIR/retest-pwd"; ` +
			`echo "^({tests})$" > "$TMP_DIR/retest-tests"; echo "$OBOR_FAILED_PKGS" > "$TMP_DIR/retest-pkgs"; ` +
			`echo "1 pass"; exit ` + rc
	}
	t.Run("passes on the retest: green, the flake retried and passed", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, goLines(), cmd("check", redOne), cmd("retest", retestCmd("0")))
		c := k.green(lane(k.submit()))
		o, err := k.laneTest(c)
		if err != nil || o.Kind != port.OutcomeGreen {
			t.Fatalf("test: %+v %v; want green on the retest\noutput:\n%s", o, err, k.out.String())
		}
		k.match(o.Line, `^GREEN T-05 \(1 pass, full:no-guard\)$`, "the step is green on the retest's counts")
		k.eq(k.Read(filepath.Join(k.Base, "retests")), "1\n", "one retest, never more")
		k.eq(strings.TrimSpace(k.Read(filepath.Join(k.Base, "retest-tests"))), "^(TestAlpha)$",
			"{tests}: the failing test, in commands.retest's template")
		k.eq(strings.TrimSpace(k.Read(filepath.Join(k.Base, "retest-pkgs"))), "example.com/pkg", "OBOR_FAILED_PKGS: its package")
		laneDir, _ := filepath.EvalSymlinks(k.lane)
		k.eq(strings.TrimSpace(k.Read(filepath.Join(k.Base, "retest-pwd"))), laneDir, "the retest ran in the lane")
		k.restored(c.SHA) // detached at the candidate before the checks, the retest's tree
		got := k.flakeLog()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		k.eq(got[0].Test, "TestAlpha", "the flake's test")
		k.eq(got[0].SHA, c.SHA, "the flake's sha")
		k.eq(fmt.Sprint(got[0].Retried, got[0].PassedOnRetry), "true true", "retried, passed_on_retry")
	})

	t.Run("fails again: the original red, one retest", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, goLines(), cmd("check", redOne), cmd("retest", retestCmd("1")))
		o, err := k.laneTest(k.green(lane(k.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red", o, err)
		}
		k.match(o.Line, `^RED check \(rc=1, full:no-guard\) — failing: --- FAIL: TestAlpha \(0\.01s\), FAIL example\.com/pkg 0\.03s; log `,
			"the red is the check's own line")
		k.eq(k.Read(filepath.Join(k.Base, "retests")), "1\n", "one retest, never more")
		got := k.flakeLog()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		k.eq(fmt.Sprint(got[0].Retried, got[0].PassedOnRetry), "true false", "retried, not passed_on_retry")
	})

	t.Run("a retest that ran no tests: red", func(t *testing.T) {
		t.Parallel()
		// the retry's summary says it tested nothing ("go test: 0 pass, 0 fail") yet exits 0: the red stands.
		k := newFk(t, goLines(), cmd("check", redOne),
			cmd("retest", `echo "go test: 0 pass, 0 fail"; echo ran > "$TMP_DIR/retest-ran"; exit 0`))
		o, err := k.laneTest(k.green(lane(k.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red (a retry that ran no tests is not green)\noutput:\n%s", o, err, k.out.String())
		}
		k.match(o.Line, `^RED check \(rc=1, full:no-guard\) — failing: --- FAIL: TestAlpha`, "the original red")
		k.eq(k.Read(filepath.Join(k.Base, "retest-ran")), "ran\n", "the retest ran")
		got := k.flakeLog()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		k.eq(fmt.Sprint(got[0].Retried, got[0].PassedOnRetry), "true false",
			"retried, not passed_on_retry: a retest that ran no tests proves nothing")
	})

	t.Run("a retest with no summary: rc decides", func(t *testing.T) {
		t.Parallel()
		// No summary line to read says nothing about the tests: the retest's rc 0 is green.
		k := newFk(t, goLines(), cmd("check", redOne), cmd("retest", `exit 0`))
		o, err := k.laneTest(k.green(lane(k.submit())))
		if err != nil || o.Kind != port.OutcomeGreen {
			t.Fatalf("test: %+v %v; want green (nothing to count: rc 0)\noutput:\n%s", o, err, k.out.String())
		}
		k.match(o.Line, `^GREEN T-05 \(rc=0, full:no-guard\)$`, "the retest's rc is the step's counts")
	})

	t.Run("a build failure: red, no retest", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, goLines(),
			cmd("check", `echo "FAIL make build"; echo "FAIL example.com/pkg [build failed]"; exit 1`),
			cmd("retest", retestCmd("0")))
		o, err := k.laneTest(k.green(lane(k.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red", o, err)
		}
		if k.ran("retests") {
			t.Error("a build failure was retested; want no retest")
		}
	})

	t.Run("no commands.retest: red, no rerun", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, goLines(), cmd("check", redOne))
		o, err := k.laneTest(k.green(lane(k.submit())))
		if err != nil || o.Kind != port.OutcomeRed {
			t.Fatalf("test: %+v %v; want red", o, err)
		}
		k.match(o.Line, `^RED check \(rc=1, full:no-guard\) — failing: --- FAIL: TestAlpha`, "the red line")
		got := k.flakeLog()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		k.eq(fmt.Sprint(got[0].Retried, got[0].PassedOnRetry), "false false", "no retry")
	})
}
