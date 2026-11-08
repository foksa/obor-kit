package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/store"
	"obor/internal/testkit"
)

// T-1015 (RV-141 #1), the fold's half: serve's replay records a task's submission (the lane commit the gate merges) and
// its merge in the lanes table, and the row reads landed at the submitted head, not at the merge's. What the lane table
// then makes of that head (free at it, working after a further commit, so a dispatch does not pick it) is its own, in
// internal/checks' lane table tests (T-1015). The project is a plain dir (testkit.NewDir): the app package's tests run
// no git process (T-1024).
func TestFoldPinsTheLandedRowAtTheSubmittedHead(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	lane := p.Lane("L-01", "task/T-1")
	p.Config(testkit.Set("roles.integrator", ""))
	now := time.Now()
	const submitted, merged = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	rec := func(step, kind string, d any) event.Record {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return event.Record{Task: "T-1", Attempt: 1, Step: step, Kind: kind, Data: raw}
	}
	submit := event.StepSubmit // submit.1's step name
	if err := app.WriteLog(p.Root, now,
		rec(event.StepDispatched, event.KindFact, map[string]any{"lane": "L-01", "dir": lane, "agent": "pj-l-01",
			"branch": "task/T-1", "target": "main"}),
		rec(event.StepPrepare, event.KindIntent, port.PrepareInput{}),
		rec(event.StepPrepare, event.KindResult, map[string]any{"base": "b1", "head": "b1"}),
		rec(event.StepSubmitted, event.KindFact, map[string]any{"lane": "L-01", "agent": "pj-l-01", "report": "r"}),
		rec(submit, event.KindIntent, map[string]any{"lane": "L-01", "agent": "pj-l-01"}),
		rec(submit, event.KindResult, map[string]any{"sha": submitted}),
		rec(event.StepMerged, event.KindFact, event.MergedData{Head: merged}),
	); err != nil {
		t.Fatal(err)
	}
	app.ReplayLanes(p.Root, now)
	row, ok, err := store.ReadLaneAt(p.Root, "L-01")
	if err != nil || !ok {
		t.Fatalf("L-01's row %+v, present %v, err %v; want the replayed row", row, ok, err)
	}
	if row.Task != "T-1" || !row.Landed {
		t.Fatalf("precondition: L-01's row %+v; want T-1 landed", row)
	}
	if row.Head != submitted {
		t.Errorf("L-01's head %q, want the submitted commit %q (not the merge's %q): the lane's free state is read at "+
			"the commit the gate merged", row.Head, submitted, merged)
	}
}
