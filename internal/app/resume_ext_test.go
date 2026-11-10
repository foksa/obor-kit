package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// asked is the fetch rig with T-1's worker working on attempt 1, its session's inbox recorded (a Claude worker's
// SessionStart hook records it), and its Blocked: report's question in force.
func asked(t *testing.T) *loopRig {
	t.Helper()
	r := fetchRig(t)
	r.inbox.record("pj-l-01")
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.workerIs("working")
	if _, err := r.answer("lead", "a0", "yes"); err == nil || !strings.Contains(err.Error(), "T-1 is working with nothing to resume") {
		t.Fatalf("answer with no question: %v; want refused", err)
	}
	if _, err := r.callDone("pj-l-01", "worker", "b1", "Summary: half done\nBlocked: may I edit internal/x.go?\n"); err != nil {
		t.Fatal(err)
	}
	r.waitStep("0 fact needs-you{asks: may I edit internal/x.go?}")
	return r
}

// obor resume of a question (row 16b): once the worker's Blocked: report is in force, the answer ends the question and
// serve delivers "[obor answer] <text>" to the worker (posted to its session's inbox, as its kind takes a line),
// in the same attempt: no fix job, no attempt 2. A worker cannot answer, an empty answer is refused, a repeated call
// id gets the stored reply, and an answered task has no question left. The worker's full report submits attempt 1.
func TestAnswerSendsTheWorkerTheAnswer(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := asked(t)
		if f := r.app.Status().Project.Facts; len(f) != 1 {
			t.Fatalf("precondition: facts in force %+v, want the asks fact", f)
		}
		if _, err := r.answer("worker", "a1", "yes"); err == nil || !strings.Contains(err.Error(), "the lead or the owner resumes") {
			t.Errorf("a worker's answer: %v; want refused", err)
		}
		if _, err := r.answer("lead", "a1", " "); err == nil || !strings.Contains(err.Error(), "say the text") {
			t.Errorf("an empty answer: %v; want refused", err)
		}
		r.workerIs("idle") // the worker stopped and waits
		want := "ANSWERED T-1: obor serve sends pj-l-01 the answer"
		reply, err := r.answer("", "a2", "yes, edit internal/x.go too") // the owner: no role
		if err != nil || reply != want {
			t.Fatalf("answer: %q, %v; want %q", reply, err, want)
		}
		if f := r.app.Status().Project.Facts; len(f) != 0 {
			t.Errorf("facts in force after the answer %+v, want none", f)
		}
		line := "[obor answer] yes, edit internal/x.go too"
		r.waitFor("the answer posted to pj-l-01", func() bool { return r.inbox.count("pj-l-01", line) == 1 })
		r.passes()
		if n := r.inbox.count("pj-l-01", line); n != 1 {
			t.Errorf("the answer posted %d times, want once", n)
		}
		if r.hasPrefix("2 ") || r.taskState() != "working" {
			t.Errorf("want no attempt 2 (no fix job), T-1 working: %s, %q", r.taskState(), r.steps())
		}
		if again, err := r.answer("", "a2", "yes, edit internal/x.go too"); err != nil || again != want {
			t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
		}
		if _, err := r.answer("", "a3", "and more"); err == nil || !strings.Contains(err.Error(), "T-1 is working with nothing to resume") {
			t.Errorf("a second answer with no question: %v; want refused", err)
		}
		full := "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
		if reply, err := r.callDone("pj-l-01", "worker", "t1", full); err != nil || reply != "submitted" {
			t.Fatalf("task_done after the answer: %q, %v", reply, err)
		}
		r.waitStep("1 fact submitted")
	})
}

// After its Blocked: report the worker stops, the Stop hook makes it take one more turn (seen ready, then working, then
// ready again) and it waits. That turn is no answer: the question stays in force, its answers' obor resume is accepted
// and reaches the worker; no stall is recorded while it waits.
func TestAnswerAfterTheStopHooksTurn(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := asked(t)
		r.workerIs("idle")
		r.passes()
		r.workerIs("working")
		r.passes()
		r.workerIs("idle")
		r.later(2 * time.Hour) // longer than the stall timer's Idle
		fact := r.needsYou()
		if !strings.HasPrefix(fact.Fact().Reason, "asks: ") || r.has("0 fact resumed") || r.hasPrefix("0 fact stalled") {
			t.Fatalf("the question ended or stalled by the worker's turn: %+v, %q", fact, r.steps())
		}
		if reply, err := r.runPath(fact, "resume", "a1", "yes"); err != nil || !strings.HasPrefix(reply, "ANSWERED T-1") {
			t.Fatalf("%s: %q, %v; want ANSWERED", r.answerOf(fact), reply, err)
		}
		r.waitFor("the answer posted", func() bool { return r.inbox.count("pj-l-01", "[obor answer] yes") == 1 })
		if r.facts() != 0 {
			t.Errorf("facts in force after the answer: %d, want none", r.facts())
		}
	})
}

// Each needs-you's suggested commands (serve's answers: obor status, the lead's needs-you line) are accepted in the
// state that needs-you leaves. asks: TestAnswerSendsTheWorkerTheAnswer, TestAnswerAfterTheStopHooksTurn. A drop
// by lane needs the lane adapters: the dispatch rig's case (the plain rig has none).
func TestNeedsYouAnswerPathsAreAccepted(t *testing.T) {
	t.Parallel()
	hold := func(r *loopRig) event.Record {
		r.t.Helper()
		if _, err := r.ask(&wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1",
			Note: "the owner decides on internal/x.go"}); err != nil {
			r.t.Fatal(err)
		}
		r.waitFor("the hold in force", func() bool { return r.facts() == 1 })
		return r.needsYou()
	}
	red := func(t *testing.T) (*loopRig, event.Record) {
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		r.submit()
		if err := app.WriteLog(r.p.Root, time.Now(), []event.Record{
			{Task: "T-1", Attempt: 1, Step: "submitted", Kind: event.KindFact, Data: []byte(`{"lane":"L-01","agent":"pj-l-01"}`)},
			{Task: "T-1", Attempt: 1, Step: "gate", Kind: event.KindIntent, Class: event.ClassGit, Data: []byte(`{"kind":"lane","lane":"L-01"}`)},
			{Task: "T-1", Attempt: 1, Step: "gate", Kind: event.KindResult, Data: []byte(`{"outcome":"red","line":"RED T-1"}`)},
			{Task: "T-1", Attempt: 1, Step: "needs-you", Kind: event.KindFact,
				Data: []byte(`{"lane":"L-01","reason":"gate red after 2 sendbacks: RED T-1","merge":true,"cause":"red"}`)},
		}...); err != nil {
			t.Fatal(err)
		}
		r.start()
		r.passes()
		return r, r.needsYou()
	}
	ok := func(t *testing.T, r *loopRig, fact event.Record, verb, text, want string) {
		t.Helper()
		// A pass the needs-you's own step (or a delivery) kicked may still hold T-1 while its herdr call or effect runs;
		// obor drop and resume answer only a task no step holds (busy). Two passes: the second started after the
		// needs-you, so it picks no step of T-1; then wait out the one still running.
		r.passes()
		r.waitFor("no step of T-1 running", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
		if reply, err := r.runPath(fact, verb, "p1", text); err != nil || !strings.Contains(reply, want) {
			t.Fatalf("%s (%s): %q, %v; want %s", r.answerOf(fact), verb, reply, err, want)
		}
	}
	t.Run("held → resume with the lead's decision", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, _ := reviewed(t)
			fact := hold(r)
			if p := r.answerOf(fact); !strings.Contains(p, `obor resume T-1 "<decision>"`) {
				t.Errorf("answers %q: want the resume with the owner's decision", p)
			}
			ok(t, r, fact, "resume", "yes: internal/x.go is in scope", "RESUMED T-1: reprompt (held)")
			if !r.has(`1 fact unblocked{reprompt}`) {
				t.Fatalf("want the resume recorded: %q", r.steps())
			}
			job := r.waitJob("pj-int", "T-1/1/review.2")
			if !strings.Contains(job, "Lead's decision on your hold: yes: internal/x.go is in scope\n") {
				t.Errorf("the review job after the resume does not carry the decision:\n%s", job)
			}
			r.stop() // a replay keeps it: the decision is the unblocked fact's
			r.start()
			if job := r.waitJob("pj-int", "T-1/1/review.2"); !strings.Contains(job, "Lead's decision on your hold: yes") {
				t.Errorf("the review job after a restart does not carry the decision:\n%s", job)
			}
		})
	})
	t.Run("held → resume, no decision: refused", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, _ := reviewed(t)
			fact := hold(r)
			r.passes()
			r.waitFor("no step of T-1 running", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
			for _, c := range []struct{ to, note, want string }{
				{"", "", "say the text: obor resume T-1 \"<text>\" sends the integrator its job anew"},
				{wire.ResumeSendback, "keep it out", "--sendback does not fit: obor resume T-1 sends the integrator its job anew"},
				{wire.ResumeRegate, "", "--regate does not fit"},
			} {
				_, err := r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
					Agent: "pj-lead", Role: "lead", CallID: "x" + c.to}, Task: "T-1", Flag: c.to, Text: c.note})
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("resume --%s %q of a hold: %v; want refused %q", c.to, c.note, err, c.want)
				}
			}
			if r.facts() != 1 || r.has("1 fact unblocked{reprompt}") {
				t.Errorf("a refused resume ended the hold: %d facts, %q", r.facts(), r.steps())
			}
			_ = fact
		})
	})
	t.Run("red past K, neither flag: refused naming both", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, fact := red(t)
			if p := r.answerOf(fact); !strings.HasPrefix(p, `obor resume T-1 --regate | obor resume T-1 --sendback "<why>"`) {
				t.Errorf("answers %q: want the two flags", p)
			}
			r.passes()
			r.waitFor("no step of T-1 running", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
			_, err := r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
				Agent: "pj-lead", Role: "lead", CallID: "x1"}, Task: "T-1", Text: "go"})
			if err == nil || !strings.Contains(err.Error(), "say --regate or --sendback") {
				t.Errorf("resume of red past K with no flag: %v; want refused naming both", err)
			}
			if r.has("1 fact unblocked{regate}") {
				t.Errorf("a refused resume recorded: %q", r.steps())
			}
		})
	})
	t.Run("red past K → resume --regate", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, fact := red(t)
			ok(t, r, fact, "resume --regate", "", "RESUMED T-1: regate (red)")
		})
	})
	t.Run("red past K → resume --sendback", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, fact := red(t)
			ok(t, r, fact, "resume --sendback", "fix the gate", "SENDBACK T-1")
		})
	})
	t.Run("a refused dispatch step → resume", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, _ := refusedRig(t)
			ok(t, r, r.needsYou(), "resume", "", "RESUMED T-1: retry")
		})
	})
	t.Run("a refused dispatch step → drop", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, _ := refusedRig(t)
			ok(t, r, r.needsYou(), "drop", "not needed", "DROPPED T-1")
		})
	})
}
