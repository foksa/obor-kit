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

// reviewGit on a fixture repo: the head, the files since a sha, and Closed, a review's own close
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

// reviewGit.Detach adds a missing role worktree detached at the sha, moves one that is there to another sha
// (a commit the role checked out is left behind; a lock another git holds is waited for), and refuses a dir
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

// reviewGit.Detach moves a role worktree that holds obor's link (a symlink to the main checkout's file) at a path the
// sha tracks: the link is removed first, the worktree gets the tracked file, a line says so. A regular file at that
// path stays in the way: the move fails.
func TestReviewGitDetachRemovesALinkTheShaTracks(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	var out strings.Builder
	g := reviewGit{root: p.Root, locked: checks.Scratch{Options: checks.Options{Environ: os.Environ()}}.Locked, out: &out}
	ctx := context.Background()
	wt := p.LaneDir("reviewer")
	if err := g.Detach(ctx, wt, p.Git(p.Root, "rev-parse", "HEAD")); err != nil {
		t.Fatal(err)
	}
	p.Write("conf/local.env", "from main\n")
	link := filepath.Join(wt, "conf", "local.env")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.Root, "conf", "local.env"), link); err != nil {
		t.Fatal(err)
	}
	if st := p.Git(wt, "status", "--porcelain", "--untracked-files=all", "--", "conf"); st != "?? conf/local.env" {
		t.Fatalf("precondition: the link's status %q, want it untracked", st)
	}
	p.Git(p.Root, "add", "conf/local.env")
	p.Git(p.Root, "commit", "-q", "-m", "track conf/local.env")
	sha := p.Git(p.Root, "rev-parse", "HEAD")
	if err := g.Detach(ctx, wt, sha); err != nil {
		t.Fatalf("move: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("%s: %v, %v; want the tracked file, no link", link, fi, err)
	}
	if want := "removed link conf/local.env: " + sha[:7] + " tracks it\n"; out.String() != want {
		t.Errorf("progress %q, want %q", out.String(), want)
	}
	if head := p.Git(wt, "rev-parse", "HEAD"); head != sha {
		t.Errorf("worktree at %s, want %s", head, sha)
	}

	// a regular file at the path is the worktree's: the move to a commit that tracks it fails
	p.Git(wt, "checkout", "-q", "--detach", "HEAD~1")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("the role's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := g.Detach(ctx, wt, sha); err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Errorf("move over a regular file: %v, want the checkout's refusal", err)
	}
	if b, _ := os.ReadFile(link); string(b) != "the role's own\n" || out.Len() != 0 {
		t.Errorf("file %q, progress %q: want the role's file kept and no line", b, out.String())
	}
}
