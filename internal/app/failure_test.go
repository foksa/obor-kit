package app_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/testkit"
)

// The failure rule (T-340, docs/state-model.md "Crash classes"): an effect gives a result only for an outcome final
// for its step; anything else leaves the step pending, tried again, and its third counted failure is a needs-you
// during which the step is not tried (also after a restart); obor resume's default retries it.

// countPrefix is how many records' lines start with s.
func (r *loopRig) countPrefix(s string) int {
	n := 0
	for _, line := range r.steps() {
		if strings.HasPrefix(line, s) {
			n++
		}
	}
	return n
}

// gitIO is the error an armed Scratch method gives: git could not tell.
func gitIO(context.Context) error { return errors.New("git I/O: index.lock exists") }

// armScratch makes the Scratch's method fail; the heal is returned.
func (r *loopRig) armScratch(method string) func() {
	r.scratch.arm(method, gitIO)
	return func() { r.scratch.arm(method, nil) }
}

// failRow is one error cell of the table: arm makes step fail (before the start) and returns what heals it.
type failRow struct {
	step    string // the step's key
	attempt int    // its attempt (0: 1)
	check   string // commands.check ("": echo 1 pass)
	arm     func(r *loopRig, ans *answers) (heal func())
	pending func(r *loopRig) // more checks while the step is pending
	end     string           // the record that ends the task once the step ran ("": the merge's last step)
}

func (row failRow) run(t *testing.T) {
	check := row.check
	if check == "" {
		check = "echo 1 pass"
	}
	at := fmt.Sprint(max(row.attempt, 1))
	r := newLoopRig(t, check)
	r.work("feature.txt", "feature\n")
	ans := &answers{r: r}
	heal := row.arm(r, ans)
	r.start()
	r.submit()
	base, _, _ := strings.Cut(row.step, ".")
	if strings.HasPrefix(row.step, "start.") || row.step == "agent.stop" {
		base = row.step
	}
	needs := at + " fact needs-you{" + row.step + " failed: "
	r.waitFor(needs, func() bool { ans.pass(); return r.hasPrefix(needs) })
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
		if r.scratch.gates.Load() != 0 || r.scratch.submits.Load() != 3 {
			r.t.Fatalf("want 3 submits and no gate: submits %d, gates %d: %q", r.scratch.submits.Load(),
				r.scratch.gates.Load(), r.steps())
		}
	}},
	"gate (git I/O)": {step: "gate", arm: func(r *loopRig, _ *answers) func() {
		return r.armScratch("Gate")
	}, pending: func(r *loopRig) {
		if r.scratch.gates.Load() != 3 || !r.hasPrefix("1 fact failed{gate: git I/O: index.lock exists") {
			r.t.Fatalf("want 3 gates, each counted: gates %d: %q", r.scratch.gates.Load(), r.steps())
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
	// T-602: no "review (agent_blocked)" row: a review job's first start is a fresh session (its wake the first
	// prompt), never typed into a running integrator; its failure is the start's (the row above), and a typed
	// integrator wake that fails is TestRV37V2ResumeRetryKeepsTheJob's (retry, nudge).
	"fix (agent_blocked)": {step: "fix", attempt: 2, check: `if [ -f red.txt ]; then echo "0 pass, 1 fail"; exit 1; fi; echo 1 pass`,
		arm: func(r *loopRig, _ *answers) func() {
			r.work("red.txt", "red\n")
			r.srv.Fail("agent.prompt", "agent_blocked", "pj-o1 is blocked")
			return func() { r.srv.Reply("agent.prompt", prompted) }
		}, end: "2 result fix"},
}

// Each error cell: pending (one intent, no result), tried again, a needs-you after 3 counted failures, no attempt
// while it is in force (also after a restart: a failed prompt is not settled as sent), obor resume retries the step.
func TestLoopFailureRule(t *testing.T) {
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
	r := newLoopRig(t, "echo 1 pass")
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
	if r.count("1 intent agent.stop") != 1 || r.result("agent.stop") != `"STOPPED pj-o1"` ||
		r.f.Pane("w1:p1").Name != "" {
		t.Errorf("want the stop retried under its one intent: %q", r.steps())
	}
}

// The failure count survives a restart: two failures recorded before it, one more after it is the third: the
// needs-you, not a fresh count.
func TestLoopFailureCountSurvivesRestart(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.submit()
	failed := []byte(`{"lane":"O-01","reason":"gate: git I/O: index.lock exists","merge":true,"step":"gate"}`)
	if err := app.WriteLog(r.p.Root, time.Now(), []app.Record{
		{Task: "T-1", Attempt: 1, Step: "submitted", Kind: app.KindFact, Data: []byte(`{"lane":"O-01","agent":"pj-o1"}`)},
		{Task: "T-1", Attempt: 1, Step: "submit", Kind: app.KindIntent, Class: app.ClassGit},
		{Task: "T-1", Attempt: 1, Step: "submit", Kind: app.KindResult, Data: []byte(`{"sha":"` + r.p.Git(r.lane, "rev-parse", "HEAD") + `"}`)},
		{Task: "T-1", Attempt: 1, Step: "gate", Kind: app.KindIntent, Class: app.ClassGit, Data: []byte(`{"kind":"lane","lane":"O-01"}`)},
		{Task: "T-1", Attempt: 1, Step: "failed", Kind: app.KindFact, Data: failed},
		{Task: "T-1", Attempt: 1, Step: "failed", Kind: app.KindFact, Data: failed},
	}...); err != nil {
		t.Fatal(err)
	}
	defer r.armScratch("Gate")()
	r.start()
	r.waitPrefix("1 fact needs-you{gate failed: git I/O: index.lock exists")
	if n := r.countPrefix("1 fact failed{gate: "); n != 3 || r.scratch.gates.Load() != 1 {
		t.Errorf("want the third failure (the first after the restart) to be the needs-you: %d failures, %d gates: %q", n,
			r.scratch.gates.Load(), r.steps())
	}
}

// An attempt obor serve's stop cuts off, or one that ends while herdr is disconnected, is not counted.
func TestLoopFailureNotCounted(t *testing.T) {
	t.Run("ctx cut", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.work("feature.txt", "feature\n")
		r.scratch.arm("Gate", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }) // the stop cuts it off
		r.start()
		r.submit()
		r.waitFor("the gate", func() bool { return r.scratch.gates.Load() == 1 })
		r.stop()
		r.scratch.arm("Gate", nil)
		if r.hasPrefix("1 fact failed") || r.has("1 result gate") {
			t.Fatalf("a cut-off gate counted or decided: %q", r.steps())
		}
		r.start()
		r.waitJob("pj-int", "T-1/1/review")
		if r.hasPrefix("1 fact failed") || r.count("1 intent gate") != 1 {
			t.Errorf("want the gate run again under its intent, nothing counted: %q", r.steps())
		}
	})
	t.Run("herdr disconnected", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.work("feature.txt", "feature\n")
		release := make(chan struct{})
		r.scratch.arm("Gate", func(context.Context) error { <-release; return errors.New("git I/O") })
		r.start()
		r.submit()
		r.waitFor("the gate", func() bool { return r.scratch.gates.Load() == 1 })
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
	for _, row := range []struct {
		name, step, want string
		edits            []func(map[string]any)
		arm              func(r *loopRig)
	}{
		{name: "after_merge's command fails: a warning, done", step: "after_merge", want: `"WARN after_merge failed: broke"`,
			edits: []func(map[string]any){testkit.Set("commands.after_merge", "echo broke; exit 2")}},
		{name: "agent.stop: not found (the snapshot lags)", step: "agent.stop", want: `"stopped: no agent (`, arm: func(r *loopRig) {
			r.onStop = func() error { return notFound{} }
		}},
		{name: "agent.stop: stop disabled", step: "agent.stop", want: `"kept: stop_idle_workers is false"`,
			edits: []func(map[string]any){testkit.Set("stop_idle_workers", false)}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, "echo 1 pass", row.edits...)
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
		r := newLoopRig(t, `if [ -f red.txt ]; then echo "0 pass, 1 fail"; exit 1; fi; echo 1 pass`)
		r.work("red.txt", "red\n")
		r.setLane(func(l *app.Lane) { l.Agent = "" })
		r.p.State("O-01", map[string]any{"lane": "O-01", "task": "T-1", "branch": "task/T-1", "base": "main"})
		r.submit() // before the start: in the log (no agent could call task_done)
		r.start()
		r.waitStep("2 fact needs-you{fix job T-1/2/fix not sent: the lane names no worker agent}")
		r.passes()
		if r.has("2 intent fix") || len(r.prompts()) != 0 || r.hasPrefix("2 fact failed") {
			t.Errorf("want the needs-you, no prompt: %q", r.steps())
		}
	})
}

// notFound is an adapter's "no such agent" (app.NotFound).
type notFound struct{}

func (notFound) Error() string  { return "agent_not_found: pj-o1" }
func (notFound) NotFound() bool { return true }

// RV-37 verify-2 #7, fixed (its probe TestRV37V2ResumeRepromptAuthorization): a retry or a nudge of the open job
// fails three times, obor resume retries it and it is sent: the job is still the one a verdict answers (job
// authorization is the folded task's open job, not a second record unblocked cleared).
func TestRV37V2ResumeRetryKeepsTheJob(t *testing.T) {
	for _, step := range []string{"retry.review", "nudge.review", "retry.resolve", "nudge.resolve"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, "echo 1 pass")
			job := "T-1/1/review"
			if strings.HasSuffix(step, "resolve") {
				job = "T-1/1/resolve"
				conflictSetup(r)
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
				scratch := r.scratch.dir()
				r.p.Write(filepath.Join(scratch, "shared.txt"), "both\n"+sharedLines)
				r.p.Git(scratch, "add", "shared.txt")
				r.p.Git(scratch, "commit", "-qm", "Merge T-1: resolved")
				if reply, err := r.verdict(app.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job "+job+")" {
					t.Fatalf("resolved after the resumed %s: %q, %v", step, reply, err)
				}
				text = r.waitJob("pj-int", "T-1/1/review")
			} else if reply, err := r.verdict(app.MethodAccept, "c1", r.patch(text), ""); err != nil ||
				reply != "ACCEPT T-1 (job "+job+")" {
				t.Fatalf("accept after the resumed %s: %q, %v", step, reply, err)
			}
			if strings.HasSuffix(step, "resolve") {
				r.accept("c2", r.patch(text))
			}
			r.merged(1)
		})
	}
}

// A reject unpins the task (the unpin step, G) before the owner is told; an unpin that fails is the failure rule's
// (pending, counted, the owner after 3) and obor resume retries it.
func TestLoopRejectUnpins(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("unpin fails ", fail), func(t *testing.T) {
			t.Parallel()
			r, _ := reviewed(t)
			if r.p.Git(r.p.Root, "for-each-ref", "refs/obor/") == "" {
				t.Fatal("precondition: no pins")
			}
			heal := func() {}
			if fail {
				heal = r.armScratch("Unpin")
			}
			if reply, err := r.verdict(app.MethodReject, "c1", "", "not this"); err != nil || reply != "REJECT T-1 (job T-1/1/review)" {
				t.Fatalf("reject: %q, %v", reply, err)
			}
			if fail {
				r.waitPrefix("1 fact needs-you{unpin failed: git I/O")
				if r.countPrefix("1 fact failed{unpin: ") != 3 || r.has("1 result unpin") ||
					r.p.Git(r.p.Root, "for-each-ref", "refs/obor/") == "" {
					t.Fatalf("want unpin pending, counted 3 times, the pins kept: %q", r.steps())
				}
				heal()
				r.waitFor("the needs-you in force", func() bool { return r.facts() == 1 })
				r.mustResume("r1", "", "RESUMED T-1: retry (unpin failed)")
			}
			r.waitStep("1 fact needs-you{rejected by the integrator: not this}")
			if !r.has("1 result unpin") || r.p.Git(r.p.Root, "for-each-ref", "refs/obor/") != "" || r.mainMerges() != 0 {
				t.Errorf("want unpinned, nothing merged: refs %q: %q", r.p.Git(r.p.Root, "for-each-ref", "refs/obor/"), r.steps())
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
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.scratch.arm("Submit", func(context.Context) error { return app.Refused{Line: "FAIL T-1: lane O-01 is not on a branch"} })
	r.start()
	r.submit()
	r.waitStep("1 fact needs-you{submit: FAIL T-1: lane O-01 is not on a branch}")
	if !r.has("1 result submit") || r.hasPrefix("1 fact failed") || r.scratch.gates.Load() != 0 || r.scratch.submits.Load() != 1 {
		t.Fatalf("want one submit with its result, nothing counted, no gate: %q", r.steps())
	}
	r.scratch.arm("Submit", nil)
	r.waitFor("the needs-you in force", func() bool { return r.facts() == 1 })
	r.mustResume("r1", "", "RESUMED T-1: regate (gate)")
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.merged(1)
	if !r.has("1 result submit.2") || r.mainMerges() != 1 {
		t.Errorf("want submit.2, then merged once: %q", r.steps())
	}
}
