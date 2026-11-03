package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// T-466, T-522, T-646: obor send <project> "<text>" is one send request, as the caller, to the target's serve: the
// target by a known name (obor projects), lead@<name> (the same thing), or a path into it; its serve on <target
// root>/.obor/state/serve.sock. Usage, the own project, an unknown name, a path outside an obor project and a path
// that is not a directory send nothing (FAIL, exit 1); serve's refusal is FAIL.
func TestSendRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	q := testkit.New(t)
	q.Config(testkit.Set("name", "other"), testkit.Set("prefix", "ot"))
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
		{[]string{"send"}, `FAIL usage: obor send <project> "<text>"`},
		{[]string{"send", "other"}, `FAIL usage: obor send <project> "<text>"`},
		{[]string{"send", "other", " "}, `FAIL usage: obor send <project> "<text>"`},
		{[]string{"send", p.Root, "hi"}, "FAIL obor send goes to another project's lead, not this one"},
		{[]string{"send", "other", "hi"}, unknownOther},
		{[]string{"send", "lead@other", "hi"}, unknownOther},
		{[]string{"send", p.Base, "hi"}, "FAIL " + p.Base + " is not in an obor project (no .obor/config.json)"},
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
		if c := calls[i]; target.methods[i] != wire.MethodSend || c.Root != q.Root || c.From != "proj" ||
			c.Note != "v0.12 is out" || c.Role != "lead" || c.Agent != "pj-lead" || c.CallID == "" {
			t.Errorf("send by %s: request %s %+v", by, target.methods[i], c)
		}
	}
	if calls, _ := own.requests(); len(calls) != 0 {
		t.Errorf("the own project's serve got %+v", calls)
	}

	startFakeServe(t, q.Root, wire.Status{}, "", "send: a lead or the owner sends, not pj-l1 (role worker)")
	if rc, stdout, _ := run(t, p.Root, env, "send", "other", "x"); rc != 1 ||
		stdout != "FAIL send: a lead or the owner sends, not pj-l1 (role worker)\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}

// T-767: obor send reaches a project whose config this binary cannot load (another release's shape): sending needs the
// target's root, and the target's serve loads its own config. By a known name and by a path into it.
func TestSendToProjectWhoseConfigDoesNotLoad(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	q := testkit.New(t)
	q.Config(testkit.Set("name", "other"), testkit.Set("prefix", "ot"))
	q.Write("sub/x.txt", "x\n")
	// The shape before T-765: this binary refuses it, a serve of the version that wrote it does not.
	q.Write(".obor/config.json", `{"name":"other","prefix":"ot","lane_types":{"opus":{"kind":"claude"}}}`+"\n")
	if _, err := config.LoadDaemon(q.Root); err == nil {
		t.Fatal("precondition: the target's old-shape config loads")
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
		if c := calls[i]; target.methods[i] != wire.MethodSend || c.Root != q.Root || c.From != "proj" ||
			c.Note != "v0.22 is out" {
			t.Errorf("send by %s: request %s %+v", by, target.methods[i], c)
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

// T-466: obor brief <role-agent> "<text>" is one brief request with the text; not a role agent (the lead, the
// integrator, a worker, nobody) or no text sends nothing.
func TestBriefRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("L-01", "")
	p.Config(testkit.Set("roles.reviewer", ""))
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "BRIEFED pj-rev (typed when its pane is idle; the lead hears when it finishes)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"brief", "pj-rev"}, `FAIL usage: obor brief <role-agent> "<text>"`},
		{[]string{"brief", "pj-rev", ""}, `FAIL usage: obor brief <role-agent> "<text>"`},
		{[]string{"brief", "pj-lead", "x"}, "FAIL pj-lead is no role agent"},
		{[]string{"brief", "pj-int", "x"}, "FAIL pj-int is no role agent"},
		{[]string{"brief", "pj-l-01", "x"}, "FAIL pj-l-01 is no role agent"},
		{[]string{"brief", "nobody", "x"}, "FAIL nobody is no role agent"},
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
	if len(calls) != 1 || serve.methods[0] != wire.MethodBrief || calls[0].To != "pj-rev" || calls[0].Note != "verify finding 3" ||
		calls[0].Root != p.Root || calls[0].Role != "lead" {
		t.Errorf("requests %v %+v", serve.methods, calls)
	}
}

// T-466: obor restart <role-agent> --fresh --prompt "<brief>" records the brief (a brief request without a text)
// once the restart succeeded; a worker's fresh start with a prompt records none; serve down is a note, not a failure.
func TestRestartFreshPromptRecordsTheBrief(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "")
	p.Config(testkit.Set("roles.reviewer", "opus"), testkit.Set("roles.deep_dive", "opus"), testkit.Set("roles.advisor", "opus"))
	f := p.Herdr()
	f.Shell("w1:p3", "pj-rev", p.Root)
	f.Shell("w1:p4", "pj-deep-dive", p.Root)
	f.Shell("w1:p5", "pj-advisor", p.Root)
	f.Shell("w1:p1", "pj-l-01", o1)
	srv := herdrtest.Serve(t, f)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "BRIEF RECORDED for pj-rev (the lead hears when it finishes)", "")
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home",
		"OBOR_STATE_HOME=" + home, "OBOR_RESTART_WAIT=1", "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}

	rc, stdout, _ := run(t, p.Root, env, "restart", "pj-advisor", "--fresh", "--prompt", "review the module map")
	if lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n"); rc != 0 || !strings.HasPrefix(lines[len(lines)-1], "RESTARTED pj-advisor") {
		t.Fatalf("restart: exit %d\n%s", rc, stdout)
	}
	got, _ := serve.requests()
	if len(got) != 1 || serve.methods[0] != wire.MethodBrief || got[0].To != "pj-advisor" || got[0].Note != "" ||
		got[0].Prompt != "review the module map" || got[0].Role != "lead" {
		t.Errorf("requests %v %+v, want one brief of pj-advisor without a text, its start's prompt with it", serve.methods, got)
	}
	for _, a := range []string{"pj-rev", "pj-deep-dive"} {
		rc, stdout, _ := run(t, p.Root, env, "restart", a, "--fresh", "--prompt", "x")
		if want := "FAIL " + a + " takes its first prompt through obor brief " + a; rc != 1 || !strings.Contains(stdout, want) ||
			strings.Contains(stdout, "RESTARTED") {
			t.Errorf("%s restart --prompt: exit %d\n%s", a, rc, stdout)
		}
	}
	if got, _ := serve.requests(); len(got) != 1 {
		t.Errorf("a refused restart recorded a brief: %+v", got)
	}
	if rc, stdout, _ := run(t, p.Root, env, "restart", "pj-l-01", "--fresh", "--prompt", "work"); rc != 0 {
		t.Fatalf("worker restart: exit %d\n%s", rc, stdout)
	}
	if got, _ := serve.requests(); len(got) != 1 {
		t.Errorf("a worker's fresh start recorded a brief: %+v", got)
	}
	serve.stop()
	down := append(env[:len(env):len(env)], "OBOR_STATE_HOME="+p.Base+"/none", callDown)
	rc, stdout, _ = run(t, p.Root, down, "restart", "pj-advisor", "--fresh", "--prompt", "again")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || !strings.HasPrefix(lines[len(lines)-1], "RESTARTED pj-advisor") ||
		!strings.Contains(stdout, "note: brief not recorded (the lead hears nothing when pj-advisor finishes): obor serve is not running") {
		t.Errorf("serve down: exit %d\n%s", rc, stdout)
	}
}

// T-485: the poster reads the lead's <agent>.inbox, dials its socket and writes the auth line (the record's token)
// then the user line with the text as it is, and the session reads both before the connection ends; no record, or a
// record whose socket is gone (a dial refused), posts nothing (began false); inboxed is true only for a record whose
// socket is there.
func TestPosterPostsToTheInbox(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	env := testkit.MapEnv(map[string]string{"OBOR_STATE_HOME": filepath.Join(dir, "state")})
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c) // to the poster's half-close
		c.Close()
		got <- string(b)
	}()
	p := poster{env}
	ctx := context.Background()

	if began, err := p.Post(ctx, agents.Default, "pj-lead", "x"); began || err == nil || !strings.Contains(err.Error(), "no session recorded for pj-lead") {
		t.Errorf("no record: began %v, %v; want refused before a byte", began, err)
	}
	if _, ok := inboxed(env)("pj-lead"); ok {
		t.Error("inboxed with no record")
	}
	if err := project.WriteInbox("pj-lead", project.Inbox{Socket: sock, Token: "tok-1", Session: "s1"}, env); err != nil {
		t.Fatal(err)
	}
	if s, ok := inboxed(env)("pj-lead"); !ok || s != "s1" {
		t.Errorf("inboxed = %q, %v; want the recording session s1 with a record whose socket is there", s, ok)
	}
	const line = `[obor other] release "v0.12" is out`
	if began, err := p.Post(ctx, agents.Default, "pj-lead", line); !began || err != nil {
		t.Fatalf("post: began %v, %v", began, err)
	}
	want := `{"type":"auth","token":"tok-1"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"[obor other] release \"v0.12\" is out"}}` + "\n"
	select {
	case b := <-got:
		if b != want {
			t.Errorf("the session read %q, want %q", b, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session read nothing")
	}

	if err := project.WriteInbox("pj-lead", project.Inbox{Socket: filepath.Join(dir, "gone.sock")}, env); err != nil {
		t.Fatal(err)
	}
	if began, err := p.Post(ctx, agents.Default, "pj-lead", line); began || err == nil {
		t.Errorf("a dead socket: began %v, %v; want refused before a byte", began, err)
	}
	if _, ok := inboxed(env)("pj-lead"); ok {
		t.Error("inboxed with a record whose socket is gone")
	}
}

// DD-v0.13 B1: poster.Post posts with the kind it is given (the pane's), not the first kind with an inbox: the same record
// is posted for a kind with an inbox, and refused before a byte (nothing dialled) for a kind without one or a kind
// obor has no bridge for. The refusals name no kind's product ("no session recorded").
func TestPosterUsesTheGivenKind(t *testing.T) {
	t.Parallel()
	var withInbox, without string
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		if k.Caps().Inbox && withInbox == "" {
			withInbox = name
		} else if !k.Caps().Inbox && without == "" {
			without = name
		}
	}
	if withInbox == "" || without == "" {
		t.Fatalf("kinds %v: want one with an inbox and one without", agents.Names())
	}
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	env := testkit.MapEnv(map[string]string{"OBOR_STATE_HOME": filepath.Join(dir, "state")})
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	dials := make(chan struct{}, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials <- struct{}{}
			_, _ = io.Copy(io.Discard, c) // read to the poster's half-close: closing first races its write (EPIPE)
			c.Close()
		}
	}()
	p := poster{env}
	ctx := context.Background()

	if began, err := p.Post(ctx, withInbox, "pj-l-01", "x"); began || err == nil ||
		!strings.Contains(err.Error(), "no session recorded for pj-l-01") || strings.Contains(err.Error(), "Claude") {
		t.Errorf("no record: began %v, %v; want refused, \"no session recorded\" naming no product", began, err)
	}
	if err := project.WriteInbox("pj-l-01", project.Inbox{Socket: sock, Token: "t", Session: "s1"}, env); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{without, "pi"} {
		if began, err := p.Post(ctx, kind, "pj-l-01", "x"); began || err == nil {
			t.Errorf("kind %s: began %v, %v; want refused before a byte", kind, began, err)
		}
	}
	select {
	case <-dials:
		t.Fatal("a kind without an inbox dialled the record's socket")
	case <-time.After(100 * time.Millisecond):
	}
	if began, err := p.Post(ctx, withInbox, "pj-l-01", "x"); !began || err != nil {
		t.Fatalf("kind %s: began %v, %v; want posted", withInbox, began, err)
	}
	select {
	case <-dials:
	case <-time.After(5 * time.Second):
		t.Fatal("the kind with an inbox dialled nothing")
	}
}
