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

// The fold's half: serve's replay records a task's submission (the lane commit the gate merges) and its merge in the
// lanes table, and the row reads landed at the submitted head, not at the merge's. What the lane table then makes of
// that head (free at it, working after a further commit, so a dispatch does not pick it) is its own, in internal/checks'
// lane table tests. The project is a plain dir (testkit.NewDir): the app package's tests run no git process.
func TestFoldPinsTheLandedRowAtTheSubmittedHead(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		p := testkit.NewDir(t)
		lane := p.Lane("L-01", "task/T-1")
		p.Config(testkit.Set("agents.roles.integrator", ""))
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
	})
}

// The lanes projection holds configured lanes only. The load's replay rebuilds a row of a lane the config does not name
// (D-01) from its dispatch records, and serve drops it; the configured lane's row stays with its model, the row launch
// starts a lane's worker at. (Status and the setup view read the row's model over git: internal/checks' TestLaneStates,
// internal/view's TestSetup.)
func TestLaneRowsHoldConfiguredLanesOnly(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, withConfig(testkit.Set("lanes.count", 2)))
		r.p.Lane("L-02", "") // the second configured lane: free
		prep, _ := json.Marshal(map[string]any{"base": "b"})
		d01, _ := json.Marshal(map[string]any{"lane": "D-01", "agent": "pj-l-01", "branch": "task/T-9", "type": "deepseek"})
		l01, _ := json.Marshal(map[string]any{"lane": "L-01", "agent": "pj-l-01", "branch": "task/T-1", "type": "opus:high"})
		sub, _ := json.Marshal(map[string]any{"lane": "D-01", "agent": "pj-l-01", "report": "Summary: x"})
		merged, _ := json.Marshal(event.MergedData{})
		stop, _ := json.Marshal(event.FactData{Lane: "L-01", Reason: "stopped: api error", Merge: true, Cause: "stopped"})
		if err := app.WriteLog(r.p.Root, time.Now(),
			// D-01's dispatch, prepared and merged: the log a load replays holds its row
			event.Record{Task: "T-9", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d01},
			event.Record{Task: "T-9", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult, Data: prep},
			event.Record{Task: "T-9", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: sub},
			event.Record{Task: "T-9", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact, Data: merged},
			// L-01's dispatch, prepared, its worker stopped (the row stays, its model shows)
			event.Record{Task: "T-1", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: l01},
			event.Record{Task: "T-1", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult, Data: prep},
			event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact, Data: stop}); err != nil {
			t.Fatal(err)
		}
		// The table also holds the row of a lane the config does not name
		db, err := store.OpenRW(event.DBFile(r.p.Root))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.WriteLanes(db, []store.LaneRow{{Lane: "D-01", Task: "T-9", Branch: "task/T-9", Type: "deepseek"},
			{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Type: "opus:high"}}); err != nil {
			t.Fatal(err)
		}
		db.Close()
		r.start()
		r.waitFor("L-01's row", func() bool {
			row, ok, _ := store.ReadLaneAt(r.p.Root, "L-01")
			return ok && row.Task == "T-1" && row.Type == "opus:high"
		})
		rows, err := store.ReadLanesAt(r.p.Root)
		if err != nil {
			t.Fatal(err)
		}
		if row, ok := rows["D-01"]; ok {
			t.Errorf("the row of D-01, a lane the config does not name, is still in the table: %+v", row)
		}
		if len(rows) != 1 {
			t.Errorf("the table %+v: want the configured lane L-01's row only", rows)
		}
		// launch: the row launch.dispatchType starts a lane's worker at (TestStartEffort covers the start itself)
		if row, ok, err := store.ReadLaneAt(r.p.Root, "L-01"); err != nil || !ok || row.Type != "opus:high" {
			t.Errorf("launch's lane row: %+v, %v, %v; want L-01's opus:high", row, ok, err)
		}
	})
}
