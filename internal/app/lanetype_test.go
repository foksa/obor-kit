package app_test

import (
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

// T-738: one answer to "what type does this lane run now" (store.LaneTypeRef): an open dispatch's recorded type
// reference, else, for a dispatch recorded before T-721 (no type), its lane's type in the config as it is read now.

// seedLegacy writes T-2's open dispatch on O-01 as recorded before T-721 (no type) to the log of the stopped app.
func (r *loopRig) seedLegacy() {
	r.t.Helper()
	d, _ := json.Marshal(map[string]any{"lane": "O-01", "agent": "pj-o1"})
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-2", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d}); err != nil {
		r.t.Fatal(err)
	}
}

// A legacy open dispatch on a lane typed opus counts toward opus's max (it runs the lane's configured type): the
// hard T-1 waits although the pool lane O-02 is free.
func TestLegacyDispatchOnATypedLaneCountsTowardItsTypesMax(t *testing.T) {
	t.Parallel()
	r := newPoolRig(t, testkit.Set("lanes.O-01", map[string]any{"type": "opus", "agent": "pj-o1"}))
	r.setLane(func(l *port.Lane) { l.Type = "opus" })
	r.seedLegacy()
	r.start()
	r.waitFor("T-2 folded", func() bool { return r.taskStatus("T-2").Task == "T-2" })
	if d := r.factData("T-2", event.StepDispatched); d["type"] != nil {
		t.Fatalf("precondition: T-2's dispatch fact %v records a type", d)
	}
	r.specOf("T-1", "# T-1 — Hard\n\nDifficulty: hard\n")
	const want = "WAITING T-1 (queued): opus at its max 1 (hard: opus:high); idle: O-02 (-)"
	if reply, err := r.dispatchOf("T-1"); err != nil || reply != want {
		t.Fatalf("T-1 beside T-2's legacy opus dispatch: %q, %v; want %q", reply, err, want)
	}
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("T-1 dispatched past opus's max: %q", r.steps())
	}
}

// A legacy open dispatch on a lane the config has since blanked (a pool lane now) runs no known type: it counts
// toward no type's max, so the hard T-1 takes the free O-02 as opus.
func TestLegacyDispatchOnABlankedLaneCountsTowardNoMax(t *testing.T) {
	t.Parallel()
	r := newPoolRig(t)
	r.seedLegacy()
	r.start()
	r.waitFor("T-2 folded", func() bool { return r.taskStatus("T-2").Task == "T-2" })
	if got := store.LaneTypeRef("", ""); got != "" {
		t.Fatalf("precondition: a legacy dispatch on an untyped lane resolves to %q, want none", got)
	}
	r.specOf("T-1", "# T-1 — Hard\n\nDifficulty: hard\n")
	if reply, err := r.dispatchOf("T-1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-02 (queued) as opus effort high") {
		t.Fatalf("T-1 beside T-2's legacy dispatch on the blanked O-01: %q, %v; want O-02 as opus effort high", reply, err)
	}
}

// redoRig is the pool rig on serve's own lane table (checks.Lanes over the fixture's worktrees and the lanes table
// serve writes): T-1's dispatch ran opus:high on the untyped O-01 (prepared, so its lane row holds it with that type)
// and left a needs-you; O-01 has T-1's work committed (working on T-1).
func redoRig(t *testing.T) *loopRig {
	t.Helper()
	r := newPoolRig(t)
	r.lanes.mu.Lock()
	r.lanes.real = checks.Lanes{}
	r.lanes.mu.Unlock()
	r.p.Commit(r.lane)
	d, _ := json.Marshal(map[string]any{"lane": "O-01", "agent": "pj-o1", "branch": "task/T-1", "type": "opus:high",
		"level": "hard", "types": []string{"opus:high"}})
	prep, _ := json.Marshal(map[string]any{"base": "b"})
	stop, _ := json.Marshal(event.FactData{Lane: "O-01", Reason: "stopped: api error", Merge: true, Cause: "stopped"})
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult, Data: prep},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact, Data: stop}); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitFor("T-1's needs-you", func() bool { return r.taskStatus("T-1").State == "needs-you" })
	r.waitFor("O-01's row with T-1 as opus:high", func() bool {
		row, ok, _ := store.ReadLaneAt(r.p.Root, "O-01")
		return ok && row.Task == "T-1" && row.Type == "opus:high"
	})
	ls, err := checks.Lanes{}.Lanes(r.p.Root)
	if err != nil || len(ls) == 0 || ls[0].Name != "O-01" || ls[0].State != "working" || ls[0].Task != "T-1" {
		t.Fatalf("precondition: serve's lane table %+v, %v; want O-01 working on T-1", ls, err)
	}
	cfg, err := config.LoadDaemon(r.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if st := checks.NewLaneTable(cfg).States(); st[0].Type != "opus" {
		t.Fatalf("precondition: obor status's type of O-01 %q, want its dispatch's opus", st[0].Type)
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

// A redo by task ID through serve's real lane table keeps the full type reference its dispatch ran (opus:high), not
// the lane row's bare type, nor the spec's level (medium: sonnet).
func TestRedoViaTheRealLaneTableKeepsItsTypeAndEffort(t *testing.T) {
	t.Parallel()
	r := redoRig(t)
	if reply, err := r.dispatchOf("T-1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued) as opus effort high") {
		t.Fatalf("redo of T-1: %q, %v; want O-01 as opus effort high", reply, err)
	}
	if types := r.redoTypes(); len(types) != 2 || types[1] != "opus:high" {
		t.Errorf("T-1's dispatch facts' types %v, want the redo's opus:high after the first", types)
	}
}

// A redo with --types sonnet runs sonnet on the pool lane.
func TestRedoWithExplicitTypesRunsThem(t *testing.T) {
	t.Parallel()
	r := redoRig(t)
	reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-T-1", Task: "T-1",
		Types: []string{"sonnet"}})
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01 (queued) as sonnet") {
		t.Fatalf("redo of T-1 --types sonnet: %q, %v; want O-01 as sonnet", reply, err)
	}
	if types := r.redoTypes(); len(types) != 2 || types[1] != "sonnet" {
		t.Errorf("T-1's dispatch facts' types %v, want the redo's sonnet after the first", types)
	}
}

// --types naming no lane type of the project, bare or with an effort, is refused before a lane is picked: no
// dispatch fact (a bare one would match the free untyped lanes and stick at launch), nor, with --after, a waiting
// fact.
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
			if err == nil || !strings.Contains(err.Error(), `"lunna" is not a lane type of this project`) {
				t.Fatalf("dispatch --types %v: %q, %v; want refused naming lunna", c.types, reply, err)
			}
			for _, step := range []string{"fact dispatch", "fact waiting"} {
				if n := r.taskStep("T-1", step); n != 0 {
					t.Errorf("%s recorded for the refused dispatch: %q", step, r.steps())
				}
			}
			// The same project takes a known type: the refusal is the type's.
			if reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-T-1b",
				Task: "T-1", Types: []string{"sonnet"}, After: c.after}); err != nil ||
				!strings.HasPrefix(reply, "DISPATCHED T-1") && !strings.HasPrefix(reply, "WAITING T-1") {
				t.Errorf("dispatch --types sonnet: %q, %v; want it taken", reply, err)
			}
		})
	}
}
