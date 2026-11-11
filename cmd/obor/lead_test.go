package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/project"
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
	p := testkit.NewPlain(t)
	p.Config(append([]func(map[string]any){
		testkit.Set("agents.roles.lead", "opus"),
	}, edits...)...)
	f := herdrtest.New()
	return &leadFx{p: p, f: f, srv: herdrtest.Serve(t, f)}
}

// launcher tunes a launcher for tests: fast waits, a ps with no stopped job, detached runs recorded. A codex start
// looks for its rollout up to 10 s by default (wrong here: no Codex writes one), so the bound is a poll too.
func (x *leadFx) launcher(l *launch.Launcher) {
	l.Wait, l.Poll, l.Sched, l.Rollout = time.Second, time.Millisecond, time.Second, time.Millisecond
	l.PS = func(args ...string) ([]byte, error) { return nil, nil }
	l.Spawn = func(dir string, args []string, out string) error { x.spawn = append(x.spawn, args); return nil }
}

// run is `obor lead <args>` in the main checkout: its output and exit code.
func (x *leadFx) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	r := leadRun{dir: x.p.Root, kit: x.p.Base, env: x.p.Env(x.env...), t: fakeTerminal(x.srv.Client(), x.p.Env(x.env...)), out: &out, tune: x.launcher}
	rc := r.run(context.Background(), args)
	return out.String(), rc
}

// runWakes is run with obor start's outside systems faked: serve up or down, its agents nothing to stop (its wake
// needs no herdr). Its output carries the start's lines and the lead's, its serve the requests start made.
func (x *leadFx) runWakes(t *testing.T, up bool, args ...string) (string, int, *sleepServe) {
	t.Helper()
	cfg, err := config.Load(x.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	serve := &sleepServe{root: x.p.Root, up: up}
	wake := &sleeper{Cfg: cfg, Env: x.p.Env(x.env...), Cwd: x.p.Root, Out: &out, Wait: 300 * time.Millisecond,
		Ask: serve.ask, Call: serve.call}
	r := leadRun{dir: x.p.Root, kit: x.p.Base, env: x.p.Env(x.env...), t: fakeTerminal(x.srv.Client(), x.p.Env(x.env...)), out: &out,
		tune: x.launcher, wake: wake}
	rc := r.run(context.Background(), args)
	return out.String(), rc, serve
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

// uuidArg matches the session id obor names a fresh start with: random, so the tests write it "NEW".
var uuidArg = regexp.MustCompile(`"--session-id","[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}"`)

func noSessionID(calls string) string {
	return uuidArg.ReplaceAllString(calls, `"--session-id","NEW"`)
}

var leadStart = startCall("pj-lead", "w1:p1", "--permission-mode", "auto", "--model", "opus") // no Remote Control default

func TestLeadRunningTouchesNothing(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	x.f.Agent("w1:p1", "pj-lead", "working", x.p.Root).Label = "pj-lead"
	for _, args := range [][]string{nil, {"--resume"}} {
		out, rc := x.run(t, args...)
		if rc != 0 || lastLine(out) != "OK pj-lead runs (pane w1:p1)" {
			t.Errorf("running lead %v: exit %d\n%s", args, rc, out)
		}
	}
	for _, c := range strings.Split(x.calls(), "\n") {
		if c != "agent.list {}" && c != "pane.list {}" {
			t.Errorf("running lead: only the agents and their panes' labels asked, no input sent: %q", x.calls())
		}
	}
}

// The lead that already runs is the agent in the pane labelled with its name, whatever name the terminal runs that
// agent under: a labelled pane whose agent herdr names otherwise (or not at all) is the running lead, and an agent
// herdr names pj-lead in a pane labelled for something else is not: obor lead then opens a lead pane of its own, where
// the name herdr still holds for the relabelled pane is cleared and the new lead starts.
func TestLeadRunsByItsPaneLabel(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "someone"} {
		x := newLeadFx(t)
		x.f.Agent("w1:p1", name, "working", x.p.Root).Label = "pj-lead"
		if out, rc := x.run(t); rc != 0 || lastLine(out) != "OK pj-lead runs (pane w1:p1)" {
			t.Errorf("labelled pj-lead, herdr name %q: exit %d\n%s", name, rc, out)
		}
		if c := x.calls(); strings.Contains(c, "agent.start") || strings.Contains(c, "pane.send_input") {
			t.Errorf("labelled pj-lead, herdr name %q: nothing started or typed:\n%s", name, c)
		}
	}

	x := newLeadFx(t)
	x.f.Agent("w1:p1", "pj-lead", "working", x.p.Root).Label = "notes"
	out, rc := x.run(t)
	if rc != 0 || lastLine(out) != "OK lead: pj-lead in pane w1:n1" {
		t.Fatalf("herdr name pj-lead in a pane labelled notes: exit %d, want a new lead pane started\n%s", rc, out)
	}
	if !strings.Contains(out, "agent start: name pj-lead taken (pane w1:p1): clearing it and retrying") {
		t.Errorf("the retried start said nothing:\n%s", out)
	}
	c := x.calls()
	if !strings.Contains(c, "tab.create") || !strings.Contains(c, `"name":"pj-lead"`) {
		t.Errorf("a lead pane of its own asked for and a start tried there:\n%s", c)
	}
	if !strings.Contains(c, `"target":"w1:p1"`) {
		t.Errorf("the relabelled pane's stale name not cleared:\n%s", c)
	}
	if strings.Contains(c, `"pane_id":"w1:p1","text"`) || strings.Contains(c, `pane.send_keys {"keys":["esc"],"pane_id":"w1:p1"`) {
		t.Errorf("the relabelled pane's agent was typed at or stopped:\n%s", c)
	}
	if x.f.Pane("w1:p1").Kind == "" {
		t.Error("the relabelled pane's agent no longer runs")
	}
}

// `obor lead` starts fresh; --resume continues the recorded session (--fresh is no flag). A fresh
// start names its session itself, and herdr's session is no source of a Claude resume.
func TestLeadLabelledPaneSession(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args   []string
		record bool
		want   string
		start  []string // the agent.start args
	}{
		{nil, false, "RESTARTED pj-lead (pane w1:p1, session new)", // the lead skill is the first prompt
			[]string{"--permission-mode", "auto", "--model", "opus", "--session-id", "NEW", "/obor:lead"}},
		{[]string{"--resume"}, true, "RESTARTED pj-lead (pane w1:p1, session old-sid)",
			[]string{"--permission-mode", "auto", "--model", "opus", "--resume", "old-sid"}},
	} {
		x := newLeadFx(t)
		x.f.Shell("w1:p1", "pj-lead", x.p.Root).Session = "old-sid" // herdr still knows the ended session
		if c.record {
			x.p.Session("pj-lead", "old-sid")
		}
		out, rc := x.run(t, c.args...)
		if rc != 0 || lastLine(out) != c.want {
			t.Fatalf("labelled pane %v: exit %d\n%s", c.args, rc, out)
		}
		call := startCall("pj-lead", "w1:p1", c.start...)
		if calls := noSessionID(x.calls()); !strings.Contains(calls, call) || strings.Contains(calls, "tab create") {
			t.Errorf("labelled pane %v: starts %s in pane w1:p1, no new tab:\n%s", c.args, call, calls)
		}
		if len(x.spawn) != 0 {
			t.Errorf("labelled pane %v: not scheduled: %v", c.args, x.spawn)
		}
	}
}

// From the lead's own pane at its shell obor lead itself has the foreground, and herdr starts an agent only at the
// shell prompt: the start runs detached once obor lead has exited (fresh unless --resume).
func TestLeadOwnPaneStartsOnceItExits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want string
	}{
		// The scheduled run carries no first prompt: a fresh start of a role agent gets its role's,
		// the lead's /obor:lead.
		{nil, "restart pj-lead --pane w1:p1 --fresh"},
		{[]string{"--resume"}, "restart pj-lead --pane w1:p1"},
	} {
		x := newLeadFx(t)
		x.f.Shell("w1:p1", "pj-lead", x.p.Root).Session = "old-sid"
		x.p.Session("pj-lead", "old-sid") // the resume's source is the record, not herdr's session
		x.env = []string{"OBOR_PANE=w1:p1"}
		x.srv.Reply("pane.process_info", map[string]any{"type": "pane_process_info", "process_info": map[string]any{
			"pane_id": "w1:p1", "shell_pid": 1, "foreground_process_group_id": 77,
			"foreground_processes": []any{map[string]any{"pid": 77, "argv0": "obor"}}}}) // obor lead itself has the foreground
		var buf bytes.Buffer
		r := leadRun{dir: x.p.Root, kit: x.p.Base, env: x.p.Env(x.env...), t: fakeTerminal(x.srv.Client(), x.p.Env(x.env...)), out: &buf, tune: x.launcher}
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
	// No pane of the project anywhere: its own herdr workspace first, wn1.
	if rc != 0 || lastLine(out) != "OK lead: pj-lead in pane wn1:n2" {
		t.Fatalf("no pane: exit %d\n%s", rc, out)
	}
	c := x.calls()
	if !strings.Contains(c, `workspace.create {"cwd":"`+x.p.Root+`","focus":false,"label":"proj"}`) ||
		!strings.Contains(c, `tab.create {"cwd":"`+x.p.Root+`","env":{"OBOR_AGENT":"","OBOR_LANE":"","OBOR_OPENCODE_MODEL":"","OBOR_PANE":"","OBOR_ROLE":"lead","OPENCODE_CONFIG":"","OPENCODE_DISABLE_CLAUDE_CODE_SKILLS":""},"focus":false,"label":"lead","workspace_id":"wn1"}`) ||
		!strings.Contains(c, "export OBOR_ROLE=lead OBOR_PANE=wn1:n2 ") || // the new pane's id, before the start
		!strings.Contains(noSessionID(c), startCall("pj-lead", "wn1:n2", "--permission-mode", "auto", "--model", "opus", "--session-id", "NEW", "/obor:lead")) || strings.Contains(c, "--resume") {
		t.Errorf("obor setup role lead --new-tab lead, fresh:\n%s", c)
	}
}

// only a new pane needs roles.lead.
func TestLeadNoRoleTypeFailsOnlyForANewPane(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t, testkit.Set("agents.roles.lead", ""))
	out, rc := x.run(t)
	if rc != 1 || !strings.HasPrefix(lastLine(out), "FAIL roles.lead is not set") {
		t.Errorf("no roles.lead, no pane: exit %d\n%s", rc, out)
	}
	if c := x.calls(); strings.Contains(c, "tab create") || strings.Contains(c, "agent.start") {
		t.Errorf("no roles.lead: no tab, no start:\n%s", c)
	}
}

func TestLeadUntypedRunningOrLabelled(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t, testkit.Set("agents.roles.lead", ""))
	x.f.Agent("w1:p1", "pj-lead", "working", x.p.Root).Label = "pj-lead"
	if out, rc := x.run(t); rc != 0 || lastLine(out) != "OK pj-lead runs (pane w1:p1)" {
		t.Errorf("untyped running lead: exit %d\n%s", rc, out)
	}

	x = newLeadFx(t, testkit.Set("agents.roles.lead", ""))
	x.f.Shell("w1:p1", "pj-lead", x.p.Root).Session = "old-sid"
	x.p.Session("pj-lead", "old-sid") // a resume reads the record, never herdr's session
	out, rc := x.run(t, "--resume")
	if rc != 0 || lastLine(out) != "RESTARTED pj-lead (pane w1:p1, session old-sid)" {
		t.Fatalf("untyped labelled pane: exit %d\n%s", rc, out)
	}
	if c := x.calls(); !strings.Contains(c, `"kind":"claude","name":"pj-lead","pane_id":"w1:p1"`) ||
		!strings.Contains(c, `"--resume","old-sid"`) || strings.Contains(c, "tab create") {
		t.Errorf("untyped labelled pane: resumes old-sid in w1:p1 as claude, no new tab:\n%s", c)
	}
}

func TestLeadUsage(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	for _, args := range [][]string{{"--fres"}, {"lead"}, {"--fresh"}, {"--fresh2"}, {"--resume", "--nope"}, {""}} {
		if out, rc := x.run(t, args...); rc != 1 || lastLine(out) != "FAIL "+help.Usage("lead") {
			t.Errorf("obor lead %q: exit %d\n%s", args, rc, out)
		}
	}
	if c := x.calls(); c != "" {
		t.Errorf("refused before herdr is asked: %q", c)
	}
}

// only a Claude lead gets the skill as its first prompt, in a labelled pane and in a new tab; --resume none.
func TestLeadPromptOnlyForAFreshClaudeLead(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t, testkit.Set("agents.roles.lead", "luna"))
	x.f.Shell("w1:p1", "pj-lead", x.p.Root)
	if out, rc := x.run(t); rc != 0 || !strings.HasPrefix(lastLine(out), "RESTARTED pj-lead") {
		t.Fatalf("codex lead, labelled pane: exit %d\n%s", rc, out)
	}
	if c := x.calls(); !strings.Contains(c, "agent.start") || strings.Contains(c, "/obor:lead") {
		t.Errorf("codex lead, labelled pane: started with no /obor:lead:\n%s", c)
	}

	x = newLeadFx(t, testkit.Set("agents.roles.lead", "luna"))
	if out, rc := x.run(t); rc != 0 || lastLine(out) != "OK lead: pj-lead in pane wn1:n2" { // its own workspace
		t.Fatalf("codex lead, new tab: exit %d\n%s", rc, out)
	}
	if c := x.calls(); !strings.Contains(c, "agent.start") || strings.Contains(c, "/obor:lead") {
		t.Errorf("codex lead, new tab: started with no /obor:lead:\n%s", c)
	}
}

// a stopped project is woken before its lead: obor start's steps run first (its STARTED line, no marker,
// the project registered), then the lead starts; a start that fails ends obor lead with its error at once and starts
// no lead; a running project runs no start at all.
func TestLeadWakesAStoppedProjectFirst(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		fakeLaunchctl(t) // the start installs serve (darwin); no t.Parallel: the fake is the process's
		x := newLeadFx(t)
		if err := project.WriteStopMarker(x.p.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
			t.Fatal(err)
		}
		out, rc, serve := x.runWakes(t, true)
		if rc != 0 || !strings.HasSuffix(out, "OK lead: pj-lead in pane wn1:n2\n") || serve.asks == 0 {
			t.Fatalf("stopped: exit %d\n%s", rc, out)
		}
		started, lead := strings.Index(out, "STARTED proj (agents start on first use)"), strings.Index(out, "OK lead:")
		if started < 0 || started > lead {
			t.Errorf("start's STARTED not printed before the lead starts:\n%s", out)
		}
		if project.Stopped(x.p.Root) {
			t.Error("the marker survives: the project was not woken")
		}
		if _, ok := project.RootOf("proj", x.p.Env()); !ok {
			t.Error("the woken project is not in the machine's list")
		}
		if c := x.calls(); !strings.Contains(c, "tab.create") || !strings.Contains(c, "agent.start") {
			t.Errorf("the lead did not launch after the start:\n%s", c)
		}
	})
	t.Run("stopped, start fails", func(t *testing.T) {
		fakeLaunchctl(t)
		x := newLeadFx(t)
		if err := project.WriteStopMarker(x.p.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
			t.Fatal(err)
		}
		out, rc, _ := x.runWakes(t, false)
		if rc != 1 || !strings.Contains(out, "FAIL obor serve does not answer for ") || strings.Contains(out, "OK lead") {
			t.Fatalf("start fails: exit %d\n%s", rc, out)
		}
		if c := x.calls(); c != "" {
			t.Errorf("start failed, yet herdr was asked (a lead started?):\n%s", c)
		}
		if _, err := os.Stat(project.StartingFile(x.p.Root)); err != nil {
			t.Errorf("no starting file for obor start's rerun: %v", err)
		}
	})
	t.Run("running", func(t *testing.T) {
		x := newLeadFx(t)
		out, rc, serve := x.runWakes(t, true)
		if rc != 0 || !strings.HasSuffix(out, "OK lead: pj-lead in pane wn1:n2\n") || strings.Contains(out, "STARTED") {
			t.Fatalf("running: exit %d\n%s", rc, out)
		}
		if serve.asks != 0 {
			t.Errorf("a running project: start asked serve %d times", serve.asks)
		}
	})
}
