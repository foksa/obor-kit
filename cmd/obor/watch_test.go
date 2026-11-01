package main

import (
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/testkit"
)

// T-523: obor watch <T-xx> [<T-yy> …] is one watch request per task as the caller, serve's reply printed for each;
// no task is one request for the list; --rm <T-xx> one request that ends the watch. A usage error or a bad task id
// sends nothing; those and serve's refusal are FAIL on stdout, exit 1.
func TestWatchRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, app.Status{}, "WATCHING", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"watch", "--rm"}, {"watch", "--rm", "T-1", "T-2"}, {"watch", "--all"},
		{"watch", "T-1", "--rm"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor watch") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "watch", "nope"); rc != 1 || !strings.HasPrefix(stdout, "FAIL nope is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}

	if rc, stdout, _ := run(t, p.Root, env, "watch", "T-1", "T-2"); rc != 0 || stdout != "WATCHING\nWATCHING\n" {
		t.Errorf("watch T-1 T-2: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env, "watch")
	run(t, p.Root, env, "watch", "--rm", "T-2")
	calls, _ := serve.requests()
	if len(calls) != 4 {
		t.Fatalf("%d requests, want 4", len(calls))
	}
	serve.mu.Lock()
	methods := append([]string(nil), serve.methods...)
	serve.mu.Unlock()
	for i, m := range methods {
		if m != app.MethodWatch {
			t.Errorf("request %d: method %q, want %q", i+1, m, app.MethodWatch)
		}
	}
	for i, want := range []app.Call{{Task: "T-1"}, {Task: "T-2"}, {}, {Task: "T-2", Clear: true}} {
		c := calls[i]
		if c.Task != want.Task || c.Clear != want.Clear || c.Role != "lead" || c.Agent != "pj-lead" || c.Root != p.Root ||
			c.CallID == "" {
			t.Errorf("request %d: %+v, want task %q clear %v", i+1, c, want.Task, want.Clear)
		}
	}
	if calls[0].CallID == calls[1].CallID {
		t.Error("two tasks' requests share a call id")
	}

	home2, refusing := startFakeServe(t, p.Root, app.Status{}, "", "watch: unknown task T-9 (no dispatch or spec in proj)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "watch", "T-9"); rc != 1 ||
		stdout != "FAIL watch: unknown task T-9 (no dispatch or spec in proj)\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
	refusing.stop()
}

// T-528: each argument of obor watch is a task or a comma list, one request each with the list as its task; --rm
// takes a comma list; a bad id in a list sends nothing.
func TestWatchSetRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, app.Status{}, "WATCHING", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	if rc, stdout, _ := run(t, p.Root, env, "watch", "T-1,nope"); rc != 1 || !strings.HasPrefix(stdout, "FAIL nope is no task id") {
		t.Errorf("a bad task id in a list: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}
	if rc, stdout, _ := run(t, p.Root, env, "watch", "T-1,T-2,T-3", "T-4"); rc != 0 || stdout != "WATCHING\nWATCHING\n" {
		t.Errorf("watch T-1,T-2,T-3 T-4: exit %d, %q", rc, stdout)
	}
	if rc, _, _ := run(t, p.Root, env, "watch", "--rm", "T-1,T-2,T-3"); rc != 0 {
		t.Errorf("--rm of a list: exit %d", rc)
	}
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	for i, want := range []app.Call{{Task: "T-1,T-2,T-3"}, {Task: "T-4"}, {Task: "T-1,T-2,T-3", Clear: true}} {
		if c := calls[i]; c.Task != want.Task || c.Clear != want.Clear {
			t.Errorf("request %d: task %q clear %v, want %q %v", i+1, c.Task, c.Clear, want.Task, want.Clear)
		}
	}
}

// T-666: --note "<text>" goes with a watch (one request per set, each carrying the note); a note with no set, with no
// text or with --rm is a usage error, as is a note before a missing text; nothing is sent then.
func TestWatchNoteFlag(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, app.Status{}, "WATCHING", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"watch", "--note"}, {"watch", "--note", "restart serve"},
		{"watch", "T-1", "--note"}, {"watch", "--rm", "T-1", "--note", "restart serve"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor watch") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}
	const note = "restart serve, then release v0.17.4"
	if rc, stdout, _ := run(t, p.Root, env, "watch", "T-1,T-2", "T-3", "--note", note); rc != 0 ||
		stdout != "WATCHING\nWATCHING\n" {
		t.Errorf("watch T-1,T-2 T-3 --note: exit %d, %q", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 2 {
		t.Fatalf("%d requests, want 2", len(calls))
	}
	for i, want := range []string{"T-1,T-2", "T-3"} {
		if c := calls[i]; c.Task != want || c.Note != note || c.Clear {
			t.Errorf("request %d: task %q note %q clear %v, want %q with the note", i+1, c.Task, c.Note, c.Clear, want)
		}
	}
}
