package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// reviewCmd is `obor review [start [--after T-a,T-b] [--note "<text>"]] | obor review --rm`, the lead's or the
// owner's: bare `obor review` prints the review state (obor status' review card, or NO REVIEW) and changes nothing;
// `obor review start` is one request to obor serve (wire.MethodReview). Serve's reply: REVIEW REQUESTED
// (now | after T-a,T-b)[ (replaces the request of <age> ago)], REVIEW REQUEST ENDED / NO REVIEW REQUEST for --rm.
// Serve starts the review once every --after task merged and the reviewer is idle, and tells the lead. FAIL: serve
// down, its refusal or a usage error (exit 1).
func reviewCmd(args []string) int {
	switch {
	case len(args) > 0 && args[0] == "close":
		return reviewCloseCmd(args[1:])
	case len(args) > 0 && args[0] == "start":
		return reviewStart(args[1:])
	case len(args) == 0:
		return reviewShow()
	}
	var c wire.ReviewReq
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--rm" && len(args) == 1:
			c.Clear = true
		case a == "--after" || a == "--note":
			fmt.Fprintf(out, "FAIL --after and --note start a review: %s\n", help.Usage("review start"))
			return 1
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("review"))
			return 1
		}
	}
	return reviewRequest(c)
}

// reviewStart is `obor review start [--after T-a,T-b] [--note "<text>"]`: the request to start a review, now or once
// the --after tasks merged.
func reviewStart(args []string) int {
	var c wire.ReviewReq
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case (a == "--after" || a == "--note") && i+1 < len(args) && args[i+1] != "":
			i++
			if a == "--after" {
				c.After = strings.Split(args[i], ",")
			} else {
				c.Note = args[i]
			}
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("review start"))
			return 1
		}
	}
	return reviewRequest(c)
}

// reviewRequest sends c as the caller's one review request.
func reviewRequest(c wire.ReviewReq) int {
	return request(context.Background(), project.OSEnv, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		if err := taskIDs(cfg, c.After...); err != nil {
			return nil, err
		}
		c.Envelope = id
		return &c, nil
	})
}

// reviewShow is bare `obor review`: the review card obor status shows — the open review's line, then the open request
// (or the "review due" line) — and nothing where neither is (NO REVIEW). It sends no request: no state changes.
func reviewShow() int {
	cfg, _, rc := loadReader()
	if rc != 0 {
		return rc
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ss, err := wire.Ask(ctx, wire.SocketPath(cfg.Root))
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	var open *wire.ReviewOpenStatus
	reqLine := ""
	if p := ss.Project; p != nil {
		open, reqLine = p.ReviewOpen, p.Review
	}
	if open == nil && reqLine == "" {
		fmt.Fprintln(out, "NO REVIEW")
		return 0
	}
	writeReviewOpen(out, open, time.Now())
	if reqLine != "" {
		fmt.Fprintln(out, reqLine)
	}
	return 0
}

// reviewCloseCmd is `obor review close "<summary>" [--accept "<line>"]… [--force]`: one request to obor serve
// (wire.MethodReviewClose). Serve's reply: CLOSED <subject>, the commit "RV-<n> closed · <reviewed sha> · approved ·
// <summary>" (with --accept lines: "accepted <n>" in place of "approved"; "abandoned" for a review whose file was never
// written) on the integration branch. --force also closes an unwritten review that covers no task (nothing to delete,
// the reviewed sha its start's head); one that covers tasks is refused until its reviewer writes it. FAIL:
// serve down, its refusal (no open review, no review start for it in the log, the main checkout not on the
// integration branch or dirty outside a tracked BACKLOG.md, docs/accepted.md and the review's files, a linked fix not
// verified without --force, a review with no file without --force, or an unwritten review that covers tasks even with
// --force) or a usage error (exit 1).
func reviewCloseCmd(args []string) int {
	var summary string
	var accepts []string
	force := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--force" && !force:
			force = true
		case a == "--accept" && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "":
			i++
			accepts = append(accepts, strings.Join(strings.Fields(args[i]), " "))
		case summary == "" && strings.TrimSpace(a) != "" && !strings.HasPrefix(a, "-"):
			summary = strings.Join(strings.Fields(a), " ")
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("review close"))
			return 1
		}
	}
	if summary == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("review close"))
		return 1
	}
	return request(context.Background(), project.OSEnv, func(_ *config.Config, id wire.Envelope) (wire.Req, error) {
		return &wire.ReviewCloseReq{Envelope: id, Summary: summary, Accept: accepts, Force: force}, nil
	})
}

// reviewGit is port.Review on the project's main checkout: git -C <root>. locked runs Detach's checkout as serve's
// own git calls run (checks.Scratch.Locked: a lock another git holds is waited for); nil: Detach fails.
type reviewGit struct {
	root   string
	locked func(ctx context.Context, dir string, args ...string) (string, error)
}

var _ port.Review = reviewGit{}

func (g reviewGit) Head(ctx context.Context, branch string) (string, error) {
	out, err := g.git(ctx, "rev-parse", "--verify", "--end-of-options", branch+"^{commit}")
	return strings.TrimSpace(out), err
}

func (g reviewGit) Files(ctx context.Context, from, to string) ([]string, error) {
	if from == "" {
		out, err := g.git(ctx, "ls-tree", "-r", "--name-only", "--end-of-options", to)
		return lines(out), err
	}
	out, err := g.git(ctx, "diff", "--name-only", "--end-of-options", from, to)
	return lines(out), err
}

// Detach puts the role worktree dir on sha, detached: added at it when dir is missing (project.AddWorktree),
// else checked out there. A dir that is no worktree of this repo, or local changes in the way of the checkout: an
// error, nothing forced. The checkout waits for a lock another git holds (locked: a publish's ref update meanwhile).
func (g reviewGit) Detach(ctx context.Context, dir, sha string) error {
	if g.locked == nil {
		return fmt.Errorf("the review git of %s has no locked git runner", g.root)
	}
	added, err := project.AddWorktree(ctx, g.root, dir, sha)
	if err != nil || added {
		return err
	}
	_, err = g.locked(ctx, dir, "checkout", "-q", "--detach", sha)
	return err
}

func (g reviewGit) git(ctx context.Context, args ...string) (string, error) {
	return project.GitCtx(ctx, g.root, args...)
}

// span is git's range from..to (from "": all of to).
func span(from, to string) string {
	if from == "" {
		return to
	}
	return from + ".." + to
}

// lines is out's non-empty lines.
func lines(out string) []string {
	var l []string
	for _, s := range strings.Split(out, "\n") {
		if s = strings.TrimSpace(s); s != "" {
			l = append(l, s)
		}
	}
	return l
}
