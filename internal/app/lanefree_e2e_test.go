package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
	"obor/internal/view"
)

// T-1015 (RV-141 #1), end to end: serve's fold records a task's submission (the pinned lane commit) and its merge in
// the lanes table; the lane reads free while its HEAD is that commit, and once a further clean commit is made in the
// lane (a driver after obor land) obor status's lane table shows it working and a dispatch does not pick it.
func TestLandedLaneWithALaterCommitStaysWorking(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-1")
	p.Config(testkit.Set("roles.integrator", ""))
	p.Commit(lane)
	now := time.Now()
	head := p.Git(lane, "rev-parse", "HEAD")
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
		rec(submit, event.KindResult, map[string]any{"sha": head}),
		rec(event.StepMerged, event.KindFact, event.MergedData{Head: "m1"}),
	); err != nil {
		t.Fatal(err)
	}
	app.ReplayLanes(p.Root, now)
	row, ok, err := store.ReadLaneAt(p.Root, "L-01")
	if err != nil || !ok || row.Task != "T-1" || !row.Landed || row.Head != head {
		t.Fatalf("precondition: L-01's row %+v, %v, %v; want T-1 landed at the submitted head %s", row, ok, err, head)
	}

	// laneState is L-01 as dispatch reads it (serve's lanes source) and as obor status shows it.
	laneState := func() (port.Lane, string) {
		t.Helper()
		ls, err := checks.Lanes{}.Lanes(context.Background(), p.Root)
		if err != nil || len(ls) != 1 {
			t.Fatalf("lanes %+v, %v", ls, err)
		}
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.OpenRO(store.File(p.Root))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		v := view.Build(view.Input{Status: wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj",
			Root: p.Root}}, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: now})
		if len(v.Lanes) != 1 {
			t.Fatalf("status lanes %+v, want L-01", v.Lanes)
		}
		return ls[0], v.Lanes[0].Column
	}
	call := wire.Call{Task: "T-9", Types: []string{"sonnet"}}
	pick := func() string {
		ls, _ := laneState()
		l, _, _ := app.PickLane([]port.Lane{ls}, call, map[string]string{}, map[string]string{}, "", false)
		return l.Name
	}
	if l, column := laneState(); l.State != port.LaneFree || column != "free" {
		t.Fatalf("precondition: at the submitted head L-01 is %s, status column %q; want free", l.State, column)
	}
	if got := pick(); got != "L-01" {
		t.Fatalf("precondition: a dispatch at the submitted head picks %q, want L-01", got)
	}

	p.Git(lane, "commit", "-q", "--allow-empty", "-m", "after the land")
	if p.Git(lane, "status", "--porcelain") != "" {
		t.Fatal("precondition: the lane is clean after its further commit")
	}
	if l, column := laneState(); l.State != port.LaneWorking || column != "working" || l.Task != "T-1" {
		t.Errorf("after a further commit L-01 is %s (task %q), status column %q; want working on T-1", l.State, l.Task,
			column)
	}
	if got := pick(); got != "" {
		t.Errorf("after a further commit a dispatch picks %q, want no lane", got)
	}
}
