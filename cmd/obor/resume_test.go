package main

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// obor resume <T-xx> ["<text>"] [--regate|--sendback] (T-775): one request to obor serve carrying the task, the text
// and the flag as the caller (caller: the lead from the main checkout, the owner with no role, a worker from its lane's
// cwd whatever OBOR_ROLE says), serve's reply printed; a usage error or a bad task id sends nothing; serve's refusal is
// FAIL, exit 1. obor answer and obor sendback are gone. --reprompt is a text (RV-99 #5): with another, two texts.
func TestResumeRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "ANSWERED T-01: obor serve sends pj-l-01 the answer", "")
	env := func(role string) []string {
		return []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=" + role, "OBOR_AGENT=pj-lead", "OBOR_LANE="}
	}
	for _, args := range [][]string{{"resume"}, {"resume", "T-01", ""}, {"resume", "T-01", "a", "b"},
		{"resume", "T-01", "--regate", "--sendback"}, {"resume", "T-01", "--reprompt", "yes"}, {"resume", "--regate", "T-01"}} {
		if rc, stdout, _ := run(t, p.Root, env("lead"), args...); rc != 1 || !strings.Contains(stdout, "FAIL "+wire.ResumeUsage) {
			t.Errorf("obor %q: exit %d, %q; want the usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env("lead"), "resume", "X-1", "yes"); rc != 1 || !strings.Contains(stdout, "X-1 is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	for _, verb := range []string{"answer", "sendback"} {
		if rc, _, stderr := run(t, p.Root, env("lead"), verb, "T-01", "yes"); rc != 2 || !strings.Contains(stderr, "unknown subcommand: "+verb) {
			t.Errorf("obor %s: exit %d, %q; want gone (T-775)", verb, rc, stderr)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}

	rc, stdout, _ := run(t, p.Root, env("lead"), "resume", "T-01", "yes, edit x.go")
	if rc != 0 || stdout != "ANSWERED T-01: obor serve sends pj-l-01 the answer\n" {
		t.Errorf("resume: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env(""), "resume", "T-01", "--sendback", "as the owner")
	run(t, o1, env("lead"), "resume", "T-01", "--regate")
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	for i, want := range []struct{ role, note, to string }{{"lead", "yes, edit x.go", ""},
		{"", "as the owner", wire.ResumeSendback}, {"worker", "", wire.ResumeRegate}} {
		c := calls[i]
		if c.Task != "T-01" || c.Note != want.note || c.To != want.to || c.Role != want.role || c.Agent != "pj-lead" ||
			c.Root != p.Root || c.CallID == "" {
			t.Errorf("request %d: %+v; want task T-01, note %q, to %q, role %q, agent pj-lead, root, a call id", i+1, c,
				want.note, want.to, want.role)
		}
	}
	if calls[0].CallID == calls[1].CallID {
		t.Error("two resumes share a call id")
	}

	home2, _ := startFakeServe(t, p.Root, wire.Status{}, "", "resume: T-01 is working with nothing to resume")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "resume", "T-01", "yes"); rc != 1 ||
		stdout != "FAIL resume: T-01 is working with nothing to resume\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}
