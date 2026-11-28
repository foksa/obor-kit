package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// obor send's argument check (a project and a text that is not blank) ends before the command reads its directory or
// sends a request: the rows call sendCmd.
func TestSendArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"other"}, {"other", " "}, {"", "hi"}, {"other", "hi", "x"}} {
		if rc, stdout := callCmd(t, sendCmd, args...); rc != 1 || stdout != "FAIL "+help.Usage("send")+"\n" {
			t.Errorf("send %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
}

// obor send <project> "<text>" is one send request, as the caller, to the target's serve: the
// target by a known name (obor projects), lead@<name> (the same thing), or a path into it; its serve on <target
// root>/.obor/state/serve.sock. Usage, the own project, an unknown name, a path outside an obor project and a path
// that is not a directory send nothing (FAIL, exit 1); serve's refusal is FAIL.
func TestSendRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	q := testkit.NewPlain(t)
	q.Config(testkit.Set("project.name", "other"), testkit.Set("project.agent_prefix", "ot"))
	q.Write("sub/x.txt", "x\n")
	_, own := startFakeServe(t, p.Root, wire.Status{}, "SENT (to the own project's serve)", "")
	home, err := os.MkdirTemp("", "hs") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	const unknownOther = "FAIL no obor project named other (known: none: obor serve and obor init register a project)"

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"send", p.Root, "hi"}, "FAIL obor send goes to another project's lead, not this one"},
		{[]string{"send", "other", "hi"}, unknownOther},
		{[]string{"send", "lead@other", "hi"}, unknownOther},
		// a path outside an obor project: TestSendRequestOutsideAProject (send_integration_test.go: project.Find asks git)
		{[]string{"send", filepath.Join(p.Base, "nope"), "hi"},
			"FAIL " + filepath.Join(p.Base, "nope") + " is not a directory: obor send takes a known project name " +
				"(obor projects) or a path into the target project"},
	} {
		if rc, stdout, _ := run(t, p.Root, env, c.args...); rc != 1 || strings.TrimRight(stdout, "\n") != c.want {
			t.Errorf("obor %q: exit %d, %q; want 1, %q", c.args, rc, stdout, c.want)
		}
	}
	if calls, _ := own.requests(); len(calls) != 0 {
		t.Fatalf("a refused send sent %+v", calls)
	}

	// The machine's list of obor projects by name: obor serve and obor init write it (project.Register); obor send
	// reads it. The target's own serve socket receives the request, the text as it is.
	if _, err := project.Register(q.Root, p.Env("OBOR_STATE_HOME="+home)); err != nil {
		t.Fatal(err)
	}
	if rc, stdout, _ := run(t, p.Root, env, "send", "nobody", "hi"); rc != 1 ||
		stdout != "FAIL no obor project named nobody (known: other)\n" {
		t.Errorf("an unknown name with a known one: exit %d, %q", rc, stdout)
	}
	if _, err := os.Stat(wire.SocketPath(q.Root)); err == nil {
		t.Fatal("precondition: the target has no serve socket")
	}
	_, target := startFakeServe(t, q.Root, wire.Status{}, "SENT to ot-lead in other (posted to its session)", "")
	for i, by := range []string{"other", "lead@other", filepath.Join(q.Root, "sub")} {
		if rc, stdout, _ := run(t, p.Root, env, "send", by, "v0.12 is out"); rc != 0 ||
			stdout != "SENT to ot-lead in other (posted to its session)\n" {
			t.Errorf("send by %s: exit %d, %q", by, rc, stdout)
		}
		calls, _ := target.requests()
		if len(calls) != i+1 {
			t.Fatalf("send by %s: %d requests to the target's serve, want %d", by, len(calls), i+1)
		}
		if c := calls[i]; target.sentMethods()[i] != wire.MethodSend || c.Root != q.Root || c.From != "proj" ||
			c.Note != "v0.12 is out" || c.Role != "lead" || c.Agent != "pj-lead" || c.CallID == "" {
			t.Errorf("send by %s: request %s %+v", by, target.sentMethods()[i], c)
		}
	}
	if calls, _ := own.requests(); len(calls) != 0 {
		t.Errorf("the own project's serve got %+v", calls)
	}

	startFakeServe(t, q.Root, wire.Status{}, "", "send: the lead or the owner sends, not pj-l1 (role worker)")
	if rc, stdout, _ := run(t, p.Root, env, "send", "other", "x"); rc != 1 ||
		stdout != "FAIL send: the lead or the owner sends, not pj-l1 (role worker)\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}

// obor send reaches a project whose config this binary cannot load (a broken key): sending needs the
// target's root, and the target's serve loads its own config. By a known name and by a path into it.
func TestSendToProjectWhoseConfigDoesNotLoad(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	q := testkit.NewPlain(t)
	q.Config(testkit.Set("project.name", "other"), testkit.Set("project.agent_prefix", "ot"))
	q.Write("sub/x.txt", "x\n")
	// A config this binary cannot load (a key of a type the config does not take): the target's serve loads its own.
	q.Write(".obor/config.json", `{"project":{"name":"other","agent_prefix":"ot"},"lanes":1}`+"\n")
	if _, err := config.Load(q.Root); err == nil {
		t.Fatal("precondition: the target's config loads")
	}
	home, target := startFakeServe(t, q.Root, wire.Status{}, "SENT to ot-lead in other (posted to its session)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	penv := p.Env("OBOR_STATE_HOME=" + home)
	// The list as the target's own version registered it: the entry is there, its config is not this binary's to load.
	writeProjects(t, penv, map[string]string{"other": project.Physical(q.Root)})
	for i, by := range []string{"other", filepath.Join(q.Root, "sub")} {
		if rc, stdout, _ := run(t, p.Root, env, "send", by, "v0.22 is out"); rc != 0 ||
			stdout != "SENT to ot-lead in other (posted to its session)\n" {
			t.Errorf("send by %s: exit %d, %q; want the send to the target's serve", by, rc, stdout)
		}
		calls, _ := target.requests()
		if len(calls) != i+1 {
			t.Errorf("send by %s: %d requests to the target's serve, want %d", by, len(calls), i+1)
			continue
		}
		if c := calls[i]; target.sentMethods()[i] != wire.MethodSend || c.Root != q.Root || c.From != "proj" ||
			c.Note != "v0.22 is out" {
			t.Errorf("send by %s: request %s %+v", by, target.sentMethods()[i], c)
		}
	}
	if changed, err := project.Register(q.Root, penv); err != nil || changed {
		t.Errorf("registering the target again: changed %v, %v; want its entry kept as it was", changed, err)
	}
}

// writeProjects writes the machine's list of obor projects by name into env's state home.
func writeProjects(t *testing.T, env project.Env, list map[string]string) {
	t.Helper()
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := project.ProjectsFile(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// obor brief <role-agent> "<text>" is one brief request with the text; not a role agent (the lead, the
// integrator, a worker, nobody) or no text sends nothing.
func TestBriefRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "")
	p.Config(testkit.Set("agents.roles.reviewer", ""))
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "BRIEFED pj-rev (typed when its pane is idle; the lead hears when it finishes)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"brief", "pj-rev"}, `FAIL usage: obor brief <role-agent> [--fresh] "<text>"`},
		{[]string{"brief", "pj-rev", ""}, `FAIL usage: obor brief <role-agent> [--fresh] "<text>"`},
		{[]string{"brief", "pj-lead", "x"}, "FAIL pj-lead is no role agent"},
		{[]string{"brief", "pj-int", "x"}, "FAIL pj-int is no role agent"},
		{[]string{"brief", "pj-l-01", "x"}, "FAIL pj-l-01 is no role agent"},
		{[]string{"brief", "pj-l-01-drive", "x"}, "FAIL pj-l-01-drive is no role agent"}, // a lane's drive
		{[]string{"brief", "nobody", "x"}, "FAIL nobody is no role agent"},
		// A role agent is checked before anything is stopped: --fresh stops nothing for an invalid target.
		{[]string{"brief", "pj-lead", "--fresh", "x"}, "FAIL pj-lead is no role agent"},
	} {
		if rc, stdout, _ := run(t, p.Root, env, c.args...); rc != 1 || !strings.HasPrefix(stdout, c.want) {
			t.Errorf("obor %q: exit %d, %q; want 1, %q", c.args, rc, stdout, c.want)
		}
	}
	if calls, _ := serve.requests(); len(calls) != 0 {
		t.Fatalf("a refused brief sent %+v", calls)
	}
	if rc, stdout, _ := run(t, p.Root, env, "brief", "pj-rev", "verify finding 3"); rc != 0 || !strings.HasPrefix(stdout, "BRIEFED pj-rev") {
		t.Errorf("brief: exit %d, %q", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.sentMethods()[0] != wire.MethodBrief || calls[0].To != "pj-rev" || calls[0].Note != "verify finding 3" ||
		calls[0].Root != p.Root || calls[0].Role != "lead" {
		t.Errorf("requests %v %+v", serve.sentMethods(), calls)
	}
}

// obor brief <role-agent> --fresh is one brief request with fresh set and the text; the CLI stops
// nothing and starts nothing (serve stops and starts the agent once it has recorded the brief), so a running agent
// still runs after the request, and a refused request leaves it running too. Without --fresh, fresh is not set.
func TestBriefFresh(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("agents.roles.reviewer", "opus"), testkit.Set("agents.roles.advisor", "opus"))
	f := p.Herdr()
	f.Agent("w1:p5", "pj-advisor", "working", p.Root).Label = "pj-advisor"
	srv := herdrtest.Serve(t, f)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "BRIEFED (test)", "")
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + home, "OBOR_RESTART_WAIT=1", "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	if pane := f.Pane("w1:p5"); pane.Kind == "" {
		t.Fatalf("precondition: pj-advisor runs in w1:p5: %+v", pane)
	}

	rc, stdout, _ := run(t, p.Root, env, "brief", "pj-advisor", "--fresh", "review the module map")
	if rc != 0 || strings.TrimSpace(stdout) != "BRIEFED (test)" {
		t.Fatalf("brief --fresh: exit %d\n%s", rc, stdout)
	}
	got, _ := serve.requests()
	if len(got) != 1 || serve.sentMethods()[0] != wire.MethodBrief || got[0].To != "pj-advisor" || got[0].Note != "review the module map" ||
		got[0].Role != "lead" || !got[0].Fresh {
		t.Errorf("requests %v %+v, want one fresh brief of pj-advisor with the text", serve.sentMethods(), got)
	}
	if pane := f.Pane("w1:p5"); pane.Kind == "" {
		t.Errorf("the CLI stopped pj-advisor in w1:p5: %+v", pane)
	}
	if rc, _, _ := run(t, p.Root, env, "brief", "pj-rev", "verify finding 3"); rc != 0 {
		t.Fatalf("brief: exit %d", rc)
	}
	if got, _ := serve.requests(); len(got) != 2 || got[1].To != "pj-rev" || got[1].Fresh {
		t.Errorf("requests %+v, want a second brief of pj-rev without fresh", got)
	}
	for _, r := range srv.Requests() {
		if !strings.HasSuffix(r.Method, ".get") && !strings.HasSuffix(r.Method, ".list") {
			t.Errorf("the CLI drove herdr (only reads expected): %+v", r)
		}
	}
}

// a brief --fresh that serve refuses is FAIL with serve's reason, and the running agent is not stopped.
func TestBriefFreshRefused(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("agents.roles.advisor", "opus"))
	f := p.Herdr()
	f.Agent("w1:p5", "pj-advisor", "working", p.Root).Label = "pj-advisor"
	srv := herdrtest.Serve(t, f)
	home, _ := startFakeServe(t, p.Root, wire.Status{}, "", "brief: the lead or the owner briefs, not pj-l-01 (role worker)")
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	if pane := f.Pane("w1:p5"); pane.Kind == "" {
		t.Fatalf("precondition: pj-advisor runs in w1:p5: %+v", pane)
	}
	rc, stdout, _ := run(t, p.Root, env, "brief", "pj-advisor", "--fresh", "x")
	if rc != 1 || !strings.Contains(stdout, "FAIL") || !strings.Contains(stdout, "the lead or the owner briefs") {
		t.Errorf("refused brief --fresh: exit %d, %q", rc, stdout)
	}
	if pane := f.Pane("w1:p5"); pane.Kind == "" {
		t.Errorf("a refused brief --fresh stopped pj-advisor: %+v", pane)
	}
}
