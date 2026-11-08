package view

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-969: while a task's lane test runs, the lane is detached at the candidate (checks.Scratch.Test) and git's HEAD names
// no branch: the lane serve owns still shows its task, the dispatch's branch and the phase testing. After the test's
// restore git is the source as before; a detached lane serve does not own still reads as git says.
func TestLaneTestingShowsItsTaskAndBranch(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "") // a detached lane serve does not own
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01")
	// A stale row: its task is not open (dropped: landed, T-1012).
	p.State("L-02", map[string]any{"task": "T-02", "branch": "task/T-02", "landed": true})
	p.Git(o1, "checkout", "-q", "--detach", "main") // the lane test's checkout: detached, off the branch
	p.Commit(o1, "candidate")
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "proj", Root: p.Root,
			Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "candidate",
				Phase: wire.PhaseTesting, Stage: wire.StageGating, Running: event.StepTest},
				{Task: "T-02", Lane: "L-02", State: "dropped", Ended: true, Phase: wire.PhaseDropped}},
			Running: []wire.RunningStep{{Step: event.StepTest, Task: "T-01", Since: now.Add(-time.Minute).Unix()}}}}
	read := func() Project {
		t.Helper()
		v, err := reading{ask: func(context.Context, string) (wire.Status, error) { return ss, nil }}.load(
			context.Background(), p.Root, now, 5)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := read()
	if len(v.Lanes) != 2 {
		t.Fatalf("lanes %+v, want L-01 and L-02", v.Lanes)
	}
	l := v.Lanes[0]
	if l.State != "working" || l.Task != "T-01" || l.Branch != "task/T-01" || l.Column != wire.PhaseTesting {
		t.Errorf("detached testing lane %+v; want working, task T-01, branch task/T-01, column testing", l)
	}
	if tk := v.TaskOn(l); tk.Task != "T-01" || tk.TaskState != "candidate" || tk.Phase != wire.PhaseTesting {
		t.Errorf("TaskOn = %+v; want serve's candidate T-01 testing", tk)
	}
	// obor status --json (and the web, which serves the same view) carries the row: branch, task and column.
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var js struct {
		Branch *string `json:"branch"`
		Task   *string `json:"task"`
		Column string  `json:"column"`
	}
	if err := json.Unmarshal(b, &js); err != nil {
		t.Fatal(err)
	}
	if js.Branch == nil || *js.Branch != "task/T-01" || js.Task == nil || *js.Task != "T-01" || js.Column != wire.PhaseTesting {
		t.Errorf("--json lane %s; want branch task/T-01, task T-01, column testing", b)
	}
	if l2 := v.Lanes[1]; l2.Task != "" || l2.Branch != "" || l2.Column != "free" {
		t.Errorf("unowned detached lane %+v; want no task, no branch, free (git's reading of the branch; its row's task landed)", l2)
	}

	// After the test's restore the lane is back on its branch and git names the task, as before T-969.
	p.Git(o1, "checkout", "-q", "task/T-01")
	v = read()
	if l := v.Lanes[0]; l.State != "working" || l.Task != "T-01" || l.Branch != "task/T-01" || l.Column != wire.PhaseTesting {
		t.Errorf("restored testing lane %+v; want working, task T-01, branch task/T-01, column testing", l)
	}
}
