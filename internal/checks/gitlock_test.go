package checks

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/project"
)

// T-382: git lock contention at serve's git boundary (docs/state-model.md "Crash classes").

// lockFx is the fixture's Scratch (real git) and the lane's index.lock.
func lockFx(t *testing.T) (*fx, Scratch, string) {
	t.Helper()
	x := newFx(t)
	lock := x.Git(x.lane, "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
	return x, Scratch{x.opts()}, lock
}

func (x *fx) committed() bool {
	return x.Git(x.lane, "log", "-1", "--format=%s") != "fixture" && x.Git(x.lane, "status", "--porcelain",
		"--", "feature.txt") == ""
}

// T-382: git lock contention at serve's git boundary (docs/state-model.md "Crash classes"), on submit's git calls: a
// lock a live git holds, old enough to be stale but for its holder, is waited for and the submit goes on; a stale one
// (no git running) is removed once, logged; a stale one that comes back is not removed again, and its git call's error
// is the step's.
func TestSubmitLocks(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		run  func(t *testing.T, x *fx, s Scratch, lock string)
	}{
		{"a live git's lock: waited for, then committed, nothing removed", func(t *testing.T, x *fx, s Scratch, lock string) {
			asked := 0
			s.GitAlive = func(dir string) bool {
				if asked++; asked == 2 { // the holder ends after a look
					os.Remove(lock)
				}
				return true
			}
			if _, err := s.Submit(ctx, "T-05", "1", "L-01", "feature", "pj-l1"); err != nil {
				t.Fatalf("submit with the lock held by a live git: %v", err)
			}
			if asked < 2 {
				t.Errorf("the lock's holder was asked about %d times, want the submit to wait for it", asked)
			}
			if !x.committed() {
				t.Errorf("the lane's work is not committed")
			}
			if strings.Contains(x.out.String(), "removed") {
				t.Errorf("a live git's lock was removed:\n%s", x.out.String())
			}
		}},
		{"a stale lock with no git: removed once, logged", func(t *testing.T, x *fx, s Scratch, lock string) {
			s.GitAlive = func(string) bool { return false }
			if _, err := s.Submit(ctx, "T-05", "1", "L-01", "feature", "pj-l1"); err != nil {
				t.Fatalf("submit with a stale lock: %v", err)
			}
			if !x.committed() {
				t.Errorf("the lane's work is not committed")
			}
			if n := strings.Count(x.out.String(), "removed stale git lock "+lock); n != 1 {
				t.Errorf("stale lock removals logged %d times, want 1:\n%s", n, x.out.String())
			}
		}},
		{"a stale lock that comes back: removed once, then git's error", func(t *testing.T, x *fx, s Scratch, lock string) {
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			// Each git add finds a stale lock (a killed git's, again and again).
			s.Git = x.stub("git-relock", fmt.Sprintf(`if [ "$3" = add ]; then : > '%[1]s'; touch -t 200001010000 '%[1]s'; fi
exec '%[2]s' "$@"`, lock, real))
			s.GitAlive = func(string) bool { return false }
			_, err = s.Submit(ctx, "T-05", "1", "L-01", "feature", "pj-l1")
			if err == nil || !strings.Contains(err.Error(), "git add") || !strings.Contains(err.Error(), "Another git process") {
				t.Fatalf("submit: %v; want git add's lock error", err)
			}
			if n := strings.Count(x.out.String(), "removed stale git lock "); n != 1 {
				t.Errorf("stale lock removals logged %d times, want 1:\n%s", n, x.out.String())
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x, s, lock := lockFx(t)
			x.Write(lock, "")
			old := time.Now().Add(-2 * staleLock)
			if err := os.Chtimes(lock, old, old); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Fatalf("precondition: no lock: %v", err)
			}
			c.run(t, x, s, lock)
		})
	}
}

// gitAlive sees a git running in one of the repository's worktrees.
func TestGitAliveSeesGitInWorktree(t *testing.T) {
	x := newFx(t)
	cmd := exec.Command("git", "-C", x.lane, "cat-file", "--batch") // waits on its stdin
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); cmd.Wait() }()
	fmt.Fprintln(in, "HEAD")
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil { // it answered: it runs in the lane
		t.Fatalf("precondition: git cat-file did not answer: %v", err)
	}
	if !gitAlive(x.Root) {
		t.Errorf("gitAlive(%s) false with git cat-file running in lane %s", x.Root, filepath.Base(x.lane))
	}
}

// processCwd reads a running process's working directory: this test's own pid, physical (the survey tree covered its
// lsof path only when gitAlive reached a readable git; T-842 pins it on its own), and a pid that is not a number.
func TestProcessCwdReadsARunningProcess(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := processCwd(strconv.Itoa(os.Getpid()))
	if !ok {
		t.Fatal("precondition: processCwd cannot read this test's own working directory")
	}
	if want := project.Physical(wd); got != want {
		t.Errorf("processCwd(%d) = %q, want %q", os.Getpid(), got, want)
	}
	if got, ok := processCwd("not-a-pid"); ok || got != "" {
		t.Errorf(`processCwd("not-a-pid") = %q, %v; want "", false`, got, ok)
	}
}
