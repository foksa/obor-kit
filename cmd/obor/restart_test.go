package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/testkit"
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
		if got := agentJob(c.st, "/p", "worker", "L-01"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// T-947: the integrator's open job is a task's review or resolve job, whatever the task's lane; never a lane worker's
// work or fix job, another project's job or none.
func TestIntegratorJob(t *testing.T) {
	t.Parallel()
	st := func(root string, tasks ...wire.TaskStatus) wire.Status {
		return wire.Status{Project: &wire.ProjectStatus{Root: root, Tasks: tasks}}
	}
	for _, c := range []struct {
		name string
		st   wire.Status
		want string
	}{
		{"a review job", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/1/review"}), "T-1/1/review"},
		{"a resolve job", st("/p", wire.TaskStatus{Task: "T-2", Lane: "L-03", Job: "T-2/2/resolve"}), "T-2/2/resolve"},
		{"another .N review job", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/2/review.1"}),
			"T-1/2/review.1"},
		{"a work job is the lane worker's", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/1/work"}), ""},
		{"a fix job is the lane worker's", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/2/fix"}), ""},
		{"no job open", st("/p", wire.TaskStatus{Task: "T-1", Lane: "L-01", State: "working"}), ""},
		{"another project's", st("/q", wire.TaskStatus{Task: "T-1", Lane: "L-01", Job: "T-1/1/review"}), ""},
		{"serve has no project", wire.Status{}, ""},
	} {
		if got := agentJob(c.st, "/p", "integrator", ""); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// T-778: obor restart of a lane's worker resolves its job before anything stops: serve down (no socket) or serving
// another project on a lane whose row holds a task is an error naming the task and serve's state (the restart refuses);
// serve up gives the job; a lane with no task and serve down is the plain restart (no job, no error). RV-99 #6: the
// refusal names obor serve restart (installed service) or obor serve (in a pane) and obor status --facts to check, no
// obor serve status.
func TestServeLaneJobServeDown(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.State("L-01", `{"task":"T-5","branch":"task/T-5","agent":"pj-l-01"}`)
	cfg := &config.Config{Root: p.Root}
	if _, err := os.Stat(wire.SocketPath(p.Root)); err == nil {
		t.Fatal("precondition: no serve socket")
	}
	job, err := serveAgentJob(context.Background(), cfg, "worker", "L-01")
	if err == nil || job != "" {
		t.Fatalf("serve down, lane with a task: %q, %v; want an error", job, err)
	}
	for _, want := range []string{"L-01 holds T-5", "obor serve cannot tell its job", "start obor serve",
		"obor serve restart for the installed service", "obor serve in a pane", "obor status --facts checks it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "serve status") { // RV-99 #6: obor serve status is gone (T-774)
		t.Errorf("error %q names obor serve status", err)
	}
	if job, err := serveAgentJob(context.Background(), cfg, "worker", "L-02"); job != "" || err != nil {
		t.Errorf("serve down, lane with no task: %q, %v; want the plain restart", job, err)
	}
	// T-947: the integrator's lane is "", and serve down cannot tell its job; no lane row says otherwise, so the
	// plain restart (no wake), as before this task.
	if job, err := serveAgentJob(context.Background(), cfg, "integrator", ""); job != "" || err != nil {
		t.Errorf("serve down, integrator: %q, %v; want the plain restart", job, err)
	}
	up := func(st wire.Status) func() (wire.Status, error) {
		return func() (wire.Status, error) { return st, nil }
	}
	served := wire.Status{Project: &wire.ProjectStatus{Root: p.Root, Tasks: []wire.TaskStatus{
		{Task: "T-5", Lane: "L-01", Job: "T-5/2/fix"}}}}
	if job, err := resolveAgentJob(p.Root, "worker", "L-01", up(served)); job != "T-5/2/fix" || err != nil {
		t.Errorf("serve up: %q, %v; want its job", job, err)
	}
	if job, err := resolveAgentJob(p.Root, "integrator", "", up(served)); job != "" || err != nil {
		t.Errorf("serve up, a fix job is the worker's: %q, %v; want no job", job, err)
	}
	review := wire.Status{Project: &wire.ProjectStatus{Root: p.Root, Tasks: []wire.TaskStatus{
		{Task: "T-5", Lane: "L-01", Job: "T-5/2/review"}}}}
	if job, err := resolveAgentJob(p.Root, "integrator", "", up(review)); job != "T-5/2/review" || err != nil {
		t.Errorf("serve up, review open: %q, %v; want its job", job, err)
	}
	other := wire.Status{Project: &wire.ProjectStatus{Root: "/elsewhere"}}
	if _, err := resolveAgentJob(p.Root, "worker", "L-01", up(other)); err == nil || !strings.Contains(err.Error(), "serves no project at") {
		t.Errorf("serve of another project, lane with a task: %v; want an error", err)
	}
	down := func() (wire.Status, error) { return wire.Status{}, errors.New("i/o timeout") }
	if _, err := resolveAgentJob(p.Root, "worker", "L-01", down); err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("serve timing out, lane with a task: %v; want an error naming it", err)
	}
}
