package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/service"
	"obor/internal/testkit"
)

// stopCall is one agent stop a sleeper asked for.
type stopCall struct {
	agent, pane string
	force       bool
}

// sleepFx is a project with lane O-01 registered in the machine's list (the test's state home), a sleeper on it whose
// serve is down (Ask fails), whose drops and agent stops are recorded, and whose herdr is a fake with no panes.
func sleepFx(t *testing.T) (p *testkit.Project, s *sleeper, out *bytes.Buffer, stops *[]stopCall, drops *[]wire.Call) {
	t.Helper()
	p = testkit.New(t)
	p.Lane("O-01", "task/T-02")
	p.Config()
	out, stops, drops = &bytes.Buffer{}, &[]stopCall{}, &[]wire.Call{}
	cfg, warn, err := config.LoadReader(p.Root)
	if err != nil || warn != "" {
		t.Fatal(err, warn)
	}
	env := p.Env("PATH=" + os.Getenv("PATH"))
	if _, err := project.Register(p.Root, env); err != nil {
		t.Fatal(err)
	}
	s = &sleeper{Cfg: cfg, Env: env, Out: out, Why: "obor stop", H: &herdr.Socket{Path: herdrtest.Serve(t, p.Herdr()).Path},
		Ask: func(context.Context) (wire.Status, error) { return wire.Status{}, errors.New("serve down") },
		Drop: func(_ context.Context, c wire.Call) (string, error) {
			*drops = append(*drops, c)
			return "DROPPED " + c.Task + c.Lane, nil
		},
		Stop: func(_ context.Context, agent, pane string, force bool) (string, error) {
			*stops = append(*stops, stopCall{agent, pane, force})
			return "STOPPED " + agent + " (pane " + pane + ")", nil
		}}
	return p, s, out, stops, drops
}

// nothingDone: no marker, the project still registered, no agent stopped.
func nothingDone(t *testing.T, p *testkit.Project, s *sleeper, stops []stopCall) {
	t.Helper()
	if project.Stopped(p.Root) || len(stops) != 0 {
		t.Errorf("marker %v, stops %v: want nothing done", project.Stopped(p.Root), stops)
	}
	if _, ok := project.RootOf("proj", s.Env); !ok {
		t.Error("the project left the machine's list")
	}
}

// T-709: obor stop refuses while a task is in progress — serve down, a lane git and the store call working; serve up,
// a waiting dispatch on no lane — and does nothing then.
func TestStopRefusesATaskInProgress(t *testing.T) {
	t.Parallel()
	p, s, out, stops, drops := sleepFx(t)
	p.Dispatched("O-01")
	if rc := s.stop(context.Background()); rc != 1 ||
		!strings.Contains(out.String(), "FAIL tasks in progress: T-02 (O-01, working)") {
		t.Errorf("serve down, O-01 working: exit %d\n%s", rc, out)
	}
	nothingDone(t, p, s, *stops)

	out.Reset()
	s.Ask = func(context.Context) (wire.Status, error) {
		return wire.Status{Project: &wire.ProjectStatus{Root: p.Root, Tasks: []wire.TaskStatus{
			{Task: "T-01", Lane: "O-01", State: "merged"}, {Task: "T-03", State: "waiting"}}}}, nil
	}
	if rc := s.stop(context.Background()); rc != 1 ||
		!strings.Contains(out.String(), "FAIL tasks in progress: T-03 (no lane, waiting)") || strings.Contains(out.String(), "T-01") {
		t.Errorf("serve up, T-03 waiting (T-01 merged): exit %d\n%s", rc, out)
	}
	nothingDone(t, p, s, *stops)
	if len(*drops) != 0 {
		t.Errorf("drops %v without --force", *drops)
	}
}

// T-709: obor stop --force drops each task in progress through serve (a lane by its lane, a waiting task by its id,
// each with --force), a merged one not, and goes on to stop.
func TestStopForceDrops(t *testing.T) {
	p, s, out, _, drops := sleepFx(t)
	fakeLaunchctl(t)
	s.Force = true
	s.Ask = func(context.Context) (wire.Status, error) {
		return wire.Status{Project: &wire.ProjectStatus{Root: p.Root, Tasks: []wire.TaskStatus{
			{Task: "T-02", Lane: "O-01", State: "gating"}, {Task: "T-01", Lane: "O-01", State: "merged"},
			{Task: "T-03", State: "waiting"}}}}, nil
	}
	if rc := s.stop(context.Background()); rc != 0 || !strings.Contains(out.String(), "STOPPED proj") {
		t.Fatalf("stop --force: exit %d\n%s", rc, out)
	}
	if len(*drops) != 2 {
		t.Fatalf("drops %+v, want O-01's and T-03's", *drops)
	}
	d0, d1 := (*drops)[0], (*drops)[1]
	if d0.Lane != "O-01" || d0.Task != "" || !d0.Force || d0.Root != p.Root || !strings.Contains(d0.Note, "obor stop --force") {
		t.Errorf("first drop %+v, want lane O-01 with --force", d0)
	}
	if d1.Task != "T-03" || d1.Lane != "" || !d1.Force {
		t.Errorf("second drop %+v, want task T-03 with --force", d1)
	}
	if !strings.Contains(out.String(), "DROPPED O-01") || !strings.Contains(out.String(), "DROPPED T-03") {
		t.Errorf("serve's replies not printed:\n%s", out)
	}
}

// fakeLaunchctl puts a launchctl on PATH (the test's process env: no t.Parallel) that logs each call to the returned
// file and keeps one label loaded while its state file exists (as internal/service's own fake).
func fakeLaunchctl(t *testing.T) (log, state string) {
	dir := t.TempDir()
	log, state = filepath.Join(dir, "calls"), filepath.Join(dir, "loaded")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
state="` + state + `"
case "$1" in
bootstrap) : > "$state" ;;
bootout) [ -f "$state" ] || exit 3; rm "$state" ;;
print) [ -f "$state" ] || exit 113; printf 'pid = 4242\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log, state
}

// T-709: obor stop's steps against fakes: herdr's panes (the worker and the integrator stopped; the caller's own pane
// and agent, a pane at its shell, an unknown label and another project's agent not), launchctl (the launchd agent booted
// out, its plist gone), the machine's project list (the project out of it) and the marker; a second stop is idempotent
// (each step already done, no agent stopped). obor start reverses it: no marker, the agent installed again, the project
// in the list.
func TestStopStepsAndStart(t *testing.T) {
	p, s, out, stops, _ := sleepFx(t)
	log, state := fakeLaunchctl(t)
	o1 := p.LaneDir("O-01")
	f := p.Herdr()
	srv := herdrtest.Serve(t, f)
	s.H = &herdr.Socket{Path: srv.Path}
	f.Agent("w1:p1", "pj-o-01", "idle", o1).Label = "pj-o-01"
	f.Agent("w1:p2", "pj-int", "working", p.Root).Label = "pj-int"
	f.Agent("w1:p3", "pj-lead", "idle", p.Root).Label = "pj-lead"               // the caller's agent (OBOR_AGENT)
	f.Agent("w1:p9", "pj-rev", "idle", p.Root).Label = "pj-rev"                 // the caller's own pane (HERDR_PANE_ID)
	f.Shell("w1:p4", "pj-advisor", p.Root)                                      // at its shell
	f.Agent("w1:p5", "someone", "idle", p.Root).Label = "someone"               // no agent of this project
	f.Agent("w1:p6", "pj-advisor2", "idle", t.TempDir()).Label = "pj-deep-dive" // another project's pane
	s.Env = p.Env("PATH="+os.Getenv("PATH"), "HERDR_PANE_ID=w1:p9", "OBOR_AGENT=pj-lead")
	s.Force = true // the integrator is working

	svc := serviceOf(s.Cfg, s.Env)
	if svc.Supported() { // installed and loaded before
		if err := os.MkdirAll(filepath.Dir(svc.PlistPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(svc.PlistPath(), svc.Plist(), 0o644)
		os.WriteFile(state, nil, 0o644)
	}

	if rc := s.stop(context.Background()); rc != 0 {
		t.Fatalf("stop: exit %d\n%s", rc, out)
	}
	want := []stopCall{{"pj-o-01", "w1:p1", true}, {"pj-int", "w1:p2", true}}
	if len(*stops) != 2 || (*stops)[0] != want[0] || (*stops)[1] != want[1] {
		t.Errorf("agents stopped %+v, want %+v", *stops, want)
	}
	if !project.Stopped(p.Root) || !strings.Contains(p.Read(".obor/state/stopped"), "why: obor stop") {
		t.Errorf("marker: %v", project.Stopped(p.Root))
	}
	if _, ok := project.RootOf("proj", s.Env); ok {
		t.Error("the project is still in the machine's list")
	}
	if svc.Supported() {
		b, _ := os.ReadFile(log)
		if _, err := os.Stat(svc.PlistPath()); err == nil || !strings.Contains(string(b), "bootout gui/") {
			t.Errorf("launchd agent not uninstalled: plist %v, calls\n%s", err, b)
		}
	} else if !strings.Contains(out.String(), service.Elsewhere) {
		t.Errorf("off darwin: no %q line\n%s", service.Elsewhere, out)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); lines[len(lines)-1] != "STOPPED proj (files stay; obor start wakes it)" {
		t.Errorf("last line %q", lines[len(lines)-1])
	}

	// Idempotent: the agents are at their shells now, every step says it is done already.
	for _, id := range []string{"w1:p1", "w1:p2"} {
		pn := f.Pane(id)
		pn.Kind, pn.Name = "", ""
	}
	out.Reset()
	*stops = nil
	if rc := s.stop(context.Background()); rc != 0 || len(*stops) != 0 ||
		!strings.Contains(out.String(), "agents: none running (already)") ||
		!strings.Contains(out.String(), "not in ") || !strings.Contains(out.String(), "stopped (already)") {
		t.Errorf("second stop: exit %d, stops %v\n%s", rc, *stops, out)
	}
	if svc.Supported() && !strings.Contains(out.String(), "is not installed") {
		t.Errorf("second stop: serve not said uninstalled already\n%s", out)
	}

	out.Reset()
	if rc := s.start(context.Background()); rc != 0 || !strings.Contains(out.String(), "STARTED proj") {
		t.Fatalf("start: exit %d\n%s", rc, out)
	}
	if project.Stopped(p.Root) {
		t.Error("start left the marker")
	}
	if root, ok := project.RootOf("proj", s.Env); !ok || root != project.Physical(p.Root) {
		t.Errorf("start: project list has %q, %v", root, ok)
	}
	if svc.Supported() {
		b, _ := os.ReadFile(svc.PlistPath())
		calls, _ := os.ReadFile(log)
		if string(b) != string(svc.Plist()) || !strings.Contains(string(calls), "bootstrap gui/") {
			t.Errorf("start: launchd agent not installed\n%s", calls)
		}
	}
}

// T-709, the binary: in a stopped project whose config has a key this binary does not know, obor status (text and
// --json) loads it with the warning and the stopped line, obor whoami says it is stopped, obor config validate keeps
// refusing the key, and obor serve and obor init refuse to run, saying obor start.
func TestStoppedProjectWithANewerConfig(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config(testkit.Set("roles.zz_future", map[string]any{}))
	env := []string{"HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"HERDR_SOCKET_PATH=" + herdrtest.Serve(t, p.Herdr()).Path}
	rc, stdout, stderr := run(t, p.Root, env, "status")
	if rc != 0 || !strings.Contains(stdout, `WARN `) || !strings.Contains(stdout, `unknown field "zz_future"`) ||
		strings.Contains(stdout, project.StoppedLine) {
		t.Errorf("status (running): exit %d\n%s---\n%s", rc, stdout, stderr)
	}
	if err := project.MarkStopped(p.Root, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	rc, stdout, stderr = run(t, p.Root, env, "status")
	lines := strings.Split(stdout, "\n")
	if rc != 0 || len(lines) < 3 || !strings.HasPrefix(lines[0], "WARN ") || lines[1] != project.StoppedLine {
		t.Errorf("status (stopped): exit %d\n%s---\n%s", rc, stdout, stderr)
	}
	rc, stdout, _ = run(t, p.Root, env, "status", "--json")
	var j struct{ Notes []string }
	if err := json.Unmarshal([]byte(stdout), &j); rc != 0 || err != nil || len(j.Notes) != 2 || j.Notes[1] != project.StoppedLine {
		t.Errorf("status --json: exit %d, %v, notes %q", rc, err, j.Notes)
	}
	if rc, _, stderr := run(t, p.Root, env, "config", "validate"); rc == 0 || !strings.Contains(stderr, `"zz_future"`) {
		t.Errorf("config validate: exit %d, %q; want the unknown key refused", rc, stderr)
	}

	p.Config() // a config this binary knows: whoami, serve and init load it
	if err := os.Remove(project.StoppedFile(p.Root)); err != nil {
		t.Fatal(err)
	}
	if rc, stdout, _ := run(t, p.Root, env, "whoami"); rc != 0 || strings.Contains(stdout, project.StoppedLine) {
		t.Errorf("whoami (running): exit %d, says stopped:\n%s", rc, stdout)
	}
	if err := project.MarkStopped(p.Root, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if rc, stdout, stderr := run(t, p.Root, env, "whoami"); rc != 0 || !strings.HasPrefix(stdout, project.StoppedLine+"\n") {
		t.Errorf("whoami (stopped): exit %d\n%s---\n%s", rc, stdout, stderr)
	}
	if rc, _, stderr := run(t, p.Root, env, "serve"); rc != 1 || !strings.Contains(stderr, "is stopped (obor stop): obor start wakes it") {
		t.Errorf("serve (stopped): exit %d, %q", rc, stderr)
	}
	if rc, stdout, _ := run(t, p.Root, env, "init"); rc != 1 || !strings.Contains(stdout, "is stopped (obor stop): obor start wakes it") {
		t.Errorf("init (stopped): exit %d\n%s", rc, stdout)
	}
}
