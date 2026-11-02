package app_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app"
)

// T-689: a spec's `Type:` line (under Difficulty) puts its lane types first in the dispatch order; the level's types
// follow. The rig (newLevelRig) has O-01 (deepseek) and O-02 (sonnet); medium is sonnet, deepseek.

func (r *loopRig) specType(types string) {
	r.t.Helper()
	r.setSpec("s-type", "# T-1 — Typed\n\nDifficulty: medium\nType: "+types+"\n")
}

func TestSpecTypePrefersAFreeLaneOfItsType(t *testing.T) {
	r := newLevelRig(t)
	r.start()
	r.specType("deepseek") // medium alone would take O-02 (sonnet) first
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued)") {
		t.Fatalf("Type: deepseek dispatch: %q, %v; want O-01 (deepseek)", reply, err)
	}
	if d := r.factData("T-1", app.StepDispatched); d["level"] != "medium" || d["lane"] != "O-01" {
		t.Errorf("dispatch fact %v, want level medium on O-01", d)
	}
}

func TestSpecTypeFallsBackToTheLevelWhenItsLaneIsBusy(t *testing.T) {
	r := newLevelRig(t)
	r.lanes.mu.Lock()
	r.lanes.lanes[0].State, r.lanes.lanes[0].Task = "working", "T-9" // O-01, the deepseek lane
	r.lanes.mu.Unlock()
	r.start()
	r.specType("deepseek")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-02 (queued)") {
		t.Fatalf("Type: deepseek with its lane busy: %q, %v; want the level's O-02 (sonnet)", reply, err)
	}
}

func TestSpecTypeWaitsForEitherWhenAllAreBusy(t *testing.T) {
	r := newLevelRig(t)
	r.lanes.mu.Lock()
	for i := range r.lanes.lanes {
		r.lanes.lanes[i].State, r.lanes.lanes[i].Task = "working", "T-9"
	}
	r.lanes.mu.Unlock()
	r.start()
	r.specType("deepseek")
	const want = "WAITING T-1 (queued): waiting for a medium lane (deepseek, sonnet)"
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, want) {
		t.Fatalf("all busy: %q, %v; want %q", reply, err, want)
	}
	r.passes()
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Level != "medium" || strings.Join(st.Types, ",") != "deepseek,sonnet" {
		t.Errorf("status %+v, want waiting for medium (deepseek, sonnet)", st)
	}
	r.lanes.mu.Lock()
	r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "free", "" // the level's sonnet frees, not the named type
	r.lanes.mu.Unlock()
	r.waitFor("the dispatch once the level's lane frees", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if d := r.factData("T-1", app.StepDispatched); d["lane"] != "O-02" {
		t.Errorf("dispatch fact %v, want O-02", d)
	}
}

func TestSpecTypeIsOverriddenByTypes(t *testing.T) {
	r := newLevelRig(t)
	r.start()
	r.specType("deepseek")
	if reply, err := r.dispatchLevel("d1", "sonnet"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-02 (queued)") {
		t.Fatalf("--types sonnet over Type: deepseek: %q, %v; want O-02", reply, err)
	}
	if d := r.factData("T-1", app.StepDispatched); d["level"] != nil {
		t.Errorf("dispatch fact %v, want no level (--types overrides everything)", d)
	}
}

func TestSpecTypeSpecSetRefusesAnUnknownType(t *testing.T) {
	r := newLevelRig(t)
	r.start()
	set := func(callID, typ string) error {
		_, err := app.Verdict(context.Background(), r.sock, app.MethodSpecSet, app.Call{Root: r.p.Root, Role: "lead",
			CallID: callID, Task: "T-1", Spec: "# T-1 — A\n\nDifficulty: medium\nType: " + typ + "\n"})
		return err
	}
	err := set("s1", "sonnet, nosuch")
	if err == nil || !strings.Contains(err.Error(), "nosuch") || !strings.Contains(err.Error(), "sonnet") ||
		!strings.Contains(err.Error(), "deepseek") {
		t.Errorf("spec set with an unknown type: %v; want a refusal naming it and the lane types", err)
	}
	if err := set("s2", "sonnet, deepseek"); err != nil {
		t.Errorf("spec set with known types: %v", err)
	}
}
