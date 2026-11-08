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

// T-1016: a drop of a spec stored and never dispatched records the spec's rev with its why, and the queue leaves the
// task out until a later obor spec set (a new rev) revives it; a drop of a task the merge loop held records no rev and
// hides nothing.
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
	if _, _, err := store.SetSpec(db, "T-3", "# T-3 — B\n\nDifficulty: medium\n", "", "lead", now); err != nil {
		t.Fatal(err)
	}
	addFact(t, db, now, "T-1", event.StepDropped, event.FactData{Reason: "superseded on 11-07", Merge: true, Rev: 1})
	addFact(t, db, now, "T-3", event.StepDropped, event.FactData{Reason: "abandoned", Merge: true}) // a loop drop: rev 0
	if got := droppedSpecs(db); len(got) != 1 || got["T-1"] != 1 {
		t.Fatalf("droppedSpecs %+v, want T-1 at rev 1 alone", got)
	}
	specs, dispatched := readSpecs(db), dispatchedTasks(db)
	q := queue(&Project{}, wire.ProjectStatus{}, "", specs, dispatched, droppedSpecs(db), now)
	if len(q.Waiting) != 2 || q.Waiting[0].Task != "T-2" || q.Waiting[1].Task != "T-3" {
		t.Errorf("waiting %+v, want T-2 and T-3: T-1 dropped at its rev, T-3's loop drop hides nothing", q.Waiting)
	}
	// A later spec set is the next rev: the task is queued again.
	if _, _, err := store.SetSpec(db, "T-1", "# T-1 — A, widened\n\nDifficulty: medium\n", "", "lead", now); err != nil {
		t.Fatal(err)
	}
	q = queue(&Project{}, wire.ProjectStatus{}, "", readSpecs(db), dispatched, droppedSpecs(db), now)
	if len(q.Waiting) != 3 || q.Waiting[0].Task != "T-1" || q.Waiting[0].WaitsOn != WaitsNever {
		t.Errorf("waiting %+v, want T-1 queued again after rev 2, T-2 and T-3", q.Waiting)
	}
}
