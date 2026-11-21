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
	"time"

	"obor/internal/config"
	"obor/internal/project"
)

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
	// Git runs git -C dir args… under ctx with env as its whole environment (nil: the process's) for every git read of
	// the run (the worktree, the run marker's ignore rule, the changed-file list's tree and base): nil is
	// project.GitCtxEnv, the real git. A test of what the run writes or refuses sets a fake.
	Git GitFunc
}

// GitFunc is a git command: project.GitCtxEnv's shape.
type GitFunc func(ctx context.Context, dir string, env []string, args ...string) (string, error)

// git is the run's git: o.Git, else the real one.
func (o Options) git() GitFunc {
	if o.Git != nil {
		return o.Git
	}
	return project.GitCtxEnv
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
	top, err := o.git()(context.Background(), o.Dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(o.Stdout, "FAIL not in a git worktree")
		return 2
	}
	top = project.Physical(strings.TrimSpace(top))

	lane := laneOf(o.Cfg, top)
	if lane != "" {
		marker := filepath.Join(top, MarkerName)
		// Not ignored, the marker dirties the lane while the command runs (status, the file guard, obor serve's gate).
		var ee *exec.ExitError
		if _, err := o.git()(context.Background(), top, nil, "check-ignore", "-q", MarkerName); errors.As(err, &ee) && ee.ExitCode() == 1 {
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
		path, err := slots.Acquire(ctx, prio, o.PID, o.Stderr)
		if err != nil {
			if ctx.Err() != nil {
				return interrupted()
			}
			fmt.Fprintf(o.Stdout, "FAIL %v\n", err)
			return 2
		}
		defer Release(path, o.PID)
		// The command runs under this slot: a slot-aware runner inside reuses it.
		environ = append(append([]string(nil), environ...), "CHECK_SLOT_HELD="+path, CPUsEnv(o.Cfg, o.Env))
		if lane != "" {
			o.exportChangedFiles(ctx, top, lane, &environ)
		}
	}

	rc, timedOut := Execute(ctx, c, top, o.Args, environ, o.Stdout, o.Stderr)
	switch {
	case signal.Load() != 0:
		return interrupted()
	case timedOut:
		fmt.Fprintf(o.Stderr, "obor run: %s timed out after %d s\n", c.Name, c.Timeout)
		return 124
	}
	return rc
}

// exportChangedFiles exports OBOR_CHANGED_FILES, the lane's changes against the merge base with merge.target_branch
// in .obor/state/run-changed-<lane>, as the gate does for its own run. Without a tree or a base the command gets no
// list (its selector then runs everything).
func (o Options) exportChangedFiles(ctx context.Context, top, lane string, environ *[]string) {
	tree, err := tree(ctx, o.git(), top, o.Environ)
	if err != nil {
		fmt.Fprintf(o.Stderr, "note: cannot compute the lane's tree (%v): no changed-file list\n", err)
		return
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
}

// changedFiles is tree's files against the merge base of HEAD and merge.target_branch, whatever the main checkout has
// checked out.
func (o Options) changedFiles(top, tree string) ([]string, error) {
	git, ctx := o.git(), context.Background() // no ctx: as before, a signal does not end these reads

	target := o.Cfg.MainBranch()
	base, err := git(ctx, top, nil, "merge-base", target, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("no merge base with %s", target)
	}
	return changedFiles(ctx, git, top, strings.TrimSpace(base), tree)
}

// tree is the content tree of the work in dir (a worktree): HEAD plus every tracked and untracked change, as serve's
// submit commits a lane (add -A), the run marker excluded. It is computed with a temporary index (its own git, under
// the caller's ctx: a canceled run's Tree ends with it), so dir's index and history are not touched.
func tree(ctx context.Context, git GitFunc, dir string, environ []string) (string, error) {
	tmp, err := os.MkdirTemp("", "obor-tree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if environ == nil {
		environ = os.Environ()
	}
	env := append(append([]string(nil), environ...), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
	run := func(args ...string) (string, error) {
		out, err := git(ctx, dir, env, args...)
		return strings.TrimSpace(out), err
	}
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"},
		{"reset", "-q", "--", MarkerName}} {
		if _, err := run(args...); err != nil {
			return "", fmt.Errorf("git %s in %s: %w", args[0], dir, err)
		}
	}
	return run("write-tree")
}

// changedFiles is the files a tree changes against base (names only, a rename as a deletion and an addition: the
// merge's own listing).
func changedFiles(ctx context.Context, git GitFunc, dir, base, tree string) ([]string, error) {
	out, err := git(ctx, dir, nil, "diff", "--no-renames", "--name-only", base, tree)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// Execute runs the command with bash in its own process group (project.Group: TERM on a timeout or a signal, KILL
// after project.Grace, and nothing of the group outlives the return): its exit code, and whether it timed out. obor run
// and internal/checks use it.
func Execute(ctx context.Context, c config.Command, dir string, args, environ []string, stdout, stderr io.Writer) (int, bool) {
	tctx, cancel := context.WithTimeout(ctx, time.Duration(c.Timeout)*time.Second)
	defer cancel()
	script := c.Cmd
	if len(args) > 0 {
		script += ` "$@"`
	}
	cmd := exec.CommandContext(tctx, "bash", append([]string{"-c", script, "obor-run-" + c.Name}, args...)...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, environ, stdout, stderr
	cleanup := project.Group(cmd)
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
