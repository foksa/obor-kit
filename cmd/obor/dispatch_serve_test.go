package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/testkit"
)

// fakeServe answers a project's app socket (<root>/.obor/state/serve.sock): status with st, every other request with
// reply (or err), and records the requests.
type fakeServe struct {
	mu      sync.Mutex
	calls   []wire.Call
	methods []string // the method of each call
	n       int      // requests of any method
	ln      net.Listener
}

// stop closes the socket (its file removed): serve down.
func (f *fakeServe) stop() { _ = f.ln.Close() }

// startFakeServe is a fakeServe on root's socket, and a fresh state home.
func startFakeServe(t *testing.T, root string, st wire.Status, reply, errText string) (home string, f *fakeServe) {
	t.Helper()
	return t.TempDir(), startFakeServeAt(t, wire.SocketPath(root), st, reply, errText)
}

// startFakeServeAt is startFakeServe's server listening at sock (a previous one there is no longer reached).
func startFakeServeAt(t *testing.T, sock string, st wire.Status, reply, errText string) *fakeServe {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeServe{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, _ := bufio.NewReader(c).ReadBytes('\n')
			var req struct {
				Method string     `json:"method"`
				Call   *wire.Call `json:"call"`
			}
			_ = json.Unmarshal(b, &req)
			f.mu.Lock()
			f.n++
			if req.Call != nil {
				f.calls, f.methods = append(f.calls, *req.Call), append(f.methods, req.Method)
			}
			f.mu.Unlock()
			ans := map[string]any{"reply": reply, "error": errText}
			if req.Method == "status" {
				ans = map[string]any{"status": st}
			}
			out, _ := json.Marshal(ans)
			_, _ = c.Write(append(out, '\n'))
			c.Close()
		}
	}()
	return f
}

func (f *fakeServe) requests() (calls []wire.Call, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wire.Call(nil), f.calls...), f.n
}

// servedProject is a project whose merge loop is obor serve's (an integrator role), with lane O-01; edits change the
// config further.
func servedProject(t *testing.T, edits ...func(map[string]any)) *testkit.Project {
	t.Helper()
	p := testkit.New(t)
	p.Lane("O-01", "task/T-01")
	p.Config(append([]func(map[string]any){testkit.Set("roles.integrator", map[string]any{})}, edits...)...)
	return p
}

// obor dispatch is one socket request to obor serve, the only dispatcher (T-408; as obor resume is): the level form, the
// types form, --after (T-462) and --force reach it as the call, its reply is printed; serve down is FAIL with the lane untouched; the
// options that mean nothing to a queued dispatch are usage errors; its refusal is the caller's FAIL.
func TestDispatchServeRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → O-01 (queued)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}

	for _, c := range []struct {
		name string
		args []string
		want wire.Call
	}{
		{"level", []string{"dispatch", "T-07"}, wire.Call{Task: "T-07"}},
		{"level, force", []string{"dispatch", "T-07", "--force"}, wire.Call{Task: "T-07", Force: true}},
		{"types", []string{"dispatch", "T-07", "--types", "sonnet,opus"}, wire.Call{Task: "T-07", Types: []string{"sonnet", "opus"}}},
		{"types, force first", []string{"dispatch", "--force", "--types", "opus", "T-07"},
			wire.Call{Task: "T-07", Types: []string{"opus"}, Force: true}},
		{"level, after", []string{"dispatch", "T-07", "--after", "T-05,T-06"},
			wire.Call{Task: "T-07", After: []string{"T-05", "T-06"}}},
		{"types, after first", []string{"dispatch", "--after", "T-05", "T-07", "--types", "opus"},
			wire.Call{Task: "T-07", Types: []string{"opus"}, After: []string{"T-05"}}},
	} {
		before, _ := serve.requests()
		rc, stdout, stderr := run(t, p.Root, env, c.args...)
		if rc != 0 || stdout != "DISPATCHED T-07 → O-01 (queued)\n" {
			t.Errorf("%s: exit %d, %q (stderr %q); want the reply", c.name, rc, stdout, stderr)
		}
		calls, _ := serve.requests()
		if len(calls) != len(before)+1 {
			t.Errorf("%s: %d requests, want one more than %d", c.name, len(calls), len(before))
			continue
		}
		got := calls[len(calls)-1]
		if got.CallID == "" {
			t.Errorf("%s: no call id", c.name)
		}
		got.CallID = ""
		c.want.Root, c.want.Agent, c.want.Role = p.Root, "pj-lead", "lead"
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: call %+v, want %+v", c.name, got, c.want)
		}
	}

	_, n := serve.requests()
	for _, c := range []struct {
		name string
		args []string
	}{
		{"agent", []string{"dispatch", "T-07", "--agent", "x"}},
		{"no-prompt", []string{"dispatch", "T-07", "--no-prompt"}},
		{"strict", []string{"dispatch", "T-07", "--strict"}},
		{"fresh", []string{"dispatch", "T-07", "--fresh"}},
		{"lane form (gone)", []string{"dispatch", "O-01", "T-07"}},
		{"lane form with types", []string{"dispatch", "O-01", "T-07", "--types", "opus"}},
		{"types without a task", []string{"dispatch", "--types", "opus"}},
		{"empty type", []string{"dispatch", "T-07", "--types", "opus,"}},
		{"types without a value", []string{"dispatch", "T-07", "--types"}},
		{"two tasks", []string{"dispatch", "T-07", "T-08"}},
		{"unknown flag", []string{"dispatch", "T-07", "--bogus"}},
		{"after without a value", []string{"dispatch", "T-07", "--after"}},
		{"empty after", []string{"dispatch", "T-07", "--after", "T-05,"}},
		{"after twice", []string{"dispatch", "T-07", "--after", "T-05", "--after", "T-06"}},
	} {
		rc, stdout, _ := run(t, p.Root, env, c.args...)
		if rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor dispatch <T-xx> ") {
			t.Errorf("%s: exit %d, %q; want a usage error", c.name, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "dispatch", "X-1"); rc != 1 ||
		stdout != "FAIL X-1 is no task id (task_id_pattern T-[0-9]+[a-z]?)\n" {
		t.Errorf("bad task id: exit %d, %q", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env, "dispatch", "T-07", "--after", "T-05,X-2"); rc != 1 ||
		stdout != "FAIL X-2 is no task id (task_id_pattern T-[0-9]+[a-z]?)\n" {
		t.Errorf("bad task id in --after: exit %d, %q", rc, stdout)
	}
	if _, after := serve.requests(); after != n {
		t.Errorf("a usage error or a bad task id asked serve (%d requests, %d before)", after, n)
	}

	// Serve's refusal is the caller's FAIL.
	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "", "dispatch T-07: no spec for T-07 in obor serve's store (obor spec set T-07 <path>)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "dispatch", "T-07"); rc != 1 ||
		stdout != "FAIL dispatch T-07: no spec for T-07 in obor serve's store (obor spec set T-07 <path>)\n" {
		t.Errorf("refusal: exit %d, %q", rc, stdout)
	}

	// Serve down: FAIL, no fallback: the lane is as it was.
	refusing.stop()
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), callDown}, "dispatch", "T-07"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
	if b := p.Git(p.LaneDir("O-01"), "branch", "--show-current"); b != "task/T-01" {
		t.Errorf("serve down: lane O-01 touched (on %q)", b)
	}
}

// T-408, removed by v0.10.0 and unknown since v0.12: a config that still sets dispatch.serve does not load, so obor
// config validate FAILs on it and obor dispatch asks serve nothing.
func TestDispatchObsoleteSwitch(t *testing.T) {
	t.Parallel()
	p := servedProject(t, testkit.Set("dispatch.serve", false))
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → O-01 (queued)", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	if rc, _, stderr := run(t, p.Root, env, "config", "validate"); rc != 1 || !strings.Contains(stderr, `unknown field "dispatch"`) {
		t.Errorf("validate: exit %d, stderr %q; want an unknown field FAIL", rc, stderr)
	}
	if rc, _, _ := run(t, p.Root, env, "dispatch", "T-07"); rc == 0 {
		t.Errorf("dispatch: exit 0 with an unloadable config")
	}
	if calls, _ := serve.requests(); len(calls) != 0 {
		t.Errorf("serve's requests %+v, want none", calls)
	}
}

// obor drop is one request to obor serve (T-439): the lane, the why, --discard and --force reach it as the call (the
// caller's role and agent), its reply is printed; a task id instead of a lane is the drop of that task (a waiting one,
// T-462); its refusal is the caller's FAIL; serve down is FAIL with the lane untouched; a usage error or an unknown lane
// asks serve nothing.
func TestDropServeRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DROPPED T-01 from O-01 (abandoned)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	rc, stdout, _ := run(t, p.Root, env, "drop", "O-01", "abandoned", "--discard", "--force")
	if rc != 0 || stdout != "DROPPED T-01 from O-01 (abandoned)\n" {
		t.Errorf("drop: exit %d, %q; want serve's reply", rc, stdout)
	}
	calls, n := serve.requests()
	if n != 1 || len(calls) != 1 {
		t.Fatalf("serve's requests: %d (%+v); want the one drop", n, calls)
	}
	if c := calls[0]; c.Lane != "O-01" || c.Note != "abandoned" || !c.Discard || !c.Force || c.Task != "" ||
		c.Role != "lead" || c.Agent != "pj-lead" || c.Root != p.Root || c.CallID == "" {
		t.Errorf("the drop call %+v; want lane O-01, the why, discard, force, by the lead, no task", c)
	}
	rc, stdout, _ = run(t, p.Root, env, "drop", "T-07", "not needed")
	if calls, _ := serve.requests(); rc != 0 || len(calls) != 2 || calls[1].Task != "T-07" || calls[1].Lane != "" ||
		calls[1].Note != "not needed" {
		t.Errorf("drop of a task: exit %d, %q, calls %+v; want task T-07, no lane", rc, stdout, calls)
	}
	if rc, stdout, _ := run(t, p.Root, env, "drop", "T-07", "why", "--discard"); rc != 1 ||
		stdout != "FAIL unknown lane T-07 (not in the config's lanes)\n" {
		t.Errorf("drop of a task with --discard: exit %d, %q; want refused (no lane to discard)", rc, stdout)
	}
	_, n = serve.requests()
	for _, args := range [][]string{{"drop"}, {"drop", "O-01"}, {"drop", "O-01", "why", "extra"}, {"drop", "O-01", "why", "--bad"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor drop") {
			t.Errorf("%q: exit %d, %q; want a usage error", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "drop", "O-09", "why"); rc != 1 ||
		stdout != "FAIL unknown lane O-09 (not in the config's lanes)\n" {
		t.Errorf("unknown lane: exit %d, %q", rc, stdout)
	}
	if _, after := serve.requests(); after != n {
		t.Errorf("a usage error or an unknown lane asked serve (%d requests, %d before)", after, n)
	}

	// Serve's refusal is the caller's FAIL.
	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "", "drop: lane O-01 is dirty (--discard to drop its uncommitted changes)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "drop", "O-01", "why"); rc != 1 ||
		stdout != "FAIL drop: lane O-01 is dirty (--discard to drop its uncommitted changes)\n" {
		t.Errorf("refusal: exit %d, %q", rc, stdout)
	}

	// Serve down: FAIL, the lane as it was.
	refusing.stop()
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), callDown}, "drop", "O-01", "why"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
	if b := p.Git(p.LaneDir("O-01"), "branch", "--show-current"); b != "task/T-01" {
		t.Errorf("serve down: lane O-01 moved to %q", b)
	}
}

// specs: Check wants a task id and a stored spec (T-440: the text the app read from the store, "" none; a spec in
// git counts for nothing; T-496: a files section is no longer needed); Target is the target's sha and whether the task
// branch is ahead of it.
func TestSpecs(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("O-01", "task/T-01")
	p.Config()
	const stored = "# T\n\na spec\n"
	p.Write("docs/tasks/T-5.md", stored) // committed, not stored
	p.Git(p.Root, "add", "docs/tasks/T-5.md")
	p.Git(p.Root, "commit", "-q", "-m", "spec in git")

	load := func(edits ...func(map[string]any)) specs {
		p.Config(edits...)
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		return specs{cfg: cfg, env: p.Env()}
	}
	ctx := t.Context()
	s := load()
	for _, c := range []struct {
		name, task, text string
		want             string
	}{
		{"stored spec", "T-1", stored, ""},
		{"stored spec without a files section", "T-2", "# T\n\nno files section\n", ""},
		{"no stored spec", "T-4", "", "no spec for T-4 in obor serve's store (obor spec set T-4 <path>)"},
		{"committed in git, not stored", "T-5", "", "no spec for T-5 in obor serve's store (obor spec set T-5 <path>)"},
		{"not a task id", "X-1", stored, "X-1 is no task id (task_id_pattern T-[0-9]+[a-z]?)"},
	} {
		got, err := s.Check(ctx, c.task, c.text)
		if err != nil || got != c.want {
			t.Errorf("Check %s (%s): %q, %v; want %q", c.task, c.name, got, err, c.want)
		}
	}

	head := p.Git(p.Root, "rev-parse", "main")
	if sha, ahead, err := s.Target(ctx, "T-1"); err != nil || sha != head || ahead {
		t.Errorf("Target, no branch: %q %v %v; want %q, not ahead", sha, ahead, err, head)
	}
	if sha, ahead, err := s.Target(ctx, "T-01"); err != nil || sha != head || ahead { // O-01's branch task/T-01 = main
		t.Errorf("Target, branch at main: %q %v %v; want not ahead", sha, ahead, err)
	}
	p.Commit(p.LaneDir("O-01"))
	if _, ahead, err := s.Target(ctx, "T-01"); err != nil || !ahead {
		t.Errorf("Target, branch with a commit: ahead %v, %v; want ahead", ahead, err)
	}
}

// serve's merge loop always gets the dispatch adapters (the lane adapter, the spec reader, the kit) (T-408).
func TestMergeLoopDispatchWiring(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}})(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Lane.(checks.Lane); !ok || l.Specs == nil || l.Kit == "" {
		t.Errorf("Lane %+v, Specs %v, Kit %q", l.Lane, l.Specs, l.Kit)
	}
}

// T-425: the merge loop's Checkout pauses it while the main checkout is off branches.main (another branch, a detached
// HEAD) and not on it; the dispatch target stays branches.main's sha either way.
func TestMergeLoopCheckoutPause(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}})(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if l.Checkout == nil {
		t.Fatal("no Checkout wired")
	}
	ctx := t.Context()
	main := p.Git(p.Root, "rev-parse", "main")
	check := func(name, want string) {
		t.Helper()
		err := l.Checkout(ctx)
		if (want == "") != (err == nil) || (err != nil && err.Error() != want) {
			t.Errorf("%s: Checkout %v; want %q", name, err, want)
		}
		if sha, _, err := l.Specs.Target(ctx, "T-1"); err != nil || sha != main {
			t.Errorf("%s: Target %q %v; want main's %q", name, sha, err, main)
		}
	}
	check("on main", "")
	p.Git(p.Root, "switch", "-q", "-c", "side")
	p.Commit(p.Root)
	check("on side", "main checkout is on side, not main")
	p.Git(p.Root, "switch", "-q", "--detach", "side")
	check("detached", "main checkout is on a detached HEAD, not main")
	p.Git(p.Root, "switch", "-q", "main")
	check("back on main", "")
}

// obor sendback <LANE> is a request to obor serve (T-410, RV-45 #1): the lane's open task (one status request) gets
// serve's sendback, the lead's call with the why, and serve's reply or refusal is printed. No open task, an unknown
// lane and serve down are FAIL with no sendback asked and the lane untouched.
func TestLaneSendbackServeRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	p.Commit(p.LaneDir("O-01"))
	status := func(root, state string) wire.Status {
		return wire.Status{Connected: true, Project: &wire.ProjectStatus{Root: root, Merging: true,
			Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "O-01", Attempt: 1, State: state}}}}
	}
	lane := func() string {
		return p.Git(p.LaneDir("O-01"), "rev-parse", "HEAD") + " " + p.Git(p.LaneDir("O-01"), "status", "--porcelain")
	}
	before := lane()
	for _, c := range []struct {
		name    string
		st      wire.Status
		down    bool
		args    []string
		errText string
		want    string // the output
		asked   bool   // serve was asked to send T-01 back
	}{
		{name: "awaiting", st: status(p.Root, "awaiting-accept"), want: "SENDBACK T-01 (job T-01/1/review)\n", asked: true},
		{name: "refused", st: status(p.Root, "submitted"), errText: "sendback: T-01 is submitted; retry when it settles",
			want: "FAIL sendback: T-01 is submitted; retry when it settles\n", asked: true},
		{name: "no open task", st: status(p.Root, "dropped"), want: "FAIL lane O-01 has no task open in obor serve\n"},
		{name: "another project's task", st: status(filepath.Join(p.Base, "other"), "green"),
			want: "FAIL lane O-01 has no task open in obor serve\n"},
		{name: "unknown lane", st: status(p.Root, "green"), args: []string{"sendback", "Z-09", "why"},
			want: "FAIL unknown lane Z-09 (not in the config's lanes)\n"},
		{name: "serve down", down: true, want: "FAIL obor serve is not running"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, serve := t.TempDir(), (*fakeServe)(nil)
			if !c.down {
				home, serve = startFakeServe(t, p.Root, c.st, "SENDBACK T-01 (job T-01/1/review)", c.errText)
			}
			args := c.args
			if args == nil {
				args = []string{"sendback", "O-01", "fix the edge case"}
			}
			env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
			if c.down {
				env = append(env, callDown)
			}
			rc, stdout, stderr := run(t, p.Root, env, args...)
			ok := c.asked && c.errText == ""
			if (rc == 0) != ok || !strings.HasPrefix(stdout, c.want) || stderr != "" {
				t.Errorf("exit %d, %q (stderr %q); want %q", rc, stdout, stderr, c.want)
			}
			if serve != nil {
				var sent []wire.Call
				calls, _ := serve.requests()
				for _, call := range calls {
					if call.Task != "" {
						sent = append(sent, call)
					}
				}
				want := wire.Call{Root: p.Root, Agent: "pj-lead", Role: "lead", Task: "T-01", Note: "fix the edge case"}
				switch {
				case !c.asked && len(sent) != 0:
					t.Errorf("serve was asked to send back: %+v", sent)
				case c.asked && len(sent) != 1:
					t.Errorf("serve was asked %+v; want one sendback", sent)
				case c.asked && sent[0].CallID == "":
					t.Error("no call id")
				case c.asked:
					sent[0].CallID = ""
					if !reflect.DeepEqual(sent[0], want) {
						t.Errorf("call %+v, want %+v", sent[0], want)
					}
				}
			}
			if after := lane(); after != before || p.Exists(".obor/state/dropped") {
				t.Errorf("lane O-01 touched: %q → %q", before, after)
			}
		})
	}
}
