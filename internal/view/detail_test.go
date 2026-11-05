package view

import (
	"context"
	"testing"
	"time"

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
	if !ok || row.Agent == "" {
		t.Fatalf("precondition: the dispatch recorded an agent on L-01: %+v", row)
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
	if d.Lane != "L-01" || d.Agent != row.Agent {
		t.Errorf("merged task: lane %q, agent %q; want L-01 and %q (its dispatch's)", d.Lane, d.Agent, row.Agent)
	}
	if d.Spec != "# T-01 — a task title\n\nDifficulty: simple\n" || d.Title != "a task title" {
		t.Errorf("spec %q, title %q; want the stored spec's text and title", d.Spec, d.Title)
	}
}
