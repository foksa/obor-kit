//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// A worker the restore restarts starts with the wake of the job the App holds for it, which the launcher asks in
// process (Launcher.Job: obor restart's own mechanism), and serve's start asks serve's socket nothing: a fake serve on
// the project's socket counts no request, the App answers where, the session, the job and the start's confirmation.
func TestRestoreRestartWakesFromTheLauncherAndDialsNoSocket(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "task/T-01")
	p.Config()
	p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
	_, sock := startFakeServe(t, p.Root, wire.Status{}, "", "")
	f := p.Herdr()
	f.Shell("w1:p1", "pj-l-01", lane)
	srv := herdrtest.Serve(t, f)
	env := p.Env("HERDR_SOCKET_PATH="+srv.Path, "CLAUDE_CONFIG_DIR="+t.TempDir(), "OBOR_RESTART_WAIT=1")
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	term, err := terminal(cfg, env)
	if err != nil {
		t.Fatal(err)
	}
	app := &fakeApp{jobs: map[string]string{"pj-l-01": "T-01/1/work"}}

	var out bytes.Buffer
	line, err := restorer(term, env, app).Restart(context.Background(), cfg,
		launch.Request{Agent: "pj-l-01", Pane: "w1:p1", Fresh: true}, &out)
	if err != nil || !strings.HasPrefix(line, "RESTARTED pj-l-01") {
		t.Fatalf("restart: %q, %v\n%s", line, err, out.String())
	}

	var start string
	for _, r := range srv.Requests() {
		if r.Method == "agent.start" {
			start = string(r.Params)
		}
	}
	if start == "" {
		t.Fatalf("precondition: no agent.start\n%s", out.String())
	}
	if wake := wire.WakeLine("T-01/1/work"); !strings.Contains(start, wake) {
		t.Errorf("the start %s does not carry the job's wake %q as its first prompt", start, wake)
	}
	if n := app.count("job " + p.Root + " pj-l-01 worker L-01"); n != 1 {
		t.Errorf("the App was asked for the worker's job %d times, want 1: %q", n, app.asked)
	}
	for _, what := range []string{"where ", "sessions ", "report ", "started "} {
		if app.count(what+p.Root+" pj-l-01") == 0 {
			t.Errorf("the App was never asked %q: %q", what, app.asked)
		}
	}
	if calls, n := sock.requests(); n != 0 {
		t.Errorf("serve's start made %d requests to serve's own socket, want none: %+v", n, calls)
	}
}

// A worker whose job read errors is restored with no wake and the log says why (the restore's rule: an error restores
// the pane); obor restart's refusal on the same error is resolveAgentJob's (TestRestartJob*).
func TestRestoreRestartsWithNoWakeWhenTheJobReadErrors(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "task/T-01")
	p.Config()
	p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
	startFakeServe(t, p.Root, wire.Status{}, "", "")
	f := p.Herdr()
	f.Shell("w1:p1", "pj-l-01", lane)
	srv := herdrtest.Serve(t, f)
	env := p.Env("HERDR_SOCKET_PATH="+srv.Path, "CLAUDE_CONFIG_DIR="+t.TempDir(), "OBOR_RESTART_WAIT=1")
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	term, err := terminal(cfg, env)
	if err != nil {
		t.Fatal(err)
	}
	app := &fakeApp{jobs: map[string]string{"pj-l-01": "T-01/1/work"}, jobErr: errors.New("store unreadable")}

	var out bytes.Buffer
	line, err := restorer(term, env, app).Restart(context.Background(), cfg,
		launch.Request{Agent: "pj-l-01", Pane: "w1:p1", Fresh: true}, &out)
	if err != nil || !strings.HasPrefix(line, "RESTARTED pj-l-01") {
		t.Fatalf("restart: %q, %v\n%s", line, err, out.String())
	}
	if n := app.count("job "); n != 1 {
		t.Fatalf("precondition: the job was read %d times, want 1: %q", n, app.asked)
	}
	var start string
	for _, r := range srv.Requests() {
		if r.Method == "agent.start" {
			start = string(r.Params)
		}
	}
	if start == "" {
		t.Fatalf("no agent.start: the pane was not restored\n%s", out.String())
	}
	if strings.Contains(start, "T-01/1/work") {
		t.Errorf("the start %s carries a wake despite the job read's error", start)
	}
	if !strings.Contains(out.String(), "store unreadable") {
		t.Errorf("the log does not say the job read failed: %q", out.String())
	}
}
