package main

import (
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
)

// obor land (T-751) is one socket request after two checks of its own: the task is a driving one in serve's status
// (by id, or by the drive's name in its stored spec), and its lane worktree is clean. A dirty worktree is refused with
// its paths and nothing is sent (land commits nothing); no summary: the branch's commit subjects since main; none:
// refused. Serve's reply is printed.
func TestLandRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	p.Spec("T-01", "# T-01 — Owner drive: web page\n")
	lane := p.LaneDir("O-01")
	st := wire.Status{Connected: true, Project: &wire.ProjectStatus{Root: p.Root, Merging: true,
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "O-01", State: wire.TaskDriving}, {Task: "T-02", Lane: "O-02", State: "working"}}}}
	home, serve := startFakeServe(t, p.Root, st, "LANDED T-01: submitted (gate, review and merge follow)", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	lands := func() []wire.Call {
		calls, _ := serve.requests()
		return calls
	}

	p.Write(filepath.Join(lane, "page.html"), "<p>draft</p>\n")
	rc, stdout, _ := run(t, p.Root, env, "land", "T-01")
	if rc != 1 || !strings.Contains(stdout, "FAIL lane O-01 has uncommitted changes") || !strings.Contains(stdout, "page.html") {
		t.Errorf("land with a dirty worktree: rc %d, %q; want FAIL naming page.html", rc, stdout)
	}
	if n := len(lands()); n != 0 {
		t.Fatalf("a refused land sent %d requests", n)
	}

	p.Git(lane, "add", "-A")
	p.Git(lane, "commit", "-qm", "page draft")
	p.Write(filepath.Join(lane, "page.html"), "<p>final</p>\n")
	p.Git(lane, "commit", "-qam", "page final")
	rc, stdout, _ = run(t, p.Root, env, "land", "T-01")
	calls := lands()
	if rc != 0 || !strings.Contains(stdout, "LANDED T-01") || len(calls) != 1 {
		t.Fatalf("land: rc %d, %q, %d calls", rc, stdout, len(calls))
	}
	if c := calls[0]; c.Task != "T-01" || c.Report != "page draft\npage final" || c.CallID == "" {
		t.Errorf("land's call %+v: want T-01 with the branch's commit subjects as its report", c)
	}

	rc, stdout, _ = run(t, p.Root, env, "land", "web page", "the new page")
	calls = lands()
	if rc != 0 || len(calls) != 2 || calls[1].Task != "T-01" || calls[1].Report != "the new page" {
		t.Errorf("land by name with a summary: rc %d, %q, %+v", rc, stdout, calls)
	}

	for _, args := range [][]string{{"land", "T-02"}, {"land", "nothing"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL no open drive "+args[1]) {
			t.Errorf("%q: rc %d, %q; want FAIL no open drive", args, rc, stdout)
		}
	}
	for _, args := range [][]string{{"land"}, {"land", "T-01", "a", "b"}, {"land", "--x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL usage: obor land") {
			t.Errorf("%q: rc %d, %q; want usage", args, rc, stdout)
		}
	}
	if n := len(lands()); n != 2 {
		t.Errorf("refusals sent requests: %d calls", n)
	}
}

// obor drive's request: a name is a new drive (Note, --type as its one type), a task id a take-over (--type refused);
// serve's refusal is the caller's FAIL and nothing is started.
func TestDriveRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "", "drive web: no free untyped lane (O-01 (opus) free)")
	env := []string{"OBOR_STATE_HOME=" + home}
	rc, stdout, _ := run(t, p.Root, env, "drive", "web", "--type", "sonnet:high")
	if rc != 1 || !strings.Contains(stdout, "FAIL drive web: no free untyped lane (O-01 (opus) free)") {
		t.Errorf("refused drive: rc %d, %q", rc, stdout)
	}
	rc, _, _ = run(t, p.Root, env, "drive", "T-07")
	calls, _ := serve.requests()
	if rc != 1 || len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	if c := calls[0]; c.Note != "web" || c.Task != "" || len(c.Types) != 1 || c.Types[0] != "sonnet:high" {
		t.Errorf("a new drive's call %+v", c)
	}
	if c := calls[1]; c.Task != "T-07" || c.Note != "" || len(c.Types) != 0 {
		t.Errorf("a take-over's call %+v", c)
	}
	for _, args := range [][]string{{"drive"}, {"drive", "a", "b"}, {"drive", "--type"}, {"drive", "-x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL usage: obor drive") {
			t.Errorf("%q: rc %d, %q; want usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "drive", "T-07", "--type", "opus"); rc != 1 || !strings.Contains(stdout, "--type is a new drive's") {
		t.Errorf("a take-over with --type: rc %d, %q", rc, stdout)
	}
	if calls, _ := serve.requests(); len(calls) != 2 {
		t.Errorf("usage errors sent requests: %d calls", len(calls))
	}
}

// parseDrive reads serve's DRIVE reply, a take-over's tail ignored.
func TestParseDrive(t *testing.T) {
	t.Parallel()
	for reply, want := range map[string]drive{
		"DRIVE T-3 → O-01 (pj-o-01-drive, opus:high)":                                            {"T-3", "O-01", "pj-o-01-drive", "opus:high"},
		"DRIVE T-1 → O-02 (pj-o-02-drive, -) taking over from pj-o-02; STOPPED pj-o-02":          {"T-1", "O-02", "pj-o-02-drive", "-"},
		"DRIVE T-1 → O-02 (pj-o-02-drive, sonnet) taking over from pj-o-02; pj-o-02 not running": {"T-1", "O-02", "pj-o-02-drive", "sonnet"},
	} {
		if got, ok := parseDrive(reply); !ok || got != want {
			t.Errorf("parseDrive(%q) = %+v, %v; want %+v", reply, got, ok, want)
		}
	}
	if _, ok := parseDrive("DISPATCHED T-1 → O-01 (queued)"); ok {
		t.Error("parseDrive read a dispatch's reply")
	}
}

// obor drive on a drive whose agent runs (T-755): serve's DRIVING reply is printed with OK (exit 0), nothing started
// (no lane wait, no herdr: the fake project has neither).
func TestDriveAgentRunningStartsNothing(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DRIVING T-07 → O-01: pj-o1-drive runs already (pane w1:p5)", "")
	rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home}, "drive", "T-07")
	if calls, _ := serve.requests(); len(calls) != 1 || calls[0].Task != "T-07" {
		t.Fatalf("precondition: calls %+v, want T-07's drive", calls)
	}
	if rc != 0 || !strings.Contains(stdout, "DRIVING T-07 → O-01: pj-o1-drive runs already") ||
		!strings.Contains(stdout, "OK T-07 → O-01: pj-o1-drive runs already (pane w1:p5)") || strings.Contains(stdout, "FAIL") {
		t.Errorf("drive while its agent runs: rc %d, %q; want the reply and OK, nothing started", rc, stdout)
	}
}
