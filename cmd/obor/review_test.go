package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/testkit"
)

// T-638: obor review [--after T-a,T-b] [--note …] | --rm is one review request as the caller; a usage error or a bad
// task id sends nothing (FAIL on stdout, exit 1).
func TestReviewRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "REVIEW REQUESTED", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "--rm", "--after", "T-1"}, {"review", "--after"}, {"review", "--note", ""},
		{"review", "T-1"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "--after", "T-1,nope"); rc != 1 || !strings.HasPrefix(stdout, "FAIL nope is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "--after", "T-1,T-2", "--note", "skip docs"); rc != 0 || stdout != "REVIEW REQUESTED\n" {
		t.Errorf("review --after: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env, "review")
	run(t, p.Root, env, "review", "--rm")
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	if c := calls[0]; strings.Join(c.After, ",") != "T-1,T-2" || c.Note != "skip docs" || c.Clear || c.Role != "lead" || c.CallID == "" {
		t.Errorf("request 1: %+v", c)
	}
	if c := calls[1]; len(c.After) != 0 || c.Note != "" || c.Clear {
		t.Errorf("request 2 (no argument): %+v", c)
	}
	if c := calls[2]; !c.Clear {
		t.Errorf("request 3 (--rm): %+v", c)
	}
}

// T-638: reviewGit on a fixture repo: the newest RV close, the head, the first-parent merges and files since a sha.
func TestReviewGit(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	g := reviewGit{root: p.Root}
	ctx := context.Background()
	if last, err := g.Last(ctx, "main"); err != nil || last != (app.ReviewClose{}) {
		t.Fatalf("no close yet: %+v, %v", last, err)
	}
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "RV-3 closed · 0000000 · approved")
	base := p.Git(p.Root, "rev-parse", "HEAD")
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "RV-4 closed · "+base[:7]+" · approved · T-8",
		"-m", "Covered: T-5, T-6\nStart: "+base)
	p.Git(p.Root, "checkout", "-q", "-b", "task/T-9")
	p.Write("a.txt", "a\n")
	p.Git(p.Root, "add", "a.txt")
	p.Git(p.Root, "commit", "-q", "-m", "T-9: a")
	p.Git(p.Root, "checkout", "-q", "main")
	p.Git(p.Root, "merge", "-q", "--no-ff", "-m", "Merge T-9: a", "task/T-9")
	head := p.Git(p.Root, "rev-parse", "HEAD")
	if last, err := g.Last(ctx, "main"); err != nil || last.Subject != "RV-4 closed · "+base[:7]+" · approved · T-8" ||
		last.Body != "Covered: T-5, T-6\nStart: "+base {
		t.Errorf("last: %+v, %v", last, err)
	}
	// T-985: the close commits of a range, newest first, each its subject and body.
	all, err := g.Closes(ctx, "", head)
	if err != nil || len(all) != 2 || all[0].Subject != "RV-4 closed · "+base[:7]+" · approved · T-8" ||
		all[0].Body != "Covered: T-5, T-6\nStart: "+base || all[1] != (app.ReviewClose{Subject: "RV-3 closed · 0000000 · approved"}) {
		t.Errorf("closes of all of main: %+v, %v; want RV-4's then RV-3's", all, err)
	}
	if since, err := g.Closes(ctx, base, head); err != nil || len(since) != 1 || since[0].Subject != all[0].Subject {
		t.Errorf("closes since RV-3's: %+v, %v; want RV-4's only", since, err)
	}
	if none, err := g.Closes(ctx, p.Git(p.Root, "rev-parse", "HEAD^"), head); err != nil || len(none) != 0 {
		t.Errorf("closes of the merge alone: %+v, %v; want none", none, err)
	}
	if h, err := g.Head(ctx, "main"); err != nil || h != head {
		t.Errorf("head %q, %v; want %s", h, err, head)
	}
	if m, err := g.Merges(ctx, base[:7], head); err != nil || len(m) != 1 || m[0] != head+" Merge T-9: a" {
		t.Errorf("merges %q, %v", m, err)
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
