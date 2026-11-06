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

// startedFact records RV-4's review start at head in the project's log (as serve's start leaves it).
func startedFact(t *testing.T, p *testkit.Project, head string) {
	t.Helper()
	appendFacts(t, p.Root, logFact(t, event.ReviewTask, event.StepReviewStarted, app.ReviewData{ID: "RV-4", Head: head}))
}

// T-716: obor review close refuses a dirty main checkout or a review with no start in the log (T-869; nothing
// changed); then it deletes the review's files from the main checkout (T-718), appends each --accept to BACKLOG.md's
// Open section naming the RV and commits "RV-n closed · <reviewed sha> · accepted <n> · <summary>" on main with
// BACKLOG.md (T-909: two accepted findings, so the subject says accepted 2, not approved); it stops nothing (T-869:
// serve stops the reviewer once it sees the close). The reviewed sha is the start's, not main's head: the lead's doc
// commit during the review is no part of it.
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
	if rc != 1 || stdout != "FAIL no review start for RV-4 in the log: nothing changed\n" {
		t.Errorf("no review start: exit %d, %q", rc, stdout)
	}
	if !p.Exists(review) || p.Git(p.Root, "rev-parse", "HEAD") != head {
		t.Fatalf("a close with no review start changed something")
	}
	startedFact(t, p, head)
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
	subject := "RV-4 closed · " + head[:7] + " · accepted 2 · T-5 (fix)"
	if rc != 0 || stdout != "CLOSED "+subject+"\n" { // no STOPPED line: the CLI stops nothing (T-869, DD D1-A)
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
			startedFact(t, p, head)
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
			if got, want := p.Git(p.Root, "log", "-1", "--format=%s"),
				"RV-4 closed · "+head[:7]+" · accepted 1 · T-5 (fix)"; got != want { // T-909: one accepted finding
				t.Errorf("the close subject %q, want %q", got, want)
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
	appendFacts(t, p.Root,
		logFact(t, event.ReviewTask, event.StepReviewStarted, app.ReviewData{ID: "RV-4", Head: head, Tasks: []string{"T-2", "T-3"}}),
		// a finished verify: T-2 again (the set is a set) and T-4
		logFact(t, event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor obor] verify", Task: "T-2,T-4", Reviewed: head}),
		logFact(t, event.MsgTask, event.StepMessage, event.Message{To: "pj-lead", From: "obor", Ends: "pj-rev", Line: "[obor] pj-rev finished"}))
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

// T-869 (DD R2): only another review's start in the log (RV-3's), none of the open RV-4: the close is refused, nothing
// changed (refs/obor/reviewed and the old-format close are gone: every review has a start fact).
func TestReviewCloseNoStartFact(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	appendFacts(t, p.Root, logFact(t, event.ReviewTask, event.StepReviewStarted, app.ReviewData{ID: "RV-3", Head: head}))
	env := []string{"HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state"}
	p.Write("docs/reviews/RV-4.md", "# RV-4\n")
	if rc, stdout, _ := run(t, p.Root, env, "review", "close", "T-5 (fix)"); rc != 1 ||
		stdout != "FAIL no review start for RV-4 in the log: nothing changed\n" {
		t.Fatalf("close: exit %d, %q; want the refusal", rc, stdout)
	}
	if !p.Exists(p.Path("docs/reviews/RV-4.md")) || p.Git(p.Root, "rev-parse", "HEAD") != head {
		t.Error("a refused close changed something")
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

// fixSpec stores task's spec with the line Fixes: <review> in the project's store and records its dispatch.
func fixSpec(t *testing.T, root, task, review string) {
	t.Helper()
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	text := "# " + task + " — fix\n\nDifficulty: medium\nFixes: " + review + "\n\n## Do\nfix it\n"
	if _, _, err := store.SetSpec(db, task, text, "", "pj-lead", time.Now()); err != nil {
		t.Fatal(err)
	}
	appendFacts(t, root, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact,
		Data: json.RawMessage(`{}`)})
}

// appendFacts appends facts to the project's event log (no serve runs: the test is its one writer): their seqs.
func appendFacts(t *testing.T, root string, recs ...event.Record) []int64 {
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
	var seqs []int64
	for _, r := range recs {
		rec, err := log.Append(r, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, rec.Seq)
	}
	return seqs
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

// T-924: obor review close is refused while a task linked by its spec's Fixes: RV-4 line is open, or merged and no
// finished verify covers it, naming them, nothing changed: open; merged and not briefed; briefed and not finished; ended
// by the reviewer's overloaded stop (T-857: not finished). A linked task dropped and a task fixing another review are
// not counted. Once the reviewer finished a verify of T-5 it closes, naming that verify's head and covering T-5.
// --force closes with a fix pending.
func TestReviewCloseFixPending(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		p, head := reviewFixture(t)
		older := p.Git(p.Root, "rev-parse", head+"~1")
		appendFacts(t, p.Root, logFact(t, event.ReviewTask, event.StepReviewStarted, app.ReviewData{ID: "RV-4", Head: older}))
		fixSpec(t, p.Root, "T-5", "RV-4")
		fixSpec(t, p.Root, "T-6", "RV-4 #2")
		fixSpec(t, p.Root, "T-7", "RV-3")
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
		refused := func(what, pending string) {
			t.Helper()
			rc, stdout, _ := run(t, p.Root, env, "review", "close", "fixed")
			if rc != 1 || !strings.HasPrefix(stdout, "FAIL RV-4's fixes are not verified: "+pending+" ") {
				t.Fatalf("%s: exit %d, %q; want the refusal naming %s", what, rc, stdout, pending)
			}
			if !p.Exists(p.Path("docs/reviews/RV-4.md")) || p.Git(p.Root, "rev-parse", "HEAD") != before {
				t.Fatalf("%s: a refused close changed something", what)
			}
		}
		refused("both open", "T-5, T-6")
		appendFacts(t, p.Root,
			event.Record{Task: "T-5", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact, Data: json.RawMessage(`{"head":"` + head + `"}`)},
			event.Record{Task: "T-6", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact, Data: json.RawMessage(`{"reason":"superseded"}`)})
		refused("T-5 merged, no verify; T-6 dropped", "T-5")
		verify := logFact(t, event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor obor] verify",
			Task: "T-5", Reviewed: head})
		appendFacts(t, p.Root, verify)
		refused("T-5's verify briefed, its line not delivered", "T-5")
		appendFacts(t, p.Root, logFact(t, event.MsgTask, event.StepStopped, struct {
			Agent string `json:"agent"`
			Error string `json:"error"`
			event.Message
		}{"pj-rev", "overloaded", event.Message{To: "pj-lead", From: "obor", Ends: "pj-rev", Line: "[obor] pj-rev stopped: overloaded"}}))
		refused("T-5's verify ended by the reviewer's overloaded stop", "T-5")
		appendFacts(t, p.Root, verify, logFact(t, event.MsgTask, event.StepMessage,
			event.Message{To: "pj-lead", From: "obor", Ends: "pj-rev", Line: "[obor] pj-rev finished (briefed 2m ago)"}))
		rc, stdout, _ := run(t, p.Root, env, "review", "close", "fixed")
		if rc != 0 || !strings.HasPrefix(stdout, "CLOSED RV-4 closed · "+head[:7]+" · ") {
			t.Errorf("T-5 verified, T-6 dropped: exit %d, %q; want CLOSED at the verify's head %s", rc, stdout, head[:7])
		}
		if body := p.Git(p.Root, "log", "-1", "--format=%b"); !strings.HasPrefix(body, "Covered: T-5\n") {
			t.Errorf("the close's body %q; want T-5 covered", body)
		}
	}
}

// T-857 (RV-113 #4a): a verify of T-5 briefed at the head, then an ordinary brief to the reviewer before it finished:
// close is allowed (T-5 is linked by no spec) and names the review start's head, not the superseded verify's, and
// covers nothing of it.
func TestReviewCloseSupersededVerify(t *testing.T) {
	t.Parallel()
	p, head := reviewFixture(t)
	older := p.Git(p.Root, "rev-parse", head+"~1")
	appendFacts(t, p.Root,
		logFact(t, event.ReviewTask, event.StepReviewStarted, app.ReviewData{ID: "RV-4", Head: older, Tasks: []string{"T-1"}}),
		logFact(t, event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor obor] verify", Task: "T-5",
			Reviewed: head}),
		logFact(t, event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor pj-lead] look at the flaky test"}))
	if c, err := app.ReadReviewCycle(p.Root, "RV-4"); err != nil || len(c.Pending) != 0 || c.Reviewed != older {
		t.Fatalf("the verify superseded: cycle %+v (%v); want nothing pending, the start's head %s", c, err, older)
	}
	f := p.Herdr()
	f.Shell("w1:p3", "pj-rev", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"GIT_AUTHOR_NAME=lead", "GIT_AUTHOR_EMAIL=lead@example.invalid",
		"GIT_COMMITTER_NAME=lead", "GIT_COMMITTER_EMAIL=lead@example.invalid"}
	p.Write("docs/reviews/RV-4.md", "# RV-4\n")
	rc, stdout, _ := run(t, p.Root, env, "review", "close", "fixed")
	if rc != 0 || !strings.HasPrefix(stdout, "CLOSED RV-4 closed · "+older[:7]+" · ") {
		t.Fatalf("the verify superseded: exit %d, %q; want CLOSED at the start's head %s", rc, stdout, older[:7])
	}
	if body := p.Git(p.Root, "log", "-1", "--format=%b"); !strings.Contains(body, "Covered: T-1\n") {
		t.Errorf("the close's body %q; want only the start's T-1 covered", body)
	}
}
