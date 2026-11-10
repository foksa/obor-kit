package main

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// reviewCmd is `obor review [--after T-a,T-b] [--note "<text>"] | --rm`, the lead's or the owner's: one request
// to obor serve (wire.MethodReview). Serve's reply: REVIEW REQUESTED (now | after T-a,T-b)[ (replaces the request of
// <age> ago)], REVIEW REQUEST ENDED / NO REVIEW REQUEST for --rm; with no argument the open request (REVIEW REQUEST
// since <age> …), else a request to start now. Serve starts the review once every --after task merged and the
// reviewer is idle, and tells the lead. FAIL: serve down, its refusal or a usage error (exit 1).
func reviewCmd(args []string) int {
	if len(args) > 0 && args[0] == "close" {
		return reviewCloseCmd(args[1:])
	}
	var c wire.ReviewReq
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--rm" && len(args) == 1:
			c.Clear = true
		case (a == "--after" || a == "--note") && i+1 < len(args) && args[i+1] != "":
			i++
			if a == "--after" {
				c.After = strings.Split(args[i], ",")
			} else {
				c.Note = args[i]
			}
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("review"))
			return 1
		}
	}
	return request(context.Background(), project.OSEnv, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		if err := taskIDs(cfg, c.After...); err != nil {
			return nil, err
		}
		c.Envelope = id
		return &c, nil
	})
}

// reviewCloseCmd is `obor review close "<summary>" [--accept "<line>"]… [--force]`: one request to obor serve
// (wire.MethodReviewClose). Serve's reply: CLOSED <subject>, the commit "RV-<n> closed · <reviewed sha> · approved ·
// <summary>" (with --accept lines: "accepted <n>" in place of "approved"; "abandoned" for a review whose file was never
// written) on the integration branch. --force also closes an unwritten review that covers no task (nothing to delete,
// the reviewed sha its start's head); one that covers tasks is refused until its reviewer writes it. FAIL:
// serve down, its refusal (no open review, no review start for it in the log, the main checkout not on the
// integration branch or dirty outside a BACKLOG.md, docs/accepted.md and the review's files, a linked fix not verified
// without --force, a review with no file without --force, or an unwritten review that covers tasks even with --force)
// or a usage error (exit 1).
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
