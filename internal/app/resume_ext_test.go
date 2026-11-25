package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app"
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
	if _, err := r.answer("lead", "a0", "yes"); err == nil {
		t.Fatal("precondition: an answer with no question is refused")
	}
	if _, err := r.callDone("pj-l-01", "worker", "b1", "Summary: half done\nBlocked: may I edit internal/x.go?\n"); err != nil {
		t.Fatal(err)
	}
	r.waitStep("0 fact needs-you{asks: may I edit internal/x.go?}")
	return r
}

// obor resume of a question (row 16b): once the worker's Blocked: report is in force, the answer ends the question and
// serve delivers "[obor answer] <text>" to the worker (posted to its session's inbox, as its kind takes a line).
// A worker cannot answer, an empty answer is refused, a repeated call id gets the stored reply, and an answered task has
// no question left. The answer ends the question in the same attempt: the task stays working, and the worker's full
// report submits attempt 1.
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
		r.workerIs("idle")                                              // the worker stopped and waits
		reply, err := r.answer("", "a2", "yes, edit internal/x.go too") // the owner: no role
		if err != nil || !strings.HasPrefix(reply, "ANSWERED T-1") {
			t.Fatalf("answer: %q, %v; want ANSWERED", reply, err)
		}
		line := "[obor answer] yes, edit internal/x.go too"
		r.waitFor("the answer posted to pj-l-01", func() bool { return r.inbox.count("pj-l-01", line) == 1 })
		r.passes()
		if n := r.inbox.count("pj-l-01", line); n != 1 {
			t.Errorf("the answer posted %d times, want once", n)
		}
		if again, err := r.answer("", "a2", "yes, edit internal/x.go too"); err != nil || again != reply {
			t.Errorf("a repeated call id: %q, %v; want the stored reply %q", again, err, reply)
		}
		r.workerIs("working")
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

// A resume line waiting for a worker that already fetched its job is settled by its refetch: the worker's job_next of
// its open work job records fact refetched once, the line retires, its 24 h needs-you ends, and the job's delivered
// fact stays one. A replay of the log gives the same state.
func TestRefetchRetiresWaitingAnswer(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := asked(t) // the job fetched, its question in force
		if reply, err := r.answer("lead", "a1", "yes, edit internal/x.go too"); err != nil ||
			!strings.HasPrefix(reply, "ANSWERED T-1") {
			t.Fatalf("answer: %q, %v; want ANSWERED", reply, err)
		}
		r.later(25 * time.Hour) // busy: the line waits past msgWait
		r.waitFor("the undelivered line's needs-you", func() bool {
			for _, f := range r.app.Status().Project.Facts {
				if strings.Contains(f.Fact().Reason, "waiting over") {
					return true
				}
			}
			return false
		})
		if q := r.waiting(); len(q) != 1 {
			t.Fatalf("precondition: queued %v, want the answer", q)
		}
		if jr := r.workerNext("n2"); jr.Job != "T-1/1/work" {
			t.Fatalf("refetch: %+v, want T-1/1/work", jr)
		}
		r.waitFor("the refetch's fact", func() bool { return r.count("1 fact refetched") == 1 })
		r.waitFor("the line retired", func() bool { return len(r.waiting()) == 0 })
		r.waitFor("the needs-you ended", func() bool { return r.facts() == 0 })
		eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts")
		r.stop()
		r.start()
		r.passes()
		if q, n := r.waiting(), r.count("1 fact refetched"); len(q) != 0 || n != 1 {
			t.Errorf("after a replay: queued %v, refetched facts %d; want none and one", q, n)
		}
		if f := r.facts(); f != 0 {
			t.Errorf("after a replay: %d facts in force, want none", f)
		}
	})
}

// RV-260 verify #1: a resume line whose send is claimed (in flight) when the worker refetches its already fetched job
// is settled by that refetch too: fact refetched once, the line SendingSettled. A send then refused before any byte
// retires it (no needs-you 25 h later); a send that goes out delivers it once and leaves nothing waiting. A replay of
// the log gives the same state.
func TestRefetchSettlesAnswerBeingSent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		refuse bool
		posts  int // the answer's posts that went out
	}{
		{"refused before a byte", true, 0},
		{"delivered", false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := asked(t) // the job fetched, its question in force
				const line = "[obor answer] yes, edit internal/x.go too"
				if reply, err := r.answer("lead", "a1", "yes, edit internal/x.go too"); err != nil ||
					!strings.HasPrefix(reply, "ANSWERED T-1") {
					t.Fatalf("answer: %q, %v; want ANSWERED", reply, err)
				}
				r.passes() // busy: the line waits
				if q := r.waiting(); len(q) != 1 {
					t.Fatalf("precondition: queued %v, want the answer", q)
				}
				started, release := r.inbox.holdPost("[obor answer]", c.refuse)
				defer release()    // a failed assertion below still lets the held post end
				r.workerIs("idle") // the line's send is claimed and held in flight
				select {
				case <-started:
				case <-time.After(time.Minute):
					t.Fatal("precondition: the answer's send did not start")
				}
				if jr := r.workerNext("n2"); jr.Job != "T-1/1/work" {
					t.Fatalf("refetch: %+v, want T-1/1/work", jr)
				}
				if n := r.count("1 fact refetched"); n != 1 {
					t.Fatalf("refetched facts %d during the send, want one: %q", n, r.steps())
				}
				r.workerIs("working") // the refetching worker works: no new delivery is tried
				release()
				r.waitFor("the line's send ended", func() bool { return len(r.waiting()) == 0 })
				r.later(25 * time.Hour)
				r.passes()
				check := func(when string) {
					t.Helper()
					if q := r.waiting(); len(q) != 0 {
						t.Errorf("%s: queued %v, want none", when, q)
					}
					if f := r.app.Status().Project.Facts; len(f) != 0 {
						t.Errorf("%s: facts in force %+v, want none (no waiting-line needs-you)", when, f)
					}
					if n := r.inbox.count("pj-l-01", line); n != c.posts {
						t.Errorf("%s: the answer posted %d times, want %d", when, n, c.posts)
					}
					if n := r.count("1 fact refetched"); n != 1 {
						t.Errorf("%s: refetched facts %d, want one", when, n)
					}
					eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, when+": delivered facts")
				}
				check("25 h after the send")
				r.stop()
				r.start()
				r.passes()
				check("after a replay")
			})
		})
	}
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

// A hold's suggested resume (serve's answers: obor status, the lead's needs-you line), run through obor resume's own
// parser with the lead's decision, is accepted: fact unblocked{reprompt}, and the integrator's next review job carries
// the decision, after a replay too. Every cause's suggested commands being accepted is TestAnswerPathEveryCause's; a
// hold resumed with no decision, or with --regate, is refused by TestResumeActByState's rule (TestHoldAndResume's
// flow); red past K's flags: TestResumeRegate; a refused dispatch step's resume: TestDispatchNeedsYouThenResume.
func TestResumeOfAHoldCarriesTheDecision(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r, _ := reviewed(t)
		if _, err := r.ask(&wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1",
			Note: "the owner decides on internal/x.go"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the hold in force", func() bool { return r.facts() == 1 })
		fact := r.needsYou()
		if p := r.answerOf(fact); !strings.Contains(p, `obor resume T-1 "<decision>"`) {
			t.Errorf("answers %q: want the resume with the owner's decision", p)
		}
		// A pass the needs-you's own step (or a delivery) kicked may still hold T-1 while its herdr call or effect runs;
		// obor resume answers only a task no step holds (busy). Two passes: the second started after the needs-you, so it
		// picks no step of T-1; then wait out the one still running.
		r.passes()
		r.waitFor("no step of T-1 running", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
		const want = "RESUMED T-1: reprompt (held)"
		if reply, err := r.runPath(fact, "resume", "p1", "yes: internal/x.go is in scope"); err != nil ||
			!strings.Contains(reply, want) {
			t.Fatalf("%s (resume): %q, %v; want %s", r.answerOf(fact), reply, err, want)
		}
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
}

// obor resume with a line to a working task whose worker is idle with its job open (no question, no needs-you): the
// line is typed to the worker as the stalled form's is, and the reply says the worker was idle. A worker that is
// working is refused, the refusal naming why; a repeated call id gets the stored reply.
func TestResumeLineToAnIdleWorker(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.inbox.record("pj-l-01")
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		r.workerNext("n1")
		r.waitFor("working", func() bool { return r.taskState() == "working" })
		if f := r.app.Status().Project.Facts; len(f) != 0 {
			t.Fatalf("precondition: facts in force %+v, want none (no question, no needs-you)", f)
		}
		r.workerIs("working")
		_, err := r.answer("lead", "i0", "carry on")
		if err == nil || !strings.Contains(err.Error(), "the worker is working") {
			t.Fatalf("resume of a working worker: %v; want refused, naming that the worker is working", err)
		}
		if n := r.inbox.count("pj-l-01", "[obor answer] carry on"); n != 0 {
			t.Fatalf("the refused line was posted %d times", n)
		}
		r.workerIs("idle")
		const want = "RESUMED T-1: reprompt (worker idle)"
		if reply, err := r.answer("lead", "i1", "carry on"); err != nil || reply != want {
			t.Fatalf("resume of an idle worker: %q, %v; want %q", reply, err, want)
		}
		r.waitFor("the line posted to pj-l-01", func() bool { return r.inbox.count("pj-l-01", "[obor answer] carry on") == 1 })
		r.passes()
		if n := r.inbox.count("pj-l-01", "[obor answer] carry on"); n != 1 {
			t.Errorf("the line posted %d times, want once", n)
		}
		if again, err := r.answer("lead", "i1", "carry on"); err != nil || again != want {
			t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
		}
		if _, err := r.answer("lead", "i2", " "); err == nil || !strings.Contains(err.Error(), "say the text") {
			t.Errorf("an empty line: %v; want refused", err)
		}
		if r.taskState() != "working" || r.hasPrefix("2 ") {
			t.Errorf("want T-1 working on attempt 1: %s, %q", r.taskState(), r.steps())
		}
	})
}
