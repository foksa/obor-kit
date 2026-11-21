package main

import (
	"slices"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/testkit"
)

// obor resume <T-xx> ["<text>"] [--regate|--sendback], and obor resume nightly "<why>": one request to obor serve
// carrying the task (nightly is no task id: serve decides), the text and the flag as the caller (caller: the lead from
// the main checkout, the owner with no role, a worker from its lane's cwd whatever OBOR_ROLE says), serve's reply
// printed; a usage error or a bad task id sends nothing; serve's refusal is FAIL, exit 1. obor answer and obor sendback
// are no subcommands. --reprompt is a text: with another, two texts.
func TestResumeRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	o1 := p.PlainLane("L-01", "task/T-01")
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "ANSWERED T-01: obor serve sends pj-l-01 the answer", "")
	env := func(role string) []string {
		return []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=" + role, "OBOR_AGENT=pj-lead", "OBOR_LANE="}
	}
	// the usage line is printed for an argument set wire.ResumeArgs refuses (its rows: internal/app TestResumeArgs)
	if rc, stdout, _ := run(t, p.Root, env("lead"), "resume"); rc != 1 || !strings.Contains(stdout, "FAIL "+help.Usage("resume")) {
		t.Errorf("obor resume: exit %d, %q; want the usage", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env("lead"), "resume", "X-1", "yes"); rc != 1 || !strings.Contains(stdout, "X-1 is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	for _, verb := range []string{"answer", "sendback"} { // no subcommands: main does not dispatch them
		if slices.Contains(dispatched(t), verb) {
			t.Errorf("obor %s is dispatched; want gone", verb)
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
	run(t, p.Root, env("lead"), "resume", "nightly", "the flake is fixed in bdee5eb") // no task id: serve's form
	calls, _ := serve.requests()
	if len(calls) != 4 {
		t.Fatalf("%d requests, want 4", len(calls))
	}
	// The agent is derived from the role: the owner (no role) is no agent, a stale OBOR_AGENT is never read.
	for i, want := range []struct{ role, agent, task, note, to string }{{"lead", "pj-lead", "T-01", "yes, edit x.go", ""},
		{"", "", "T-01", "as the owner", wire.ResumeSendback}, {"worker", "pj-l-01", "T-01", "", wire.ResumeRegate},
		{"lead", "pj-lead", "nightly", "the flake is fixed in bdee5eb", ""}} {
		c := calls[i]
		if c.Task != want.task || c.Note != want.note || c.To != want.to || c.Role != want.role || c.Agent != want.agent ||
			c.Root != p.Root || c.CallID == "" {
			t.Errorf("request %d: %+v; want task %s, note %q, to %q, role %q, agent %q, root, a call id", i+1, c,
				want.task, want.note, want.to, want.role, want.agent)
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

// obor resume with obor serve not running: a request that reaches serve is the one FAIL line saying it is down; the
// task id check holds for a task and not for nightly (a word that is no mode is a text). The arguments that are no
// request are wire.ResumeArgs' (internal/app TestResumeArgs). With serve answering: TestResumeRequest. The integrator's
// verdicts are no subcommands: main does not dispatch them, and the capability check says so.
func TestResumeServeDownAndNoVerdictVerbs(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	env := []string{"OBOR_STATE_HOME=" + t.TempDir(), "OBOR_ROLE=integrator", "OBOR_AGENT=pj-int", callDown}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"resume", "T-01", "yes"}, "FAIL obor serve is not running"},
		{[]string{"resume", "X-1"}, "FAIL X-1 is no task id (task_id_pattern T-[0-9]+[a-z]?)"},
		{[]string{"resume", "nightly", "the flake is fixed"}, "FAIL obor serve is not running"}, // nightly is no task id
	} {
		rc, stdout, _ := run(t, p.Root, env, c.args...)
		if got := strings.TrimSpace(stdout); rc != 1 || !strings.HasPrefix(got, c.want) {
			t.Errorf("obor %v: exit %d, %q; want 1 and %q", c.args, rc, got, c.want)
		}
	}
	for _, verb := range []string{"accept", "reject", "resolved", "hold"} {
		if slices.Contains(dispatched(t), verb) {
			t.Errorf("obor %s is dispatched; want it an MCP tool only", verb)
		}
		if rc := has([]string{verb}); rc != 1 {
			t.Errorf("obor has %s: exit %d, want 1", verb, rc)
		}
	}
}
