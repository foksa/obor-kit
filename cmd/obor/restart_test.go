package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
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

// obor restart (internal/launch has the behaviour): the result line last, FAIL → exit 1 (a --stop that gives up
// prints its evidence first).
func TestRestartCommand(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	o1 := p.PlainLane("L-01", "")
	p.Config()
	f := p.Herdr()
	f.Shell("w1:p2", "pj-int", p.Root)
	a := f.Agent("w1:p1", "pj-l-01", "idle", o1)
	a.Label = "pj-l-01"
	// The agent's group leader is a live pid (this test's own), and herdr lists no process in its group (RV-24 #1:
	// an empty list proves no exit), so the stop still times out.
	a.Foreground, a.OnQuit = os.Getpid(), "stay" // the live fake would quit on /exit: this agent ignores it
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + p.Base + "/state", "OBOR_RESTART_WAIT=1", callDown}
	for _, c := range []struct {
		args []string
		rc   int
		last string
	}{
		{[]string{"restart"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "a", "b"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "pj-l-01", "--stop", "--fresh"}, 1, "FAIL --stop takes no --fresh"},
		// T-466: --keep-inbox went with the inbox: an unknown flag.
		{[]string{"restart", "pj-l-01", "--fresh", "--keep-inbox"}, 1, "FAIL " + help.Usage("restart")},
		// T-864: the flags restart gave up are unknown flags now (T-785's one-minor notice is gone): the usage.
		{[]string{"restart", "pj-l-01", "--prompt", "go"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "pj-l-01", "--type", "luna"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "pj-l-01", "--effort", "high"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "pj-l-01", "--sid", "sid-9"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "pj-l-01", "--type=luna"}, 1, "FAIL " + help.Usage("restart")},
		{[]string{"restart", "nobody"}, 1, "FAIL unknown agent nobody (no lane, role, agents entry or recorded session)"},
		// T-959: serve is down and may hold an open job for the integrator: refused, not restarted without a wake.
		{[]string{"restart", "pj-int", "--fresh"}, 1, "FAIL restart pj-int: obor serve is not running; it may hold an " +
			"open job for pj-int: start serve, then restart"},
		{[]string{"restart", "pj-int", "--stop"}, 0, "STOPPED pj-int (pane w1:p2, already at a shell)"},
	} {
		rc, stdout, stderr := run(t, p.Root, env, c.args...)
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if rc != c.rc || lines[len(lines)-1] != c.last || stderr != "" {
			t.Errorf("obor %v: exit %d (want %d)\n%s---\n%s", c.args, rc, c.rc, stdout, stderr)
		}
	}
	// A direct stop that gives up prints its evidence, then the one-line FAIL last (T-213).
	rc, stdout, _ := run(t, p.Root, env, "restart", "pj-l-01", "--stop")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 1 || lines[len(lines)-1] != "FAIL pj-l-01 did not exit in 1s (pane w1:p1; the input was cleared; --force terminates it)" ||
		!strings.Contains(stdout, "agent status:") || !strings.Contains(stdout, "pane processes:") ||
		strings.Index(stdout, "agent status:") > strings.Index(stdout, "FAIL ") {
		t.Errorf("obor restart --stop that cannot stop: evidence, then FAIL last: exit %d\n%s", rc, stdout)
	}
	var typed, started bool
	for _, r := range srv.Requests() {
		typed = typed || r.Method == "pane.send_input" && strings.Contains(string(r.Params), `"pane_id":"w1:p1"`) &&
			strings.Contains(string(r.Params), `"text":"/exit"`)
		started = started || r.Method == "agent.start"
	}
	if !typed || started {
		t.Errorf("--stop typed /exit in the worker's pane, started nothing: typed %v, started %v: %+v", typed, started, srv.Requests())
	}
}
