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

// T-538: obor dispatch <T-xx> takes its lane types from the spec's Difficulty level (config difficulty, built-in
// default), the first free lane of the first type that has one; no free lane of the level: the task waits for one,
// never for a higher level. The rig has the lane types sonnet and deepseek (the built-in lists' other types are not in
// the project: skipped), O-01 (deepseek) and O-02 (sonnet), both free.

func newLevelRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	edits = append([]func(map[string]any){
		testkit.Set("lane_types.sonnet", map[string]any{"kind": "claude"}),
		testkit.Set("lane_types.deepseek", map[string]any{"kind": "claude"}),
		testkit.Set("lanes.O-01", map[string]any{"type": "deepseek", "agent": "pj-o1"}),
		testkit.Set("lanes.O-02", "sonnet"),
	}, edits...)
	r := newDispatchRig(t, edits...)
	r.setLane(func(l *port.Lane) { l.Type = "deepseek" })
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: "O-02", Dir: r.p.LaneDir("O-02"), Type: "sonnet", State: "free"})
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

func TestDifficultyPicksTheFirstFreeLaneOfTheTypesInOrder(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.specLevel("medium") // sonnet, deepseek: O-02 (sonnet) before O-01 (deepseek), though O-01 is first in the config
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-02 (queued)") {
		t.Fatalf("medium dispatch: %q, %v; want O-02 (sonnet)", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["level"] != "medium" || d["lane"] != "O-02" {
		t.Errorf("dispatch fact %v, want level medium on O-02", d)
	}
}

// T-629: a level whose listed types no lane can run falls back to medium's list (sonnet before opus): a simple level
// naming a type no lane can run runs on sonnet, never waiting for a luna no lane can run. The reply has no note of it
// (T-723 deleted the "level has no lane" note).
func TestDifficultyFallsBackWhenTheLevelHasNoLane(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t, testkit.Set("lane_types.sonnet", map[string]any{"kind": "claude"}),
		testkit.Set("difficulty.simple", []string{"luna"}),
		testkit.Set("lanes.O-02", map[string]any{"type": "sonnet", "agent": "pj-o1"}))
	r.setLane(func(l *port.Lane) { l.Type = "opus" })
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: "O-02", Dir: r.lane, Type: "sonnet", State: "free", Agent: "pj-o1"})
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("simple") // luna: no lane; medium's sonnet is the fallback
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-02 (queued)") || strings.Contains(reply, "has no lane") {
		t.Fatalf("simple dispatch with no simple lane: %q, %v", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["level"] != "simple" || d["lane"] != "O-02" {
		t.Errorf("dispatch fact %v, want level simple on O-02", d)
	}
}

func TestDifficultyTakesTheNextTypeWhenTheFirstIsBusy(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.lanes.mu.Lock()
	r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "working", "T-9"
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("medium")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued)") {
		t.Fatalf("medium dispatch with sonnet busy: %q, %v; want O-01 (deepseek)", reply, err)
	}
}

func TestDifficultyWaitsForALaneOfTheLevelNeverAHigherOne(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.setLane(func(l *port.Lane) { l.Type = "opus" }) // O-01 is a free opus (hard) lane; O-02 (sonnet) is busy
	r.lanes.mu.Lock()
	r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "working", "T-9"
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("simple") // deepseek and sonnet: O-02 (sonnet) is busy, O-01 is a higher level
	const want = "WAITING T-1 (queued): waiting for a free lane (simple: deepseek, sonnet); idle: O-01 (opus)"
	if reply, err := r.dispatchLevel("d1"); err != nil || reply != want {
		t.Fatalf("simple dispatch, no lane of its types free: %q, %v; want %q", reply, err, want)
	}
	r.passes()
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Level != "simple" || strings.Join(st.Types, ",") != "deepseek,sonnet" {
		t.Errorf("status %+v, want waiting for simple (deepseek, sonnet)", st)
	}
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Fatalf("dispatched onto a lane of another level: %q", r.steps())
	}
	r.later(2 * time.Hour)
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Errorf("status %+v after two hours, want still waiting (a busy lane is not a needs-you)", st)
	}
	r.lanes.mu.Lock()
	r.lanes.lanes[1].Type, r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "deepseek", "free", ""
	r.lanes.mu.Unlock()
	r.waitFor("the dispatch once a deepseek lane frees", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if d := r.factData("T-1", event.StepDispatched); d["lane"] != "O-02" || d["level"] != "simple" {
		t.Errorf("dispatch fact %v, want simple on O-02", d)
	}
}

func TestDifficultyTypesOverrideTheLevel(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.specLevel("hard") // opus: no lane of it; --types sonnet reaches O-02
	if reply, err := r.dispatchLevel("d1", "sonnet"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-02 (queued)") {
		t.Fatalf("--types sonnet: %q, %v; want O-02", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["level"] != nil {
		t.Errorf("dispatch fact %v, want no level (--types overrides it)", d)
	}
}

func TestDifficultyTypesWithNoFreeLaneRefuse(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t)
	r.start()
	r.specLevel("medium")
	reply, err := r.dispatchLevel("d1", "opus")
	if err == nil || !strings.Contains(err.Error(), "no free lane of the types opus") || reply != "" {
		t.Errorf("--types opus: %q, %v; want the refusal, no waiting", reply, err)
	}
	if n := r.taskStep("T-1", "fact waiting"); n != 0 {
		t.Errorf("--types waited: %q", r.steps())
	}
}

func TestDifficultyConfigOverridesTheDefault(t *testing.T) {
	t.Parallel()
	r := newLevelRig(t, testkit.Set("difficulty.medium", []string{"deepseek"}))
	r.start()
	r.specLevel("medium")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued)") {
		t.Fatalf("medium with difficulty.medium = [deepseek]: %q, %v; want O-01", reply, err)
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
}

// A request that replaces a waiting one (new after, new types) is the task's waiting fact in force; the old one's
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
	if reply, err := r.dispatchLevel("d2", "deepseek"); err == nil || reply != "" || !strings.Contains(err.Error(), "no free lane of the types deepseek") {
		t.Fatalf("--types while all is busy: %q, %v; want the refusal (no waiting)", reply, err)
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Level != "medium" {
		t.Fatalf("status %+v after a refused replacement, want the first request waiting", st)
	}
	r.setSpec("s2", "# T-1 — Leveled\n\nDifficulty: simple\n")
	reply, err := r.dispatchLevel("d3")
	if err != nil || !strings.HasPrefix(reply, "WAITING T-1 (queued): waiting for a free lane (simple: deepseek, sonnet)") {
		t.Fatalf("replacement at another level: %q, %v", reply, err)
	}
	if n := r.taskStep("T-1", "fact waiting"); n != 2 {
		t.Errorf("waiting facts %d, want 2 (the request, its replacement)", n)
	}
	if st := r.taskStatus("T-1"); st.Level != "simple" || strings.Join(st.Types, ",") != "deepseek,sonnet" {
		t.Errorf("status %+v, want the replacement's level and types", st)
	}
	r.lanes.mu.Lock()
	r.lanes.lanes[0].State, r.lanes.lanes[0].Task = "free", ""
	r.lanes.mu.Unlock()
	r.waitFor("the replacement's dispatch", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if n := r.taskStep("T-1", "fact waiting"); n != 2 {
		t.Errorf("waiting facts %d after the dispatch, want 2", n)
	}
}
