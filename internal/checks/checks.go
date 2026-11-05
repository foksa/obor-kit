// Package checks is obor serve's merge gate over git (Scratch, scratch.go), the lane steps (lane.go) and `obor nightly`
// (design "Machine load (v2.1)", "Machine-wide check slots (v2.4)", "Change-based merge
// checks (v2.5)", "Check tiers (v2.6)"). Every command but lane_setup runs holding the machine-wide
// merge-running-<project> check slot (internal/run) with CHECK_PRIORITY=merge and CHECK_SLOT_HELD=<its path>. Each
// command's full output goes to .obor/state/logs/<log name>-<name>.log, the log name the task (after_merge's runs
// included) or nightly. A failing check names that log and its failing tests (merge.fail_grep matches) in
// the result line; a failed after_merge names it in its WARN line.
//
// nightly runs in its own worktree, <merge.scratch_dir>-nightly, one at a time per project
// (.obor/state/nightly.flock). Result line (printed last) and exit code: NIGHTLY GREEN 0 | RED 3 | FAIL 1. A signal
// (INT, TERM, HUP) ends a run with 130, 143, 129.
package checks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

// Exit codes of the result lines.
const (
	OK   = 0
	Fail = 1
	Red  = 3
)

// Options is what a run needs from its caller.
type Options struct {
	Cfg         *config.Config
	Env         project.Env // OBOR_GATE, OBOR_FULL, the slot settings
	Environ     []string    // the base environment of git and the commands
	Out         io.Writer   // progress and detail lines (the result line is the caller's to print)
	PID         int         // this process (lock, slot and op record holder)
	Interrupted <-chan int  // a signal's exit code (130, 143, 129)
	Slots       *run.Slots  // nil: run.SlotsFor(Cfg, Env)
	// Load is the 1-min load average (ok false: unreadable); nil reads the system's. It goes with a red run into the
	// flake log.
	Load func() (float64, bool)
	// WindowWait is how long the nightly waits for another nightly's lock before it FAILs (default
	// DefaultWindowWait); a test shortens it.
	WindowWait time.Duration
	// Git is the git binary ("": git on PATH); a test puts a shim here that fails a chosen call.
	Git string
	// GitAlive reports whether a git process runs in dir's repository (a lock it holds is not stale); nil: the
	// system's (ps, the processes' cwd). Serve's git calls only (gitLocked).
	GitAlive func(dir string) bool
	// gitCtx ends the git calls: one running is killed with its process group (project.Group), a later one fails (nil:
	// never). A session sets its own, Scratch the one each method is given.
	gitCtx context.Context
	// callCtx ends the run's socket calls, the nightly's backup (nil: the run's own). A test shortens a nightly with no
	// serve, so its backup goes on at once instead of waiting out the deadline table (T-685).
	callCtx context.Context
}

// Result is the result line and exit code.
type Result struct {
	Code int
	Line string
}

func result(code int, format string, a ...any) *Result {
	return &Result{Code: code, Line: fmt.Sprintf(format, a...)}
}

// session is one nightly run: its lock, its worktree, and the check runner in it.
type session struct {
	Options
	runner
	cancel  context.CancelFunc
	signal  int // the signal's exit code once interrupted
	sigDone chan struct{}
	target  string // the main checkout's current branch
	scratch string // <merge.scratch_dir>-nightly
	window  *project.Lock
	locked  bool
	start   time.Time
}

// open starts a session: the target branch and the worktree path; a signal on Interrupted or the end of ctx cancels
// its context.
func open(ctx context.Context, o Options, logName string) (*session, *Result) {
	s := &session{Options: o, start: time.Now()}
	s.scratch = o.Cfg.ScratchDir() + "-nightly"
	ctx, s.cancel = context.WithCancel(ctx)
	s.Options.gitCtx = ctx // a signal kills a running git too
	s.runner = newRunner(ctx, &s.Options, s.scratch, logName, "flakes-changed-files")
	s.sigDone = make(chan struct{})
	s.sigCode = func() int { <-s.sigDone; return s.signal }
	go func() {
		defer close(s.sigDone)
		select {
		case code := <-o.Interrupted:
			s.signal = code
			s.cancel()
		case <-s.ctx.Done():
		}
	}()
	s.window = project.NewLock(filepath.Join(s.state, "nightly.flock"))
	s.window.Output = io.Discard
	branch, err := s.git(s.root, "branch", "--show-current")
	if err != nil || branch == "" {
		s.close()
		return nil, result(Fail, "FAIL the main checkout %s is not on a branch", s.root)
	}
	s.target = branch
	return s, nil
}

// close releases the slot and the lock.
func (s *session) close() {
	s.releaseSlot()
	if s.locked {
		s.window.Unlock()
		s.locked = false
	}
	s.cancel()
	<-s.sigDone
}

// git runs git -C dir args… with the run's environment (OBOR_MERGE=1: our own commits pass the obor pre-commit
// hook): stdout without the trailing newline.
func (o *Options) git(dir string, args ...string) (string, error) {
	out, _, err := o.gitOut(dir, args...)
	return out, err
}

func (o *Options) gitOut(dir string, args ...string) (string, string, error) {
	return o.gitEnv(nil, dir, args...)
}

// gitEnv is gitOut with env added to the environment. git runs in its own process group (hooks, filters and signing
// helpers with it), killed when o.gitCtx ends.
func (o *Options) gitEnv(env []string, dir string, args ...string) (string, string, error) {
	bin := o.Git
	if bin == "" {
		bin = "git"
	}
	ctx := o.gitCtx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(append([]string(nil), o.Environ...), "OBOR_MERGE=1"), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cleanup := project.Group(cmd)
	err := cmd.Run()
	cleanup()
	return strings.TrimRight(stdout.String(), "\n"), strings.TrimRight(stderr.String(), "\n"), err
}

// ok runs git and reports whether it exited 0.
func (o *Options) ok(dir string, args ...string) bool {
	_, err := o.git(dir, args...)
	return err == nil
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, x := range ss {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// DefaultWindowWait is how long a nightly waits for another nightly's lock (merge.window_wait's old default, T-864).
const DefaultWindowWait = 900 * time.Second

// lock takes the nightly lock (nightly.flock) until close. A live holder: a line every 30 s, FAIL after WindowWait.
func (s *session) lock(who string) *Result {
	wait := s.WindowWait
	if wait <= 0 {
		wait = DefaultWindowWait
	}
	if err := os.MkdirAll(s.state, 0o755); err != nil {
		return result(Fail, "FAIL cannot take %s", s.window.Path)
	}
	t0 := time.Now()
	next := time.Duration(0)
	for {
		ok, err := s.window.Try()
		if err != nil {
			return result(Fail, "FAIL cannot take %s", s.window.Path)
		}
		if ok {
			s.locked = true
			return nil
		}
		info := "pid " + s.window.Holder()
		waited := time.Since(t0)
		if next > 0 && waited >= wait {
			return result(Fail, "FAIL %s still held after %ds (%s)", s.window.Path, int(waited.Seconds()), info)
		}
		if waited >= next {
			fmt.Fprintf(s.Out, "%s: waiting for %s (%s), %ds of max %ds\n", who, s.window.Path, info, int(waited.Seconds()), int(wait.Seconds()))
			next += 30 * time.Second
		}
		select {
		case <-s.ctx.Done():
			return result(s.interrupted(), "interrupted while waiting for %s", s.window.Path)
		case <-time.After(time.Second):
		}
	}
}

// reset puts the scratch at rev without command artifacts (ignored files, such as lane links, stay).
func (s *session) reset(rev string) bool {
	return s.ok(s.scratch, "reset", "-q", "--hard", rev) && s.ok(s.scratch, "clean", "-fdq")
}

// linkLaneLinks symlinks each lane_links entry from root into dir (missing ones only).
func linkLaneLinks(cfg *config.Config, root, dir string) {
	for _, l := range cfg.Data.LaneLinks {
		src, dst := filepath.Join(root, l), filepath.Join(dir, l)
		if _, err := os.Stat(src); l == "" || err != nil {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		os.Symlink(src, dst)
	}
}

func secs(d time.Duration) int { return int(d / time.Second) }
