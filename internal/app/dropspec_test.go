package app_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-1016: obor drop of a task with a stored spec and no loop record records the drop at the spec's rev (its why with
// it) and answers DROPPED; a dispatch of the task is then refused naming the drop, and a new obor spec set (a later
// rev) revives it. A task with neither a spec nor a loop record is refused as before.
func TestDropOfASpecOnlyTaskThenDispatchUntilANewRev(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	if got := r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n"); got != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q, want SET T-1 rev 1", got)
	}
	drop := func(callID, task string) (string, error) {
		return wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: callID, Task: task, Note: "superseded on 11-07"})
	}
	if got, err := drop("x1", "T-9"); err == nil || !strings.Contains(err.Error(), "T-9 is not open in obor serve") {
		t.Errorf("drop of an unknown task: %q, %v; want refused as before", got, err)
	}
	if got, err := drop("x2", "T-1"); err != nil || got != "DROPPED T-1" {
		t.Fatalf("drop of a spec-only task: %q, %v; want DROPPED T-1", got, err)
	}
	if d := r.factData("T-1", event.StepDropped); d["rev"] != float64(1) || d["reason"] != "superseded on 11-07" {
		t.Errorf("the dropped fact %+v, want rev 1 and its why", d)
	}
	if _, err := r.dispatch("d1", "L-01"); err == nil ||
		!strings.Contains(err.Error(), "T-1's spec rev 1 was dropped (superseded on 11-07): obor spec set T-1") {
		t.Errorf("dispatch of the dropped spec: %v; want refused naming the drop", err)
	}
	// A new rev revives it: the same request dispatches.
	if got := r.setSpec("s2", "# T-1 — Stored, widened\n\nDifficulty: medium\n"); got != "SET T-1 rev 2" {
		t.Fatalf("spec set: %q, want SET T-1 rev 2", got)
	}
	if got, err := r.dispatch("d2", "L-01"); err != nil || !strings.HasPrefix(got, "DISPATCHED T-1 → L-01 (queued)") {
		t.Errorf("dispatch after rev 2: %q, %v; want dispatched", got, err)
	}
}
