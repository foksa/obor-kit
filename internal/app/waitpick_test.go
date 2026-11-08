package app_test

import (
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-1030: the next free lane goes to the first waiting dispatch by priority, then dispatch order. T-1 and T-3 both
// wait after T-9; T-3, dispatched second but high, takes L-01 once T-9 merged, and T-1 keeps waiting.
func TestHighWaitingTaskTakesTheNextFreeLane(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.specOf("T-9", "# T-9 — Dependency\n\nDifficulty: medium\n")
	r.specOf("T-1", "# T-1 — Ordinary\n\nDifficulty: medium\n")
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	for _, task := range []string{"T-1", "T-3"} {
		reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
			Role: "lead", CallID: "d-" + task}, Task: task, After: []string{"T-9"}})
		if err != nil || !strings.HasPrefix(reply, "WAITING "+task+" after T-9 (queued)") {
			t.Fatalf("dispatch of %s after T-9: %q, %v; want WAITING after T-9", task, reply, err)
		}
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.High {
		t.Fatalf("precondition: T-1's status %+v, want waiting, not high", st)
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" || !st.High {
		t.Fatalf("precondition: T-3's status %+v, want waiting, high", st)
	}
	r.scratch.Land("T-9") // obor merge by hand: both waits are ready on the next pass
	r.waitFor("T-3's dispatch", func() bool { return r.taskStep("T-3", "fact dispatch") == 1 })
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("T-1, the ordinary waiting task, took the lane: %q", r.steps())
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Errorf("T-1's status %+v, want waiting for the next free lane", st)
	}
	if d := r.factData("T-3", event.StepDispatched); d["lane"] != "L-01" {
		t.Errorf("T-3's dispatch %v, want L-01", d)
	}
}

// T-1030: --after still holds. A high task whose dependency has not merged waits as before; the ordinary task that is
// ready takes the free lane.
func TestHighWaitingTaskWithAnOpenAfterStillWaits(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.specOf("T-9", "# T-9 — Dependency\n\nDifficulty: medium\n")
	r.specOf("T-1", "# T-1 — Ordinary\n\nDifficulty: medium\n")
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-3"}, Task: "T-3", After: []string{"T-9"}}); err != nil || !strings.HasPrefix(reply,
		"WAITING T-3 after T-9") {
		t.Fatalf("dispatch of the high task after T-9: %q, %v; want WAITING", reply, err)
	}
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-1"}, Task: "T-1"}); err != nil || !strings.HasPrefix(reply,
		"DISPATCHED T-1 → L-01") {
		t.Fatalf("dispatch of the ready ordinary task: %q, %v; want DISPATCHED onto L-01", reply, err)
	}
	r.passes()
	if n := r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Errorf("the high task dispatched before its --after merged: %q", r.steps())
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" {
		t.Errorf("T-3's status %+v, want waiting after T-9", st)
	}
}

// T-1030: a high task whose model is paused does not hold the next task's start: the ordinary one takes the free lane
// on its own model.
func TestHighWaitingTaskOnAPausedModelDoesNotBlockTheNext(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	if _, err := r.pause("lead", "p1", wire.PauseReq{Model: "opus", Why: "quota"}); err != nil {
		t.Fatal(err)
	}
	r.specOf("T-9", "# T-9 — Dependency\n\nDifficulty: medium\n")
	r.specOf("T-1", "# T-1 — Ordinary\n\nDifficulty: medium\n")
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-1"}, Task: "T-1", After: []string{"T-9"}}); err != nil || !strings.HasPrefix(reply,
		"WAITING T-1 after T-9") {
		t.Fatalf("dispatch of T-1 after T-9: %q, %v; want WAITING", reply, err)
	}
	// --model opus: T-3's one model, paused: no other of its level is tried.
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-3"}, Task: "T-3", Types: []string{"opus"}, After: []string{"T-9"}}); err != nil ||
		!strings.HasPrefix(reply, "WAITING T-3 after T-9") {
		t.Fatalf("dispatch of T-3 --model opus after T-9: %q, %v; want WAITING", reply, err)
	}
	if st := r.taskStatus("T-3"); !st.High {
		t.Fatalf("precondition: T-3's status %+v, want high", st)
	}
	r.scratch.Land("T-9")
	r.waitFor("T-1's dispatch", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if d := r.factData("T-1", event.StepDispatched); d["type"] != "sonnet" {
		t.Errorf("T-1's dispatch %v, want its own model sonnet", d)
	}
	r.passes()
	if n := r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Errorf("the high task on a paused model dispatched: %q", r.steps())
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" || !strings.Contains(st.Why, "opus paused: quota") {
		t.Errorf("T-3's status %+v, want waiting with the pause reason", st)
	}
}
