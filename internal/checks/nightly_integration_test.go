//go:build integration

package checks

import (
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
	"obor/internal/testkit"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logged is a command that logs "<name> <scratch HEAD> <CHECK_PRIORITY>" to <Base>/ran, then runs then.
func logged(name, then string) string {
	return fmt.Sprintf(`echo "%s $(git rev-parse HEAD) ${CHECK_PRIORITY:-none}" >> "$TMP_DIR/ran"; %s`, name, then)
}

// nightly is a config edit: nightly.also_run is names, in order (T-817).
func nightly(names ...any) func(map[string]any) {
	return testkit.Set("nightly.also_run", names)
}

func TestNightly(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		test func(t *testing.T)
	}{
		// bash nightly.test.sh case_green_all_commands
		{"GREEN runs lane_setup, check, check_milestone at the head; state file; lock/slot released", func(t *testing.T) {
			x := newFx(t, cmd("lane_setup", logged("lane_setup", "true")),
				cmd("check", map[string]any{"cmd": logged("check", "echo 7 pass; echo 0 fail"), "timeout": 10}),
				cmd("check_milestone", logged("check_milestone", "echo 30 pass")))
			head := x.head(x.Root)
			x.gone(x.nightlyDir(), "precondition: no worktree yet")
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(lane_setup rc=0, check 7 pass, 0 fail, check_milestone 30 pass, [0-9]+s\)$`)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), strings.ReplaceAll("lane_setup H none\ncheck H merge\ncheck_milestone H merge\n", "H", head),
				"every command once, in order, at the head")
			state := x.Read(".obor/state/last-full-check")
			x.match(state, `^`+head+` [0-9]+ GREEN lane_setup rc=0, check 7 pass, 0 fail, check_milestone 30 pass, [0-9]+s\n$`, "state file")
			var epoch int64
			fmt.Sscan(strings.Fields(state)[1], &epoch)
			if time.Now().Unix()-epoch > 60 {
				t.Errorf("state epoch %d not recent", epoch)
			}
			x.gone(x.window(), "nightly.flock released")
			x.gone(filepath.Join(x.slots, "merge-running-proj"), "slot released")
			x.eq(x.Git(x.nightlyDir(), "branch", "--show-current"), "", "worktree detached")
			x.eq(x.head(x.nightlyDir()), head, "worktree at the head")
			x.eq(x.Git(x.nightlyDir(), "status", "--porcelain"), "", "worktree clean")
			x.gone(x.scratch, "serve's worktree not created")
			x.eq(x.Git(x.Root, "status", "--porcelain"), "", "main checkout untouched")
			for _, name := range []string{"lane_setup", "check", "check_milestone"} {
				x.gone(x.Path(".obor/state/logs/nightly-"+name+".log"), "no log for a passing "+name)
			}
		}},
		// bash case_red_runs_everything; new: failing tests and log paths in the RED line
		{"RED keeps running the rest, details per failed command, failing tests and logs named, RED state file, exit 3", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "echo 1 pass; echo 2 fail; echo Expected: 4; echo Received; exit 1")),
				cmd("check_milestone", logged("check_milestone", "seq 1 20; exit 5")))
			head := x.head(x.Root)
			logs := x.Path(".obor/state/logs/nightly-")
			x.is(x.nightly(), Red, `^NIGHTLY RED `+head[:7]+` on main \(failed: check, check_milestone; check 1 pass, 2 fail, check_milestone rc=5, [0-9]+s\) — `+
				`check: failing: Expected: 4; log `+q(logs)+`check\.log \| check_milestone: log `+q(logs)+`check_milestone\.log$`)
			ran := x.Read(filepath.Join(x.Base, "ran"))
			x.eq(fmt.Sprint(strings.Count(ran, "\n")), "2", "non-blocking: both ran")
			out := x.out.String()
			x.contains(out, "--- check (rc 1)\nExpected: 4\n", "check details")
			x.lacks(out, "Received", "only fail_grep lines")
			x.contains(out, "--- check_milestone (rc 5)\n6\n7\n", "check_milestone: its last 15 lines")
			x.lacks(out, "\n5\n", "check_milestone: not more")
			x.contains(x.Read(logs+"check.log"), "Received", "full log kept")
			x.match(x.Read(".obor/state/last-full-check"), `^`+head+` [0-9]+ RED failed: check, check_milestone; check 1 pass, 2 fail, check_milestone rc=5, [0-9]+s\n$`, "state file")
			x.gone(x.window(), "lock released")
		}},
		// T-805: a passing conformance run's skips and warns are in the report (RV-103 #1: herdr missing from
		// nightly's PATH reads "0 pass, 0 fail" today)
		{"conformance exit 0 with SKIP lines: the line carries their count, every SKIP line printed, still GREEN", func(t *testing.T) {
			x := newFx(t, nightly("conformance"), cmd("conformance", logged("conformance",
				`echo "SKIP H-S1 herdr is not installed (herdr.sock, CAP §5.1)"; echo "SKIP H-S2 herdr is not installed (herdr.go:12, CAP §5.1)"; echo "conformance: 0 pass, 0 fail, 2 skip, 0 warn"`)))
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(conformance \(non-gating\) 0 pass, 0 fail, 2 skip, 0 warn, [0-9]+s\)$`)
			x.contains(x.out.String(), "--- conformance (non-gating)\n"+
				"SKIP H-S1 herdr is not installed (herdr.sock, CAP §5.1)\n"+
				"SKIP H-S2 herdr is not installed (herdr.go:12, CAP §5.1)\n", "both SKIP lines below the conformance line")
			x.match(x.Read(".obor/state/last-full-check"),
				`^`+head+` [0-9]+ GREEN conformance \(non-gating\) 0 pass, 0 fail, 2 skip, 0 warn, [0-9]+s\n$`, "state file")
		}},
		{"conformance exit 0 with a WARN line: the line carries its count, the WARN line printed, still GREEN", func(t *testing.T) {
			x := newFx(t, nightly("conformance"), cmd("conformance", logged("conformance",
				`echo "PASS H-S1 obor sends only fields herdr knows (herdr.go:40, CAP §6)"; echo "WARN H-S2 herdr's protocol is newer than obor's (herdr.go:9, CAP §6)"; echo "conformance: 1 pass, 0 fail, 0 skip, 1 warn"`)))
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(conformance \(non-gating\) 1 pass, 0 fail, 0 skip, 1 warn, [0-9]+s\)$`)
			x.contains(x.out.String(), "--- conformance (non-gating)\n"+
				"WARN H-S2 herdr's protocol is newer than obor's (herdr.go:9, CAP §6)\n", "the WARN line below the conformance line")
			x.lacks(x.out.String(), "PASS H-S1", "no PASS lines")
		}},
		{"conformance exit 0 all PASS: one line as today, no details", func(t *testing.T) {
			x := newFx(t, nightly("conformance"), cmd("conformance", logged("conformance",
				`echo "PASS H-S1 obor sends only fields herdr knows (herdr.go:40, CAP §6)"; echo "conformance: 2 pass, 0 fail, 0 skip, 0 warn"`)))
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(conformance \(non-gating\) 2 pass, 0 fail, 0 skip, 0 warn, [0-9]+s\)$`)
			// T-1098: no merged-log step, and every sweep step had something to go on: no details.
			x.eq(x.out.String(), "", "no conformance detail lines")
		}},
		// T-798: the conformance suite's static tier is a line in the report, never RED
		{"a failing conformance command is a non-gating WARN line: GREEN, its FAIL lines printed", func(t *testing.T) {
			x := newFx(t, nightly("conformance"), cmd("check", logged("check", "echo 7 pass; echo 0 fail")),
				cmd("conformance", logged("conformance", "echo FAIL H-S1 a method is gone; echo 3 pass, 1 fail; exit 1")))
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(check 7 pass, 0 fail, WARN conformance \(rc 1, non-gating\): 3 pass, 1 fail, [0-9]+s\)$`)
			x.contains(x.out.String(), "--- conformance (non-gating)\nFAIL H-S1 a method is gone\n", "conformance details")
			x.match(x.Read(".obor/state/last-full-check"), `^`+head+` [0-9]+ GREEN check 7 pass, 0 fail, WARN conformance \(rc 1, non-gating\): 3 pass, 1 fail, [0-9]+s\n$`, "state file")
		}},
		// bash case_lane_setup_failure_and_timeout
		{"lane_setup failure → RED, nothing else runs; a timeout → RED timed out", func(t *testing.T) {
			x := newFx(t, cmd("lane_setup", "echo boom; exit 2"), cmd("check", logged("check", "true")))
			x.is(x.nightly(), Red, `^NIGHTLY RED [0-9a-f]{7} on main \(failed: lane_setup; lane_setup rc=2, [0-9]+s\) — lane_setup: log `)
			x.gone(filepath.Join(x.Base, "ran"), "nothing after a lane_setup failure")
			x.contains(x.out.String(), "boom", "its output")
			y := newFx(t, cmd("check", map[string]any{"cmd": "sleep 1", "timeout": 1}))
			y.is(y.nightly(), Red, `^NIGHTLY RED [0-9a-f]{7} on main \(failed: check; check timed out after 1 s, [0-9]+s\)`)
		}},
		// bash case_no_commands_green
		{"no commands → GREEN no commands", func(t *testing.T) {
			x := newFx(t)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(no commands, [0-9]+s\)$`)
			x.match(x.Read(".obor/state/last-full-check"), `^[0-9a-f]{40} [0-9]+ GREEN no commands, [0-9]+s\n$`, "state file")
		}},
		// bash case_scratch_reset_to_current_branch
		{"an existing worktree is reset to the current branch's head, untracked junk cleaned", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "test ! -e junk.txt")))
			x.Git(x.Root, "worktree", "add", "-q", "--detach", x.nightlyDir(), "main~1")
			x.Write(filepath.Join(x.nightlyDir(), "junk.txt"), "junk\n")
			x.Git(x.Root, "checkout", "-q", "-b", "ms/m1")
			x.Write("b.txt", "two\n")
			x.Git(x.Root, "add", "b.txt")
			x.Git(x.Root, "commit", "-q", "-m", "milestone work")
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on ms/m1 `)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check "+head+" merge\n", "ran at the head, junk cleaned")
		}},
		// bash case_live_lock_waits_then_fails
		{"a live nightly lock → waits (line), FAIL after its wait, nothing run", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "true")))
			x.windowWait = time.Second
			x.holdWindow()
			x.is(x.nightly(), Fail, `^FAIL `+q(x.window())+` still held after [0-9]+s \(pid `+fmt.Sprint(os.Getpid())+`\)$`)
			x.contains(x.out.String(), fmt.Sprintf("obor nightly: waiting for %s (pid %d)", x.window(), os.Getpid()), "wait line")
			x.gone(filepath.Join(x.Base, "ran"), "nothing ran")
			x.gone(".obor/state/last-full-check", "no state written")
			x.gone(x.nightlyDir(), "worktree not created")
		}},
		// bash case_live_lock_released_then_runs, case_stale_lock_replaced
		{"a live lock that goes away → waits, then GREEN; a dead holder's lock file is not waited for", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "true")))
			x.windowWait = 20 * time.Second
			stop := x.holdWindow()
			time.AfterFunc(1500*time.Millisecond, stop)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `)
			x.contains(x.out.String(), "waiting for "+x.window(), "waited")
			x.Write(x.window(), fmt.Sprintf("%d\n", testkit.DeadPID(t)))
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `)
			x.lacks(x.out.String(), "waiting", "a dead holder's lock is not waited for")
		}},
		// T-409: nightly runs in its own worktree; serve's (<lanes_dir>/merge), an open merge in it and all, is not
		// touched.
		{"serve's worktree holding a merge is untouched; nightly's own worktree with a commit not in the target is reset", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "test ! -e held.txt")))
			for _, dir := range []string{x.scratch, x.nightlyDir()} {
				x.Git(x.Root, "worktree", "add", "-q", "--detach", dir, "main")
				x.Write(filepath.Join(dir, "held.txt"), "held\n")
				x.Git(dir, "add", "held.txt")
				x.Git(dir, "commit", "-q", "-m", "held")
			}
			held := x.head(x.scratch)
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main `)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check "+head+" merge\n", "ran at the head")
			x.eq(x.head(x.scratch), held, "serve's worktree untouched")
			x.eq(x.head(x.nightlyDir()), head, "nightly's worktree reset")
		}},
		{"a failing check_milestone → RED, its log named, state file", func(t *testing.T) {
			x := newFx(t, cmd("check_milestone", "echo slow; exit 1"))
			x.is(x.nightly(), Red, `^NIGHTLY RED [0-9a-f]{7} on main \(failed: check_milestone; .* — check_milestone: log .*/nightly-check_milestone\.log$`)
			x.match(x.Read(".obor/state/last-full-check"), ` RED failed: check_milestone; `, "state file")
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}

// T-1168: the nightly's check and check_milestone rerun a red's named failing tests once with commands.retest, as a
// lane test does (T-964), inside the slot the check already holds: green then is the step's green, the GREEN line names
// the rerun ("retest saved <n>") and the flake is logged retried and passed. A retest that fails leaves the original
// red with its flake line retried, not passed; a project without commands.retest reds as before.
func TestNightlyRetest(t *testing.T) {
	t.Parallel()
	// redOne is a check that records the slot it held and names one failing test and its package (T-1047's lines).
	const redOne = `echo "${CHECK_SLOT_HELD:-none}" > "$TMP_DIR/check-slot"; ` +
		`echo "--- FAIL: TestAlpha (0.01s)"; echo "FAIL example.com/pkg 0.03s"; exit 1`
	// retestCmd is commands.retest: it counts its runs and records where and how it ran under $TMP_DIR, then exits rc.
	retestCmd := func(rc string) string {
		return `n=$(cat "$TMP_DIR/retests" 2>/dev/null || echo 0); echo $((n + 1)) > "$TMP_DIR/retests"; ` +
			`pwd -P > "$TMP_DIR/retest-pwd"; git rev-parse HEAD > "$TMP_DIR/retest-head"; ` +
			`echo "${CHECK_PRIORITY:-none}" > "$TMP_DIR/retest-prio"; echo "${CHECK_SLOT_HELD:-none}" > "$TMP_DIR/retest-slot"; ` +
			`echo "^({tests})$" > "$TMP_DIR/retest-tests"; echo "$OBOR_FAILED_PKGS" > "$TMP_DIR/retest-pkgs"; ` +
			`echo "1 pass"; exit ` + rc
	}
	t.Run("check: the retest saves it, GREEN names the rerun, the flake passed on retry", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", redOne), cmd("retest", retestCmd("0")))
		head := x.head(x.Root)
		x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(check 1 pass, retest saved 1, [0-9]+s\)$`)
		x.match(x.Read(".obor/state/last-full-check"),
			`^`+head+` [0-9]+ GREEN check 1 pass, retest saved 1, [0-9]+s\n$`, "state file")
		x.eq(x.Read(filepath.Join(x.Base, "retests")), "1\n", "one retest, never more")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-tests"))), "^(TestAlpha)$",
			"{tests}: the failing test, in commands.retest's template")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-pkgs"))), "example.com/pkg", "OBOR_FAILED_PKGS: its package")
		dir, _ := filepath.EvalSymlinks(x.nightlyDir())
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-pwd"))), dir, "the retest ran in the nightly's worktree")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-head"))), head, "at the nightly's head")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-prio"))), "merge", "at merge priority")
		x.eq(strings.TrimSpace(x.Read(filepath.Join(x.Base, "retest-slot"))),
			strings.TrimSpace(x.Read(filepath.Join(x.Base, "check-slot"))), "inside the slot the check already held")
		got := x.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		x.eq(got[0].Test, "TestAlpha", "the flake's test")
		x.eq(got[0].Check, "check", "the flake's command")
		x.eq(got[0].Task, "nightly", "the flake's task")
		x.eq(got[0].SHA, head, "the flake's sha")
		x.eqBool(got[0].Retried, true, "retried")
		x.eqBool(got[0].PassedOnRetry, true, "passed_on_retry")
	})
	t.Run("check: the retest fails too, the original RED, the flake not passed", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", redOne), cmd("retest", retestCmd("1")))
		head := x.head(x.Root)
		x.is(x.nightly(), Red, `^NIGHTLY RED `+head[:7]+` on main \(failed: check; check rc=1, [0-9]+s\) — `+
			`check: failing: --- FAIL: TestAlpha \(0\.01s\), FAIL example\.com/pkg 0\.03s; log `)
		x.eq(x.Read(filepath.Join(x.Base, "retests")), "1\n", "one retest, never more")
		got := x.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		x.eqBool(got[0].Retried, true, "retried")
		x.eqBool(got[0].PassedOnRetry, false, "passed_on_retry: the retest failed too")
	})
	t.Run("check_milestone: the retest saves it too", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", "echo 7 pass"), cmd("check_milestone", redOne), cmd("retest", retestCmd("0")))
		head := x.head(x.Root)
		x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(check 7 pass, check_milestone 1 pass, retest saved 1, [0-9]+s\)$`)
		x.eq(x.Read(filepath.Join(x.Base, "retests")), "1\n", "one retest")
		got := x.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		x.eq(got[0].Check, "check_milestone", "the flake's command")
		x.eqBool(got[0].PassedOnRetry, true, "passed_on_retry")
	})
	t.Run("no commands.retest: RED as before, nothing rerun", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, goLines(), cmd("check", redOne))
		head := x.head(x.Root)
		x.is(x.nightly(), Red, `^NIGHTLY RED `+head[:7]+` on main \(failed: check; check rc=1, [0-9]+s\) — `+
			`check: failing: --- FAIL: TestAlpha \(0\.01s\), FAIL example\.com/pkg 0\.03s; log `)
		x.gone(filepath.Join(x.Base, "retests"), "no retest without commands.retest")
		got := x.flakes()
		if len(got) != 1 {
			t.Fatalf("flake lines %d, want 1: %+v", len(got), got)
		}
		x.eqBool(got[0].Retried || got[0].PassedOnRetry, false, "no retry")
	})
}

// T-435: NIGHTLY backs up the project's database (no serve runs: directly) into .obor/state/backup and names it in
// its summary; a project without one has nothing to back up.
func TestNightlyBackup(t *testing.T) {
	t.Parallel()
	x := newFx(t, cmd("check", "true"))
	x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, [0-9]+s\)$`)
	x.gone(wire.BackupDir(x.Root), "no database: no backup")

	db, err := store.OpenRW(event.DBFile(x.Root))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db\.gz, [0-9]+s\)$`)
	if got, _ := filepath.Glob(filepath.Join(wire.BackupDir(x.Root), "obor-*.db.gz")); len(got) != 1 {
		t.Errorf("backups = %v, want one", got)
	}
}

// T-817: merge.nightly_commands runs after the gating checks and the backup, in order, each non-gating: one report
// line, never RED; a nonzero exit is a WARN line.
func TestNightlyCommands(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		test func(t *testing.T)
	}{
		{"two commands in order, one line each: the last line, its WARN/SKIP/FAIL lines, a failing one a WARN line; still GREEN", func(t *testing.T) {
			x := newFx(t, nightly("slow_a", "slow_b"),
				cmd("slow_a", logged("slow_a", `echo "SKIP S-1 herdr is not installed"; echo "WARN S-2 herdr is newer"; echo "slow_a: 2 pass"`)),
				cmd("slow_b", logged("slow_b", `echo "FAIL b-1 a method is gone"; echo "0 pass, 1 fail"; exit 1`)))
			head := x.head(x.Root)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(slow_a \(non-gating\) 2 pass, WARN slow_b \(rc 1, non-gating\): 0 pass, 1 fail, [0-9]+s\)$`)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), strings.ReplaceAll("slow_a H merge\nslow_b H merge\n", "H", head),
				"both ran once, in order, at the head, under the check slot")
			x.contains(x.out.String(), "--- slow_a (non-gating)\nSKIP S-1 herdr is not installed\nWARN S-2 herdr is newer\n", "slow_a's notes")
			x.contains(x.out.String(), "--- slow_b (non-gating)\nFAIL b-1 a method is gone\n", "slow_b's notes")
			x.match(x.Read(".obor/state/last-full-check"),
				` GREEN slow_a \(non-gating\) 2 pass, WARN slow_b \(rc 1, non-gating\): 0 pass, 1 fail, [0-9]+s\n$`, "state file")
		}},
		{"after the gating checks and the backup: its line comes last", func(t *testing.T) {
			x := newFx(t, nightly("slow_a"), cmd("check", "true"), cmd("slow_a", logged("slow_a", "echo done")))
			db, err := store.OpenRW(event.DBFile(x.Root))
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db\.gz, slow_a \(non-gating\) done, [0-9]+s\)$`)
		}},
		{"unset: a command no list names does not run, no line", func(t *testing.T) {
			x := newFx(t, cmd("slow_a", logged("slow_a", "echo hi")))
			x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(no commands, [0-9]+s\)$`)
			x.gone(filepath.Join(x.Base, "ran"), "nothing ran")
			// T-1098: no merged-log step: no command detail lines
			x.eq(x.out.String(), "", "no detail lines")
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}
