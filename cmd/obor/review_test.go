package main

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// obor review [--after T-a,T-b] [--note …] | --rm is one review request as the caller; a usage error or a bad
// task id sends nothing (FAIL on stdout, exit 1).
func TestReviewRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "REVIEW REQUESTED", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "--rm", "--after", "T-1"}, {"review", "--after"}, {"review", "--note", ""},
		{"review", "T-1"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "--after", "T-1,nope"); rc != 1 || !strings.HasPrefix(stdout, "FAIL nope is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "--after", "T-1,T-2", "--note", "skip docs"); rc != 0 || stdout != "REVIEW REQUESTED\n" {
		t.Errorf("review --after: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env, "review")
	run(t, p.Root, env, "review", "--rm")
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	if c := calls[0]; strings.Join(c.After, ",") != "T-1,T-2" || c.Note != "skip docs" || c.Clear || c.Role != "lead" || c.CallID == "" {
		t.Errorf("request 1: %+v", c)
	}
	if c := calls[1]; len(c.After) != 0 || c.Note != "" || c.Clear {
		t.Errorf("request 2 (no argument): %+v", c)
	}
	if c := calls[2]; !c.Clear {
		t.Errorf("request 3 (--rm): %+v", c)
	}
}
