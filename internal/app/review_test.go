package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

func TestReviewRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, r *reviewRig)
	}{
		{"fires after the last --after merges", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.submitted("T-b")
			if got := r.mustCall("r1", []string{"T-a", "T-b"}, "skip docs", false); got != "REVIEW REQUESTED (after T-a,T-b)" {
				t.Fatalf("reply %q", got)
			}
			r.merged("T-a")
			r.pass()
			if len(r.agents.launches) != 0 || r.p.folded.review == nil {
				t.Fatalf("started with T-b open: %+v", r.agents.launches)
			}
			r.merged("T-b")
			r.pass()
			if len(r.agents.launches) != 1 {
				t.Fatalf("launches %+v, want one", r.agents.launches)
			}
			l := r.agents.launches[0]
			if l.Agent != "pj-rev" || !l.Fresh || !strings.HasPrefix(l.Prompt, "Review RV-5. main is at abcdef0. In scope: T-a (ccccccc), T-b (ccccccc); files: internal/app/x.go, docs/y.md.") ||
				!strings.Contains(l.Prompt, "Previous review: RV-4 (") || !strings.Contains(l.Prompt, "write only reviews/RV-5.md") ||
				// Real findings only; constructed ones a one-line list without a fix.
				!strings.Contains(l.Prompt, " Report only findings a real config or workflow hits; ") ||
				!strings.Contains(l.Prompt, `goes in a short "Not reported (constructed)" list instead: one line each, no fix proposal.`) ||
				// docs/design.md "Change rules" rule 6; only the generic half stays in serve's brief.
				!strings.Contains(l.Prompt, " "+redundantLine+" ") ||
				!strings.HasSuffix(l.Prompt, "\nLead's note: skip docs") ||
				!strings.Contains(l.Prompt, " Previous review: RV-4 (its close commit: RV-4 closed · 1111111 · approved · T-9; its accepted"+
					" findings are the lines of docs/accepted.md that name RV-4) — don't repeat them. ") || strings.Contains(l.Prompt, ".md, or the file") {
				t.Errorf("launch %+v", l)
			}
			// The reviewer's role worktree is moved to the head before its start.
			if wt := filepath.Join(filepath.Dir(r.p.root), filepath.Base(r.p.root)+"-wt", "reviewer"); len(r.git.detached) != 1 ||

				r.git.detached[0] != wt+"@abcdef0123456789abcdef0123456789abcdef01" {
				t.Errorf("detached %q, want the reviewer's worktree %s at the head", r.git.detached, wt)
			}
			if !strings.Contains(l.Prompt, "Your worktree is at abcdef0 (check out any commit there); write only"+
				" reviews/RV-5.md in the main checkout "+r.p.root+". "+resultLine+" Do not commit.") {
				t.Errorf("the brief does not name the worktree and the main checkout: %q", l.Prompt)
			}
			// The brief itself, not only the role text, asks for the file's Result first line.
			if !strings.Contains(l.Prompt, "Result: approved | changes requested · <sha reviewed> · <scope>") {
				t.Errorf("the brief does not ask for the Result line: %q", l.Prompt)
			}
			// serve's brief carries none of obor-kit's own three sentences: they are in its docs/agents/reviewer.md.
			assertNoKitVocabulary(t, l.Prompt)

			if lines := r.leadLines(); len(lines) != 0 {
				t.Errorf("lead lines %q, want none on a start (T-650)", lines)
			}
			if r.p.out.Brief("pj-rev") == nil || r.p.folded.review != nil || r.facts(event.StepReviewStarted) != 1 {
				t.Errorf("brief %v, request %v, started facts %d", r.p.out.Brief("pj-rev"), r.p.folded.review, r.facts(event.StepReviewStarted))
			}
			rows, err := store.ReadPrompts(r.p.log.DB(), store.PromptQuery{Agent: "pj-rev"}, 10)
			if err != nil || len(rows) != 1 || rows[0].Kind != store.PromptStart || rows[0].Text != l.Prompt ||
				rows[0].Task != "" {
				t.Errorf("the review start's prompt row: %+v %v; want its first prompt (T-665)", rows, err)
			}
		}},
		{"waits while the reviewer is busy", func(t *testing.T, r *reviewRig) {
			lineStatus(r.a, "w:rev", "working", r.now)
			r.mustCall("r1", nil, "", false)
			if r.p.folded.review == nil {
				t.Fatal("precondition: no request open")
			}
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(event.StepReviewFire) != 0 {
				t.Fatalf("fired while pj-rev works: %+v", r.agents.launches)
			}
			lineStatus(r.a, "w:rev", "idle", r.now)
			r.pass()
			if len(r.agents.launches) != 1 {
				t.Errorf("launches %+v once idle, want one", r.agents.launches)
			}
		}},
		{"a dropped --after ends it with the line", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.submitted("T-b")
			r.mustCall("r1", []string{"T-a", "T-b"}, "", false)
			r.merged("T-a")
			r.dropped("T-b")
			r.pass()
			r.pass()
			if lines := r.leadLines(); len(lines) != 1 || lines[0] != "[obor] review request ended: T-b dropped" {
				t.Errorf("lead lines %q", lines)
			}
			if r.p.folded.review != nil || len(r.agents.launches) != 0 {
				t.Errorf("request %v, launches %+v; want ended, none", r.p.folded.review, r.agents.launches)
			}
			r.reload()
			r.pass()
			if lines := r.leadLines(); len(lines) != 1 || r.p.folded.review != nil {
				t.Errorf("after a replay: lines %q, request %v", lines, r.p.folded.review)
			}
		}},
		{"replay after the firing fact starts nothing", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "", false)
			seq := r.p.folded.review.seq
			// A crash right after the firing fact: nothing else of the start ran.
			r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewFire, Kind: event.KindFact}, ReviewData{Request: seq})
			r.reload()
			if r.facts(event.StepReviewFire) != 1 {
				t.Fatal("precondition: no firing fact")
			}
			r.pass()
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(event.StepReviewFire) != 1 || r.p.folded.review != nil {
				t.Errorf("launches %+v, fire facts %d, request %v; want none started", r.agents.launches,
					r.facts(event.StepReviewFire), r.p.folded.review)
			}
		}},
		{"second call replaces", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.submitted("T-b")
			r.mustCall("r1", []string{"T-a"}, "", false)
			r.now = r.now.Add(5 * time.Minute)
			if got := r.mustCall("r2", []string{"T-b"}, "", false); got != "REVIEW REQUESTED (after T-b) (replaces the request of 5m ago)" {
				t.Errorf("reply %q", got)
			}
			if got := r.mustCall("r3", nil, "", false); got != "REVIEW REQUEST since 0s (after T-b, waiting T-b)" {
				t.Errorf("the list %q", got)
			}
			r.merged("T-a")
			r.pass()
			if len(r.agents.launches) != 0 {
				t.Errorf("fired on the replaced request's task: %+v", r.agents.launches)
			}
		}},
		{"--rm", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.mustCall("r1", []string{"T-a"}, "", false)
			if got := r.mustCall("r2", nil, "", true); got != "REVIEW REQUEST ENDED" {
				t.Errorf("--rm %q", got)
			}
			if got := r.mustCall("r3", nil, "", true); got != "NO REVIEW REQUEST" {
				t.Errorf("--rm again %q", got)
			}
			r.merged("T-a")
			r.pass()
			if len(r.agents.launches) != 0 || r.p.folded.review != nil || r.facts(event.StepReviewRm) != 1 {
				t.Errorf("launches %+v, request %v", r.agents.launches, r.p.folded.review)
			}
		}},
		{"a request while a started RV has no close fact is refused (T-926: a close commit alone does not close it)", func(t *testing.T, r *reviewRig) {
			r.started()
			if _, err := r.call("r2", nil, "again", false); err == nil || err.Error() != "RV-5 is open (not closed): close it first" {
				t.Errorf("request with RV-5 open: %v", err)
			}
			if _, err := r.call("r3", []string{}, "", false); err == nil || !strings.Contains(err.Error(), "RV-5 is open") {
				t.Errorf("a request to start now with RV-5 open: %v", err)
			}
			if r.facts(event.StepReviewRequest) != 1 || r.p.folded.review != nil {
				t.Errorf("request facts %d, request %v; want only r1, none open", r.facts(event.StepReviewRequest), r.p.folded.review)
			}
			r.git.closes = append(r.git.closes, port.ReviewCommit{ID: "RV-5", Subject: "RV-5 closed · abcdef0 · approved · x"})
			if _, err := r.call("r5", nil, "", false); err == nil || err.Error() != "RV-5 is open (not closed): close it first" {
				t.Errorf("request with a close commit and no close fact: %v", err) // a commit, no fact: git is not read
			}
			r.closedBy("RV-5", "RV-5 closed · abcdef0 · approved")
			if got := r.mustCall("r4", nil, "", false); got != "REVIEW REQUESTED (now)" {
				t.Errorf("after RV-5 closed: %q", got)
			}
		}},
		{"the open check survives a replay", func(t *testing.T, r *reviewRig) {
			r.started()
			r.reload()
			if r.p.folded.rv.Last().ID != "RV-5" {
				t.Fatalf("precondition: the replay lost RV-5's start: %+v", r.p.folded.rv.Last())
			}
			if _, err := r.call("r2", nil, "", false); err == nil || err.Error() != "RV-5 is open (not closed): close it first" {
				t.Errorf("request after a replay with RV-5 open: %v", err)
			}
		}},
		{"a queued request waits while RV-5 is open, then fires after its close fact", func(t *testing.T, r *reviewRig) {
			r.started()
			// A request already in the log (one obor review recorded before RV-5's start, or an older serve's).
			r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewRequest, Kind: event.KindFact}, ReviewData{By: "pj-lead", Note: "next"})
			if r.p.folded.review == nil || !r.a.roleIdle(r.p, "pj-rev") {
				t.Fatal("precondition: no request open, or the reviewer is not idle")
			}
			r.pass()
			if len(r.agents.launches) != 1 || r.facts(event.StepReviewFire) != 1 {
				t.Fatalf("fired with RV-5 open: launches %d, fire facts %d", len(r.agents.launches), r.facts(event.StepReviewFire))
			}
			if got := r.a.Status().Project.Review; got != "review request since 0s (now, waiting RV-5 to close), note: next" {
				t.Errorf("status %q", got)
			}
			if q := r.a.Status().Project.ReviewRequest; q == nil || q.Held != "RV-5" || q.Note != "next" || q.By != "pj-lead" ||
				q.Since == 0 || len(q.After) != 0 { // the request as data
				t.Errorf("status review_request %+v; want held by RV-5, note next, by pj-lead, its time", q)
			}
			r.closedBy("RV-5", "RV-5 closed · abcdef0 · approved · T-7")
			r.pass()
			if len(r.agents.launches) != 2 || !strings.HasPrefix(r.agents.launches[1].Prompt, "Review RV-6. ") ||
				!strings.HasSuffix(r.agents.launches[1].Prompt, "\nLead's note: next") {
				t.Errorf("after RV-5 closed: launches %+v", r.agents.launches)
			}
		}},
		{"replace and --rm during a claim are refused, the old request never fires after them", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "old", false)
			old := r.p.folded.review.seq
			var errs []error
			r.p.rv.claimed = func() {
				_, err := r.call("r2", nil, "new", false)
				errs = append(errs, err)
				_, err = r.call("r3", nil, "", true)
				errs = append(errs, err)
			}
			r.refuseStep(event.StepReviewFire) // the claim ends without a firing: the old request stays open
			r.pass()
			want := "review: obor serve is starting the review request now: nothing changed (try again once it is done)"
			if len(errs) != 2 || errs[0] == nil || errs[0].Error() != want || errs[1] == nil || errs[1].Error() != want {
				t.Fatalf("replace and --rm during the claim: %v", errs)
			}
			if r.p.folded.review == nil || r.p.folded.review.seq != old || r.facts(event.StepReviewRequest) != 1 || r.facts(event.StepReviewRm) != 0 {
				t.Fatalf("the claim's request changed: %+v", r.p.folded.review)
			}
			r.p.rv.claimed, r.p.log.Refuse = nil, nil
			if got := r.mustCall("r4", nil, "new", false); !strings.HasPrefix(got, "REVIEW REQUESTED (now) (replaces") {
				t.Errorf("replace after the claim: %q", got)
			}
			r.pass()
			if len(r.agents.launches) != 1 || !strings.HasSuffix(r.agents.launches[0].Prompt, "\nLead's note: new") {
				t.Errorf("launches %+v, want the new request's only", r.agents.launches)
			}
			r.pass()
			if len(r.agents.launches) != 1 || r.facts(event.StepReviewFire) != 1 {
				t.Errorf("the old request fired after the replace: launches %d, fire facts %d", len(r.agents.launches), r.facts(event.StepReviewFire))
			}
		}},
		{"--rm after a refused claim ends the request: nothing fires", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "", false)
			var rmErr error
			r.p.rv.claimed = func() { _, rmErr = r.call("r2", nil, "", true) }
			r.refuseStep(event.StepReviewFire)
			r.pass()
			if rmErr == nil || !strings.Contains(rmErr.Error(), "is starting the review request now") {
				t.Fatalf("--rm during the claim: %v", rmErr)
			}
			r.p.rv.claimed, r.p.log.Refuse = nil, nil
			if got := r.mustCall("r3", nil, "", true); got != "REVIEW REQUEST ENDED" {
				t.Errorf("--rm after the claim: %q", got)
			}
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(event.StepReviewFire) != 0 {
				t.Errorf("fired after --rm: launches %d, fire facts %d", len(r.agents.launches), r.facts(event.StepReviewFire))
			}
		}},
		{"a replace during the ended line's claim is refused", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.mustCall("r1", []string{"T-a"}, "", false)
			r.dropped("T-a")
			var err error
			r.p.rv.claimed = func() { _, err = r.call("r2", nil, "replace", false) }
			r.pass()
			if err == nil || !strings.Contains(err.Error(), "is ending the review request now") {
				t.Errorf("replace during the ended line's claim: %v", err)
			}
			if lines := r.leadLines(); len(lines) != 1 || r.p.folded.review != nil || r.facts(event.StepReviewRequest) != 1 {
				t.Errorf("lines %q, request %v, request facts %d", lines, r.p.folded.review, r.facts(event.StepReviewRequest))
			}
		}},
		{"a failing append after the launch is a needs-you", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "", false)
			r.refuseStep(event.StepReviewStarted)
			r.pass()
			if len(r.agents.launches) != 1 || r.facts(event.StepReviewStarted) != 0 {
				t.Fatalf("precondition: launches %d, started facts %d; want launched, not recorded", len(r.agents.launches), r.facts(event.StepReviewStarted))
			}
			if n := r.needs(); len(n) != 1 || n[0] != "RV-5: started but not recorded: disk full" {
				t.Errorf("needs-you %q", n)
			}
		}},
		{"a failing move is a needs-you, no restart", func(t *testing.T, r *reviewRig) {
			r.git.detachErr = errors.New("git checkout: local changes")
			r.mustCall("r1", nil, "", false)
			r.pass()
			if len(r.agents.launches) != 0 {
				t.Errorf("launched %+v; want none", r.agents.launches)
			}
			if n := r.needs(); len(n) != 1 || n[0] != "RV-5: git checkout: local changes" {
				t.Errorf("needs-you %q", n)
			}
			if r.p.folded.review != nil || r.facts(event.StepReviewStarted) != 0 {
				t.Errorf("request %v, started %d; want ended, none", r.p.folded.review, r.facts(event.StepReviewStarted))
			}
			r.git.detachErr = nil
			r.mustCall("r2", nil, "", false)
			if n := r.needs(); len(n) != 0 {
				t.Errorf("needs-you %q after a new request, want ended", n)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, newReviewRig(t))
		})
	}
}

// A reviewer start the launch refuses (a quarantined Codex install: port.Refused) starts nothing, records the
// review's needs-you naming the files and the fix, and ends the request; the lead's line is that reason (needsRule
// copies it verbatim), so the owner sees the fix without a tool call of the agent that never ran.
func TestReviewStartRefusedNamesTheFix(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	line := "codex is quarantined: /c/0.167.0/bin/codex-code-mode-host carry com.apple.quarantine; a Codex start is " +
		"refused until the owner clears the flag: xattr -dr com.apple.quarantine /c/0.167.0 (obor applies no fix)"
	r.agents.launchErr = port.Refused{Line: line}
	r.mustCall("r1", nil, "", false)
	r.pass()
	if n := r.facts(event.StepReviewStarted); n != 0 {
		t.Errorf("review started facts %d, want none", n)
	}
	if n := r.needs(); len(n) != 1 || n[0] != r.next+": "+line {
		t.Errorf("needs-you %q, want one naming the refusal", n)
	}
	if _, act, ok := needsRule(context.Background(), r.a, r.p, nil, r.now); !ok || !strings.Contains(act.line, line) {
		t.Errorf("the lead's line %q (ok %v), want it to name the fix %q", act.line, ok, line)
	}
	if r.p.folded.review != nil {
		t.Errorf("request %+v still open, want it ended by the refused start", r.p.folded.review)
	}
}

// The close commit's writer: its subject and body (rendered from the fold's cycle, read back by
// no one but the crash cut's ClosesReview), and the "accepted <n>" verdict a close with --accept lines writes.
func TestCloseCommitText(t *testing.T) {
	t.Parallel()
	subject, body := CloseCommit("RV-7", "abcdef0", "T-1, T-2 (fix)", []string{"T-1", "T-2"}, "1234567", 0, false)
	if want := "RV-7 closed · abcdef0 · approved · T-1, T-2 (fix)"; subject != want {
		t.Errorf("subject %q, want %q", subject, want)
	}
	if want := "Covered: T-1, T-2\nStart: 1234567"; body != want {
		t.Errorf("body %q, want %q", body, want)
	}
	if !ClosesReview(subject, "RV-7") || ClosesReview(subject, "RV-70") || ClosesReview("RV-17 closed · x", "RV-7") {
		t.Errorf("ClosesReview: %q is RV-7's close only", subject)
	}

	// A close with two accepted findings says so
	subject, _ = CloseCommit("RV-8", "abcdef0", "T-9 (fix)", nil, "1234567", 2, false)
	if want := "RV-8 closed · abcdef0 · accepted 2 · T-9 (fix)"; subject != want {
		t.Fatalf("subject %q, want %q", subject, want)
	}

	// A review closed unwritten says it was not approved
	subject, _ = CloseCommit("RV-9", "abcdef0", "never written", nil, "1234567", 0, true)
	if want := "RV-9 closed · abcdef0 · abandoned · never written"; subject != want {
		t.Fatalf("subject %q, want %q", subject, want)
	}
}

// A pass's claim of the request is in flight too: its fire fact ends the request before the reviewer's brief
// opens, so from the claim to the brief nothing may call the pipeline idle.
func TestReviewClaimIsInFlight(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.submitted("T-a")
	r.merged("T-a")
	for _, step := range afterSteps {
		r.a.record(r.p, event.Record{Task: "T-a", Attempt: 1, Step: step, Kind: event.KindResult}, nil)
	}
	r.a.mu.Lock()
	n := r.p.idleNow()
	r.a.mu.Unlock()
	if !n.ok {
		t.Fatalf("precondition: T-a merged with its after-merge steps done is not idle: %+v", n)
	}
	r.a.mu.Lock()
	r.p.rv.what = "starting"
	n = r.p.idleNow()
	r.a.mu.Unlock()
	if n.ok {
		t.Error("idle while a pass starts the review request")
	}
}

// assertNoKitVocabulary fails when a brief carries obor-kit's own review vocabulary, which lives in its
// docs/agents/reviewer.md, not in the brief every project's reviewer gets.
func assertNoKitVocabulary(t *testing.T, text string) {
	t.Helper()
	for _, sentence := range []string{
		"On rule-tier steps a crash or interleaving finding counts only when a kernel guarantee breaks.",
		"Does it add a field or type that copies data another type already holds? Name the owner type.",
		"Does a client decide something from a state string that serve knows? Then serve ships the predicate.",
	} {
		if strings.Contains(text, sentence) {
			t.Errorf("the brief carries obor-kit's %q", sentence)
		}
	}
}

// A verify of a task merged since the previous review's start (here: during the previous verify, so not in
// RV-5's start scope) is allowed: the verify's range is the next review's scope, read from the fold.
func TestReviewVerifyOutsideScope(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.mergedAt("T-c", "ccccccc3")
	r.fact(event.ReviewTask, event.StepReviewStarted, ReviewData{ID: "RV-5", Head: "ddddddd4", Tasks: []string{"T-a", "T-b"}})
	s, err := reviewConfig(r.p.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.p.root, s.Reviews), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.p.root, s.Reviews, "RV-5.md"), []byte("# RV-5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.a.verifyText(context.Background(), r.p, r.git, s, "RV-5", []string{"T-z"}, nil); err == nil ||
		err.Error() != "T-z has no merge on main since RV-4" {
		t.Errorf("verify of a task the log never merged: %v", err)
	}
	text, _, err := r.a.verifyText(context.Background(), r.p, r.git, s, "RV-5", []string{"T-c"}, nil)
	if err != nil {
		t.Fatalf("verify of T-c, merged since the previous review started: %v", err)
	}
	if !strings.Contains(text, "fixed in T-c (ccccccc)") || !strings.Contains(text, " every finding ") ||
		!strings.Contains(text, " "+redundantLine+" ") { // design.md "Change rules" rule 6

		t.Errorf("the brief: %q", text)
	}
	assertNoKitVocabulary(t, text) // the kit's three sentences are not serve's
}

// obor status' "review due" line is the fold's scope, counted on every pass: at 3 merges none, at 4 "review
// due: 4 merges since RV-4", none once RV-5 started on them.
func TestReviewDueFromTheFold(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t) // RV-4 closed, T-a and T-b merged since
	r.mergedAt("T-c", "ccccccc3")
	r.tick()
	if got := r.a.Status().Project.Review; got != "" {
		t.Fatalf("at 3 merges: %q, want none", got)
	}
	r.mergedAt("T-d", "ddddddd4")
	r.tick()
	if got := r.a.Status().Project.Review; got != "review due: 4 merges since RV-4" {
		t.Errorf("at 4 merges: %q", got)
	}
	r.mustCall("r1", nil, "", false)
	r.pass()
	if l := r.agents.launches; len(l) != 1 ||
		!strings.Contains(l[0].Prompt, "In scope: T-a (aaaaaaa), T-b (bbbbbbb), T-c (ccccccc), T-d (ddddddd);") {
		t.Fatalf("RV-5's brief: %+v, want the 4 merges in scope", l)
	}
	r.tick()
	if got := r.a.Status().Project.Review; got != "" {
		t.Errorf("RV-5 started on them: %q, want none", got)
	}
}

// The open review's stage as obor status reads it (Project.ReviewOpen), through a scripted cycle: a request is no open
// review; the start is reviewing (the reviewer's brief open); its brief ended is written (the file shown once in the
// main checkout); a linked fix dispatched is fixes, merged still fixes; its verify briefed is verifying; the verify
// finished is approved; the close leaves none. Each stage's since is its start.
func TestReviewOpenStages(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	open := func() *wire.ReviewOpenStatus { return r.a.Status().Project.ReviewOpen }
	want := func(when, stage string, since time.Time) *wire.ReviewOpenStatus {
		t.Helper()
		ro := open()
		if ro == nil {
			t.Fatalf("%s: no open review, want %s", when, stage)
		}
		if ro.ID != "RV-5" || ro.Stage != stage || ro.Since != since.Unix() || ro.Agent != "pj-rev" {
			t.Fatalf("%s: %+v, want RV-5 %s since %s, reviewer pj-rev", when, *ro, stage, since)
		}
		return ro
	}
	r.mustCall("r1", nil, "", false)
	if ro := open(); ro != nil {
		t.Fatalf("a request: open review %+v, want none (the request line shows it)", *ro)
	}
	start := r.now
	r.pass()
	r.now = r.now.Add(time.Minute)
	lineStatus(r.a, "w:rev", "working", r.now)
	ro := want("started", wire.ReviewReviewing, start)
	if strings.Join(ro.Tasks, ",") != "T-a,T-b" || ro.AgentStatus != "working" || ro.File != "" || len(ro.Fixes) != 0 {
		t.Errorf("started: %+v, want T-a,T-b covered, the reviewer working, no file, no fixes", *ro)
	}

	r.now = r.now.Add(5 * time.Minute)
	written := r.now
	lineStatus(r.a, "w:rev", "idle", r.now)
	r.a.deliver(context.Background(), r.p, &Loop{Inbox: &inboxSpy{}}) // the finished line ends the brief
	if r.p.out.Brief("pj-rev") != nil {
		t.Fatal("precondition: the reviewer's brief is still open")
	}
	if ro := want("brief ended, no file", wire.ReviewWritten, written); ro.File != "" || ro.AgentStatus != "idle" {
		t.Errorf("no review file: %+v, want no file, the reviewer idle", *ro)
	}
	writeReview(t, r.p.root, "RV-5", "Result: changes requested · 1234567 · T-a,T-b\n")
	if ro := want("written", wire.ReviewWritten, written); ro.File != filepath.Join("reviews", "RV-5.md") {
		t.Errorf("written: file %q, want reviews/RV-5.md", ro.File)
	}

	r.now = r.now.Add(3 * time.Minute)
	dispatched := r.now
	r.fixTask("T-c", "Fixes: RV-5")
	r.verifyPass()
	if ro := want("fix dispatched", wire.ReviewFixes, dispatched); len(ro.Fixes) != 1 ||
		ro.Fixes[0] != (wire.ReviewFix{Task: "T-c", State: "open"}) {
		t.Errorf("fix dispatched: fixes %+v, want T-c open", ro.Fixes)
	}
	r.now = r.now.Add(4 * time.Minute)
	r.merged("T-c")
	r.working(true) // the reviewer busy: the verify waits
	r.verifyPass()
	if ro := want("fix merged", wire.ReviewFixes, dispatched); len(ro.Fixes) != 1 || ro.Fixes[0].State != "merged" {
		t.Errorf("fix merged: fixes %+v, want T-c merged", ro.Fixes)
	}

	r.now = r.now.Add(time.Minute)
	verify := r.now
	r.working(false)
	r.verifyPass()
	if n := len(verifyBriefs(t, r.p.root)); n != 1 {
		t.Fatalf("precondition: %d verify briefs, want one", n)
	}
	want("verify briefed", wire.ReviewVerifying, verify)

	r.now = r.now.Add(2 * time.Minute)
	verified := r.now
	r.finish()
	r.verifyPass()
	if ro := want("verify finished", wire.ReviewApproved, verified); strings.Join(ro.Tasks, ",") != "T-a,T-b,T-c" ||
		len(ro.Fixes) != 1 || ro.Fixes[0].State != "verified" {
		t.Errorf("verify finished: %+v, want T-a,T-b,T-c covered, T-c verified", *ro)
	}

	r.closedBy("RV-5", "RV-5 closed · 2222222 · approved · T-a,T-b,T-c")
	if ro := open(); ro != nil {
		t.Errorf("closed: open review %+v, want none", *ro)
	}
}
