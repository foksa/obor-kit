package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"obor/internal/project"
)

// PromoteResult is a promotion run's outcome (T-974): the sha it checked, GREEN or RED, its line (RED: what failed and
// the failing tests), and whether the failing tests were rerun (commands.retest, T-964).
type PromoteResult struct {
	SHA, Verdict, Line string
	Retried            bool
}

// promoteName is the promotion session's name: its worktree <merge.scratch_dir>-promote and its logs' prefix.
const promoteName = "promote"

// Promote is serve's promotion run (T-974, docs/state-model.md row 50): the full suite at sha in its own worktree,
// <merge.scratch_dir>-promote (added when missing), reset to sha with lane_links: commands.lane_setup, then
// commands.check, holding a pool slot (prio worker, never the merge marker: slots.max bounds it with the lane tests).
// A red check whose failing tests and packages it names is rerun once by commands.retest at the same commit (T-964):
// green then is GREEN, Retried set. It writes .obor/state/last-full-check as obor nightly does (LastFullCheck). The
// promoted branch (branches.promote) at sha already is GREEN with nothing run (a crash between the fast-forward and
// its fact). GREEN and RED are outcomes; anything else (git, the slot, lane_setup, ctx ended) is an error: the
// promotion runs again. One runs at a time (serve's pass).
func Promote(ctx context.Context, o Options, sha string) (PromoteResult, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	sc := Scratch{Options: o}
	sc.gitCtx = ctx
	branch := o.Cfg.Data.Branches.Promote
	if branch == "" {
		return PromoteResult{}, errors.New("no branches.promote")
	}
	if at, err := sc.call(o.Cfg.Root, "rev-parse", "refs/heads/"+branch); err == nil && at == sha {
		return PromoteResult{SHA: sha, Verdict: "GREEN", Line: branch + " at " + short(sha) + " already"}, nil
	}
	cut, done := make(chan int, 1), make(chan struct{})
	defer close(done)
	go func() { // ctx's end is a signal's (143): the session's commands are cut, not red
		select {
		case <-ctx.Done():
			cut <- 143
		case <-done:
		}
	}()
	o.Interrupted = cut
	s, r := open(context.Background(), o, promoteName)
	if r != nil {
		return PromoteResult{}, errors.New(r.Line)
	}
	defer s.close()
	return s.promote(sha)
}

// promote is Promote's run in the session's worktree.
func (s *session) promote(sha string) (PromoteResult, error) {
	if err := (Scratch{Options: s.Options}).worktree(s.scratch, sha); err != nil {
		return PromoteResult{}, err
	}
	if !s.reset(sha) {
		return PromoteResult{}, fmt.Errorf("cannot reset %s to %s", s.scratch, short(sha))
	}
	linkLaneLinks(s.Cfg, s.root, s.scratch)
	s.prio, s.retest = "worker", true
	if h := s.laneSetup(); h != nil {
		return PromoteResult{}, s.promoteHalt(h, "lane_setup")
	}
	o, ok, h := s.must("check")
	if h != nil && h.red == nil {
		return PromoteResult{}, s.promoteHalt(h, "check")
	}
	if !ok {
		return PromoteResult{}, errors.New("commands.check not configured")
	}
	s.releaseSlot()
	s.reset(sha) // no command artifacts left behind
	verdict, summary := "GREEN", fmt.Sprintf("promote: check %s, %ds", s.what(o), secs(time.Since(s.start)))
	line := "check " + s.what(o)
	if h != nil {
		verdict = "RED"
		summary = "failed: check; " + summary
		s.printLines(s.details(o))
		if e := s.evidence(o); e != "" {
			line += " — " + e
		}
	}
	if s.retried {
		summary += ", retried"
	}
	file := filepath.Join(s.state, "last-full-check")
	if err := project.WriteAtomic(file, []byte(fmt.Sprintf("%s %d %s %s\n", sha, time.Now().Unix(), verdict, summary))); err != nil {
		return PromoteResult{}, err
	}
	return PromoteResult{SHA: sha, Verdict: verdict, Line: line, Retried: s.retried}, nil
}

// promoteHalt is a promotion's halt as an error: a signal (ctx ended), a git or slot error, a failed command.
func (s *session) promoteHalt(h *halt, name string) error {
	switch {
	case h.signal:
		return fmt.Errorf("%s interrupted (exit %d)", name, h.code)
	case h.err != nil:
		return h.err
	case h.red != nil:
		return fmt.Errorf("%s %s", name, s.what(*h.red))
	}
	return errors.New(h.fail)
}

// PromoteFF fast-forwards branches.promote to sha (T-974): update-ref with the branch's sha as the old value, only when
// that sha is an ancestor of sha; the branch at sha or past it already: nothing to do. Anything else is an error.
func PromoteFF(ctx context.Context, o Options, sha string) (string, error) {
	s := Scratch{Options: o}
	s.gitCtx = ctx
	branch, root := o.Cfg.Data.Branches.Promote, o.Cfg.Root
	if branch == "" {
		return "", errors.New("no branches.promote")
	}
	at, err := s.call(root, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("no branch %s (create it at a commit of %s): %w", branch, o.Cfg.Data.Branches.Main, err)
	}
	if at == sha {
		return branch + " at " + short(sha) + " already", nil
	}
	if past, err := s.ancestor(root, sha, at); err != nil || past {
		return branch + " at " + short(at) + ", past " + short(sha) + " already", err
	}
	ff, err := s.ancestor(root, at, sha)
	if err != nil {
		return "", err
	}
	if !ff {
		return "", fmt.Errorf("%s at %s is not an ancestor of %s: not fast-forwarded", branch, short(at), short(sha))
	}
	if _, err := s.call(root, "update-ref", "-m", "obor promote", "refs/heads/"+branch, sha, at); err != nil {
		return "", err
	}
	return "promoted " + branch + " " + short(at) + ".." + short(sha), nil
}

// AfterPromote runs commands.after_merge for the promoted sha (T-974, docs/state-model.md row 51) in the promotion
// worktree reset to sha, with OBOR_ROOT=<the main checkout> in its environment (obor-kit: make build
// OUT=$OBOR_ROOT/dist/obor, so serve's binary follows the promoted branch). Its when paths are matched against
// since..sha (since "": it runs). ran | skipped | WARN … (a failed command, as after_merge's); git or a cut run: an
// error (it runs again: after_merge is idempotent, T-525).
func AfterPromote(ctx context.Context, o Options, sha, since string) (string, error) {
	s := Scratch{Options: o}
	s.gitCtx = ctx
	if s.Out == nil {
		s.Out = io.Discard
	}
	cmd, ok := s.command("after_merge")
	if !ok {
		return "skipped", nil
	}
	root := o.Cfg.Root
	if len(cmd.When) > 0 && since != "" {
		out, err := s.call(root, "diff", "--no-renames", "--name-only", since, sha)
		if err != nil {
			return "", err
		}
		run := false
		for _, f := range lines(out) {
			run = run || matchAny(f, cmd.When)
		}
		if !run {
			return "skipped", nil
		}
	}
	dir := o.Cfg.ScratchDir() + "-" + promoteName
	if err := s.worktree(dir, sha); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"reset", "-q", "--hard", sha}, {"clean", "-fdq"}} {
		if _, err := s.call(dir, args...); err != nil {
			return "", err
		}
	}
	linkLaneLinks(o.Cfg, root, dir)
	r := newRunner(ctx, &s.Options, dir, promoteName, "promote-changed-files")
	r.env = append(r.env, "OBOR_ROOT="+root)
	out, _, h := r.run("after_merge", dir)
	if h != nil {
		return "", fmt.Errorf("after_merge interrupted (exit %d)", h.code)
	}
	if out.rc != 0 {
		line := afterMergeWarn(out)
		fmt.Fprintln(s.Out, line)
		return line, nil
	}
	return "ran", nil
}

// short is sha's first 7 characters.
func short(sha string) string { return sha[:min(7, len(sha))] }
