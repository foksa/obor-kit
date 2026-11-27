//go:build integration

package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
	"obor/internal/version"
)

// obor dispatch is one socket request to obor serve, the only dispatcher (as obor resume is): the level form, the
// types form, --after and --force reach it as the call, its reply is printed; serve down is FAIL with the lane
// untouched; the
// options that mean nothing to a queued dispatch are usage errors; its refusal is the caller's FAIL.
func TestDispatchServeRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	laneBefore := laneEntries(t, p)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → L-01 (starting)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}

	// one row with every field: the arguments' order and each field alone are TestDispatchArguments'
	before, _ := serve.requests()
	rc, stdout, stderr := run(t, p.Root, env, "dispatch", "--force", "--after", "T-05,T-06", "T-07", "--model", "sonnet,opus")
	if rc != 0 || stdout != "DISPATCHED T-07 → L-01 (starting)\n" {
		t.Errorf("exit %d, %q (stderr %q); want the reply", rc, stdout, stderr)
	}
	calls, _ := serve.requests()
	if len(calls) != len(before)+1 {
		t.Fatalf("%d requests, want one more than %d", len(calls), len(before))
	}
	got := calls[len(calls)-1]
	if got.CallID == "" {
		t.Error("no call id")
	}
	got.CallID = ""
	want := sentCall{Task: "T-07", Types: []string{"sonnet", "opus"}, After: []string{"T-05", "T-06"}, Force: true}
	want.Root, want.Agent, want.Role = p.Root, "pj-lead", "lead"
	want.Version = version.Version // every call names its binary
	if !reflect.DeepEqual(got, want) {
		t.Errorf("call %+v, want %+v", got, want)
	}

	// a usage error and a bad task id ask serve nothing (the rows: TestDispatchArguments)
	_, n := serve.requests()
	if rc, stdout, _ := run(t, p.Root, env, "dispatch", "T-07", "--bogus"); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL usage: obor dispatch <T-xx> ") {
		t.Errorf("usage: exit %d, %q; want a usage error", rc, stdout)
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
	if got := laneEntries(t, p); got != laneBefore {
		t.Errorf("serve down: lane L-01 touched (%q, was %q)", got, laneBefore)
	}
}

// obor drop is one request to obor serve: the task, the why, --discard and --force reach it as the call
// (the caller's role and agent; serve knows the lane), its reply is printed; its refusal is the caller's FAIL; serve
// down is FAIL with the lane untouched; a usage error or a lane instead of a task id asks serve nothing.
func TestDropServeRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	laneBefore := laneEntries(t, p)
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
	if got := laneEntries(t, p); got != laneBefore {
		t.Errorf("serve down: lane L-01 touched (%q, was %q)", got, laneBefore)
	}
}

// laneEntries is the names in lane L-01's dir, one line: a command that only asks serve leaves the lane as it was (it
// runs no git there).
func laneEntries(t *testing.T, p *testkit.Project) string {
	t.Helper()
	es, err := os.ReadDir(p.LaneDir("L-01"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}
