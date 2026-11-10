package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// The close's refusals, each before any change (no commit, no fact): no open review (no review file), no review start
// for it in the log, the main checkout's own refusal (not on the branch, dirty outside the allowed files: its message
// passed on), a linked fix pending; --force closes with the fix pending.
func TestReviewCloseRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setup func(r *reviewRig)
		want  string // the refusal's start
	}{
		{"no open review: no review file", func(r *reviewRig) { r.started() },
			"no open review: no reviews/RV-5.md in "},
		{"no review start in the log", func(r *reviewRig) { openReviewFile(r.t, r.p.root) },
			"no open review (the last review is closed)"},
		{"not on the branch", func(r *reviewRig) {
			r.started()
			openReviewFile(r.t, r.p.root)
			r.git.closeErr = errors.New("the main checkout /x is not on main: nothing changed")
		}, "the main checkout /x is not on main: nothing changed"},
		{"dirty outside the allowed files", func(r *reviewRig) {
			r.started()
			openReviewFile(r.t, r.p.root)
			r.git.closeErr = errors.New("the main checkout is dirty outside BACKLOG.md and RV-5's files (?? x): nothing changed")
		}, "the main checkout is dirty outside BACKLOG.md and RV-5's files (?? x): nothing changed"},
		{"a linked fix pending", func(r *reviewRig) {
			openReviewFile(r.t, r.p.root)
			startedReview(r.t, r)
			r.fixTask("T-a", "Fixes: RV-5")
		}, "RV-5's fixes are not verified: T-a (open, or merged and its verify not finished; --force closes anyway): nothing changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReviewRig(t)
			c.setup(r)
			_, err := r.close("c1", "fixed", false)
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("close: %v; want %q", err, c.want)
			}
			if n, f := r.commits(), r.closedFacts(); n != 0 || len(f) != 0 {
				t.Errorf("a refused close: %d commits, facts %+v; want none", n, f)
			}
			if c.name != "a linked fix pending" {
				return
			}
			reply, err := r.close("c2", "forced", true)
			if err != nil || !strings.HasPrefix(reply, "CLOSED RV-5 closed · 1234567 · approved · forced") {
				t.Errorf("--force: %q, %v; want CLOSED", reply, err)
			}
			if r.commits() != 1 || len(r.closedFacts()) != 1 || r.p.folded.rv.last().open() != "" {
				t.Errorf("--force: %d commits, %d facts, open %q; want one, one, none", r.commits(), len(r.closedFacts()),
					r.p.folded.rv.last().open())
			}
		})
	}
}

// A close of a verified fix round: the main checkout commits the subject and body as before (the verify's head, the
// cycle's covered tasks and its start; accepted 1 for one --accept line), the review file and the accepted line handed
// to it; fact review.closed{id, sha, covered, start} recorded; a repeated call id gets the reply and commits nothing
// again. The next rule pass stops the reviewer (no git read) and obor status shows no fix line of the closed review
// a round that reads an empty snapshot.
func TestReviewCloseCommitsAndRecords(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.finish()
	r.verifyPass()
	if got := r.a.Status().Project.Review; !strings.Contains(got, "RV-5 fixes: T-a verified · T-b verified") {
		t.Fatalf("precondition: status %q, want the verified fix round", got)
	}
	reply, err := r.close("c1", "fixed", false, "rare case")
	subject := "RV-5 closed · abcdef0 · accepted 1 · fixed"
	if err != nil || reply != "CLOSED "+subject {
		t.Fatalf("close: %q, %v", reply, err)
	}
	want := port.ReviewCommit{Branch: "main", ID: "RV-5", File: r.p.root + "/reviews/RV-5.md", Reviewed: headSHA,
		Subject: subject, Body: "Covered: T-start, T-a, T-b\nStart: " + startHead, Accepts: []string{"rare case"}}
	if r.commits() != 1 || !commitEqual(r.git.closes[0], want) {
		t.Errorf("committed %+v, want %+v", r.git.closes, want)
	}
	f := r.closedFacts()
	if len(f) != 1 || f[0].ID != "RV-5" || f[0].Reply != "CLOSED "+subject {
		t.Errorf("review.closed facts %+v", f)
	}
	if r.p.folded.rv.last().open() != "" || r.p.folded.rv.last().closed == 0 {
		t.Errorf("the fold %+v, want RV-5 closed", r.p.folded.rv.last())
	}
	if again, err := r.close("c1", "fixed", false, "rare case"); err != nil || again != reply || r.commits() != 1 {
		t.Errorf("the repeated call: %q, %v, %d commits; want the stored reply, one commit", again, err, r.commits())
	}

	s := &reviewStopSpy{}
	reads := r.git.reads()
	r.a.rulePass(context.Background(), rules, &Loop{Workflow: WorkflowOf(r.git), Agents: s})
	if !slices.Equal(s.stops, []string{"pj-rev w:rev force=false"}) {
		t.Errorf("stops %q after the close, want the reviewer stopped on the next pass", s.stops)
	}
	if n := r.git.reads() - reads; n != 0 {
		t.Errorf("the rule pass read git %d times, want none", n)
	}
	if got := r.a.Status().Project.Review; strings.Contains(got, "fixes") {
		t.Errorf("status %q after the close, want no fix line (RV-125 #2)", got)
	}
}

// A repeated close call id waits for the close in flight (configMu is taken before the stored-reply lookup): the
// second gets the first's CLOSED reply and commits nothing again.
func TestReviewCloseSameCallWhileInFlight(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.started()
	openReviewFile(t, r.p.root)
	gate, began := make(chan struct{}), make(chan struct{}, 1)
	r.git.closeGate, r.git.closeBegan = gate, began
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	first := make(chan callReply, 1)
	go func() {
		reply, err := r.close("c1", "fixed", false)
		first <- callReply{reply, err}
	}()
	select {
	case <-began: // the first is in its commit, holding configMu
	case <-time.After(5 * time.Second):
		t.Fatal("precondition: the first close never reached its commit")
	}
	second := make(chan callReply, 1)
	go func() {
		reply, err := r.close("c1", "again", false)
		second <- callReply{reply, err}
	}()
	if !waiting(second) {
		t.Error("the repeated close answered while the first held configMu, want it to wait")
	}
	release()
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || !strings.HasPrefix(a.reply, "CLOSED RV-5 closed") || b.reply != a.reply {
		t.Fatalf("closes: %q, %v and %q, %v; want both the first's reply", a.reply, a.err, b.reply, b.err)
	}
	if n, f := r.commits(), r.closedFacts(); n != 1 || len(f) != 1 {
		t.Errorf("%d commits, %d close facts; want one of each (the repeated call id did nothing)", n, len(f))
	}
}

// commitEqual: a and b are the same close.
func commitEqual(a, b port.ReviewCommit) bool {
	return a.Branch == b.Branch && a.ID == b.ID && a.File == b.File && a.Reviewed == b.Reviewed && a.Subject == b.Subject &&
		a.Body == b.Body && slices.Equal(a.Accepts, b.Accepts)
}

// The close's crash cut: its commit is in and its fact is not (a crash, here a refused append). The
// next pass after a restart records the close once from the commit (port.Review.Closed, the one read) and commits
// nothing; a close call retried before any pass reads it too: refused, no second commit, the close recorded once.
// Either way obor review then starts the next review with no merge in scope (the closed one covered T-a and T-b).
func TestReviewCloseCrashBeforeFact(t *testing.T) {
	t.Parallel()
	cut := func(t *testing.T) *reviewRig {
		r := newReviewRig(t)
		r.started()
		openReviewFile(t, r.p.root)
		r.refuseStep(event.StepReviewClosed)
		if _, err := r.close("c1", "fixed", false); err == nil || !strings.Contains(err.Error(), "committed RV-5, but the event log") {
			t.Fatalf("close with the fact refused: %v", err)
		}
		if r.commits() != 1 || len(r.closedFacts()) != 0 {
			t.Fatalf("precondition: %d commits, %d facts; want the commit, no fact", r.commits(), len(r.closedFacts()))
		}
		r.p.log.Refuse = nil
		r.reload()
		if r.p.folded.rv.last().open() != "RV-5" {
			t.Fatalf("precondition: RV-5 not open after the restart: %+v", r.p.folded.rv.last())
		}
		return r
	}
	next := func(t *testing.T, r *reviewRig) {
		t.Helper()
		if r.commits() != 1 {
			t.Errorf("%d close commits, want the one", r.commits())
		}
		if f := r.closedFacts(); len(f) != 1 || f[0].ID != "RV-5" || f[0].Reply != "CLOSED "+r.git.closes[0].Subject {
			t.Fatalf("review.closed facts %+v, want RV-5's once, from its commit", f)
		}
		if r.p.folded.rv.last().open() != "" {
			t.Fatal("RV-5 still open")
		}
		if got := r.mustCall("r2", nil, "", false); got != "REVIEW REQUESTED (now)" {
			t.Fatalf("obor review after the close: %q", got)
		}
		r.pass()
		if l := r.agents.launches; len(l) != 2 || !strings.HasPrefix(l[1].Prompt, "Review RV-6. ") ||
			!strings.Contains(l[1].Prompt, "In scope: no merges;") ||
			!strings.Contains(l[1].Prompt, "Previous review: RV-5 (its close commit: "+r.git.closes[0].Subject+";") {
			t.Errorf("launches %+v; want RV-6, RV-5's covered tasks out of scope", l)
		}
	}
	t.Run("the next pass", func(t *testing.T) {
		t.Parallel()
		r := cut(t)
		read := func() {
			r.a.reviewCloseRead(context.Background(), r.p, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents})
		}
		read()
		read()
		r.reload()
		read()
		next(t, r)
	})
	t.Run("a retried close", func(t *testing.T) {
		t.Parallel()
		r := cut(t)
		if _, err := r.close("c2", "fixed", false); err == nil || !strings.HasPrefix(err.Error(), "no open review") {
			t.Fatalf("the retried close: %v, want refused (RV-5 is closed by its commit)", err)
		}
		next(t, r)
	})
}

// The close's snapshot (its reviewed head and covered tasks) is decided once and recorded before
// the commit (fact review.closing): a verify finishing after the close, or between the snapshot and a fact recovered
// from the commit after a crash, changes neither what the review covered nor the next review's scope. --force (the
// linked fixes' verify still running) and a normal close (a verify of an unlinked fix), each without and with the
// crash cut.
func TestReviewCloseSnapshotStands(t *testing.T) {
	t.Parallel()
	const h2 = "f1f1f1f2f1f1f1f2"
	for _, c := range []struct {
		name  string
		force bool
		crash bool
	}{
		{name: "force", force: true},
		{name: "force, the verify finishing in the crash cut", force: true, crash: true},
		{name: "normal"},
		{name: "normal, the verify finishing in the crash cut", crash: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var r *reviewRig
			var head string // the snapshot: the review's start head, its start's tasks; scope the verify's tasks
			var tasks, scope []string
			var finish func()
			if c.force {
				r = fixRig(t)
				r.merged("T-a")
				r.merged("T-b")
				r.verifyPass() // the verify of T-a, T-b at headSHA, open
				if r.p.out.Brief("pj-rev") == nil || r.pending() != "T-a,T-b" {
					t.Fatalf("precondition: brief %v, pending %q; want the verify open, T-a, T-b pending",
						r.p.out.Brief("pj-rev"), r.pending())
				}
				head, tasks, scope, finish = startHead, []string{"T-start"}, []string{"T-a", "T-b"}, r.finish
			} else {
				r = newReviewRig(t)
				r.started() // the review at headSHA, T-a and T-b in scope
				openReviewFile(t, r.p.root)
				r.mergedAt("T-fix", h2)
				r.fact(event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "verify", Task: "T-fix",
					Reviewed: h2})
				head, tasks, scope = headSHA, []string{"T-a", "T-b"}, []string{"T-fix"}
				finish = func() {
					r.fact(event.MsgTask, event.StepMessage, event.Message{To: "pj-lead", From: "pj-rev",
						Line: "finished", Ends: "pj-rev"})
				}
			}
			if c.crash {
				r.refuseStep(event.StepReviewClosed)
			}
			_, err := r.close("c1", "fixed", c.force)
			if c.crash != (err != nil) {
				t.Fatalf("close (crash %v): %v", c.crash, err)
			}
			if r.commits() != 1 || r.git.closes[0].Reviewed != head ||
				!strings.Contains(r.git.closes[0].Body, "Covered: "+strings.Join(tasks, ", ")+"\n") {
				t.Fatalf("precondition: commits %+v, want one at %s covering %q", r.git.closes, head, tasks)
			}
			finish() // the verify ends after the snapshot
			r.a.mu.Lock()
			open := r.p.folded.rv.open["pj-rev"]
			r.a.mu.Unlock()
			if open != nil {
				t.Fatal("precondition: the verify is still open")
			}
			if c.crash {
				r.p.log.Refuse = nil
				r.reload()
				r.a.reviewCloseRead(context.Background(), r.p, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents})
			}
			f := r.closedFacts()
			if len(f) != 1 || f[0].Head != head || !slices.Equal(f[0].Tasks, tasks) {
				t.Errorf("review.closed facts %+v, want one carrying the snapshot %s %q", f, head, tasks)
			}
			r.a.mu.Lock()
			defer r.a.mu.Unlock()
			l := &r.p.folded.rv
			if got := l.cur().cycle(); l.cur().closed == 0 || !slices.Equal(got, tasks) {
				t.Errorf("RV-5 (closed %d) covered %q, want %q", l.cur().closed, got, tasks)
			}
			if got := tasksOf(l.scope()); !slices.Equal(got, scope) {
				t.Errorf("the next review's scope %q, want the late verify's %q", got, scope)
			}
		})
	}
}

// Only the lead closes a review (the owner's call has no role): a reviewer or a worker is refused, plain and
// with --force, before any effect (no commit, no fact, the review still open); the lead's close goes through.
func TestReviewCloseLeadOnly(t *testing.T) {
	t.Parallel()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.finish()
	r.verifyPass()
	if r.p.folded.rv.last().open() != "RV-5" {
		t.Fatalf("precondition: %+v, want RV-5 open", r.p.folded.rv.last())
	}
	for _, role := range []string{"reviewer", "worker"} {
		for _, force := range []bool{false, true} {
			_, err := r.a.reviewCloseCall(wire.ReviewCloseReq{Envelope: wire.Envelope{Root: r.p.root,
				CallID: "x-" + role, Role: role, Agent: "pj-" + role}, Summary: "fixed", Force: force})
			if want := "review close: only the lead closes a review (" + role + ")"; err == nil || err.Error() != want {
				t.Errorf("%s force=%v: %v, want %q", role, force, err, want)
			}
		}
	}
	if r.commits() != 0 || len(r.closedFacts()) != 0 || r.p.folded.rv.last().open() != "RV-5" {
		t.Fatalf("a refused close changed something: %d commits, %d facts, open %q", r.commits(), len(r.closedFacts()),
			r.p.folded.rv.last().open())
	}
	if reply, err := r.close("c1", "fixed", false); err != nil || !strings.HasPrefix(reply, "CLOSED RV-5 closed") {
		t.Fatalf("the lead's close: %q, %v", reply, err)
	}
}
