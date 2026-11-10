package view

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/launch"
)

// a lane whose restart lock is held has op restart (obor status); a lock file nobody holds (its restart
// was killed) is no op.
func TestRestartOpIsTheHeldRestartLock(t *testing.T) {
	root := t.TempDir()
	lock := launch.RestartLock(root, "L-01", "")
	if ok, err := lock.Try(); !ok || err != nil {
		t.Fatalf("Try = %v, %v", ok, err)
	}
	if got := restartOp(root, "L-01"); got != "restart" {
		t.Errorf("held: op = %q, want restart", got)
	}
	if got := restartOp(root, "L-02"); got != "" {
		t.Errorf("another lane's lock: op = %q, want none", got)
	}
	lock.Unlock()
	if got := restartOp(root, "L-01"); got != "" {
		t.Errorf("freed: op = %q, want none", got)
	}
}

// The merge queue is serve's: its tasks in the merge loop's order with their phase and its
// stage (serve's phase, the lane row's word) and what each waits for, copied from its scheduler (never
// predicted: a task nothing waits behind says nothing more). A task its worker works on is no merge item.
func TestQueueMerge(t *testing.T) {
	now := time.Now()
	tasks := []wire.TaskStatus{{Task: "T-10", State: "submitted", Phase: wire.PhaseGating, Stage: wire.StageGating},
		{Task: "T-3", State: "working", Phase: wire.PhaseWorking},
		{Task: "T-4", State: "working", Phase: wire.PhaseNeedsYou}, // a watchdog's needs-you: no stage
		{Task: "T-5", State: "dispatched", Phase: wire.PhaseNeedsYou},
		{Task: "T-7", State: "needs-you", Phase: wire.PhaseHeld, Stage: wire.StageHeld},
		{Task: "T-8", State: "accepted", Phase: wire.PhasePublish, Stage: wire.StagePublish},
		{Task: "T-9", State: "submitted", Phase: wire.PhaseSubmitted, Stage: wire.StageSubmitted}}
	ps := wire.ProjectStatus{Tasks: tasks, Scratch: &wire.ScratchStatus{Task: "T-7", Why: wire.ScratchHeld},
		ScratchQueue: []wire.ScratchWait{{Task: "T-10", WaitsOn: "T-7", Why: wire.ScratchHeld}, {Task: "T-8"},
			{Task: "T-9", WaitsOn: "T-7", Why: wire.ScratchHeld}}}
	q := queue(&Project{}, ps, "", nil, nil, now)
	want := []MergeItem{
		{Task: "T-10", Stage: wire.StageGating, WaitsOn: "T-7", Why: wire.ScratchHeld},
		{Task: "T-7", Stage: wire.StageHeld},
		{Task: "T-8", Stage: wire.StagePublish},
		{Task: "T-9", Stage: wire.StageSubmitted, WaitsOn: "T-7", Why: wire.ScratchHeld}}
	if !slices.Equal(q.Merge, want) {
		t.Errorf("merge %+v\nwant %+v", q.Merge, want)
	}
	if q.Scratch != ps.Scratch {
		t.Errorf("scratch %+v, want serve's %+v", q.Scratch, ps.Scratch)
	}
}

// a task an older serve lists without an ended field is open, so the queue holds it; the new
// serve's ended: true (a dropped task) leaves it out, as does an ended waiting task.
func TestQueueReadsAnOlderServesTaskAsOpen(t *testing.T) {
	t.Parallel()
	var older wire.ProjectStatus
	if err := json.Unmarshal([]byte(`{"tasks":[{"task":"T-1","state":"submitted","phase":"gating","stage":"gating"}]}`),
		&older); err != nil {
		t.Fatal(err)
	}
	if len(older.Tasks) != 1 || older.Tasks[0].Ended {
		t.Fatalf("precondition: %+v, want one task with no ended", older.Tasks)
	}
	if q := queue(&Project{}, older, "", nil, nil, time.Now()); len(q.Merge) != 1 || q.Merge[0].Task != "T-1" {
		t.Errorf("T-1 without ended: merge %+v, want it", q.Merge)
	}

	var ended wire.ProjectStatus
	if err := json.Unmarshal([]byte(`{"tasks":[{"task":"T-1","state":"submitted","stage":"gating","ended":true},`+
		`{"task":"T-2","state":"waiting","waiting":true,"rank":1,"ended":true}]}`), &ended); err != nil {
		t.Fatal(err)
	}
	if len(ended.Tasks) != 2 || !ended.Tasks[0].Ended || !ended.Tasks[1].Ended {
		t.Fatalf("precondition: %+v, want both ended", ended.Tasks)
	}
	if q := queue(&Project{}, ended, "", nil, nil, time.Now()); len(q.Merge) != 0 || len(q.Waiting) != 0 {
		t.Errorf("ended tasks: merge %+v, waiting %+v, want neither", q.Merge, q.Waiting)
	}
}

// the nightly check's next run is today's time when it is still ahead, else tomorrow's; none without
// merge.nightly_at.
func TestNightlyNext(t *testing.T) {
	now := time.Date(2026, 11, 1, 14, 0, 0, 0, time.Local)
	if n := nightly("03:00", now); n == nil || n.At != "03:00" ||
		n.Next != time.Date(2026, 11, 2, 3, 0, 0, 0, time.Local).Unix() {
		t.Errorf("03:00 at 14:00: %+v, want tomorrow 03:00", n)
	}
	if n := nightly("23:30", now); n == nil || n.Next != time.Date(2026, 11, 1, 23, 30, 0, 0, time.Local).Unix() {
		t.Errorf("23:30 at 14:00: %+v, want today 23:30", n)
	}
	if n := nightly("", now); n != nil {
		t.Errorf("none configured: %+v, want nil", n)
	}
}
