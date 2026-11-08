//go:build integration

package main

import (
	"reflect"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/testkit"
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
		want wire.Call
	}{
		{"level", []string{"dispatch", "T-07"}, wire.Call{Task: "T-07"}},
		{"level, force", []string{"dispatch", "T-07", "--force"}, wire.Call{Task: "T-07", Force: true}},
		{"models", []string{"dispatch", "T-07", "--model", "sonnet,opus"}, wire.Call{Task: "T-07", Types: []string{"sonnet", "opus"}}},
		{"models, force first", []string{"dispatch", "--force", "--model", "opus", "T-07"},
			wire.Call{Task: "T-07", Types: []string{"opus"}, Force: true}},
		{"level, after", []string{"dispatch", "T-07", "--after", "T-05,T-06"},
			wire.Call{Task: "T-07", After: []string{"T-05", "T-06"}}},
		{"models, after first", []string{"dispatch", "--after", "T-05", "T-07", "--model", "opus"},
			wire.Call{Task: "T-07", Types: []string{"opus"}, After: []string{"T-05"}}},
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

// specs: Check wants a task id and a stored spec (T-440: the text the app read from the store, "" none; a spec in
// git counts for nothing; T-496: a files section is no longer needed); Target is the target's sha and whether the task
// branch is ahead of it.
func TestSpecs(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
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
	if sha, ahead, err := s.Target(ctx, "T-01"); err != nil || sha != head || ahead { // L-01's branch task/T-01 = main
		t.Errorf("Target, branch at main: %q %v %v; want not ahead", sha, ahead, err)
	}
	p.Commit(p.LaneDir("L-01"))
	if _, ahead, err := s.Target(ctx, "T-01"); err != nil || !ahead {
		t.Errorf("Target, branch with a commit: ahead %v, %v; want ahead", ahead, err)
	}
}

// T-425, T-1023: the dispatch target is branches.main's sha whatever the main checkout has checked out (another
// branch, a detached HEAD); the merge loop no longer pauses off it (no Checkout).
func TestDispatchTargetOffBranch(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
	l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}})(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	main := p.Git(p.Root, "rev-parse", "main")
	check := func(name string) {
		t.Helper()
		if sha, _, err := l.Specs.Target(ctx, "T-1"); err != nil || sha != main {
			t.Errorf("%s: Target %q %v; want main's %q", name, sha, err, main)
		}
	}
	check("on main")
	p.Git(p.Root, "switch", "-q", "-c", "side")
	p.Commit(p.Root)
	check("on side")
	p.Git(p.Root, "switch", "-q", "--detach", "side")
	check("detached")
}

// servedGitProject is servedProject in a git project (testkit.New, lane L-01 a real worktree on task/T-01), for the
// tests whose CLI path runs git.
func servedGitProject(t *testing.T, edits ...func(map[string]any)) *testkit.Project {
	t.Helper()
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	p.Config(append([]func(map[string]any){testkit.Set("roles.integrator", "")}, edits...)...)
	return p
}
