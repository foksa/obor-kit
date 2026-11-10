package view

import (
	"context"
	"errors"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// TestMain isolates the tests and removes the project template New copies.
func TestMain(m *testing.M) { testkit.Main(m) }

// Load builds one project's view as obor status does — serve down is its error in the view, not an error — from
// the lane table and the store alone (the lane's spec rev and its events are there).
func TestLoadWithoutServe(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01")
	p.Spec("T-01", "# T-01 — a task title\n\nDifficulty: simple\n")
	v, err := Load(context.Background(), p.Root, time.Now(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if v.Serve.Up || v.Serve.Error == "" {
		t.Errorf("serve: up %v, error %q; want down with its error", v.Serve.Up, v.Serve.Error)
	}
	if len(v.Lanes) != 1 || v.Lanes[0].Name != "L-01" || v.Lanes[0].Task != "T-01" {
		t.Fatalf("lanes %+v, want L-01 on T-01", v.Lanes)
	}
	if tk := v.Tasks["T-01"]; tk.SpecRev != 1 || tk.Lane != "L-01" {
		t.Errorf("the lane's task %+v, want spec rev 1 (the store's) on L-01", tk)
	}
	if len(v.Events) == 0 || v.Events[len(v.Events)-1].Line == "" {
		t.Errorf("events %+v, want the store's worded records", v.Events)
	}
}

// the task and agent pages' reads spawn no git and no ps; the overview runs both. The injected runners count the
// spawns and the injected serve answer makes the overview's background scan reach ps.
func TestNarrowReadsSpawnNoProcesses(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01")
	p.Spec("T-01", "# T-01 — a task title\n\nDifficulty: simple\n")
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
			Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l-01", Lane: "L-01", Status: "working"}},
			Tasks:  []wire.TaskStatus{{Task: "T-01", Lane: "L-01", State: "working"}},
			Facts: []event.Record{{Seq: 1, Task: "T-01", Step: event.StepNeedsYou, Kind: event.KindFact,
				Data: []byte(`{"lane":"L-01","reason":"asks: may I edit x.go?"}`)}},
			Answers: map[int64]string{1: `obor resume T-01 "<answer>"`}}}
	var git, ps int
	r := reading{
		ask:   func(context.Context, string) (wire.Status, error) { return ss, nil },
		git:   func(context.Context, string, ...string) (string, error) { git++; return "", errors.New("no git") },
		procs: func(context.Context) ([]Proc, error) { ps++; return nil, errors.New("no ps") },
	}
	reads := func(name string, fn func() error) (int, int) {
		g, q := git, ps
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return git - g, ps - q
	}

	// the task page: serve's task on its lane and the task's spec, from the store's rows alone
	if g, q := reads("task", func() error {
		d, err := r.task(context.Background(), p.Root)
		if err != nil {
			return err
		}
		if tk := d.Project.Tasks["T-01"]; tk.Lane != "L-01" || tk.SpecRev != 1 || tk.TaskState != "working" {
			t.Errorf("task read: %+v, want T-01 on L-01 at spec rev 1, working", tk)
		}
		if n := d.Project.NeedsYou; len(n) != 1 || n[0].Reason != "asks: may I edit x.go?" ||
			n[0].Answer != `obor resume T-01 "<answer>"` {
			t.Errorf("task read: needs-you %+v, want the ask with its answer", n)
		}
		return nil
	}); g != 0 || q != 0 {
		t.Errorf("task read spawned %d git, %d ps; want none", g, q)
	}

	// the agent page: serve's agent, from the status answer alone
	if g, q := reads("agent", func() error {
		d, err := r.agent(context.Background(), p.Root)
		if err != nil {
			return err
		}
		if len(d.Project.Agents) != 1 || d.Project.Agents[0].Name != "pj-l-01" {
			t.Errorf("agent read: agents %+v, want serve's pj-l-01", d.Project.Agents)
		}
		return nil
	}); g != 0 || q != 0 {
		t.Errorf("agent read spawned %d git, %d ps; want none", g, q)
	}

	// the setup page: the lane table's git (a lane's state), and still no ps
	if g, q := reads("setup", func() error {
		_, err := r.setup(context.Background(), p.Root)
		return err
	}); g == 0 || q != 0 {
		t.Errorf("setup read spawned %d git, %d ps; want git and no ps", g, q)
	}

	// the overview: both
	if g, q := reads("overview", func() error {
		_, err := r.load(context.Background(), p.Root, now, 5)
		return err
	}); g == 0 || q == 0 {
		t.Errorf("overview spawned %d git, %d ps; want both", g, q)
	}
}

// serve down is no error for the pages either, as for Load: the rows and the store answer, serve's error is the
// view's (the endpoints still answer as today).
func TestNarrowReadsWithoutServe(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01")
	p.Spec("T-01", "# T-01 — a task title\n\nDifficulty: simple\n")
	d, err := LoadTask(context.Background(), p.Root)
	if err != nil || d.Project.Serve.Up || d.Project.Serve.Error == "" {
		t.Fatalf("task read: %v, serve up %v, error %q; want the store's rows and serve's error",
			err, d.Project.Serve.Up, d.Project.Serve.Error)
	}
	if tk := d.Project.Tasks["T-01"]; tk.Lane != "L-01" || tk.SpecRev != 1 {
		t.Errorf("task read: %+v, want T-01 on L-01 at spec rev 1", tk)
	}
	d, err = LoadAgent(context.Background(), p.Root)
	if err != nil || d.Project.Serve.Error == "" || len(d.Project.Roles) != 0 {
		t.Errorf("agent read: %v, serve error %q, %d roles; want serve's error and no stale roles",
			err, d.Project.Serve.Error, len(d.Project.Roles))
	}
	s, err := LoadSetup(context.Background(), p.Root)
	if err != nil || len(s.Lanes) != 1 || s.Lanes[0].Lane != "L-01" {
		t.Errorf("setup read: %v, lanes %+v; want L-01", err, s.Lanes)
	}
}
