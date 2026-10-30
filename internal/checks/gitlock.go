package checks

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/project"
)

// Git lock contention at serve's git effect boundary (docs/state-model.md "Crash classes"): a git call that cannot
// take a lock (index.lock, a ref's .lock) is tried again, not failed, while the lock comes and goes; a lock older
// than staleLock with no git process running in the repository's worktrees is stale (a killed git left it) and
// removed once, logged. Past lockWait of trying the call's error is the step's (counted).
const (
	staleLock = 60 * time.Second
	lockWait  = 60 * time.Second
	lockPoll  = 50 * time.Millisecond
)

// lockRe finds the lock git could not take: "Unable to create '<path>.lock': File exists."
var lockRe = regexp.MustCompile(`Unable to create '([^']+\.lock)': File exists`)

// gitLocked is gitEnv, run again while it fails on a lock (the rule above).
func (s Scratch) gitLocked(env []string, dir string, args ...string) (string, string, error) {
	deadline := time.Now().Add(lockWait)
	removed := false
	for {
		out, errOut, err := s.gitEnv(env, dir, args...)
		m := lockRe.FindStringSubmatch(errOut)
		if err == nil || m == nil {
			return out, errOut, err
		}
		if !s.lockGone(dir, m[1], deadline, &removed) {
			return out, errOut, err
		}
	}
}

// lockGone waits until lock is gone (true: try again): removed when stale (no git running, older than staleLock), at
// most once per call (*removed). False: the deadline or ctx came first, or a removed lock is back and stale.
func (s Scratch) lockGone(dir, lock string, deadline time.Time, removed *bool) bool {
	alive := s.GitAlive
	if alive == nil {
		alive = gitAlive
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
			if s.Out != nil {
				fmt.Fprintf(s.Out, "removed stale git lock %s (%s old, no git running)\n", lock, age.Round(time.Second))
			}
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-s.ctxDone():
			return false
		case <-time.After(lockPoll):
		}
	}
}

func (o *Options) ctxDone() <-chan struct{} {
	if o.gitCtx == nil {
		return nil
	}
	return o.gitCtx.Done()
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
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
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
