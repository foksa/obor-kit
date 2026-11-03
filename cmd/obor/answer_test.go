package main

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// obor answer <T-xx> "<text>": one request to obor serve carrying the task and the answer as the caller (caller: the
// lead from the main checkout, the owner with no role, a worker from its lane's cwd whatever OBOR_ROLE says), serve's
// reply printed; a usage error or a bad task id sends nothing; serve's refusal is FAIL, exit 1.
func TestAnswerRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "ANSWERED T-01 (attempt 2): obor serve sends the worker the answer", "")
	env := func(role string) []string {
		return []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=" + role, "OBOR_AGENT=pj-lead", "OBOR_LANE="}
	}
	for _, args := range [][]string{{"answer"}, {"answer", "T-01"}, {"answer", "T-01", ""}, {"answer", "T-01", "a", "b"}} {
		if rc, stdout, _ := run(t, p.Root, env("lead"), args...); rc != 1 || !strings.Contains(stdout, `FAIL usage: obor answer <T-xx> "<text>"`) {
			t.Errorf("obor %q: exit %d, %q; want the usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env("lead"), "answer", "X-1", "yes"); rc != 1 || !strings.Contains(stdout, "X-1 is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}

	rc, stdout, _ := run(t, p.Root, env("lead"), "answer", "T-01", "yes, edit x.go")
	if rc != 0 || stdout != "ANSWERED T-01 (attempt 2): obor serve sends the worker the answer\n" {
		t.Errorf("answer: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env(""), "answer", "T-01", "as the owner")
	run(t, o1, env("lead"), "answer", "T-01", "from a lane")
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	for i, want := range []struct{ role, note string }{{"lead", "yes, edit x.go"}, {"", "as the owner"}, {"worker", "from a lane"}} {
		c := calls[i]
		if c.Task != "T-01" || c.Note != want.note || c.Role != want.role || c.Agent != "pj-lead" || c.Root != p.Root ||
			c.CallID == "" {
			t.Errorf("request %d: %+v; want task T-01, note %q, role %q, agent pj-lead, root, a call id", i+1, c, want.note,
				want.role)
		}
	}
	if calls[0].CallID == calls[1].CallID {
		t.Error("two answers share a call id")
	}

	home2, _ := startFakeServe(t, p.Root, wire.Status{}, "", "answer: T-01 has no open question")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "answer", "T-01", "yes"); rc != 1 ||
		stdout != "FAIL answer: T-01 has no open question\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}
