package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/port"
)

// The review close's commit plumbing: serve checks the open review against its fold, renders
// the close (internal/app/reviewclose.go) and commits it in the main checkout here (reviewGit.Close); reviewGit.Closed
// is the close's crash cut, its one read.

// Close is port.Review's close on the main checkout (serve calls it admitted): refused before any change when
// the checkout is not on c.Branch, dirty outside BACKLOG.md, docs/accepted.md and c's files, or c.Reviewed is no
// commit; then c's files deleted (its review file and, when there, its verify file), each accepted line appended to
// docs/accepted.md naming the review (the file created when missing), and the commit c.Subject / c.Body with
// BACKLOG.md and docs/accepted.md (a dirty one of either is committed too).
func (g reviewGit) Close(ctx context.Context, c port.ReviewCommit) error {
	root := g.root
	if on, err := g.git(ctx, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || strings.TrimSpace(on) != c.Branch {
		return fmt.Errorf("the main checkout %s is not on %s: nothing changed", root, c.Branch)
	}
	files := []string{c.File}
	if verify := filepath.Join(filepath.Dir(c.File), c.ID+"-verify.md"); fileThere(verify) {
		files = append(files, verify)
	}
	own := map[string]bool{} // the review's files: untracked when reviews.dir is not gitignored
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		own[filepath.ToSlash(rel)] = true
	}
	st, err := g.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	docs := map[string]bool{} // of BACKLOG.md and app.AcceptedFile: the ones the close commit carries
	for _, l := range porcelainLines(st) {
		switch {
		case l[3:] == "BACKLOG.md" || l[3:] == app.AcceptedFile:
			docs[l[3:]] = true
		case strings.HasPrefix(l, "?? ") && own[l[3:]]:
		default:
			return fmt.Errorf("the main checkout is dirty outside BACKLOG.md, %s and %s's files (%s): nothing changed",
				app.AcceptedFile, c.ID, l)
		}
	}
	accepted := ""
	if len(c.Accepts) > 0 {
		b, err := os.ReadFile(filepath.Join(root, app.AcceptedFile))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("--accept: %v", err)
		}
		accepted = app.AcceptedLines(string(b), c.ID, c.Accepts)
		docs[app.AcceptedFile] = true
	}
	if _, err := g.git(ctx, "rev-parse", "--verify", "-q", c.Reviewed+"^{commit}"); err != nil {
		return fmt.Errorf("no reviewed sha (%s, the head the reviewer last finished, is no commit): nothing changed",
			c.Reviewed)
	}
	if len(docs) > 0 {
		var paths []string
		for _, p := range []string{"BACKLOG.md", app.AcceptedFile} {
			if docs[p] {
				paths = append(paths, p)
			}
		}
		return g.commitDocs(ctx, c, paths, accepted, files)
	}
	if _, err = g.git(ctx, "commit", "-q", "--allow-empty", "-m", c.Subject, "-m", c.Body); err != nil {
		return err
	}
	return removeFiles(files)
}

// commitDocs stages paths (BACKLOG.md and/or docs/accepted.md, with accepted written to docs/accepted.md when not ""),
// commits, and only then removes files: a failed commit (a hook, git, a cancelled ctx) restores each path's content
// (a file that did not exist is removed) and its index entry as they were and returns the error, the review files
// untouched (they are gitignored: the only copy of the findings). A crash after the commit leaves the files: inert
// (the next review is RV-<n+1>; the gitignored files are no dirt), serve's recovery (reviewCloseRead) records the
// close and removes nothing.
func (g reviewGit) commitDocs(ctx context.Context, c port.ReviewCommit, paths []string, accepted string, files []string) error {
	type before struct {
		content string
		existed bool
		entry   string
	}
	was := map[string]before{}
	for _, p := range paths {
		path := filepath.Join(g.root, p)
		content, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		entry, err := g.git(ctx, "ls-files", "--stage", "--", p) // "<mode> <blob> <stage>\t<path>", "" untracked
		if err != nil {
			return err
		}
		was[p] = before{content: string(content), existed: readErr == nil, entry: entry}
	}
	restore := func() {
		// not ctx: a cancelled one is what may have failed the commit
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, p := range paths {
			path := filepath.Join(g.root, p)
			if was[p].existed {
				_ = os.WriteFile(path, []byte(was[p].content), 0o644)
			} else {
				_ = os.Remove(path)
			}
			if f := strings.Fields(was[p].entry); len(f) >= 2 {
				_, _ = g.git(rctx, "update-index", "--cacheinfo", f[0]+","+f[1]+","+p)
			} else {
				_, _ = g.git(rctx, "reset", "-q", "--", p)
			}
		}
	}
	if accepted != "" {
		path := filepath.Join(g.root, app.AcceptedFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(accepted), 0o644); err != nil {
			return err
		}
	}
	if _, err := g.git(ctx, append([]string{"add", "--"}, paths...)...); err != nil {
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

// porcelainLines is git status --porcelain's entry lines, untrimmed: the two status columns stay, so each
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

// Closed is the subject of review id's close commit on branch since from, newest first ("": none): port.Review's one
// read of a close, the crash cut between its commit and its fact (reviewCloseRead). git's grep matches any line of a
// message, so a commit that only quotes a close subject is skipped (app.ClosesReview).
func (g reviewGit) Closed(ctx context.Context, branch, from, id string) (string, error) {
	out, err := g.git(ctx, "log", "-E", "--grep=^"+regexp.QuoteMeta(id)+" closed · ", "--format=%s", "--end-of-options",
		span(from, branch))
	for _, s := range lines(out) {
		if app.ClosesReview(s, id) {
			return s, err
		}
	}
	return "", err
}

// fileThere: path is there.
func fileThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
