package checks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
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

// TestLanePrepare: a fresh prepare puts the branch at the target; base is the target (serve's lane row), and it writes
// no lane state file (serve's row replaces it). A redo keeps the lane's commits (no checkout).
func TestLanePrepare(t *testing.T) {
	t.Parallel()
	k := newLk(t)
	if k.g.headOf(k.lane) == k.target {
		t.Fatal("precondition: the lane is behind the target")
	}
	got, err := k.l.Prepare(context.Background(), laneInput(k.lane, k.target))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if want := (port.Prepared{Base: k.target}); got != want {
		t.Errorf("prepared %+v, want %+v", got, want)
	}
	if got, want := k.writes(), "NewBranch "+k.lane+" task/T-01 "+k.target; got != want {
		t.Errorf("writes %q, want %q", got, want)
	}
	if got := k.g.headOf(k.lane); got != k.target {
		t.Errorf("lane HEAD = %s, want the target %s", got, k.target)
	}
	if got := k.g.branch[k.lane]; got != "task/T-01" {
		t.Errorf("lane branch = %q, want task/T-01", got)
	}
	if k.Exists(".obor/state/lanes/L-01.json") {
		t.Error("prepare wrote a lane state file (serve's row replaces it)")
	}

	work := k.commit()
	k.g.calls = nil
	in := laneInput(k.lane, k.target)
	in.Redo = true
	got, err = k.l.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("redo prepare: %v", err)
	}
	if w := k.writes(); w != "" {
		t.Errorf("redo wrote %q, want nothing", w)
	}
	if got := k.g.headOf(k.lane); got != work {
		t.Errorf("redo moved the lane HEAD: %s, want its commit %s", got, work)
	}
	if got.Base != k.target {
		t.Errorf("redo prepared %+v, want base the target %q", got, k.target)
	}
}

// TestLanePrepareRefused: no lane dir, lane_setup red, lane_ready still red and lane_ready red without lane_setup are
// final refusals (not retries).
func TestLanePrepareRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		edits []func(map[string]any)
		noDir bool
		want  string
	}{
		{"no lane dir", nil, true, "no lane dir"},
		{"lane_setup red", []func(map[string]any){
			testkit.Set("lanes.ready", "test -f ready"),
			testkit.Set("lanes.setup", "echo install broke; exit 3")}, false,
			"lane_setup in L-01: rc 3: install broke"},
		{"lane_ready still red", []func(map[string]any){
			testkit.Set("lanes.ready", "test -f ready"),
			testkit.Set("lanes.setup", "true")}, false,
			"lane_setup in L-01: ran, but lanes.ready still fails"},
		{"lane_ready red, no lane_setup", []func(map[string]any){
			testkit.Set("lanes.ready", "false")}, false,
			"lanes.setup is not set"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newLk(t, c.edits...)
			in := laneInput(k.lane, k.target)
			if c.noDir {
				in.Dir = filepath.Join(k.Base, "no-such-lane")
			}
			_, err := k.l.Prepare(context.Background(), in)
			var refused port.Refused
			if !errors.As(err, &refused) {
				t.Fatalf("prepare: %v; want an port.Refused", err)
			}
			if !strings.Contains(refused.Line, c.want) {
				t.Errorf("refusal %q lacks %q", refused.Line, c.want)
			}
		})
	}
}

// TestLanePrepareRepeats: a failure at the checkout git call is a plain error (retry), and preparing again converges on
// the same result as a clean prepare.
func TestLanePrepareRepeats(t *testing.T) {
	t.Parallel()
	k := newLk(t)
	in := laneInput(k.lane, k.target)
	k.g.failing["NewBranch"] = errors.New("checkout failed")
	if _, err := k.l.Prepare(context.Background(), in); err == nil {
		t.Fatal("prepare with a failing checkout: no error")
	} else {
		var refused port.Refused
		if errors.As(err, &refused) {
			t.Errorf("git failure is a refusal %q, want a plain error to retry", refused.Line)
		}
	}
	if got := k.g.headOf(k.lane); got == k.target {
		t.Fatal("precondition: the failed checkout left the lane behind the target")
	}
	delete(k.g.failing, "NewBranch")
	first, err := k.l.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	second, err := k.l.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("second repeat: %v", err)
	}
	if want := (port.Prepared{Base: k.target}); first != want || second != want {
		t.Errorf("repeats prepared %+v, %+v; want %+v", first, second, want)
	}
	if h, b := k.g.headOf(k.lane), k.g.branch[k.lane]; h != k.target || b != "task/T-01" {
		t.Errorf("lane on %q at %s after the repeats, want task/T-01 at the target %s", b, h, k.target)
	}
}

// TestLaneFree (obor drop): a dirty lane and a branch with commits not in the target are refused, nothing
// changed; --discard drops both; then the lane is detached at the target and the branch deleted, and freeing again
// converges.
func TestLaneFree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name           string
		dirty, commit  bool
		discard        bool
		refusal        string // "": freed
		discardedLines []string
	}{
		{name: "clean"},
		{name: "dirty", dirty: true, refusal: "lane L-01 is dirty"},
		{name: "commits", commit: true, refusal: "task/T-01 has commits not in the target"},
		{name: "dirty, discard", dirty: true, discard: true, discardedLines: []string{"discarded in L-01:", "scratch.txt"}},
		{name: "commits, discard", commit: true, discard: true,
			discardedLines: []string{"discarded commits on task/T-01:", "work in L-01"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newLk(t)
			k.prepare()
			if c.commit {
				k.commit()
			}
			if c.dirty {
				k.dirty()
			}
			k.g.calls = nil
			line, err := k.free(c.discard)
			if c.refusal != "" {
				var refused port.Refused
				if !errors.As(err, &refused) || !strings.Contains(refused.Line, c.refusal) {
					t.Fatalf("free: %v; want refused with %q", err, c.refusal)
				}
				if w := k.writes(); w != "" {
					t.Errorf("a refused free wrote %q", w)
				}
				if b := k.g.branch[k.lane]; b != "task/T-01" {
					t.Errorf("a refused free moved the lane to %q", b)
				}
				return
			}
			if err != nil {
				t.Fatalf("free: %v", err)
			}
			for _, want := range c.discardedLines {
				if !strings.Contains(line, want) {
					t.Errorf("line %q lacks %q", line, want)
				}
			}
			if len(c.discardedLines) == 0 && line != "" {
				t.Errorf("a clean free says %q", line)
			}
			if b := k.g.branch[k.lane]; b != "" {
				t.Errorf("lane on %q, want detached", b)
			}
			if h := k.g.headOf(k.lane); h != k.target {
				t.Errorf("lane HEAD %s, want the target %s", h, k.target)
			}
			if st := k.g.status[k.lane]; len(st) != 0 {
				t.Errorf("lane not clean after free: %q", st)
			}
			if _, ok := k.g.refs[branchRef]; ok {
				t.Error("task branch kept")
			}
			if line, err := k.free(c.discard); err != nil || line != "" {
				t.Errorf("free again: %q, %v; want nothing to do", line, err)
			}
		})
	}
}

// A failed history read is unknown, never proof that a branch is merged: a failing branch read, ancestry check or
// unmerged-commits read is a retryable error that detaches and deletes nothing, even with --discard; a healthy retry
// still needs --discard for the unmerged work.
func TestLaneFreeHistoryFailurePreservesBranch(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"Verify", "Ancestor", "Ahead"} {
		for _, discard := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s discard %v", method, discard), func(t *testing.T) {
				t.Parallel()
				k := newLk(t)
				head := k.commit()
				k.g.failing[method] = errors.New(method + " failed")
				_, err := k.free(discard)
				var refused port.Refused
				if err == nil || errors.As(err, &refused) {
					t.Fatalf("history failure: %v; want a retryable error", err)
				}
				if !strings.Contains(err.Error(), method+" failed") {
					t.Fatalf("precondition: the error %q is the failing %s's", err, method)
				}
				if w := k.writes(); w != "" {
					t.Errorf("failed free wrote %q", w)
				}
				if b := k.g.branch[k.lane]; b != "task/T-01" {
					t.Errorf("failed free moved lane to %q", b)
				}
				if got := k.g.refs[branchRef]; got != head {
					t.Errorf("failed free lost branch head %q, want %q", got, head)
				}
				delete(k.g.failing, method)
				_, err = k.free(discard)
				if discard && err != nil || !discard && !errors.As(err, &refused) {
					t.Errorf("retry: %v; want refusal without --discard, success with it", err)
				}
			})
		}
	}
}

// a drop whose branch delete fails after the lane was detached says the same drop finishes it, and it does:
// the lane is freed all the same (detached at the target), the branch kept, and a free without the failing git deletes
// it. Nothing before the detach carries the hint (that failure freed no lane).
func TestLaneFreeBranchDeleteFailsAfterTheLaneIsFreed(t *testing.T) {
	t.Parallel()
	const hint = " — run the same obor drop again to finish it"
	k := newLk(t)
	k.prepare()
	if b := k.g.branch[k.lane]; b != "task/T-01" {
		t.Fatalf("precondition: the lane is on %q, want task/T-01", b)
	}
	k.g.failing["Detach"] = errors.New("detach failed")
	if _, err := k.free(false); err == nil || strings.Contains(err.Error(), hint) {
		t.Errorf("detach failure: %v; want an error without the retry hint", err)
	}
	if b := k.g.branch[k.lane]; b != "task/T-01" {
		t.Errorf("the failed detach left the lane on %q, want task/T-01", b)
	}
	delete(k.g.failing, "Detach")

	k.g.failing["DeleteBranch"] = errors.New("delete failed")
	_, err := k.free(false)
	if err == nil || !strings.Contains(err.Error(), "git branch -D task/T-01") || !strings.HasSuffix(err.Error(), hint) {
		t.Fatalf("branch delete failure: %v; want the retry hint", err)
	}
	if b := k.g.branch[k.lane]; b != "" {
		t.Errorf("the failed free left the lane on %q, want detached (freed)", b)
	}
	if h := k.g.headOf(k.lane); h != k.target {
		t.Errorf("lane HEAD %s, want the target %s", h, k.target)
	}
	if _, ok := k.g.refs[branchRef]; !ok {
		t.Fatal("precondition: the failed delete kept task/T-01")
	}
	delete(k.g.failing, "DeleteBranch")
	if _, err := k.free(false); err != nil {
		t.Fatalf("the same free again: %v", err)
	}
	if _, ok := k.g.refs[branchRef]; ok {
		t.Error("the second free kept the task branch")
	}
}
