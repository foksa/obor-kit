package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"testing"
	"time"

	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// T-738, T-765: a redo runs the model ref its dispatch ran unless its --model names another; a model ref naming no
// model of the project is refused before a lane is picked. The rig: lanes L-01 and L-02, both free.

func newPoolRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newDispatchRig(t, append([]func(map[string]any){testkit.Set("lanes", 2)}, edits...)...)
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: "L-02", Dir: r.p.LaneDir("L-02"), State: "free"})
	r.lanes.mu.Unlock()
	return r
}

// specOf stores task's spec (obor spec set).
func (r *loopRig) specOf(task, text string) {
	r.t.Helper()
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Agent: "lead",
		Role: "lead", CallID: "s-" + task, Task: task, Spec: text}); err != nil {
		r.t.Fatalf("spec set %s: %v", task, err)
	}
}

// dispatchOf is the lead's dispatch of task by its spec's level.
func (r *loopRig) dispatchOf(task string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-" + task, Task: task})
}

// redoRig is the pool rig on serve's own lane table (checks.Lanes over the fixture's worktrees and the lanes table
// serve writes): T-1's dispatch ran opus:high on L-01 (prepared, so its lane row holds it with that type)
// and left a needs-you; L-01 has T-1's work committed (working on T-1).
func redoRig(t *testing.T) *loopRig {
	t.Helper()
	r := newPoolRig(t)
	r.lanes.mu.Lock()
	r.lanes.real = checks.Lanes{}
	r.lanes.mu.Unlock()
	r.p.Commit(r.lane)
	d, _ := json.Marshal(map[string]any{"lane": "L-01", "agent": "pj-l-01", "branch": "task/T-1", "type": "opus:high",
		"level": "hard", "types": []string{"opus:high"}})
	prep, _ := json.Marshal(map[string]any{"base": "b"})
	stop, _ := json.Marshal(event.FactData{Lane: "L-01", Reason: "stopped: api error", Merge: true, Cause: "stopped"})
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult, Data: prep},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact, Data: stop}); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitFor("T-1's needs-you", func() bool { return r.taskStatus("T-1").State == "needs-you" })
	r.waitFor("L-01's row with T-1 as opus:high", func() bool {
		row, ok, _ := store.ReadLaneAt(r.p.Root, "L-01")
		return ok && row.Task == "T-1" && row.Type == "opus:high"
	})
	ls, err := checks.Lanes{}.Lanes(r.p.Root)
	if err != nil || len(ls) == 0 || ls[0].Name != "L-01" || ls[0].State != "working" || ls[0].Task != "T-1" {
		t.Fatalf("precondition: serve's lane table %+v, %v; want L-01 working on T-1", ls, err)
	}
	cfg, err := config.LoadDaemon(r.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if st := checks.NewLaneTable(cfg).States(); st[0].Type != "opus" {
		t.Fatalf("precondition: obor status's type of L-01 %q, want its dispatch's opus", st[0].Type)
	}
	r.specOf("T-1", "# T-1 — Redone\n\nDifficulty: medium\n")
	return r
}

// redoTypes is the type of each of T-1's dispatch facts, in order.
func (r *loopRig) redoTypes() []any {
	var types []any
	for _, rec := range r.records() {
		if rec.Task == "T-1" && rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			var m map[string]any
			_ = json.Unmarshal(rec.Data, &m)
			types = append(types, m["type"])
		}
	}
	return types
}

// A redo by task ID through serve's real lane table keeps the full model ref its dispatch ran (opus:high), not the lane
// row's bare model, nor the spec's level (medium: sonnet); a pause of opus does not hold it (its own model, T-742).
func TestRedoViaTheRealLaneTableKeepsItsTypeAndEffort(t *testing.T) {
	t.Parallel()
	r := redoRig(t)
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodPause, wire.Call{Root: r.p.Root, CallID: "p1",
		Model: "opus", Note: "quota"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.dispatchOf("T-1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort high") {
		t.Fatalf("redo of T-1: %q, %v; want L-01 as opus effort high", reply, err)
	}
	if types := r.redoTypes(); len(types) != 2 || types[1] != "opus:high" {
		t.Errorf("T-1's dispatch facts' types %v, want the redo's opus:high after the first", types)
	}
}

// A redo with --model sonnet runs sonnet on its lane; to a paused model it is refused, as a fresh dispatch of that
// model is (T-742).
func TestRedoWithExplicitTypesRunsThem(t *testing.T) {
	t.Parallel()
	r := redoRig(t)
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodPause, wire.Call{Root: r.p.Root, CallID: "p1",
		Model: "haiku", Note: "quota"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-T-1h", Task: "T-1",
		Types: []string{"haiku"}}); err == nil || !strings.Contains(err.Error(), "haiku paused: quota") {
		t.Fatalf("redo of T-1 --model haiku while haiku is paused: %q, %v; want refused naming the pause", reply, err)
	}
	reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-T-1", Task: "T-1",
		Types: []string{"sonnet"}})
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as sonnet") {
		t.Fatalf("redo of T-1 --model sonnet: %q, %v; want L-01 as sonnet", reply, err)
	}
	if types := r.redoTypes(); len(types) != 2 || types[1] != "sonnet" {
		t.Errorf("T-1's dispatch facts' types %v, want the redo's sonnet after the first", types)
	}
}

// --model naming no model of the project, bare or with an effort, is refused before a lane is picked: no dispatch
// fact, nor, with --after, a waiting fact.
func TestUnknownExplicitTypeIsRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		types []string
		after []string
	}{
		{"bare", []string{"lunna"}, nil},
		{"with an effort", []string{"lunna:high"}, nil},
		{"after a known one", []string{"sonnet", "lunna"}, nil},
		{"with --after", []string{"lunna"}, []string{"T-9"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newPoolRig(t)
			r.start()
			r.specOf("T-9", "# T-9 — First\n\nDifficulty: medium\n")
			r.specOf("T-1", "# T-1 — Typed\n\nDifficulty: medium\n")
			reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-T-1",
				Task: "T-1", Types: c.types, After: c.after})
			if err == nil || !strings.Contains(err.Error(), `"lunna" is not a model (models: `) {
				t.Fatalf("dispatch --model %v: %q, %v; want refused naming lunna", c.types, reply, err)
			}
			for _, step := range []string{"fact dispatch", "fact waiting"} {
				if n := r.taskStep("T-1", step); n != 0 {
					t.Errorf("%s recorded for the refused dispatch: %q", step, r.steps())
				}
			}
			// The same project takes a known model: the refusal is the model's.
			if reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-T-1b",
				Task: "T-1", Types: []string{"sonnet"}, After: c.after}); err != nil ||
				!strings.HasPrefix(reply, "DISPATCHED T-1") && !strings.HasPrefix(reply, "WAITING T-1") {
				t.Errorf("dispatch --model sonnet: %q, %v; want it taken", reply, err)
			}
		})
	}
}
