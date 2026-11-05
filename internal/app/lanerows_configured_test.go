package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
	"obor/internal/view"
	"testing"
	"time"
)

// T-865: the lanes projection holds configured lanes only. The load's replay rebuilds a row of a lane the config no
// longer names (D-01, removed by T-765) from its old dispatch records, and serve drops it; the configured lane's row
// stays and reads the same in status, the setup view and launch (which starts a lane's worker at the row's model).
func TestLaneRowsHoldConfiguredLanesOnly(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass", testkit.Set("lanes", 2))
	r.p.Lane("L-02", "") // the second configured lane: free
	r.p.Commit(r.lane)   // L-01 works on T-1: its row's model shows in status
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
	// status: the configured lane's model is its row's (checks.LaneTable, as obor status reads it)
	cfg, err := config.LoadDaemon(r.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, s := range checks.NewLaneTable(context.Background(), cfg).States() {
		states[s.Name] = s.Type
	}
	if states["L-01"] != "opus" || states["L-02"] != "" {
		t.Errorf("status: lane models %+v, want L-01's row model opus and the free L-02's none", states)
	}
	// the setup view: the same row
	ro, err := store.OpenRO(store.File(r.p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	setup, err := view.Detail{Root: r.p.Root, Project: view.Build(view.Input{Lanes: checks.NewLaneTable(context.Background(), cfg), DB: ro,
		Now: time.Now(), Events: 20})}.Setup()
	if err != nil {
		t.Fatal(err)
	}
	lanes := map[string]string{}
	for _, l := range setup.Lanes {
		lanes[l.Lane] = l.Type
	}
	if len(lanes) != 2 || lanes["L-01"] != "opus" {
		t.Errorf("setup: lane models %+v, want the two configured lanes, L-01's row model opus", lanes)
	}
	// launch: the row launch.dispatchType starts a lane's worker at (TestStartEffort covers the start itself)
	if row, ok, err := store.ReadLaneAt(r.p.Root, "L-01"); err != nil || !ok || row.Type != "opus:high" {
		t.Errorf("launch's lane row: %+v, %v, %v; want L-01's opus:high", row, ok, err)
	}
}
