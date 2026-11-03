package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"testing"
	"time"

	"obor/internal/testkit"
)

// T-538, T-765: obor dispatch <T-xx> takes its models from the spec's Difficulty level (config difficulty, built-in
// default), the spec's Model: line first: the first model not paused, on the first free lane (every lane is pooled);
// no free lane: the task waits for one; every model of the request paused: it waits, the reply says why. The rig has
// lanes L-01 and L-02, both free.

func newLevelRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newDispatchRig(t, append([]func(map[string]any){testkit.Set("lanes", 2)}, edits...)...)
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: "L-02", Dir: r.p.LaneDir("L-02"), State: "free"})
	r.lanes.mu.Unlock()
	return r
}

// dispatchLevel is the lead's dispatch of T-1 by its spec's level (no lane, no types).
func (r *loopRig) dispatchLevel(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID, Task: "T-1", Types: types})
}

func (r *loopRig) specLevel(level string) {
	r.t.Helper()
	r.setSpec("s-"+level, "# T-1 — Leveled\n\nDifficulty: "+level+"\n")
}

// factData is the data of task's first fact of step.
func (r *loopRig) factData(task, step string) map[string]any {
	r.t.Helper()
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind == event.KindFact && rec.Step == step {
			var d map[string]any
			if err := json.Unmarshal(rec.Data, &d); err != nil {
				r.t.Fatal(err)
			}
			return d
		}
	}
	r.t.Fatalf("no fact %s of %s: %q", step, task, r.steps())
	return nil
}

func TestDifficultyRunsTheLevelsFirstModel(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.specLevel("medium") // sonnet, deepseek: sonnet on the first free lane
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as sonnet") {
		t.Fatalf("medium dispatch: %q, %v; want L-01 as sonnet", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["level"] != "medium" || d["lane"] != "L-01" || d["type"] != "sonnet" {
		t.Errorf("dispatch fact %v, want level medium, sonnet on L-01", d)
	}
}

// T-689, T-765: the spec's Model: line puts its models first.
func TestDifficultyModelLineFirst(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Leveled\n\nDifficulty: medium\nModel: haiku:low, sonnet\n")
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as haiku effort low") {
		t.Fatalf("Model: haiku:low: %q, %v; want haiku at effort low", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["type"] != "haiku:low" || d["effort"] != "low" {
		t.Errorf("dispatch fact %v, want haiku:low", d)
	}
}

// T-765: a model paused with obor pause --model is skipped and the level's next model runs; every model of the level
// paused: the task waits with each pause named, and dispatches once a pause is cleared.
func TestDifficultySkipsAPausedModel(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.specLevel("medium") // sonnet, deepseek
	if reply, err := r.pause("lead", "p0", wire.Call{Model: "gpt", Note: "x"}); err == nil || !strings.Contains(err.Error(), `gpt is not a model`) {
		t.Errorf("pause of an unknown model: %q, %v; want refused", reply, err)
	}
	if reply, err := r.pause("lead", "p1", wire.Call{Model: "sonnet", Note: "quota"}); err != nil || reply != "PAUSED sonnet until cleared" {
		t.Fatalf("pause sonnet: %q, %v", reply, err)
	}
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as deepseek") {
		t.Fatalf("medium with sonnet paused: %q, %v; want deepseek", reply, err)
	}
	r.specOf("T-3", "# T-3 — Leveled\n\nDifficulty: medium\n")
	if _, err := r.pause("lead", "p2", wire.Call{Model: "deepseek", Note: "down", Until: time.Now().Add(3 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	reply, err = r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d3", Task: "T-3"})
	if err != nil || !strings.HasPrefix(reply, "WAITING T-3 (queued): sonnet paused: quota (until cleared), deepseek paused: down (until ") ||
		!strings.Contains(reply, "(medium: sonnet, deepseek)") {
		t.Fatalf("medium with both paused: %q, %v; want WAITING naming both pauses", reply, err)
	}
	if n := r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Fatalf("dispatched with every model paused: %q", r.steps())
	}
	if reply, err := r.pause("lead", "c1", wire.Call{Model: "sonnet", Clear: true}); err != nil || reply != "PAUSE CLEARED sonnet" {
		t.Fatalf("clear sonnet: %q, %v", reply, err)
	}
	r.waitFor("T-3's dispatch once sonnet's pause ends", func() bool { return r.taskStep("T-3", "fact dispatch") == 1 })
	if d := r.factData("T-3", event.StepDispatched); d["type"] != "sonnet" || d["lane"] != "L-02" {
		t.Errorf("T-3's dispatch %v, want sonnet on L-02", d)
	}
	if reply, err := r.pause("lead", "c2", wire.Call{Model: "sonnet", Clear: true}); err != nil || reply != "NOT PAUSED sonnet" {
		t.Errorf("clear sonnet again: %q, %v", reply, err)
	}
}

func TestDifficultyWaitsForAFreeLane(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.lanes.mu.Lock()
	for i := range r.lanes.lanes {
		r.lanes.lanes[i].State, r.lanes.lanes[i].Task = "working", "T-9"
	}
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("simple")
	const want = "WAITING T-1 (queued): waiting for a free lane (simple: deepseek, sonnet, haiku)"
	if reply, err := r.dispatchLevel("d1"); err != nil || reply != want {
		t.Fatalf("simple dispatch, no lane free: %q, %v; want %q", reply, err, want)
	}
	r.passes()
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Level != "simple" || strings.Join(st.Types, ",") != "deepseek,sonnet,haiku" {
		t.Errorf("status %+v, want waiting for simple (deepseek, sonnet, haiku)", st)
	}
	r.later(2 * time.Hour)
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Errorf("status %+v after two hours, want still waiting (a busy lane is not a needs-you)", st)
	}
	r.lanes.mu.Lock()
	r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "free", ""
	r.lanes.mu.Unlock()
	r.waitFor("the dispatch once a lane frees", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if d := r.factData("T-1", event.StepDispatched); d["lane"] != "L-02" || d["level"] != "simple" || d["type"] != "deepseek" {
		t.Errorf("dispatch fact %v, want simple's deepseek on L-02", d)
	}
}

func TestDifficultyModelsOverrideTheLevel(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.specLevel("hard")
	if reply, err := r.dispatchLevel("d1", "terra:high"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as terra effort high") {
		t.Fatalf("--model terra:high: %q, %v", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["level"] != nil {
		t.Errorf("dispatch fact %v, want no level (--model overrides it)", d)
	}
}

func TestDifficultyModelsWithNoFreeLaneRefuse(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
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

func TestDifficultyConfigOverridesTheDefault(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t, testkit.Set("difficulty.medium", []string{"luna:low"}))
	r.start()
	r.specLevel("medium")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as luna effort low") {
		t.Fatalf("medium with difficulty.medium = [luna:low]: %q, %v", reply, err)
	}
}

func TestDifficultySpecSetRefusesASpecWithoutALevel(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	for _, text := range []string{"# T-1 — A\n", "# T-1 — A\n\nDifficulty: easy\n", "# T-1 — A\n\nsome text\nDifficulty: hard\n"} {
		_, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: "s", Task: "T-1", Spec: text})
		if err == nil || !strings.Contains(err.Error(), "hard, medium, simple") {
			t.Errorf("spec set of %q: %v; want a refusal naming the levels", text, err)
		}
	}
	for text, want := range map[string]string{
		"# T-1 — A\n\nDifficulty: hard\nType: opus\n":      `the "Type:" line is now "Model:`,
		"# T-1 — A\n\nDifficulty: hard\nModel: gpt\n":      `"Model: gpt": "gpt" is not a model`,
		"# T-1 — A\n\nDifficulty: hard\nModel: luna:max\n": `effort "max" is not one of codex's`,
	} {
		_, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: "s-" + want, Task: "T-1", Spec: text})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("spec set of %q: %v; want %q", text, err, want)
		}
	}
}

// A request that replaces a waiting one (new after, new models) is the task's waiting fact in force; the old one's
// reason does not carry over; a lane that is free then dispatches it.
func TestDifficultyRedispatchOfAWaitingTaskReplacesIt(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.lanes.mu.Lock()
	r.lanes.lanes[0].State, r.lanes.lanes[0].Task = "working", "T-9"
	r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "working", "T-9"
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("medium")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "WAITING T-1 (queued): waiting for a free lane (medium: sonnet, deepseek)") {
		t.Fatalf("first request: %q, %v", reply, err)
	}
	if reply, err := r.dispatchLevel("d2", "deepseek"); err == nil || reply != "" || !strings.Contains(err.Error(), "no free lane for the models deepseek") {
		t.Fatalf("--model while all is busy: %q, %v; want the refusal (no waiting)", reply, err)
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Level != "medium" {
		t.Fatalf("status %+v after a refused replacement, want the first request waiting", st)
	}
	r.setSpec("s2", "# T-1 — Leveled\n\nDifficulty: simple\n")
	reply, err := r.dispatchLevel("d3")
	if err != nil || !strings.HasPrefix(reply, "WAITING T-1 (queued): waiting for a free lane (simple: deepseek, sonnet, haiku)") {
		t.Fatalf("replacement at another level: %q, %v", reply, err)
	}
	if n := r.taskStep("T-1", "fact waiting"); n != 2 {
		t.Errorf("waiting facts %d, want 2 (the request, its replacement)", n)
	}
	if st := r.taskStatus("T-1"); st.Level != "simple" || strings.Join(st.Types, ",") != "deepseek,sonnet,haiku" {
		t.Errorf("status %+v, want the replacement's level and models", st)
	}
	r.lanes.mu.Lock()
	r.lanes.lanes[0].State, r.lanes.lanes[0].Task = "free", ""
	r.lanes.mu.Unlock()
	r.waitFor("the replacement's dispatch", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if n := r.taskStep("T-1", "fact waiting"); n != 2 {
		t.Errorf("waiting facts %d after the dispatch, want 2", n)
	}
}
