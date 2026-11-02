package app_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/testkit"
)

// T-695: effort beside the type. A type reference `opus:high` (the built-in hard level, a spec's Type: line, --types)
// matches a lane of type opus; the dispatch records and replies the effort, the worker's start request carries it, and
// status shows it.

// effortRig is a dispatch rig whose lane O-01 (agent pj-o1) is a free opus lane.
func effortRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newDispatchRig(t, append([]func(map[string]any){testkit.Set("lanes.O-01", map[string]any{"type": "opus", "agent": "pj-o1"})},
		edits...)...)
	r.setLane(func(l *app.Lane) { l.Type, l.Agent = "opus", "pj-o1" })
	return r
}

// launchOf is the first Agents.Launch of agent (ok false: none yet).
func (r *loopRig) launchOf(agent string) (app.Launch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches {
		if l.Agent == agent {
			return l, true
		}
	}
	return app.Launch{}, false
}

func TestEffortHardLevelStartsAnOpusLaneAtHigh(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("hard") // the built-in hard level is opus:high
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued) effort high") {
		t.Fatalf("hard dispatch: %q, %v; want O-01 at effort high", reply, err)
	}
	if d := r.factData("T-1", app.StepDispatched); d["effort"] != "high" || d["lane"] != "O-01" {
		t.Errorf("dispatch fact %v, want effort high on O-01", d)
	}
	r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-o1"); return ok })
	if l, _ := r.launchOf("pj-o1"); l.Effort != "high" || !l.Fresh {
		t.Errorf("worker start %+v, want a fresh start at effort high", l)
	}
	if st := r.taskStatus("T-1"); st.Effort != "high" {
		t.Errorf("status %+v, want effort high", st)
	}
}

func TestEffortSpecTypeAndTypesNameIt(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Typed\n\nDifficulty: medium\nType: opus:max\n")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued) effort max") {
		t.Fatalf("Type: opus:max dispatch: %q, %v", reply, err)
	}
	if d := r.factData("T-1", app.StepDispatched); d["effort"] != "max" {
		t.Errorf("dispatch fact %v, want effort max", d)
	}

	r2 := effortRig(t)
	r2.start()
	r2.specLevel("medium")
	if reply, err := r2.dispatchLevel("d1", "opus:low"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued) effort low") {
		t.Fatalf("--types opus:low: %q, %v", reply, err)
	}
}

func TestEffortPlainTypeNamesNone(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	reply, err := r.dispatchLevel("d1", "opus")
	if err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
		t.Fatalf("--types opus: %q, %v; want no effort in the reply", reply, err)
	}
	if d := r.factData("T-1", app.StepDispatched); d["effort"] != nil {
		t.Errorf("dispatch fact %v, want no effort (the type's args)", d)
	}
	r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-o1"); return ok })
	if l, _ := r.launchOf("pj-o1"); l.Effort != "" {
		t.Errorf("worker start %+v, want no effort", l)
	}
}

func TestEffortRefusals(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	if _, err := r.dispatchLevel("d1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
		t.Errorf("--types opus:turbo: %v; want a refusal naming claude's efforts", err)
	}
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("a refused dispatch recorded %d dispatch facts", n)
	}
	set := func(callID, typ string) error {
		_, err := app.Verdict(context.Background(), r.sock, app.MethodSpecSet, app.Call{Root: r.p.Root, Role: "lead",
			CallID: callID, Task: "T-2", Spec: "# T-2 — A\n\nDifficulty: medium\nType: " + typ + "\n"})
		return err
	}
	if err := set("s1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
		t.Errorf("spec set Type: opus:turbo: %v; want a refusal", err)
	}
	if err := set("s2", "ghost:high"); err == nil || !strings.Contains(err.Error(), "names a lane type this project does not have") {
		t.Errorf("spec set Type: ghost:high: %v; want the unknown type refused", err)
	}
	if err := set("s3", "opus:high"); err != nil {
		t.Errorf("spec set Type: opus:high: %v", err)
	}
}
