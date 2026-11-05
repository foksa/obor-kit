package main

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/app/wire"
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

// T-638: reviewGit on a fixture repo: the newest RV close, the head, the first-parent merges and files since a sha,
// and the reviewed head marked (T-718: a ref, no worktree).
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
	if h, err := g.Head(ctx, "main"); err != nil || h != head {
		t.Errorf("head %q, %v; want %s", h, err, head)
	}
	if m, err := g.Merges(ctx, base[:7], head); err != nil || len(m) != 1 || m[0] != head+" Merge T-9: a" {
		t.Errorf("merges %q, %v", m, err)
	}
	if f, err := g.Files(ctx, base[:7], head); err != nil || strings.Join(f, ",") != "a.txt" {
		t.Errorf("files %q, %v", f, err)
	}
	if err := g.Mark(ctx, head); err != nil {
		t.Fatal(err)
	}
	if at := p.Git(p.Root, "rev-parse", app.ReviewedRef); at != head {
		t.Errorf("%s at %s, want %s", app.ReviewedRef, at, head)
	}
	if err := g.Mark(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "git update-ref") {
		t.Errorf("a bad sha: %v", err)
	}
}

// T-853: reviewGit.Detach adds a missing role worktree detached at the sha, moves one that is there to another sha
// (a commit the role checked out is left behind), and refuses a dir that is no worktree of the repo, nothing forced.
func TestReviewGitDetach(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	g := reviewGit{root: p.Root}
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
	if err := g.Detach(ctx, wt, second); err != nil {
		t.Fatal(err)
	}
	if head, branch := at(); head != second || branch != "HEAD" {
		t.Errorf("moved: at %s on %q, want %s detached", head, branch, second)
	}
	p.Write(p.LaneDir("other")+"/x.txt", "x\n")
	if err := g.Detach(ctx, p.LaneDir("other"), second); err == nil || !strings.Contains(err.Error(), "is not a worktree of") {
		t.Errorf("a plain dir: %v, want refused", err)
	}
}
