//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/checks"
	"obor/internal/testkit"
)

// T-638, T-1023: reviewGit on a fixture repo: the head, the files since a sha, and Closed, a review's own close
// commit since a sha (the close's crash cut): not a later review's whose number starts the same, not a commit that
// only quotes a close subject in its body, none before the range.
func TestReviewGit(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	g := reviewGit{root: p.Root}
	ctx := context.Background()
	if s, err := g.Closed(ctx, "main", "", "RV-4"); err != nil || s != "" {
		t.Fatalf("no close yet: %q, %v", s, err)
	}
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "RV-3 closed · 0000000 · approved · T-1")
	base := p.Git(p.Root, "rev-parse", "HEAD")
	rv4 := "RV-4 closed · " + base[:7] + " · approved · T-8"
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", rv4, "-m", "Covered: T-5, T-6\nStart: "+base)
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "RV-40 closed · "+base[:7]+" · approved · T-9")
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "BACKLOG: notes", "-m", "RV-4 closed · 1111111 · quoted")
	p.Git(p.Root, "checkout", "-q", "-b", "task/T-9")
	p.Write("a.txt", "a\n")
	p.Git(p.Root, "add", "a.txt")
	p.Git(p.Root, "commit", "-q", "-m", "T-9: a")
	p.Git(p.Root, "checkout", "-q", "main")
	p.Git(p.Root, "merge", "-q", "--no-ff", "-m", "Merge T-9: a", "task/T-9")
	head := p.Git(p.Root, "rev-parse", "HEAD")
	for _, c := range []struct{ id, from, want string }{
		{"RV-4", "", rv4}, {"RV-4", base, rv4}, {"RV-3", "", "RV-3 closed · 0000000 · approved · T-1"},
		{"RV-3", base, ""}, {"RV-5", "", ""},
	} {
		if s, err := g.Closed(ctx, "main", c.from, c.id); err != nil || s != c.want {
			t.Errorf("Closed(%s since %q) = %q, %v; want %q", c.id, c.from, s, err, c.want)
		}
	}
	if h, err := g.Head(ctx, "main"); err != nil || h != head {
		t.Errorf("head %q, %v; want %s", h, err, head)
	}
	if f, err := g.Files(ctx, base[:7], head); err != nil || strings.Join(f, ",") != "a.txt" {
		t.Errorf("files %q, %v", f, err)
	}
}

// T-853: reviewGit.Detach adds a missing role worktree detached at the sha, moves one that is there to another sha
// (a commit the role checked out is left behind; T-872: a lock another git holds is waited for), and refuses a dir
// that is no worktree of the repo, nothing forced; with no locked runner it fails.
func TestReviewGitDetach(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	g := reviewGit{root: p.Root, locked: checks.Scratch{Options: checks.Options{Environ: os.Environ()}}.Locked}
	ctx := context.Background()
	first := p.Git(p.Root, "rev-parse", "HEAD")
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "second")
	second := p.Git(p.Root, "rev-parse", "HEAD")
	wt := p.LaneDir("reviewer")
	if p.Exists(wt) {
		t.Fatalf("precondition: %s there", wt)
	}
	at := func() (head, branch string) {
		return p.Git(wt, "rev-parse", "HEAD"), p.Git(wt, "rev-parse", "--abbrev-ref", "HEAD")
	}
	if err := g.Detach(ctx, wt, first); err != nil {
		t.Fatal(err)
	}
	if head, branch := at(); head != first || branch != "HEAD" {
		t.Errorf("added: at %s on %q, want %s detached", head, branch, first)
	}
	p.Git(wt, "checkout", "-q", "--detach", first+"^{}") // the role checked out a commit of its own
	lock := filepath.Join(p.Git(wt, "rev-parse", "--absolute-git-dir"), "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil { // another git's index lock in the role worktree
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- g.Detach(ctx, wt, second) }()
	select {
	case err := <-done:
		t.Fatalf("Detach answered (%v) while the lock was held, want it waiting", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if head, branch := at(); head != second || branch != "HEAD" {
		t.Errorf("moved: at %s on %q, want %s detached", head, branch, second)
	}
	p.Write(p.LaneDir("other")+"/x.txt", "x\n")
	if err := g.Detach(ctx, p.LaneDir("other"), second); err == nil || !strings.Contains(err.Error(), "is not a worktree of") {
		t.Errorf("a plain dir: %v, want refused", err)
	}
	if err := (reviewGit{root: p.Root}).Detach(ctx, wt, first); err == nil ||
		!strings.Contains(err.Error(), "no locked git runner") {
		t.Errorf("no locked runner: %v, want refused", err)
	}
	if head, _ := at(); head != second {
		t.Errorf("refused, the worktree moved to %s, want it at %s", head, second)
	}
}
