package app_test

import (
	"encoding/json"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/store"
	"obor/internal/testkit"
	"testing"
	"time"
)

// T-865: the lanes projection holds configured lanes only. The load's replay rebuilds a row of a lane the config no
// longer names (D-01, removed by T-765) from its old dispatch records, and serve drops it; the configured lane's row
// stays with its model, the row launch starts a lane's worker at. (Status and the setup view read the row's model
// over git: internal/checks' TestLaneStates, internal/view's TestSetup.)
func TestLaneRowsHoldConfiguredLanesOnly(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, withConfig(testkit.Set("lanes", 2)))
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
	// an older serve's table held the removed lane's row too
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
}
