package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
)

// Delivery (rows 7–8): no job_next within 5 min of the job's wake → one retry; none 5 min after it → the owner; the
// fetch, or the verdict, is delivery and stops the timer.
func TestJobDelivery(t *testing.T) {
	t.Parallel()
	t.Run("undelivered: retry, then needs-you", func(t *testing.T) {
		t.Parallel()
		r := woke(t)
		r.later(4*time.Minute + 59*time.Second)
		if r.has("1 intent retry.review") || r.jobPrompts("T-1/1/review") != 1 {
			t.Fatalf("a retry before 5 min: %q", r.steps())
		}
		r.later(time.Second)
		r.waitStep("1 result retry.review")
		if n := r.jobPrompts("T-1/1/review"); n != 2 {
			t.Errorf("review job prompted %d times, want 2 (the job and its retry)", n)
		}
		if got := r.cleared("pj-int"); len(got) != 1 || !got[0] {
			t.Errorf("input line cleared before prompts %v, want [true] (the retry, the one typed, clears)", got)
		}
		r.later(4*time.Minute + 59*time.Second)
		if r.hasPrefix("1 fact needs-you") {
			t.Fatalf("a needs-you before 5 min after the retry: %q", r.steps())
		}
		r.later(time.Second)
		r.waitStep("1 fact needs-you{job not delivered: T-1/1/review prompted twice to pj-int}")
		r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
		r.later(10 * time.Minute)
		if n := r.jobPrompts("T-1/1/review"); n != 2 || r.count("1 intent retry.review") != 1 {
			t.Errorf("after the needs-you: %d prompts, %d retries; want 2, 1", n, r.count("1 intent retry.review"))
		}
		if st := r.app.Status().Project; st.Tasks[0].State != "needs-you" || len(st.Facts) != 1 {
			t.Errorf("status: %+v", st)
		}
	})
	t.Run("fetched at 3 min: delivered, no timer", func(t *testing.T) {
		t.Parallel()
		r := woke(t)
		r.later(3 * time.Minute)
		r.waitJob("pj-int", "T-1/1/review")
		r.waitStep("1 fact delivered")
		r.integratorIs("working")
		r.later(10 * time.Minute)
		r.later(10 * time.Minute)
		if r.has("1 intent retry.review") || r.has("1 intent nudge.review") || r.hasPrefix("1 fact needs-you") ||
			r.jobPrompts("T-1/1/review") != 1 {
			t.Errorf("a timer fired for a delivered job of a working integrator: %q", r.steps())
		}
	})
	t.Run("verdict without a fetch: delivered", func(t *testing.T) {
		t.Parallel()
		r := woke(t)
		r.later(3 * time.Minute)
		r.accept("c1", r.candidatePatch())
		r.merged(1)
		r.later(10 * time.Minute)
		if r.has("1 fact delivered") || r.has("1 intent retry.review") || r.hasPrefix("1 fact needs-you") ||
			r.jobPrompts("T-1/1/review") != 1 {
			t.Errorf("want merged, no delivered fact needed, no retry: %q", r.steps())
		}
	})
}

// A delivered job, then the integrator ready and quiet with it for 10 min (the stall timer's quiet time) → one nudge
// (the same job id), then 10 more → the owner. The integrator exiting with a job open → started again and the job
// sent anew under a new key (the old job id refused); exiting again → the owner.
func TestJobStalled(t *testing.T) {
	t.Parallel()
	t.Run("idle: nudge, then needs-you", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		r.integratorIs("idle")
		r.quietBaseline("T-1/1/review")
		r.later(9 * time.Minute)
		if r.has("1 intent nudge.review") {
			t.Fatalf("a nudge before 10 min quiet: %q", r.steps())
		}
		r.later(time.Minute)
		r.waitStep("1 result nudge.review")
		var nudge string
		for _, p := range r.prompts() {
			if strings.HasPrefix(p.text, "[obor job T-1/1/review] Reminder:") {
				nudge = p.text
			}
		}
		if nudge == "" || r.jobPrompts("T-1/1/review") != 2 || !strings.Contains(nudge, "open for T-1; answer it with one obor tool (hold") {
			t.Errorf("want the job again with a reminder naming the hold tool: %d prompts, %q", r.jobPrompts("T-1/1/review"), nudge)
		}
		r.later(9 * time.Minute)
		if r.hasPrefix("1 fact needs-you") {
			t.Fatalf("a needs-you before 10 min after the nudge: %q", r.steps())
		}
		r.later(time.Minute)
		r.waitStep("1 fact needs-you{job stalled: pj-int idle with job T-1/1/review for 10m0s after a nudge}")
		if r.count("1 intent nudge.review") != 1 || r.has("1 intent retry.review") {
			t.Errorf("want one nudge, no retry: %q", r.steps())
		}
	})
	t.Run("pane exits: one start, one re-send, then needs-you", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		r.integratorGone("T-1/1/review")
		r.later(30 * time.Second)
		if r.has("1 fact job.lost{integrator exited}") || r.starts.Load() != 1 {
			t.Fatalf("lost before Gone (1 min): %q", r.steps())
		}
		r.later(30 * time.Second)
		job := r.waitJob("pj-int", "T-1/1/review.2")
		if r.starts.Load() != 2 || r.count("1 fact job.lost{integrator exited}") != 1 || !r.has("1 result start.review.2") ||
			r.jobPrompts("T-1/1/review") != 1 || r.jobPrompts("T-1/1/review.2") != 1 {
			t.Errorf("starts %d: want one start and one re-send under review.2: %q", r.starts.Load(), r.steps())
		}
		stale := &wire.VerdictReq{Verb: wire.MethodAccept, Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-int",
			Role: "integrator", CallID: "c1"}, Task: "T-1", Job: "T-1/1/review", Patch: r.patch(job)}
		if _, err := wire.Verdict(context.Background(), r.sock, stale); err == nil ||
			!strings.Contains(err.Error(), "stale job T-1/1/review (T-1's job is T-1/1/review.2)") {
			t.Errorf("accept of the lost job: %v", err)
		}
		r.integratorGone("T-1/1/review.2")
		r.later(time.Minute)
		r.waitStep("1 fact needs-you{job stalled: pj-int exited again with job T-1/1/review.2 open}")
		r.later(time.Minute)
		if r.starts.Load() != 2 || r.count("1 fact job.lost{integrator exited}") != 1 {
			t.Errorf("starts %d after the second exit, want 2 (the owner decides): %q", r.starts.Load(), r.steps())
		}
		r.mustResume("r1", "", "RESUMED T-1: reprompt (job stalled)")
		r.accept("c2", r.patch(r.waitJob("pj-int", "T-1/1/review.3")))
		r.merged(1)
		if r.starts.Load() != 3 {
			t.Errorf("starts %d, want 3", r.starts.Load())
		}
	})
}

// T-946: a delivered integrator job's quiet time starts at its delivered fact, not at serve's or the agent's restart,
// so the nudge comes at most Idle after delivery whatever restarts happen; activity after delivery still moves it
// forward.
func TestJobNudgeFromDelivery(t *testing.T) {
	t.Parallel()
	t.Run("serve restarted at 4 min: the nudge at 10 min from delivery", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		r.integratorIs("idle")
		r.stop() // serve restarted with the job open: the timer is live state, so it used to start over
		r.clock.Add(4 * time.Minute)
		r.start()
		r.later(5 * time.Minute) // 9 min after delivery, 5 after the restart
		if r.has("1 intent nudge.review") {
			t.Fatalf("a nudge 9 min after delivery: %q", r.steps())
		}
		r.later(time.Minute) // 10 min after delivery (not 14: the restart's 4 + Idle)
		r.waitStep("1 result nudge.review")
		if n := r.count("1 intent nudge.review"); n != 1 {
			t.Errorf("%d nudges, want one: %q", n, r.steps())
		}
	})
	t.Run("agent restarted at 12 min: the nudge on the next pass, then needs-you", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		r.integratorIs("idle")
		r.stop()
		r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Session = "int-2" }) // the integrator restarted: a new session
		r.clock.Add(12 * time.Minute)                                     // quiet since delivery (12 min) is already past Idle
		r.start()
		r.waitStep("1 result nudge.review")
		r.later(10 * time.Minute)
		r.waitStep("1 fact needs-you{job stalled: pj-int idle with job T-1/1/review for 10m0s after a nudge}")
	})
	t.Run("activity at 8 min: no nudge before 18 min", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		r.integratorIs("idle")
		r.later(8 * time.Minute)
		r.integratorIs("working") // the integrator works with the job open
		// A pass must see the working pane end the job's quiet time before it idles again (the BACKLOG's T-946 nudge
		// flake under load: the status alone can flip back before a pass sampled it, and the old baseline then nudged
		// at 17 min).
		r.waitFor("the job's quiet time ended by the activity", func() bool {
			job, _, quiet := app.JobTimer(r.app, r.p.Root, "T-1")
			r.push()
			return job == "T-1/1/review" && quiet.IsZero()
		})
		r.integratorIs("idle")
		at := r.clock.Now()
		r.waitFor("the job's quiet time begun again at the activity", func() bool {
			job, _, quiet := app.JobTimer(r.app, r.p.Root, "T-1")
			r.push()
			return job == "T-1/1/review" && quiet.Equal(at)
		})
		r.later(9 * time.Minute) // 17 min after delivery, 9 after the activity
		if r.has("1 intent nudge.review") {
			t.Fatalf("a nudge 9 min after the activity: %q", r.steps())
		}
		r.later(time.Minute) // 18 min after delivery: Idle after the activity
		r.waitStep("1 result nudge.review")
	})
}

// T-960: after a restart following a nudge, the quiet time counts from the nudge, not from delivery, so the needs-you
// comes Idle after the nudge and not on the restart's first sample.
func TestJobNudgeFromNudge(t *testing.T) {
	t.Parallel()
	r, _ := reviewed(t)
	r.integratorIs("idle")
	r.quietBaseline("T-1/1/review")
	r.later(10 * time.Minute) // t0+Idle: the nudge
	r.waitStep("1 result nudge.review")
	r.stop()
	r.clock.Add(2 * time.Minute) // t0+Idle+2m
	r.start()
	r.quietBaseline("T-1/1/review") // the restart's timer, from the nudge, is taken
	if r.hasPrefix("1 fact needs-you") {
		t.Fatalf("needs-you on the restart's first sample: %q", r.steps())
	}
	r.later(7 * time.Minute) // t0+2·Idle−1m: no needs-you before Idle after the nudge
	if r.hasPrefix("1 fact needs-you") {
		t.Fatalf("needs-you before Idle after the nudge: %q", r.steps())
	}
	r.later(time.Minute) // t0+2·Idle: Idle after the nudge
	r.waitStep("1 fact needs-you{job stalled: pj-int idle with job T-1/1/review for 10m0s after a nudge}")
}

// obor hold: the job stays open, needs-you{held: why}, the owner told once; only the integrator holds, with a why;
// a repeated call id gets the stored reply. obor resume (the lead's): its default for held is reprompt, a new job,
// the old job id refused; a repeated call id gets the stored reply, a resume with no needs-you is refused.
func TestHoldAndResume(t *testing.T) {
	t.Parallel()
	t.Run("resume reprompts", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		hold := func(agent, role, callID, note string) (string, error) {
			return wire.Verdict(context.Background(), r.sock, &wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
				Agent: agent, Role: role, CallID: callID}, Verb: wire.MethodHold, Task: "T-1", Note: note})
		}
		if _, err := hold("pj-l-01", "worker", "h0", "why"); err == nil ||
			!strings.Contains(err.Error(), "only the integrator (pj-int) gives verdicts, not pj-l-01 (role worker)") {
			t.Errorf("hold by the worker: %v", err)
		}
		if _, err := hold("pj-int", "integrator", "h0", " "); err == nil || !strings.Contains(err.Error(), "hold: say why") {
			t.Errorf("hold without a why: %v", err)
		}
		if r.hasPrefix("1 fact needs-you") {
			t.Fatalf("a refused hold was recorded: %q", r.steps())
		}
		const reply = "HOLD T-1 (job T-1/1/review): the owner is told"
		for _, id := range []string{"h1", "h1"} {
			if got, err := hold("pj-int", "integrator", id, "spec deviation: the owner decides"); err != nil || got != reply {
				t.Errorf("hold %s: %q, %v; want %q", id, got, err, reply)
			}
		}
		if got, err := hold("pj-int", "integrator", "h2", "again"); err == nil || !strings.Contains(err.Error(), "held already") {
			t.Errorf("a second hold: %q, %v; want refused as held already (the job is open)", got, err)
		}
		r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
		if r.count("1 fact needs-you{held: spec deviation: the owner decides}") != 1 ||
			r.app.Status().Project.Tasks[0].State != "needs-you" {
			t.Errorf("want one needs-you{held}, the task needs-you: %q", r.steps())
		}

		worker := &wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-l-01", Role: "worker",
			CallID: "r0"}, Task: "T-1"}
		if _, err := wire.Verdict(context.Background(), r.sock, worker); err == nil ||
			!strings.Contains(err.Error(), "the lead or the owner resumes a task, not pj-l-01 (role worker)") {
			t.Errorf("resume by the worker: %v", err)
		}
		if _, err := r.resume("r1", ""); err == nil || !strings.Contains(err.Error(), "say the text") {
			t.Errorf("resume of a hold with no decision: %v; want refused (T-775)", err)
		}
		for _, id := range []string{"r1", "r1"} {
			if reply, err := r.resumeWith(id, "", "go on"); err != nil || reply != "RESUMED T-1: reprompt (held)" {
				t.Fatalf("resume %s: %q, %v", id, reply, err)
			}
		}
		// The reprompt sends review.2: wait for that step to finish before a resume reads the task, else it is busy
		// under load, not awaiting (T-796, T-804, T-808's gates saw "T-1 has review.2 running"; BACKLOG 11-04).
		r.waitFor("review.2 out and settled", func() bool {
			return r.has("1 result review.2") && app.Settled(r.app, r.p.Root, "T-1")
		})
		if _, err := r.resume("r2", ""); err == nil || !strings.Contains(err.Error(), "sends it back to its worker") { // awaiting again: a sendback, its why required
			t.Errorf("resume with no needs-you: %v", err)
		}
		job := r.waitJob("pj-int", "T-1/1/review.2")
		stale := &wire.VerdictReq{Verb: wire.MethodAccept, Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-int",
			Role: "integrator", CallID: "c1"}, Task: "T-1", Job: "T-1/1/review", Patch: r.patch(job)}
		if _, err := wire.Verdict(context.Background(), r.sock, stale); err == nil ||
			!strings.Contains(err.Error(), "stale job T-1/1/review") {
			t.Errorf("accept of the held job after the resume: %v", err)
		}
		r.accept("c2", r.patch(job))
		r.merged(1)
		if r.count("1 fact unblocked{reprompt}") != 1 || r.facts() != 0 || r.jobPrompts("T-1/1/review") != 1 {
			t.Errorf("want one unblocked, no fact in force, the held job not sent again: %q", r.steps())
		}
	})
	t.Run("a verdict on the held job ends the hold", func(t *testing.T) {
		t.Parallel()
		r, job := reviewed(t)
		if _, err := wire.Verdict(context.Background(), r.sock, &wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1",
			Note: "asking the owner"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the hold in force", func() bool { return r.facts() == 1 })
		r.accept("c1", r.patch(job))
		r.merged(1)
		if r.facts() != 0 || r.has("1 fact unblocked") {
			t.Errorf("want the hold ended by the accept: %q", r.steps())
		}
	})
}

// obor resume's regate for the causes whose default it is: red past K submits and gates the lane again; a held
// publish is published again; after_merge: TestLoopAfterMergeFailure. A reprompt with no integrator job in the
// attempt is refused.
func TestResumeRegate(t *testing.T) {
	t.Parallel()
	t.Run("red past K", func(t *testing.T) {
		t.Parallel()
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
		if r.scratch.Gates.Load() != 0 || r.app.Status().Project.Tasks[0].State != "needs-you" {
			t.Fatalf("precondition: the task waits for the owner: %q", r.steps())
		}
		if _, err := r.resume("r0", ""); err == nil || !strings.Contains(err.Error(),
			"say --regate or --sendback (gate red after 2 sendbacks: RED T-1)") {
			t.Errorf("resume of red past K with no flag: %v; want refused naming both (T-775)", err)
		}
		r.mustResume("r1", wire.ResumeRegate, "RESUMED T-1: regate (red)")
		r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
		r.merged(1)
		if !r.has("1 result gate.2") || r.scratch.Gates.Load() != 1 || r.mainMerges() != 1 {
			t.Errorf("want gate.2 (a lane gate), merged: %q", r.steps())
		}
	})
	t.Run("publish held", func(t *testing.T) {
		t.Parallel()
		r, job := reviewed(t)
		// an untracked feature.txt in the main checkout's way: the fast-forward would overwrite it (git's refusal:
		// internal/checks' TestPublishDecisions, on real git TestScratchGitScenarios)
		r.scratch.Script("Publish", "T-1", port.PublishHold+" feature.txt")
		r.accept("c1", r.patch(job))
		r.waitPrefix("1 fact needs-you{publish: ")
		r.later(time.Minute)
		if r.scratch.Publishes.Load() != 1 || r.mainMerges() != 0 {
			t.Fatalf("publishes %d, merges %d: want the one refused publish: %q", r.scratch.Publishes.Load(), r.mainMerges(), r.steps())
		}
		r.scratch.Script("Publish", "T-1", "") // the file is gone
		r.mustResume("r1", "", "RESUMED T-1: regate (publish)")
		r.merged(1)
		if !r.has("1 result publish.2") || r.mainMerges() != 1 || r.jobPrompts("T-1/1/review") != 1 {
			t.Errorf("want publish.2, merged, no second job: %q", r.steps())
		}
	})
}

// Crash rows (T-338's table) of the timers and resume: the retry and the re-sent job are prompts (T-524: a pending one
// reaches the integrator once more on replay, the timers go on), unblocked a fact (refused: the resume is given again).
func TestJobRecovery(t *testing.T) {
	t.Parallel()
	for _, before := range []bool{true, false} {
		point := "result lost"
		kind := event.KindResult
		if before {
			point, kind = "before the effect", event.KindIntent
		}
		t.Run("retry/"+point, func(t *testing.T) {
			t.Parallel()
			r := woke(t)
			crashed := r.crashAt(kind, "retry.review")
			r.clock.Add(5 * time.Minute)
			r.waitFor("the crash", crashed.Load)
			r.stop()
			if before {
				r.appendIntent("retry.review", event.ClassPrompt)
			}
			sent := r.jobPrompts("T-1/1/review")
			r.start()
			r.waitTimer("the timer started afresh", func(j string, _, _ time.Time) bool { return j == "T-1/1/review" })
			r.later(4 * time.Minute)
			if r.jobPrompts("T-1/1/review") != sent+1 || r.count("1 intent retry.review") != 1 || len(r.open()) != 0 {
				t.Errorf("the retry was not sent once more on replay, or left open: %q", r.steps())
			}
			r.later(time.Minute)
			r.waitPrefix("1 fact needs-you{job not delivered")
		})
		t.Run("re-sent job/"+point, func(t *testing.T) {
			t.Parallel()
			r, job := reviewed(t)
			r.integratorGone("T-1/1/review")
			crashed := r.crashAt(kind, "review.2")
			r.clock.Add(time.Minute)
			r.waitFor("the crash", crashed.Load)
			r.stop()
			if before { // the intent as the loop writes it: the patch the job's verdict must name
				if err := app.WriteLog(r.p.Root, time.Now(), event.Record{Task: "T-1", Attempt: 1, Step: "review.2",
					Kind: event.KindIntent, Class: event.ClassPrompt, Data: []byte(`{"patch":"` + r.patch(job) + `"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			sent, starts := r.jobPrompts("T-1/1/review.2"), r.starts.Load()
			r.start()
			r.passes()
			// The job reaches the integrator once more: typed, or as the first prompt of the start a crash before the
			// effect left to run.
			restarts := int32(0)
			if before {
				restarts = 1
			}
			if r.jobPrompts("T-1/1/review.2") != sent+1 || r.starts.Load() != starts+restarts ||
				r.count("1 intent review.2") != 1 || r.count("1 fact job.lost{integrator exited}") != 1 || len(r.open()) != 0 {
				t.Errorf("job prompts %d (was %d), starts %d (was %d); want the job once more, %d start: %q",
					r.jobPrompts("T-1/1/review.2"), sent, r.starts.Load(), starts, restarts, r.steps())
			}
			r.accept("c1", r.patch(job))
			r.merged(1)
		})
	}
	t.Run("unblocked refused", func(t *testing.T) {
		t.Parallel()
		r, _ := reviewed(t)
		if _, err := wire.Verdict(context.Background(), r.sock, &wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1",
			Note: "owner"}); err != nil {
			t.Fatal(err)
		}
		crashed := r.crashAt(event.KindFact, "unblocked")
		if _, err := r.resumeWith("r1", "", "go on"); err == nil {
			t.Error("the resume whose fact was refused answered ok")
		}
		r.waitFor("the crash", crashed.Load)
		r.stop()
		r.start()
		r.passes()
		if r.app.Status().Project.Tasks[0].State != "needs-you" || r.jobPrompts("T-1/1/review.2") != 0 {
			t.Fatalf("after the replay: %q", r.steps())
		}
		if reply, err := r.resumeWith("r1", "", "go on"); err != nil || reply != "RESUMED T-1: reprompt (held)" {
			t.Fatalf("resume: %q, %v", reply, err)
		}
		r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review.2")))
		r.merged(1)
		if r.count("1 fact unblocked{reprompt}") != 1 || r.jobPrompts("T-1/1/review.2") != 1 {
			t.Errorf("want one unblocked, one review.2: %q", r.steps())
		}
	})
}

// T-861: the stall timer's sample of the idle integrator's open job (a herdr read on most passes) holds no task: a hold
// and a resume given while it reads are answered, not refused "T-1 has review running; retry when it settles"
// (TestHoldAndResume's and TestJobRecovery/unblocked_refused's load failures: the refused resume left the latter
// waiting a minute for a crash). A sample that comes back due after the resume changed the task nudges nothing: the
// resume's new job goes out, the old one gets no reminder.
func TestJobSampleHoldsNoVerb(t *testing.T) {
	t.Parallel()
	r, _ := reviewed(t)
	r.integratorIs("idle")
	r.quietBaseline("T-1/1/review")
	release, reached := r.srv.Hold("pane.process_info") // the sample's child-process read
	defer release()
	<-reached
	r.clock.Add(10 * time.Minute) // the sample in flight comes back due: quiet with the job for Idle
	if reply, err := wire.Verdict(context.Background(), r.sock, &wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1",
		Note: "owner"}); err != nil {
		t.Fatalf("hold while the job is sampled: %q, %v", reply, err)
	}
	if reply, err := r.resumeWith("r1", "", "go on"); err != nil || reply != "RESUMED T-1: reprompt (held)" {
		t.Fatalf("resume while the job is sampled: %q, %v; want RESUMED", reply, err)
	}
	release()
	r.waitWake("pj-int", "T-1/1/review.2")
	r.passes()
	if r.has("1 intent nudge.review") || r.jobPrompts("T-1/1/review.2") != 1 {
		t.Errorf("want review.2 sent once and no nudge of the resumed job: %q", r.steps())
	}
}
