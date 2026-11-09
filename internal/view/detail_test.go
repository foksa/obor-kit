package view

import (
	"context"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
	"obor/internal/testkit"
)

// T-675: a merged task's lane was freed (its row cleared, the worktree off its branch), yet its page still says where
// it ran: the lane and the agent of its newest dispatch. Its spec's text is on the page too.
func TestMergedTaskKeepsItsLaneAndAgent(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Spec("T-01", "# T-01 — a task title\n\nDifficulty: simple\n")
	p.Submit("L-01")
	row, ok := p.Row("L-01")
	if !ok || row.Task != "T-01" {
		t.Fatalf("precondition: the dispatch put T-01 on L-01: %+v", row)
	}
	p.MergedAt("T-01", "cafe1234", false, time.Now().Unix())
	p.ClearState("L-01")
	p.Git(o1, "checkout", "-q", "--detach")
	v, err := Load(context.Background(), p.Root, time.Now(), 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range v.Lanes {
		if l.Task == "T-01" {
			t.Fatalf("precondition: the lane is freed, but %s still names T-01", l.Name)
		}
	}
	d, ok := Detail{Root: p.Root, Project: v}.Task("T-01")
	if !ok {
		t.Fatal("the merged task has no page")
	}
	if d.Lane != "L-01" || d.Agent != "pj-l-01" {
		t.Errorf("merged task: lane %q, agent %q; want L-01 and pj-l-01 (its dispatch's)", d.Lane, d.Agent)
	}
	if d.Spec != "# T-01 — a task title\n\nDifficulty: simple\n" || d.Title != "a task title" {
		t.Errorf("spec %q, title %q; want the stored spec's text and title", d.Spec, d.Title)
	}
}

// T-1089: a task the nightly prune left with its merged fact alone: its page shows its outcome (serve's state) and its
// spec, with the one record and no error.
func TestPrunedTaskPage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	spec := "# T-01 — a task title\n\nDifficulty: simple\n"
	if _, _, err := store.SetSpec(db, "T-01", spec, "", "lead", time.Now()); err != nil {
		t.Fatal(err)
	}
	l, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(event.Record{Task: "T-01", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact,
		Data: []byte(`{"head":"cafe1234"}`)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	l.Close()
	v := Project{Tasks: map[string]Task{"T-01": {Task: "T-01", Title: "a task title", TaskState: "merged", Phase: wire.PhaseMerged}}}
	d, ok := Detail{Root: root, Project: v}.Task("T-01")
	if !ok {
		t.Fatal("the pruned task has no page")
	}
	if d.TaskState != "merged" || d.Spec != spec {
		t.Errorf("state %q, spec %q; want merged and the stored spec", d.TaskState, d.Spec)
	}
	if len(d.Records) != 1 || d.Records[0].Step != event.StepMerged {
		t.Errorf("records %+v, want the merged fact alone", d.Records)
	}
}
