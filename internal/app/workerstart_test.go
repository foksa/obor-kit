package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
)

// T-874: the worker start rule. The rig: T-1 dispatched on L-01, its work job delivered to pj-l-01 (w1:p1, labelled).

// workingRig is newTakeOverRig's T-1: working, its work job fetched by pj-l-01.
func workingRig(t *testing.T) *loopRig {
	t.Helper()
	r := newTakeOverRig(t)
	if !slices.Contains(r.deliveries(), "T-1/1/work mcp") {
		t.Fatalf("precondition: the work job delivered: %q", r.deliveries())
	}
	return r
}

// workerExits leaves pj-l-01's labelled pane at its shell (its agent quit: obor stop's stop, or a crash) and waits
// until serve sees it gone.
func (r *loopRig) workerExits() {
	r.t.Helper()
	r.agentGone("w1:p1", "pj-l-01", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
}

// ruleStarts is pj-l-01's starts without --fresh: the worker start rule's (a dispatch's own start is fresh).
func (r *loopRig) ruleStarts() []port.Launch {
	var out []port.Launch
	for _, l := range r.workerLaunches() {
		if !l.Fresh {
			out = append(out, l)
		}
	}
	return out
}

// pastSettle runs a pass that sees the condition, then moves the clock past the rule's settle.
func (r *loopRig) pastSettle() {
	r.passes()
	r.later(app.WorkerStartSettle)
}

// resumes is the rule's starts applied (a failed one is in ruleStarts only).
func (r *loopRig) resumes() int {
	r.disp.mu.Lock()
	defer r.disp.mu.Unlock()
	return r.disp.resumes
}

// startedWith waits for the rule's first start applied, moving the clock past the settle while there is none (a herdr
// resubscribe gap, which the rig's pushes cause, holds no condition for a pass and so starts the settle again), then
// for pj-l-01 up again; that start was in w1:p1 with job's wake.
func (r *loopRig) startedWith(job string) {
	r.t.Helper()
	r.waitFor("the rule's start of pj-l-01", func() bool {
		if r.resumes() > 0 {
			return true
		}
		r.later(app.WorkerStartSettle)
		return false
	})
	starts := r.ruleStarts()
	l := starts[len(starts)-1]
	if want := "[obor job " + job + "] call the obor job_next tool"; l.Pane != "w1:p1" || l.Prompt != want {
		r.t.Errorf("the rule's start %+v; want pane w1:p1, prompt %q", l, want)
	}
	r.waitFor("pj-l-01 up again", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01" {
				return true
			}
		}
		r.push()
		return false
	})
}

// ruleNeedsYou: the log has the worker start rule's needs-you.
func (r *loopRig) ruleNeedsYou() bool {
	return slices.ContainsFunc(r.records(), func(rec event.Record) bool {
		return rec.Task == event.RuleTask && rec.Step == event.StepNeedsYou &&
			strings.Contains(rec.String(), "rule worker.start failed 3 passes in a row")
	})
}

// The lifecycle (RV-119 #1): obor stop --force of a working task (its pause, the worker stopped, serve ends), then
// obor start (serve replays the log, the pause cleared). While obor stop's pause holds nothing starts the worker,
// before serve ends and after it replays; once cleared, the rule starts pj-l-01 (no --fresh: its session resumed)
// with its delivered work job's wake, job_next hands it the same job, and its task_done submits T-1.
func TestWorkerStartAfterStopForce(t *testing.T) {
	t.Parallel()
	r := workingRig(t)
	if reply, err := r.pause("lead", "p1", wire.Call{Note: wire.StopPause}); err != nil || reply != "PAUSED until cleared" {
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
	if reply, err := r.pause("lead", "p2", wire.Call{Clear: true}); err != nil || reply != "PAUSE CLEARED" {
		t.Fatalf("obor start's clear: %q, %v", reply, err)
	}
	r.pastSettle()
	r.startedWith("T-1/1/work")
	if text := r.waitJob("pj-l-01", "T-1/1/work"); !strings.Contains(text, "implement the spec of T-1") {
		t.Errorf("job_next after the start: %q; want the work job", text)
	}
	r.submit()
	r.waitStep("1 fact submitted")
}

// The rule's cases end to end (its condition table: TestWorkerStartRuleCondition): a crash, a failing start, a fix
// job.
func TestWorkerStartRule(t *testing.T) {
	t.Parallel()
	t.Run("a worker that exits while working: started again past the settle, with its job's wake", func(t *testing.T) {
		t.Parallel()
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
	t.Run("its start keeps failing: the rule's needs-you after 3 passes, ended by a start", func(t *testing.T) {
		t.Parallel()
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
	t.Run("a fix job delivered (sendback): started with the fix job's wake", func(t *testing.T) {
		t.Parallel()
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
	t.Run("a sendback while the worker is stopped: started with the fix job's wake, then sent it", func(t *testing.T) {
		t.Parallel()
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
}
