package main

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// reviewCmd is `obor review [--after T-a,T-b] [--note "<text>"] | --rm`, the lead's or the owner's (T-638): one request
// to obor serve (wire.MethodReview). Serve's reply: REVIEW REQUESTED (now | after T-a,T-b)[ (replaces the request of
// <age> ago)], REVIEW REQUEST ENDED / NO REVIEW REQUEST for --rm; with no argument the open request (REVIEW REQUEST
// since <age> …), else a request to start now. Serve starts the review once every --after task merged and the
// reviewer is idle, and tells the lead. FAIL: serve down, its refusal or a usage error (exit 1).
func reviewCmd(args []string) int {
	if len(args) > 0 && args[0] == "close" {
		return reviewCloseCmd(args[1:])
	}
	var c wire.Call
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
	return request(context.Background(), project.OSEnv, wire.MethodReview, func(cfg *config.Config, call *wire.Call) error {
		if err := taskIDs(cfg, c.After...); err != nil {
			return err
		}
		call.After, call.Note, call.Clear = c.After, c.Note, c.Clear
		return nil
	})
}

// reviewGit is app.ReviewGit on the project's main checkout: git -C <root>. locked runs Detach's checkout as serve's
// own git calls run (checks.Scratch.Locked: a lock another git holds is waited for, T-872); nil: Detach fails.
type reviewGit struct {
	root   string
	locked func(ctx context.Context, dir string, args ...string) (string, error)
}

var _ app.ReviewGit = reviewGit{}

func (g reviewGit) Last(ctx context.Context, branch string) (app.ReviewClose, error) {
	out, err := g.git(ctx, "log", "-1", "-E", "--grep="+app.CloseCommitGrep, "--format=%B", "--end-of-options", branch)
	if err != nil {
		return app.ReviewClose{}, err
	}
	subject, body, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return app.ReviewClose{Subject: subject, Body: strings.TrimSpace(body)}, nil
}

func (g reviewGit) Head(ctx context.Context, branch string) (string, error) {
	out, err := g.git(ctx, "rev-parse", "--verify", "--end-of-options", branch+"^{commit}")
	return strings.TrimSpace(out), err
}

func (g reviewGit) Merges(ctx context.Context, from, to string) ([]string, error) {
	out, err := g.git(ctx, "log", "--reverse", "--first-parent", "--merges", "--format=%H %s", "--end-of-options", span(from, to))
	return lines(out), err
}

func (g reviewGit) Files(ctx context.Context, from, to string) ([]string, error) {
	if from == "" {
		out, err := g.git(ctx, "ls-tree", "-r", "--name-only", "--end-of-options", to)
		return lines(out), err
	}
	out, err := g.git(ctx, "diff", "--name-only", "--end-of-options", from, to)
	return lines(out), err
}

// Detach puts the role worktree dir on sha, detached (T-853): added at it when dir is missing (project.AddWorktree),
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
