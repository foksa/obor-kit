package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// leadFx is a project (prefix pj, lane type opus: claude --model opus, roles.lead.type opus) and a fake herdr with
// no panes.
type leadFx struct {
	p     *testkit.Project
	f     *herdrtest.Fake
	srv   *herdrtest.Server // f over the socket: what `obor lead` itself asks
	env   []string          // more env ("VAR=value")
	spawn [][]string        // the detached runs started
}

func newLeadFx(t *testing.T, edits ...func(map[string]any)) *leadFx {
	t.Helper()
	p := testkit.New(t)
	p.Config(append([]func(map[string]any){
		testkit.Set("lane_types", `{"opus": {"kind": "claude", "args": ["--model", "opus"]}}`),
		testkit.Set("roles.lead", `{"type": "opus"}`),
	}, edits...)...)
	f := herdrtest.New()
	return &leadFx{p: p, f: f, srv: herdrtest.Serve(t, f)}
}

// launcher tunes a launcher for tests: fast waits, a ps with no stopped job, detached runs recorded.
func (x *leadFx) launcher(l *launch.Launcher) {
	l.Wait, l.Poll, l.Sched = time.Second, time.Millisecond, time.Second
	l.PS = func(args ...string) ([]byte, error) { return nil, nil }
	l.Spawn = func(dir string, args []string, out string) error { x.spawn = append(x.spawn, args); return nil }
}

// run is `obor lead <args>` in the main checkout: its output and exit code.
func (x *leadFx) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	r := leadRun{dir: x.p.Root, kit: x.p.Base, env: x.p.Env(x.env...), h: x.srv.Client(), out: &out, tune: x.launcher}
	rc := r.run(context.Background(), args)
	return out.String(), rc
}

// calls is what herdr was asked: the socket requests (method and params, as sent).
func (x *leadFx) calls() string {
	var all []string
	for _, r := range x.srv.Requests() {
		all = append(all, strings.TrimSpace(r.Method+" "+string(r.Params)))
	}
	return strings.Join(all, "\n")
}

// startCall is the agent.start request for name in pane with args, as the socket client sends it.
func startCall(name, pane string, args ...string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "kind": "claude", "pane_id": pane, "args": args})
	return "agent.start " + string(b)
}

var leadStart = startCall("pj-lead", "w1:p1", "--model", "opus") // no Remote Control default (T-280)

func TestLeadRunningTouchesNothing(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	x.f.Agent("w1:p1", "pj-lead", "working", x.p.Root).Label = "pj-lead"
	for _, args := range [][]string{nil, {"--fresh"}, {"--resume"}} {
		out, rc := x.run(t, args...)
		if rc != 0 || lastLine(out) != "OK pj-lead runs (pane w1:p1)" {
			t.Errorf("running lead %v: exit %d\n%s", args, rc, out)
		}
	}
	for _, c := range strings.Split(x.calls(), "\n") {
		if c != "agent.list {}" {
			t.Errorf("running lead: only the agent list asked, no input sent: %q", x.calls())
		}
	}
}

// T-553: `obor lead` starts fresh; --resume continues the last session; --fresh still means the default.
func TestLeadLabelledPaneSession(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args  []string
		want  string
		start []string // the agent.start args
	}{
		{nil, "RESTARTED pj-lead (pane w1:p1, session new)", []string{"--model", "opus"}},
		{[]string{"--fresh"}, "RESTARTED pj-lead (pane w1:p1, session new)", []string{"--model", "opus"}},
		{[]string{"--resume"}, "RESTARTED pj-lead (pane w1:p1, session old-sid)", []string{"--model", "opus", "--resume", "old-sid"}},
	} {
		x := newLeadFx(t)
		x.f.Shell("w1:p1", "pj-lead", x.p.Root).Session = "old-sid" // herdr still knows the ended session
		out, rc := x.run(t, c.args...)
		if rc != 0 || lastLine(out) != c.want {
			t.Fatalf("labelled pane %v: exit %d\n%s", c.args, rc, out)
		}
		call := startCall("pj-lead", "w1:p1", c.start...)
		if calls := x.calls(); !strings.Contains(calls, call) || strings.Contains(calls, "tab create") {
			t.Errorf("labelled pane %v: starts %s in pane w1:p1, no new tab:\n%s", c.args, call, calls)
		}
		if len(x.spawn) != 0 {
			t.Errorf("labelled pane %v: not scheduled: %v", c.args, x.spawn)
		}
	}
}

// From the lead's own pane at its shell obor lead itself has the foreground, and herdr starts an agent only at the
// shell prompt: the start runs detached once obor lead has exited (T-553: fresh unless --resume).
func TestLeadOwnPaneStartsOnceItExits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "restart pj-lead --pane w1:p1 --fresh"},
		{[]string{"--fresh"}, "restart pj-lead --pane w1:p1 --fresh"},
		{[]string{"--resume"}, "restart pj-lead --pane w1:p1"},
	} {
		x := newLeadFx(t)
		x.f.Shell("w1:p1", "pj-lead", x.p.Root).Session = "old-sid"
		x.env = []string{"HERDR_PANE_ID=w1:p1"}
		x.srv.Reply("pane.process_info", map[string]any{"type": "pane_process_info", "process_info": map[string]any{
			"pane_id": "w1:p1", "shell_pid": 1, "foreground_process_group_id": 77,
			"foreground_processes": []any{map[string]any{"pid": 77, "argv0": "obor"}}}}) // obor lead itself has the foreground
		var buf bytes.Buffer
		r := leadRun{dir: x.p.Root, kit: x.p.Base, env: x.p.Env(x.env...), h: x.srv.Client(), out: &buf, tune: x.launcher}
		rc := r.run(context.Background(), c.args)
		out := buf.String()
		if rc != 0 || lastLine(out) != "STARTING pj-lead in this pane (w1:p1) once obor lead exits" || strings.Contains(out, "SCHEDULED") {
			t.Fatalf("own pane %v: exit %d\n%s", c.args, rc, out)
		}
		if len(x.spawn) != 1 || strings.Join(x.spawn[0], " ") != c.want {
			t.Errorf("own pane %v: one detached restart %q: %v", c.args, c.want, x.spawn)
		}
		if calls := x.calls(); strings.Contains(calls, "agent.start") || strings.Contains(calls, "pane.send_input") {
			t.Errorf("nothing typed while obor lead has the foreground:\n%s", calls)
		}
	}
}

func TestLeadNoPaneSetsUp(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	out, rc := x.run(t)
	if rc != 0 || lastLine(out) != "OK lead: pj-lead in pane w1:n1" {
		t.Fatalf("no pane: exit %d\n%s", rc, out)
	}
	c := x.calls()
	if !strings.Contains(c, `tab.create {"cwd":"`+x.p.Root+`","env":{"OBOR_AGENT":"pj-lead","OBOR_ROLE":"lead"},"focus":false,"label":"lead"}`) ||
		!strings.Contains(c, startCall("pj-lead", "w1:n1", "--model", "opus")) || strings.Contains(c, "--resume") {
		t.Errorf("obor setup role lead --new-tab lead, fresh:\n%s", c)
	}
}

// RV-51 #4: only a new pane needs roles.lead.type.
func TestLeadNoRoleTypeFailsOnlyForANewPane(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t, testkit.Set("roles.lead", `{}`))
	out, rc := x.run(t)
	if rc != 1 || !strings.HasPrefix(lastLine(out), "FAIL roles.lead.type is not set") {
		t.Errorf("no roles.lead.type, no pane: exit %d\n%s", rc, out)
	}
	if c := x.calls(); strings.Contains(c, "tab create") || strings.Contains(c, "agent.start") {
		t.Errorf("no roles.lead.type: no tab, no start:\n%s", c)
	}
}

func TestLeadUntypedRunningOrLabelled(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t, testkit.Set("roles.lead", `{"agent": "my-lead"}`))
	x.f.Agent("w1:p1", "my-lead", "working", x.p.Root).Label = "my-lead"
	if out, rc := x.run(t); rc != 0 || lastLine(out) != "OK my-lead runs (pane w1:p1)" {
		t.Errorf("untyped running lead: exit %d\n%s", rc, out)
	}

	x = newLeadFx(t, testkit.Set("roles.lead", `{"agent": "my-lead"}`))
	x.f.Shell("w1:p1", "my-lead", x.p.Root).Session = "old-sid"
	out, rc := x.run(t, "--resume")
	if rc != 0 || lastLine(out) != "RESTARTED my-lead (pane w1:p1, session old-sid)" {
		t.Fatalf("untyped labelled pane: exit %d\n%s", rc, out)
	}
	if c := x.calls(); !strings.Contains(c, `"kind":"claude","name":"my-lead","pane_id":"w1:p1"`) ||
		!strings.Contains(c, `"--resume","old-sid"`) || strings.Contains(c, "tab create") {
		t.Errorf("untyped labelled pane: resumes old-sid in w1:p1 as claude, no new tab:\n%s", c)
	}
}

// RV-51 #5: agent names are machine-wide; another project's pj-lead is not this project's lead.
func TestLeadNamesakeInAnotherProjectFails(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	other := t.TempDir()
	x.f.Agent("w1:p1", "pj-lead", "working", other).Label = "pj-lead"
	out, rc := x.run(t)
	if rc != 1 || !strings.HasPrefix(lastLine(out), "FAIL pj-lead runs in pane w1:p1 with cwd "+other+", outside ") {
		t.Fatalf("namesake elsewhere: exit %d\n%s", rc, out)
	}
	if c := x.calls(); strings.Contains(c, "agent.start") || strings.Contains(c, "tab create") ||
		strings.Contains(c, "pane.send_input") || strings.Contains(c, "stop") {
		t.Errorf("namesake elsewhere: nothing started, typed or stopped:\n%s", c)
	}

	x = newLeadFx(t) // no cwd known: taken as ours, as the launcher does
	x.f.Agent("w1:p1", "pj-lead", "working", "").Label = "pj-lead"
	if out, rc := x.run(t); rc != 0 || lastLine(out) != "OK pj-lead runs (pane w1:p1)" {
		t.Errorf("no cwd known: exit %d\n%s", rc, out)
	}
}

func TestLeadUsage(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	for _, args := range [][]string{{"--fres"}, {"lead"}, {"--fresh2"}, {"--resume", "--nope"}, {""}} {
		if out, rc := x.run(t, args...); rc != 1 || lastLine(out) != "FAIL "+leadUsage {
			t.Errorf("obor lead %q: exit %d\n%s", args, rc, out)
		}
	}
	if c := x.calls(); c != "" {
		t.Errorf("refused before herdr is asked: %q", c)
	}
}
