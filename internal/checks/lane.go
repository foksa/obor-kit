package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	herdrun "obor/internal/run"
)

// Lane is port.LaneSetup for one project: the lane.prepare step of a dispatched task, the target merged into a lane
// before a fix attempt (lane.sync) and the lane a drop gives back. Its git is laneGit: real git in the lane's worktree
// with the caller's ctx; obor's links around a checkout or the merge are l's own git calls (linkGit).
type Lane struct {
	Cfg  *config.Config
	Out  io.Writer // progress lines (nil: none)
	Git  string    // the git binary ("" : git on PATH); a test puts a shim here (a lock's holder)
	fake laneGit   // the tests' git (fakegit_test.go); nil: real git
}

var _ port.LaneSetup = Lane{}

// laneGit is the git Lane runs in a lane's worktree: each method one question or one change. Prepare and Free decide
// over its answers only (and the config, the lane dir on disk), so the decision tests run them on fakeGit.
type laneGit interface {
	// Dirt is the lane's uncommitted work, obor's own links left out (project.LaneDirt): its paths.
	Dirt(dir string) ([]string, error)
	// Verify is rev-parse -q --verify ref: its sha, ok false (and no error) when ref does not resolve.
	Verify(dir, ref string) (sha string, ok bool, err error)
	// Ancestor reports whether a is an ancestor of b (or b itself).
	Ancestor(dir, a, b string) (bool, error)
	// Ahead is log --oneline from..to: the commits to has that from does not, a line each.
	Ahead(dir, from, to string) (string, error)
	// NewBranch checks branch out in dir, made (or moved) at target (checkout -B), obor's links kept around it.
	NewBranch(dir, branch, target string) error
	// Reset is reset --hard rev ("": HEAD) in dir; Clean removes its untracked files (ignored ones and obor's links,
	// project.LinksIn, stay).
	Reset(dir, rev string) error
	Clean(dir string) error
	// Detach checks sha out detached in dir, obor's links kept around it.
	Detach(dir, sha string) error
	// DeleteBranch deletes branch (branch -D).
	DeleteBranch(dir, branch string) error
	// Branch is the branch dir has checked out ("": detached).
	Branch(dir string) (string, error)
	// Parents is sha and its parents (rev-list --parents -n 1).
	Parents(dir, sha string) ([]string, error)
	// Merge merges input into dir's HEAD, --no-commit --no-ff: an error when it stopped (a conflict among them:
	// MERGE_HEAD stays); Unmerged is the paths an open merge left unmerged; MergeAbort is merge --abort. Its caller
	// keeps obor's links out of its way (project.WithLinks).
	Merge(dir, input string) error
	Unmerged(dir string) ([]string, error)
	MergeAbort(dir string) error
	// CommitMerge commits the merge open in dir with msg, --no-verify.
	CommitMerge(dir, msg string) error
}

// lgit is the git l works through under ctx: its fake when a test set one, else laneReal.
func (l Lane) lgit(ctx context.Context) laneGit {
	if l.fake != nil {
		return l.fake
	}
	return laneReal{l: l, ctx: ctx}
}

// Prepare puts in.Dir on in.Branch at in.Target (in.Redo: the branch is kept as it is) and runs lane_ready (then
// lane_setup and lane_ready again when it was red). It returns the target sha the branch is based on: serve's lane row
// (its head is the submitted head). Repeating it converges on the same state.
// A final refusal: no lane dir, lane_setup red, lane_ready still red; git failures are plain errors (retry); a lock
// the rule could not clear (gitLocked) names it, and nothing more is removed.
func (l Lane) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if !project.IsDir(in.Dir) {
		return port.Prepared{}, port.Refused{Line: "no lane dir " + in.Dir}
	}
	if !in.Redo {
		if err := l.lgit(ctx).NewBranch(in.Dir, in.Branch, in.Target); err != nil {
			return port.Prepared{}, fmt.Errorf("checkout -B %s %s in %s: %w%s", in.Branch, in.Target, in.Dir, err,
				lockHint(in.Dir, err))
		}
	}
	if err := LaneSetup(ctx, l.Cfg, in.Lane, in.Dir, l.Out); err != nil {
		return port.Prepared{}, port.Refused{Line: err.Error()}
	}
	return port.Prepared{Base: in.Target}, nil
}

// Sync merges in.Target into the task branch in.Dir has checked out (port.LaneSetup.Sync, lane.sync): its commit
// subject "Merge <target branch> into <branch> (obor: fix attempt <n>)". A merge an earlier cut Sync left open
// (MERGE_HEAD) is aborted first; a HEAD that is the merge of in.Target already (a cut after the commit) is merged.
// The merge keeps obor's links the way a checkout does (project.WithLinks). Then: the target in HEAD's history is
// current, uncommitted changes (project.LaneDirt: obor's links are not the lane's) are dirty, nothing touched; a merge
// that stops with unmerged paths is aborted (the lane as it was) and is a conflict naming them; one that stops otherwise
// is aborted and an error. No lane dir, a lane not on in.Branch and git failures are errors (retry).
func (l Lane) Sync(ctx context.Context, in port.SyncInput) (port.Synced, error) {
	if !project.IsDir(in.Dir) {
		return port.Synced{}, fmt.Errorf("no lane dir %s", in.Dir)
	}
	git := l.lgit(ctx)
	branch, err := git.Branch(in.Dir)
	if err != nil {
		return port.Synced{}, fmt.Errorf("read the branch of %s: %w", in.Dir, err)
	}
	if branch != in.Branch {
		return port.Synced{}, fmt.Errorf("lane %s is on %q, not on %s", in.Lane, branch, in.Branch)
	}
	if err := l.abortMerge(git, in.Dir); err != nil {
		return port.Synced{}, err
	}
	head, ok, err := git.Verify(in.Dir, "HEAD")
	if err != nil || !ok {
		return port.Synced{}, fmt.Errorf("read HEAD of %s: %v", in.Dir, cmpErr(err, "no HEAD"))
	}
	parents, err := git.Parents(in.Dir, head)
	if err != nil {
		return port.Synced{}, fmt.Errorf("read the parents of %s: %w", head, err)
	}
	if len(parents) == 3 && parents[2] == in.Target {
		return port.Synced{Kind: port.SyncMerged, SHA: head}, nil // a cut Sync committed it
	}
	current, err := git.Ancestor(in.Dir, in.Target, head)
	if err != nil {
		return port.Synced{}, fmt.Errorf("check %s in %s's history: %w", in.Target, in.Branch, err)
	}
	if current {
		return port.Synced{Kind: port.SyncCurrent}, nil
	}
	dirt, err := git.Dirt(in.Dir)
	if err != nil {
		return port.Synced{}, fmt.Errorf("git status in %s: %w", in.Dir, err)
	}
	if len(dirt) > 0 {
		return port.Synced{Kind: port.SyncDirty, Files: dirt}, nil
	}
	// The merge keeps obor's links the way a checkout does (project.WithLinks): a link where the target starts
	// tracking the path comes out of its way and the tracked file takes the path; a link the merge leaves no file for
	// comes back. A merge that stops is aborted inside the change, so the links return around a lane as it was.
	var (
		mergeErr error    // the merge's own failure (nil: it merged)
		files    []string // the paths the stopped merge left unmerged
		unread   error    // the unmerged read's error
		abortErr error    // the abort's error, after a merge that stopped
	)
	err = project.WithLinks(l.Cfg, in.Dir, in.Target, l.linkGit(ctx), l.say, func() error {
		if mergeErr = git.Merge(in.Dir, in.Target); mergeErr == nil {
			return nil
		}
		files, unread = git.Unmerged(in.Dir)
		abortErr = l.abortMerge(git, in.Dir)
		return mergeErr
	})
	if err != nil {
		if abortErr != nil {
			return port.Synced{}, fmt.Errorf("merge %s in %s: %v; %w", in.Target, in.Dir, mergeErr, abortErr)
		}
		if mergeErr == nil {
			return port.Synced{}, fmt.Errorf("merge %s in %s: %w", in.Target, in.Dir, err) // the links' own failure
		}
		if unread == nil && len(files) > 0 {
			return port.Synced{Kind: port.SyncConflict, Files: files}, nil
		}
		return port.Synced{}, fmt.Errorf("merge %s in %s: %w%s", in.Target, in.Dir, mergeErr, lockHint(in.Dir, mergeErr))
	}
	msg := fmt.Sprintf("Merge %s into %s (obor: fix attempt %d)", l.Cfg.MainBranch(), in.Branch, in.Attempt)
	if err := git.CommitMerge(in.Dir, msg); err != nil {
		if aerr := l.abortMerge(git, in.Dir); aerr != nil {
			return port.Synced{}, fmt.Errorf("commit the merge in %s: %v; %w", in.Dir, err, aerr)
		}
		return port.Synced{}, fmt.Errorf("commit the merge in %s: %w", in.Dir, err)
	}
	sha, ok, err := git.Verify(in.Dir, "HEAD")
	if err != nil || !ok {
		return port.Synced{}, fmt.Errorf("read HEAD of %s after the merge: %v", in.Dir, cmpErr(err, "no HEAD"))
	}
	return port.Synced{Kind: port.SyncMerged, SHA: sha}, nil
}

// abortMerge aborts the merge open in dir (MERGE_HEAD; none: nothing to do).
func (l Lane) abortMerge(git laneGit, dir string) error {
	_, open, err := git.Verify(dir, "MERGE_HEAD")
	if err != nil {
		return fmt.Errorf("read MERGE_HEAD of %s: %w", dir, err)
	}
	if !open {
		return nil
	}
	if err := git.MergeAbort(dir); err != nil {
		return fmt.Errorf("git merge --abort in %s: %w", dir, err)
	}
	return nil
}

// cmpErr is err, else an error of why (a read that answered nothing).
func cmpErr(err error, why string) error {
	if err != nil {
		return err
	}
	return errors.New(why)
}

// checkout is git checkout -q args… in dir, which puts it on target, with obor's links kept around it
// (project.CheckoutWorktree: the ones target tracks removed first, the missing ones made after), a line each to l.Out.
func (l Lane) checkout(ctx context.Context, dir, target string, args ...string) error {
	return project.CheckoutWorktree(l.Cfg, dir, target, l.linkGit(ctx), l.say, func() error {
		_, err := l.git(ctx, dir, append([]string{"checkout", "-q"}, args...)...)
		return err
	})
}

// linkGit is the git seam of the link handling (project.WithLinks: the status, ls-tree and rev-parse of the links
// around a checkout or the merge): l's own git call under ctx.
func (l Lane) linkGit(ctx context.Context) func(dir string, args ...string) (string, error) {
	return func(dir string, args ...string) (string, error) { return l.gitRaw(ctx, dir, args...) }
}

// say is a progress line to l.Out (nil: dropped).
func (l Lane) say(line string) {
	if l.Out != nil {
		fmt.Fprintln(l.Out, line)
	}
}

// lockHint is a failed lane git call's hint for a lock that outlived the wait (gitLocked): git's exit 128 whose error
// names index.lock gets the lock's path as git names it (the lane worktree's git dir), else the lane's own. "" for any
// other failure.
func lockHint(dir string, err error) string {
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 128 || !bytes.Contains([]byte(err.Error()), []byte("index.lock")) {
		return ""
	}
	path := filepath.Join(dir, "index.lock")
	if m := lockRe.FindStringSubmatch(err.Error()); m != nil {
		path = m[1]
	}
	return " — still locked after the wait: " + path
}

// Free gives a dropped task's lane back (obor drop). Refused, nothing changed: no lane dir; uncommitted
// changes (project.LaneDirt: obor's links are not the lane's) or commits on in.Branch not in in.Target, unless
// in.Discard (then git reset --hard and git clean -fd, which keeps obor's links, the commits left with the deleted
// branch). Then the lane is detached at in.Target and in.Branch deleted (a branch that
// is gone already is fine). Its line lists what it discarded ("" nothing). A failure after the detach (the branch
// delete) leaves the lane freed: its error says the same obor drop finishes it.
func (l Lane) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if !project.IsDir(in.Dir) {
		return "", port.Refused{Line: "no lane dir " + in.Dir}
	}
	git := l.lgit(ctx)
	dirt, err := git.Dirt(in.Dir)
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", in.Dir, err)
	}
	status := strings.Join(dirt, "\n")
	_, exists, err := git.Verify(in.Dir, "refs/heads/"+in.Branch)
	if err != nil {
		return "", fmt.Errorf("read branch %s: %w", in.Branch, err)
	}
	var commits string
	if exists {
		merged, err := git.Ancestor(in.Dir, "refs/heads/"+in.Branch, in.Target)
		if err != nil {
			return "", fmt.Errorf("check branch %s ancestry: %w", in.Branch, err)
		}
		if !merged {
			commits, err = git.Ahead(in.Dir, in.Target, "refs/heads/"+in.Branch)
			if err != nil {
				return "", fmt.Errorf("read unmerged commits on %s: %w", in.Branch, err)
			}
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
		if err := git.Reset(in.Dir, ""); err != nil {
			return "", fmt.Errorf("git reset --hard in %s: %w", in.Dir, err)
		}
		if err := git.Clean(in.Dir); err != nil {
			return "", fmt.Errorf("git clean -fd in %s: %w", in.Dir, err)
		}
		lines = append(lines, "discarded in "+in.Lane+":\n  "+strings.ReplaceAll(status, "\n", "\n  "))
	}
	if commits != "" {
		lines = append(lines, "discarded commits on "+in.Branch+":\n  "+strings.ReplaceAll(commits, "\n", "\n  "))
	}
	if err := git.Detach(in.Dir, in.Target); err != nil {
		return "", fmt.Errorf("git checkout --detach %s in %s: %w", in.Target, in.Dir, err)
	}
	if exists {
		if err := git.DeleteBranch(in.Dir, in.Branch); err != nil {
			return "", fmt.Errorf("git branch -D %s: %w — run the same obor drop again to finish it", in.Branch, err)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// laneReal is laneGit over real git: each method Lane's own git call (gitRaw: obor's git environment, the lock rule,
// its error line), under ctx.
type laneReal struct {
	l   Lane
	ctx context.Context
}

var _ laneGit = laneReal{}

func (g laneReal) run(dir string, args ...string) (string, error) {
	return g.l.gitRaw(g.ctx, dir, args...)
}

func (g laneReal) Dirt(dir string) ([]string, error) { return project.LaneDirt(g.l.Cfg, dir, g.run) }

func (g laneReal) Verify(dir, ref string) (string, bool, error) {
	sha, err := g.l.git(g.ctx, dir, "rev-parse", "-q", "--verify", ref)
	ok, err := exit1(err)
	if !ok {
		sha = ""
	}
	return sha, ok, err
}

func (g laneReal) Ancestor(dir, a, b string) (bool, error) {
	_, err := g.l.git(g.ctx, dir, "merge-base", "--is-ancestor", a, b)
	return exit1(err)
}

func (g laneReal) Ahead(dir, from, to string) (string, error) {
	return g.l.git(g.ctx, dir, "log", "--oneline", from+".."+to)
}

func (g laneReal) NewBranch(dir, branch, target string) error {
	return g.l.checkout(g.ctx, dir, target, "-B", branch, target)
}

func (g laneReal) Reset(dir, rev string) error {
	_, err := g.l.git(g.ctx, dir, nonEmpty("reset", "-q", "--hard", rev)...)
	return err
}

func (g laneReal) Clean(dir string) error {
	args := []string{"clean", "-q", "-fd"}
	for _, link := range project.LinksIn(g.l.Cfg, dir, g.run) {
		args = append(args, "-e", "/"+globEscape.Replace(link))
	}
	_, err := g.l.git(g.ctx, dir, args...)
	return err
}

func (g laneReal) Detach(dir, sha string) error {
	return g.l.checkout(g.ctx, dir, sha, "--detach", sha)
}

func (g laneReal) DeleteBranch(dir, branch string) error {
	_, err := g.l.git(g.ctx, dir, "branch", "-q", "-D", branch)
	return err
}

func (g laneReal) Branch(dir string) (string, error) {
	return g.l.git(g.ctx, dir, "branch", "--show-current")
}

func (g laneReal) Parents(dir, sha string) ([]string, error) {
	out, err := g.l.git(g.ctx, dir, "rev-list", "--parents", "-n", "1", sha)
	return strings.Fields(out), err
}

func (g laneReal) Merge(dir, input string) error {
	_, err := g.l.git(g.ctx, dir, "merge", "-q", "--no-commit", "--no-ff", input)
	return err
}

func (g laneReal) Unmerged(dir string) ([]string, error) {
	out, err := g.l.git(g.ctx, dir, "diff", "--name-only", "--diff-filter=U")
	return lines(out), err
}

func (g laneReal) MergeAbort(dir string) error {
	_, err := g.l.git(g.ctx, dir, "merge", "--abort")
	return err
}

func (g laneReal) CommitMerge(dir, msg string) error {
	_, err := g.l.git(g.ctx, dir, "commit", "-q", "--no-verify", "-m", msg)
	return err
}

// git runs git -C dir args… (l.Git, else git on PATH): its stdout, trimmed.
func (l Lane) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := l.gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git with its stdout as git wrote it (status --porcelain's leading space is part of a line), carrying obor's
// git environment (project.GitEnv) and the lock rule (gitLocked: a stale lock removed once, logged to l.Out); an error
// carries git's lock line (the one naming the lock it could not take) or, for another failure, its last stderr line.
func (l Lane) gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	bin := l.Git
	if bin == "" {
		bin = "git"
	}
	git := func(env []string, dir string, args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...)
		cmd.Env = project.GitEnv(env)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cleanup := project.Group(cmd)
		defer cleanup()
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	out, errOut, err := gitLocked(ctx, l.Out, nil, git, nil, dir, args...)
	if err != nil {
		if ls := lines(errOut); len(ls) > 0 {
			return out, fmt.Errorf("%w: %s", err, stderrLine(ls))
		}
		return out, err
	}
	return out, nil
}

// stderrLine is the failed git call's line to report: the lock line (lockRe: git's "Unable to create '…': File exists"
// above its hint, which a lockHint needs the path from), else the last line.
func stderrLine(ls []string) string {
	for _, l := range ls {
		if lockRe.MatchString(l) {
			return l
		}
	}
	return ls[len(ls)-1]
}

// LaneSetup: a lane that fails lanes.ready gets lanes.setup, and must pass lane_ready after. A role worktree serve
// moves to a head gets the same (ScratchFF.Detach).
// No lane_ready: nothing runs. Its errors are final (port.Refused in Lane.Prepare). Every command runs under ctx
// a canceled prepare ends it with its process group.
func LaneSetup(ctx context.Context, cfg *config.Config, lane, dir string, w io.Writer) error {
	ready, ok := herdrun.Command(cfg, "lane_ready")
	if !ok {
		return nil
	}
	if rc, _ := runIn(ctx, dir, ready); rc == 0 {
		return nil
	}
	setup, ok := herdrun.Command(cfg, "lane_setup")
	if !ok {
		return project.Failf("lane_setup in %s: lanes.ready fails and lanes.setup is not set", lane)
	}
	if rc, text := runIn(ctx, dir, setup); rc != 0 {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		return project.Failf("lane_setup in %s: rc %d: %s", lane, rc, strings.Join(lines[max(0, len(lines)-3):], " "))
	}
	if rc, _ := runIn(ctx, dir, ready); rc != 0 {
		return project.Failf("lane_setup in %s: ran, but lanes.ready still fails (rc %d)", lane, rc)
	}
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintf(w, "lane_setup ran in %s (lanes.ready failed)\n", dir)
	return nil
}

// runIn runs a configured command in dir under ctx (run.Execute: bash, the whole process group killed when it returns,
// the ctx ends or its timeout passes, rc 124; herdrun.Command sets the default timeout): its exit code and combined
// output.
func runIn(ctx context.Context, dir string, c config.Command) (int, string) {
	var out bytes.Buffer
	rc, timedOut := herdrun.Execute(ctx, c, dir, nil, nil, &out, &out)
	if timedOut {
		return 124, out.String()
	}
	return rc, out.String()
}
