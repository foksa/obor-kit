package checks

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lockStderr is git's stderr for a lock it could not take, as git writes it: the lock's line, a blank line, its hint.
func lockStderr(lock string) string {
	return fmt.Sprintf("fatal: Unable to create '%s': File exists.\n\nAnother git process seems to be running in "+
		"this repository, or the lock file may be stale\n", lock)
}

// exited is a git-like failure with an exit code, as gitRaw wraps it.
func exited(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("exit %d: %v", code, err)
	}
	return err
}

// lockHint names the lock a lane git call could not take past the wait (gitLocked), and nothing for another failure.
// git's stderr for a lock is its hint under the lock's line, so the line such an error carries is the lock's
// (stderrLine), the one the hint reads the path from.
func TestLockHint(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dir := filepath.Join(base, "proj-wt", "L-01")
	lock := filepath.Join(base, "proj", ".git", "worktrees", "L-01", "index.lock")
	stderr := lockStderr(lock)
	line, _, _ := strings.Cut(stderr, "\n")
	if got := stderrLine(lines(stderr)); got != line {
		t.Errorf("stderrLine(git's lock stderr) = %q, want the lock's line %q (its last line names no path)", got, line)
	}
	locked := fmt.Errorf("checkout: %w", fmt.Errorf("%w: %s", exited(t, 128), line))
	if got, want := lockHint(dir, locked), " — still locked after the wait: "+lock; got != want {
		t.Errorf("lockHint on a lock = %q, want %q", got, want)
	}
	for _, err := range []error{
		fmt.Errorf("checkout: %w: %s", exited(t, 1), line),
		errors.New("fatal: not a git repository"),
	} {
		if got := lockHint(dir, err); got != "" {
			t.Errorf("lockHint(%v) = %q, want none", err, got)
		}
	}
}
