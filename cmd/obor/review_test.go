package main

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// obor review start [--after T-a,T-b] [--note …] | --rm is one review request as the caller; a usage error or a bad
// task id sends nothing (FAIL on stdout, exit 1). A bare obor review given --after or --note is refused, naming start.
func TestReviewRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "REVIEW REQUESTED", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	for _, args := range [][]string{{"review", "start", "--rm", "--after", "T-1"}, {"review", "start", "--after"},
		{"review", "start", "--note", ""}, {"review", "start", "T-1"}, {"review", "--rm", "--note", "x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor review") {
			t.Errorf("obor %q: exit %d, %q; want 1, FAIL usage", args, rc, stdout)
		}
	}
	for _, args := range [][]string{{"review", "--after", "T-1"}, {"review", "--note", "x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "obor review start") {
			t.Errorf("obor %q: exit %d, %q; want 1, a refusal naming obor review start", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "start", "--after", "T-1,nope"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL nope is no task id") {
		t.Errorf("a bad task id: exit %d, %q", rc, stdout)
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a refused command sent %d requests", n)
	}
	if rc, stdout, _ := run(t, p.Root, env, "review", "start", "--after", "T-1,T-2", "--note", "skip docs"); rc != 0 ||
		stdout != "REVIEW REQUESTED\n" {
		t.Errorf("review start --after: exit %d, %q", rc, stdout)
	}
	run(t, p.Root, env, "review", "start")
	run(t, p.Root, env, "review", "--rm")
	calls, _ := serve.requests()
	if len(calls) != 3 {
		t.Fatalf("%d requests, want 3", len(calls))
	}
	if c := calls[0]; strings.Join(c.After, ",") != "T-1,T-2" || c.Note != "skip docs" || c.Clear || c.Role != "lead" || c.CallID == "" {
		t.Errorf("request 1: %+v", c)
	}
	if c := calls[1]; len(c.After) != 0 || c.Note != "" || c.Clear {
		t.Errorf("request 2 (start, no flag): %+v", c)
	}
	if c := calls[2]; !c.Clear {
		t.Errorf("request 3 (--rm): %+v", c)
	}
}

// bare obor review prints the review state obor status' card shows: the open review's line and its request (or the
// review due line), NO REVIEW where neither is; it records nothing.
func TestReviewShowsItsState(t *testing.T) {
	t.Parallel()
	env := []string{"OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	t.Run("none", func(t *testing.T) {
		p := testkit.NewPlain(t)
		p.Config()
		home, serve := startFakeServe(t, p.Root, wire.Status{Project: &wire.ProjectStatus{Name: "pj", Root: p.Root}}, "", "")
		env := append(env, "OBOR_STATE_HOME="+home)
		rc, stdout, _ := run(t, p.Root, env, "review")
		if rc != 0 || stdout != "NO REVIEW\n" {
			t.Errorf("exit %d, %q; want 0, NO REVIEW", rc, stdout)
		}
		if calls, n := serve.requests(); len(calls) != 0 || n != 1 {
			t.Errorf("bare obor review sent %d calls of %d requests, want the status read alone", len(calls), n)
		}
	})
	t.Run("open review", func(t *testing.T) {
		p := testkit.NewPlain(t)
		p.Config()
		ss := wire.Status{Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
			ReviewOpen: &wire.ReviewOpenStatus{ID: "RV-190", Stage: wire.ReviewApproved},
			Review:     "review request since 2m (after T-1, waiting T-1)"}}
		home, serve := startFakeServe(t, p.Root, ss, "", "")
		env := append(env, "OBOR_STATE_HOME="+home)
		rc, stdout, _ := run(t, p.Root, env, "review")
		if rc != 0 || stdout != "review RV-190: approved: close it\nreview request since 2m (after T-1, waiting T-1)\n" {
			t.Errorf("exit %d, %q; want the review card", rc, stdout)
		}
		if calls, n := serve.requests(); len(calls) != 0 || n != 1 {
			t.Errorf("bare obor review sent %d calls of %d requests, want the status read alone", len(calls), n)
		}
	})
}
