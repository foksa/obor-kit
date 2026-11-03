package main

import (
	"testing"

	"obor/internal/app/wire"
)

// T-773: obor restart of a lane's worker sends the wake of the job its lane's task has open in serve: a work or fix job
// of that lane, never the integrator's review job of a task there, nor another lane's or another project's job.
func TestLaneJob(t *testing.T) {
	t.Parallel()
	st := func(root string, tasks ...wire.TaskStatus) wire.Status {
		return wire.Status{Project: &wire.ProjectStatus{Root: root, Tasks: tasks}}
	}
	for _, c := range []struct {
		name string
		st   wire.Status
		want string
	}{
		{"the lane's work job", st("/p", wire.TaskStatus{Task: "T-2", Lane: "L-02", Job: "T-2/1/work"},
			wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/3/work.2"}), "T-1/3/work.2"},
		{"a fix job", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/2/fix"}), "T-1/2/fix"},
		{"a review job is the integrator's", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/1/review"}), ""},
		{"no job open", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", State: "working"}), ""},
		{"another project's", st("/q", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/1/work"}), ""},
		{"serve has no project", wire.Status{}, ""},
	} {
		if got := laneJob(c.st, "/p", "L-01"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
