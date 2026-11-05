package checks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"obor/internal/app/port"
	"obor/internal/project"
)

// Scratch is obor serve's merge gate over real git (port.Scratch). It works in
// serve's own worktree, merge.scratch_dir, which only obor serve writes: every gate starts by resetting it, so there
// is no merge window, no lock and no ownership proof. Each step is a function of its inputs (shas the
// app records, the pins under refs/obor/), never of the worktree's state. An outcome is final for its step; an error
// is "not decided": the app runs the step again. Every git call ends with the method's ctx (killed with its process
// group).
type Scratch struct{ Options }

var _ port.Scratch = Scratch{}

// submittedRefs pins a submitted lane head, the gate's input: refs/obor/submitted/<task>/<submit id>, one per submit
// step. candidateRefs a green gate's merge, one per task; resolvedRefs the merge the integrator resolved in serve's
// worktree, the resolved gate's input: one per task.
const (
	submittedRefs = "refs/obor/submitted/"
	candidateRefs = "refs/obor/candidate/"
	resolvedRefs  = "refs/obor/resolved/"
)

// Submit commits the lane's uncommitted work ("<task>: <summary>") and pins the lane's HEAD at
// refs/obor/submitted/<task>/<id>. A pin there is this submit's result already: it is returned, nothing touched (a
// later worker commit stays out). A cut between the commit and the pin reads HEAD again (a residual race). The commit
// passes --no-verify (T-622): the project's checking step is commands.check, not its commit hooks (prepare-commit-msg
// still runs).
func (s Scratch) Submit(ctx context.Context, task, id, lane, summary, worker string) (string, error) {
	s.gitCtx = ctx
	pin := submittedRefs + task + "/" + id
	if sha, err := s.call(s.Cfg.Root, "rev-parse", "-q", "--verify", pin); err == nil {
		return sha, nil
	} else if _, err := exit1(err); err != nil {
		return "", err
	}
	dir := ""
	for _, l := range s.Cfg.Lanes() {
		if l.Name == lane {
			dir = l.Dir
		}
	}
	if fi, err := os.Stat(dir); dir == "" || err != nil || !fi.IsDir() {
		return "", port.Refused{Line: fmt.Sprintf("FAIL no lane %s", lane)}
	}
	branch, err := s.call(dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", port.Refused{Line: fmt.Sprintf("FAIL %s is not on a branch", dir)}
	}
	st, err := s.call(dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if st != "" {
		msg := task + ": " + summary + "\n\nImplemented by " + worker + ", reviewed by integrator." + s.trailer()
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "--no-verify", "-m", msg}} {
			if _, err := s.call(dir, args...); err != nil {
				return "", err
			}
		}
	}
	_, target, err := s.target()
	if err != nil {
		return "", err
	}
	head, err := s.call(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if in, err := s.ancestor(dir, head, target); err != nil {
		return "", err
	} else if in {
		return "", port.Refused{Line: fmt.Sprintf("FAIL %s is already in the target", branch)}
	}
	if err := s.dropFlatPin(task); err != nil {
		return "", err
	}
	if _, err := s.call(s.Cfg.Root, "update-ref", pin, head); err != nil {
		return "", err
	}
	return head, nil
}

// Gate resets serve's worktree (created when missing) and gates in.Input there: a lane or moved input (the submitted
// sha, never the old candidate, T-403) is merged onto the target (merge --no-ff --no-commit; a conflict stays open), a
// resolved one is checked as committed. Green: the merge committed and pinned at refs/obor/candidate/<task>. The merge
// commit's date is its newer parent's, so a repeat makes the same commit.
func (s Scratch) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	s.gitCtx = ctx
	if s.Out == nil {
		s.Out = io.Discard
	}
	branch, target, err := s.target()
	if err != nil {
		return port.GateOutcome{}, err
	}
	dir := s.dir()
	if err := s.worktree(dir, target); err != nil {
		return port.GateOutcome{}, err
	}
	base := target
	switch in.Kind {
	case port.InputLane, port.InputMoved:
	case port.InputResolved:
		base = in.Input
	default:
		return port.GateOutcome{}, fmt.Errorf("gate %s: unknown input kind %q", in.Task, in.Kind)
	}
	if _, err := s.call(dir, "reset", "-q", "--hard", base); err != nil {
		return port.GateOutcome{}, err
	}
	if _, err := s.call(dir, "clean", "-fdq"); err != nil {
		return port.GateOutcome{}, err
	}
	linkLaneLinks(s.Cfg, s.Cfg.Root, dir)

	r := newRunner(ctx, &s.Options, dir, in.Task, "serve-changed-files")
	r.onCommand = in.Command // obor status' running line names the command while it runs, none between two (T-669, T-673)
	defer r.releaseSlot()
	c := port.Candidate{Task: in.Task, Lane: in.Lane, SHA: in.Input}
	rev := ""
	if in.Kind == port.InputResolved {
		if r.pre, err = s.call(dir, "merge-base", target, in.Input); err != nil {
			return port.GateOutcome{}, err
		}
		if c.Target, err = s.call(dir, "rev-parse", in.Input+"^1"); err != nil {
			return port.GateOutcome{}, err
		}
		if c.SHA, err = s.specResolved(dir, in); err != nil {
			return port.GateOutcome{}, err
		}
		rev = r.pre + ".." + c.SHA
	} else {
		r.pre, c.Target, rev = target, target, target+"..."+in.Input
		if _, err := s.call(dir, "merge", "-q", "--no-commit", "--no-ff", in.Input); err != nil {
			if open, verr := s.exists(dir, "MERGE_HEAD"); verr != nil || !open {
				return port.GateOutcome{}, err
			}
			out, err := s.call(dir, "diff", "--name-only", "--diff-filter=U")
			if err != nil {
				return port.GateOutcome{}, err
			}
			files := lines(out)
			return port.GateOutcome{Kind: port.OutcomeConflict, Files: files, Dir: dir,
				Line: fmt.Sprintf("CONFLICT in %s: %s", dir, strings.Join(files, " "))}, nil
		}
	}
	if o, err := gateHalt(&r, r.changed(in.Task, rev)); o != nil || err != nil {
		return *o, err
	}
	if o, err := gateHalt(&r, r.laneSetup()); o != nil || err != nil {
		return *o, err
	}
	if o, err := gateHalt(&r, r.runChecks(in.Kind != port.InputResolved)); o != nil || err != nil {
		return *o, err
	}
	r.releaseSlot()

	if in.Kind != port.InputResolved {
		moved := ""
		if in.Kind == port.InputMoved {
			// T-360: a moved target keeps the task's subject rule; where the merge went is in the body.
			moved = "Merged onto the moved " + branch + " at " + target
		}
		msg := mergeMessage(in, moved, "") + s.trailer()
		if c.SHA, err = s.commit(dir, msg, target, in.Input); err != nil {
			return port.GateOutcome{}, err
		}
	}
	if _, err := s.call(s.Cfg.Root, "update-ref", candidateRefs+in.Task, c.SHA); err != nil {
		return port.GateOutcome{}, err
	}
	diff, err := s.call(s.Cfg.Root, "diff", c.Target, c.SHA)
	if err != nil {
		return port.GateOutcome{}, err
	}
	c.Patch = fmt.Sprintf("%x", sha256.Sum256([]byte(diff)))
	if len(diff) <= port.InlineDiff {
		c.Diff = diff // the review job carries it (T-602)
	}
	if c.Stat, err = s.call(s.Cfg.Root, "diff", "--stat", c.Target, c.SHA); err != nil {
		return port.GateOutcome{}, err
	}
	c.Checks, c.Full = r.check+r.gate, !r.class.Partial
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: c,
		Line: fmt.Sprintf("GREEN %s (%s, %s)%s", in.Task, c.Checks, r.mode, r.loaded)}, nil
}

// specBody is the spec part of a merge's message (T-521): a blank line, "Spec rev N:", a blank line and in.Spec (the
// store's rev at gate time); "" when none is stored (a task dispatched before T-440).
func specBody(in port.GateInput) string {
	if strings.TrimSpace(in.Spec) == "" {
		return ""
	}
	return fmt.Sprintf("\n\nSpec rev %d:\n\n%s", in.SpecRev, strings.TrimRight(in.Spec, "\n"))
}

// mergeMessage is every merge's message, the gate's own and the integrator's resolved one alike (T-802). Subject:
// "Merge T-xx: <in.Title>" (the spec's title, T-790), else the worker's summary's first line. Body, each part after a
// blank line: the spec (specBody), moved (a moved target's "Merged onto ..." line), resolver (the integrator's own
// message body without git's "#" comment lines) and "Summary: <in.Body>" (the report's whole Summary section, T-807;
// no Body: the first line, in.Summary; not repeated when it is the subject).
func mergeMessage(in port.GateInput, moved, resolver string) string {
	summary, _, _ := strings.Cut(strings.TrimSpace(in.Summary), "\n")
	body := strings.TrimSpace(in.Body)
	if body == "" {
		body = summary
	}
	subject := strings.TrimSpace(in.Title)
	if subject == "" {
		subject = summary
	}
	msg := strings.TrimSpace("Merge " + in.Task + ": " + subject)
	msg += specBody(in)
	if moved != "" {
		msg += "\n\n" + moved
	}
	var kept []string
	for _, l := range strings.Split(resolver, "\n") {
		if !strings.HasPrefix(l, "#") {
			kept = append(kept, strings.TrimRight(l, " \t"))
		}
	}
	if r := strings.TrimSpace(strings.Join(kept, "\n")); r != "" {
		msg += "\n\n" + r
	}
	if body != "" && body != subject {
		msg += "\n\nSummary: " + body
	}
	return msg
}

// specResolved gives the resolved merge checked out in dir the gate's message (mergeMessage: the subject
// "Merge T-xx: <title>" in place of git's own, the spec, the resolver's body): the merge is amended (same parents,
// tree and author; the committer date the merge's, so a repeat makes the same commit). The candidate's sha.
func (s Scratch) specResolved(dir string, in port.GateInput) (string, error) {
	out, err := s.call(dir, "show", "-s", "--format=%ct%n%B", in.Input)
	if err != nil {
		return "", err
	}
	ct, msg, _ := strings.Cut(out, "\n")
	_, rest, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	msg = mergeMessage(in, "", rest)
	env := []string{"GIT_COMMITTER_DATE=@" + ct + " +0000"}
	if _, errOut, err := s.gitLocked(env, dir, "commit", "-q", "--amend", "--no-verify", "-m", msg); err != nil {
		return "", gitError(dir, []string{"commit", "--amend"}, errOut, err)
	}
	return s.call(dir, "rev-parse", "HEAD")
}

// gateHalt is h, how r's checks stopped, as a gate outcome: red or fail are final (nothing to clean: the next gate resets), git I/O, the check
// slot and a signal are errors; nil, nil when h is.
func gateHalt(r *runner, h *halt) (*port.GateOutcome, error) {
	o := &port.GateOutcome{}
	switch {
	case h == nil:
		return nil, nil
	case h.signal:
		return o, fmt.Errorf("interrupted (exit %d)", h.code)
	case h.err != nil:
		return o, h.err
	case h.red != nil:
		what, evidence := r.redLine(*h.red)
		o.Kind, o.Line = port.OutcomeRed, fmt.Sprintf("RED %s (%s)%s%s", h.red.name, what, r.loaded, evidence)
	default:
		o.Kind, o.Line = port.OutcomeFail, "FAIL "+h.fail
	}
	return o, nil
}

// commit commits the open merge of the target and input in dir with the newer parent's date (the same commit on every
// repeat): its sha. --no-verify as submit's (T-622); prepare-commit-msg still runs.
func (s Scratch) commit(dir, msg, target, input string) (string, error) {
	out, err := s.call(dir, "show", "-s", "--format=%ct", target, input)
	if err != nil {
		return "", err
	}
	var newest int64
	for _, l := range lines(out) {
		ct, err := strconv.ParseInt(l, 10, 64)
		if err != nil {
			return "", fmt.Errorf("commit dates of %s and %s: %q", target, input, out)
		}
		newest = max(newest, ct)
	}
	date := fmt.Sprintf("@%d +0000", newest)
	env := []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	if _, errOut, err := s.gitLocked(env, dir, "commit", "-q", "--no-verify", "-m", msg); err != nil {
		return "", gitError(dir, []string{"commit"}, errOut, err)
	}
	return s.call(dir, "rev-parse", "HEAD")
}

// Resolved pins serve's worktree HEAD, the merge the integrator resolved and committed there, at
// refs/obor/resolved/<task>, only when it is the merge of input (the conflicting gate's: submit's sha, a lane or moved
// gate alike, T-403) on top of the target: a merge whose second parent is input and whose first is in the target.
// Refused: no worktree, a merge still open, another HEAD (another task's gate, say), or one the target holds.
func (s Scratch) Resolved(ctx context.Context, task, input string) (string, error) {
	s.gitCtx = ctx
	dir := s.dir()
	if _, err := os.Stat(dir); err != nil {
		return "", port.Refused{Line: "FAIL no serve worktree " + dir}
	}
	if open, err := s.exists(dir, "MERGE_HEAD"); err != nil {
		return "", err
	} else if open {
		return "", port.Refused{Line: fmt.Sprintf("FAIL merge in progress in %s: commit the resolution first", dir)}
	}
	head, err := s.call(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	_, target, err := s.target()
	if err != nil {
		return "", err
	}
	if in, err := s.ancestor(dir, head, target); err != nil {
		return "", err
	} else if in {
		return "", port.Refused{Line: fmt.Sprintf("FAIL no resolved merge in %s (commit it there)", dir)}
	}
	out, err := s.call(dir, "rev-list", "--parents", "-n", "1", head)
	if err != nil {
		return "", err
	}
	if parents := strings.Fields(out); len(parents) != 3 || parents[2] != input {
		return "", port.Refused{Line: fmt.Sprintf("FAIL HEAD of %s is not %s's merge of %s (resolve that conflict there)",
			dir, task, input)}
	} else if in, err := s.ancestor(dir, parents[1], target); err != nil {
		return "", err
	} else if !in {
		return "", port.Refused{Line: fmt.Sprintf("FAIL HEAD of %s is not a merge onto %s", dir, target)}
	}
	if _, err := s.call(s.Cfg.Root, "update-ref", resolvedRefs+task, head); err != nil {
		return "", err
	}
	return head, nil
}

// Publish fast-forwards the target to c.SHA when it is still c.Target and deletes the task's pins. Already: the
// target holds c (a repeat after the fast-forward). Hold: uncommitted changes in the main checkout touch merged files;
// decided before any write, so a lock in the main checkout can only be an error.
func (s Scratch) Publish(ctx context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.gitCtx = ctx
	root := s.Cfg.Root
	// A fast-forward refused while the target moved goes round once more: Already or Moved then.
	for range 2 {
		_, head, err := s.target()
		if err != nil {
			return port.PublishOutcome{}, err
		}
		if in, err := s.ancestor(root, c.SHA, head); err != nil {
			return port.PublishOutcome{}, err
		} else if in {
			return port.PublishOutcome{Kind: port.PublishAlready}, s.Unpin(ctx, c.Task)
		}
		if head != c.Target {
			return port.PublishOutcome{Kind: port.PublishMoved, Head: head}, nil
		}
		out, err := s.call(root, "diff", "--no-renames", "--name-only", c.Target, c.SHA)
		if err != nil {
			return port.PublishOutcome{}, err
		}
		if paths := lines(out); len(paths) > 0 {
			st, err := s.call(root, append([]string{"--literal-pathspecs", "status", "--porcelain", "--"}, paths...)...)
			if err != nil {
				return port.PublishOutcome{}, err
			}
			if st != "" {
				var files []string
				for _, l := range lines(st) {
					files = append(files, l[min(3, len(l)):])
				}
				return port.PublishOutcome{Kind: port.PublishHold, Files: files}, nil
			}
		}
		// ff-only moves the checked-out branch: off the target it would publish elsewhere (the loop pauses, T-425).
		if b, err := s.call(root, "branch", "--show-current"); err != nil {
			return port.PublishOutcome{}, err
		} else if b != s.Cfg.Data.Branches.Main {
			return port.PublishOutcome{}, fmt.Errorf("publish %s: the main checkout %s is not on %s", c.Task, root, s.Cfg.Data.Branches.Main)
		}
		_, ffErr := s.call(root, "merge", "-q", "--ff-only", c.SHA)
		if ffErr == nil {
			return port.PublishOutcome{Kind: port.PublishMerged}, s.Unpin(ctx, c.Task)
		}
		if _, now, err := s.target(); err != nil {
			return port.PublishOutcome{}, err
		} else if now == c.Target {
			return port.PublishOutcome{}, ffErr
		}
	}
	return port.PublishOutcome{}, fmt.Errorf("publish %s: the target keeps moving", c.Task)
}

// AfterMerge runs commands.after_merge in the main checkout for the published c when its "when" globs match the
// merged files (none: always). The run's full output goes to .obor/state/logs/<task>-after_merge.log (green or failed,
// T-414). A failing command is the WARN line. Its intent (G) is the once: a cut before its result runs it again
// (after_merge is idempotent, T-525).
func (s Scratch) AfterMerge(ctx context.Context, c port.Candidate) (string, error) {
	s.gitCtx = ctx
	if s.Out == nil {
		s.Out = io.Discard
	}
	line := "skipped"
	if cmd, ok := s.command("after_merge"); ok {
		run := true
		if len(cmd.When) > 0 {
			out, err := s.call(s.Cfg.Root, "diff", "--no-renames", "--name-only", c.Target, c.SHA)
			if err != nil {
				return "", err
			}
			run = false
			for _, f := range lines(out) {
				run = run || matchAny(f, cmd.When)
			}
		}
		if run {
			var err error
			if line, err = s.afterMergeRun(ctx, c); err != nil {
				return "", err
			}
		}
	}
	return line, nil
}

// afterMergeRun runs commands.after_merge in the main checkout for the published c and returns its result line. The
// checkout must be at c.SHA on a branch (publish fast-forwards it; a later merge or a checkout moved it away): else a
// WARN line and no run.
func (s Scratch) afterMergeRun(ctx context.Context, c port.Candidate) (string, error) {
	branch, err := s.call(s.Cfg.Root, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	at := ""
	if branch != "" {
		if at, err = s.call(s.Cfg.Root, "rev-parse", "refs/heads/"+branch); err != nil {
			return "", err
		}
	}
	if at != c.SHA {
		line := "WARN after_merge not run: the main checkout is not at the published merge " + c.SHA
		fmt.Fprintln(s.Out, line)
		return line, nil
	}
	r := newRunner(ctx, &s.Options, s.Cfg.Root, c.Task, "serve-changed-files")
	o, _, h := r.run("after_merge", s.Cfg.Root)
	if h != nil {
		return "", fmt.Errorf("after_merge interrupted (exit %d)", h.code)
	}
	if o.rc != 0 {
		line := afterMergeWarn(o)
		fmt.Fprintln(s.Out, line)
		return line, nil
	}
	return "ran", nil
}

// afterMergeWarn is a failed after_merge's WARN line: its last non-blank output line, else its exit code, and its log
// (named as a failing check's line names its own).
func afterMergeWarn(o output) string {
	last := fmt.Sprintf("rc %d", o.rc)
	for _, l := range lines(o.text) {
		if strings.TrimSpace(l) != "" {
			last = l
		}
	}
	line := "WARN after_merge failed: " + last
	if o.log != "" {
		line += "; log " + o.log
	}
	return line
}

// Landed: the target holds merge evidence of task (a merge outside the app); an error: git could not read its log.
// MergeEvidenceBy reads HEAD's log: here it reads branches.main's, whatever the main checkout is on (T-425).
func (s Scratch) Landed(ctx context.Context, task string) (bool, error) {
	s.gitCtx = ctx
	branch, _, err := s.target()
	if err != nil {
		return false, err
	}
	return NewLaneTable(ctx, s.Cfg).MergeEvidenceBy(task, func(args ...string) (string, error) {
		if n := len(args); n > 0 && args[n-1] == "HEAD" {
			args = append(args[:n-1:n-1], "refs/heads/"+branch)
		}
		return s.call(s.Cfg.Root, args...)
	})
}

// Unpin deletes the task's pins: every submitted one, candidate, resolved (an absent one is not an error).
func (s Scratch) Unpin(ctx context.Context, task string) error {
	s.gitCtx = ctx
	out, err := s.call(s.Cfg.Root, "for-each-ref", "--format=%(refname)", submittedRefs+task+"/")
	if err != nil {
		return err
	}
	for _, ref := range append(lines(out), candidateRefs+task, resolvedRefs+task) {
		if _, err := s.call(s.Cfg.Root, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return s.dropFlatPin(task)
}

// dropFlatPin deletes refs/obor/submitted/<task>, the pin before submits had an id: it would stand in the way of the
// task's directory of pins.
func (s Scratch) dropFlatPin(task string) error {
	if flat, err := s.exists(s.Cfg.Root, submittedRefs+task); err != nil || !flat {
		return err
	}
	_, err := s.call(s.Cfg.Root, "update-ref", "-d", submittedRefs+task)
	return err
}

// dir is serve's worktree, merge.scratch_dir.
func (s Scratch) dir() string {
	return s.Cfg.ScratchDir()
}

// worktree makes sure dir is a worktree of the project, added detached at target when missing (-f: a worktree whose
// directory was removed is still registered).
func (s Scratch) worktree(dir, target string) error {
	if _, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		_, err := s.call(s.Cfg.Root, "worktree", "add", "-f", "-q", "--detach", dir, target)
		return err
	}
	top, err := s.call(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if project.Physical(top) != project.Physical(dir) {
		return fmt.Errorf("%s is not a git worktree", dir)
	}
	return nil
}

// target is branches.main and the sha it is at, whatever the main checkout has checked out (T-425).
func (s Scratch) target() (branch, head string, err error) {
	branch = s.Cfg.Data.Branches.Main
	head, err = s.call(s.Cfg.Root, "rev-parse", "refs/heads/"+branch)
	return branch, head, err
}

// ancestor reports whether a is an ancestor of b (or b itself); an error: git could not tell.
func (s Scratch) ancestor(dir, a, b string) (bool, error) {
	_, err := s.call(dir, "merge-base", "--is-ancestor", a, b)
	return exit1(err)
}

// exists reports whether ref resolves in dir (MERGE_HEAD: a merge is open); an error: git could not tell.
func (s Scratch) exists(dir, ref string) (bool, error) {
	_, err := s.call(dir, "rev-parse", "-q", "--verify", ref)
	return exit1(err)
}

// exit1 is a yes/no git call's answer: exit 0 yes, exit 1 no, anything else an error.
func exit1(err error) (bool, error) {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// call runs git -C dir args… (a lock taken by another git waited for: gitLocked): stdout; an error names the call
// and git's last stderr line.
func (s Scratch) call(dir string, args ...string) (string, error) {
	out, errOut, err := s.gitLocked(nil, dir, args...)
	if err != nil {
		return out, gitError(dir, args, errOut, err)
	}
	return out, nil
}

// gitError wraps a failed git call's error (errors.As still finds its *exec.ExitError).
func gitError(dir string, args []string, errOut string, err error) error {
	why := ""
	if l := lines(errOut); len(l) > 0 {
		why = ": " + l[len(l)-1]
	}
	return fmt.Errorf("git %s in %s: %w%s", args[0], dir, err, why)
}

func (s Scratch) trailer() string {
	if t := s.Cfg.Data.Merge.Trailer; t != "" {
		return "\n\n" + t
	}
	return ""
}
