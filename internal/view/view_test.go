package view

import (
	"slices"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/launch"
)

// T-192, T-500: a lane whose restart lock is held has op restart (obor status); a lock file nobody holds (its restart
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

// T-669 (RV-75 #1 #2), T-673: the merge queue is serve's: its tasks in the merge loop's order with their phase and its
// stage (T-788: serve's phase, the lane row's word) and what each waits for, copied from its scheduler (never
// predicted: a task nothing waits behind says nothing more). A task its worker works on is no merge item.
func TestQueueMerge(t *testing.T) {
	now := time.Now()
	tasks := []wire.TaskStatus{{Task: "T-10", State: "submitted", Phase: wire.PhaseGating, Stage: wire.StageGating},
		{Task: "T-3", State: "working", Phase: wire.PhaseWorking},
		{Task: "T-4", State: "working", Phase: wire.PhaseNeedsYou}, // a watchdog's needs-you: no stage (T-797)
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

// T-664: the nightly check's next run is today's time when it is still ahead, else tomorrow's; none without
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
