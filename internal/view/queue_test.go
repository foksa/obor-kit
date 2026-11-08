package view

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// addFact inserts one fact of task with data d into db's log.
func addFact(t *testing.T, db *sql.DB, at time.Time, task, step string, d any) {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, ?, '', ?)`,
		at.UTC().Format(time.RFC3339), task, step, event.KindFact, string(raw)); err != nil {
		t.Fatal(err)
	}
}

// T-1016, T-1026: a spec stored and never dispatched, then dropped, is serve's dropped task (ended) until a later
// obor spec set revives it: the queue reads serve's tasks alone (no second reading of the log's drops), so it leaves the
// task out while serve lists it and lists it as never dispatched once serve no longer does.
func TestQueueSkipsADroppedSpecUntilANewRev(t *testing.T) {
	t.Parallel()
	db, err := store.OpenRW(store.File(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, task := range []string{"T-1", "T-2"} {
		if _, _, err := store.SetSpec(db, task, "# "+task+" — A\n\nDifficulty: medium\n", "", "lead", now); err != nil {
			t.Fatal(err)
		}
	}
	addFact(t, db, now, "T-1", event.StepDropped, event.FactData{Reason: "superseded on 11-07", Merge: true, Rev: 1})
	specs, dispatched := readSpecs(db), dispatchedTasks(db)
	if len(specs) != 2 || dispatched["T-1"] {
		t.Fatalf("specs %+v, dispatched %+v: want T-1 and T-2 stored, neither dispatched", specs, dispatched)
	}
	dropped := wire.ProjectStatus{Tasks: []wire.TaskStatus{{Task: "T-1", State: "dropped", Ended: true}}}
	q := queue(&Project{}, dropped, "", specs, dispatched, now)
	if len(q.Waiting) != 1 || q.Waiting[0].Task != "T-2" {
		t.Errorf("waiting %+v, want T-2 alone: serve lists T-1 dropped", q.Waiting)
	}
	// A later spec set revives it: serve lists it no more, and the queue lists it as never dispatched.
	q = queue(&Project{}, wire.ProjectStatus{}, "", specs, dispatched, now)
	if len(q.Waiting) != 2 || q.Waiting[0].Task != "T-1" || q.Waiting[0].WaitsOn != WaitsNever {
		t.Errorf("waiting %+v, want T-1 queued again (never dispatched) and T-2", q.Waiting)
	}
}
