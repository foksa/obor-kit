package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
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

// sleepServe is serve as a sleeper sees it: up or down, its open tasks and its pause; each request recorded (method,
// call) and answered as serve does (a pause sets or clears its pause; a drop's error is dropErr).
type sleepServe struct {
	root    string
	up      bool
	tasks   []wire.TaskStatus
	paused  *event.Pause
	dropErr error
	calls   []string // "<method> <task|lane|note|clear>"
	asks    int
	// onExit is serve's graceful exit on the exit request (nil: it answers, nothing ends).
	onExit func()
}

func (f *sleepServe) ask(context.Context) (wire.Status, error) {
	f.asks++
	if !f.up {
		return wire.Status{}, errors.New("serve down")
	}
	return wire.Status{Project: &wire.ProjectStatus{Root: f.root, Tasks: f.tasks, Paused: f.paused}}, nil
}

func (f *sleepServe) call(_ context.Context, method string, c wire.Call) (string, error) {
	if !f.up {
		return "", errors.New("serve down")
	}
	switch {
	case method == wire.MethodPause && c.Clear:
		f.calls = append(f.calls, "pause --clear")
		f.paused = nil
		return "PAUSE CLEARED", nil
	case method == wire.MethodPause:
		f.calls = append(f.calls, "pause "+c.Note)
		f.paused = &event.Pause{Why: c.Note, Until: c.Until}
		if c.Until != 0 {
			return "PAUSED until " + time.Unix(c.Until, 0).Format("15:04"), nil
		}
		return "PAUSED until cleared", nil
	case method == wire.MethodExit:
		f.calls = append(f.calls, "exit")
		if f.onExit != nil {
			f.onExit()
		}
		return "EXITING", nil
	case method == wire.MethodDrop:
		if c.CallID == "" || !c.Force || !strings.Contains(c.Note, "obor stop --force") {
			return "", errors.New("bad drop call")
		}
		f.calls = append(f.calls, "drop "+c.Task+c.Lane)
		if f.dropErr != nil {
			return "", f.dropErr
		}
		return "DROPPED " + c.Task + c.Lane, nil
	}
	return "", errors.New("unexpected " + method)
}

// sleepFx is a project with lane L-01 registered in the machine's list (the test's state home), a sleeper on it run
// from the main checkout as the owner, whose serve is a sleepServe (up, nothing open), whose agent stops are recorded
// and whose herdr is a fake with no panes.
func sleepFx(t *testing.T) (p *testkit.Project, s *sleeper, out *bytes.Buffer, stops *[]stopCall, f *sleepServe) {
	t.Helper()
	p = testkit.New(t)
	p.Lane("L-01", "task/T-02")
	p.Config()
	out, stops = &bytes.Buffer{}, &[]stopCall{}
	cfg, warn, err := config.LoadReader(p.Root)
	if err != nil || warn != "" {
		t.Fatal(err, warn)
	}
	env := p.Env("PATH=" + os.Getenv("PATH"))
	if _, err := project.Register(p.Root, env); err != nil {
		t.Fatal(err)
	}
	f = &sleepServe{root: p.Root, up: true}
	s = &sleeper{Cfg: cfg, Env: env, Cwd: p.Root, Out: out, Why: "obor stop", Wait: 300 * time.Millisecond,
		H: &herdr.Socket{Path: herdrtest.Serve(t, p.Herdr()).Path}, Ask: f.ask, Call: f.call,
		Stop: func(_ context.Context, agent, pane string, force bool) (string, error) {
			*stops = append(*stops, stopCall{agent, pane, force})
			return "STOPPED " + agent + " (pane " + pane + ")", nil
		}}
	return p, s, out, stops, f
}

// nothingDone: no marker, the project still registered, no agent stopped, no pause left in force.
func nothingDone(t *testing.T, p *testkit.Project, s *sleeper, stops []stopCall, f *sleepServe) {
	t.Helper()
	if project.Stopped(p.Root) || len(stops) != 0 {
		t.Errorf("marker %v, stops %v: want nothing done", project.Stopped(p.Root), stops)
	}
	if _, ok := project.RootOf("proj", s.Env); !ok {
		t.Error("the project left the machine's list")
	}
	if f.paused != nil {
		t.Errorf("pause %q left in force", f.paused.Why)
	}
}

// pausedThenCleared: the stop set its pause and, refusing, cleared it.
func pausedThenCleared(t *testing.T, f *sleepServe, out *bytes.Buffer) {
	t.Helper()
	if len(f.calls) < 2 || f.calls[0] != "pause "+stopPause || f.calls[len(f.calls)-1] != "pause --clear" ||
		!strings.Contains(out.String(), "pause: PAUSE CLEARED") {
		t.Errorf("calls %q: want the stop's pause first and its clear last\n%s", f.calls, out)
	}
}

// launchdServe makes serve launchd's (darwin; the fake launchctl answers pid 4242) or, with pid not 4242, one in a pane:
// its plist installed and loaded, serve.flock naming pid (not held: the process is gone once asked).
func launchdServe(t *testing.T, s *sleeper, state string, pid string) service.Service {
	t.Helper()
	svc := serviceOf(s.Cfg, s.Env)
	if svc.Supported() {
		if err := os.MkdirAll(filepath.Dir(svc.PlistPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.PlistPath(), svc.Plist(), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(app.FlockFile(s.Cfg.Root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.FlockFile(s.Cfg.Root), []byte(pid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return svc
}

// T-710 (RV-84 #1, #2): obor stop from a worker lane's cwd is refused before anything is asked or sent — with no
// OBOR_ROLE (the owner's identity otherwise) and with OBOR_ROLE=lead (the cwd wins) — and so is OBOR_ROLE=worker in
// the main checkout.
func TestStopRefusedFromAWorker(t *testing.T) {
	t.Parallel()
	p, s, out, stops, f := sleepFx(t)
	f.tasks = []wire.TaskStatus{{Task: "T-02", Lane: "L-01", State: "working"}}
	s.Force = true
	for _, c := range []struct {
		name, cwd, role, want string
	}{
		{"lane cwd, no OBOR_ROLE", p.LaneDir("L-01"), "", "(lane L-01)"},
		{"lane cwd, OBOR_ROLE=lead", p.LaneDir("L-01"), "lead", "(lane L-01)"},
		{"main checkout, OBOR_ROLE=worker", p.Root, "worker", "(OBOR_ROLE=worker)"},
	} {
		out.Reset()
		s.Cwd, s.Env = c.cwd, p.Env("PATH="+os.Getenv("PATH"), "OBOR_ROLE="+c.role)
		if rc := s.stop(context.Background()); rc != 1 ||
			!strings.Contains(out.String(), "FAIL obor stop is the owner's or the lead's, not a worker's "+c.want) {
			t.Errorf("%s: exit %d\n%s", c.name, rc, out)
		}
		if f.asks != 0 || len(f.calls) != 0 {
			t.Errorf("%s: serve asked %d times, calls %q: want nothing sent", c.name, f.asks, f.calls)
		}
		nothingDone(t, p, s, *stops, f)
	}
}

// T-710 (RV-84 #3): serve down, obor stop refuses (start serve first) and does nothing: no offline guess at the tasks.
func TestStopRefusedWhileServeIsDown(t *testing.T) {
	t.Parallel()
	p, s, out, stops, f := sleepFx(t)
	f.up = false
	s.Force = true
	if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), "start serve first") {
		t.Errorf("serve down: exit %d\n%s", rc, out)
	}
	if f.asks == 0 || len(f.calls) != 0 {
		t.Errorf("asks %d, calls %q: want serve asked, nothing sent", f.asks, f.calls)
	}
	nothingDone(t, p, s, *stops, f)
}

// T-710: a waiting dispatch (no lane) refuses obor stop after its pause, which it clears; a merged task is not open.
func TestStopRefusesATaskInProgress(t *testing.T) {
	t.Parallel()
	p, s, out, stops, f := sleepFx(t)
	f.tasks = []wire.TaskStatus{{Task: "T-01", Lane: "L-01", State: "merged"}, {Task: "T-03", State: "waiting"}}
	if rc := s.stop(context.Background()); rc != 1 ||
		!strings.Contains(out.String(), "FAIL tasks in progress: T-03 (no lane, waiting)") || strings.Contains(out.String(), "T-01") {
		t.Errorf("T-03 waiting (T-01 merged): exit %d\n%s", rc, out)
	}
	pausedThenCleared(t, f, out)
	if len(f.calls) != 2 {
		t.Errorf("calls %q: want only the pause and its clear (no drop without --force)", f.calls)
	}
	nothingDone(t, p, s, *stops, f)
}

// T-711 (RV-84 #3): a timed pause in force before obor stop is replaced by stop's own untimed one; a refusal restores
// it (its why and end) while it has not expired, and clears stop's once it has.
func TestStopReplacesATimedPause(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, c := range []struct {
		name string
		at   time.Time // the refusal's now
		want string    // the last call
	}{
		{"unexpired", now, "pause owner's break"},
		{"expired", now.Add(2 * time.Hour), "pause --clear"},
	} {
		_, s, out, _, f := sleepFx(t)
		until := now.Add(time.Hour).Unix()
		f.paused = &event.Pause{Why: "owner's break", Until: until}
		f.tasks = []wire.TaskStatus{{Task: "T-03", State: "waiting"}}
		s.Now = func() time.Time { return c.at }
		if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), `pause: "owner's break" in force (until `) {
			t.Errorf("%s: exit %d\n%s", c.name, rc, out)
		}
		if len(f.calls) != 2 || f.calls[0] != "pause "+stopPause || f.calls[1] != c.want {
			t.Errorf("%s: calls %q: want stop's own pause, then %q", c.name, f.calls, c.want)
		}
		switch {
		case c.want == "pause --clear" && f.paused != nil:
			t.Errorf("%s: pause %+v left in force", c.name, f.paused)
		case c.want != "pause --clear" && (f.paused == nil || f.paused.Why != "owner's break" || f.paused.Until != until):
			t.Errorf("%s: pause %+v, want the owner's restored until %d", c.name, f.paused, until)
		}
	}
}

// T-711 (RV-84 #1): the caller's own herdr pane runs the agent of a busy lane (its label pj-l-01; its cwd the main
// checkout, OBOR_ROLE unset: the owner's identity otherwise): obor stop --force refuses before any drop, clears its
// pause and does nothing; so it does when the pane's cwd is in the lane under a label of no agent.
func TestStopRefusesTheCallersLane(t *testing.T) {
	t.Parallel()
	p, s, out, stops, f := sleepFx(t)
	fk := p.Herdr()
	s.H = &herdr.Socket{Path: herdrtest.Serve(t, fk).Path}
	fk.Agent("w1:p1", "pj-l-01", "working", p.Root).Label = "pj-l-01"
	s.Env = p.Env("PATH="+os.Getenv("PATH"), "HERDR_PANE_ID=w1:p1")
	s.Force = true
	f.tasks = []wire.TaskStatus{{Task: "T-03", State: "waiting"}, {Task: "T-02", Lane: "L-01", State: "working"}}
	if r := caller(s.Env, s.Cwd, s.Cfg); r.Role != "" {
		t.Fatalf("precondition: the caller resolves to role %q, want the owner's", r.Role)
	}
	if rc := s.stop(context.Background()); rc != 1 ||
		!strings.Contains(out.String(), "FAIL lane L-01's agent runs in this pane (w1:p1)") {
		t.Errorf("exit %d\n%s", rc, out)
	}
	pausedThenCleared(t, f, out)
	if len(f.calls) != 2 {
		t.Errorf("calls %q: want only the pause and its clear (no drop)", f.calls)
	}
	nothingDone(t, p, s, *stops, f)

	out.Reset()
	f.calls = nil
	fk.Agent("w1:p1", "", "working", p.LaneDir("L-01")).Label = "someone"
	if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), "FAIL lane L-01's agent runs in this pane") {
		t.Errorf("cwd in the lane: exit %d\n%s", rc, out)
	}
	if len(f.calls) != 2 {
		t.Errorf("cwd in the lane: calls %q: want only the pause and its clear (no drop)", f.calls)
	}
	nothingDone(t, p, s, *stops, f)
}

// T-710 (RV-84 #3): with --force a drop serve refuses refuses obor stop — no "not dropped" and go on — and clears the
// pause; nothing after it runs (no agent stopped).
func TestStopForceFailedDropRefuses(t *testing.T) {
	t.Parallel()
	p, s, out, stops, f := sleepFx(t)
	s.Force = true
	f.tasks = []wire.TaskStatus{{Task: "T-03", State: "waiting"}, {Task: "T-02", Lane: "L-01", State: "working"}}
	f.dropErr = errors.New("drop: refused")
	if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), "FAIL drop T-03 (no lane, waiting): drop: refused") {
		t.Errorf("exit %d\n%s", rc, out)
	}
	pausedThenCleared(t, f, out)
	if len(f.calls) != 3 || f.calls[1] != "drop T-03" {
		t.Errorf("calls %q: want pause, the one drop, clear", f.calls)
	}
	nothingDone(t, p, s, *stops, f)
}

// T-710 (RV-84 #6): herdr's panes not listed refuses obor stop (with --force too): no proof no agent runs; the pause
// cleared.
func TestStopRefusedWhenHerdrFails(t *testing.T) {
	t.Parallel()
	p, s, out, stops, f := sleepFx(t)
	s.Force = true
	s.H = &herdr.Socket{Path: filepath.Join(t.TempDir(), "no.sock")}
	if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), "FAIL herdr's panes not listed") {
		t.Errorf("exit %d\n%s", rc, out)
	}
	pausedThenCleared(t, f, out)
	nothingDone(t, p, s, *stops, f)
}

// paneServe makes serve one in a pane (not launchd's, any OS; on darwin a fake launchctl with no agent loaded):
// serve.flock held by this process naming pid 999 until serve's exit request, which releases it when exits.
func paneServe(t *testing.T, s *sleeper, f *sleepServe, exits bool) *project.Lock {
	t.Helper()
	fakeLaunchctl(t)
	held := project.NewLock(app.FlockFile(s.Cfg.Root))
	if ok, err := held.Try(); !ok || err != nil {
		t.Fatal(ok, err)
	}
	t.Cleanup(func() { _ = held.Unlock() })
	if err := held.Write([]byte("999\n")); err != nil {
		t.Fatal(err)
	}
	if exits {
		f.onExit = func() { _ = held.Unlock() }
	}
	return held
}

// T-711 (RV-84 #4): a serve that is not launchd's (in a pane or run by hand, any OS) is asked to exit over its socket
// and confirmed down (serve.flock released, no socket): STOPPED, the marker written, the project unregistered.
func TestStopEndsAPaneServe(t *testing.T) {
	p, s, out, _, f := sleepFx(t)
	held := paneServe(t, s, f, true)
	if !held.Held() {
		t.Fatal("precondition: serve.flock not held")
	}
	if rc := s.stop(context.Background()); rc != 0 {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if strings.Join(f.calls, "|") != "pause "+stopPause+"|exit" {
		t.Errorf("calls %q: want the pause, then the exit", f.calls)
	}
	for _, w := range []string{"serve: EXITING (pid 999)", "serve: down (pid 999: serve.flock and serve.sock released)",
		"STOPPED proj"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("no %q line:\n%s", w, out)
		}
	}
	if !project.Stopped(p.Root) {
		t.Error("no marker")
	}
	if _, ok := project.RootOf("proj", s.Env); ok {
		t.Error("the project is still in the machine's list")
	}
}

// T-711: a serve in a pane that answers its exit but keeps serve.flock is not confirmed down: refused, the pause
// cleared, no marker.
func TestStopRefusesAPaneServeThatStaysUp(t *testing.T) {
	p, s, out, _, f := sleepFx(t)
	paneServe(t, s, f, false)
	if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), "FAIL obor serve (pid 999) still holds") ||
		!strings.Contains(out.String(), "after its exit") {
		t.Errorf("exit %d\n%s", rc, out)
	}
	pausedThenCleared(t, f, out)
	if project.Stopped(p.Root) {
		t.Error("marker written")
	}
}

// T-710 (RV-84 #4): launchd's serve uninstalled that still holds serve.flock is not confirmed down: refused, no
// marker, the project still registered.
func TestStopRefusesAServeThatStaysUp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd's serve: darwin only")
	}
	p, s, out, _, _ := sleepFx(t)
	_, state := fakeLaunchctl(t)
	launchdServe(t, s, state, "4242")
	held := project.NewLock(app.FlockFile(p.Root))
	// Try writes this process's pid: put launchd's back, as the serve it stands for would have it.
	if ok, err := held.Try(); !ok || err != nil {
		t.Fatal(ok, err)
	}
	defer held.Unlock()
	if err := held.Write([]byte("4242\n")); err != nil {
		t.Fatal(err)
	}
	if rc := s.stop(context.Background()); rc != 1 || !strings.Contains(out.String(), "still holds") ||
		!strings.Contains(out.String(), "after its uninstall") {
		t.Errorf("exit %d\n%s", rc, out)
	}
	if project.Stopped(p.Root) {
		t.Error("marker written")
	}
	if _, ok := project.RootOf("proj", s.Env); !ok {
		t.Error("the project left the machine's list")
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

// T-709, T-710: obor stop's transition against fakes: serve paused, the tasks in progress dropped with --force (a
// lane by its lane, a waiting task by its id), herdr's panes (the worker and the integrator stopped; the caller's own
// pane and agent, a pane at its shell, an unknown label and another project's agent not), launchd's serve booted out
// and confirmed down, the project out of the machine's list, the marker; a second stop says it is stopped already and
// sends nothing. obor start reverses it: no marker, the agent installed again, the project in the list, the stop's
// pause cleared.
func TestStopStepsAndStart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd's serve: darwin only (off darwin obor stop refuses a serve it cannot end: TestStopRefusesAPaneServe)")
	}
	p, s, out, stops, f := sleepFx(t)
	log, state := fakeLaunchctl(t)
	o1 := p.LaneDir("L-01")
	fk := p.Herdr()
	srv := herdrtest.Serve(t, fk)
	s.H = &herdr.Socket{Path: srv.Path}
	fk.Agent("w1:p1", "pj-l-01", "idle", o1).Label = "pj-l-01"
	fk.Agent("w1:p2", "pj-int", "working", p.Root).Label = "pj-int"
	fk.Agent("w1:p3", "pj-lead", "idle", p.Root).Label = "pj-lead"               // the caller's agent (OBOR_AGENT)
	fk.Agent("w1:p9", "pj-rev", "idle", p.Root).Label = "pj-rev"                 // the caller's own pane (HERDR_PANE_ID)
	fk.Shell("w1:p4", "pj-advisor", p.Root)                                      // at its shell
	fk.Agent("w1:p5", "someone", "idle", p.Root).Label = "someone"               // no agent of this project
	fk.Agent("w1:p6", "pj-advisor2", "idle", t.TempDir()).Label = "pj-deep-dive" // another project's pane
	s.Env = p.Env("PATH="+os.Getenv("PATH"), "HERDR_PANE_ID=w1:p9", "OBOR_AGENT=pj-lead", "OBOR_ROLE=lead")
	s.Force = true
	f.tasks = []wire.TaskStatus{{Task: "T-02", Lane: "L-01", State: "gating"}, {Task: "T-01", Lane: "L-01", State: "merged"},
		{Task: "T-03", State: "waiting"}}
	svc := launchdServe(t, s, state, "4242")

	if rc := s.stop(context.Background()); rc != 0 {
		t.Fatalf("stop: exit %d\n%s", rc, out)
	}
	if want := []string{"pause " + stopPause, "drop L-01", "drop T-03"}; strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls %q, want %q (the pause kept: serve is down)", f.calls, want)
	}
	want := []stopCall{{"pj-l-01", "w1:p1", true}, {"pj-int", "w1:p2", true}}
	if len(*stops) != 2 || (*stops)[0] != want[0] || (*stops)[1] != want[1] {
		t.Errorf("agents stopped %+v, want %+v", *stops, want)
	}
	if !project.Stopped(p.Root) || !strings.Contains(p.Read(".obor/state/stopped"), "why: obor stop") {
		t.Errorf("marker: %v", project.Stopped(p.Root))
	}
	if _, ok := project.RootOf("proj", s.Env); ok {
		t.Error("the project is still in the machine's list")
	}
	b, _ := os.ReadFile(log)
	if _, err := os.Stat(svc.PlistPath()); err == nil || !strings.Contains(string(b), "bootout gui/") ||
		!strings.Contains(out.String(), "serve: down (pid 4242: serve.flock and serve.sock released)") {
		t.Errorf("launchd's serve not uninstalled and confirmed down: plist %v, calls\n%s\n%s", err, b, out)
	}
	for _, w := range []string{"DROPPED L-01", "DROPPED T-03", "pause: PAUSED until cleared"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("no %q line:\n%s", w, out)
		}
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); lines[len(lines)-1] != "STOPPED proj (files stay; obor start wakes it)" {
		t.Errorf("last line %q", lines[len(lines)-1])
	}

	// Stopped already: said, nothing asked or sent (serve is down and refuses to start on the marker).
	f.up = false
	out.Reset()
	*stops, f.calls, f.asks = nil, nil, 0
	if rc := s.stop(context.Background()); rc != 0 || len(*stops) != 0 || f.asks != 0 ||
		!strings.Contains(out.String(), "STOPPED proj (already: ") {
		t.Errorf("second stop: exit %d, stops %v, asks %d\n%s", rc, *stops, f.asks, out)
	}

	f.up = true // launchd's serve, installed again, answers: the stop's pause still in force
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
	plist, _ := os.ReadFile(svc.PlistPath())
	calls, _ := os.ReadFile(log)
	if string(plist) != string(svc.Plist()) || !strings.Contains(string(calls), "bootstrap gui/") {
		t.Errorf("start: launchd agent not installed\n%s", calls)
	}
	if f.paused != nil || len(f.calls) != 1 || f.calls[0] != "pause --clear" || !strings.Contains(out.String(), "pause: PAUSE CLEARED") {
		t.Errorf("start: calls %q, pause %+v: want the stop's pause cleared\n%s", f.calls, f.paused, out)
	}
}

// T-710: obor start leaves a pause that is not obor stop's.
func TestStartClearsOnlyTheStopsPause(t *testing.T) {
	_, s, out, _, f := sleepFx(t)
	fakeLaunchctl(t)
	f.paused = &event.Pause{Why: "owner's break"}
	if rc := s.start(context.Background()); rc != 0 || !strings.Contains(out.String(), "pause: none of obor stop's (already)") {
		t.Errorf("another pause: exit %d\n%s", rc, out)
	}
	if len(f.calls) != 0 || f.paused == nil {
		t.Errorf("calls %q, pause %+v: want the owner's pause kept", f.calls, f.paused)
	}
}

// T-711 (RV-84 #3, #7): a timed pause in force is replaced by obor stop's own untimed one and recorded in the marker;
// obor start with serve not answering fails (exit 1, the retry line), stop's pause still in force; its rerun, serve
// up, completes it: the owner's pause restored while it has not expired, cleared once it has; the marker gone, and a
// third start sends nothing.
func TestStopAndStartRestoreATimedPause(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name    string
		startAt time.Time
	}{{"unexpired", now}, {"expired", now.Add(2 * time.Hour)}} {
		p, s, out, _, f := sleepFx(t)
		paneServe(t, s, f, true)
		until := now.Add(time.Hour).Unix()
		f.paused = &event.Pause{Why: "owner's break", Until: until}
		s.Now = func() time.Time { return now }
		if rc := s.stop(context.Background()); rc != 0 {
			t.Fatalf("%s: stop: exit %d\n%s", c.name, rc, out)
		}
		if f.paused == nil || f.paused.Why != stopPause || f.paused.Until != 0 {
			t.Fatalf("%s: pause %+v after stop, want stop's own, untimed", c.name, f.paused)
		}
		if m := p.Read(".obor/state/stopped"); !strings.Contains(m, "pause: owner's break\n") ||
			!strings.Contains(m, fmt.Sprintf("pause_until: %d\n", until)) {
			t.Errorf("%s: marker without the replaced pause:\n%s", c.name, m)
		}

		f.up, f.calls = false, nil
		s.Now = func() time.Time { return c.startAt }
		out.Reset()
		if rc := s.start(context.Background()); rc != 1 || !strings.Contains(out.String(), "FAIL obor serve does not answer") ||
			!strings.Contains(out.String(), "obor start again") || strings.Contains(out.String(), "STARTED") {
			t.Errorf("%s: start, serve down: exit %d\n%s", c.name, rc, out)
		}
		if f.paused == nil || f.paused.Why != stopPause {
			t.Errorf("%s: pause %+v after the failed start, want stop's", c.name, f.paused)
		}
		if project.Stopped(p.Root) {
			t.Errorf("%s: the marker is in place: serve could not start", c.name)
		}
		if _, err := os.Stat(project.StartingFile(p.Root)); err != nil {
			t.Errorf("%s: no starting file for the rerun: %v", c.name, err)
		}

		f.up = true
		out.Reset()
		if rc := s.start(context.Background()); rc != 0 || !strings.Contains(out.String(), "STARTED proj") ||
			!strings.Contains(out.String(), "(an earlier obor start's)") {
			t.Fatalf("%s: start again: exit %d\n%s", c.name, rc, out)
		}
		switch c.name {
		case "unexpired":
			if f.paused == nil || f.paused.Why != "owner's break" || f.paused.Until != until ||
				!strings.Contains(out.String(), `pause: "owner's break" restored`) {
				t.Errorf("%s: pause %+v, want the owner's restored until %d\n%s", c.name, f.paused, until, out)
			}
		default:
			if f.paused != nil || len(f.calls) != 1 || f.calls[0] != "pause --clear" ||
				!strings.Contains(out.String(), "expired") {
				t.Errorf("%s: pause %+v, calls %q: want stop's cleared, the owner's not restored\n%s", c.name, f.paused,
					f.calls, out)
			}
		}
		if _, err := os.Stat(project.StartingFile(p.Root)); !os.IsNotExist(err) {
			t.Errorf("%s: starting file left: %v", c.name, err)
		}
		out.Reset()
		f.calls = nil
		if rc := s.start(context.Background()); rc != 0 || !strings.Contains(out.String(), "marker: none (already)") ||
			len(f.calls) != 0 {
			t.Errorf("%s: start a third time: exit %d, calls %q\n%s", c.name, rc, f.calls, out)
		}
	}
}

// T-709, the binary: in a stopped project whose config has a key this binary does not know, obor status (text and
// --json) loads it with the warning and the stopped line, obor whoami says it is stopped, obor config validate keeps
// refusing the key, and obor serve and obor init refuse to run in the stopped project, saying obor start. (Serve itself
// is lenient about the key, as the readers are: config.LoadDaemon, T-377; only obor config validate is strict.)
func TestStoppedProjectWithANewerConfig(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config(testkit.Set("roles.zz_future", ""))
	env := []string{"HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state",
		"HERDR_SOCKET_PATH=" + herdrtest.Serve(t, p.Herdr()).Path}
	rc, stdout, stderr := run(t, p.Root, env, "status")
	if rc != 0 || !strings.Contains(stdout, `WARN `) || !strings.Contains(stdout, `unknown field "zz_future"`) ||
		strings.Contains(stdout, project.StoppedLine) {
		t.Errorf("status (running): exit %d\n%s---\n%s", rc, stdout, stderr)
	}
	if err := project.WriteStopMarker(p.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
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
	if err := project.WriteStopMarker(p.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
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
