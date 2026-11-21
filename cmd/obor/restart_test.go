package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// obor restart's first prompt: a handoff restart that names its pane starts the lead with /obor:lead handoff; a lead
// whose recorded session has no transcript is a fresh start and gets the plain role prompt.
func TestRestartHandoffOnlyWithTheFlag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, roles, agent string
		prep               func(p *testkit.Project)
		args               []string
		prompt             string // in the start's args ("": none)
	}{
		{"the lead's --handoff with its pane named: the handoff form", `{"lead":"opus"}`, "pj-lead", nil,
			[]string{"restart", "pj-lead", "--pane", "w1:p1", "--handoff"}, "/obor:lead handoff"},
		{"a lead's fresh start without --fresh (its recorded session has no transcript): the plain role prompt",
			`{"lead":"opus"}`, "pj-lead", func(p *testkit.Project) {
				p.Session("pj-lead", "sid-1")
				if err := os.Remove(p.Path(".obor/state/sessions/sid-1.jsonl")); err != nil {
					t.Fatal(err)
				}
			}, []string{"restart", "pj-lead"}, "/obor:lead"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := testkit.NewPlain(t)
			p.Config(testkit.Set("agents.roles", c.roles))
			f := p.Herdr()
			f.Shell("w1:p1", c.agent, p.Root)
			srv := herdrtest.Serve(t, f)
			env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
				"OBOR_STATE_HOME=" + p.Base + "/state", "OBOR_RESTART_WAIT=1", callDown}
			if c.prep != nil {
				c.prep(p)
			}
			rc, stdout, stderr := run(t, p.Root, env, c.args...)
			if rc != 0 || !strings.HasPrefix(lastLine(stdout), "RESTARTED "+c.agent) || stderr != "" {
				t.Fatalf("obor %v: exit %d\n%s---\n%s", c.args, rc, stdout, stderr)
			}
			var start string
			for _, r := range srv.Requests() {
				if r.Method == "agent.start" {
					start = string(r.Params)
				}
			}
			if start == "" {
				t.Fatalf("precondition: no agent.start\n%s", stdout)
			}
			if c.prompt != "" && !strings.Contains(start, `"`+c.prompt+`"`) {
				t.Errorf("obor %v: start %s, want the first prompt %q", c.args, start, c.prompt)
			}
			if c.prompt != "/obor:lead handoff" && strings.Contains(start, "handoff") {
				t.Errorf("obor %v: start %s names the handoff form", c.args, start)
			}
		})
	}
}

// --handoff is the lead's handoff restart: any other agent is refused in one line, with nothing started or typed.
func TestRestartHandoffRefusedForANonLead(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("agents.roles", `{"reviewer":"opus"}`))
	f := p.Herdr()
	f.Shell("w1:p1", "pj-rev", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + p.Base + "/state", "OBOR_RESTART_WAIT=1"}
	rc, stdout, stderr := run(t, p.Root, env, "restart", "pj-rev", "--handoff")
	want := "FAIL --handoff is the lead's handoff restart: pj-rev is not the lead"
	if rc != 1 || stdout != want+"\n" || stderr != "" {
		t.Fatalf("obor restart pj-rev --handoff: exit %d, %q, %q; want the one FAIL line", rc, stdout, stderr)
	}
	if calls := srv.Requests(); len(calls) != 0 {
		t.Errorf("nothing looked up or touched: %+v", calls)
	}
	// The lead's agent is the one that takes it, and only the lead: an agent no config knows is no lead either.
	if rc, stdout, _ := run(t, p.Root, env, "restart", "nobody", "--handoff"); rc != 1 ||
		lastLine(stdout) != "FAIL --handoff is the lead's handoff restart: nobody is not the lead" {
		t.Errorf("obor restart nobody --handoff: exit %d, %q", rc, stdout)
	}
}

// No .obor/config.json (load's exit 3): restart fails in load's one diagnostic line, before any agent lookup and
// before anything is started or typed.
func TestRestartWithoutAConfig(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t) // no p.Config(): the project has no .obor/config.json
	if _, err := os.Stat(p.Path(".obor/config.json")); !os.IsNotExist(err) {
		t.Fatalf("precondition: the config exists (%v)", err)
	}
	for _, args := range [][]string{
		{"restart", "pj-lead", "--stop"},
		{"restart", "pj-lead", "--fresh"},
		{"restart", "pj-lead", "--handoff"},
	} {
		rc, stdout, stderr := run(t, p.Root, nil, args...)
		lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
		if rc != 3 || stdout != "" || len(lines) != 1 || !strings.Contains(lines[0], "no "+p.Root+"/.obor/config.json") {
			t.Errorf("obor %v: exit %d, stdout %q, stderr %q; want one line naming the missing config", args, rc, stdout, stderr)
		}
	}
}

// A config that exists but does not load: restart's FAIL carries the load error.
func TestRestartBrokenConfigNamesItsError(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	if err := os.MkdirAll(p.Path(".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path(".obor/config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load(p.Root)
	if loadErr == nil {
		t.Fatal("precondition: the broken config loads")
	}
	rc, stdout, _ := run(t, p.Root, nil, "restart", "pj-lead", "--fresh")
	want := "FAIL the config does not load: " + loadErr.Error() + " (obor config validate)"
	if rc != 1 || strings.TrimSpace(stdout) != want {
		t.Errorf("exit %d, stdout %q; want %q", rc, stdout, want)
	}
}

// the usage offers --handoff beside --fresh: it is restart's own flag, not a start parameter.
func TestRestartUsageNamesHandoff(t *testing.T) {
	t.Parallel()
	if u := help.Usage("restart"); !strings.Contains(u, "--handoff") {
		t.Errorf("usage %q does not name --handoff", u)
	}
}

// obor restart asks serve for the job an agent's restart carries (restart_job), with the project, the
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

// serve down refuses where it may hold a job. A lane whose row holds a task names the task and serve's
// state (obor serve restart (the installed service) or obor serve (a pane), obor status --facts to check, no
// obor serve status). The integrator, whose lane is "", has no row to say otherwise: it always refuses with its own
// line. A lane with no task is the plain restart.
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
	if strings.Contains(err.Error(), "serve status") { // obor serve status is no subcommand
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

// serve down and the lane's state row unreadable: the restart refuses, naming the read's failure, because it cannot
// tell that the lane holds no task whose job serve would hand back with the wake.
func TestRestartJobUnreadableLaneRow(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Write(".obor/state/obor.db", "not a database\n")
	down := func(wire.RestartJobReq) (string, error) { return "", errors.New("i/o timeout") }
	job, err := resolveAgentJob(p.Root, "pj-l-01", "worker", "L-01", down)
	if err == nil || job != "" {
		t.Fatalf("serve down, state row unreadable: %q, %v; want an error", job, err)
	}
	for _, want := range []string{"L-01: its state row cannot be read", "not a database",
		"obor serve may hold a job for it", "start obor serve, then retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// obor restart's argument check (usage, the flags it does not take, --stop with --fresh or --handoff) ends before the
// command reads its directory: the rows call restart. A refused --handoff is TestRestartHandoffRefusedForANonLead's.
func TestRestartArguments(t *testing.T) {
	usage := "FAIL " + help.Usage("restart")
	for _, c := range []struct {
		args []string
		last string
	}{
		{nil, usage},
		{[]string{"a", "b"}, usage},
		{[]string{"pj-l-01", "--stop", "--fresh"}, "FAIL --stop takes no --fresh"},
		{[]string{"pj-l-01", "--stop", "--handoff"}, "FAIL --stop takes no --handoff"},
		{[]string{"pj-l-01", "--fresh", "--keep-inbox"}, usage}, // --keep-inbox is an unknown flag
		// the flags restart does not take are unknown flags: the usage.
		{[]string{"pj-l-01", "--prompt", "go"}, usage},
		{[]string{"pj-l-01", "--type", "luna"}, usage},
		{[]string{"pj-l-01", "--effort", "high"}, usage},
		{[]string{"pj-l-01", "--sid", "sid-9"}, usage},
		{[]string{"pj-l-01", "--type=luna"}, usage},
	} {
		rc, stdout := callCmd(t, restart, c.args...)
		if rc != 1 || stdout != c.last+"\n" {
			t.Errorf("restart %q: exit %d, %q; want 1 and %q", c.args, rc, stdout, c.last)
		}
	}
}

// obor restart on the binary (internal/launch has the behaviour): the result line last, FAIL → exit 1 (a --stop that
// gives up prints its evidence first). The rows are the ones that need the project: an agent no config knows, serve
// down for an agent that may hold a job, a stop of a pane at its shell.
func TestRestartCommand(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	o1 := p.PlainLane("L-01", "")
	p.Config()
	f := p.Herdr()
	f.Shell("w1:p2", "pj-int", p.Root)
	a := f.Agent("w1:p1", "pj-l-01", "idle", o1)
	a.Label = "pj-l-01"
	// The agent's group leader is a live pid (this test's own), and herdr lists no process in its group (a
	// empty list proves no exit), so the stop still times out.
	a.Foreground, a.OnQuit = os.Getpid(), "stay" // the live fake would quit on /exit: this agent ignores it
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + p.Base + "/state", "OBOR_RESTART_WAIT=1", callDown}
	for _, c := range []struct {
		args []string
		rc   int
		last string
	}{
		{[]string{"restart", "nobody"}, 1, "FAIL unknown agent nobody (no lane, role or recorded session)"},
		// serve is down and may hold an open job for the integrator: refused, not restarted without a wake.
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
	// A direct stop that gives up prints its evidence, then the one-line FAIL last.
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

// obor restart's load (load()) takes a config naming keys this obor does not know (the promotion's):
// the stop runs, with a warning, where it was "FAIL the config does not load".
func TestRestartLoadsAConfigWithUnknownKeys(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "")
	p.Config(testkit.Set("branches.promote", "release"), testkit.Set("merge.weights", map[string]any{"core": 3}))
	f := p.Herdr()
	f.Shell("w1:p2", "pj-int", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + p.Base + "/state", "OBOR_RESTART_WAIT=1", callDown}
	rc, stdout, stderr := run(t, p.Root, env, "restart", "pj-int", "--stop")
	if rc != 0 || lastLine(stdout) != "STOPPED pj-int (pane w1:p2, already at a shell)" {
		t.Errorf("restart --stop: exit %d, %q (%s); want the stop", rc, stdout, stderr)
	}
	for _, k := range []string{"branches", "merge.weights"} { // branches: shape 1's, left with promote alone
		if !strings.Contains(stderr, `unknown key "`+k+`" ignored`) {
			t.Errorf("restart: no warning naming %s in %q", k, stderr)
		}
	}
}
