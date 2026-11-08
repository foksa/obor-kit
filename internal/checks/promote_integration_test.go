//go:build integration

package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// T-974: serve's promotion run (Promote: the full suite in <scratch_dir>-promote at the sha, a pool slot, the retest
// once), the fast-forward of branches.promote (PromoteFF) and after_merge in the promotion worktree (AfterPromote).

// promoteFx is fx with branches.promote "prod" at the fixture commit and one more commit on main: its sha.
func promoteFx(t *testing.T, edits ...func(map[string]any)) (*fx, string) {
	t.Helper()
	x := newFx(t, append([]func(map[string]any){testkit.Set("branches.promote", "prod")}, edits...)...)
	x.Git(x.Root, "branch", "prod")
	x.Write("next.txt", "next\n")
	x.Git(x.Root, "add", "next.txt")
	x.Git(x.Root, "commit", "-qm", "Merge T-05: next")
	return x, x.head(x.Root)
}

// record is a command that writes where, at which commit, with which slot priority and OBOR_ROOT it ran under
// $TMP_DIR/<name>-*, then prints "1 pass".
func record(name string) string {
	return `pwd -P > "$TMP_DIR/` + name + `-pwd"; git rev-parse HEAD > "$TMP_DIR/` + name + `-head"; ` +
		`echo "$CHECK_PRIORITY" > "$TMP_DIR/` + name + `-prio"; echo "$OBOR_ROOT" > "$TMP_DIR/` + name + `-root"; echo "1 pass"`
}

func (x *fx) tmp(name string) string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(x.Base, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func TestPromoteRunsTheFullSuiteAndFastForwards(t *testing.T) {
	t.Parallel()
	x, h := promoteFx(t, cmd("check", record("check")))
	prod := x.ref("refs/heads/prod")
	r, err := Promote(context.Background(), x.opts(), h)
	if err != nil || r.Verdict != "GREEN" || r.SHA != h || r.Retried {
		t.Fatalf("Promote = %+v, %v; want GREEN at %s, not retried\n%s", r, err, h, x.out.String())
	}
	dir, _ := filepath.EvalSymlinks(x.scratch + "-promote")
	x.eq(x.tmp("check-pwd"), dir, "check ran in the promotion worktree")
	x.eq(x.tmp("check-head"), h, "at the promoted sha")
	x.eq(x.tmp("check-prio"), "worker", "holding a pool slot, never the merge marker")
	x.match(x.Read(".obor/state/"+LastPromotionFile), `^`+h+` [0-9]+ GREEN promote: check 1 pass, [0-9]+s\n$`,
		"the promotion's own result file")
	x.gone(".obor/state/last-full-check", "the nightly's file untouched (T-978: a promotion is no hand-run nightly)")
	x.eq(x.ref("refs/heads/prod"), prod, "the run itself moves no branch")

	ff, err := PromoteFF(context.Background(), x.opts(), h)
	if err != nil || ff.Outcome != port.FFMoved || x.ref("refs/heads/prod") != h {
		t.Fatalf("PromoteFF = %+v, %v; prod %s, want moved to %s", ff, err, x.ref("refs/heads/prod"), h)
	}
	if ff, err := PromoteFF(context.Background(), x.opts(), h); err != nil || ff.Outcome != port.FFAlready {
		t.Errorf("a second PromoteFF = %+v, %v; want already", ff, err)
	}
	if err := os.Remove(filepath.Join(x.Base, "check-head")); err != nil {
		t.Fatal(err)
	}
	if r, err := Promote(context.Background(), x.opts(), h); err != nil || r.Verdict != "GREEN" || x.tmp("check-head") != "" {
		t.Errorf("prod at %s already: Promote = %+v, %v, check ran: %q; want GREEN without a second run", h, r, err,
			x.tmp("check-head"))
	}
}

// T-989: the lane test runs the short tier (CHECK_SHORT=1, lanetest.go); the promotion's check does not carry it, so the
// full suite runs at the promotion.
func TestPromoteRunsTheFullSuite(t *testing.T) {
	t.Parallel()
	x, h := promoteFx(t, cmd("check", `env > "$TMP_DIR/promote-env"; echo 1 pass`))
	if r, err := Promote(context.Background(), x.opts(), h); err != nil || r.Verdict != "GREEN" {
		t.Fatalf("Promote = %+v, %v; want GREEN\n%s", r, err, x.out.String())
	}
	env := x.tmp("promote-env")
	if env == "" {
		t.Fatal("precondition: the promotion's check recorded its environment")
	}
	if strings.Contains("\n"+env+"\n", "\nCHECK_SHORT=1\n") {
		t.Errorf("the promotion's env carries CHECK_SHORT=1: the full suite must run there:\n%s", env)
	}
}

// RV-136 #3 (T-978): a nightly RED stays in last-full-check through a GREEN promotion (its own file,
// last-promotion), so serve's nightly (LastFullCheck) never reads the promotion as a hand-run GREEN nightly.
func TestPromoteLeavesANightlyRedAlone(t *testing.T) {
	t.Parallel()
	x, h := promoteFx(t, cmd("check", "echo 1 pass"))
	x.Write(".obor/state/last-full-check", "abc1234 1700000000 RED failed: check_milestone; 9s\n")
	if sha, _, verdict, err := LastFullCheck(x.Root); err != nil || sha != "abc1234" || verdict != "RED" {
		t.Fatalf("precondition: the nightly RED = %s %s %v", sha, verdict, err)
	}
	if r, err := Promote(context.Background(), x.opts(), h); err != nil || r.Verdict != "GREEN" {
		t.Fatalf("Promote = %+v, %v; want GREEN\n%s", r, err, x.out.String())
	}
	if sha, _, verdict, err := LastFullCheck(x.Root); err != nil || sha != "abc1234" || verdict != "RED" {
		t.Errorf("after a GREEN promotion: last-full-check %s %s %v, want the nightly's RED abc1234 kept", sha, verdict,
			err)
	}
	x.match(x.Read(".obor/state/"+LastPromotionFile), `^`+h+` [0-9]+ GREEN `, "the promotion's GREEN in its own file")
}

func TestPromoteFFRefusesANonAncestor(t *testing.T) {
	t.Parallel()
	x, h := promoteFx(t, cmd("check", "echo 1 pass"))
	x.Git(x.Root, "checkout", "-q", "prod")
	x.Write("other.txt", "other\n")
	x.Git(x.Root, "add", "other.txt")
	x.Git(x.Root, "commit", "-qm", "on prod by hand")
	x.Git(x.Root, "checkout", "-q", "main")
	prod := x.ref("refs/heads/prod")
	if ff, err := PromoteFF(context.Background(), x.opts(), h); err != nil || ff.Outcome != port.FFRefused ||
		!strings.Contains(ff.Line, "not an ancestor") {
		t.Errorf("PromoteFF over a diverged prod = %+v, %v; want refused, not an ancestor", ff, err)
	}
	x.eq(x.ref("refs/heads/prod"), prod, "prod untouched")
	if passed, err := PromotePassed(context.Background(), x.opts(), h); err != nil || passed {
		t.Errorf("PromotePassed over a diverged prod = %v, %v; want false", passed, err)
	}
}

// T-984 (RV-136 verify #1): a sha behind the promoted branch (the switch created prod past it) is passed: PromotePassed
// says so, PromoteFF is already (prod not moved back), Promote is GREEN with no suite run; a sha ahead of prod is not.
func TestPromoteShaBehindThePromotedBranch(t *testing.T) {
	t.Parallel()
	x, h := promoteFx(t, cmd("check", record("check")))
	old := x.ref("refs/heads/prod")
	if passed, err := PromotePassed(context.Background(), x.opts(), h); err != nil || passed {
		t.Fatalf("precondition: prod at %s behind %s: PromotePassed = %v, %v; want false", old, h, passed, err)
	}
	x.Git(x.Root, "branch", "-f", "prod", h) // the switch: prod at the head, past old
	for _, sha := range []string{old, h} {
		if passed, err := PromotePassed(context.Background(), x.opts(), sha); err != nil || !passed {
			t.Errorf("prod at %s: PromotePassed(%s) = %v, %v; want true", h, sha, passed, err)
		}
	}
	ff, err := PromoteFF(context.Background(), x.opts(), old)
	if err != nil || ff.Outcome != port.FFAlready {
		t.Errorf("PromoteFF to %s behind prod = %+v, %v; want already", old, ff, err)
	}
	x.eq(x.ref("refs/heads/prod"), h, "prod not moved back")
	r, err := Promote(context.Background(), x.opts(), old)
	if err != nil || r.Verdict != "GREEN" || r.SHA != old {
		t.Errorf("Promote at %s behind prod = %+v, %v; want GREEN", old, r, err)
	}
	x.eq(x.tmp("check-head"), "", "no suite run at a passed sha")
	x.gone(".obor/state/"+LastPromotionFile, "no promotion result written")
}

// A red check reruns its failing tests once (commands.retest): green then is GREEN, retried; red again is RED with the
// failing test named.
func TestPromoteRetestsRedOnce(t *testing.T) {
	t.Parallel()
	const grep = `^--- FAIL|^FAIL\s`
	const redOne = `echo "--- FAIL: TestAlpha (0.01s)"; echo "FAIL example.com/pkg 0.03s"; exit 1`
	retest := func(rc string) string {
		return `n=$(cat "$TMP_DIR/retests" 2>/dev/null || echo 0); echo $((n + 1)) > "$TMP_DIR/retests"; echo "1 pass"; exit ` + rc
	}
	t.Run("green on the retest", func(t *testing.T) {
		t.Parallel()
		x, h := promoteFx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne), cmd("retest", retest("0")))
		r, err := Promote(context.Background(), x.opts(), h)
		if err != nil || r.Verdict != "GREEN" || !r.Retried {
			t.Fatalf("Promote = %+v, %v; want GREEN, retried\n%s", r, err, x.out.String())
		}
		x.eq(x.tmp("retests"), "1", "one retest")
	})
	t.Run("red twice", func(t *testing.T) {
		t.Parallel()
		x, h := promoteFx(t, testkit.Set("merge.fail_grep", grep), cmd("check", redOne), cmd("retest", retest("1")))
		r, err := Promote(context.Background(), x.opts(), h)
		if err != nil || r.Verdict != "RED" || !r.Retried {
			t.Fatalf("Promote = %+v, %v; want RED, retried\n%s", r, err, x.out.String())
		}
		x.contains(r.Line, "TestAlpha", "the RED line names the failing test")
		x.eq(x.tmp("retests"), "1", "one retest, never more")
		x.match(x.Read(".obor/state/"+LastPromotionFile), `^`+h+` [0-9]+ RED failed: check; `, "last-promotion")
		x.gone(".obor/state/last-full-check", "the nightly's file untouched")
	})
}

func TestAfterPromoteRunsInThePromotionWorktree(t *testing.T) {
	t.Parallel()
	x, h := promoteFx(t, cmd("check", "echo 1 pass"), cmd("after_merge", record("after")))
	line, err := AfterPromote(context.Background(), x.opts(), h, "")
	if err != nil || line != "ran" {
		t.Fatalf("AfterPromote = %q, %v; want ran\n%s", line, err, x.out.String())
	}
	dir, _ := filepath.EvalSymlinks(x.scratch + "-promote")
	x.eq(x.tmp("after-pwd"), dir, "after_merge ran in the promotion worktree")
	x.eq(x.tmp("after-head"), h, "at the promoted sha")
	x.eq(x.tmp("after-root"), x.Root, "OBOR_ROOT is the main checkout")
}

// T-974, row 45: the nightly leaves check out while branches.promote is set (each promotion runs it).
func TestNightlyDropsCheckWhenPromoteIsSet(t *testing.T) {
	t.Parallel()
	for _, promote := range []string{"", "prod"} {
		x := newFx(t, testkit.Set("branches.promote", promote), cmd("check", record("check")))
		r := x.nightly()
		ran := x.tmp("check-head") != ""
		if r.Code != OK || ran != (promote == "") {
			t.Errorf("promote %q: nightly %+v, check ran %v; want GREEN, check run only while promote is unset",
				promote, r, ran)
		}
	}
}
