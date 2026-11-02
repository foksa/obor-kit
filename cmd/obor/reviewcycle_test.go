package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// reviewFixture is a project with a reviewer (pj-rev, worktree proj-wt/review detached at the first commit), a closed
// RV-3, then T-5 merged on main: the open review is RV-4 once its file is written.
func reviewFixture(t *testing.T) (p *testkit.Project, wt, head string) {
	t.Helper()
	p = testkit.New(t)
	p.Config(testkit.Set("roles.reviewer", `{"worktree":"review"}`))
	p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- old line\n\n## Concerns\n")
	p.Git(p.Root, "add", "BACKLOG.md")
	p.Git(p.Root, "commit", "-q", "-m", "backlog")
	wt = filepath.Join(p.Base, "proj-wt", "review")
	p.Git(p.Root, "worktree", "add", "-q", "--detach", wt, "main")
	base := p.Git(p.Root, "rev-parse", "HEAD")
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "RV-3 closed · "+base[:7]+" · approved · T-1")
	p.Git(p.Root, "checkout", "-q", "-b", "task/T-5")
	p.Write("fix.txt", "fix\n")
	p.Git(p.Root, "add", "fix.txt")
	p.Git(p.Root, "commit", "-q", "-m", "T-5: fix")
	p.Git(p.Root, "checkout", "-q", "main")
	p.Git(p.Root, "merge", "-q", "--no-ff", "-m", "Merge T-5: fix", "task/T-5")
	return p, wt, p.Git(p.Root, "rev-parse", "HEAD")
}

// T-716: obor review verify detaches the review worktree at the head and briefs the reviewer with §4's text built from
// the open RV, the fix tasks' merge shas, the findings and the verify file; with no open review, or a task not merged
// since the previous review, it refuses and sends nothing.
func TestReviewVerify(t *testing.T) {
	t.Parallel()
	p, wt, head := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "BRIEFED pj-rev", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	if at := p.Git(wt, "rev-parse", "HEAD"); at == head {
		t.Fatalf("precondition: the review worktree is already at the head %s", head)
	}
	rc, stdout, _ := run(t, p.Root, env, "review", "verify", "T-5")
	if rc != 1 || !strings.HasPrefix(stdout, "FAIL no open review: no docs/reviews/RV-4.md in ") {
		t.Errorf("no review file: exit %d, %q; want FAIL no open review", rc, stdout)
	}
	for _, args := range [][]string{{"review", "verify"}, {"review", "verify", "T-5", "--findings"}, {"review", "verify", "--note", "x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review verify") {
			t.Errorf("obor %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
	p.Write(filepath.Join(wt, "docs/reviews/RV-4.md"), "# RV-4\n")
	if rc, stdout, _ := run(t, p.Root, env, "review", "verify", "T-6"); rc != 1 || stdout != "FAIL T-6 has no merge on main since RV-3\n" {
		t.Errorf("a task not merged: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused verify sent %d requests", n)
	}
	if at := p.Git(wt, "rev-parse", "HEAD"); at == head {
		t.Errorf("a refused verify detached the review worktree at the head")
	}
	rc, stdout, _ = run(t, p.Root, env, "review", "verify", "T-5", "--findings", "1,3", "--note", "#2 accepted")
	if rc != 0 || stdout != "BRIEFED pj-rev\n" {
		t.Fatalf("verify: exit %d, %q", rc, stdout)
	}
	if at := p.Git(wt, "rev-parse", "HEAD"); at != head {
		t.Errorf("the review worktree is at %s, want the head %s", at, head)
	}
	if !p.Exists(filepath.Join(wt, "docs/reviews/RV-4.md")) {
		t.Errorf("the detach lost the review file")
	}
	calls, _ := serve.requests()
	want := "The RV-4 findings were fixed in T-5 (" + head[:7] + "); main is at " + head[:7] + ". Re-run your probes for" +
		" #1, #3 and report fixed / incomplete / regressed per finding. Write docs/reviews/RV-4-verify.md. Do not commit." +
		"\nLead's note: #2 accepted"
	if len(calls) != 1 || serve.methods[0] != wire.MethodBrief || calls[0].To != "pj-rev" || calls[0].Note != want {
		t.Fatalf("the brief: %q %+v\nwant %q", serve.methods, calls, want)
	}
	if rc, _, _ := run(t, p.Root, env, "review", "verify", "T-5"); rc != 0 {
		t.Fatalf("verify without --findings: exit %d", rc)
	}
	if calls, _ = serve.requests(); len(calls) != 2 || !strings.Contains(calls[1].Note, "Re-run your probes for every finding and") {
		t.Errorf("no --findings: every finding: %+v", calls)
	}
}

// T-716: obor review close refuses a dirty main checkout (nothing changed); then it deletes the review's files (the
// review worktree is clean), appends each --accept to BACKLOG.md's Open section naming the RV, commits "RV-n closed ·
// <reviewed sha> · approved · <summary>" on main with BACKLOG.md, and stops the reviewer.
func TestReviewClose(t *testing.T) {
	t.Parallel()
	p, wt, head := reviewFixture(t)
	p.Git(wt, "checkout", "-q", "--detach", head) // as verify left it
	f := p.Herdr()
	f.Shell("w1:p3", "pj-rev", wt)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
		"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
	review, verify := filepath.Join(wt, "docs/reviews/RV-4.md"), filepath.Join(wt, "docs/reviews/RV-4-verify.md")
	p.Write(review, "# RV-4\n")
	p.Write(verify, "# RV-4 verify\n")
	for _, args := range [][]string{{"review", "close"}, {"review", "close", "x", "--accept"}, {"review", "close", "x", "y"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review close") {
			t.Errorf("obor %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
	p.Write("stray.txt", "x\n")
	rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5 (fix)", "--accept", "rare case")
	if rc != 1 || stdout != "FAIL the main checkout is dirty outside BACKLOG.md (?? stray.txt): nothing changed\n" {
		t.Errorf("a dirty main checkout: exit %d, %q", rc, stdout)
	}
	if !p.Exists(review) || !p.Exists(verify) || p.Git(p.Root, "rev-parse", "HEAD") != head ||
		strings.Contains(p.Read("BACKLOG.md"), "rare case") {
		t.Fatalf("a refused close changed something")
	}
	if err := os.Remove(p.Path("stray.txt")); err != nil {
		t.Fatal(err)
	}
	rc, stdout, _ = run(t, p.Root, env, "review", "close", "T-5 (fix)", "--accept", "rare case", "--accept", "other  one")
	subject := "RV-4 closed · " + head[:7] + " · approved · T-5 (fix)"
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || lines[0] != "CLOSED "+subject || lines[len(lines)-1] != "STOPPED pj-rev (pane w1:p3, already at a shell)" {
		t.Fatalf("close: exit %d\n%s", rc, stdout)
	}
	if got := p.Git(p.Root, "log", "-1", "--format=%s"); got != subject {
		t.Errorf("the close commit %q, want %q", got, subject)
	}
	if p.Git(p.Root, "rev-parse", "HEAD~1") != head {
		t.Errorf("the close is not one commit on main's head")
	}
	if p.Exists(review) || p.Exists(verify) || p.Git(wt, "status", "--porcelain", "--untracked-files=all") != "" {
		t.Errorf("the review worktree keeps the review's files")
	}
	backlog := p.Git(p.Root, "show", "HEAD:BACKLOG.md")
	if !strings.Contains(backlog, "## Open\n- (RV-4, accepted) rare case\n- (RV-4, accepted) other one\n- old line\n") {
		t.Errorf("BACKLOG.md committed:\n%s", backlog)
	}
	if st := p.Git(p.Root, "status", "--porcelain"); st != "" {
		t.Errorf("the main checkout is dirty after the close: %q", st)
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "close", "again"); rc != 1 || !strings.HasPrefix(stdout, "FAIL no open review: no docs/reviews/RV-5.md") {
		t.Errorf("a second close: exit %d, %q", rc, stdout)
	}
}
