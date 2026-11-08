package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
)

// failRow is one error cell of the table: arm makes step fail (before the start) and returns what heals it.
type failRow struct {
	step    string // the step's key
	attempt int    // its attempt (0: 1)
	red     bool   // every lane test of T-1 red (a sendback, then attempt 2's fix job)
	arm     func(r *loopRig, ans *answers) (heal func())
	pending func(r *loopRig) // more checks while the step is pending
	end     string           // the record that ends the task once the step ran ("": the merge's last step)
	tick    time.Duration    // the clock moved on per look while waiting for the needs-you (0: not moved)
}

func (row failRow) run(t *testing.T) {
	at := fmt.Sprint(max(row.attempt, 1))
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	if row.red {
		r.scratch.Script("Test", "T-1", port.OutcomeRed)
	}
	ans := &answers{r: r}
	heal := row.arm(r, ans)
	r.start()
	r.submit()
	base, _, _ := strings.Cut(row.step, ".")
	if strings.HasPrefix(row.step, "start.") || row.step == "agent.stop" {
		base = row.step
	}
	needs := at + " fact needs-you{" + row.step + " failed: "
	r.waitFor(needs, func() bool {
		if row.tick > 0 {
			r.clock.Add(row.tick)
		}
		ans.pass()
		return r.hasPrefix(needs)
	})
	failed := at + " fact failed{" + row.step + ": "
	pending := func(when string) {
		t.Helper()
		if r.countPrefix(failed) != 3 || r.count(at+" intent "+row.step) != 1 || r.has(at+" result "+row.step) {
			t.Fatalf("%s: want 3 counted failures, one intent, no result of %s: %q", when, row.step, r.steps())
		}
		if row.pending != nil {
			row.pending(r)
		}
	}
	pending("at the needs-you")
	heal()
	r.later(time.Minute)
	pending("with the needs-you in force")
	r.stop()
	r.start()
	r.later(time.Minute)
	pending("after a restart")

	r.mustResume("r1", "", "RESUMED T-1: retry ("+base+" failed)")
	end := row.end
	if end == "" {
		end = "1 result agent.stop"
	}
	r.waitFor(end, func() bool { ans.pass(); return r.has(end) })
	if !r.has(at+" result "+row.step) || r.count(at+" intent "+row.step) != 1 || r.countPrefix(failed) != 3 {
		t.Errorf("after the resume: want %s's result under its one intent, no more failures: %q", row.step, r.steps())
	}
}

// failRows are the error cells of the table, one per step.
var failRows = map[string]failRow{
	"submit (git I/O)": {step: "submit", arm: func(r *loopRig, _ *answers) func() {
		return r.armScratch("Submit")
	}, pending: func(r *loopRig) {
		if r.scratch.Gates.Load() != 0 || r.scratch.Submits.Load() != 3 {
			r.t.Fatalf("want 3 submits and no gate: submits %d, gates %d: %q", r.scratch.Submits.Load(),
				r.scratch.Gates.Load(), r.steps())
		}
	}},
	"gate (git I/O)": {step: "gate", arm: func(r *loopRig, _ *answers) func() {
		return r.armScratch("Gate")
	}, pending: func(r *loopRig) {
		if r.scratch.Gates.Load() != 3 || !r.hasPrefix("1 fact failed{gate: git I/O: index.lock exists") {
			r.t.Fatalf("want 3 gates, each counted: gates %d: %q", r.scratch.Gates.Load(), r.steps())
		}
	}},
	"publish (git I/O)": {step: "publish", arm: func(r *loopRig, ans *answers) func() {
		heal := func() {}
		ans.beforeAccept = func() { heal = r.armScratch("Publish") }
		return func() { heal() }
	}, pending: func(r *loopRig) {
		if r.mainMerges() != 0 {
			r.t.Fatalf("merged with publish pending")
		}
	}},
	"after_merge (git I/O)": {step: "after_merge", arm: func(r *loopRig, _ *answers) func() {
		return r.armScratch("AfterMerge")
	}, pending: func(r *loopRig) {
		if r.has("1 result after_merge") || r.has("1 intent agent.stop") {
			r.t.Fatalf("after_merge's result written or the worker stopped with after_merge pending: %q", r.steps())
		}
	}},
	"agent.stop (the stop fails, the agent still there)": {step: "agent.stop", arm: func(r *loopRig, _ *answers) func() {
		r.mu.Lock()
		r.onStop = func() error { return errors.New("kill failed") }
		r.mu.Unlock()
		return func() { r.mu.Lock(); r.onStop = nil; r.mu.Unlock() }
	}, pending: func(r *loopRig) {
		if r.has("1 result agent.stop") {
			r.t.Fatalf("agent.stop has a result while it fails: %q", r.steps())
		}
	}},
	"start.review (the launcher)": {step: "start.review", arm: func(r *loopRig, _ *answers) func() {
		r.mu.Lock()
		r.onStart = func() error { return errors.New("no pane for pj-int") }
		r.mu.Unlock()
		return func() { r.mu.Lock(); r.onStart = nil; r.mu.Unlock() }
	}, pending: func(r *loopRig) {
		if len(r.prompts()) != 0 || r.starts.Load() != 3 {
			r.t.Fatalf("want 3 starts and no prompt: starts %d, %q", r.starts.Load(), r.steps())
		}
	}},
	// T-602: a review job's first start is a fresh session (its wake the first prompt), never typed into a running
	// integrator: a blocked integrator is the start's failure (the two rows below); a typed integrator wake that fails
	// is TestRV37V2ResumeRetryKeepsTheJob's (retry, nudge).
	"start.review (integrator idle, start refused)": {step: "start.review", arm: func(r *loopRig, _ *answers) func() {
		in := r.f.Agent("w1:p9", "pj-int", "idle", r.p.Root)
		in.Label, in.Session = "pj-int", "int-0" // a refused start leaves it running
		r.mu.Lock()
		r.onStart = func() error { return errors.New("pj-int has background tasks (finish them, or --force to stop them)") }
		r.mu.Unlock()
		return func() { r.mu.Lock(); r.onStart = nil; r.mu.Unlock() }
	}, pending: func(r *loopRig) { r.startsWere(3, false) }},
	"start.review (integrator blocked past the bound)": {step: "start.review", tick: 20 * time.Second,
		arm: func(r *loopRig, _ *answers) func() {
			in := r.f.Agent("w1:p9", "pj-int", "blocked", r.p.Root)
			in.Label, in.Session = "pj-int", "int-0" // a failed start leaves it running
			r.mu.Lock()
			r.onStart = func() error { return errors.New("pj-int: exit dialog without a numbered option (closed it)") }
			r.mu.Unlock()
			return func() {
				r.mu.Lock()
				r.onStart = nil
				r.mu.Unlock()
				r.integratorIs("idle") // its turn ended
			}
		}, pending: func(r *loopRig) { r.startsWere(3, true) }},
	"fix (agent_blocked)": {step: "fix", attempt: 2, red: true,
		arm: func(r *loopRig, _ *answers) func() {
			r.srv.Fail("agent.prompt", "agent_blocked", "pj-l-01 is blocked")
			return func() { r.srv.Reply("agent.prompt", prompted) }
		}, end: "2 result fix"},
}

// Each error cell: pending (one intent, no result), tried again, a needs-you after 3 counted failures, no attempt
// while it is in force (also after a restart: a failed prompt is not settled as sent), obor resume retries the step.
func TestLoopFailureRule(t *testing.T) {
	t.Parallel()
	for name, row := range failRows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			row.run(t)
		})
	}
}

// Sol's probe TestRV37VerifyStopFailure, fixed: a stop that fails while the agent is still in its pane is pending,
// and the stop is retried after a reload.
func TestRV37VerifyStopFailure(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.mu.Lock()
	r.onStop = func() error { time.Sleep(200 * time.Millisecond); return errors.New("kill failed") }
	r.mu.Unlock()
	r.start()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.waitPrefix("1 fact failed{agent.stop: kill failed}")
	r.stop()
	if r.has("1 result agent.stop") || r.hasPrefix("1 fact needs-you") {
		t.Fatalf("precondition: agent.stop pending, no needs-you: %q", r.steps())
	}
	r.mu.Lock()
	r.onStop = nil
	r.mu.Unlock()
	r.start()
	r.merged(1)
	if r.count("1 intent agent.stop") != 1 || r.result("agent.stop") != `"STOPPED pj-l-01"` ||
		r.f.Pane("w1:p1").Name != "" {
		t.Errorf("want the stop retried under its one intent: %q", r.steps())
	}
}

// The failure count survives a restart: two failures recorded before it, one more after it is the third: the
// needs-you, not a fresh count.
func TestLoopFailureCountSurvivesRestart(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.submit()
	failed := []byte(`{"lane":"L-01","reason":"gate: git I/O: index.lock exists","merge":true,"step":"gate"}`)
	if err := app.WriteLog(r.p.Root, time.Now(), []event.Record{
		{Task: "T-1", Attempt: 1, Step: "submitted", Kind: event.KindFact, Data: []byte(`{"lane":"L-01","agent":"pj-l-01"}`)},
		{Task: "T-1", Attempt: 1, Step: "submit", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-1", Attempt: 1, Step: "submit", Kind: event.KindResult, Data: []byte(`{"sha":"` + r.scratch.LaneHead("L-01") + `"}`)},
		{Task: "T-1", Attempt: 1, Step: "gate", Kind: event.KindIntent, Class: event.ClassGit, Data: []byte(`{"kind":"lane","lane":"L-01"}`)},
		{Task: "T-1", Attempt: 1, Step: "failed", Kind: event.KindFact, Data: failed},
		{Task: "T-1", Attempt: 1, Step: "failed", Kind: event.KindFact, Data: failed},
	}...); err != nil {
		t.Fatal(err)
	}
	defer r.armScratch("Gate")()
	r.start()
	r.waitPrefix("1 fact needs-you{gate failed: git I/O: index.lock exists")
	if n := r.countPrefix("1 fact failed{gate: "); n != 3 || r.scratch.Gates.Load() != 1 {
		t.Errorf("want the third failure (the first after the restart) to be the needs-you: %d failures, %d gates: %q", n,
			r.scratch.Gates.Load(), r.steps())
	}
}

// An attempt obor serve's stop cuts off, or one that ends while herdr is disconnected, is not counted.
func TestLoopFailureNotCounted(t *testing.T) {
	t.Parallel()
	t.Run("ctx cut", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		r.scratch.Arm("Gate", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }) // the stop cuts it off
		r.start()
		r.submit()
		r.waitFor("the gate", func() bool { return r.scratch.Gates.Load() == 1 })
		r.stop()
		r.scratch.Arm("Gate", nil)
		if r.hasPrefix("1 fact failed") || r.has("1 result gate") {
			t.Fatalf("a cut-off gate counted or decided: %q", r.steps())
		}
		if out := r.out.String(); !strings.Contains(out, "shutting down: T-1 failed{gate: context canceled} not recorded") {
			t.Errorf("the cut-off gate's failure was not logged as not recorded:\n%s", out)
		}
		r.start()
		r.waitJob("pj-int", "T-1/1/review")
		if r.hasPrefix("1 fact failed") || r.count("1 intent gate") != 1 {
			t.Errorf("want the gate run again under its intent, nothing counted: %q", r.steps())
		}
	})
	t.Run("herdr disconnected", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		release := make(chan struct{})
		r.scratch.Arm("Gate", func(context.Context) error { <-release; return errors.New("git I/O") })
		r.start()
		r.submit()
		r.waitFor("the gate", func() bool { return r.scratch.Gates.Load() == 1 })
		_, gating := app.Counts(r.app) // the pass running the gate is not done yet
		r.srv.Fail("session.snapshot", "unavailable", "herdr is down")
		r.srv.DropConns()
		r.waitFor("disconnected", func() bool { return !r.app.Status().Connected })
		close(release)
		// Disconnected, no check kicks another pass: wait for the gate's own to end.
		r.waitFor("the gate's pass", func() bool { _, passes := app.Counts(r.app); return passes > gating })
		if r.hasPrefix("1 fact failed") || r.has("1 result gate") || r.count("1 intent gate") != 1 {
			t.Errorf("a gate that failed while herdr was disconnected counted or decided: %q", r.steps())
		}
	})
}

// The final cells beyond the ones the loop tests already reach (green, red, conflict, moved, a check's FAIL, merged,
// already landed, ff refused, lane passed, stopped, started): each is a result and the task goes on.
func TestLoopFinalOutcomes(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name, step, want string
		arm              func(r *loopRig)
	}{
		{name: "after_merge's command fails: a warning, done", step: "after_merge", want: `"WARN after_merge failed: broke"`,
			arm: func(r *loopRig) { r.scratch.Script("AfterMerge", "T-1", "WARN after_merge failed: broke") }},
		{name: "agent.stop: not found (the snapshot lags)", step: "agent.stop", want: `"stopped: no agent (`, arm: func(r *loopRig) {
			r.onStop = func() error { return notFound{} }
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t)
			r.work("feature.txt", "feature\n")
			if row.arm != nil {
				row.arm(r)
			}
			ans := &answers{r: r}
			r.start()
			r.submit()
			r.waitFor("merged", func() bool { ans.pass(); return r.has("1 result agent.stop") })
			if got := r.result(row.step); !strings.HasPrefix(got, row.want) || r.hasPrefix("1 fact failed") {
				t.Errorf("%s's result %s, want %s…, nothing failed: %q", row.step, got, row.want, r.steps())
			}
		})
	}
	t.Run("fix job: the lane names no worker agent", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t)
		r.work("red.txt", "red\n")
		r.scratch.Script("Test", "T-1", port.OutcomeRed)
		r.setLane(func(l *port.Lane) { l.Agent = "" })
		r.p.State("L-01", map[string]any{"lane": "L-01", "task": "T-1", "branch": "task/T-1", "base": "main"})
		r.submit() // before the start: in the log (no agent could call task_done)
		r.start()
		r.waitStep("2 fact needs-you{fix job T-1/2/fix not sent: the lane names no worker agent}")
		r.passes()
		if r.has("2 intent fix") || len(r.prompts()) != 0 || r.hasPrefix("2 fact failed") {
			t.Errorf("want the needs-you, no prompt: %q", r.steps())
		}
	})
}

// RV-37 verify-2 #7, fixed (its probe TestRV37V2ResumeRepromptAuthorization): a retry or a nudge of the open job
// fails three times, obor resume retries it and it is sent: the job is still the one a verdict answers (job
// authorization is the folded task's open job, not a second record unblocked cleared).
func TestRV37V2ResumeRetryKeepsTheJob(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"retry.review", "nudge.review", "retry.resolve", "nudge.resolve"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t)
			job := "T-1/1/review"
			if strings.HasSuffix(step, "resolve") {
				job = "T-1/1/resolve"
				r.conflicting()
			} else {
				r.work("feature.txt", "feature\n")
			}
			r.start()
			r.submit()
			var text string
			if strings.HasPrefix(step, "nudge.") { // fetched: delivered, then quiet with it
				text = r.waitJob("pj-int", job)
				r.integratorIs("idle")
				r.quietBaseline(job)
			} else { // not fetched: undelivered, retried
				r.waitWake("pj-int", job)
			}
			r.srv.Fail("agent.prompt", "agent_blocked", "pj-int is blocked")
			r.later(10 * time.Minute)
			r.waitPrefix("1 fact needs-you{" + step + " failed: ")
			if r.countPrefix("1 fact failed{"+step+": ") != 3 || r.has("1 result "+step) {
				t.Fatalf("precondition: 3 counted failures of %s, no result: %q", step, r.steps())
			}
			r.srv.Reply("agent.prompt", prompted)
			r.waitFor("the needs-you in force", func() bool { return r.facts() == 1 })
			r.mustResume("r1", "", "RESUMED T-1: retry ("+step+" failed)")
			for i := 0; i < 50 && !r.has("1 result "+step); i++ {
				r.later(time.Minute)
			}
			r.waitStep("1 result " + step)
			if text == "" {
				text = r.waitJob("pj-int", job)
			}
			if strings.HasSuffix(step, "resolve") {
				r.resolveInScratch()
				if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job "+job+")" {
					t.Fatalf("resolved after the resumed %s: %q, %v", step, reply, err)
				}
				// T-973: a first-gate conflict's resolution is tested in the lane and then reviewed (its resolve job
				// carries only the conflict); the review's accept publishes it.
				if reply, err := r.verdict(wire.MethodAccept, "c2", r.patch(r.waitJob("pj-int", "T-1/1/review")), ""); err != nil ||
					reply != "ACCEPT T-1 (job T-1/1/review)" {
					t.Fatalf("accept after the resolution: %q, %v", reply, err)
				}
			} else if reply, err := r.verdict(wire.MethodAccept, "c1", r.patch(text), ""); err != nil ||
				reply != "ACCEPT T-1 (job "+job+")" {
				t.Fatalf("accept after the resumed %s: %q, %v", step, reply, err)
			}
			r.merged(1)
		})
	}
}

// A reject unpins the task (the unpin step, G) before the owner is told; an unpin that fails is the failure rule's
// (pending, counted, the owner after 3) and obor resume retries it.
func TestLoopRejectUnpins(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("unpin fails ", fail), func(t *testing.T) {
			t.Parallel()
			r, _ := reviewed(t)
			if len(r.scratch.Pins()) == 0 {
				t.Fatal("precondition: no pins")
			}
			heal := func() {}
			if fail {
				heal = r.armScratch("Unpin")
			}
			if reply, err := r.verdict(wire.MethodReject, "c1", "", "not this"); err != nil || reply != "REJECT T-1 (job T-1/1/review)" {
				t.Fatalf("reject: %q, %v", reply, err)
			}
			if fail {
				r.waitPrefix("1 fact needs-you{unpin failed: git I/O")
				if r.countPrefix("1 fact failed{unpin: ") != 3 || r.has("1 result unpin") || len(r.scratch.Pins()) == 0 {
					t.Fatalf("want unpin pending, counted 3 times, the pins kept: %q", r.steps())
				}
				heal()
				r.waitFor("the needs-you in force", func() bool { return r.facts() == 1 })
				r.mustResume("r1", "", "RESUMED T-1: retry (unpin failed)")
			}
			r.waitStep("1 fact needs-you{rejected by the integrator: not this}")
			if !r.has("1 result unpin") || len(r.scratch.Pins()) != 0 || r.mainMerges() != 0 {
				t.Errorf("want unpinned, nothing merged: pins %q: %q", r.scratch.Pins(), r.steps())
			}
			if _, err := r.resume("r2", ""); err == nil || !strings.Contains(err.Error(), "is rejected") {
				t.Errorf("resume of the reject's needs-you: %v", err)
			}
		})
	}
}

// Submit's Refused is final (a result, not a counted failure): no gate, the owner decides; obor resume's regate
// submits again (submit.2), then the task goes on.
func TestLoopSubmitRefused(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.scratch.Arm("Submit", func(context.Context) error { return port.Refused{Line: "FAIL T-1: lane L-01 is not on a branch"} })
	r.start()
	r.submit()
	r.waitStep("1 fact needs-you{submit: FAIL T-1: lane L-01 is not on a branch}")
	if !r.has("1 result submit") || r.hasPrefix("1 fact failed") || r.scratch.Gates.Load() != 0 || r.scratch.Submits.Load() != 1 {
		t.Fatalf("want one submit with its result, nothing counted, no gate: %q", r.steps())
	}
	r.scratch.Arm("Submit", nil)
	r.waitFor("the needs-you in force", func() bool { return r.facts() == 1 })
	r.mustResume("r1", "", "RESUMED T-1: regate (gate)")
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.merged(1)
	if !r.has("1 result submit.2") || r.mainMerges() != 1 {
		t.Errorf("want submit.2, then merged once: %q", r.steps())
	}
}

// From a task_done to merged with no one but the integrator in it: the integrator is not running, so it is
// started for its review job; it accepts the patch the job names; the app publishes and runs the after-merge work.
func TestLoopHappyPath(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	base := r.scratch.TargetHead()
	r.passes()
	if got := r.steps(); len(got) != 1 || got[0] != "1 fact dispatch" {
		t.Fatalf("precondition: before the task_done only T-1's dispatch: %q", got)
	}

	// T-1005: the captured GateInput's body is the report's whole Summary section, bullets and all (RV-139 #4).
	body := "done.\n" +
		"- internal/app/steps.go: the whole Summary into GateInput.Body\n" +
		"- internal/app/loop_test.go: this assertion"
	rep := "Summary: " + body + "\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	if reply, err := r.taskDone(rep); err != nil || reply != "submitted" {
		t.Fatalf("task_done: %q, %v; records:\n%s", reply, err, strings.Join(r.steps(), "\n"))
	}
	job := r.waitJob("pj-int", "T-1/1/review")
	if got := r.scratch.Tests.Load(); got != 1 {
		t.Errorf("lane tests %d before the review, want 1 (T-961: gate, candidate, test, then the review)", got)
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane is at %s after its test, want its branch back", got)
	}
	if r.starts.Load() != 1 {
		t.Errorf("integrator starts = %d, want 1 (it did not run)", r.starts.Load())
	}
	for _, want := range []string{"Review T-1 for obor serve (attempt 1, lane L-01): the slot compiled it (no commands.compile) " +
		"and its lane test is green (rc=0)", "No spec is stored for T-1.\n", "feature.txt", "The worker's report:\n" + rep,
		"Patch hash: ", "the task's own diff"} {
		if !strings.Contains(job, want) {
			t.Errorf("review job has no %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "Answer with one obor tool") {
		t.Errorf("review job repeats the role text's verbs:\n%s", job)
	}
	if r.scratch.TargetHead() != base {
		t.Fatal("main moved before the accept")
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if c := r.published(); r.mainMerges() != 1 || !strings.Contains(c.Stat, "feature.txt") || r.scratch.TargetHead() != c.SHA {
		t.Errorf("main: %d merges of T-1, at %s; want one merge, its candidate %+v with the feature", r.mainMerges(),
			r.scratch.TargetHead(), c)
	}
	var steps []string // the idle state's own records are not the merge's (T-803): each asserted on its own
	for _, s := range r.steps() {
		switch {
		case strings.HasSuffix(s, " fact idle"), strings.HasSuffix(s, " fact message"):
		case strings.Contains(s, " intent deliver."), strings.Contains(s, " result deliver."):
		default:
			steps = append(steps, s)
		}
	}
	eq(t, steps, []string{"1 fact dispatch", "1 fact submitted", "1 intent submit", "1 result submit", "1 intent gate",
		"1 result gate", "1 intent test", "1 result test", "1 intent review", "1 intent start.review", "1 result start.review", "1 result review",
		"1 fact delivered", "1 intent accept.c1", "1 result accept.c1", "1 intent publish", "1 result publish",
		"1 fact merged", "1 intent after_merge", "1 result after_merge", "1 intent agent.stop", "1 result agent.stop"},
		"records")
	if in := r.scratch.Inputs(); len(in) != 1 || in[0].Summary != "done." || in[0].Title != "" || in[0].Body != body {
		t.Errorf("gate inputs %+v, want one with the report's summary, the whole Summary section as its body and no title (the merge's subject)", in)
	}
	if r.scratch.AfterMerges.Load() != 1 || r.scratch.Publishes.Load() != 1 {
		t.Errorf("after_merge runs %d, publishes %d; want 1, 1", r.scratch.AfterMerges.Load(), r.scratch.Publishes.Load())
	}
	if r.p.Exists(".obor/state/done") {
		t.Errorf("a done-file archive dir (gone with T-437)")
	}
	if got := strings.Join(r.workerStops(), ","); got != "pj-l-01 w1:p1" {
		t.Errorf("stops %q; want pj-l-01 in its submitted pane", got)
	}
	if n := len(r.stopped()) - len(r.workerStops()); n != 1 {
		t.Errorf("integrator stops %d (%q), want 1: after its verdict, no job left for it (T-672)", n, r.stopped())
	}
	r.noLead()
	if n := len(r.prompts()); n != 0 {
		t.Errorf("prompts = %d, want none (the integrator's start carried the review job's wake)", n)
	}
}

// A red lane test (T-961) goes back to the worker with the reasons; its next task_done is the next attempt. After K = 2 sendbacks a third red goes to the owner (one notification), not to the worker again.
func TestLoopRedSendsBackTwiceThenNeedsYou(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("red.txt", "red\n")
	r.scratch.Script("Test", "T-1", port.OutcomeRed) // every lane test of T-1
	r.start()
	r.submit()
	for attempt := 2; attempt <= 3; attempt++ {
		job := r.waitJob("pj-l-01", "T-1/"+string(rune('0'+attempt))+"/fix")
		for _, want := range []string{"T-1 is sent back to you", "RED check", "call task_done again"} {
			if !strings.Contains(job, want) {
				t.Errorf("fix job %d has no %q:\n%s", attempt, want, job)
			}
		}
		r.waitStep(string(rune('0'+attempt-1)) + " fact sendback")
		r.submit()
		r.waitStep(string(rune('0'+attempt)) + " fact submitted")
	}
	r.waitStep("3 result test") // T-961: the class's check runs in the lane test, not the gate
	r.waitStep("3 fact needs-you{gate red after 2 sendbacks: " + r.redLine() + "}")
	r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if n := r.count("3 fact sendback"); n != 0 {
		t.Errorf("a third sendback recorded")
	}
	fixes := 0
	for _, p := range r.prompts() {
		if p.to == "pj-l-01" && strings.HasSuffix(p.text, "/fix] call the obor job_next tool") {
			fixes++
		}
	}
	if fixes != 2 || r.mainMerges() != 0 {
		t.Errorf("fix jobs %d, merges %d; want 2, 0", fixes, r.mainMerges())
	}
	if st := r.app.Status().Project; len(st.Tasks) != 1 || st.Tasks[0].State != "needs-you" || len(st.Facts) != 1 {
		t.Errorf("status: %+v", st)
	}
	r.noLead()
}

// A conflict is a resolve job for the integrator (not the owner): it resolves and commits the merge in the scratch,
// says resolved, the app gates that merge, tests it once in the lane, and reviews it (T-973: the resolve job carries
// only the conflict, so a first-gate conflict gets the task's review), then publishes after the accept.
func TestLoopConflictResolvedThenMerged(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.conflicting()
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	for _, want := range []string{"Resolve T-1's merge for obor serve", "CONFLICT in " + scratch, "shared.txt"} {
		if !strings.Contains(job, want) {
			t.Errorf("resolve job has no %q:\n%s", want, job)
		}
	}
	own, _, _ := strings.Cut(job, "Your role text (") // the job's own part: the role texts (T-941) come after it
	if strings.Contains(own, "obor resolved tool") || strings.Contains(own, "git commit -m") {
		t.Errorf("resolve job repeats the role text's steps:\n%s", job)
	}
	if strings.Contains(own, "--ff-retry") {
		t.Errorf("resolve job names obor merge --ff-retry:\n%s", job)
	}
	if _, err := r.verdict(wire.MethodAccept, "c0", "x", ""); err == nil || !strings.Contains(err.Error(), "is a resolve job") {
		t.Errorf("accept on the resolve job: %v", err)
	}
	resolution := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job T-1/1/resolve)" {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	// T-973: a first-gate conflict had no accept, so the resolved candidate's green lane test is reviewed once.
	review := r.waitJob("pj-int", "T-1/1/review")
	r.accept("c2", r.patch(review))
	r.merged(1)
	if c := r.published(); c.SHA != resolution || r.mainMerges() != 1 {
		t.Errorf("published %s, merges %d; want the resolution %s, merged once", c.SHA, r.mainMerges(), resolution)
	}
	for _, s := range []string{"1 result gate", "1 intent resolve", "1 result resolved.c1", "1 result gate.2",
		"1 intent test", "1 result test", "1 intent review", "1 result review", "1 result publish", "1 fact merged"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	if n := r.count("1 intent resolve"); n != 1 {
		t.Errorf("resolve jobs %d, want one: %q", n, r.steps())
	}
	if n := r.count("1 intent review"); n != 1 {
		t.Errorf("review jobs %d, want one (a first-gate conflict's single review): %q", n, r.steps())
	}
	if r.has("1 fact accept.kept") {
		t.Errorf("a kept accept with no earlier accept: %q", r.steps())
	}
	if r.starts.Load() != 2 {
		t.Errorf("integrator starts = %d, want 2 (the resolve's and the review's)", r.starts.Load())
	}
	r.noLead()
}

// The target moves between the review job and the accept: publish finds it moved, the gate runs again on the new
// target (T-961: the slot merges and compiles the task onto it again, gate.2). The accept binds to the task's own diff
// Base..S, which a move leaves as it was: accept.kept, then publish.2, no lane test and no review job on a move; a
// verdict after the merge finds no job. (That a move in the patch's context keeps the patch and both edits is git's:
// internal/checks' TestGateCandidateIsTheTasksOwnDiff.)
func TestLoopTargetMoved(t *testing.T) {
	t.Parallel()
	t.Run("identical patch", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		r.start()
		r.submit()
		job := r.waitJob("pj-int", "T-1/1/review")
		if n := r.scratch.Tests.Load(); n != 1 {
			t.Fatalf("precondition: one lane test before the review, got %d", n)
		}
		submitted := r.scratch.LaneHead("L-01")
		moved := r.scratch.Move() // an unrelated commit on main
		r.accept("c1", r.patch(job))
		r.merged(1)
		for _, s := range []string{"1 result publish", "1 fact target.moved", "1 result gate.2", "1 fact accept.kept",
			"1 result publish.2", "1 fact merged"} {
			if !r.has(s) {
				t.Errorf("no record %q: %q", s, r.steps())
			}
		}
		c := r.published()
		if n := r.jobPrompts("T-1/1/review"); n != 1 || len(r.prompts()) != 0 || r.mainMerges() != 1 || c.Target != moved {
			t.Errorf("prompts %d, merges %d, onto %s; want the one review job, merged once on the moved main %s", n,
				r.mainMerges(), c.Target, moved)
		}
		if got := r.scratch.Merges(""); got != 1 {
			t.Errorf("%d merges on main, want one for T-1 (T-403)", got)
		}
		if got := r.scratch.Parents(r.scratch.TargetHead()); len(got) != 2 || got[1] != submitted {
			t.Errorf("main's merge's parents %q, want the submitted sha %s second", got, submitted)
		}
		in := r.scratch.Inputs()
		if len(in) != 2 || in[1].Kind != port.InputMoved || in[1].Input != submitted || in[1].Summary != "done." {
			t.Errorf("gate inputs %+v; want the moved gate of the submitted sha, the task's subject rule (T-360)", in)
		}
		for _, s := range []string{"1 intent test.2", "1 intent review.2"} {
			if r.has(s) {
				t.Errorf("a move ran %q: %q", s, r.steps())
			}
		}
		if n := r.scratch.Tests.Load(); n != 1 {
			t.Errorf("lane tests %d, want the one before the review (none on the move)", n)
		}
		if _, err := r.verdict(wire.MethodReject, "c5", "", "late"); err == nil || !strings.Contains(err.Error(), "no job open") {
			t.Errorf("a verdict after the merge: %v", err)
		}
	})
}

// T-403: a moved gate that conflicts is resolved against the submitted sha (the merge's second parent), then gated as
// committed, tested in the lane and merged (T-961: no second review); the old candidate does not go along.
func TestLoopMovedConflictResolved(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	submitted := r.scratch.LaneHead("L-01")
	r.movedConflict() // main edits the lane's line: the moved merge conflicts
	r.accept("c1", r.patch(job))
	job = r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	if !strings.Contains(job, "CONFLICT in "+scratch) {
		t.Fatalf("resolve job does not name the moved gate's conflict:\n%s", job)
	}
	resolution := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job T-1/1/resolve)" {
		t.Fatalf("resolved against the submitted sha: %q, %v", reply, err)
	}
	// T-961: the resolution is compiled (gate.3), tested once in the lane (test.2) and not reviewed again: the accept
	// binds to the task's own diff, which the resolution leaves as it was (accept.kept).
	r.merged(1)
	for _, s := range []string{"1 result gate.3", "1 intent test.2", "1 result test.2", "1 fact accept.kept"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	if n := r.scratch.Tests.Load(); n != 2 {
		t.Errorf("lane tests %d, want 2: the lane merge's and the resolution's, the move's none", n)
	}
	if n := r.jobPrompts("T-1/1/review.2"); n != 0 || r.has("1 intent review.2") {
		t.Errorf("the resolution was reviewed again (%d prompts): %q", n, r.steps())
	}
	if got := r.scratch.TargetHead(); got != resolution || r.scratch.Merges("") != 1 {
		t.Errorf("main at %s with %d merges, want the resolution %s, one merge for T-1 (T-403)", got,
			r.scratch.Merges(""), resolution)
	}
	if got := r.scratch.Parents(resolution); len(got) != 2 || got[1] != submitted {
		t.Errorf("the resolution's parents %q, want the submitted sha %s second", got, submitted)
	}
}

// An obor merge outside the app while the task waits for its accept (the cutover's race): reconciled to merged, no
// publish and no after-merge work of its own; the integrator's late accept finds no job.
func TestLoopMergedOutsideTheApp(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	r.scratch.Land("T-1") // obor merge by hand: "Merge T-1: by hand" on main
	r.waitStep("1 fact merged")
	var d struct{ Outside bool }
	for _, rec := range r.records() {
		if rec.Step == "merged" {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	r.passes()
	if !d.Outside || r.scratch.Publishes.Load() != 0 || r.has("1 intent after_merge") || r.has("1 intent archive") ||
		r.has("1 intent agent.stop") || r.mainMerges() != 1 {
		t.Errorf("outside %v, publishes %d, merges %d: %q", d.Outside, r.scratch.Publishes.Load(), r.mainMerges(), r.steps())
	}
	if _, err := r.verdict(wire.MethodAccept, "c1", r.patch(job), ""); err == nil || !strings.Contains(err.Error(), "no job open") {
		t.Errorf("accept after the outside merge: %v", err)
	}
}

// obor serve restarts while the task waits for its accept: the replay settles the review prompt (never sent again),
// and the integrator's verb after the restart merges it.
func TestLoopRestartWhileAwaitingAccept(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	r.waitStep("1 result review")
	r.stop()
	r.start()
	r.passes()
	if st := r.app.Status().Project; len(st.Tasks) != 1 || st.Tasks[0].State != "awaiting-accept" {
		t.Fatalf("after the restart: %+v", st.Tasks)
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if n := r.jobPrompts("T-1/1/review"); n != 1 || r.mainMerges() != 1 || r.scratch.Gates.Load() != 1 {
		t.Errorf("prompts %d, merges %d, prepares %d; want 1 each", n, r.mainMerges(), r.scratch.Gates.Load())
	}
}

// A pass that cannot build the loop for a project (a config the binary cannot read) is a fact (T-377): one needs-you
// "loop: <error>" and one notification however many passes fail, a changed error a new one, and the first pass that
// works ends it.
func TestLoopErrorIsAFactOnceAndClears(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	setErr := func(err error) {
		r.mu.Lock()
		r.loopErr = err
		r.mu.Unlock()
	}
	setErr(errors.New("config.json: boom"))
	r.start()
	r.waitStep("0 fact needs-you{loop: config.json: boom}")
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	for i := 0; i < 3; i++ {
		r.passes()
	}
	if n, m := r.count("0 fact needs-you{loop: config.json: boom}"), countReq(r.srv, "notification.show"); n != 1 || m != 1 {
		t.Errorf("after repeated failing passes: %d needs-you, %d notifications; want 1, 1", n, m)
	}
	if st := r.app.Status().Project; len(st.Facts) != 1 || st.Facts[0].Task != event.LoopTask {
		t.Errorf("status facts: %+v", st.Facts)
	}

	setErr(errors.New("config.json: other"))
	r.waitStep("0 fact needs-you{loop: config.json: other}")
	r.waitFor("the second notification", func() bool { return countReq(r.srv, "notification.show") == 2 })
	if st := r.app.Status().Project; len(st.Facts) != 1 {
		t.Errorf("a changed error must replace the fact, status facts: %+v", st.Facts)
	}

	setErr(nil)
	r.waitFor("the fact to clear", func() bool { return len(r.app.Status().Project.Facts) == 0 })
	if !r.has("0 fact resumed") {
		t.Errorf("no resumed record: %q", r.steps())
	}
	r.passes()
	r.passes()
	if n := r.count("0 fact resumed"); n != 2 { // one for the replaced error, one for the end
		t.Errorf("resumed records = %d, want 2 (no more once the loop works): %q", n, r.steps())
	}
}

// T-360 (RV-39 #6), T-440, T-521: the lane's commit and its merge take the task's title from the stored spec; the
// title in the main checkout's spec file changes neither. The review job carries the gated spec rev's text; the gate
// gets that rev and its text for the merge's message (the message itself, and that the gate writes no spec file, are
// internal/checks': TestGateDecisions).
func TestCommitSubjectFromStoredSpec(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	const committed = "# T-1 — Committed target title"
	r.p.Write("docs/tasks/T-1.md", committed+"\n")
	r.work("feature.txt", "feature\n")
	r.start()
	const stored = "# T-1 — Stored title\n\nDifficulty: medium\n\n## Files you may touch\n`feature.txt`\n"
	r.setSpec("s1", stored)
	r.submit()
	r.waitStep("1 result submit")
	if got := r.scratch.Subjects(); len(got) != 1 || got[0] != "T-1: Stored title" {
		t.Errorf("lane commit subjects %q, want the stored spec's title", got)
	}
	job := r.waitJob("pj-int", "T-1/1/review")
	if want := "Spec to review: rev 1 (the one the candidate's merge message contains):\n\nThe spec of T-1 (rev 1, " +
		"as obor serve stores it; obor spec show T-1 prints the current rev):\n\n" + stored; !strings.Contains(job, want) {
		t.Errorf("review job has no %q:\n%s", want, job)
	}
	if strings.Contains(job, "docs/tasks") {
		t.Errorf("review job names a spec file:\n%s", job)
	}
	// The lead changes the spec after the gate: the job names the gated rev and where its text is, then the current one.
	const later = "# T-1 — Stored title\n\nDifficulty: medium\n\nwidened\n"
	r.setSpec("s2", later)
	again, ok := r.fetch("pj-int", "T-1/1/review")
	if !ok {
		t.Fatal("review job not fetched again")
	}
	if want := "Spec to review: rev 1, in the candidate's merge message (git log -1 --format=%B "; !strings.Contains(again, want) ||
		!strings.Contains(again, "(rev 2, as obor serve stores it") || !strings.Contains(again, later) {
		t.Errorf("review job after the spec changed has no %q nor rev 2's text:\n%s", want, again)
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if in := r.scratch.Inputs(); len(in) != 1 || in[0].Title != "Stored title" || in[0].SpecRev != 1 || in[0].Spec != stored {
		t.Errorf("gate inputs %+v, want the stored spec's title, then rev 1 and its text", in)
	}
	if got := r.p.Read("docs/tasks/T-1.md"); got != committed+"\n" {
		t.Errorf("the main checkout's docs/tasks/T-1.md = %q, want it as it was", got)
	}
}

// T-861: a merge-loop pass in a resubscribe's gap (the rig's push ends serve's stream; herdr has not answered the next
// subscribe yet) skips the project, so it is no pass a test waits on (passes, later: StreamPasses). Counted, two such
// passes let a wait return before any pass saw the clock the test had moved (TestBriefEndsWhenItsAgentIsGone/
// not_identified's 62 s wait: the brief's lost-since was sampled 30 s late). Passes count again on the next stream.
func TestPassInResubscribeGapNotCounted(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, noCheck())
	r.binary = func() bool { return false } // the binary watch wakes the merge loop each tick, herdr connected or not
	r.start()
	r.passes()
	release, reached := r.srv.Hold("events.subscribe")
	defer release()
	r.srv.Push("workspace_created", workspaceEvent) // raw: the next subscribe is held
	<-reached
	r.waitFor("serve between two streams", func() bool { return !r.app.Status().Connected })
	_, all := app.Counts(r.app)
	before := app.StreamPasses(r.app)
	r.waitFor("three passes in the gap", func() bool { _, n := app.Counts(r.app); return n >= all+3 })
	during := app.StreamPasses(r.app)
	if r.app.Status().Connected {
		t.Fatal("precondition: serve connected again while its subscribe is held")
	}
	if during != before {
		t.Errorf("stream passes %d → %d while serve was not connected: want none counted", before, during)
	}
	release()
	r.passes() // the next stream's passes count: this wait ends
}

// answers is the integrator as a test plays it: each resolve job is resolved in the scratch (the open conflict
// committed) and answered resolved, each review job accepted (beforeAccept runs once first); only a job whose prompt
// has its result (sent). A verdict the app refuses (its log refused it: a crash) is given again later.
type answers struct {
	r            *loopRig
	beforeAccept func()
	done         map[string]bool
	calls        int
}

// pass answers the jobs not answered yet: each one woken (typed, or carried by the integrator's start) and sent is
// fetched by job_next, then answered.
func (a *answers) pass() {
	if a.done == nil {
		a.done = map[string]bool{}
	}
	for _, job := range a.r.woken("pj-int") {
		if a.done[job] || !a.r.sent(job) {
			continue
		}
		text, ok := a.r.fetch("pj-int", job)
		if !ok {
			continue
		}
		m := []string{"", job}
		p := sent{to: "pj-int", text: text}
		a.calls++
		id := fmt.Sprintf("c%d", a.calls)
		var err error
		if strings.HasSuffix(m[1], "/resolve") || strings.Contains(m[1], "/resolve.") {
			if a.r.scratch.Open() {
				a.r.scratch.Resolve()
			}
			_, err = a.r.verdict(wire.MethodResolved, id, "", "")
		} else {
			if a.beforeAccept != nil {
				a.beforeAccept()
				a.beforeAccept = nil
			}
			_, err = a.r.verdict(wire.MethodAccept, id, a.r.patch(p.text), "")
		}
		if err == nil {
			a.done[m[1]] = true
		}
	}
}

// A merge-loop scenario: how the task gets to its end, and what that end is.
type scenario struct {
	setup     func(r *loopRig) // the lane's work, main's commits and the scripted outcomes, before the app starts
	moveMain  bool             // main moves before the accept (an unrelated commit)
	end       string           // the record that ends it ("" merged)
	publishes int32            // Publish calls when merged
	jobs      int32            // integrator jobs, each its fresh start (T-602; 0: 1)
}

var scenarios = map[string]scenario{
	"lane":     {setup: func(r *loopRig) { r.work("feature.txt", "feature\n") }, publishes: 1},
	"moved":    {setup: func(r *loopRig) { r.work("feature.txt", "feature\n") }, moveMain: true, publishes: 2},
	"resolved": {setup: (*loopRig).conflicting, publishes: 1, jobs: 2},
	// the resolution's lane test is red
	"resolved red": {setup: func(r *loopRig) {
		r.conflicting()
		r.scratch.Script("Test", "T-1", port.OutcomeRed)
	}, end: "1 fact sendback"},
	// the resolved gate's lane_setup fails
	"resolved fail": {setup: func(r *loopRig) {
		r.conflicting()
		r.scratch.Script("Gate resolved", "T-1", port.OutcomeFail+" FAIL lane_setup (rc 3)")
	}, end: "1 fact needs-you{gate: FAIL lane_setup (rc 3)}"},
}

// Each step's effect at both crash points (before it; done, result lost): the restart ends the task as without the
// crash: merged exactly once (one merge on main, one fact merged, after_merge's effect once: run again only after a
// cut between it and its result, idempotent (T-525), the lane's work committed once), no prompt sent twice, one integrator start per job (T-602), before any job reaches it, and lane L-02 untouched. A red or failed
// resolved gate replays on the same pinned resolution: one sendback, or one needs-you.
func TestLoopRecovery(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ scenario, step, class string }{
		{"lane", "submit", event.ClassGit},
		{"lane", "gate", event.ClassGit},
		{"lane", "start.review", event.ClassStart},
		{"lane", "publish", event.ClassGit},
		{"lane", "after_merge", event.ClassGit},
		{"lane", "agent.stop", event.ClassStart},
		{"moved", "gate.2", event.ClassGit},
		{"resolved", "gate", event.ClassGit},
		{"resolved", "start.resolve", event.ClassStart},
		{"resolved", "gate.2", event.ClassGit},
		{"resolved red", "gate.2", event.ClassGit},
		{"resolved fail", "gate.2", event.ClassGit},
	} {
		for _, before := range []bool{true, false} {
			point := "result lost"
			if before {
				point = "before the effect"
			}
			t.Run(row.scenario+"/"+row.step+"/"+point, func(t *testing.T) {
				t.Parallel()
				sc := scenarios[row.scenario]
				r := newLoopRig(t)
				r.foreign()
				sc.setup(r)
				var early atomic.Bool // a job prompt reached pj-int before it was started
				r.srv.Before(func(req herdrtest.Request) {
					if req.Method == "agent.prompt" && strings.Contains(string(req.Params), `"pj-int"`) && r.starts.Load() == 0 {
						early.Store(true)
					}
				})
				ans := &answers{r: r}
				if sc.moveMain {
					ans.beforeAccept = func() { r.scratch.Move() } // an unrelated commit on main
				}
				r.start()
				kind := event.KindResult
				if before {
					kind = event.KindIntent
				}
				crashed := r.crashAt(kind, row.step)
				r.submit()
				r.waitFor("the crash at "+kind+" "+row.step, func() bool { ans.pass(); return crashed.Load() })
				r.stop()
				if r.has("1 " + kind + " " + row.step) {
					t.Fatalf("precondition: the %s %s reached the log", row.step, kind)
				}
				if before {
					r.appendIntent(row.step, row.class)
				}
				r.start()
				end := sc.end
				if end == "" {
					end = "1 result agent.stop"
				}
				r.waitFor(end, func() bool {
					ans.pass()
					for _, s := range r.steps() {
						if strings.HasPrefix(s, end) {
							return true
						}
					}
					return false
				})

				jobs := map[string]int{}
				for _, p := range r.prompts() {
					if m := jobRe.FindStringSubmatch(p.text); m != nil {
						jobs[m[1]]++
					}
				}
				for job, n := range jobs {
					if n != 1 {
						t.Errorf("job %s prompted %d times", job, n)
					}
				}
				if want := max(sc.jobs, 1); r.starts.Load() != want || early.Load() {
					t.Errorf("integrator starts %d (want %d: one per job), a job before the start: %v", r.starts.Load(),
						want, early.Load())
				}
				for _, s := range r.workerStops() {
					if s != "pj-l-01 w1:p1" {
						t.Errorf("a stop of %s", s)
					}
				}
				if r.count("1 intent "+row.step) != 1 {
					t.Errorf("%d intents of %s, want 1: %q", r.count("1 intent "+row.step), row.step, r.steps())
				}
				r.waitFor("every intent with its result", func() bool { return len(r.open()) == 0 })
				if sc.end != "" {
					if r.mainMerges() != 0 || r.count("1 result gate.2") != 1 || r.count("1 fact sendback")+
						r.count("1 fact needs-you{gate: FAIL lane_setup (rc 3)}") != 1 {
						t.Errorf("want one resolved gate result, one sendback or needs-you, no merge: %q", r.steps())
					}
					return
				}
				publishes := sc.publishes
				if row.step == "publish" && !before {
					publishes++ // the replay's publish finds the candidate in the target: Already
				}
				runs := 1
				if row.step == "after_merge" && !before {
					runs++ // the replay runs the idempotent after_merge again (its intent is the once, T-525)
				}
				if r.mainMerges() != 1 || r.scratch.Publishes.Load() != publishes || r.count("1 fact merged") != 1 ||
					r.count("1 result after_merge") != 1 || r.scratch.AfterRuns("T-1") != runs {
					t.Errorf("merges %d, publishes %d (want %d), after_merge runs %d (want %d): %q",
						r.mainMerges(), r.scratch.Publishes.Load(), publishes, r.scratch.AfterRuns("T-1"), runs, r.steps())
				}
				if n := r.scratch.Commits("T-1"); n != 1 {
					t.Errorf("the lane has %d submit commits of T-1, want the one", n)
				}
				if r.scratch.LaneHead("L-02") != r.scratch.LaneBase("L-02") || r.scratch.LaneAt("L-02") != "" {
					t.Errorf("lane L-02 was touched: head %s, at %q", r.scratch.LaneHead("L-02"), r.scratch.LaneAt("L-02"))
				}
				if len(r.workerStops()) != 1 {
					t.Errorf("stops %q, want pj-l-01 once", r.stopped())
				}
			})
		}
	}
}

// RV-37 #2 (Sol's probe TestRV37StartIntentBeforeEffect): a start.review intent whose effect never ran, the
// integrator not running on restart: it is started once, carrying the review job's wake (T-524: every start under its
// job carries it), nothing typed.
func TestLoopStartIntentBeforeEffect(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	crashed := r.crashAt(event.KindIntent, "start.review")
	r.submit()
	r.waitFor("the crash", crashed.Load)
	r.stop()
	if r.starts.Load() != 0 || len(r.prompts()) != 0 {
		t.Fatalf("precondition: starts %d, prompts %d before the restart", r.starts.Load(), len(r.prompts()))
	}
	r.appendIntent("start.review", event.ClassStart)
	var early atomic.Bool
	r.srv.Before(func(req herdrtest.Request) {
		if req.Method == "agent.prompt" && r.starts.Load() == 0 {
			early.Store(true)
		}
	})
	r.start()
	r.waitJob("pj-int", "T-1/1/review")
	r.waitStep("1 result review")
	if r.starts.Load() != 1 || early.Load() || len(r.prompts()) != 0 || r.result("review") != `"sent (start prompt)"` {
		t.Errorf("starts %d, prompt before the start %v, prompts %d, review %s; want one start carrying the job",
			r.starts.Load(), early.Load(), len(r.prompts()), r.result("review"))
	}
	if r.count("1 intent start.review") != 1 || r.result("start.review") != `{"line":"STARTED pj-int","agent":"pj-int","pane":"w1:p9","kind":"claude","sid":"int-1"}` || r.has("1 intent start.review.2") {
		t.Errorf("start.review: want its one intent, the start's line as its result, no second key: %q", r.steps())
	}
}

// RV-37 #1 (Sol's probe TestRV37AfterMergeCrashThenLaneReused): after_merge ran, its result was lost, and while obor
// serve was down the lane took T-06 (its dispatch in the log). The replay stops nothing: T-06's worker stays.
func TestLoopAfterMergeCrashThenLaneReused(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	crashed := r.crashAt(event.KindResult, "after_merge")
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.waitFor("the crash", crashed.Load)
	r.stop()
	// T-06's dispatch onto L-01 and its lane.prepare, in the log: its row takes the lane (T-439).
	d, _ := json.Marshal(map[string]any{"lane": "L-01", "dir": r.lane, "agent": "pj-l-01", "branch": "task/T-06",
		"target": "main", "reply": "DISPATCHED T-06 → L-01 (queued)"})
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-06", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d},
		event.Record{Task: "T-06", Attempt: 1, Step: event.StepPrepare, Kind: event.KindIntent, Class: event.ClassGit},
		event.Record{Task: "T-06", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult,
			Data: json.RawMessage(`{"base":"main"}`)}); err != nil {
		t.Fatal(err)
	}
	r.setLane(func(l *port.Lane) { l.Task = "T-06" })
	r.start()
	r.merged(1)
	if len(r.workerStops()) != 0 || r.f.Pane("w1:p1").Name != "pj-l-01" {
		t.Errorf("the lane's worker was stopped: %q", r.stopped())
	}
	for _, step := range []string{"agent.stop"} {
		if res := r.result(step); res != `"skipped: lane L-01 holds T-06"` {
			t.Errorf("%s's result %s, want skipped", step, res)
		}
	}
}

// RV-37 #4 (Sol's probe TestRV37AfterMergeLockFailure): after_merge fails: no result, the step stays pending (one
// intent), the owner is told after three tries and it is not tried again (T-339: it would hold up every other task
// each pass), not even once it would pass; obor resume retries it (T-340's default for a failed step), and the
// needs-you ends.
func TestLoopAfterMergeFailure(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	heal := r.armScratch("AfterMerge")
	r.start()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.waitPrefix("1 fact needs-you{after_merge failed: git I/O: index.lock exists")
	r.waitFor("the needs-you in force", func() bool { return len(r.app.Status().Project.Facts) == 1 })
	r.later(time.Minute)
	heal()
	r.later(time.Minute)
	if n := r.scratch.AfterMerges.Load(); n != 3 || r.has("1 result after_merge") || r.count("1 intent after_merge") != 1 {
		t.Fatalf("after %d tries: want 3, no result, one intent: %q", n, r.steps())
	}
	r.mustResume("r1", "", "RESUMED T-1: retry (after_merge failed)")
	r.merged(1)
	if r.count("1 result after_merge") != 1 || r.count("1 intent after_merge") != 1 ||
		r.countPrefix("1 fact needs-you{after_merge") != 1 {
		t.Errorf("want one after_merge intent and result, one needs-you: %q", r.steps())
	}
	r.waitFor("the needs-you to end", func() bool { return len(r.app.Status().Project.Facts) == 0 })
}

// T-973 (RV-134 verify #1): a conflict resolved before any accept is not the task's review: the resolve job carries
// only the conflict, so its resolved candidate is gated, tested once in the lane, and then reviewed before it
// publishes. A red lane test sends back; a moved conflict keeps its earlier accept (row 20).

// resolve is the integrator's resolution of T-1's first-gate conflict (r.conflicting) committed in the scratch, and
// its resolved verdict: the resolution's sha.
func resolve(t *testing.T, r *loopRig, callID string) string {
	t.Helper()
	r.waitJob("pj-int", "T-1/1/resolve")
	sha := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, callID, "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	return sha
}

// A first-gate conflict's resolved candidate is tested in the lane and then reviewed once (T-973): the review job
// carries the stored spec, and its accept publishes the resolution.
func TestLoopResolvedFirstConflictReviewsThenMerges(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.conflicting()
	r.start()
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	r.submit()
	resolution := resolve(t, r, "c1")
	job := r.waitJob("pj-int", "T-1/1/review")
	if !strings.Contains(job, "Do the thing.") {
		t.Errorf("the review job carries no rev 1 spec:\n%s", job)
	}
	for _, s := range []string{"1 result gate.2", "1 intent test", "1 result test"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish", "1 fact merged"} {
		if r.has(s) {
			t.Errorf("the resolution published before its review (%q): %q", s, r.steps())
		}
	}
	r.accept("c2", r.patch(job))
	r.merged(1)
	if n := r.count("1 intent review"); n != 1 || r.mainMerges() != 1 || r.published().SHA != resolution {
		t.Errorf("review jobs %d, merges %d; want one review and one merge of the resolution: %q", n, r.mainMerges(), r.steps())
	}
	r.noLead()
}

// The resolved candidate's lane test is red: the task goes back to its worker with the red line, as any red test; no
// review job and no publish.
func TestLoopResolvedFirstConflictRedSendsBack(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.conflicting()
	r.scratch.Script("Test", "T-1", port.OutcomeRed) // the first lane test is the resolution's
	r.start()
	r.submit()
	resolve(t, r, "c1")
	job := r.waitJob("pj-l-01", "T-1/2/fix")
	if !strings.Contains(job, "RED check") {
		t.Errorf("the fix job names no red lane test:\n%s", job)
	}
	for _, s := range []string{"1 result gate.2", "1 intent test", "1 result test", "1 fact sendback"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish", "1 fact merged", "1 intent review"} {
		if r.has(s) {
			t.Errorf("a red lane test after the resolution ran %q: %q", s, r.steps())
		}
	}
	if n, p := r.mainMerges(), r.scratch.Publishes.Load(); n != 0 || p != 0 {
		t.Errorf("merges %d, publishes %d; want none", n, p)
	}
	r.noLead()
}

// The lead sets a new spec rev after the resolution: the review job that follows the resolved candidate's lane test
// carries that rev, and its accept publishes the task. A low disk holds the resolved gate off the scratch until the rev
// is set, so the case is not a race.
func TestLoopResolvedFirstConflictNewSpecRevReviews(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	var free atomic.Uint64
	free.Store(50 << 30)
	r.disk = &app.Disk{Volume: "/vol/pj", Free: func() (uint64, error) { return free.Load(), nil }}
	r.conflicting()
	r.start()
	r.submit()
	r.waitJob("pj-int", "T-1/1/resolve")
	resolution := r.resolveInScratch()
	free.Store(1 << 30) // the resolved gate would only fail on a full disk: it does not start
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	if n := r.scratch.Gates.Load(); n != 1 {
		t.Fatalf("precondition: %d gates before the spec rev, want the conflict's one", n)
	}
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	free.Store(50 << 30)
	job := r.waitJob("pj-int", "T-1/1/review")
	if !strings.Contains(job, "Do the thing.") {
		t.Errorf("the review job carries no rev 1 text:\n%s", job)
	}
	for _, s := range []string{"1 result gate.2", "1 intent test", "1 result test"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish", "1 fact merged"} {
		if r.has(s) {
			t.Errorf("the resolution published before its review (%q): %q", s, r.steps())
		}
	}
	r.accept("c2", r.patch(job))
	r.merged(1)
	if got := r.published().SHA; got != resolution || r.mainMerges() != 1 {
		t.Errorf("published %s, merges %d; want the resolution %s merged once", got, r.mainMerges(), resolution)
	}
	r.noLead()
}

// A conflict after an accepted move is resolved by a rev change's review, as before: the accept was of rev 0, the lead
// set rev 1, so the resolved gate ends the accept and the review job (review.2) comes before the publish.
func TestLoopMovedConflictNewSpecRevReviews(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	first := r.waitJob("pj-int", "T-1/1/review")
	r.movedConflict() // main edits the lane's line: the moved merge conflicts
	r.accept("c1", r.patch(first))
	job := r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	if !strings.Contains(job, "CONFLICT in "+scratch) {
		t.Fatalf("the resolve job does not name the moved gate's conflict:\n%s", job)
	}
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q", reply)
	}
	resolution := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, "c2", "", ""); err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	second := r.waitJob("pj-int", "T-1/1/review.2")
	if r.patch(second) != r.patch(first) {
		t.Errorf("review.2's patch %s, want the task's own %s", r.patch(second), r.patch(first))
	}
	for _, s := range []string{"1 fact accept.kept", "1 result publish.2", "1 fact merged"} {
		if r.has(s) {
			t.Errorf("the resolution of a rev-changed accept published (%q): %q", s, r.steps())
		}
	}
	r.accept("c3", r.patch(second))
	r.merged(1)
	if got := r.published().SHA; got != resolution || r.mainMerges() != 1 {
		t.Errorf("published %s, merges %d; want the resolution %s merged once", got, r.mainMerges(), resolution)
	}
}

// The sample integration (T-1039, docs/agents/integrations.md): a tracker, a process that runs as the custom role
// tracker (OBOR_ROLE=tracker, roles.tracker.verbs [spec_set, dispatch]), takes a ticket of a fake tracker into obor
// and marks it done once its task merged. It is what an integration script does with obor spec set, obor dispatch and
// obor events --follow --after <cursor> --json, through the same wire calls those commands make.

// ticket is one ticket of the fake tracker.
type ticket struct {
	ID, Title, Task string
	Done            bool
}

// tracker is the sample integration: its identity (the derived name and role a CLI sends), its fake tracker's
// tickets and its own cursor, the seq of the last feed record it handled.
type tracker struct {
	sock, root string
	mu         sync.Mutex
	tickets    map[string]*ticket // by Ref
	cursor     int64
	handled    map[int64]int // feed seq → how often handled (once per cursor)
}

func (tr *tracker) call(c wire.Req) (string, error) {
	*c.Env() = wire.Envelope{Root: tr.root, Agent: "pj-tracker", Role: "tracker", CallID: wire.NewCallID()}
	return wire.Verdict(context.Background(), tr.sock, c)
}

// take is a ticket into obor: its spec (with a Ref: line naming the ticket) and its dispatch.
func (tr *tracker) take(tk *ticket) error {
	spec := fmt.Sprintf("# %s — %s\n\nDifficulty: medium\nRef: %s\n", tk.Task, tk.Title, tk.ID)
	if _, err := tr.call(&wire.SpecSetReq{Task: tk.Task, Spec: spec}); err != nil {
		return err
	}
	tr.mu.Lock()
	tr.tickets[tk.ID] = tk
	tr.mu.Unlock()
	_, err := tr.call(&wire.DispatchReq{Task: tk.Task, Lane: "L-01", Types: []string{"sonnet"}})
	return err
}

// follow reads the feed from its cursor until ctx ends, asking again from the cursor when the feed ends under it (a
// serve restart): a task's merged fact marks its ticket (by the record's Ref) done.
func (tr *tracker) follow(ctx context.Context) {
	for ctx.Err() == nil {
		tr.mu.Lock()
		from := tr.cursor
		tr.mu.Unlock()
		_, err := wire.Feed(ctx, tr.sock, tr.root, from, true, func(f wire.FeedRecord) error {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.handled[f.Seq]++
			if tk := tr.tickets[f.Ref]; tk != nil && f.Kind == event.KindFact && f.Step == event.StepMerged && f.Task == tk.Task {
				tk.Done = true
			}
			tr.cursor = f.Seq
			return nil
		})
		if err != nil && !wire.FeedAgain(err) && !errors.Is(err, context.Canceled) {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// T-1039 e2e on fakes (no agents, no git): the tracker, a custom role with verbs spec_set and dispatch, takes
// TICKET-1 (spec_set with Ref: TICKET-1, then dispatch), follows the feed from its cursor, and on T-1's merged record
// (carrying ref TICKET-1) marks the ticket done; it handled each record once. Without a verb serve refuses it.
func TestSampleIntegrationTicketToMerged(t *testing.T) {
	t.Parallel()
	r := verbsRig(t)
	tr := &tracker{sock: r.sock, root: r.p.Root, tickets: map[string]*ticket{}, handled: map[int64]int{}}
	start := feed(t, r, 0) // the tracker's cursor: where it starts reading, the log's end now
	if len(start) > 0 {
		tr.cursor = start[len(start)-1].Seq
	}
	// Precondition: the tracker's verbs are spec_set and dispatch only: watch is refused.
	if _, err := tr.call(&wire.WatchReq{Task: "T-1"}); err == nil {
		t.Fatal("precondition: the tracker's watch was admitted, want it refused (no verb)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	followed := make(chan struct{})
	go func() { tr.follow(ctx); close(followed) }()

	tk := &ticket{ID: "TICKET-1", Title: "Fix the widget", Task: "T-1"}
	if err := tr.take(tk); err != nil {
		t.Fatalf("take TICKET-1: %v", err)
	}
	if spec, ok, err := store.ReadSpecAt(r.p.Root, "T-1"); err != nil || !ok || spec.SetBy != "pj-tracker" {
		t.Errorf("T-1's spec %+v, %v; want set by pj-tracker", spec, ok)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	tr.mu.Lock()
	done := tk.Done
	tr.mu.Unlock()
	if done {
		t.Fatal("TICKET-1 done before T-1 merged")
	}
	r.runToMerge("T-1", nil)
	merged, _ := r.record("T-1", event.KindFact, event.StepMerged)
	r.waitFor("TICKET-1 marked done", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tk.Done && tr.cursor >= merged.Seq
	})
	cancel()
	<-followed
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for seq, n := range tr.handled {
		if n != 1 || seq <= seqOrZero(start) {
			t.Errorf("record %d handled %d times (cursor start %d); want each record after the cursor once", seq, n, seqOrZero(start))
		}
	}
	if tr.handled[merged.Seq] != 1 {
		t.Errorf("the merged record %d was not handled once: %d", merged.Seq, tr.handled[merged.Seq])
	}
	// A restart of the tracker from its cursor repeats nothing: the feed after it has no merged record of T-1.
	for _, f := range feed(t, r, tr.cursor) {
		if f.Task == "T-1" && f.Step == event.StepMerged {
			t.Errorf("the feed after the cursor %d repeats T-1's merge at %d", tr.cursor, f.Seq)
		}
	}
}

func seqOrZero(fs []wire.FeedRecord) int64 {
	if len(fs) == 0 {
		return 0
	}
	return fs[len(fs)-1].Seq
}
