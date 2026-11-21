//go:build integration

package checks

import (
	"fmt"
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
		// bash case_live_lock_released_then_runs
		{"a live lock that goes away → waits, then GREEN", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "true")))
			x.windowWait = 20 * time.Second
			stop := x.holdWindow()
			time.AfterFunc(1500*time.Millisecond, stop)
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `)
			x.contains(x.out.String(), "waiting for "+x.window(), "waited")
		}},
		// bash case_stale_lock_replaced
		{"a dead holder's lock file is not waited for", func(t *testing.T) {
			x := newFx(t, cmd("check", logged("check", "true")))
			x.Write(x.window(), fmt.Sprintf("%d\n", testkit.DeadPID(t)))
			x.is(x.nightly(), OK, `^NIGHTLY GREEN `)
			x.lacks(x.out.String(), "waiting", "a dead holder's lock is not waited for")
		}},
		// nightly runs in its own worktree; serve's (<lanes.dir>/merge), an open merge in it and all, is not
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
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t) })
	}
}
