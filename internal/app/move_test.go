package app_test

import (
	"context"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"

	"obor/internal/store"
)

// T-773: obor dispatch T-xx --model <ref> on a task its worker works on moves it: serve stops the worker by its
// identity, keeps the lane, branch and spec, and records the redo dispatch of the next attempt with the new model,
// whose agent.start launches that model with the new attempt's work job. The rig: newTakeOverRig (T-1 working on
// L-01 as sonnet, its worker pj-l-01 in w1:p1).

// moveTo is the lead's obor dispatch T-1 --model types.
func (r *loopRig) moveTo(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID, Task: "T-1",
		Types: types})
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

// A working task moved to opus: the worker stopped at its pane, MOVED with both models and the next attempt, the
// lane prepared again on the same branch as a redo, the new attempt's agent.start launching opus fresh with job
// T-1/2/work, which the new agent fetches. The lane row names opus. A repeated call id gets the stored reply.
func TestMoveWorkingTaskToAnotherModel(t *testing.T) {
	t.Parallel()
	r := newTakeOverRig(t)
	if ls := r.workerLaunches(); len(ls) != 1 || ls[0].Type != "sonnet" || r.task("T-1").Attempt != 1 {
		t.Fatalf("precondition: one sonnet start of attempt 1: %+v, attempt %d", ls, r.task("T-1").Attempt)
	}
	reply, err := r.moveTo("m1", "opus")
	if want := "MOVED T-1 on L-01: sonnet → opus (attempt 2)"; err != nil || reply != want {
		t.Fatalf("move: %q, %v; want %q", reply, err, want)
	}
	if !slices.Contains(r.workerStops(), "pj-l-01 w1:p1") {
		t.Errorf("the worker was not stopped at its pane: %q", r.workerStops())
	}
	r.waitJob("pj-l-01", "T-1/2/work")
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
	n := len(r.records())
	if again, err := r.moveTo("m1", "opus"); err != nil || again != reply || len(r.records()) != n {
		t.Errorf("the same call id again: %q, %v (%d records, was %d); want the stored reply, nothing recorded", again,
			err, len(r.records()), n)
	}
}

// A move to the model the task runs is refused ("already on"); nothing is recorded or stopped.
func TestMoveToTheSameModelRefused(t *testing.T) {
	t.Parallel()
	r := newTakeOverRig(t)
	n := len(r.records())
	_, err := r.moveTo("m1", "sonnet")
	if err == nil || err.Error() != "dispatch T-1: T-1 is already on sonnet (lane L-01)" {
		t.Fatalf("move to sonnet: %v; want refused: already on sonnet", err)
	}
	if len(r.records()) != n || len(r.workerStops()) != 0 {
		t.Errorf("a refused move recorded %q or stopped %q", r.steps()[n:], r.workerStops())
	}
}

// A task gating, under review or merging is not moved: refused by its state as obor drive refuses, nothing recorded or
// stopped.
func TestMoveWhileGatingRefused(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
	r.mustDispatch("d1")
	r.waitJob("pj-l-01", "T-1/1/work")
	release := make(chan struct{})
	r.scratch.arm("Gate", func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	defer close(release)
	r.submit()
	r.waitStep("1 intent gate")
	n := len(r.records())
	_, err := r.moveTo("m1", "opus")
	if err == nil || !strings.Contains(err.Error(), "not one gating, merging or under review") {
		t.Fatalf("move while gating: %v, want refused", err)
	}
	if len(r.records()) != n || r.count("2 fact dispatch") != 0 {
		t.Errorf("a refused move recorded: %q", r.steps()[n:])
	}
	if stops := r.workerStops(); len(stops) != 0 {
		t.Errorf("a refused move stopped %q", stops)
	}
}

// Serve restarted in the middle of a move (the move recorded, the new agent's start cut before it ran) replays the
// move: the start runs again, with the new model and the new attempt's job.
func TestMoveReplayedToTheNewModel(t *testing.T) {
	t.Parallel()
	r := newTakeOverRig(t)
	r.disp.set(func(d *dispatchFakes) { d.cut["start"] = "before" })
	if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVED T-1 on L-01: sonnet → opus") {
		t.Fatalf("move: %q, %v", reply, err)
	}
	r.waitFor("the move's start cut", func() bool { r.disp.mu.Lock(); defer r.disp.mu.Unlock(); return r.disp.hit["start"] })
	r.stop()
	if !r.has("2 intent agent.start") || r.has("2 result agent.start") {
		t.Fatalf("precondition: the move's start pending: %q", r.steps())
	}
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
}
