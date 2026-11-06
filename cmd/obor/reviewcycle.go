package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// The review cycle's close (T-716; skills/lead/review-brief.md §5; T-926: a call to serve). obor review close sends
// serve the summary, the accepted lines and --force; serve checks the open review against its log, commits the close
// in the main checkout (reviewGit.Close, below) and records fact review.closed, which stops the reviewer
// (internal/app/reviewclose.go).

// reviewCloseCmd is `obor review close "<summary>" [--accept "<line>"]… [--force]`: one request to obor serve
// (wire.MethodReviewClose). Serve's reply: CLOSED <subject>, the commit "RV-<n> closed · <reviewed sha> · approved ·
// <summary>" (with --accept lines: "accepted <n>" in place of "approved", T-909) on the integration branch. FAIL:
// serve down, its refusal (no open review, no review start for it in the log, the main checkout not on the
// integration branch or dirty outside BACKLOG.md and the review's files, a linked fix not verified without --force) or
// a usage error (exit 1).
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
	return request(context.Background(), project.OSEnv, wire.MethodReviewClose, func(_ *config.Config, c *wire.Call) error {
		c.Note, c.Accept, c.Force = summary, accepts, force
		return nil
	})
}

// Close is app.ReviewGit's close on the main checkout (serve calls it admitted, T-926): refused before any change when
// the checkout is not on c.Branch, dirty outside BACKLOG.md and c's files, BACKLOG.md has no Open section for
// c.Accepts, or c.Reviewed is no commit; then c's files deleted (its review file and, when there, its verify file),
// each accepted line at the top of BACKLOG.md's Open section naming the review, and the commit c.Subject / c.Body with
// BACKLOG.md.
func (g reviewGit) Close(ctx context.Context, c app.ReviewCommit) error {
	root := g.root
	if on, err := g.git(ctx, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || strings.TrimSpace(on) != c.Branch {
		return fmt.Errorf("the main checkout %s is not on %s: nothing changed", root, c.Branch)
	}
	files := []string{c.File}
	if verify := filepath.Join(filepath.Dir(c.File), c.ID+"-verify.md"); fileThere(verify) {
		files = append(files, verify)
	}
	own := map[string]bool{} // the review's files: untracked when paths.reviews is not gitignored
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		own[filepath.ToSlash(rel)] = true
	}
	st, err := g.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	backlogDirty := false
	for _, l := range porcelainLines(st) {
		switch {
		case l[3:] == "BACKLOG.md":
			backlogDirty = true
		case strings.HasPrefix(l, "?? ") && own[l[3:]]:
		default:
			return fmt.Errorf("the main checkout is dirty outside BACKLOG.md and %s's files (%s): nothing changed", c.ID, l)
		}
	}
	backlog := ""
	if len(c.Accepts) > 0 {
		b, err := os.ReadFile(filepath.Join(root, "BACKLOG.md"))
		if err != nil {
			return fmt.Errorf("--accept: %v", err)
		}
		if backlog, err = acceptLines(string(b), c.ID, c.Accepts); err != nil {
			return err
		}
	}
	if _, err := g.git(ctx, "rev-parse", "--verify", "-q", c.Reviewed+"^{commit}"); err != nil {
		return fmt.Errorf("no reviewed sha (%s, the head the reviewer last finished, is no commit): nothing changed",
			c.Reviewed)
	}
	if backlogDirty || backlog != "" {
		return g.commitBacklog(ctx, c, backlog, files)
	}
	if _, err = g.git(ctx, "commit", "-q", "--allow-empty", "-m", c.Subject, "-m", c.Body); err != nil {
		return err
	}
	return removeFiles(files)
}

// commitBacklog stages BACKLOG.md (with backlog's accepted lines when not ""), commits, and only then removes files:
// a failed commit (a hook, git, a cancelled ctx) restores BACKLOG.md's content and its index entry as they were and
// returns the error, the review files untouched (they are gitignored: the only copy of the findings). A crash after
// the commit leaves the files: inert (the next review is RV-<n+1>; the gitignored files are no dirt), serve's
// recovery (reviewCloseRead) records the close and removes nothing.
func (g reviewGit) commitBacklog(ctx context.Context, c app.ReviewCommit, backlog string, files []string) error {
	path := filepath.Join(g.root, "BACKLOG.md")
	was, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	entry, err := g.git(ctx, "ls-files", "--stage", "--", "BACKLOG.md") // "<mode> <blob> <stage>\tBACKLOG.md", "" untracked
	if err != nil {
		return err
	}
	restore := func() {
		// not ctx: a cancelled one is what may have failed the commit
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = os.WriteFile(path, was, 0o644)
		if f := strings.Fields(entry); len(f) >= 2 {
			_, _ = g.git(rctx, "update-index", "--cacheinfo", f[0]+","+f[1]+",BACKLOG.md")
		} else {
			_, _ = g.git(rctx, "reset", "-q", "--", "BACKLOG.md")
		}
	}
	if backlog != "" {
		if err := os.WriteFile(path, []byte(backlog), 0o644); err != nil {
			return err
		}
	}
	if _, err := g.git(ctx, "add", "--", "BACKLOG.md"); err != nil {
		restore()
		return err
	}
	if _, err := g.git(ctx, "commit", "-q", "--allow-empty", "-m", c.Subject, "-m", c.Body); err != nil {
		restore()
		return err
	}
	return removeFiles(files)
}

// removeFiles removes the review's files after its close commit is in (a file already gone is fine).
func removeFiles(files []string) error {
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// porcelainLines is git status --porcelain's entry lines, untrimmed (T-725): the two status columns stay, so each
// line's path starts at column 3 (" M BACKLOG.md" is an unstaged edit); a line shorter than "XY p" is dropped.
func porcelainLines(out string) []string {
	var l []string
	for _, s := range strings.Split(out, "\n") {
		if s = strings.TrimRight(s, "\r"); len(s) > 3 {
			l = append(l, s)
		}
	}
	return l
}

// acceptLines is backlog with one line "- (<id>, accepted) <line>" per accepted finding at the top of its "## Open"
// section; no such section: an error.
func acceptLines(backlog, id string, accepts []string) (string, error) {
	ls := strings.SplitAfter(backlog, "\n")
	for i, l := range ls {
		if strings.TrimSpace(l) != "## Open" {
			continue
		}
		var add []string
		for _, a := range accepts {
			add = append(add, "- ("+id+", accepted) "+a+"\n")
		}
		if !strings.HasSuffix(l, "\n") {
			ls[i] += "\n"
		}
		return strings.Join(ls[:i+1], "") + strings.Join(add, "") + strings.Join(ls[i+1:], ""), nil
	}
	return "", errors.New("--accept: BACKLOG.md has no \"## Open\" section: nothing changed")
}

// fileThere: path is there.
func fileThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
