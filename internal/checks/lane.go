package checks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	herdrun "obor/internal/run"
)

// Lane is port.LaneSetup for one project: the lane.prepare step of a dispatched task
// and the lane a drop gives back. Its git is real, run in the lane's worktree with the caller's ctx.
type Lane struct {
	Cfg *config.Config
	Out io.Writer // progress lines (nil: none)
	Git string    // the git binary ("" : git on PATH); a test puts a shim here that fails a chosen call
}

var _ port.LaneSetup = Lane{}

// Prepare puts in.Dir on in.Branch at in.Target (in.Redo: the branch is kept as it is) and runs lane_ready (then
// lane_setup and lane_ready again when it was red). It returns the target sha the branch is based on and the lane's
// head (merge-base(HEAD, target)): serve's lane row (T-439). Repeating it converges on the same state.
// A final refusal: no lane dir, lane_setup red, lane_ready still red; git failures are plain errors (retry).
func (l Lane) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if !project.IsDir(in.Dir) {
		return port.Prepared{}, port.Refused{Line: "no lane dir " + in.Dir}
	}
	if !in.Redo {
		if _, err := l.git(ctx, in.Dir, "checkout", "-q", "-B", in.Branch, in.Target); err != nil {
			return port.Prepared{}, fmt.Errorf("checkout -B %s %s in %s: %w", in.Branch, in.Target, in.Dir, err)
		}
	}
	if err := LaneSetup(l.Cfg, in.Lane, in.Dir, l.Out); err != nil {
		return port.Prepared{}, port.Refused{Line: err.Error()}
	}
	head, err := l.git(ctx, in.Dir, "merge-base", "HEAD", in.Target)
	if err != nil {
		return port.Prepared{}, fmt.Errorf("merge-base HEAD %s in %s: %w", in.Target, in.Dir, err)
	}
	return port.Prepared{Base: in.Target, Head: head}, nil
}

// Free gives a dropped task's lane back (obor drop, T-439). Refused, nothing changed: no lane dir; uncommitted
// changes or commits on in.Branch not in in.Target, unless in.Discard (then git reset --hard and git clean -fd, the
// commits left with the deleted branch). Then the lane is detached at in.Target and in.Branch deleted (a branch that
// is gone already is fine). Its line lists what it discarded ("" nothing).
func (l Lane) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if !project.IsDir(in.Dir) {
		return "", port.Refused{Line: "no lane dir " + in.Dir}
	}
	status, err := l.git(ctx, in.Dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", in.Dir, err)
	}
	_, noBranch := l.git(ctx, in.Dir, "rev-parse", "-q", "--verify", "refs/heads/"+in.Branch)
	var commits string
	if noBranch == nil {
		if _, err := l.git(ctx, in.Dir, "merge-base", "--is-ancestor", "refs/heads/"+in.Branch, in.Target); err != nil {
			commits, _ = l.git(ctx, in.Dir, "log", "--oneline", in.Target+"..refs/heads/"+in.Branch)
		}
	}
	switch {
	case status != "" && !in.Discard:
		return "", port.Refused{Line: fmt.Sprintf("lane %s is dirty (--discard to drop its uncommitted changes):\n%s",
			in.Lane, status)}
	case commits != "" && !in.Discard:
		return "", port.Refused{Line: fmt.Sprintf("%s has commits not in the target (--discard to drop them):\n%s",
			in.Branch, commits)}
	}
	var lines []string
	if status != "" {
		if _, err := l.git(ctx, in.Dir, "reset", "-q", "--hard"); err != nil {
			return "", fmt.Errorf("git reset --hard in %s: %w", in.Dir, err)
		}
		if _, err := l.git(ctx, in.Dir, "clean", "-q", "-fd"); err != nil {
			return "", fmt.Errorf("git clean -fd in %s: %w", in.Dir, err)
		}
		lines = append(lines, "discarded in "+in.Lane+":\n  "+strings.ReplaceAll(status, "\n", "\n  "))
	}
	if commits != "" {
		lines = append(lines, "discarded commits on "+in.Branch+":\n  "+strings.ReplaceAll(commits, "\n", "\n  "))
	}
	if _, err := l.git(ctx, in.Dir, "checkout", "-q", "--detach", in.Target); err != nil {
		return "", fmt.Errorf("git checkout --detach %s in %s: %w", in.Target, in.Dir, err)
	}
	if noBranch == nil {
		if _, err := l.git(ctx, in.Dir, "branch", "-q", "-D", in.Branch); err != nil {
			return "", fmt.Errorf("git branch -D %s: %w", in.Branch, err)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// git runs git -C dir args… (l.Git, else git on PATH): its stdout, trimmed.
func (l Lane) git(ctx context.Context, dir string, args ...string) (string, error) {
	bin := l.Git
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...)
	cleanup := herdrun.Group(cmd)
	defer cleanup()
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// LaneSetup (T-122): a lane that fails commands.lane_ready gets commands.lane_setup, and must pass lane_ready after.
// No lane_ready: nothing runs. Its errors are final (port.Refused in Lane.Prepare).
func LaneSetup(cfg *config.Config, lane, dir string, w io.Writer) error {
	ready, ok := herdrun.Command(cfg, "lane_ready")
	if !ok {
		return nil
	}
	if rc, _ := runIn(dir, ready); rc == 0 {
		return nil
	}
	setup, ok := herdrun.Command(cfg, "lane_setup")
	if !ok {
		return project.Failf("lane_setup in %s: commands.lane_ready fails and commands.lane_setup is not set", lane)
	}
	if rc, text := runIn(dir, setup); rc != 0 {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		return project.Failf("lane_setup in %s: rc %d: %s", lane, rc, strings.Join(lines[max(0, len(lines)-3):], " "))
	}
	if rc, _ := runIn(dir, ready); rc != 0 {
		return project.Failf("lane_setup in %s: ran, but commands.lane_ready still fails (rc %d)", lane, rc)
	}
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintf(w, "lane_setup ran in %s (commands.lane_ready failed)\n", dir)
	return nil
}

// runIn runs a configured command in dir (run.Execute: bash, the whole process group killed when it returns or after
// its timeout, rc 124; herdrun.Command sets the default timeout): its exit code and combined output.
func runIn(dir string, c config.Command) (int, string) {
	var out bytes.Buffer
	rc, timedOut := herdrun.Execute(context.Background(), c, dir, nil, nil, &out, &out)
	if timedOut {
		return 124, out.String()
	}
	return rc, out.String()
}
