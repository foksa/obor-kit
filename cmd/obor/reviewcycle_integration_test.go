//go:build integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// acceptedHeadText is what a new docs/accepted.md starts with (app's acceptedHead, pinned here: the file's readers see
// it, so a change to it is a change to what they read).
const acceptedHeadText = "# Accepted findings\n\n" +
	"Review findings accepted instead of fixed, one line each, written by `obor review close --accept` (newest\n" +
	"last). Nobody reads this file at session start: look a line up when its symptom shows, and fix it then.\n\n"

// reviewFixture is a project with a reviewer (pj-rev, in the main checkout), its state dir gitignored (the log's
// database is not the close's to commit), a closed RV-3, then T-5 merged on main: the open review is RV-4 once its
// file is written in the main checkout.
func reviewFixture(t *testing.T) (p *testkit.Project, head string) {
	t.Helper()
	p = testkit.New(t)
	reviewTmpl.once.Do(func() {
		p.Config(testkit.Set("agents.roles.reviewer", ""))
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
		reviewTmpl.head = p.Git(p.Root, "rev-parse", "HEAD")
		dir, err := os.MkdirTemp("", "obor-rvfx")
		if err != nil {
			t.Fatal(err)
		}
		afterRun = append(afterRun, func() { os.RemoveAll(dir) })
		reviewTmpl.dir = filepath.Join(dir, "proj")
		if out, err := exec.Command("cp", "-Rp", p.Root, reviewTmpl.dir).CombinedOutput(); err != nil {
			t.Fatalf("copy the review fixture: %v\n%s", err, out)
		}
	})
	if reviewTmpl.dir == "" {
		t.Fatal("the review fixture was not built")
	}
	if !p.Exists("BACKLOG.md") { // not the project that built it: its Root becomes a copy of the built fixture
		if err := os.RemoveAll(p.Root); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("cp", "-Rp", reviewTmpl.dir, p.Root).CombinedOutput(); err != nil {
			t.Fatalf("copy the review fixture: %v\n%s", err, out)
		}
	}
	return p, reviewTmpl.head
}

// reviewTmpl is the fixture built once per test binary (git runs once, not per test): every reviewFixture's Root is a
// copy (cp -Rp keeps the index's mtimes); TestMain removes it (afterRun).
var reviewTmpl struct {
	once      sync.Once
	dir, head string
}

// obor review close's argument check ends before the command reads its directory or sends a request: the rows call
// reviewCloseCmd.
func TestReviewCloseArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"x", "--accept"}, {"x", "y"}, {"x", "--force", "--force"}} {
		if rc, stdout := callCmd(t, reviewCloseCmd, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review close") {
			t.Errorf("review close %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
}

// obor review close is one review_close request to serve: the summary, each --accept line (spaces folded) and
// --force as the call's fields, serve's reply printed; a usage error sends nothing; serve's refusal is the FAIL line.
func TestReviewCloseCall(t *testing.T) {
	t.Parallel()
	p, _ := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "CLOSED RV-4 closed · abcdef0 · accepted 2 · T-5 (fix)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	// a usage error sends nothing (the rows: TestReviewCloseArguments)
	if rc, stdout, _ := run(t, p.Root, env, "review", "close"); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review close") {
		t.Errorf("obor review close: exit %d, %q; want FAIL usage", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
	rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5  (fix)", "--accept", "rare  case", "--accept", "other", "--force")
	if rc != 0 || stdout != "CLOSED RV-4 closed · abcdef0 · accepted 2 · T-5 (fix)\n" {
		t.Errorf("close: exit %d, %q; want serve's reply", rc, stdout)
	}
	calls, n := serve.requests()
	if n != 1 || len(calls) != 1 || serve.sentMethods()[0] != wire.MethodReviewClose {
		t.Fatalf("requests %d %+v (%q), want one review_close", n, calls, serve.sentMethods())
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
func closeOf(p *testkit.Project, reviewed string, accepts ...string) port.ReviewCommit {
	return closeOfID(p, "RV-4", reviewed, accepts...)
}

// closeOfID is review id's close on the fixture (a second close, another id).
func closeOfID(p *testkit.Project, id, reviewed string, accepts ...string) port.ReviewCommit {
	subject, body := app.CloseCommit(id, reviewed[:7], "T-5 (fix)", []string{"T-5"}, reviewed, len(accepts), false)
	return port.ReviewCommit{Branch: "main", ID: id, File: p.Path("docs/reviews/" + id + ".md"), Reviewed: reviewed,
		Subject: subject, Body: body, Accepts: accepts}
}

// the main checkout's close (reviewGit.Close, serve's adapter) refuses before any change: not on
// the integration branch, a tracked file modified or staged outside a BACKLOG.md, docs/accepted.md and the review's
// files, or a reviewed head that is no commit. Then it deletes the review's files, appends each accepted line to
// docs/accepted.md naming the review (created when missing) and commits the subject and body as one commit on main's
// head with it; a BACKLOG.md keeps no accepted line; the checkout is clean after it; a second close appends below the
// first.
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
			strings.Contains(p.Read("BACKLOG.md"), "rare case") || p.Exists(app.AcceptedFile) {
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

	p.Write("fix.txt", "changed\n")
	if err := g.Close(ctx, closeOf(p, head, "rare case")); err == nil ||
		err.Error() != "the main checkout is dirty outside a BACKLOG.md, docs/accepted.md and RV-4's files ( M fix.txt): nothing changed" {
		t.Errorf("a modified tracked file: %v", err)
	}
	unchanged("a modified tracked file")
	p.Write("fix.txt", "fix\n")

	if err := g.Close(ctx, closeOf(p, strings.Repeat("e", 40), "rare case")); err == nil ||
		!strings.HasPrefix(err.Error(), "no reviewed sha (eeee") || !strings.HasSuffix(err.Error(), ": nothing changed") {
		t.Errorf("a reviewed head that is no commit: %v", err)
	}
	unchanged("no reviewed commit")

	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "decisions: a lead doc commit during the review")
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
	if accepted, want := p.Git(p.Root, "show", "HEAD:"+app.AcceptedFile),
		strings.TrimSpace(acceptedHeadText+"- (RV-4, accepted) rare case\n- (RV-4, accepted) other one\n"); accepted != want {
		t.Errorf("docs/accepted.md committed:\n%q\nwant:\n%q", accepted, want)
	}
	if backlog := p.Git(p.Root, "show", "HEAD:BACKLOG.md"); strings.Contains(backlog, "accepted)") {
		t.Errorf("BACKLOG.md committed carries an accepted line:\n%s", backlog)
	}
	if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the main checkout is dirty after the close: %q", st)
	}
	if s, err := g.Closed(ctx, "main", head, "RV-4"); err != nil || s != c.Subject {
		t.Errorf("Closed after the close: %q, %v; want the close commit's subject", s, err)
	}

	// a second close appends below the first one's lines and keeps the head once.
	p.Write("docs/reviews/RV-5.md", "# RV-5\n")
	if err := g.Close(ctx, closeOfID(p, "RV-5", head, "another one")); err != nil {
		t.Fatalf("a second close: %v", err)
	}
	accepted := p.Git(p.Root, "show", "HEAD:"+app.AcceptedFile)
	if want := strings.TrimSpace(acceptedHeadText +
		"- (RV-4, accepted) rare case\n- (RV-4, accepted) other one\n- (RV-5, accepted) another one\n"); accepted != want {
		t.Errorf("docs/accepted.md after a second close:\n%q\nwant:\n%q", accepted, want)
	}
	if n := strings.Count(accepted, "# Accepted findings"); n != 1 {
		t.Errorf("docs/accepted.md has %d heads, want one", n)
	}
}

// the close accepts a dirty BACKLOG.md in the main checkout, at the root or in any directory (here docs/), unstaged
// (" M docs/BACKLOG.md", whose leading space a trimmed line lost) or staged ("M  BACKLOG.md"), and commits that edit
// with the close; another modified tracked file is refused, named by its untrimmed status line. The accepted lines go
// to docs/accepted.md. The two backlogs are dirty in one close, one staged at the root and one unstaged in a
// directory: the status line's two columns and the path's directory are what differ, and every path is read the same.
func TestReviewGitCloseDirtyBacklog(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	g := reviewGit{root: p.Root}
	ctx := context.Background()
	p.Write("docs/reviews/RV-4.md", "# RV-4\n") // in the main checkout: untracked, RV-4's own
	const backlogText = "# Backlog\n\n## Open\n- old line\n\n## Concerns\n"
	p.Write("docs/BACKLOG.md", backlogText) // the project's backlog also lives in a directory: tracked, then dirty below
	p.Git(p.Root, "add", "docs/BACKLOG.md")
	p.Git(p.Root, "commit", "-q", "-m", "backlog at docs/BACKLOG.md")
	before := p.Git(p.Root, "rev-parse", "HEAD")

	p.Write("fix.txt", "changed\n")
	if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=no"); st != "M fix.txt" { // p.Git trims: the line is " M fix.txt"
		t.Fatalf("precondition: fix.txt modified unstaged, status %q", st)
	}
	if err := g.Close(ctx, closeOf(p, head)); err == nil ||
		err.Error() != "the main checkout is dirty outside a BACKLOG.md, docs/accepted.md and RV-4's files ( M fix.txt): nothing changed" {
		t.Errorf("a modified fix.txt: %v; want refused", err)
	}
	if p.Git(p.Root, "rev-parse", "HEAD") != before {
		t.Fatalf("a refused close committed")
	}
	p.Git(p.Root, "checkout", "-q", "--", "fix.txt")

	const edited = "# Backlog\n\n## Open\n- lead's line\n- old line\n\n## Concerns\n"
	p.Write("BACKLOG.md", edited)
	p.Git(p.Root, "add", "BACKLOG.md")
	p.Write("docs/BACKLOG.md", edited)
	if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=no"); st != "M  BACKLOG.md\n M docs/BACKLOG.md" {
		t.Fatalf("precondition: the root backlog staged and docs/'s unstaged, status %q", st)
	}
	if err := g.Close(ctx, closeOf(p, head, "rare case")); err != nil {
		t.Fatalf("close with a staged and an unstaged backlog: %v; want accepted", err)
	}
	for _, path := range []string{"BACKLOG.md", "docs/BACKLOG.md"} {
		backlog := p.Git(p.Root, "show", "HEAD:"+path)
		if !strings.Contains(backlog, "## Open\n- lead's line\n- old line\n") {
			t.Errorf("%s committed without the edit:\n%s", path, backlog)
		}
		if strings.Contains(backlog, "accepted)") {
			t.Errorf("%s committed carries an accepted line:\n%s", path, backlog)
		}
	}
	if accepted, want := p.Git(p.Root, "show", "HEAD:"+app.AcceptedFile),
		strings.TrimSpace(acceptedHeadText+"- (RV-4, accepted) rare case\n"); accepted != want {
		t.Errorf("docs/accepted.md committed:\n%q\nwant:\n%q", accepted, want)
	}
	if st := p.Git(p.Root, "status", "--porcelain"); st != "" {
		t.Errorf("the main checkout is dirty after the close: %q", st)
	}
}

// an untracked file is no dirt, whatever its name: a file outside the allowed paths and an untracked BACKLOG.md in a
// directory leave the close to commit only docs/accepted.md, and each file is still untracked afterwards. A modified
// tracked one still refuses (TestReviewGitClose, TestReviewGitCloseDirtyBacklog). The close with --accept creates
// docs/accepted.md when it is missing; a BACKLOG.md without an "## Open" section is no refusal.
func TestReviewGitCloseUntracked(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	p.Write("BACKLOG.md", "# Backlog\n")
	p.Git(p.Root, "commit", "-q", "-am", "backlog without Open")
	head = p.Git(p.Root, "rev-parse", "HEAD")
	g := reviewGit{root: p.Root}
	if p.Exists(app.AcceptedFile) {
		t.Fatal("precondition: docs/accepted.md exists before the close")
	}
	p.Write("docs/reviews/RV-4.md", "# RV-4\n")
	p.Write("output/x", "art\n")
	p.Write("notes/BACKLOG.md", "# Backlog\n\n## Open\n- a stray backlog\n")
	if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(st, "?? output/x") ||
		!strings.Contains(st, "?? notes/BACKLOG.md") {
		t.Fatalf("precondition: the two files untracked, status %q", st)
	}

	if err := g.Close(context.Background(), closeOf(p, head, "rare case")); err != nil {
		t.Fatalf("a close beside untracked files: %v; want committed", err)
	}
	if got := p.Git(p.Root, "show", "--name-only", "--format=", "HEAD"); got != app.AcceptedFile {
		t.Errorf("the close commit's files %q, want only %s", got, app.AcceptedFile)
	}
	if got := p.Git(p.Root, "rev-parse", "HEAD~1"); got != head {
		t.Errorf("the close is not one commit on main's head: HEAD~1 %s, want %s", got, head)
	}
	if accepted, want := p.Git(p.Root, "show", "HEAD:"+app.AcceptedFile),
		strings.TrimSpace(acceptedHeadText+"- (RV-4, accepted) rare case\n"); accepted != want {
		t.Errorf("docs/accepted.md committed:\n%q\nwant:\n%q", accepted, want)
	}
	if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); st != "?? notes/BACKLOG.md\n?? output/x" {
		t.Errorf("the checkout after the close: %q, want the two untracked files", st)
	}
	if p.Exists("docs/reviews/RV-4.md") {
		t.Errorf("the close kept the review's file")
	}
}

// git status -z: the close reads every changed path whole, so a review's files in a directory whose name has a space,
// a quote and a non-ASCII character commit like any other, a renamed tracked BACKLOG.md (status "R  new\0old") commits
// the rename, both halves, and a tracked BACKLOG.md under a directory named notes* commits alone, not the untracked
// notes2/BACKLOG.md its path would match as a pattern; the checkout is clean after each (the stray one stays untracked).
func TestReviewGitClosePathShapes(t *testing.T) {
	t.Parallel()
	// a directory name with a space, a quote and a non-ASCII character: git status -z gives each path whole, so the
	// three shapes are read the same way.
	t.Run("space, quote and non-ASCII in the directory", func(t *testing.T) {
		t.Parallel()
		p, head := reviewFixture(t)
		g := reviewGit{root: p.Root}
		dir := `docs/ré "view files`
		file, verify := dir+"/RV-4.md", dir+"/RV-4-verify.md"
		p.Write(file, "# RV-4\n")
		p.Write(verify, "# RV-4 verify\n")
		c := closeOf(p, head, "rare case")
		c.File = p.Path(file)
		if err := g.Close(context.Background(), c); err != nil {
			t.Fatalf("close with %q as its dir: %v; want committed", dir, err)
		}
		if p.Exists(file) || p.Exists(verify) {
			t.Errorf("the close kept the review's files")
		}
		if accepted, want := p.Git(p.Root, "show", "HEAD:"+app.AcceptedFile),
			strings.TrimSpace(acceptedHeadText+"- (RV-4, accepted) rare case\n"); accepted != want {
			t.Errorf("docs/accepted.md committed:\n%q\nwant:\n%q", accepted, want)
		}
		if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); st != "" {
			t.Errorf("the main checkout is dirty after the close: %q", st)
		}
	})

	// the path is a pathspec: a dirty backlog under notes* is staged as itself, never as a pattern for the untracked
	// notes2/BACKLOG.md it would match.
	t.Run("glob in the backlog's directory", func(t *testing.T) {
		t.Parallel()
		p, head := reviewFixture(t)
		g := reviewGit{root: p.Root}
		p.Write("docs/reviews/RV-4.md", "# RV-4\n")
		p.Write("notes*/BACKLOG.md", "# Backlog\n\n## Open\n- old line\n\n## Concerns\n")
		p.Git(p.Root, "add", "notes*/BACKLOG.md")
		p.Git(p.Root, "commit", "-q", "-m", "a backlog under notes*")
		p.Write("notes*/BACKLOG.md", "# Backlog\n\n## Open\n- lead's line\n- old line\n\n## Concerns\n")
		p.Write("notes2/BACKLOG.md", "# a stray backlog\n")
		if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(st, "M notes*/BACKLOG.md") ||
			!strings.Contains(st, "?? notes2/BACKLOG.md") {
			t.Fatalf("precondition: the backlog under notes* dirty and notes2's untracked, status %q", st)
		}
		if err := g.Close(context.Background(), closeOf(p, head, "rare case")); err != nil {
			t.Fatalf("close with a backlog under notes*: %v; want committed", err)
		}
		if got, want := p.Git(p.Root, "show", "--name-only", "--format=", "HEAD"),
			app.AcceptedFile+"\nnotes*/BACKLOG.md"; got != want {
			t.Errorf("the close commit's files:\n%s\nwant only:\n%s", got, want)
		}
		if got := p.Git(p.Root, "show", "HEAD:notes*/BACKLOG.md"); !strings.Contains(got, "- lead's line") {
			t.Errorf("the close did not commit the backlog's edit:\n%s", got)
		}
		if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); st != "?? notes2/BACKLOG.md" {
			t.Errorf("the checkout after the close: %q, want the stray file untracked", st)
		}
	})

	t.Run("rename", func(t *testing.T) {
		t.Parallel()
		p, head := reviewFixture(t)
		g := reviewGit{root: p.Root}
		p.Write("docs/reviews/RV-4.md", "# RV-4\n")
		if err := os.MkdirAll(p.Path("notes"), 0o755); err != nil {
			t.Fatal(err)
		}
		p.Git(p.Root, "mv", "BACKLOG.md", "notes/BACKLOG.md")
		if st := p.Git(p.Root, "status", "--porcelain"); !strings.Contains(st, "R  BACKLOG.md -> notes/BACKLOG.md") {
			t.Fatalf("precondition: BACKLOG.md renamed, status %q", st)
		}
		if err := g.Close(context.Background(), closeOf(p, head, "rare case")); err != nil {
			t.Fatalf("close with a renamed BACKLOG.md: %v; want committed", err)
		}
		if got := p.Git(p.Root, "show", "HEAD:notes/BACKLOG.md"); !strings.Contains(got, "# Backlog") {
			t.Errorf("notes/BACKLOG.md committed:\n%s", got)
		}
		if out := p.Git(p.Root, "ls-tree", "-r", "--name-only", "HEAD"); slices.Contains(strings.Split(out, "\n"), "BACKLOG.md") {
			t.Errorf("the old BACKLOG.md is still committed:\n%s", out)
		}
		if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=all"); st != "" {
			t.Errorf("the main checkout is dirty after the close: %q", st)
		}
	})
	// the allowance is for a BACKLOG.md on both halves: another tracked file renamed onto that name is dirty.
	t.Run("a file renamed onto BACKLOG.md", func(t *testing.T) {
		t.Parallel()
		p, head := reviewFixture(t)
		g := reviewGit{root: p.Root}
		p.Write("docs/reviews/RV-4.md", "# RV-4\n")
		if err := os.MkdirAll(p.Path("docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		p.Git(p.Root, "mv", "fix.txt", "docs/BACKLOG.md")
		if st := p.Git(p.Root, "status", "--porcelain"); !strings.Contains(st, "R  fix.txt -> docs/BACKLOG.md") {
			t.Fatalf("precondition: fix.txt renamed, status %q", st)
		}
		if err := g.Close(context.Background(), closeOf(p, head)); err == nil ||
			err.Error() != "the main checkout is dirty outside a BACKLOG.md, docs/accepted.md and RV-4's files (R  fix.txt -> docs/BACKLOG.md): nothing changed" {
			t.Errorf("a file renamed onto BACKLOG.md: %v; want refused", err)
		}
		if got := p.Git(p.Root, "rev-parse", "HEAD"); got != head {
			t.Errorf("a refused close committed: HEAD %s, want %s", got, head)
		}
		if !p.Exists("docs/reviews/RV-4.md") || p.Exists(app.AcceptedFile) {
			t.Errorf("a refused close changed the checkout")
		}
	})
}

// obor review fix is no subcommand (a fix task's spec line Fixes: RV-<n> links it), and there is no verify
// by hand: each is a usage error of obor review, which names neither; nothing is sent.
func TestReviewFixGone(t *testing.T) {
	t.Parallel()
	p, _ := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "REVIEW REQUESTED (now)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "fix", "T-5,T-6"}, {"review", "verify", "T-5"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review") ||
			strings.Contains(stdout, "verify") || strings.Contains(stdout, "fix") {
			t.Errorf("obor %q: exit %d, %q; want obor review's usage, naming neither", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
}

// a close whose commit fails (a rejecting pre-commit hook, a cancelled context) leaves the review's
// files, BACKLOG.md and docs/accepted.md (content and index) as they were before the call, and returns the error; a
// docs/accepted.md the close creates is removed again, nothing of it staged. A close that succeeds removes the
// files only after the commit.
// Each path's restore is its own (a BACKLOG.md staged or not, an accepted file there or created) and the hook's
// failure is the commit's either way, so two cases cover the four states and the two failures: a rejecting hook with
// a staged BACKLOG.md and an accepted file that is there, a cancelled context with an unstaged BACKLOG.md and an
// accepted file the close creates.
func TestReviewGitCloseCommitFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, hook, backlog string
		accepted            bool // docs/accepted.md is committed before the close; else the close creates it
	}{
		{"reject/staged BACKLOG.md, accepted file there", "reject", "staged", true},
		{"cancel/unstaged BACKLOG.md, accepted file created", "cancel", "unstaged", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, head := reviewFixture(t)
			g := reviewGit{root: p.Root}
			review, verify := p.Path("docs/reviews/RV-4.md"), p.Path("docs/reviews/RV-4-verify.md")
			p.Write(review, "# RV-4\n")
			p.Write(verify, "# RV-4 verify\n")
			if tc.accepted {
				p.Write(app.AcceptedFile, acceptedHeadText+"- (RV-3, accepted) an old line\n")
				p.Git(p.Root, "add", app.AcceptedFile)
				p.Git(p.Root, "commit", "-q", "-m", "accepted findings")
			}
			p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- lead's line\n- old line\n\n## Concerns\n")
			if tc.backlog == "staged" {
				p.Git(p.Root, "add", "BACKLOG.md")
			}
			marker := filepath.Join(t.TempDir(), "hook-ran")
			script := "#!/bin/sh\nexec >/dev/null 2>&1\ntouch " + marker + "\nexit 1\n"
			if tc.hook == "cancel" {
				script = "#!/bin/sh\nexec >/dev/null 2>&1\ntouch " + marker + "\nsleep 3\n"
			}
			hookPath := filepath.Join(p.Root, strings.TrimSpace(p.Git(p.Root, "rev-parse", "--git-path", "hooks")), "pre-commit")
			if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			content, status := p.Read("BACKLOG.md"), p.Git(p.Root, "status", "--porcelain", "--untracked-files=all")
			staged := p.Git(p.Root, "diff", "--cached", "--", "BACKLOG.md")
			if (tc.backlog == "staged") != (staged != "") {
				t.Fatalf("precondition: BACKLOG.md %s, staged diff %q", tc.backlog, staged)
			}
			acceptedContent := p.Read(app.AcceptedFile)
			acceptedStaged := p.Git(p.Root, "diff", "--cached", "--", app.AcceptedFile)
			if tc.accepted != p.Exists(app.AcceptedFile) {
				t.Fatalf("precondition: docs/accepted.md exists %v, want %v", p.Exists(app.AcceptedFile), tc.accepted)
			}
			before := p.Git(p.Root, "rev-parse", "HEAD")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.hook == "cancel" {
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
			if p.Git(p.Root, "rev-parse", "HEAD") != before {
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
			if got := p.Read(app.AcceptedFile); got != acceptedContent {
				t.Errorf("docs/accepted.md after the failed close:\n%s\nwant:\n%s", got, acceptedContent)
			}
			if got := p.Git(p.Root, "diff", "--cached", "--", app.AcceptedFile); got != acceptedStaged {
				t.Errorf("docs/accepted.md's index entry after the failed close:\n%s\nwant:\n%s", got, acceptedStaged)
			}
			if !tc.accepted && p.Exists(app.AcceptedFile) {
				t.Errorf("a failed commit left the docs/accepted.md it created")
			}
		})
	}
}
