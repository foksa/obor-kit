package view

import (
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// TestViewLaneFreeByItsLandedRow: the view's Free and state column come from the one free rule over the
// lane's row: a lane whose task landed shows free though its commit is not in the main checkout's history; a lane whose
// row holds a task that has not landed shows working though its HEAD is in main's history; landed with uncommitted
// changes shows dirty-free, still free for the view (not idle for dispatch).
func TestViewLaneFreeByItsLandedRow(t *testing.T) {
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "task/T-01")
	p.PlainLane("L-02", "task/T-02")
	l3 := p.PlainLane("L-03", "task/T-03")
	p.Config()
	// git's facts: L-01's HEAD is its landed head, not in main's history; L-02's HEAD is in main's history; L-03 is at
	// its landed head with an uncommitted file.
	lanes := map[string]laneFacts{"L-01": {Branch: "task/T-01", Head: "1111"},
		"L-02": {Branch: "task/T-02", Head: "2222", Merged: true},
		"L-03": {Branch: "task/T-03", Head: "3333", Dirty: []string{"scratch.txt"}}}
	for _, lane := range []string{"L-01", "L-02", "L-03"} {
		p.Dispatched(lane)
	}
	p.State("L-01", map[string]any{"task": "T-01", "branch": "task/T-01", "landed": true, "head": "1111"})
	p.State("L-03", map[string]any{"task": "T-03", "branch": "task/T-03", "landed": true, "head": "3333"})
	p.Write(l3+"/scratch.txt", "x")
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := Build(Input{Status: wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root}},
		Lanes: laneTable(p, cfg, lanes), DB: db, Now: time.Now()})
	if len(v.Lanes) != 3 {
		t.Fatalf("precondition: three lanes, got %+v", v.Lanes)
	}
	want := map[string]struct {
		column string
		free   bool
	}{"L-01": {"free", true}, "L-02": {"working", false}, "L-03": {"dirty-free", true}}
	for _, l := range v.Lanes {
		if w := want[l.Name]; l.State != w.column || l.Column != w.column || l.Free != w.free {
			t.Errorf("%s: state %q, column %q, free %v; want %q, free %v", l.Name, l.State, l.Column, l.Free,
				w.column, w.free)
		}
	}
}
