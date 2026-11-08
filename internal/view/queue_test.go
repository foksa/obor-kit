package view

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
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

// queueOf is view.Build of p's project with serve's answer ss (err: serve gave none), as obor status and obor web build
// it.
func queueOf(t *testing.T, p *testkit.Project, ss wire.Status, err error, now time.Time) Project {
	t.Helper()
	cfg, cerr := config.Load(p.Root)
	if cerr != nil {
		t.Fatal(cerr)
	}
	db, oerr := store.OpenRO(store.File(p.Root))
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer db.Close()
	return Build(Input{Status: ss, Err: err, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: now})
}

// storeSpec is p's task spec at the next rev, as obor spec set stores it.
func storeSpec(t *testing.T, p *testkit.Project, task, title string) {
	t.Helper()
	p.Spec(task, "# "+task+" — "+title+"\n\nDifficulty: simple\n")
}

func dropFact(t *testing.T, p *testkit.Project, at time.Time, task string, rev int) {
	t.Helper()
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	addFact(t, db, at, task, event.StepDropped, event.FactData{Reason: "superseded", Merge: true, Rev: rev})
}

// T-1028 (RV-143 verify #1): a spec dropped before any dispatch is serve's task outcome alone (T-1026, its dropped task
// status), so without serve's status for this project — down, up with no project, or answering for another one — the
// queue cannot tell the dropped spec from one never dispatched and lists neither; it never lists T-1 as a drop that did
// not happen.
func TestQueueListsNoDroppedSpecWithoutServeStatus(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	p.Config()
	now := time.Now()
	storeSpec(t, p, "T-1", "a spec dropped at rev 1")
	dropFact(t, p, now, "T-1", 1)
	for _, c := range []struct {
		name string
		ss   wire.Status
		err  error
	}{
		{name: "serve down", err: errors.New("obor serve is not running")},
		{name: "serve up, no project", ss: wire.Status{Connected: true}},
		{name: "another project", ss: wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "other",
			Root: "/elsewhere"}}},
	} {
		v := queueOf(t, p, c.ss, c.err, now)
		if v.Serve.Project {
			t.Fatalf("%s: precondition: serve has this project: %+v", c.name, v.Serve)
		}
		if len(v.Queue.Waiting) != 0 {
			t.Errorf("%s: waiting %+v, want none: a stored-only drop is unknown without serve's status", c.name,
				v.Queue.Waiting)
		}
	}
}

// T-1028: the accepted trade-off — without serve's status a valid spec that was never dispatched is not listed either
// (the view shows nothing it cannot know).
func TestQueueListsNoNeverDispatchedSpecWithoutServeStatus(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	p.Config()
	now := time.Now()
	storeSpec(t, p, "T-1", "a spec never dispatched")
	v := queueOf(t, p, wire.Status{}, errors.New("obor serve is not running"), now)
	if v.Serve.Project {
		t.Fatalf("precondition: serve has no status: %+v", v.Serve)
	}
	if len(v.Queue.Waiting) != 0 {
		t.Errorf("waiting %+v, want none: serve is down, so a spec's dispatch is unknown", v.Queue.Waiting)
	}
}

// T-1016, T-1026, T-1028: with serve's status a stored spec not in it is never dispatched — unless serve lists it as
// dropped (ended), which hides it until a later obor spec set revives it; a valid spec is listed as never dispatched.
func TestQueueListsStoredSpecsWithServeStatus(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	p.Config()
	now := time.Now()
	storeSpec(t, p, "T-1", "a dropped spec")
	storeSpec(t, p, "T-2", "a spec never dispatched")
	dropFact(t, p, now, "T-1", 1)
	status := func(tasks ...wire.TaskStatus) wire.Status {
		return wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "proj", Root: p.Root, Tasks: tasks}}
	}

	dropped := queueOf(t, p, status(wire.TaskStatus{Task: "T-1", State: "dropped", Ended: true}), nil, now)
	if !dropped.Serve.Project {
		t.Fatalf("precondition: serve has this project: %+v", dropped.Serve)
	}
	if len(dropped.Queue.Waiting) != 1 || dropped.Queue.Waiting[0].Task != "T-2" ||
		dropped.Queue.Waiting[0].WaitsOn != WaitsNever {
		t.Errorf("waiting %+v, want T-2 alone as never dispatched: serve lists T-1 dropped", dropped.Queue.Waiting)
	}

	// A later spec set is the next rev: serve lists T-1 no more, and the queue lists it as never dispatched.
	storeSpec(t, p, "T-1", "a dropped spec, widened")
	revived := queueOf(t, p, status(), nil, now)
	if len(revived.Queue.Waiting) != 2 || revived.Queue.Waiting[0].Task != "T-1" ||
		revived.Queue.Waiting[0].WaitsOn != WaitsNever || revived.Queue.Waiting[1].Task != "T-2" {
		t.Errorf("waiting %+v, want T-1 queued again (never dispatched) and T-2", revived.Queue.Waiting)
	}
}
