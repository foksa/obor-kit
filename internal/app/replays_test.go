// replays_test.go: agent.start's replay of a pending intent whose recorded pane is gone (T-783, rev 2): the start
// chooses the pane again through paneIn — a new labelled pane in the lane dir is where the agent starts — and no
// launch names the dead id.
package app

import (
	"encoding/json"
	"strings"
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
// labelled one, at its shell): the replay starts the agent once in the pane chosen again, records it in the start's
// result, and is no needs-you (RV-101 #1: a pane at its shell is still a launch).
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
	if got := known(h, "w1:p3"); got.Label != "pj-l-01" || got.Agent != "" {
		t.Fatalf("precondition: the new pane is %+v, want at its shell, labelled pj-l-01", got)
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

// RV-101 #1: the same replay when the pane chosen again already runs an agent — the start that ran there before a
// crash (or a launch error after its side effect), its result never recorded: the intent is still the gone pane's, so
// nothing tells that agent from another one. cannot tell, never a launch that would stop a worker at its job; the
// needs-you names the gone pane, the pane chosen again and what runs there.
func TestStartReplayIntoAPaneRunningAnAgentCannotTell(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) {
		h.snap.Panes = []port.Pane{{ID: "w1:p3", Workspace: "w1", Cwd: h.lane("L-01"), Label: "pj-l-01",
			Agent: "claude", Session: "new-session", Status: "working"}}
	})
	pendingStart(t, h, "w1:p1")
	if got := known(h, "w1:p1"); got.ID != "" {
		t.Fatalf("precondition: w1:p1 is in the snapshot: %+v", got)
	}
	if got := known(h, "w1:p3"); got.Agent != "claude" || got.Label != "pj-l-01" {
		t.Fatalf("precondition: w1:p3 is %+v, want claude labelled pj-l-01", got)
	}
	h.step(t, fx)
	if ag.restarts != 0 {
		t.Errorf("launched %d time(s) in %q, want none", ag.restarts, ag.pane)
	}
	var out startOut
	if err := json.Unmarshal([]byte(h.result(event.StepStartWork)), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cannot tell", "w1:p1", "w1:p3", "claude", "pj-l-01", "obor resume T-1"} {
		if !strings.Contains(out.Refused, want) {
			t.Errorf("start result refused %q, want it to name %q", out.Refused, want)
		}
	}
	h.step(t, fx)
	if !h.has("needs-you{start: cannot tell") {
		t.Errorf("no needs-you{start: cannot tell …}: %v", h.facts(0))
	}
	if ag.restarts != 0 {
		t.Errorf("launches after the needs-you: %d, want 0", ag.restarts)
	}
}
