package checks

import (
	"fmt"
	"obor/internal/app"
	"obor/internal/store"
	"obor/internal/testkit"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// logged is a command that logs "<name> <scratch HEAD> <CHECK_PRIORITY> <OBOR_GATE>" to <Base>/ran, then runs then.
func logged(name, then string) string {
	return fmt.Sprintf(`echo "%s $(git rev-parse HEAD) ${CHECK_PRIORITY:-none} ${OBOR_GATE:-0}" >> "$TMP_DIR/ran"; %s`, name, then)
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
			x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN `+head[:7]+` on main \(lane_setup rc=0, check 7 pass, 0 fail, check_milestone 30 pass, baseline rc=0, gate rc=0, e2e rc=0, [0-9]+s\)$`)
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
			x.is(x.nightly("NIGHTLY"), Red, `^NIGHTLY RED `+head[:7]+` on main \(failed: check, e2e; check 1 pass, 2 fail, gate rc=0, e2e rc=5, [0-9]+s\) — `+
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
		// bash case_lane_setup_failure_and_timeout
		{"lane_setup failure → RED, nothing else runs; a timeout → RED timed out", func(t *testing.T) {
			x := newFx(t, cmd("lane_setup", "echo boom; exit 2"), cmd("check", logged("check", "true")))
			x.is(x.nightly("NIGHTLY"), Red, `^NIGHTLY RED [0-9a-f]{7} on main \(failed: lane_setup; lane_setup rc=2, [0-9]+s\) — lane_setup: log `)
			x.gone(filepath.Join(x.Base, "ran"), "nothing after a lane_setup failure")
			x.contains(x.out.String(), "boom", "its output")
			y := newFx(t, cmd("check", map[string]any{"cmd": "sleep 8", "timeout": 1}))
			y.is(y.nightly("NIGHTLY"), Red, `^NIGHTLY RED [0-9a-f]{7} on main \(failed: check; check timed out after 1 s, [0-9]+s\)`)
		}},
		// bash case_no_commands_green
		{"no commands → GREEN no commands", func(t *testing.T) {
			x := newFx(t)
			x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(no commands, [0-9]+s\)$`)
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
			x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN `+head[:7]+` on ms/m1 `)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check "+head+" merge 0\n", "ran at the head, junk cleaned")
		}},
		// bash case_live_lock_waits_then_fails
		{"a live nightly lock → waits (line), FAIL after merge.window_wait, nothing run", func(t *testing.T) {
			x := newFx(t, testkit.Set("merge.window_wait", 1), cmd("check", logged("check", "true")))
			x.holdWindow()
			x.is(x.nightly("NIGHTLY"), Fail, `^FAIL `+q(x.window())+` still held after [0-9]+s \(pid `+fmt.Sprint(os.Getpid())+`\)$`)
			x.contains(x.out.String(), fmt.Sprintf("obor nightly: waiting for %s (pid %d)", x.window(), os.Getpid()), "wait line")
			x.gone(filepath.Join(x.Base, "ran"), "nothing ran")
			x.gone(".obor/state/last-full-check", "no state written")
			x.gone(x.nightlyDir(), "worktree not created")
		}},
		{"milestone-check: a live nightly lock → waits (line), FAIL after merge.window_wait", func(t *testing.T) {
			x := newFx(t, testkit.Set("merge.window_wait", 1), cmd("check", logged("check", "true")))
			x.holdWindow()
			x.is(x.nightly("MILESTONE"), Fail, `^FAIL `+q(x.window())+` still held after [0-9]+s`)
			x.contains(x.out.String(), "obor milestone: waiting for "+x.window(), "wait line")
			x.gone(filepath.Join(x.Base, "ran"), "nothing ran")
		}},
		// bash case_live_lock_released_then_runs, case_stale_lock_replaced
		{"a live lock that goes away → waits, then GREEN; a dead holder's lock file is not waited for", func(t *testing.T) {
			x := newFx(t, testkit.Set("merge.window_wait", 20), cmd("check", logged("check", "true")))
			stop := x.holdWindow()
			time.AfterFunc(1500*time.Millisecond, stop)
			x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN `)
			x.contains(x.out.String(), "waiting for "+x.window(), "waited")
			x.Write(x.window(), fmt.Sprintf("%d\n", testkit.DeadPID(t)))
			x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN `)
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
			x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN `+head[:7]+` on main `)
			x.eq(x.Read(filepath.Join(x.Base, "ran")), "check "+head+" merge 0\n", "ran at the head")
			x.eq(x.head(x.scratch), held, "serve's worktree untouched")
			x.eq(x.head(x.nightlyDir()), head, "nightly's worktree reset")
		}},
		// bash case_milestone_check
		{"milestone-check: the same tier → MILESTONE GREEN / RED + state file", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "true")), cmd("check_milestone", logged("check_milestone", "true")))
			head := x.head(x.Root)
			x.is(x.nightly("MILESTONE"), OK, `^MILESTONE GREEN `+head[:7]+` on main \(check rc=0, check_milestone rc=0, [0-9]+s\)$`)
			x.match(x.Read(".obor/state/last-full-check"), `^`+head+` [0-9]+ GREEN `, "state file")
			y := newFx(t, cmd("check_milestone", "echo slow; exit 1"))
			y.is(y.nightly("MILESTONE"), Red, `^MILESTONE RED [0-9a-f]{7} on main \(failed: check_milestone; .* — check_milestone: log .*/milestone-check_milestone\.log$`)
			y.match(y.Read(".obor/state/last-full-check"), ` RED failed: check_milestone; `, "state file")
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}

// T-435: NIGHTLY backs up the project's database (no serve runs: directly) into .obor/state/backup and names it in
// its summary; a project without one has nothing to back up, and MILESTONE takes none.
func TestNightlyBackup(t *testing.T) {
	t.Parallel()
	x := newFx(t, cmd("check", "true"))
	x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, [0-9]+s\)$`)
	x.gone(app.BackupDir(x.Root), "no database: no backup")

	db, err := store.OpenRW(app.DBFile(x.Root))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.is(x.nightly("MILESTONE"), OK, `^MILESTONE GREEN [0-9a-f]{7} on main \(check rc=0, [0-9]+s\)$`)
	x.gone(app.BackupDir(x.Root), "MILESTONE: no backup")
	x.is(x.nightly("NIGHTLY"), OK, `^NIGHTLY GREEN [0-9a-f]{7} on main \(check rc=0, backup obor-[0-9]{4}-[0-9]{2}-[0-9]{2}\.db, [0-9]+s\)$`)
	if got, _ := filepath.Glob(filepath.Join(app.BackupDir(x.Root), "obor-*.db")); len(got) != 1 {
		t.Errorf("backups = %v, want one", got)
	}
}

// T-464: serve's nightly run is due when the last full check is RED or has a partial merge it did not check; T-468:
// checked is git ancestry (A, then B on top of it), not the epoch. T-525: the merges are the store's facts merged
// ("<epoch> <task> <sha> full|partial" per line here); none known is due once, while the store is fresh.
func TestNightlyDue(t *testing.T) {
	t.Parallel()
	const gone = "0123456789abcdef0123456789abcdef01234567" // no such commit
	for _, c := range []struct {
		name, last, merges string
		fresh, want        bool
	}{
		{"empty store: due once", "", "", true, true},
		{"empty store, a GREEN check before it: due once", "A 200 GREEN ok\n", "", true, true},
		{"no merge since the store's first nightly", "A 200 GREEN ok\n", "", false, false},
		{"nothing yet, a nightly ran", "", "", false, false},
		{"full merge since the check", "A 200 GREEN ok\n", "100 T-1 A partial\n300 T-2 B full\n", false, false},
		{"partial merge after the checked sha", "A 200 GREEN ok\n", "100 T-1 A full\n300 T-2 B partial\n", false, true},
		{"no full check, a partial merge", "", "100 T-1 A partial\n", false, true},
		{"no full check, only full merges", "", "100 T-1 A full\n", false, false},
		{"GREEN on the partial merge", "A 200 GREEN ok\n", "100 T-1 A partial\n", false, false},
		{"GREEN on a descendant of the partial merge, older epoch", "B 50 GREEN ok\n", "100 T-1 A partial\n", false, false},
		{"GREEN, a partial merge it did not check", "A 200 GREEN ok\n", "100 T-1 A partial\n300 T-2 B partial\n", false, true},
		{"GREEN, a partial merge it did not check, older epoch", "A 200 GREEN ok\n", "100 T-2 B partial\n", false, true},
		{"GREEN, a full merge it did not check", "A 200 GREEN ok\n", "300 T-2 B full\n", false, false},
		{"RED, nothing merged since", "A 200 RED failed: check\n", "100 T-1 A partial\n", false, true},
		// DD-v0.12 A6: a sha git does not have (history rewritten, a copied store) is skipped, not an error
		{"GREEN, an unknown sha logged after it", "A 200 GREEN ok\n", "100 T-1 A partial\n300 T-9 " + gone + " partial\n", false, false},
		{"GREEN, an unknown sha and a partial merge after it", "A 200 GREEN ok\n",
			"100 T-1 A partial\n300 T-9 " + gone + " partial\n400 T-2 B partial\n", false, true},
		{"GREEN on a merge logged after an unknown sha", "B 200 GREEN ok\n", "50 T-9 " + gone + " partial\n100 T-2 B partial\n", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t)
			a := x.head(x.Root)
			x.Git(x.Root, "commit", "-q", "--allow-empty", "-m", "B")
			shas := strings.NewReplacer("A", a, "B", x.head(x.Root))
			if c.last != "" {
				x.Write(".obor/state/last-full-check", shas.Replace(c.last))
			}
			var merges []app.Merge
			for _, l := range lines(shas.Replace(c.merges)) {
				f := strings.Fields(l)
				at, _ := strconv.ParseInt(f[0], 10, 64)
				merges = append(merges, app.Merge{Task: f[1], SHA: f[2], At: time.Unix(at, 0), Full: f[3] == "full"})
			}
			if got, err := NightlyDue(x.Root, merges, c.fresh); err != nil || got != c.want {
				t.Errorf("NightlyDue = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

// RV-49 #2 (T-468): a partial merge published while a full check runs is not in the sha it checked: it stays due,
// whatever second it was published in and whichever of obor nightly / obor milestone-check wrote last-full-check.
func TestNightlyKeepsConcurrentMergeDue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, label string
		sameSecond  bool // the merge is published in the second the check completes
	}{
		{"merge during the check, completion a later second", "MILESTONE", false},
		{"merge in the completion second", "MILESTONE", true},
		{"obor nightly writes the same file", "NIGHTLY", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t)
			entered, release := filepath.Join(x.Base, "entered"), filepath.Join(x.Base, "release")
			x.Config(cmd("check", `touch "$RV49_ENTERED"; while [ ! -e "$RV49_RELEASE" ]; do sleep 0.01; done; echo 1 pass`))
			x.environ = append(x.environ, "RV49_ENTERED="+entered, "RV49_RELEASE="+release)
			old := x.head(x.Root)
			done := make(chan Result, 1)
			go func() { done <- x.nightly(c.label) }()
			ready := false
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				if _, err := os.Stat(entered); err == nil {
					ready = true
					break
				}
			}
			if !ready {
				t.Fatal("precondition: the check did not start")
			}
			if c.sameSecond { // publish early in a fresh second, so the check completes in it
				time.Sleep(time.Until(time.Unix(time.Now().Unix()+1, 0)))
			}
			x.Git(x.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-99: during the full check")
			head := x.head(x.Root)
			merges := []app.Merge{{Task: "T-99", SHA: head, At: time.Now()}} // serve's fact merged, partial
			mergeAt := time.Now().Unix()
			if !c.sameSecond {
				time.Sleep(time.Until(time.Unix(mergeAt+1, 0)))
			}
			if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := <-done
			sha, at, verdict, err := LastFullCheck(x.Root)
			if r.Code != OK || err != nil || verdict != "GREEN" || sha != old || sha == head {
				t.Fatalf("precondition: a GREEN check of the old head: %d %q; last-full-check %s %d %s %v", r.Code, r.Line, sha, at, verdict, err)
			}
			if c.sameSecond && at != mergeAt {
				t.Skipf("the check completed at %d, not in the merge's second %d", at, mergeAt)
			}
			if !c.sameSecond && at <= mergeAt {
				t.Fatalf("precondition: the check completed at %d, after the merge at %d", at, mergeAt)
			}
			if due, err := NightlyDue(x.Root, merges, false); err != nil || !due {
				t.Errorf("NightlyDue = %v, %v; want true: the merge during the check was not checked", due, err)
			}
		})
	}
}
