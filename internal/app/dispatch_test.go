package app

import (
	"testing"

	"obor/internal/app/port"
)

// A lane wait is not a bare "no lane": it names each lane that is dirty-free, its uncommitted paths too, and nothing
// about the lanes that hold a task. A lane with more paths than a wait names says the count, and a lane whose paths
// the table did not read is named without them. When every model of the request is paused the pause is the reason,
// no lane named.
func TestLaneWaitNamesDirtyFreeLanes(t *testing.T) {
	ls := []port.Lane{
		{Name: "L-01", State: port.LaneDirtyFree, Paths: []string{"src/x.ts", "out/"}},
		{Name: "L-02", State: port.LaneWorking, Task: "T-9", Paths: []string{"work.txt"}},
		{Name: "L-03", State: port.LaneDirtyFree, Paths: []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}},
		{Name: "L-04", State: port.LaneDirtyFree},
	}
	e := noLaneError{task: "T-1", level: "medium", types: []string{"sonnet", "deepseek"}, ls: ls}
	want := LaneWaitFree + " (L-01 dirty-free: src/x.ts, out/; L-03 dirty-free: a.txt, b.txt, c.txt, +2 more; " +
		"L-04 dirty-free)"
	if got := e.laneWait(); got != want {
		t.Errorf("laneWait = %q, want %q", got, want)
	}
	if got, want := e.Error(), "dispatch T-1: "+want; got != want {
		t.Errorf("Error = %q, want %q", got, want)
	}

	paused := noLaneError{task: "T-1", level: "medium", types: []string{"sonnet", "deepseek"}, ls: ls,
		paused: map[string]string{"sonnet": "sonnet paused: quota (until 18:00)",
			"deepseek": "deepseek paused: at its max 4"}}
	if got, want := paused.laneWait(), "sonnet paused: quota (until 18:00), deepseek paused: at its max 4"; got != want {
		t.Errorf("every model paused: laneWait = %q, want the pauses %q", got, want)
	}
}
