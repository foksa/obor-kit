package checks

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// lockHint names the lock a lane git call could not take past the wait (gitLocked), and nothing for another failure.
func TestLockHint(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dir := filepath.Join(base, "proj-wt", "L-01")
	lock := filepath.Join(base, "proj", ".git", "worktrees", "L-01", "index.lock")
	exited := func(code int) error {
		t.Helper()
		err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != code {
			t.Fatalf("exit %d: %v", code, err)
		}
		return err
	}
	locked := fmt.Errorf("checkout: %w", fmt.Errorf("%w: fatal: Unable to create '%s': File exists", exited(128), lock))
	if got, want := lockHint(dir, locked), " — still locked after the wait: "+lock; got != want {
		t.Errorf("lockHint on a lock = %q, want %q", got, want)
	}
	for _, err := range []error{
		fmt.Errorf("checkout: %w: fatal: Unable to create '%s': File exists", exited(1), lock),
		errors.New("fatal: not a git repository"),
	} {
		if got := lockHint(dir, err); got != "" {
			t.Errorf("lockHint(%v) = %q, want none", err, got)
		}
	}
}
