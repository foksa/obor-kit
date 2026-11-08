package view

import (
	"context"
	"slices"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// T-1031: the view's lane says its mode (keep) and its queue (serve's Queues, in dispatch order), its agent the lane's
// derived name with no lane row; a task queued on it is a waiting item naming that lane; a fresh lane says neither.
func TestViewKeepLaneModeAndQueue(t *testing.T) {
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "")
	p.Config(testkit.Set("lanes", `[{"name": "L-01", "keep_agent": true}, {"name": "L-02"}]`))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.KeepLane("L-01") || cfg.KeepLane("L-02") {
		t.Fatalf("precondition: L-01 keep, L-02 fresh: %+v", cfg.Lanes())
	}
	if rows, err := store.ReadLanesAt(p.Root); err != nil || len(rows) != 0 {
		t.Fatalf("precondition: no lane rows: %+v, %v", rows, err)
	}
	db, err := store.OpenRW(store.File(p.Root)) // the store exists, empty
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ps := &wire.ProjectStatus{Name: "pj", Root: p.Root, Queues: map[string][]string{"L-01": {"T-2", "T-3"}},
		Tasks: []wire.TaskStatus{
			{Task: "T-2", Attempt: 1, State: "waiting", Waiting: true, Rank: 1, Queue: "L-01",
				Why: "behind T-1 on keep lane L-01"},
			{Task: "T-3", Attempt: 1, State: "waiting", Waiting: true, Rank: 2, Queue: "L-01",
				Why: "behind T-2 on keep lane L-01"}}}
	v := Build(Input{Status: wire.Status{Connected: true, Project: ps}, Lanes: checks.NewLaneTable(context.Background(), cfg),
		DB: db, Now: time.Now()})
	if len(v.Lanes) != 2 {
		t.Fatalf("lanes %+v, want L-01 and L-02", v.Lanes)
	}
	keep, fresh := v.Lanes[0], v.Lanes[1]
	if keep.Mode != config.LaneKeep || !slices.Equal(keep.Queue, []string{"T-2", "T-3"}) || keep.Agent != "pj-l-01" {
		t.Errorf("L-01 mode %q, queue %q, agent %q; want keep, T-2 T-3, pj-l-01", keep.Mode, keep.Queue, keep.Agent)
	}
	if fresh.Mode != "" || len(fresh.Queue) != 0 || fresh.Agent != "pj-l-02" {
		t.Errorf("L-02 mode %q, queue %q, agent %q; want no mode, no queue, pj-l-02", fresh.Mode, fresh.Queue, fresh.Agent)
	}
	var queued []string
	for _, w := range v.Queue.Waiting {
		if w.WaitsOn == WaitsLane && w.Queue == "L-01" {
			queued = append(queued, w.Task+": "+w.Why)
		}
	}
	slices.Sort(queued)
	if want := []string{"T-2: behind T-1 on keep lane L-01", "T-3: behind T-2 on keep lane L-01"}; !slices.Equal(queued, want) {
		t.Errorf("waiting %q, want %q", queued, want)
	}
}
