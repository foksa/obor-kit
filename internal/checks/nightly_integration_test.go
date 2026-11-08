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

// logged is a command that logs "<name> <scratch HEAD> <CHECK_PRIORITY> <OBOR_GATE>" to <Base>/ran, then runs then.
func logged(name, then string) string {
	return fmt.Sprintf(`echo "%s $(git rev-parse HEAD) ${CHECK_PRIORITY:-none} ${OBOR_GATE:-0}" >> "$TMP_DIR/ran"; %s`, name, then)
}

// nightly is a config edit: merge.nightly_commands is names, in order (T-817).
func nightly(names ...any) func(map[string]any) {
	return testkit.Set("merge.nightly_commands", names)
}

func TestNightly(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		test func(t *testing.T)
	}{
		// bash nightly.test.sh case_green_all_commands
		{"GREEN runs lane_setup, check, check_milestone, baseline, gate (forced), e2e at the head; state file; lock/slot released", func(t *testing.T) {
			x := newFx(t, cmd("lane_setup", logged("lane_setup", "true")),
				cmd("check", map[string]any{"cmd": logged("check", "echo 7 pass; echo 0 fail"), "timeout": 10}),
				cmd("check_milestone", logged("check_milestone", "echo 30 pass")),
				cmd("baseline", map[string]any{"cmd": logged("baseline", `test -d "$OBOR_BASELINE"`), "dir": "base"}),
				cmd("gate", map[string]any{"cmd": logged("gate", "true"), "paths": []any{"src/ai/**"}}),
				cmd("e2e", logged("e2e", "true")))
			os.Mkdir(x.Path("base"), 0o755)
			head := x.head(x.Root)
			x.gone(x.nightlyDir(), "precondition: no worktree yet")
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(lane_setup rc=0, check 7 pass, 0 fail, check_milestone 30 pass, baseline rc=0, gate rc=0, e2e rc=0, [0-9]+s\)$`)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), strings.ReplaceAll("lane_setup H none 0\ncheck H merge 0\ncheck_milestone H merge 0\n"+
				"baseline H merge 0\ngate H merge 1\ne2e H merge 0\n", "H", head), "every command once, in order, at the head; the gate forced")
			state := x.Read(".obor/state/last-full-check")
			x.match(state, `^`+head+` [0-9]+ GREEN lane_setup rc=0, check 7 pass, 0 fail, check_milestone 30 pass, baseline rc=0, gate rc=0, e2e rc=0, [0-9]+s\n$`, "state file")
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
		}},
		// bash case_red_runs_everything; new: failing tests and log paths in the RED line
		{"RED keeps running the rest, details per failed command, failing tests and logs named, RED state file, exit 3", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "echo 1 pass; echo 2 fail; echo Expected: 4; echo Received; exit 1")),
				cmd("gate", logged("gate", "true")), cmd("e2e", logged("e2e", "seq 1 20; exit 5")))
			head := x.head(x.Root)
			logs := x.Path(".obor/state/logs/nightly-")
			x.is(x.nightly(), Red, `^NIGHTLY RED `+head[:7]+` on main \(failed: check, e2e; check 1 pass, 2 fail, gate rc=0, e2e rc=5, [0-9]+s\) — `+
				`check: failing: Expected: 4; log `+q(logs)+`check\.log \| e2e: log `+q(logs)+`e2e\.log$`)
			ran := x.Read(filepath.Join(x.Base, "ran"))
			x.eq(fmt.Sprint(strings.Count(ran, "\n")), "3", "non-blocking: all three ran")
			out := x.out.String()
			x.contains(out, "--- check (rc 1)\nExpected: 4\n", "check details")
			x.lacks(out, "Received", "only fail_grep lines")
			x.contains(out, "--- e2e (rc 5)\n6\n7\n", "e2e: its last 15 lines")
			x.lacks(out, "\n5\n", "e2e: not more")
			x.contains(x.Read(logs+"check.log"), "Received", "full log kept")
			x.match(x.Read(".obor/state/last-full-check"), `^`+head+` [0-9]+ RED failed: check, e2e; check 1 pass, 2 fail, gate rc=0, e2e rc=5, [0-9]+s\n$`, "state file")
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
			x.eq(x.out.String(), "", "no detail lines")
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
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check "+head+" merge 0\n", "ran at the head, junk cleaned")
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
		// T-409: nightly runs in its own worktree; serve's (merge.scratch_dir), an open merge in it and all, is not touched.
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
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check "+head+" merge 0\n", "ran at the head")
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
	x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db, [0-9]+s\)$`)
	if got, _ := filepath.Glob(filepath.Join(wire.BackupDir(x.Root), "obor-*.db")); len(got) != 1 {
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
			x.eq(x.Read(filepath.Join(x.Base, "ran")), strings.ReplaceAll("slow_a H merge 0\nslow_b H merge 0\n", "H", head),
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
			x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db, slow_a \(non-gating\) done, [0-9]+s\)$`)
		}},
		{"unset: a command no list names does not run, no line", func(t *testing.T) {
			x := newFx(t, cmd("slow_a", logged("slow_a", "echo hi")))
			x.is(x.nightly(), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(no commands, [0-9]+s\)$`)
			x.gone(filepath.Join(x.Base, "ran"), "nothing ran")
			x.eq(x.out.String(), "", "no detail lines")
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}
