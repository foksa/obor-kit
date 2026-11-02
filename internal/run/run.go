// Package run is `obor run <name> [args…]`: commands.<name> from the root of the current lane or worktree, under its
// timeout, args appended (each a separate word), stdin /dev/null; a heavy command first takes a machine-wide check
// slot (Slots). In a lane, <lane>/.task-run holds the pid meanwhile (obor status: waiting on background work, not
// stalled; the lane root, not .obor/state, so a Codex sandbox can write it: proposal pain 7).
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"obor/internal/config"
	"obor/internal/project"
)

// Usage is the usage line.
const Usage = "usage: obor run <command-name> [args…]  (obor run <command-name> --help: its configured line, runs nothing)"

// MarkerName is the lane-root run marker: the pid of a live `obor run`.
const MarkerName = ".task-run"

// Options is one run. Interrupted is closed with the signal's exit code (130 INT, 143 TERM, 129 HUP) on a signal.
type Options struct {
	Cfg         *config.Config
	Env         project.Env
	Environ     []string // the command's environment (CHECK_SLOT_HELD is added for a heavy one)
	Dir         string   // the caller's cwd
	Name        string
	Args        []string
	Stdout      io.Writer // the command's output and obor's FAIL lines
	Stderr      io.Writer // wait lines, notes, the timeout line
	PID         int
	Interrupted <-chan int
	Slots       *Slots // nil: SlotsFor(Cfg, Env)
}

// Command is commands.<name> with obor run's defaults (timeout 600 s).
func Command(cfg *config.Config, name string) (config.Command, bool) {
	c, ok := cfg.Command(name)
	if ok && c.Timeout <= 0 {
		c.Timeout = 600
	}
	return c, ok
}

// Line is the command's configured line for --help.
func Line(c config.Command) string {
	line := fmt.Sprintf("commands.%s: %s  (timeout %d s", c.Name, c.Cmd, c.Timeout)
	if c.Heavy {
		line += ", heavy"
	}
	return line + ")"
}

// Run runs the command: its exit code (124 when it timed out, 130/143/129 on a signal); obor's own failures are a
// "FAIL …" line on Stdout and exit 2.
func Run(o Options) int {
	c, ok := Command(o.Cfg, o.Name)
	if !ok {
		fmt.Fprintf(o.Stdout, "FAIL no commands.%s in the config\n", o.Name)
		return 2
	}
	top, err := project.Git(o.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(o.Stdout, "FAIL not in a git worktree")
		return 2
	}
	top = project.Physical(strings.TrimSpace(top))

	lane := laneOf(o.Cfg, top)
	if lane != "" {
		marker := filepath.Join(top, MarkerName)
		// Not ignored, the marker dirties the lane while the command runs (status, the file guard, obor serve's gate; T-214).
		var ee *exec.ExitError
		if _, err := project.Git(top, "check-ignore", "-q", MarkerName); errors.As(err, &ee) && ee.ExitCode() == 1 {
			fmt.Fprintf(o.Stderr, "warning: %s is not in .gitignore — run obor setup (or add the line)\n", MarkerName)
		}
		if err := os.WriteFile(marker, []byte(strconv.Itoa(o.PID)+"\n"), 0o644); err != nil {
			fmt.Fprintf(o.Stderr, "note: cannot write %s: obor status may call this lane stalled while it runs\n", marker)
		} else {
			defer removeIfOurs(marker, o.PID)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var signal atomic.Int32 // the exit code of the signal that stopped the run
	go func() {
		select {
		case code := <-o.Interrupted:
			signal.Store(int32(code))
			cancel()
		case <-ctx.Done():
		}
	}()
	interrupted := func() int { return int(signal.Load()) }

	environ := o.Environ
	var receipt *laneRun
	var loaded float64 // the load the slot was claimed under after the load wait ran out (T-720)
	if c.Heavy {
		slots := o.Slots
		if slots == nil {
			s := SlotsFor(o.Cfg, o.Env)
			slots = &s
		}
		prio := "worker"
		if o.Env.Get("CHECK_PRIORITY") == "merge" {
			prio = "merge"
		}
		path, l, err := slots.Acquire(ctx, prio, o.PID, o.Stderr)
		loaded = l
		if err != nil {
			if ctx.Err() != nil {
				return interrupted()
			}
			fmt.Fprintf(o.Stdout, "FAIL %v\n", err)
			return 2
		}
		defer Release(path, o.PID)
		// The command runs under this slot: a slot-aware runner inside reuses it (T-121).
		environ = append(append([]string(nil), environ...), "CHECK_SLOT_HELD="+path, CPUsEnv(o.Cfg, o.Env))
		if lane != "" {
			receipt = o.beforeLaneRun(top, lane, &environ)
		}
	}

	t0 := time.Now()
	rc, timedOut := Execute(ctx, c, top, o.Args, environ, o.Stdout, o.Stderr)
	switch {
	case signal.Load() != 0:
		return interrupted()
	case timedOut:
		fmt.Fprintf(o.Stderr, "obor run: %s timed out after %d s%s\n", c.Name, c.Timeout, UnderLoad(loaded))
		return 124
	case loaded > 0:
		fmt.Fprintf(o.Stderr, "obor run: %s exited %d%s\n", c.Name, rc, UnderLoad(loaded))
	}
	if rc == 0 && receipt != nil && len(o.Args) == 0 {
		o.writeReceipt(receipt, c, time.Since(t0))
	}
	return rc
}

// laneRun is a lane's state before a heavy command: its name, where it runs and the tree it ran on.
type laneRun struct {
	name, top, tree string
}

// beforeLaneRun exports OBOR_CHANGED_FILES, the lane's changes against the merge base with the target (the main
// checkout's branch) in .obor/state/run-changed-<lane>, as the gate does for its own run. Without a tree or a base the
// command gets no list (its selector then runs everything) and no receipt is written.
func (o Options) beforeLaneRun(top, lane string, environ *[]string) *laneRun {
	tree, err := Tree(top, o.Environ)
	if err != nil {
		fmt.Fprintf(o.Stderr, "note: cannot compute the lane's tree (%v): no changed-file list, no receipt\n", err)
		return nil
	}
	state := project.StateDir(o.Cfg.Root)
	files, err := o.changedFiles(top, tree)
	path := filepath.Join(state, "run-changed-"+lane)
	if err == nil {
		err = os.MkdirAll(state, 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, []byte(strings.Join(append(files, ""), "\n")), 0o644)
	}
	if err != nil {
		fmt.Fprintf(o.Stderr, "note: cannot list the lane's changed files (%v): no changed-file list\n", err)
	} else {
		*environ = append(*environ, "OBOR_CHANGED_FILES="+path)
	}
	return &laneRun{name: lane, top: top, tree: tree}
}

// changedFiles is tree's files against the merge base of HEAD and the main checkout's branch.
func (o Options) changedFiles(top, tree string) ([]string, error) {
	target, err := project.Git(o.Cfg.Root, "branch", "--show-current")
	if err != nil || strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("the main checkout is not on a branch")
	}
	base, err := project.Git(top, "merge-base", strings.TrimSpace(target), "HEAD")
	if err != nil {
		return nil, fmt.Errorf("no merge base with %s", strings.TrimSpace(target))
	}
	return ChangedFiles(top, strings.TrimSpace(base), tree)
}

// writeReceipt records the green run, unless the lane's tree changed while it ran (then it proves nothing).
func (o Options) writeReceipt(l *laneRun, c config.Command, took time.Duration) {
	after, err := Tree(l.top, o.Environ)
	if err != nil || after != l.tree {
		return
	}
	if err := WriteReceipt(project.StateDir(o.Cfg.Root), l.tree, c, l.name, took, time.Now(),
		time.Duration(o.Cfg.Data.Merge.ReceiptMaxAge)*time.Second); err != nil {
		fmt.Fprintf(o.Stderr, "note: cannot write the test receipt: %v\n", err)
	}
}

// Execute runs the command with bash in its own process group (Group: TERM on a timeout or a signal, KILL after
// Grace, and nothing of the group outlives the return): its exit code, and whether it timed out. obor run and
// internal/checks use it.
func Execute(ctx context.Context, c config.Command, dir string, args, environ []string, stdout, stderr io.Writer) (int, bool) {
	tctx, cancel := context.WithTimeout(ctx, time.Duration(c.Timeout)*time.Second)
	defer cancel()
	script := c.Cmd
	if len(args) > 0 {
		script += ` "$@"`
	}
	cmd := exec.CommandContext(tctx, "bash", append([]string{"-c", script, "obor-run-" + c.Name}, args...)...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, environ, stdout, stderr
	cleanup := Group(cmd)
	err := cmd.Run()
	cleanup()
	timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return max(ee.ExitCode(), 1), timedOut
	case err != nil:
		fmt.Fprintf(stderr, "obor run: %v\n", err)
		return 127, timedOut
	}
	return 0, timedOut
}

// Grace is how long a process group has between TERM and KILL.
const Grace = 2 * time.Second

// Group makes cmd (from exec.CommandContext, not started) the leader of its own process group: when its context ends
// the group gets TERM, then KILL after Grace; Wait gives up on pipes a descendant still holds a second later. Call the
// returned cleanup after Wait (Run, Output) returns: it KILLs what is left of the group, so a TERM-ignoring child or
// a descendant of a parent that exited normally doesn't outlive the command (RV-26 #2).
func Group(cmd *exec.Cmd) (cleanup func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		syscall.Kill(-pgid, syscall.SIGTERM)
		time.AfterFunc(Grace, func() { syscall.Kill(-pgid, syscall.SIGKILL) })
		return nil
	}
	cmd.WaitDelay = Grace + time.Second
	return func() {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

// laneOf is the name of the configured lane whose worktree is top, else "".
func laneOf(cfg *config.Config, top string) string {
	for _, l := range cfg.Lanes() {
		if project.Physical(l.Dir) == top {
			return l.Name
		}
	}
	return ""
}

func removeIfOurs(path string, pid int) {
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(pid) {
		os.Remove(path)
	}
}
