//go:build integration

package main

import (
	"reflect"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/version"
)

// obor dispatch is one socket request to obor serve, the only dispatcher (T-408; as obor resume is): the level form, the
// types form, --after (T-462) and --force reach it as the call, its reply is printed; serve down is FAIL with the lane untouched; the
// options that mean nothing to a queued dispatch are usage errors; its refusal is the caller's FAIL.
func TestDispatchServeRequest(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → L-01 (queued)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}

	for _, c := range []struct {
		name string
		args []string
		want sentCall
	}{
		{"level", []string{"dispatch", "T-07"}, sentCall{Task: "T-07"}},
		{"level, force", []string{"dispatch", "T-07", "--force"}, sentCall{Task: "T-07", Force: true}},
		{"models", []string{"dispatch", "T-07", "--model", "sonnet,opus"}, sentCall{Task: "T-07", Types: []string{"sonnet", "opus"}}},
		{"models, force first", []string{"dispatch", "--force", "--model", "opus", "T-07"},
			sentCall{Task: "T-07", Types: []string{"opus"}, Force: true}},
		{"level, after", []string{"dispatch", "T-07", "--after", "T-05,T-06"},
			sentCall{Task: "T-07", After: []string{"T-05", "T-06"}}},
		{"models, after first", []string{"dispatch", "--after", "T-05", "T-07", "--model", "opus"},
			sentCall{Task: "T-07", Types: []string{"opus"}, After: []string{"T-05"}}},
	} {
		before, _ := serve.requests()
		rc, stdout, stderr := run(t, p.Root, env, c.args...)
		if rc != 0 || stdout != "DISPATCHED T-07 → L-01 (queued)\n" {
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
		c.want.Version = version.Version // every call names its binary (T-826)
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
		{"lane form (gone)", []string{"dispatch", "L-01", "T-07"}},
		{"lane form with models", []string{"dispatch", "L-01", "T-07", "--model", "opus"}},
		{"models without a task", []string{"dispatch", "--model", "opus"}},
		{"empty model", []string{"dispatch", "T-07", "--model", "opus,"}},
		{"models without a value", []string{"dispatch", "T-07", "--model"}},
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
	if b := p.Git(p.LaneDir("L-01"), "branch", "--show-current"); b != "task/T-01" {
		t.Errorf("serve down: lane L-01 touched (on %q)", b)
	}
}

// obor drop is one request to obor serve (T-439, T-775): the task, the why, --discard and --force reach it as the call
// (the caller's role and agent; serve knows the lane), its reply is printed; its refusal is the caller's FAIL; serve
// down is FAIL with the lane untouched; a usage error or a lane instead of a task id asks serve nothing.
func TestDropServeRequest(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DROPPED T-01 from L-01 (abandoned)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	rc, stdout, _ := run(t, p.Root, env, "drop", "T-01", "abandoned", "--discard", "--force")
	if rc != 0 || stdout != "DROPPED T-01 from L-01 (abandoned)\n" {
		t.Errorf("drop: exit %d, %q; want serve's reply", rc, stdout)
	}
	calls, n := serve.requests()
	if n != 1 || len(calls) != 1 {
		t.Fatalf("serve's requests: %d (%+v); want the one drop", n, calls)
	}
	if c := calls[0]; c.Task != "T-01" || c.Note != "abandoned" || !c.Discard || !c.Force || c.Lane != "" ||
		c.Role != "lead" || c.Agent != "pj-lead" || c.Root != p.Root || c.CallID == "" {
		t.Errorf("the drop call %+v; want task T-01, the why, discard, force, by the lead, no lane", c)
	}
	_, n = serve.requests()
	for _, args := range [][]string{{"drop"}, {"drop", "T-01"}, {"drop", "T-01", "why", "extra"}, {"drop", "T-01", "why", "--bad"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor drop") {
			t.Errorf("%q: exit %d, %q; want a usage error", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "drop", "L-01", "why"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL L-01 is no task id") {
		t.Errorf("a lane (T-775: the lane form is gone): exit %d, %q", rc, stdout)
	}
	if _, after := serve.requests(); after != n {
		t.Errorf("a usage error or a lane asked serve (%d requests, %d before)", after, n)
	}

	// Serve's refusal is the caller's FAIL.
	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "", "drop: lane L-01 is dirty (--discard to drop its uncommitted changes)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "drop", "T-01", "why"); rc != 1 ||
		stdout != "FAIL drop: lane L-01 is dirty (--discard to drop its uncommitted changes)\n" {
		t.Errorf("refusal: exit %d, %q", rc, stdout)
	}

	// Serve down: FAIL, the lane as it was.
	refusing.stop()
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), callDown}, "drop", "T-01", "why"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
	if b := p.Git(p.LaneDir("L-01"), "branch", "--show-current"); b != "task/T-01" {
		t.Errorf("serve down: lane L-01 moved to %q", b)
	}
}
