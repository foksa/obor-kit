// replays_test.go: agent.start's replay of a pending intent whose recorded pane is gone (T-783, rev 2): the start
// chooses the pane again through paneIn — a new labelled pane in the lane dir is where the agent starts — and no
// launch names the dead id.
package app

import (
	"encoding/json"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// pendingStart is a pending agent.start intent of T-1 in pane: serve recorded the start and never its result.
func pendingStart(t *testing.T, h *harness, pane string) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepStartWork, event.KindIntent, startData{Lane: "L-01", Agent: "pj-l-01", Pane: pane})
}

// T-783 (rev 2): the pane an agent.start intent recorded is gone (its tab closed, then obor setup pane made a new
// labelled one): the replay starts the agent in the pane chosen again, records it in the start's result, and is no
// needs-you.
func TestStartReplayStartsInThePaneChosenAgain(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) {
		// The snapshot has no w1:p1 at all: that tab was closed. w1:p3 is the new pane, labelled for the lane's agent.
		h.snap.Panes = []port.Pane{{ID: "w1:p3", Workspace: "w1", Cwd: h.lane("L-01"), Label: "pj-l-01"}}
	})
	pendingStart(t, h, "w1:p1")
	if got := known(h, "w1:p1"); got.ID != "" {
		t.Fatalf("precondition: w1:p1 is in the snapshot: %+v", got)
	}
	if got := known(h, "w1:p3").Label; got != "pj-l-01" {
		t.Fatalf("precondition: the new pane's label is %q, want pj-l-01", got)
	}
	if !h.step(t, fx) {
		t.Fatal("the replay did not start in the pane chosen again")
	}
	if ag.pane != "w1:p3" || ag.restarts != 1 {
		t.Errorf("launched %d time(s) in %q, want once in w1:p3", ag.restarts, ag.pane)
	}
	var out startOut
	if err := json.Unmarshal([]byte(h.result(event.StepStartWork)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Pane != "w1:p3" || out.Refused != "" {
		t.Errorf("start result %+v, want the start in w1:p3", out)
	}
	if n := h.count(event.KindFact, event.StepNeedsYou); n != 0 {
		t.Errorf("needs-you facts: %d, want 0", n)
	}
}

// T-783 (rev 2): the same replay with no pane left in the lane takes the usual no-pane wait — no launch, no needs-you
// on the first pass — not a start into the recorded, dead id.
func TestStartReplayWithNoPaneLeftWaits(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) { h.snap.Panes = nil }) // every pane closed
	pendingStart(t, h, "w1:p1")
	if h.step(t, fx) {
		t.Fatal("the replay went on with no pane in the lane")
	}
	if since(h).IsZero() {
		t.Error("the no-pane clock did not start")
	}
	if ag.restarts != 0 {
		t.Errorf("launches with no pane: %d", ag.restarts)
	}
	if n := h.count(event.KindFact, event.StepNeedsYou); n != 0 {
		t.Errorf("needs-you facts on the first pass: %d, want 0", n)
	}
}
