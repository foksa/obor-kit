package app_test

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-695: effort beside the model. A model ref `opus:high` (the built-in hard level, a spec's Model: line, --model):
// the dispatch records and replies the effort, the worker's start request carries it, and status shows it.

// effortRig is a dispatch rig whose lane L-01 (agent pj-l-01) is free.
func effortRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newDispatchRig(t, edits...)
	r.setLane(func(l *port.Lane) { l.Agent = "pj-l-01" })
	return r
}

// launchOf is the first Agents.Launch of agent (ok false: none yet).
func (r *loopRig) launchOf(agent string) (port.Launch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches {
		if l.Agent == agent {
			return l, true
		}
	}
	return port.Launch{}, false
}

func TestEffortHardLevelStartsAnOpusLaneAtHigh(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("hard") // the built-in hard level is opus:high
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort high") {
		t.Fatalf("hard dispatch: %q, %v; want L-01 at effort high", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["effort"] != "high" || d["lane"] != "L-01" || d["type"] != "opus:high" {
		t.Errorf("dispatch fact %v, want effort high and type opus:high on L-01 (T-721)", d)
	}
	r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-l-01"); return ok })
	if row, ok := r.p.Row("L-01"); !ok || row.Task != "T-1" || row.Type != "opus:high" {
		t.Errorf("lane row %+v (%v), want T-1 with type opus:high (T-721)", row, ok)
	}
	if l, _ := r.launchOf("pj-l-01"); l.Effort != "high" || !l.Fresh || l.Type != "opus" {
		t.Errorf("worker start %+v, want a fresh start of the dispatch's type opus at effort high (T-722)", l)
	}
	if st := r.taskStatus("T-1"); st.Effort != "high" {
		t.Errorf("status %+v, want effort high", st)
	}
}

func TestEffortSpecTypeAndTypesNameIt(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Typed\n\nDifficulty: medium\nModel: opus:max\n")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort max") {
		t.Fatalf("Model: opus:max dispatch: %q, %v", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["effort"] != "max" {
		t.Errorf("dispatch fact %v, want effort max", d)
	}

	r2 := effortRig(t)
	r2.start()
	r2.specLevel("medium")
	if reply, err := r2.dispatchLevel("d1", "opus:low"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort low") {
		t.Fatalf("--model opus:low: %q, %v", reply, err)
	}
}

func TestEffortPlainTypeNamesNone(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	reply, err := r.dispatchLevel("d1", "opus")
	if err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as opus" {
		t.Fatalf("--model opus: %q, %v; want no effort in the reply", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["effort"] != nil || d["type"] != "opus" {
		t.Errorf("dispatch fact %v, want no effort (the kind's default) and type opus (T-721)", d)
	}
	r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-l-01"); return ok })
	if l, _ := r.launchOf("pj-l-01"); l.Effort != "" || l.Type != "opus" {
		t.Errorf("worker start %+v, want the dispatch's type opus (T-722) and no effort", l)
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
		_, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: callID, Task: "T-2", Spec: "# T-2 — A\n\nDifficulty: medium\nModel: " + typ + "\n"})
		return err
	}
	if err := set("s1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
		t.Errorf("spec set Model: opus:turbo: %v; want a refusal", err)
	}
	if err := set("s2", "ghost:high"); err == nil || !strings.Contains(err.Error(), `"ghost" is not a model`) {
		t.Errorf("spec set Model: ghost:high: %v; want the unknown model refused", err)
	}
	if err := set("s3", "opus:high"); err != nil {
		t.Errorf("spec set Model: opus:high: %v", err)
	}
}

// T-705: dispatch's reply warns when a Claude lane's worktree has no enabled obor plugin (its worker would have no
// obor MCP tools), and still dispatches; a lane with the plugin enabled gets no warning (TestEffortPlainTypeNamesNone).
func TestDispatchWarnsAClaudeLaneWithoutThePlugin(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	settings := filepath.Join(r.p.LaneDir("L-01"), ".claude", "settings.json")
	if err := os.Remove(settings); err != nil {
		t.Fatalf("precondition: the lane had the plugin enabled: %v", err)
	}
	reply, err := r.dispatchLevel("d1", "opus")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued)") ||
		!strings.Contains(reply, "WARN lane L-01 has no enabled obor plugin") {
		t.Fatalf("dispatch without the plugin: %q, %v; want DISPATCHED … and the WARN", reply, err)
	}
}
