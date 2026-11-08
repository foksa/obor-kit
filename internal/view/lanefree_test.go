package view

import (
	"context"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// TestViewLaneFreeByItsLandedRow (T-1012): the view's Free and state column come from the one free rule over the
// lane's row: a lane whose task landed shows free though its commit is not in the main checkout's history; a lane whose
// row holds a task that has not landed shows working though its HEAD is in main's history; landed with uncommitted
// changes shows dirty-free, still free for the view (not idle for dispatch).
func TestViewLaneFreeByItsLandedRow(t *testing.T) {
	p := testkit.New(t)
	l1 := p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "task/T-02")
	l3 := p.Lane("L-03", "task/T-03")
	p.Config()
	p.Commit(l1)
	p.Dispatched("L-01")
	p.Landed("L-01")
	p.Dispatched("L-02")
	p.Dispatched("L-03")
	p.Landed("L-03")
	p.Write(l3+"/scratch.txt", "x")
	if out := p.Git(p.Root, "branch", "--contains", "task/T-01", "main"); out != "" {
		t.Fatalf("precondition: L-01's commit is not on main (%q)", out)
	}
	if p.Git(p.Root, "merge-base", "task/T-02", "main") != p.Git(p.Root, "rev-parse", "task/T-02") {
		t.Fatal("precondition: L-02's HEAD is in main's history")
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
	v := Build(Input{Status: wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root}},
		Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: time.Now()})
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
