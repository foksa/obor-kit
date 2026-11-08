package main

import (
	"errors"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-773, T-959: obor restart asks serve for the job an agent's restart carries (restart_job), with the project, the
// agent, its role and a worker's lane; serve's answer is the job, or none.
func TestRestartJob(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	var got wire.RestartJobReq
	job, err := resolveAgentJob(p.Root, "pj-l-01", "worker", "L-01", func(c wire.RestartJobReq) (string, error) {
		got = c
		return "T-5/2/fix", nil
	})
	if err != nil || job != "T-5/2/fix" {
		t.Fatalf("worker: %q, %v; want its job", job, err)
	}
	if got.Root != p.Root || got.Agent != "pj-l-01" || got.Role != "worker" || got.Lane != "L-01" {
		t.Errorf("the call %+v, want the lane's worker", got)
	}
	if job, err := resolveAgentJob(p.Root, "pj-int", "integrator", "", func(wire.RestartJobReq) (string, error) {
		return "T-5/2/review", nil
	}); job != "T-5/2/review" || err != nil {
		t.Errorf("integrator: %q, %v; want its job", job, err)
	}
	if job, err := resolveAgentJob(p.Root, "pj-int", "integrator", "", func(wire.RestartJobReq) (string, error) {
		return "", nil
	}); job != "" || err != nil {
		t.Errorf("no open job: %q, %v; want no wake", job, err)
	}
}

// T-778, T-959: serve down refuses where it may hold a job. A lane whose row holds a task names the task and serve's
// state (RV-99 #6: obor serve restart (the installed service) or obor serve (a pane), obor status --facts to check, no
// obor serve status). The integrator, whose lane is "", has no row to say otherwise: it always refuses with its own
// line (RV-133 #3). A lane with no task is the plain restart.
func TestServeJobServeDown(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.State("L-01", `{"task":"T-5","branch":"task/T-5","agent":"pj-l-01"}`)
	down := func(wire.RestartJobReq) (string, error) { return "", errors.New("i/o timeout") }
	job, err := resolveAgentJob(p.Root, "pj-l-01", "worker", "L-01", down)
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
	if job, err := resolveAgentJob(p.Root, "pj-l-02", "worker", "L-02", down); job != "" || err != nil {
		t.Errorf("serve down, lane with no task: %q, %v; want the plain restart", job, err)
	}
	job, err = resolveAgentJob(p.Root, "pj-int", "integrator", "", down)
	const want = "restart pj-int: obor serve is not running; it may hold an open job for pj-int: start serve, then restart"
	if job != "" || err == nil || err.Error() != want {
		t.Errorf("serve down, integrator: %q, %v; want %q", job, err, want)
	}
	up := func(job string) func(wire.RestartJobReq) (string, error) {
		return func(wire.RestartJobReq) (string, error) { return job, nil }
	}
	if job, err := resolveAgentJob(p.Root, "pj-l-01", "worker", "L-01", up("T-5/2/fix")); job != "T-5/2/fix" || err != nil {
		t.Errorf("serve up: %q, %v; want its job", job, err)
	}
	if job, err := resolveAgentJob(p.Root, "pj-int", "integrator", "", up("T-5/2/review")); job != "T-5/2/review" || err != nil {
		t.Errorf("serve up, review open: %q, %v; want its job", job, err)
	}
}
