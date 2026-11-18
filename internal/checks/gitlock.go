package checks

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/project"
)

// Git lock contention at obor's git calls (docs/state-model.md "Crash classes": the scratch's and lane.prepare's): a
// git call that cannot take a lock (index.lock, a ref's .lock) is tried again, not failed, while the lock comes and
// goes; a lock older than staleLock with no git process running in the repository's worktrees is stale (a killed git
// left it) and removed once, logged. Past lockWait of trying the call's error is the step's (counted). Every git
// command obor runs carries GIT_OPTIONAL_LOCKS=0 (project.GitEnv), so a read never takes index.lock; a write's locks
// are as always.
const (
	staleLock = 60 * time.Second
	lockWait  = 60 * time.Second
	lockPoll  = 50 * time.Millisecond
)

// lockRe finds the lock git could not take: "Unable to create '<path>.lock': File exists."
var lockRe = regexp.MustCompile(`Unable to create '([^']+\.lock)': File exists`)

// gitCmd runs git with env in dir: its stdout, stderr and error.
type gitCmd func(env []string, dir string, args ...string) (string, string, error)

// gitLocked runs git, again while it fails on a lock (the rule above): the last call's stdout, stderr and error. out
// gets a removal's line (nil: none); alive reports whether a git process runs in dir's repository (nil: gitAlive);
// ctx ends the wait.
func gitLocked(ctx context.Context, out io.Writer, alive func(string) bool, git gitCmd, env []string, dir string,
	args ...string) (string, string, error) {
	deadline := time.Now().Add(lockWait)
	removed := false
	for {
		stdout, stderr, err := git(env, dir, args...)
		m := lockRe.FindStringSubmatch(stderr)
		if err == nil || m == nil {
			return stdout, stderr, err
		}
		if !lockGone(ctx, out, alive, dir, m[1], deadline, &removed) {
			return stdout, stderr, err
		}
	}
}

// gitLocked is gitEnv (Scratch's git), run again while it fails on a lock (the rule above).
func (s Scratch) gitLocked(env []string, dir string, args ...string) (string, string, error) {
	return gitLocked(s.gitCtx, s.Out, s.GitAlive, s.gitEnv, env, dir, args...)
}

// Locked runs git -C dir args… under ctx as serve's own git calls do (gitLocked: the review git's worktree
// move): its stdout; an error names the call and git's last stderr line.
func (s Scratch) Locked(ctx context.Context, dir string, args ...string) (string, error) {
	s.gitCtx = ctx
	return s.call(dir, args...)
}

// lockGone waits until lock is gone (true: try again): removed when stale (no git running, older than staleLock), at
// most once per call (*removed). False: the deadline or ctx came first, or a removed lock is back and stale.
func lockGone(ctx context.Context, out io.Writer, alive func(string) bool, dir, lock string, deadline time.Time,
	removed *bool) bool {
	if alive == nil {
		alive = gitAlive
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	for {
		fi, err := os.Stat(lock)
		if err != nil {
			return true
		}
		if age := time.Since(fi.ModTime()); age >= staleLock && !alive(dir) {
			if *removed || os.Remove(lock) != nil {
				return false
			}
			*removed = true
			if out != nil {
				fmt.Fprintf(out, "removed stale git lock %s (%s old, no git running)\n", lock, age.Round(time.Second))
			}
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-done:
			return false
		case <-time.After(lockPoll):
		}
	}
}

// gitAlive reports whether a git process runs in one of the worktrees of dir's repository (its cwd inside one); a git
// whose cwd cannot be read counts as running, as does any doubt (ps or the worktree list unreadable).
func gitAlive(dir string) bool {
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return true
	}
	var pids []string
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) == 2 && filepath.Base(f[1]) == "git" {
			pids = append(pids, f[0])
		}
	}
	if len(pids) == 0 {
		return false
	}
	trees, err := worktrees(dir)
	if err != nil {
		return true
	}
	for _, pid := range pids {
		cwd, ok := processCwd(pid)
		if !ok {
			return true
		}
		for _, t := range trees {
			if cwd == t || strings.HasPrefix(cwd, t+"/") {
				return true
			}
		}
	}
	return false
}

// worktrees is dir's repository's worktree dirs, physical paths (git worktree list takes no lock).
func worktrees(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain")
	cmd.Env = project.GitEnv(nil)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var trees []string
	for _, l := range strings.Split(string(out), "\n") {
		if t, ok := strings.CutPrefix(l, "worktree "); ok {
			trees = append(trees, project.Physical(t))
		}
	}
	return trees, nil
}

// processCwd is pid's working directory, physical: /proc where there is one, else lsof (macOS).
func processCwd(pid string) (string, bool) {
	if cwd, err := os.Readlink("/proc/" + pid + "/cwd"); err == nil {
		return project.Physical(cwd), true
	}
	if _, err := strconv.Atoi(pid); err != nil {
		return "", false
	}
	out, err := exec.Command("lsof", "-a", "-p", pid, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", false
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if n, ok := strings.CutPrefix(sc.Text(), "n"); ok {
			return project.Physical(n), true
		}
	}
	return "", false
}
