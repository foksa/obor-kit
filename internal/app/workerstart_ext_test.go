package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/app/wire"
)

// T-874: the worker start rule. The rig: T-1 dispatched on L-01, its work job delivered to pj-l-01 (w1:p1, labelled).

// workingRig is the working rig's T-1: working, its work job fetched by pj-l-01.
func workingRig(t *testing.T) *loopRig {
	t.Helper()
	r := newLoopRig(t, working())
	if !slices.Contains(r.deliveries(), "T-1/1/work mcp") {
		t.Fatalf("precondition: the work job delivered: %q", r.deliveries())
	}
	return r
}

// The lifecycle (RV-119 #1): obor stop --force of a working task (its pause, the worker stopped, serve ends), then
// obor start (serve replays the log, the pause cleared). While obor stop's pause holds nothing starts the worker,
// before serve ends and after it replays; once cleared, the rule starts pj-l-01 (no --fresh: its session resumed)
// with its delivered work job's wake, job_next hands it the same job, and its task_done submits T-1.
func TestWorkerStartAfterStopForce(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := workingRig(t)
		if reply, err := r.pause("lead", "p1",
			wire.PauseReq{Why: wire.StopPause}); err != nil || reply != "PAUSED until cleared" {
			t.Fatalf("obor stop's pause: %q, %v", reply, err)
		}
		r.workerExits()
		r.pastSettle()
		r.later(app.WorkerStartSettle)
		if n := len(r.ruleStarts()); n != 0 {
			t.Fatalf("started %d times while obor stop's pause holds (before serve ends): %+v", n, r.ruleStarts())
		}
		r.stop()

		r.start()
		r.pastSettle()
		if n := len(r.ruleStarts()); n != 0 {
			t.Fatalf("started %d times after the replay, the pause not cleared yet: %+v", n, r.ruleStarts())
		}
		if reply, err := r.pause("lead", "p2", wire.PauseReq{Clear: true}); err != nil || reply != "PAUSE CLEARED" {
			t.Fatalf("obor start's clear: %q, %v", reply, err)
		}
		r.pastSettle()
		r.startedWith("T-1/1/work")
		if text := r.waitJob("pj-l-01", "T-1/1/work"); !strings.Contains(text, "implement the spec of T-1") {
			t.Errorf("job_next after the start: %q; want the work job", text)
		}
		r.submit()
		r.waitStep("1 fact submitted")
	})
}

// The rule's cases end to end (its condition table: TestWorkerStartRuleCondition): a crash, a failing start, a fix
// job.
func TestWorkerStartRule(t *testing.T) {
	t.Parallel()
	t.Run("a worker that exits while working: started again past the settle, with its job's wake", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := workingRig(t)
			r.workerExits()
			r.passes()
			if n := len(r.ruleStarts()); n != 0 {
				t.Fatalf("started %d times inside the settle", n)
			}
			r.later(app.WorkerStartSettle)
			r.startedWith("T-1/1/work")
			r.pastSettle()
			if n := r.resumes(); n != 1 {
				t.Errorf("starts %d once it runs again, want 1: %+v", n, r.ruleStarts())
			}
		})
	})
	t.Run("its start keeps failing: the rule's needs-you after 3 passes, ended by a start", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := workingRig(t)
			r.disp.set(func(d *dispatchFakes) { d.fail["start"] = func() error { return errors.New("herdr: pane busy") } })
			r.workerExits()
			r.pastSettle()
			r.waitFor("the rule's needs-you", r.ruleNeedsYou)
			if n := r.disp.called("start") - 1; n < 3 { // the dispatch's own start is one
				t.Errorf("the rule's tries %d before its needs-you, want 3 or more", n)
			}
			r.disp.set(func(d *dispatchFakes) { delete(d.fail, "start") })
			r.startedWith("T-1/1/work")
		})
	})
	t.Run("a fix job delivered (sendback): started with the fix job's wake", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, _ := fetched(t)
			if _, err := r.verdict(wire.MethodSendback, "s1", "", "add a test"); err != nil {
				t.Fatalf("sendback: %v", err)
			}
			r.integratorStops()
			r.waitStep("2 result fix")
			if jr := r.workerNext("f1"); jr.Job != "T-1/2/fix" {
				t.Fatalf("precondition: the fix job fetched: %+v", jr)
			}
			r.workerExits()
			r.pastSettle()
			r.startedWith("T-1/2/fix")
			r.waitJob("pj-l-01", "T-1/2/fix")
		})
	})
	t.Run("a sendback while the worker is stopped: started with the fix job's wake, then sent it", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r, _ := fetched(t)
			r.workerExits()
			if _, err := r.verdict(wire.MethodSendback, "s1", "", "add a test"); err != nil {
				t.Fatalf("sendback: %v", err)
			}
			r.integratorStops()
			r.passes()
			if r.has("2 intent fix") {
				t.Fatalf("precondition: no fix job sent to a worker at its shell: %q", r.steps())
			}
			r.pastSettle()
			r.startedWith("T-1/2/fix")
			r.waitStep("2 result fix")
			r.waitJob("pj-l-01", "T-1/2/fix")
		})
	})
}
