package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

const reviewUsage = `FAIL usage: obor review [--after <T-a>[,<T-b>…]] [--note "<text>"] | obor review --rm` +
	` | obor review verify … | obor review close …`

// reviewCmd is `obor review [--after T-a,T-b] [--note "<text>"] | --rm`, the lead's or the owner's (T-638): one request
// to obor serve (wire.MethodReview). Serve's reply: REVIEW REQUESTED (now | after T-a,T-b)[ (replaces the request of
// <age> ago)], REVIEW REQUEST ENDED / NO REVIEW REQUEST for --rm; with no argument the open request (REVIEW REQUEST
// since <age> …), else a request to start now. Serve starts the review once every --after task merged and the
// reviewer is idle, and tells the lead. FAIL: serve down, its refusal or a usage error (exit 1).
func reviewCmd(args []string) int {
	if len(args) > 0 && args[0] == "verify" {
		return reviewVerifyCmd(args[1:])
	}
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
			fmt.Fprintln(out, reviewUsage)
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

// reviewGit is app.ReviewGit on the project's main checkout: git -C <root>.
type reviewGit struct{ root string }

var _ app.ReviewGit = reviewGit{}

func (g reviewGit) Last(ctx context.Context, branch string) (string, error) {
	out, err := g.git(ctx, "log", "-1", "-E", "--grep=^RV-[0-9]+ closed", "--format=%s", "--end-of-options", branch)
	return strings.TrimSpace(out), err
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

func (g reviewGit) Mark(ctx context.Context, sha string) error {
	_, err := g.git(ctx, "update-ref", app.ReviewedRef, sha)
	return err
}

func (g reviewGit) git(ctx context.Context, args ...string) (string, error) {
	return gitIn(ctx, g.root, args...)
}

// span is git's range from..to (from "": all of to).
func span(from, to string) string {
	if from == "" {
		return to
	}
	return from + ".." + to
}

// gitIn runs git -C dir args…: its stdout; an error carries git's stderr.
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
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
