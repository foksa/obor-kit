package app_test

import (
	"encoding/json"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"

	"obor/internal/store"
)

// T-773: obor dispatch T-xx --model <ref> on a task its worker works on moves it: serve keeps the lane, branch and
// spec and records the redo dispatch of the next attempt with the new model; the merge loop stops the worker by its
// identity (worker.stop, T-852: stopstep_test.go), then the redo's agent.start launches that model with the new attempt's work job. The rig: newLoopRig(t, working()) (T-1 working on
// L-01 as sonnet, its worker pj-l-01 in w1:p1).

// moveTo is the lead's obor dispatch T-1 --model types.
func (r *loopRig) moveTo(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Types: types})
}

// workerLaunches is the Agents.Launch calls of pj-l-01, in order.
func (r *loopRig) workerLaunches() []port.Launch {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []port.Launch
	for _, l := range r.launches {
		if l.Agent == "pj-l-01" {
			out = append(out, l)
		}
	}
	return out
}

// A working task moved to opus: MOVING with both models, the worker stopped at its pane, the
// lane prepared again on the same branch as a redo, the new attempt's agent.start launching opus fresh with job
// T-1/2/work, which the new agent fetches. The lane row names opus. A repeated call id gets the stored reply.
func TestMoveWorkingTaskToAnotherModel(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, working())
	if ls := r.workerLaunches(); len(ls) != 1 || ls[0].Type != "sonnet" || r.task("T-1").Attempt != 1 {
		t.Fatalf("precondition: one sonnet start of attempt 1: %+v, attempt %d", ls, r.task("T-1").Attempt)
	}
	reply, err := r.moveTo("m1", "opus")
	if want := "MOVING T-1 on L-01: sonnet → opus (stopping pj-l-01)"; err != nil || reply != want {
		t.Fatalf("move: %q, %v; want %q", reply, err, want)
	}
	r.waitJob("pj-l-01", "T-1/2/work")
	if !slices.Contains(r.workerStops(), "pj-l-01 w1:p1") {
		t.Errorf("the worker was not stopped at its pane: %q", r.workerStops())
	}
	r.waitFor("T-1 working on attempt 2", func() bool {
		ts := r.task("T-1")
		return ts.State == "working" && ts.Attempt == 2
	})
	r.disp.mu.Lock()
	inputs := append([]port.PrepareInput(nil), r.disp.inputs...)
	r.disp.mu.Unlock()
	if len(inputs) != 2 || !inputs[1].Redo || inputs[1].Lane != "L-01" || inputs[1].Branch != inputs[0].Branch ||
		inputs[1].Target != inputs[0].Target {
		t.Errorf("the prepares %+v; want the move's a redo on L-01, its branch and target kept", inputs)
	}
	ls := r.workerLaunches()
	if len(ls) != 2 || ls[1].Type != "opus" || !ls[1].Fresh || ls[1].Prompt != "[obor job T-1/2/work] call the obor job_next tool" {
		t.Errorf("the worker's launches %+v; want the move's fresh opus start with T-1/2/work's wake", ls)
	}
	if row, _, _ := store.ReadLaneAt(r.p.Root, "L-01"); row.Task != "T-1" || row.Type != "opus" {
		t.Errorf("L-01's row %+v after the move; want T-1 as opus", row)
	}
	// The loop records on its own meanwhile (the stop's lead line, its delivery: T-852), so the repeat is checked by
	// what it alone could record: a dispatch fact.
	n := r.dispatchFacts()
	if again, err := r.moveTo("m1", "opus"); err != nil || again != reply || r.dispatchFacts() != n {
		t.Errorf("the same call id again: %q, %v (%d dispatch facts, was %d); want the stored reply, nothing recorded",
			again, err, r.dispatchFacts(), n)
	}
}

// dispatchFacts is how many dispatch facts the log has (a move's, any attempt).
func (r *loopRig) dispatchFacts() int {
	n := 0
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			n++
		}
	}
	return n
}

// A move to the model the task runs is refused ("already on"); nothing is recorded or stopped.
func TestMoveToTheSameModelRefused(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, working())
	n := len(r.records())
	_, err := r.moveTo("m1", "sonnet")
	if err == nil || err.Error() != "dispatch T-1: T-1 is already on sonnet (lane L-01)" {
		t.Fatalf("move to sonnet: %v; want refused: already on sonnet", err)
	}
	if len(r.records()) != n || len(r.workerStops()) != 0 {
		t.Errorf("a refused move recorded %q or stopped %q", r.steps()[n:], r.workerStops())
	}
}

// Serve restarted in the middle of a move (the move recorded, the worker stopped, the new agent's start cut before it
// ran) replays the move: no second stop (T-852), the start runs again, with the new model and the new attempt's job.
func TestMoveReplayedToTheNewModel(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, working())
	r.disp.set(func(d *dispatchFakes) { d.cut["start"] = "before" })
	if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVING T-1 on L-01: sonnet → opus") {
		t.Fatalf("move: %q, %v", reply, err)
	}
	r.waitFor("the move's start cut", func() bool { r.disp.mu.Lock(); defer r.disp.mu.Unlock(); return r.disp.hit["start"] })
	r.stop()
	if !r.has("2 result worker.stop") || !r.has("2 intent agent.start") || r.has("2 result agent.start") {
		t.Fatalf("precondition: the worker stopped, the move's start pending: %q", r.steps())
	}
	stops := r.workerStops()
	before := len(r.workerLaunches())
	r.disp.set(func(d *dispatchFakes) { delete(d.cut, "start") })
	r.start()
	r.waitJob("pj-l-01", "T-1/2/work")
	ls := r.workerLaunches()[before:]
	if len(ls) != 1 || ls[0].Type != "opus" || ls[0].Prompt != "[obor job T-1/2/work] call the obor job_next tool" {
		t.Errorf("the replay's launches %+v; want one opus start with T-1/2/work's wake", ls)
	}
	if ts := r.task("T-1"); ts.Attempt != 2 {
		t.Errorf("T-1's attempt %d after the replay, want 2", ts.Attempt)
	}
	if got := r.workerStops(); !slices.Equal(got, stops) || len(got) != 1 || r.count("2 intent worker.stop") != 1 {
		t.Errorf("stops %q after the replay (before %q), %d stop intents; want the one stop, no second",
			got, stops, r.count("2 intent worker.stop"))
	}
}

// T-778 (RV-98 #1): a move of a sent-back attempt (working again after the integrator's sendback or a red gate, so after
// a submission) redoes the task on the dispatch's branch and target and carries the outstanding fix into the moved
// attempt's work job; serve restarted before the move (the log replayed past the submission) and after it gives the
// same.
func TestMoveAfterSendbackKeepsTheDispatch(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		red  bool
	}{{"integrator sendback", false}, {"red gate", true}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, dispatching())
			r.work("feature.txt", "feature\n")
			if c.red {
				r.scratch.Script("Test", "T-1", port.OutcomeRed)
			}
			r.start()
			r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
			r.mustDispatch("d1")
			r.waitJob("pj-l-01", "T-1/1/work")
			r.submit()
			fix := "add a test"
			if c.red {
				r.waitStep("1 fact sendback")
				fix = "RED check"
			} else {
				r.waitJob("pj-int", "T-1/1/review")
				if reply, err := r.verdict(wire.MethodSendback, "v1", "", fix); err != nil ||
					!strings.HasPrefix(reply, "SENDBACK T-1") {
					t.Fatalf("sendback: %q, %v", reply, err)
				}
				r.integratorStops()
			}
			if job := r.waitJob("pj-l-01", "T-1/2/fix"); !strings.Contains(job, fix) {
				t.Fatalf("precondition: the fix job names %q:\n%s", fix, job)
			}
			if ts := r.task("T-1"); ts.State != "working" || ts.Attempt != 2 {
				t.Fatalf("precondition: T-1 working on attempt 2: %+v", ts)
			}
			r.stop() // the replay folds the submission again: the dispatch it keeps comes from the log
			r.start()
			r.connectedAgain()
			reply, err := r.moveTo("m1", "opus")
			if want := "MOVING T-1 on L-01: sonnet → opus (stopping pj-l-01)"; err != nil || reply != want {
				t.Fatalf("move: %q, %v; want %q (the old model read from the kept dispatch)", reply, err, want)
			}
			job := r.waitJob("pj-l-01", "T-1/3/work")
			r.disp.mu.Lock()
			inputs := append([]port.PrepareInput(nil), r.disp.inputs...)
			r.disp.mu.Unlock()
			if n := len(inputs); n < 2 || inputs[0].Branch == "" || inputs[0].Target == "" || !inputs[n-1].Redo ||
				inputs[n-1].Branch != inputs[0].Branch || inputs[n-1].Target != inputs[0].Target {
				t.Errorf("the prepares %+v; want the move's a redo on the dispatch's branch and target", inputs)
			}
			wantFix := "T-1 moved to you (attempt 3) while it was sent back to the previous worker: "
			if !strings.Contains(job, wantFix) || !strings.Contains(job, fix) {
				t.Errorf("the moved attempt's work job lacks the fix (%q, %q):\n%s", wantFix, fix, job)
			}
			var d map[string]any
			for _, rec := range r.records() {
				if rec.Task == "T-1" && rec.Attempt == 3 && rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
					_ = json.Unmarshal(rec.Data, &d)
				}
			}
			if d["branch"] != inputs[0].Branch || d["target"] != inputs[0].Target ||
				!strings.Contains(fmt.Sprint(d["fix"]), fix) {
				t.Errorf("the move's dispatch fact %v; want the branch, target and fix", d)
			}
			r.stop()
			r.start()
			if again := r.workerNext("n2"); again.Job != "T-1/3/work" || !strings.Contains(again.Text, wantFix) ||
				!strings.Contains(again.Text, fix) {
				t.Errorf("after a replay of the move the work job is %+v; want the same fix", again)
			}
			if ts := r.task("T-1"); ts.Attempt != 3 || ts.Lane != "L-01" {
				t.Errorf("after the replay T-1 is %+v; want attempt 3 on L-01", ts)
			}
		})
	}
}
