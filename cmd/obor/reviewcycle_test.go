package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"obor/internal/testkit"
)

// reviewFixture is a project with a reviewer (pj-rev, in the main checkout: T-718), a closed RV-3, then T-5 merged on
// main: the open review is RV-4 once its file is written in the main checkout.
func reviewFixture(t *testing.T) (p *testkit.Project, head string) {
	t.Helper()
	p = testkit.New(t)
	p.Config(testkit.Set("roles.reviewer", ""))
	p.Write("BACKLOG.md", "# Backlog\n\n## Open\n- old line\n\n## Concerns\n")
	p.Git(p.Root, "add", "BACKLOG.md")
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

// T-716, T-730: obor review verify only asks serve (wire.MethodVerify: the fix tasks, the findings, the note) and
// marks nothing itself: serve reads and marks the head at its admission point (internal/app). A usage error or a bad
// finding number sends nothing.
func TestReviewVerify(t *testing.T) {
	t.Parallel()
	p, _ := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "BRIEFED pj-rev", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	marked := func() string { return p.Git(p.Root, "for-each-ref", "--format=%(objectname)", app.ReviewedRef) }
	for _, args := range [][]string{{"review", "verify"}, {"review", "verify", "T-5", "--findings"}, {"review", "verify", "--note", "x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review verify") {
			t.Errorf("obor %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "verify", "T-5", "--findings", "0"); rc != 1 || !strings.HasPrefix(stdout, "FAIL --findings:") {
		t.Errorf("finding 0: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused verify sent %d requests", n)
	}
	rc, stdout, _ := run(t, p.Root, env, "review", "verify", "T-5,T-6", "--findings", "1,3", "--note", "#2 accepted")
	if rc != 0 || stdout != "BRIEFED pj-rev\n" {
		t.Fatalf("verify: exit %d, %q", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.methods[0] != wire.MethodVerify || strings.Join(calls[0].After, ",") != "T-5,T-6" ||
		strings.Join(calls[0].Findings, ",") != "1,3" || calls[0].Note != "#2 accepted" || calls[0].CallID == "" {
		t.Fatalf("the request: %q %+v", serve.methods, calls)
	}
	if at := marked(); at != "" {
		t.Errorf("the CLI marked %s at %s: serve marks it", app.ReviewedRef, at)
	}
	home2, _ := startFakeServe(t, p.Root, wire.Status{}, "", "no open review: no docs/reviews/RV-4.md in "+p.Root)
	env2 := []string{"OBOR_STATE_HOME=" + home2, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	if rc, stdout, _ := run(t, p.Root, env2, "review", "verify", "T-5"); rc != 1 || !strings.HasPrefix(stdout, "FAIL no open review: ") {
		t.Errorf("serve refuses: exit %d, %q; want its FAIL", rc, stdout)
	}
}

// T-716: obor review close refuses a dirty main checkout or no reviewed sha (nothing changed); then it deletes the
// review's files from the main checkout (T-718), appends each --accept to BACKLOG.md's Open section naming the RV,
// commits "RV-n closed · <reviewed sha> · approved · <summary>" on main with BACKLOG.md, and stops the reviewer. The
// reviewed sha is the marked one, not main's head: the lead's doc commit during the review is no part of it.
func TestReviewClose(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	f := p.Herdr()
	f.Shell("w1:p3", "pj-rev", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
		"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
	review, verify := p.Path("docs/reviews/RV-4.md"), p.Path("docs/reviews/RV-4-verify.md")
	p.Write(review, "# RV-4\n")
	p.Write(verify, "# RV-4 verify\n")
	for _, args := range [][]string{{"review", "close"}, {"review", "close", "x", "--accept"}, {"review", "close", "x", "y"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review close") {
			t.Errorf("obor %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
	rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5 (fix)", "--accept", "rare case")
	if rc != 1 || !strings.HasPrefix(stdout, "FAIL no reviewed sha (refs/obor/reviewed, ") {
		t.Errorf("no reviewed sha: exit %d, %q", rc, stdout)
	}
	p.Git(p.Root, "update-ref", app.ReviewedRef, head) // as the review's start (or verify) left it
	p.Write("stray.txt", "x\n")
	rc, stdout, _ = run(t, p.Root, env, "review", "close", "T-5 (fix)", "--accept", "rare case")
	if rc != 1 || stdout != "FAIL the main checkout is dirty outside BACKLOG.md and RV-4's files (?? stray.txt): nothing changed\n" {
		t.Errorf("a dirty main checkout: exit %d, %q", rc, stdout)
	}
	if !p.Exists(review) || !p.Exists(verify) || p.Git(p.Root, "rev-parse", "HEAD") != head ||
		strings.Contains(p.Read("BACKLOG.md"), "rare case") {
		t.Fatalf("a refused close changed something")
	}
	if err := os.Remove(p.Path("stray.txt")); err != nil {
		t.Fatal(err)
	}
	p.Git(p.Root, "commit", "-q", "--allow-empty", "-m", "decisions: a lead doc commit during the review")
	docs := p.Git(p.Root, "rev-parse", "HEAD")
	rc, stdout, _ = run(t, p.Root, env, "review", "close", "T-5 (fix)", "--accept", "rare case", "--accept", "other  one")
	subject := "RV-4 closed · " + head[:7] + " · approved · T-5 (fix)"
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || lines[0] != "CLOSED "+subject || lines[len(lines)-1] != "STOPPED pj-rev (pane w1:p3, already at a shell)" {
		t.Fatalf("close: exit %d\n%s", rc, stdout)
	}
	if got := p.Git(p.Root, "log", "-1", "--format=%s"); got != subject {
		t.Errorf("the close commit %q, want %q", got, subject)
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
	if rc, stdout, _ := run(t, p.Root, env, "review", "close", "again"); rc != 1 || !strings.HasPrefix(stdout, "FAIL no open review: no docs/reviews/RV-5.md") {
		t.Errorf("a second close: exit %d, %q", rc, stdout)
	}
}

// T-725: obor review close accepts a dirty BACKLOG.md in the main checkout, unstaged (" M BACKLOG.md", whose leading
// space a trimmed line lost) or staged ("M  BACKLOG.md"), and commits that edit with the close; another modified
// tracked file is refused, named by its untrimmed status line.
func TestReviewCloseDirtyBacklog(t *testing.T) {
	t.Parallel()
	for _, staged := range []bool{false, true} {
		name := map[bool]string{false: "unstaged", true: "staged"}[staged]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, head := reviewFixture(t)
			p.Git(p.Root, "update-ref", app.ReviewedRef, head) // as the review's start left it (T-718)
			f := p.Herdr()
			f.Shell("w1:p3", "pj-rev", p.Root)
			srv := herdrtest.Serve(t, f)
			env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
				"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
				"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
			p.Write("docs/reviews/RV-4.md", "# RV-4\n") // in the main checkout (T-718): untracked, RV-4's own

			p.Write("fix.txt", "changed\n")
			if st := p.Git(p.Root, "status", "--porcelain", "--untracked-files=no"); st != "M fix.txt" { // p.Git trims: the line is " M fix.txt"
				t.Fatalf("precondition: fix.txt modified unstaged, status %q", st)
			}
			rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5 (fix)")
			if rc != 1 || stdout != "FAIL the main checkout is dirty outside BACKLOG.md and RV-4's files ( M fix.txt): nothing changed\n" {
				t.Errorf("a modified fix.txt: exit %d, %q; want refused", rc, stdout)
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
			rc, stdout, _ = run(t, p.Root, env, "review", "close", "T-5 (fix)", "--accept", "rare case")
			if rc != 0 || !strings.HasPrefix(stdout, "CLOSED RV-4 closed · ") {
				t.Fatalf("close with BACKLOG.md %s: exit %d, %q; want accepted", name, rc, stdout)
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

// T-822: obor review close commits the cycle's Covered set (the review's in-scope tasks plus each verify's) and the sha
// its review started at in the commit body; the subject keeps its form, so an old reader is unaffected.
func TestReviewCloseCovered(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	p.Write(".gitignore", ".obor/state/\n") // the log's database is not the close's to commit
	p.Git(p.Root, "add", ".gitignore")
	p.Git(p.Root, "commit", "-q", "-m", "gitignore: the state dir")
	p.Git(p.Root, "update-ref", app.ReviewedRef, head)
	start := app.ReviewData{ID: "RV-4", Head: head, Tasks: []string{"T-2", "T-3"}}
	verify := app.ReviewData{Tasks: []string{"T-2", "T-4"}} // T-2 again: the set is a set
	db, err := store.OpenRW(event.DBFile(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		step string
		d    app.ReviewData
	}{{event.StepReviewStarted, start}, {event.StepReviewVerify, verify}} {
		b, err := json.Marshal(r.d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Append(event.Record{Task: event.ReviewTask, Step: r.step, Kind: event.KindFact, Data: b}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	f := p.Herdr()
	f.Shell("w1:p3", "pj-rev", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
		"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
	p.Write("docs/reviews/RV-4.md", "# RV-4\n")
	rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5 (fix)")
	if rc != 0 || !strings.HasPrefix(stdout, "CLOSED RV-4 closed · ") {
		t.Fatalf("close: exit %d, %q", rc, stdout)
	}
	if got, want := p.Git(p.Root, "log", "-1", "--format=%s"), "RV-4 closed · "+head[:7]+" · approved · T-5 (fix)"; got != want {
		t.Errorf("the subject %q, want %q", got, want)
	}
	if got, want := p.Git(p.Root, "log", "-1", "--format=%b"), "Covered: T-2, T-3, T-4\nStart: "+head; got != want {
		t.Errorf("the body %q, want %q", got, want)
	}
}

// T-822: no review.started fact in the log (a review started by an older obor): the close keeps the old format, and the
// next review's scope is the sha range.
func TestReviewCloseNoStartFact(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	p.Git(p.Root, "update-ref", app.ReviewedRef, head)
	f := p.Herdr()
	f.Shell("w1:p3", "pj-rev", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
		"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
	p.Write("docs/reviews/RV-4.md", "# RV-4\n")
	if rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5 (fix)"); rc != 0 {
		t.Fatalf("close: exit %d, %q", rc, stdout)
	}
	if got := p.Git(p.Root, "log", "-1", "--format=%b"); got != "" {
		t.Errorf("the body %q, want none (the old format)", got)
	}
	if _, _, ok := app.ReviewCovered(p.Root); ok {
		t.Error("ReviewCovered found a start in a log without one")
	}
}

// T-850: obor review fix only asks serve (wire.MethodFix: the fix tasks and the note); a usage error sends nothing.
func TestReviewFix(t *testing.T) {
	t.Parallel()
	p, _ := reviewFixture(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "FIX LINKED to RV-4: T-5,T-6", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "fix"}, {"review", "fix", "T-5", "--note"}, {"review", "fix", "--note", "x"}, {"review", "fix", "T-5", "T-6"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review fix") {
			t.Errorf("obor %q: exit %d, %q; want FAIL usage", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
	rc, stdout, _ := run(t, p.Root, env, "review", "fix", "T-5,T-6", "--note", "check #2")
	if rc != 0 || stdout != "FIX LINKED to RV-4: T-5,T-6\n" {
		t.Fatalf("fix: exit %d, %q", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.methods[0] != wire.MethodFix || strings.Join(calls[0].After, ",") != "T-5,T-6" ||
		calls[0].Note != "check #2" || calls[0].CallID == "" {
		t.Fatalf("the request: %q %+v", serve.methods, calls)
	}
}

// appendFacts appends facts to the project's event log (no serve runs: the test is its one writer).
func appendFacts(t *testing.T, root string, recs ...event.Record) {
	t.Helper()
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if _, err := log.Append(r, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// logFact is a fact record of task and step with data d.
func logFact(t *testing.T, task, step string, d any) event.Record {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Task: task, Step: step, Kind: event.KindFact, Data: b}
}

// T-850: obor review close is refused while a fix task linked by obor review fix is neither verified nor dropped,
// naming them, nothing changed; a verify brief naming T-5 and the lead's drop line for T-6 let it close. --force closes
// with a fix pending.
func TestReviewCloseFixPending(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		p, head := reviewFixture(t)
		p.Write(".gitignore", ".obor/state/\n")
		p.Git(p.Root, "add", ".gitignore")
		p.Git(p.Root, "commit", "-q", "-m", "gitignore: the state dir")
		p.Git(p.Root, "update-ref", app.ReviewedRef, head)
		appendFacts(t, p.Root, logFact(t, event.ReviewTask, event.StepReviewFix, app.ReviewData{ID: "RV-4", Tasks: []string{"T-5", "T-6"}}))
		f := p.Herdr()
		f.Shell("w1:p3", "pj-rev", p.Root)
		srv := herdrtest.Serve(t, f)
		env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
			"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
			"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
		p.Write("docs/reviews/RV-4.md", "# RV-4\n")
		before := p.Git(p.Root, "rev-parse", "HEAD")
		if force {
			rc, stdout, _ := run(t, p.Root, env, "review", "close", "forced", "--force")
			if rc != 0 || !strings.HasPrefix(stdout, "CLOSED RV-4 closed · ") {
				t.Errorf("--force with T-5, T-6 pending: exit %d, %q; want CLOSED", rc, stdout)
			}
			continue
		}
		rc, stdout, _ := run(t, p.Root, env, "review", "close", "fixed")
		if rc != 1 || !strings.HasPrefix(stdout, "FAIL RV-4's fixes are not verified: T-5, T-6 ") {
			t.Fatalf("a pending fix: exit %d, %q; want the refusal naming T-5, T-6", rc, stdout)
		}
		if !p.Exists(p.Path("docs/reviews/RV-4.md")) || p.Git(p.Root, "rev-parse", "HEAD") != before {
			t.Fatal("a refused close changed something")
		}
		appendFacts(t, p.Root,
			logFact(t, event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor obor] verify", Verify: []string{"T-5"}}),
			logFact(t, event.MsgTask, event.StepMessage, event.Message{To: "pj-lead", Line: "[obor] RV-4 fix T-6 ended unmerged", FixDrop: []string{"T-6"}}))
		if rc, stdout, _ := run(t, p.Root, env, "review", "close", "fixed"); rc != 0 || !strings.HasPrefix(stdout, "CLOSED RV-4 closed · ") {
			t.Errorf("T-5 verified, T-6 dropped: exit %d, %q; want CLOSED", rc, stdout)
		}
	}
}
