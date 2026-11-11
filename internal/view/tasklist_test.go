package view

import (
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/store"
	"obor/internal/testkit"
)

// rowOfTask is rows' row for task (fatal: none).
func rowOfTask(t *testing.T, rows []TaskRow, task string) TaskRow {
	t.Helper()
	for _, r := range rows {
		if r.Task == task {
			return r
		}
	}
	t.Fatalf("%s is not in the list %+v", task, rows)
	return TaskRow{}
}

// dispatchFact inserts task's dispatch fact into p's log.
func dispatchFact(t *testing.T, p *testkit.Project, at time.Time, task string) {
	t.Helper()
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	addFact(t, db, at, task, event.StepDispatched, event.FactData{Lane: "L-01"})
}

// A spec dropped before any dispatch is dropped on the tasks list while the drop is of the current spec rev; a later
// obor spec set revives it, as serve's fold does (internal/app/specs.go foldSpecSet; its oracle,
// TestDropOfASpecOnlyTaskThenDispatchUntilANewRev), so the list shows it never dispatched and then its live state. A
// dispatched task's drop records no rev: it stays dropped whatever spec rev follows.
func TestTaskListDropOfASpecOnlyTaskFollowsTheSpecRev(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	p.Config()
	d := Detail{Root: p.Root}
	at := time.Now()

	storeSpec(t, p, "T-1", "a spec dropped at rev 1")
	dropFact(t, p, at, "T-1", 1)
	if r := rowOfTask(t, d.TaskList(), "T-1"); r.State != ListDropped || r.Rev != 1 {
		t.Errorf("drop at the spec's rev: T-1 %+v, want dropped at rev 1", r)
	}

	storeSpec(t, p, "T-1", "the same task at rev 2")
	if r := rowOfTask(t, d.TaskList(), "T-1"); r.State != ListNever || r.Rev != 2 {
		t.Errorf("drop at rev 1, spec rev 2: T-1 %+v, want never dispatched at rev 2", r)
	}

	// A dispatch after the revived spec: its live state, not the older drop.
	dispatchFact(t, p, at.Add(time.Minute), "T-1")
	if r := rowOfTask(t, d.TaskList(), "T-1"); r.State != ListRunning || r.Rev != 2 {
		t.Errorf("dispatched at rev 2: T-1 %+v, want running", r)
	}

	// A dispatched task's drop records no rev: a later spec rev does not revive it.
	dropFact(t, p, at.Add(2*time.Minute), "T-1", 0)
	storeSpec(t, p, "T-1", "the dispatched task at rev 3")
	if r := rowOfTask(t, d.TaskList(), "T-1"); r.State != ListDropped || r.Rev != 3 {
		t.Errorf("drop of the dispatched task, spec rev 3: T-1 %+v, want dropped", r)
	}
}
