package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// reviewFixture is a project with a reviewer (pj-rev, in the main checkout: T-718), its state dir gitignored (the log's
// database is not the close's to commit), a closed RV-3, then T-5 merged on main: the open review is RV-4 once its
// file is written in the main checkout.
func reviewFixture(t *testing.T) (p *testkit.Project, head string) {
	t.Helper()
	p = testkit.New(t)
	p.Config(testkit.Set("roles.reviewer", ""))
	p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- old line\n\n## Concerns\n")
	p.Write(".gitignore", ".obor/state/\n")
	p.Git(p.Root, "add", "BACKLOG.md", ".gitignore")
	p.Git(p.Root, "commit", "-q", "-m", "backlog")
	base := p.Git(p.Root, "rev-parse", "HEAD")
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "RV-3 closed · "+base[:7]+" · approved · T-1")
	p.Git(p.Root, "checkout", "-q", "-b", "task/T-5")
	p.Write("fix.txt", "fix\n")
	p.Git(p.Root, "add", "fix.txt")
	p.Git(p.Root, "commit", "-q", "-m", "T-5: fix")
	p.Git(p.Root, "checkout", "-q", "main")
	p.Git(p.Root, "merge", "-q", "--no-ff", "-m", "Merge T-5: fix", "task/T-5")
	return p, p.Git(p.Root, "rev-parse", "HEAD")
}

// T-926: obor review close is one review_close request to serve: the summary, each --accept line (spaces folded) and
// --force as the call's fields, serve's reply printed; a usage error sends nothing; serve's refusal is the FAIL line.
func TestReviewCloseCall(t *testing.T) {
	t.Parallel()
	p, _ := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "CLOSED RV-4 closed · abcdef0 · accepted 2 · T-5 (fix)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "close"}, {"review", "close", "x", "--accept"}, {"review", "close", "x", "y"},
		{"review", "close", "x", "--force", "--force"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review close") {
			t.Errorf("obor %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
	rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5  (fix)", "--accept", "rare  case", "--accept", "other", "--force")
	if rc != 0 || stdout != "CLOSED RV-4 closed · abcdef0 · accepted 2 · T-5 (fix)\n" {
		t.Errorf("close: exit %d, %q; want serve's reply", rc, stdout)
	}
	calls, n := serve.requests()
	if n != 1 || len(calls) != 1 || serve.methods[0] != wire.MethodReviewClose {
		t.Fatalf("requests %d %+v (%q), want one review_close", n, calls, serve.methods)
	}
	if c := calls[0]; c.Note != "T-5 (fix)" || !slices.Equal(c.Accept, []string{"rare case", "other"}) || !c.Force ||
		c.CallID == "" || c.Root != p.Root {
		t.Errorf("the call %+v; want the summary, both accept lines, force, a call id, the project", c)
	}

	home, _ = startFakeServe(t, p.Root, wire.Status{}, "", "no review start for RV-4 in the log: nothing changed")
	env = []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	if rc, stdout, _ := run(t, p.Root, env, "review", "close", "fixed"); rc != 1 ||
		stdout != "FAIL no review start for RV-4 in the log: nothing changed\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}

// closeOf is RV-4's close on the fixture: its review file, the reviewed head, the commit CloseCommit writes.
func closeOf(p *testkit.Project, reviewed string, accepts ...string) app.ReviewCommit {
	subject, body := app.CloseCommit("RV-4", reviewed[:7], "T-5 (fix)", []string{"T-5"}, reviewed, len(accepts))
	return app.ReviewCommit{Branch: "main", ID: "RV-4", File: p.Path("docs/reviews/RV-4.md"), Reviewed: reviewed,
		Subject: subject, Body: body, Accepts: accepts}
}

// T-716, T-926: the main checkout's close (reviewGit.Close, serve's adapter) refuses before any change: not on the
// integration branch, a file dirty outside BACKLOG.md and the review's files, --accept with no Open section in
// BACKLOG.md, a reviewed head that is no commit. Then it deletes the review's files (T-718), puts each accepted line at
// the top of BACKLOG.md's Open section naming the review and commits the subject and body as one commit on main's head
// with BACKLOG.md; the checkout is clean after it.
func TestReviewGitClose(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	g := reviewGit{root: p.Root}
	ctx := context.Background()
	review, verify := p.Path("docs/reviews/RV-4.md"), p.Path("docs/reviews/RV-4-verify.md")
	p.Write(review, "# RV-4\n")
	p.Write(verify, "# RV-4 verify\n")
	unchanged := func(what string) {
		t.Helper()
		if !p.Exists(review) || !p.Exists(verify) || p.Git(p.Root, "rev-parse", "HEAD") != head ||
			strings.Contains(p.Read("BACKLOG.md"), "rare case") {
			t.Fatalf("%s: a refused close changed something", what)
		}
	}

	p.Git(p.Root, "checkout", "-q", "-b", "side")
	if err := g.Close(ctx, closeOf(p, head, "rare case")); err == nil ||
		err.Error() != "the main checkout "+p.Root+" is not on main: nothing changed" {
		t.Errorf("on side: %v", err)
	}
	p.Git(p.Root, "checkout", "-q", "main")
	unchanged("on side")

	p.Write("stray.txt", "x\n")
	if err := g.Close(ctx, closeOf(p, head, "rare case")); err == nil ||
		err.Error() != "the main checkout is dirty outside BACKLOG.md and RV-4's files (?? stray.txt): nothing changed" {
		t.Errorf("a stray file: %v", err)
	}
	unchanged("a stray file")
	if err := os.Remove(p.Path("stray.txt")); err != nil {
		t.Fatal(err)
	}

	if err := g.Close(ctx, closeOf(p, strings.Repeat("e", 40), "rare case")); err == nil ||
		!strings.HasPrefix(err.Error(), "no reviewed sha (eeee") || !strings.HasSuffix(err.Error(), ": nothing changed") {
		t.Errorf("a reviewed head that is no commit: %v", err)
	}
	unchanged("no reviewed commit")

	p.Write("BACKLOG.md", "# Backlog\n")
	p.Git(p.Root, "commit", "-q", "-am", "backlog without Open")
	before := p.Git(p.Root, "rev-parse", "HEAD")
	if err := g.Close(ctx, closeOf(p, head, "rare case")); err == nil ||
		err.Error() != "--accept: BACKLOG.md has no \"## Open\" section: nothing changed" {
		t.Errorf("no Open section: %v", err)
	}
	if !p.Exists(review) || p.Git(p.Root, "rev-parse", "HEAD") != before {
		t.Fatal("no Open section: a refused close changed something")
	}
	p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- old line\n\n## Concerns\n")
	p.Git(p.Root, "commit", "-q", "-am", "decisions: a lead doc commit during the review")
	docs := p.Git(p.Root, "rev-parse", "HEAD")

	c := closeOf(p, head, "rare case", "other one")
	if err := g.Close(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got := p.Git(p.Root, "log", "-1", "--format=%s"); got != "RV-4 closed · "+head[:7]+" · accepted 2 · T-5 (fix)" {
		t.Errorf("the close commit %q", got)
	}
	if got, want := p.Git(p.Root, "log", "-1", "--format=%b"), "Covered: T-5\nStart: "+head; got != want {
		t.Errorf("the body %q, want %q", got, want)
	}
	if p.Git(p.Root, "rev-parse", "HEAD~1") != docs {
		t.Errorf("the close is not one commit on main's head")
	}
	if p.Exists(review) || p.Exists(verify) {
		t.Errorf("the main checkout keeps the review's files")
	}
	backlog := p.Git(p.Root, "show", "HEAD:BACKLOG.md")
	if !strings.Contains(backlog, "## Open\n- (RV-4, accepted) rare case\n- (RV-4, accepted) other one\n- old line\n") {
		t.Errorf("BACKLOG.md committed:\n%s", backlog)
	}
	if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the main checkout is dirty after the close: %q", st)
	}
	if last, err := g.Last(ctx, "main"); err != nil || last.Subject != c.Subject || last.Body != c.Body {
		t.Errorf("Last after the close: %+v, %v; want the close commit", last, err)
	}
}

// T-725: the close accepts a dirty BACKLOG.md in the main checkout, unstaged (" M BACKLOG.md", whose leading space a
// trimmed line lost) or staged ("M  BACKLOG.md"), and commits that edit with the close; another modified tracked file
// is refused, named by its untrimmed status line.
func TestReviewGitCloseDirtyBacklog(t *testing.T) {
	t.Parallel()
	for _, staged := range []bool{false, true} {
		name := map[bool]string{false: "unstaged", true: "staged"}[staged]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, head := reviewFixture(t)
			g := reviewGit{root: p.Root}
			ctx := context.Background()
			p.Write("docs/reviews/RV-4.md", "# RV-4\n") // in the main checkout (T-718): untracked, RV-4's own

			p.Write("fix.txt", "changed\n")
			if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=no"); st != "M fix.txt" { // p.Git trims: the line is " M fix.txt"
				t.Fatalf("precondition: fix.txt modified unstaged, status %q", st)
			}
			if err := g.Close(ctx, closeOf(p, head)); err == nil ||
				err.Error() != "the main checkout is dirty outside BACKLOG.md and RV-4's files ( M fix.txt): nothing changed" {
				t.Errorf("a modified fix.txt: %v; want refused", err)
			}
			if p.Git(p.Root, "rev-parse", "HEAD") != head {
				t.Fatalf("a refused close committed")
			}
			p.Git(p.Root, "checkout", "-q", "--", "fix.txt")

			p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- lead's line\n- old line\n\n## Concerns\n")
			want := " M BACKLOG.md"
			if staged {
				p.Git(p.Root, "add", "BACKLOG.md")
				want = "M  BACKLOG.md"
			}
			if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=no"); st != strings.TrimSpace(want) {
				t.Fatalf("precondition: BACKLOG.md %s, status %q", name, st)
			}
			if err := g.Close(ctx, closeOf(p, head, "rare case")); err != nil {
				t.Fatalf("close with BACKLOG.md %s: %v; want accepted", name, err)
			}
			backlog := p.Git(p.Root, "show", "HEAD:BACKLOG.md")
			if !strings.Contains(backlog, "## Open\n- (RV-4, accepted) rare case\n- lead's line\n- old line\n") {
				t.Errorf("BACKLOG.md committed without the %s edit:\n%s", name, backlog)
			}
			if st := p.Git(p.Root, "status", "--porcelain"); st != "" {
				t.Errorf("the main checkout is dirty after the close: %q", st)
			}
		})
	}
}

// T-924: obor review fix is gone (a fix task's spec line Fixes: RV-<n> links it), and T-869: there is no verify by
// hand: each is a usage error of obor review, which names neither; nothing is sent.
func TestReviewFixGone(t *testing.T) {
	t.Parallel()
	p, _ := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "REVIEW REQUESTED (now)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "fix", "T-5,T-6"}, {"review", "verify", "T-5"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review [") ||
			strings.Contains(stdout, "verify") || strings.Contains(stdout, "fix") {
			t.Errorf("obor %q: exit %d, %q; want obor review's usage, naming neither", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
}

// RV-126 #2: a close whose commit fails (a rejecting pre-commit hook, a cancelled context) leaves the review's files and
// BACKLOG.md (content and index) as they were before the call, and returns the error; a close that succeeds removes
// the files only after the commit.
func TestReviewGitCloseCommitFails(t *testing.T) {
	t.Parallel()
	for _, hook := range []string{"reject", "cancel"} {
		for _, backlog := range []string{"clean", "unstaged", "staged"} {
			t.Run(hook+"/"+backlog, func(t *testing.T) {
				t.Parallel()
				p, head := reviewFixture(t)
				g := reviewGit{root: p.Root}
				review, verify := p.Path("docs/reviews/RV-4.md"), p.Path("docs/reviews/RV-4-verify.md")
				p.Write(review, "# RV-4\n")
				p.Write(verify, "# RV-4 verify\n")
				switch backlog {
				case "unstaged", "staged":
					p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- lead's line\n- old line\n\n## Concerns\n")
				}
				if backlog == "staged" {
					p.Git(p.Root, "add", "BACKLOG.md")
				}
				marker := filepath.Join(t.TempDir(), "hook-ran")
				script := "#!/bin/sh\nexec >/dev/null 2>&1\ntouch " + marker + "\nexit 1\n"
				if hook == "cancel" {
					script = "#!/bin/sh\nexec >/dev/null 2>&1\ntouch " + marker + "\nsleep 3\n"
				}
				hookPath := filepath.Join(p.Root, strings.TrimSpace(p.Git(p.Root, "rev-parse", "--git-path", "hooks")), "pre-commit")
				if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				content, status := p.Read("BACKLOG.md"), p.Git(p.Root, "status", "--porcelain", "--untracked-files=all")
				staged := p.Git(p.Root, "diff", "--cached", "--", "BACKLOG.md")
				if (backlog == "staged") != (staged != "") {
					t.Fatalf("precondition: BACKLOG.md %s, staged diff %q", backlog, staged)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if hook == "cancel" {
					go func() {
						for !fileThere(marker) {
							time.Sleep(5 * time.Millisecond)
						}
						cancel()
					}()
				}
				if err := g.Close(ctx, closeOf(p, head, "rare case")); err == nil {
					t.Fatal("the close returned no error although its commit failed")
				}
				if !fileThere(marker) {
					t.Fatal("the commit never ran its hook: the failure is not the commit's")
				}
				if !p.Exists(review) || !p.Exists(verify) {
					t.Errorf("a failed commit removed the review's files")
				}
				if p.Git(p.Root, "rev-parse", "HEAD") != head {
					t.Errorf("a failed commit moved HEAD")
				}
				if got := p.Read("BACKLOG.md"); got != content {
					t.Errorf("BACKLOG.md after the failed close:\n%s\nwant:\n%s", got, content)
				}
				if got := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); got != status {
					t.Errorf("status after the failed close %q, want %q", got, status)
				}
				if got := p.Git(p.Root, "diff", "--cached", "--", "BACKLOG.md"); got != staged {
					t.Errorf("BACKLOG.md's index entry after the failed close:\n%s\nwant:\n%s", got, staged)
				}
			})
		}
	}
}
