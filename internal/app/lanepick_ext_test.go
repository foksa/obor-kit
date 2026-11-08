package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// T-765: a model paused with obor pause --model is skipped and the level's next model runs; every model of the level
// paused: the task waits with each pause named, and dispatches once a pause is cleared. Serve's status lists the model
// pauses in force (T-766).
func TestDifficultySkipsAPausedModel(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, pooled())
	r.start()
	r.specLevel("medium") // sonnet, deepseek
	if reply, err := r.pause("lead", "p0", wire.PauseReq{Model: "gpt",
		Why: "x"}); err == nil || !strings.Contains(err.Error(), `gpt is not a model`) {
		t.Errorf("pause of an unknown model: %q, %v; want refused", reply, err)
	}
	if reply, err := r.pause("lead", "p1", wire.PauseReq{Model: "sonnet",
		Why: "quota"}); err != nil || reply != "PAUSED sonnet until cleared" {
		t.Fatalf("pause sonnet: %q, %v", reply, err)
	}
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as deepseek") {
		t.Fatalf("medium with sonnet paused: %q, %v; want deepseek", reply, err)
	}
	r.specOf("T-3", "# T-3 — Leveled\n\nDifficulty: medium\n")
	if _, err := r.pause("lead", "p2", wire.PauseReq{Model: "deepseek", Why: "down",
		Until: time.Now().Add(3 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	reply, err = r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: "d3"}, Task: "T-3"})
	if err != nil || !strings.HasPrefix(reply, "WAITING T-3 (queued): sonnet paused: quota (until cleared), deepseek paused: down (until ") ||
		!strings.Contains(reply, "(medium: sonnet, deepseek)") {
		t.Fatalf("medium with both paused: %q, %v; want WAITING naming both pauses", reply, err)
	}
	if n := r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Fatalf("dispatched with every model paused: %q", r.steps())
	}
	// serve's status names both model pauses in force (T-766: the Setup page shows them), and not the project's
	if st := r.app.Status().Project; st.Paused != nil || len(st.ModelPauses) != 2 || st.ModelPauses["sonnet"] == nil ||
		st.ModelPauses["sonnet"].Why != "quota" || st.ModelPauses["sonnet"].Until != 0 || st.ModelPauses["deepseek"] == nil ||
		st.ModelPauses["deepseek"].Until == 0 {
		t.Errorf("status pauses %+v, %+v; want sonnet's (quota, until cleared) and deepseek's (until set)", st.Paused, st.ModelPauses)
	}
	if reply, err := r.pause("lead", "c1", wire.PauseReq{Model: "sonnet",
		Clear: true}); err != nil || reply != "PAUSE CLEARED sonnet" {
		t.Fatalf("clear sonnet: %q, %v", reply, err)
	}
	r.waitFor("T-3's dispatch once sonnet's pause ends", func() bool { return r.taskStep("T-3", "fact dispatch") == 1 })
	if mp := r.app.Status().Project.ModelPauses; len(mp) != 1 || mp["sonnet"] != nil {
		t.Errorf("status model pauses after sonnet's clear %+v, want deepseek's only", mp)
	}
	if d := r.factData("T-3", event.StepDispatched); d["type"] != "sonnet" || d["lane"] != "L-02" {
		t.Errorf("T-3's dispatch %v, want sonnet on L-02", d)
	}
	if reply, err := r.pause("lead", "c2", wire.PauseReq{Model: "sonnet",
		Clear: true}); err != nil || reply != "NOT PAUSED sonnet" {
		t.Errorf("clear sonnet again: %q, %v", reply, err)
	}
}

func TestDifficultyModelsWithNoFreeLaneRefuse(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, pooled())
	r.lanes.mu.Lock()
	for i := range r.lanes.lanes {
		r.lanes.lanes[i].State, r.lanes.lanes[i].Task = "working", "T-9"
	}
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("medium")
	reply, err := r.dispatchLevel("d1", "opus")
	if err == nil || !strings.Contains(err.Error(), "no free lane for the models opus") || reply != "" {
		t.Errorf("--model opus: %q, %v; want the refusal, no waiting", reply, err)
	}
	if n := r.taskStep("T-1", "fact waiting"); n != 0 {
		t.Errorf("--model waited: %q", r.steps())
	}
	if _, err := r.dispatchLevel("d2", "gpt"); err == nil || !strings.Contains(err.Error(), `"gpt" is not a model`) {
		t.Errorf("--model gpt: %v, want refused naming the models", err)
	}
}

// T-483: a dispatch reply names the lanes still free after it ("; idle: …"), not the lane it takes; dirty-free and
// working lanes are not idle, and nothing is appended when none stay free. A waiting request takes none: every free
// lane is listed.
func TestDispatchNamesIdleLanes(t *testing.T) {
	t.Parallel()
	busy := []port.Lane{{Name: "L-03", State: "dirty-free"}, {Name: "L-04", State: "working"}}
	t.Run("two free lanes: the other one is idle", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, dispatching())
		r.addLanes(append([]port.Lane{{Name: "L-02", State: "free"}}, busy...)...)
		r.start()
		reply, err := r.dispatch("d1", "", "sonnet", "opus")
		if err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet; idle: L-02" {
			t.Fatalf("dispatch: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
	})
	t.Run("the last free lane taken: no idle", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, dispatching())
		r.addLanes(busy...)
		r.start()
		reply, err := r.dispatch("d1", "", "sonnet")
		if err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
			t.Fatalf("dispatch: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
	})
	t.Run("waiting: every free lane is idle", func(t *testing.T) {
		t.Parallel()
		r := startWithT2(t)
		r.addLanes(append([]port.Lane{{Name: "L-02", State: "free"}}, busy...)...)
		reply, err := r.waitAfter("d1", "T-2")
		if err != nil || reply != "WAITING T-1 after T-2 (queued); idle: L-01, L-02" {
			t.Fatalf("dispatch --after: %q, %v", reply, err)
		}
	})
}
