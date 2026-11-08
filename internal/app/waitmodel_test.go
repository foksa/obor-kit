package app_test

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
)

// T-977: a waiting task's status carries its Difficulty and the model it will run on: the level's first model not
// paused, or a --model override's (the override names no level: the Difficulty is the stored spec's).
func TestWaitingTaskCarriesDifficultyAndModel(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, pooled())
	r.lanes.mu.Lock()
	for i := range r.lanes.lanes {
		r.lanes.lanes[i].State, r.lanes.lanes[i].Task = "working", "T-9"
	}
	r.lanes.mu.Unlock()
	r.start()

	// The level: medium is sonnet, deepseek; sonnet paused, so deepseek.
	r.specLevel("medium")
	if reply, err := r.pause("lead", "p1", wire.PauseReq{Model: "sonnet", Why: "quota"}); err != nil {
		t.Fatalf("pause sonnet: %q, %v", reply, err)
	}
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "WAITING T-1") {
		t.Fatalf("medium with no free lane: %q, %v; want it waiting", reply, err)
	}
	st := r.taskStatus("T-1")
	if st.State != "waiting" || st.Level != "medium" || strings.Join(st.Types, ",") != "sonnet,deepseek" {
		t.Fatalf("precondition: T-1 %+v, want waiting at medium on sonnet, deepseek", st)
	}
	if st.Difficulty != "medium" || st.Model != "deepseek" {
		t.Errorf("T-1 difficulty %q model %q; want medium on deepseek (sonnet paused)", st.Difficulty, st.Model)
	}

	// A --model override, after a task not merged: no level in the request, the spec's Difficulty; the override's model.
	r.specOf("T-2", "# T-2 — First\n\nDifficulty: simple\n")
	r.specOf("T-3", "# T-3 — Override\n\nDifficulty: hard\n")
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d3"}, Task: "T-3", Types: []string{"opus"},
		After: []string{"T-2"}}); err != nil || !strings.HasPrefix(reply, "WAITING T-3") {
		t.Fatalf("--model opus --after T-2: %q, %v; want it waiting", reply, err)
	}
	st = r.taskStatus("T-3")
	if st.State != "waiting" || st.Level != "" || strings.Join(st.After, ",") != "T-2" {
		t.Fatalf("precondition: T-3 %+v, want waiting after T-2 with no level (the override's)", st)
	}
	if st.Difficulty != "hard" || st.Model != "opus" {
		t.Errorf("T-3 difficulty %q model %q; want hard (its spec) on opus (the override)", st.Difficulty, st.Model)
	}
}
