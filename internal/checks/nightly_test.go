package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// stepsSha is the commit the nightly's steps run "at": made up, since they read no git.
const stepsSha = "0123456789abcdef0123456789abcdef01234567"

// nightly is a config edit: nightly.also_run is names, in order.
func nightly(names ...any) func(map[string]any) {
	return testkit.Set("nightly.also_run", names)
}

// ran is a command that logs "<name> <CHECK_PRIORITY>" to <Base>/ran, then runs then (no git: it runs on a plain dir).
func ran(name, then string) string {
	return fmt.Sprintf(`echo "%s ${CHECK_PRIORITY:-none}" >> "$TMP_DIR/ran"; %s`, name, then)
}

// steps runs the nightly's steps and report (session.steps) on a plain dir, the way a nightly runs them in its
// worktree at stepsSha, with no scrub. The session is built without open, and so with no lock and no worktree: git is
// plumbing around these steps and they read none (the dir's HEAD for the flake log comes from the fake). The scrub
// that puts a worktree back at the sha is left out, so the rows here prove the commands and the report, and the real
// worktree's rows (TestNightly) its reset.
func (k *fk) steps() Result { return k.stepsScrub(nil) }

// stepsScrub is steps with the worktree's scrub (its reset back at the sha, then the lane links again, as a nightly's
// worktree has them).
func (k *fk) stepsScrub(scrub func()) Result {
	k.t.Helper()
	k.out.Reset()
	dir := filepath.Join(k.Base, "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		k.t.Fatal(err)
	}
	k.g.head[dir] = stepsSha
	ctx, cancel := context.WithCancel(context.Background())
	k.t.Cleanup(cancel)
	done := make(chan struct{})
	close(done)
	s := &session{Options: k.opts(), start: time.Now(), cancel: cancel, sigDone: done, target: "main", scratch: dir}
	s.Options.gitCtx = ctx
	s.runner = newRunner(ctx, &s.Options, k.g, dir, "nightly")
	s.sigCode = func() int { return s.signal }
	defer s.close()
	return *s.steps(stepsSha, scrub)
}

// the scrub between the gating checks and the rest resets the worktree with `clean -fdq`, which takes the lane links
// with the command artifacts: the nightly links them again before the commands after it (merge.nightly_commands).
func TestNightlyStepsRelinksAfterScrub(t *testing.T) {
	t.Parallel()
	x := newFk(t, testkit.Set("lanes.links", []any{"local.env"}), nightly("sees"),
		cmd("sees", `test -L local.env && echo linked || echo missing`))
	x.Write("local.env", "main's own\n")
	dir := filepath.Join(x.Base, "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "local.env")
	if err := os.Symlink(filepath.Join(x.Root, "local.env"), link); err != nil {
		t.Fatal(err)
	}
	scrubbed := false
	x.is(x.stepsScrub(func() {
		scrubbed = true
		if err := os.Remove(link); err != nil { // the reset's clean takes it
			t.Fatalf("the scrub: %v", err)
		}
	}), OK, `^NIGHTLY GREEN `+stepsSha[:7]+` on main \(sees \(non-gating\) linked, [0-9]+s\)$`)
	if !scrubbed {
		t.Fatal("precondition: the scrub ran")
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the scrub took the link and the nightly did not link it again (%v)", err)
	}
	if got, err := os.Readlink(link); err != nil || got != filepath.Join(x.Root, "local.env") {
		t.Errorf("the link = %q, %v; want the main checkout's local.env", got, err)
	}
}

// the nightly tests merge.target_branch's head: open takes the target from the config, not from the branch the main
// checkout has checked out (this fixture's root is a plain dir, on no branch).
func TestNightlyTarget(t *testing.T) {
	t.Parallel()
	x := newFk(t, testkit.Set("merge.target_branch", "trunk"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, r := open(ctx, x.opts(), "nightly")
	if r != nil {
		t.Fatalf("open: %d %q; want the config's target", r.Code, r.Line)
	}
	defer s.close()
	if s.target != "trunk" {
		t.Errorf("nightly target = %q, want merge.target_branch %q", s.target, "trunk")
	}
}

// flakeLog is the fixture's flake log (Flakes), failing the test on an error.
func (k *fk) flakeLog() []Flake {
	k.t.Helper()
	got, err := Flakes(k.Root)
	if err != nil {
		k.t.Fatalf("flake log: %v", err)
	}
	return got
}

// a non-gating command's result line carries the command's own summary — its last "<name>: " line —
// not the wrapper's trailer: make appends `make: *** [check-race] Error 1` to a failed recipe, and the line is what the
// nightly's result (and last-full-check) carries. The WARN line keeps its shape and the rc is the command's.
func TestNonGatingLine(t *testing.T) {
	t.Parallel()
	const raceSummary = "race: 1 data race report(s), 1 package(s) with failing tests under -race"
	for _, c := range []struct {
		name string
		cmd  string // the command's name: its summary line starts "<cmd>: "
		rc   int
		text string // what it printed
		line string // the result line the nightly carries
	}{
		{"a failing race command: its race line, not make's trailer", "race", 2,
			"==================\nWARNING: DATA RACE\nRead at 0x00c0000b4010 by goroutine 7:\n" + raceSummary + "\n" +
				"make: *** [check-race] Error 1\n",
			"WARN race (rc 2, non-gating): 1 data race report(s), 1 package(s) with failing tests under -race"},
		{"the race build did not run: its race line, not make's trailer", "race", 2,
			"race: the race build did not run (FAIL)\nmake: *** [check-race] Error 2\n",
			"WARN race (rc 2, non-gating): the race build did not run (FAIL)"},
		{"a passing command whose summary is not its last line: that line", "check_budget", 0,
			"check_budget: budget OK: 12 packages\n24 git processes\n",
			"check_budget (non-gating) budget OK: 12 packages"},
		{"no \"<name>: \" line at all: the last line", "slow_b", 1,
			"FAIL b-1 a method is gone\n0 pass, 1 fail\n",
			"WARN slow_b (rc 1, non-gating): 0 pass, 1 fail"},
		{"no line at all: rc", "slow_c", 4, " \n\n", "WARN slow_c (rc 4, non-gating): rc=4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := nonGatingLine(c.cmd, c.rc, c.text); got != c.line {
				t.Errorf("line = %q, want %q", got, c.line)
			}
		})
	}
}

// nightly's backup step as a function over wire.Backup's answer, so no test starts a nightly (and git) to see
// which reply prunes days: an error is the evidence line that makes the nightly RED and prunes nothing; an empty reply
// (a project with no database) is nothing at all; a BACKUP path is its base name in the result line and the sweep's
// permission.
func TestBackupOutcome(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		reply    string
		err      error
		line     string // the result line
		failure  string // the evidence line
		backedUp bool
	}{
		{"an error is the evidence line and prunes nothing", "", errors.New("backup failed"),
			"", "backup: backup failed", false},
		{"no database (empty reply) is no line and prunes nothing", "", nil, "", "", false},
		{"a BACKUP path is its base name in the result line and lets the sweep prune", "BACKUP /p/.obor/state/backup/obor-2026-11-09.db.gz", nil,
			"backup obor-2026-11-09.db.gz", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			line, failure, backedUp := backupOutcome(c.reply, c.err)
			if line != c.line {
				t.Errorf("line = %q, want %q", line, c.line)
			}
			if failure != c.failure {
				t.Errorf("failure = %q, want %q", failure, c.failure)
			}
			if backedUp != c.backedUp {
				t.Errorf("backedUp = %v, want %v", backedUp, c.backedUp)
			}
		})
	}
}

// the nightly's steps (session.steps) on a plain dir: what the report lines say, with no worktree. A real worktree's
// own rows (the head the commands run at, the reset, serve's worktree, the lock) are TestNightly's.
func TestNightlySteps(t *testing.T) {
	t.Parallel()
	const short = stepsSha
	head := short[:7]
	for _, c := range []struct {
		name string
		test func(t *testing.T)
	}{
		{"RED keeps running the rest, details per failed command, failing tests and logs named, RED state file", func(t *testing.T) {
			x := newFk(t, cmd("check", ran("check", "echo 1 pass; echo 2 fail; echo Expected: 4; echo Received; exit 1")),
				cmd("check_milestone", ran("check_milestone", "seq 1 20; exit 5")))
			logs := x.Path(".obor/state/logs/nightly-")
			x.is(x.steps(), Red, `^NIGHTLY RED `+head+` on main \(failed: check, check_milestone; check 1 pass, 2 fail, check_milestone rc=5, [0-9]+s\) — `+
				`check: failing: Expected: 4; log `+q(logs)+`check\.log \| check_milestone: log `+q(logs)+`check_milestone\.log$`)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check merge\ncheck_milestone merge\n", "non-blocking: both ran, under the merge slot")
			out := x.out.String()
			x.contains(out, "--- check (rc 1)\nExpected: 4\n", "check details")
			x.lacks(out, "Received", "only fail_grep lines")
			x.contains(out, "--- check_milestone (rc 5)\n6\n7\n", "check_milestone: its last 15 lines")
			x.lacks(out, "\n5\n", "check_milestone: not more")
			x.contains(x.Read(logs+"check.log"), "Received", "full log kept")
			x.match(x.Read(".obor/state/last-full-check"), `^`+short+` [0-9]+ RED failed: check, check_milestone; check 1 pass, 2 fail, check_milestone rc=5, [0-9]+s\n$`, "state file")
		}},
		// a passing conformance run's skips and warns are in the report (herdr missing from
		// nightly's PATH reads "0 pass, 0 fail" today)
		{"conformance exit 0 with SKIP lines: the line carries their count, every SKIP line printed, still GREEN", func(t *testing.T) {
			x := newFk(t, nightly("conformance"), cmd("conformance", ran("conformance",
				`echo "SKIP H-S1 herdr is not installed (herdr.sock, CAP §5.1)"; echo "SKIP H-S2 herdr is not installed (herdr.go:12, CAP §5.1)"; echo "conformance: 0 pass, 0 fail, 2 skip, 0 warn"`)))
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(conformance \(non-gating\) 0 pass, 0 fail, 2 skip, 0 warn, [0-9]+s\)$`)
			x.contains(x.out.String(), "--- conformance (non-gating)\n"+
				"SKIP H-S1 herdr is not installed (herdr.sock, CAP §5.1)\n"+
				"SKIP H-S2 herdr is not installed (herdr.go:12, CAP §5.1)\n", "both SKIP lines below the conformance line")
			x.match(x.Read(".obor/state/last-full-check"),
				`^`+short+` [0-9]+ GREEN conformance \(non-gating\) 0 pass, 0 fail, 2 skip, 0 warn, [0-9]+s\n$`, "state file")
		}},
		{"conformance exit 0 with a WARN line: the line carries its count, the WARN line printed, still GREEN", func(t *testing.T) {
			x := newFk(t, nightly("conformance"), cmd("conformance", ran("conformance",
				`echo "PASS H-S1 obor sends only fields herdr knows (herdr.go:40, CAP §6)"; echo "WARN H-S2 herdr's protocol is newer than obor's (herdr.go:9, CAP §6)"; echo "conformance: 1 pass, 0 fail, 0 skip, 1 warn"`)))
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(conformance \(non-gating\) 1 pass, 0 fail, 0 skip, 1 warn, [0-9]+s\)$`)
			x.contains(x.out.String(), "--- conformance (non-gating)\n"+
				"WARN H-S2 herdr's protocol is newer than obor's (herdr.go:9, CAP §6)\n", "the WARN line below the conformance line")
			x.lacks(x.out.String(), "PASS H-S1", "no PASS lines")
		}},
		{"conformance exit 0 all PASS: one line as today, no details", func(t *testing.T) {
			x := newFk(t, nightly("conformance"), cmd("conformance", ran("conformance",
				`echo "PASS H-S1 obor sends only fields herdr knows (herdr.go:40, CAP §6)"; echo "conformance: 2 pass, 0 fail, 0 skip, 0 warn"`)))
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(conformance \(non-gating\) 2 pass, 0 fail, 0 skip, 0 warn, [0-9]+s\)$`)
			// no merged-log step, and every sweep step had something to go on: no details.
			x.eq(x.out.String(), "", "no conformance detail lines")
		}},
		// the conformance suite's static tier is a line in the report, never RED; make's trailer after
		// the command's own summary line does not take its place.
		{"a failing conformance command is a non-gating WARN line: GREEN, its FAIL lines printed", func(t *testing.T) {
			x := newFk(t, nightly("conformance"), cmd("check", ran("check", "echo 7 pass; echo 0 fail")),
				cmd("conformance", ran("conformance", `echo "FAIL H-S1 a method is gone"; echo "conformance: 3 pass, 1 fail"; echo "make: *** [conformance] Error 1"; exit 1`)))
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(check 7 pass, 0 fail, WARN conformance \(rc 1, non-gating\): 3 pass, 1 fail, [0-9]+s\)$`)
			x.contains(x.out.String(), "--- conformance (non-gating)\nFAIL H-S1 a method is gone\n", "conformance details")
			x.match(x.Read(".obor/state/last-full-check"), `^`+short+` [0-9]+ GREEN check 7 pass, 0 fail, WARN conformance \(rc 1, non-gating\): 3 pass, 1 fail, [0-9]+s\n$`, "state file")
		}},
		// bash case_lane_setup_failure_and_timeout
		{"lane_setup failure → RED, nothing else runs", func(t *testing.T) {
			x := newFk(t, cmd("lane_setup", "echo boom; exit 2"), cmd("check", ran("check", "true")))
			x.is(x.steps(), Red, `^NIGHTLY RED `+head+` on main \(failed: lane_setup; lane_setup rc=2, [0-9]+s\) — lane_setup: log `)
			x.gone(filepath.Join(x.Base, "ran"), "nothing after a lane_setup failure")
			x.contains(x.out.String(), "boom", "its output")
		}},
		{"a check that times out → RED timed out", func(t *testing.T) {
			x := newFk(t, cmd("check", map[string]any{"cmd": "sleep 1", "timeout": 1}))
			x.is(x.steps(), Red, `^NIGHTLY RED `+head+` on main \(failed: check; check timed out after 1 s, [0-9]+s\)`)
		}},
		// bash case_no_commands_green
		{"no commands → GREEN no commands", func(t *testing.T) {
			x := newFk(t)
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(no commands, [0-9]+s\)$`)
			x.match(x.Read(".obor/state/last-full-check"), `^`+short+` [0-9]+ GREEN no commands, [0-9]+s\n$`, "state file")
		}},
		{"a failing check_milestone → RED, its log named, state file", func(t *testing.T) {
			x := newFk(t, cmd("check_milestone", "echo slow; exit 1"))
			x.is(x.steps(), Red, `^NIGHTLY RED `+head+` on main \(failed: check_milestone; .* — check_milestone: log .*/nightly-check_milestone\.log$`)
			x.match(x.Read(".obor/state/last-full-check"), ` RED failed: check_milestone; `, "state file")
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}

// the nightly's check and check_milestone rerun a red's named failing tests once with commands.retest, as a
// lane test does, inside the slot the check already holds: green then is the step's green, the GREEN line names
// the rerun ("retest saved <n>") and the flake is logged retried and passed. A retest that fails leaves the original
// red with its flake line retried, not passed; a project without commands.retest is red too.
func TestNightlyStepsRetest(t *testing.T) {
	t.Parallel()
	head := stepsSha[:7]
	// redOne is a check that records the slot it held and names one failing test and its package.
	const redOne = `echo "${CHECK_SLOT_HELD:-none}" > "$TMP_DIR/check-slot"; ` +
		`echo "--- FAIL: TestAlpha (0.01s)"; echo "FAIL example.com/pkg 0.03s"; exit 1`
	// retestCmd is commands.retest: it counts its runs and records where and how it ran under $TMP_DIR, then exits rc.
	retestCmd := func(rc string) string {
		return `n=$(cat "$TMP_DIR/retests" 2>/dev/null || echo 0); echo $((n + 1)) > "$TMP_DIR/retests"; ` +
			`pwd -P > "$TMP_DIR/retest-pwd"; ` +
			`echo "${CHECK_PRIORITY:-none}" > "$TMP_DIR/retest-prio"; echo "${CHECK_SLOT_HELD:-none}" > "$TMP_DIR/retest-slot"; ` +
			`echo "^({tests})$" > "$TMP_DIR/retest-tests"; echo "$OBOR_FAILED_PKGS" > "$TMP_DIR/retest-pkgs"; ` +
			`echo "1 pass"; exit ` + rc
	}
	// one is the flake log's only line: the test fails, with the log's size, when there is not exactly one.
	one := func(t *testing.T, x *fk) Flake {
		t.Helper()
		got := x.flakeLog()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		return got[0]
	}
	t.Run("check: the retest saves it, GREEN names the rerun, the flake passed on retry", func(t *testing.T) {
		t.Parallel()
		x := newFk(t, goLines(), cmd("check", redOne), cmd("retest", retestCmd("0")))
		x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(check 1 pass, retest saved 1, [0-9]+s\)$`)
		x.match(x.Read(".obor/state/last-full-check"),
			`^`+stepsSha+` [0-9]+ GREEN check 1 pass, retest saved 1, [0-9]+s\n$`, "state file")
		x.eq(x.Read(filepath.Join(x.Base, "retests")), "1\n", "one retest, never more")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-tests"))), "^(TestAlpha)$",
			"{tests}: the failing test, in commands.retest's template")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-pkgs"))), "example.com/pkg", "OBOR_FAILED_PKGS: its package")
		dir, _ := filepath.EvalSymlinks(filepath.Join(x.Base, "plain"))
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-pwd"))), dir, "the retest ran in the session's dir")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-prio"))), "merge", "at merge priority")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-slot"))),
			strings.TrimSpace(x.Read(filepath.Join(x.Base, "check-slot"))), "inside the slot the check already held")
		f := one(t, x)
		x.eq(f.Test, "TestAlpha", "the flake's test")
		x.eq(f.Check, "check", "the flake's command")
		x.eq(f.Task, "nightly", "the flake's task")
		x.eq(f.SHA, stepsSha, "the flake's sha")
		x.eq(fmt.Sprint(f.Retried), "true", "retried")
		x.eq(fmt.Sprint(f.PassedOnRetry), "true", "passed_on_retry")
	})
	t.Run("check: the retest fails too, the original RED, the flake not passed", func(t *testing.T) {
		t.Parallel()
		x := newFk(t, goLines(), cmd("check", redOne), cmd("retest", retestCmd("1")))
		x.is(x.steps(), Red, `^NIGHTLY RED `+head+` on main \(failed: check; check rc=1, [0-9]+s\) — `+
			`check: failing: --- FAIL: TestAlpha \(0\.01s\), FAIL example\.com/pkg 0\.03s; log `)
		x.eq(x.Read(filepath.Join(x.Base, "retests")), "1\n", "one retest, never more")
		f := one(t, x)
		x.eq(fmt.Sprint(f.Retried), "true", "retried")
		x.eq(fmt.Sprint(f.PassedOnRetry), "false", "passed_on_retry: the retest failed too")
	})
	t.Run("check_milestone: the retest saves it too", func(t *testing.T) {
		t.Parallel()
		x := newFk(t, goLines(), cmd("check", "echo 7 pass"), cmd("check_milestone", redOne), cmd("retest", retestCmd("0")))
		x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(check 7 pass, check_milestone 1 pass, retest saved 1, [0-9]+s\)$`)
		x.eq(x.Read(filepath.Join(x.Base, "retests")), "1\n", "one retest")
		f := one(t, x)
		x.eq(f.Check, "check_milestone", "the flake's command")
		x.eq(fmt.Sprint(f.PassedOnRetry), "true", "passed_on_retry")
	})
	t.Run("no commands.retest: RED as before, nothing rerun", func(t *testing.T) {
		t.Parallel()
		x := newFk(t, goLines(), cmd("check", redOne))
		x.is(x.steps(), Red, `^NIGHTLY RED `+head+` on main \(failed: check; check rc=1, [0-9]+s\) — `+
			`check: failing: --- FAIL: TestAlpha \(0\.01s\), FAIL example\.com/pkg 0\.03s; log `)
		x.gone(filepath.Join(x.Base, "retests"), "no retest without commands.retest")
		f := one(t, x)
		x.eq(fmt.Sprint(f.Retried || f.PassedOnRetry), "false", "no retry")
	})
}

// the nightly backs up the project's database (no serve runs: directly) into .obor/state/backup and names it in
// its summary; a project without one has nothing to back up.
func TestNightlyStepsBackup(t *testing.T) {
	t.Parallel()
	x := newFk(t, cmd("check", "true"))
	x.is(x.steps(), OK, `^NIGHTLY GREEN `+stepsSha[:7]+` on main \(check rc=0, [0-9]+s\)$`)
	x.gone(wire.BackupDir(x.Root), "no database: no backup")

	db, err := store.OpenRW(event.DBFile(x.Root))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.is(x.steps(), OK, `^NIGHTLY GREEN `+stepsSha[:7]+` on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db\.gz, [0-9]+s\)$`)
	if got, _ := filepath.Glob(filepath.Join(wire.BackupDir(x.Root), "obor-*.db.gz")); len(got) != 1 {
		t.Errorf("backups = %v, want one", got)
	}
}

// merge.nightly_commands runs after the gating checks and the backup, in order, each non-gating: one report
// line, never RED; a nonzero exit is a WARN line.
func TestNightlyStepsCommands(t *testing.T) {
	t.Parallel()
	head := stepsSha[:7]
	for _, c := range []struct {
		name string
		test func(t *testing.T)
	}{
		{"two commands in order, one line each: the last line, its WARN/SKIP/FAIL lines, a failing one a WARN line; still GREEN", func(t *testing.T) {
			x := newFk(t, nightly("slow_a", "slow_b"),
				cmd("slow_a", ran("slow_a", `echo "SKIP S-1 herdr is not installed"; echo "WARN S-2 herdr is newer"; echo "slow_a: 2 pass"`)),
				cmd("slow_b", ran("slow_b", `echo "FAIL b-1 a method is gone"; echo "0 pass, 1 fail"; exit 1`)))
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(slow_a \(non-gating\) 2 pass, WARN slow_b \(rc 1, non-gating\): 0 pass, 1 fail, [0-9]+s\)$`)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "slow_a merge\nslow_b merge\n", "both ran once, in order, under the check slot")
			x.contains(x.out.String(), "--- slow_a (non-gating)\nSKIP S-1 herdr is not installed\nWARN S-2 herdr is newer\n", "slow_a's notes")
			x.contains(x.out.String(), "--- slow_b (non-gating)\nFAIL b-1 a method is gone\n", "slow_b's notes")
			x.match(x.Read(".obor/state/last-full-check"),
				` GREEN slow_a \(non-gating\) 2 pass, WARN slow_b \(rc 1, non-gating\): 0 pass, 1 fail, [0-9]+s\n$`, "state file")
		}},
		{"after the gating checks and the backup: its line comes last", func(t *testing.T) {
			x := newFk(t, nightly("slow_a"), cmd("check", "true"), cmd("slow_a", ran("slow_a", "echo done")))
			db, err := store.OpenRW(event.DBFile(x.Root))
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db\.gz, slow_a \(non-gating\) done, [0-9]+s\)$`)
		}},
		{"unset: a command no list names does not run, no line", func(t *testing.T) {
			x := newFk(t, cmd("slow_a", ran("slow_a", "echo hi")))
			x.is(x.steps(), OK, `^NIGHTLY GREEN `+head+` on main \(no commands, [0-9]+s\)$`)
			x.gone(filepath.Join(x.Base, "ran"), "nothing ran")
			// no merged-log step: no command detail lines
			x.eq(x.out.String(), "", "no detail lines")
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}

// LastFullCheck splits last-full-check into the sha, time, verdict and the summary after them (serve's nightly compares
// it with the next check's).
func TestLastFullCheckSummary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const summary = "check 7 pass, 0 fail, WARN conformance (rc 1, non-gating): 3 pass, 1 fail, 12s"
	file := filepath.Join(project.StateDir(root), "last-full-check")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.WriteAtomic(file, []byte("abc1234def 1700000000 GREEN "+summary+"\n")); err != nil {
		t.Fatal(err)
	}
	sha, epoch, verdict, got, err := LastFullCheck(root)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "abc1234def" || epoch != 1700000000 || verdict != "GREEN" || got != summary {
		t.Errorf("LastFullCheck = %q, %d, %q, %q; want abc1234def, 1700000000, GREEN, %q", sha, epoch, verdict, got, summary)
	}
}
