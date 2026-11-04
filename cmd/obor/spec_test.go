package main

import (
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
)

// T-440: obor spec set is one socket request to obor serve: the file's text read once, the task, the caller and its
// role (the session's: a lane's cwd is a worker's whatever OBOR_ROLE says, so serve refuses it); its reply is printed.
// A bad task id or an unreadable file asks nothing; serve down or a refusal is FAIL, exit 1 (T-520).
func TestSpecSetRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	const text = "# T-07 — Stored\n\n## Files you may touch\n`a.go`\n"
	p.Write(".obor/drafts/T-07.md", text)
	draft := filepath.Join(p.Root, ".obor/drafts/T-07.md")
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "SET T-07 rev 1", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}

	for _, c := range []struct {
		name, dir, role string
		agent           string // derived from the role (T-786), whatever OBOR_AGENT says
	}{
		{"the lead", p.Root, "lead", "pj-lead"},
		{"from a lane, OBOR_ROLE=lead", p.LaneDir("L-01"), "worker", "pj-l-01"},
	} {
		rc, stdout, stderr := run(t, c.dir, env, "spec", "set", "T-07", draft)
		if rc != 0 || stdout != "SET T-07 rev 1\n" {
			t.Errorf("%s: exit %d, %q (stderr %q); want the reply", c.name, rc, stdout, stderr)
		}
		calls, _ := serve.requests()
		got := calls[len(calls)-1]
		if got.CallID == "" || got.Task != "T-07" || got.Spec != text || got.Role != c.role || got.Agent != c.agent ||
			got.Root != p.Root {
			t.Errorf("%s: call %+v; want T-07's text, role %s, agent %s", c.name, got, c.role, c.agent)
		}
	}

	// The owner in a plain terminal (role none) is sent as no role (DD-v0.11 A3).
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home}, "spec", "set", "T-07", draft); rc != 0 ||
		stdout != "SET T-07 rev 1\n" {
		t.Errorf("the owner: exit %d, %q; want the reply", rc, stdout)
	}
	if calls, _ := serve.requests(); calls[len(calls)-1].Role != "" {
		t.Errorf("the owner: call %+v; want no role", calls[len(calls)-1])
	}

	_, n := serve.requests()
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"bad task id", []string{"spec", "set", "X-1", draft}, "FAIL X-1 is no task id"},
		{"no such file", []string{"spec", "set", "T-07", filepath.Join(p.Root, "nope.md")}, "FAIL cannot read "},
		{"no path", []string{"spec", "set", "T-07"}, "FAIL usage: obor spec set"},
		{"no subcommand", []string{"spec"}, "FAIL usage: obor spec set"},
		{"unknown subcommand", []string{"spec", "edit", "T-07"}, "FAIL usage: obor spec set"},
	} {
		if rc, stdout, _ := run(t, p.Root, env, c.args...); rc != 1 || !strings.HasPrefix(stdout, c.want) {
			t.Errorf("%s: exit %d, %q; want %q, exit 1", c.name, rc, stdout, c.want)
		}
	}
	if _, after := serve.requests(); after != n {
		t.Errorf("a refused set asked serve (%d requests, %d before)", after, n)
	}

	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "", "spec set: only the lead sets a spec, not pj-l1 (role worker)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "spec", "set", "T-07", draft); rc != 1 ||
		stdout != "FAIL spec set: only the lead sets a spec, not pj-l1 (role worker)\n" {
		t.Errorf("refusal: exit %d, %q", rc, stdout)
	}
	refusing.stop()
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), callDown}, "spec", "set", "T-07", draft); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
}

// T-440: obor spec show and list read the store read-only, with serve down: show prints the current rev's text as
// set, list one line per task; a task with no spec is FAIL, exit 1.
func TestSpecShowListServeDown(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir()} // no serve
	if rc, stdout, _ := run(t, p.Root, env, "spec", "list"); rc != 0 || stdout != "" {
		t.Errorf("list, no specs: exit %d, %q", rc, stdout)
	}
	p.Spec("T-07", "# T-07 — First\n")
	p.Spec("T-07", "# T-07 — Widened\n\n## Files you may touch\n`a.go`\n")
	p.Spec("T-03", "no title\n")
	if rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-07"); rc != 0 ||
		stdout != "# T-07 — Widened\n\n## Files you may touch\n`a.go`\n" {
		t.Errorf("show: exit %d, %q; want the current rev", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-09"); rc != 1 ||
		stdout != "FAIL no spec for T-09 in obor serve's store (obor spec set T-09 <path>)\n" {
		t.Errorf("show, none: exit %d, %q", rc, stdout)
	}
	rc, stdout, _ := run(t, p.Root, env, "spec", "list")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if rc != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "T-03 rev 1 ") ||
		!strings.HasPrefix(lines[1], "T-07 rev 2 ") || !strings.HasSuffix(lines[1], " lead Widened") {
		t.Errorf("list: exit %d, %q", rc, stdout)
	}
}
