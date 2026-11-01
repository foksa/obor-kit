package view

import (
	"context"
	"testing"
	"time"

	"obor/internal/testkit"
)

// T-667: Load builds one project's view as obor status does — serve down is its error in the view, not an error — from
// the lane table and the store alone (the lane's spec rev and its events are there).
func TestLoadWithoutServe(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Dispatched("O-01")
	p.Spec("T-01", "# T-01 — a task title\n\nDifficulty: simple\n")
	v, err := Load(context.Background(), p.Root, time.Now(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if v.Serve.Up || v.Serve.Error == "" {
		t.Errorf("serve: up %v, error %q; want down with its error", v.Serve.Up, v.Serve.Error)
	}
	if len(v.Lanes) != 1 || v.Lanes[0].Lane != "O-01" || *v.Lanes[0].Task != "T-01" {
		t.Fatalf("lanes %+v, want O-01 on T-01", v.Lanes)
	}
	if v.Lanes[0].SpecRev != 1 {
		t.Errorf("lane spec rev %d, want the store's 1", v.Lanes[0].SpecRev)
	}
	if len(v.Events) == 0 || v.Events[len(v.Events)-1].Line == "" {
		t.Errorf("events %+v, want the store's worded records", v.Events)
	}
}
