package view

import (
	"slices"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/launch"
)

// T-657: a lane's state column says what serve does for its task — gating, merging, reviewing (an integrator job is
// out), held (needs-you) — else the lane table's state (done). T-662: gating and merging only while the effect runs; a
// gate or publish whose intent waits reads gate pending / publish pending, not progress.
func TestColumn(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, c := range []struct {
		name    string
		lane    Lane
		running []Running
		pending []wire.PendingStep
		needs   []NeedsYou
		want    string
	}{
		{"gating", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "submitted"},
			[]Running{{Step: event.StepGate, Task: "T-1"}}, nil, nil, "gating"},
		{"merging", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "accepted"},
			[]Running{{Step: event.StepPublish, Task: "T-1"}}, nil, nil, "merging"},
		{"gate pending", Lane{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "submitted"},
			nil, []wire.PendingStep{{Step: event.StepGate, Task: "T-1"}}, nil, "gate pending"},
		{"publish pending", Lane{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "accepted"},
			nil, []wire.PendingStep{{Step: event.StepPublish, Task: "T-1"}}, nil, "publish pending"},
		{"pending on another task", Lane{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "working"},
			nil, []wire.PendingStep{{Step: event.StepGate, Task: "T-2"}}, nil, "working"},
		{"reviewing", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "awaiting-accept"}, nil, nil, nil,
			"reviewing"},
		{"resolving", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "resolving"}, nil, nil, nil,
			"reviewing"},
		{"held", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "needs-you"}, nil, nil, nil, "held"},
		{"held by a watchdog fact", Lane{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "working"},
			nil, nil, []NeedsYou{{Task: "T-9", Lane: "O-01", Reason: "stalled"}}, "held"},
		{"done", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "published"}, nil, nil, nil, "done"},
		// T-718: an accepted task whose publish waits for the reviewer or the deep dive
		{"merge held", Lane{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "accepted",
			Hold: "merge held: reviewer working"}, nil, nil, nil, "merge held: reviewer working"},
	} {
		v := &Project{Running: c.running, Pending: c.pending, NeedsYou: c.needs}
		if got := column(c.lane, v); got != c.want {
			t.Errorf("%s: column %q, want %q", c.name, got, c.want)
		}
	}
}

// T-192, T-500: a lane whose restart lock is held has op restart (obor status); a lock file nobody holds (its restart
// was killed) is no op.
func TestRestartOpIsTheHeldRestartLock(t *testing.T) {
	root := t.TempDir()
	lock := launch.RestartLock(root, "O-01", "")
	if ok, err := lock.Try(); !ok || err != nil {
		t.Fatalf("Try = %v, %v", ok, err)
	}
	if got := restartOp(root, "O-01"); got != "restart" {
		t.Errorf("held: op = %q, want restart", got)
	}
	if got := restartOp(root, "O-02"); got != "" {
		t.Errorf("another lane's lock: op = %q, want none", got)
	}
	lock.Unlock()
	if got := restartOp(root, "O-01"); got != "" {
		t.Errorf("freed: op = %q, want none", got)
	}
}

// T-669 (RV-75 #1 #2), T-673: the merge queue is serve's: its tasks in the merge loop's order with their stages and
// what each waits for, copied from its scheduler (never predicted: a task nothing waits behind says nothing more).
func TestQueueMerge(t *testing.T) {
	now := time.Now()
	tasks := []wire.TaskStatus{{Task: "T-10", State: "submitted"}, {Task: "T-3", State: "working"},
		{Task: "T-7", State: "needs-you"}, {Task: "T-8", State: "accepted"}, {Task: "T-9", State: "submitted"}}
	ps := wire.ProjectStatus{Scratch: &wire.ScratchStatus{Task: "T-7", Why: wire.ScratchHeld},
		ScratchQueue: []wire.ScratchWait{{Task: "T-10", WaitsOn: "T-7", Why: wire.ScratchHeld}, {Task: "T-8"},
			{Task: "T-9", WaitsOn: "T-7", Why: wire.ScratchHeld}}}
	q := queue(&Project{Tasks: tasks}, ps, "", nil, nil, now)
	want := []MergeItem{{Task: "T-10", State: "submitted", Stage: StageSubmitted, WaitsOn: "T-7", Why: wire.ScratchHeld},
		{Task: "T-7", State: "needs-you", Stage: StageHeld}, {Task: "T-8", State: "accepted", Stage: StagePublish},
		{Task: "T-9", State: "submitted", Stage: StageSubmitted, WaitsOn: "T-7", Why: wire.ScratchHeld}}
	if !slices.Equal(q.Merge, want) {
		t.Errorf("merge %+v\nwant %+v", q.Merge, want)
	}
	if q.Scratch != ps.Scratch {
		t.Errorf("scratch %+v, want serve's %+v", q.Scratch, ps.Scratch)
	}
	running := &Project{Tasks: tasks, Running: []Running{{Step: event.StepGate, Task: "T-10"}}}
	free := queue(running, wire.ProjectStatus{}, "", nil, nil, now)
	// No scratch, no scheduler facts: the stages are the facts, and nothing claims a place in line.
	wantFree := []MergeItem{{Task: "T-10", State: "submitted", Stage: StageGating},
		{Task: "T-7", State: "needs-you", Stage: StageHeld}, {Task: "T-8", State: "accepted", Stage: StagePublish},
		{Task: "T-9", State: "submitted", Stage: StageSubmitted}}
	if !slices.Equal(free.Merge, wantFree) {
		t.Errorf("free scratch: merge %+v\nwant %+v", free.Merge, wantFree)
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
