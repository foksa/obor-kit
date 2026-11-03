package main

import (
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/testkit"
)

// obor land (T-751) is one socket request after two checks of its own: the task is a driving one in serve's status
// (by id, or by the drive's name in its stored spec), and its lane worktree is clean. A dirty worktree is refused with
// its paths and nothing is sent (land commits nothing); no summary: the branch's commit subjects since main; none:
// refused. Serve's reply is printed.
func TestLandRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	p.Spec("T-01", "# T-01 — Owner drive: web page\n")
	lane := p.LaneDir("L-01")
	st := wire.Status{Connected: true, Project: &wire.ProjectStatus{Root: p.Root, Merging: true,
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", State: wire.TaskDriving}, {Task: "T-02", Lane: "L-02", State: "working"}}}}
	home, serve := startFakeServe(t, p.Root, st, "LANDED T-01: submitted (gate, review and merge follow)", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	lands := func() []wire.Call {
		calls, _ := serve.requests()
		return calls
	}

	p.Write(filepath.Join(lane, "page.html"), "<p>draft</p>\n")
	rc, stdout, _ := run(t, p.Root, env, "land", "T-01")
	if rc != 1 || !strings.Contains(stdout, "FAIL lane L-01 has uncommitted changes") || !strings.Contains(stdout, "page.html") {
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

// obor drive's request: a name is a new drive (Note, --model as its one model), a task id a take-over (--model refused);
// serve's refusal is the caller's FAIL and nothing is started.
func TestDriveRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "", "drive web: no free untyped lane (L-01 (opus) free)")
	env := []string{"OBOR_STATE_HOME=" + home}
	rc, stdout, _ := run(t, p.Root, env, "drive", "web", "--model", "sonnet:high")
	if rc != 1 || !strings.Contains(stdout, "FAIL drive web: no free untyped lane (L-01 (opus) free)") {
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
	for _, args := range [][]string{{"drive"}, {"drive", "a", "b"}, {"drive", "--model"}, {"drive", "-x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL usage: obor drive") {
			t.Errorf("%q: rc %d, %q; want usage", args, rc, stdout)
		}
	}
	if rc, stdout, _ := run(t, p.Root, env, "drive", "T-07", "--model", "opus"); rc != 1 || !strings.Contains(stdout, "--model is a new drive's") {
		t.Errorf("a take-over with --model: rc %d, %q", rc, stdout)
	}
	if calls, _ := serve.requests(); len(calls) != 2 {
		t.Errorf("usage errors sent requests: %d calls", len(calls))
	}
}

// parseDrive reads serve's DRIVE reply, a take-over's tail ignored.
func TestParseDrive(t *testing.T) {
	t.Parallel()
	for reply, want := range map[string]drive{
		"DRIVE T-3 → L-01 (pj-l-01-drive, opus:high)":                                            {"T-3", "L-01", "pj-l-01-drive", "opus:high"},
		"DRIVE T-1 → L-02 (pj-l-02-drive, -) taking over from pj-l-02; STOPPED pj-l-02":          {"T-1", "L-02", "pj-l-02-drive", "-"},
		"DRIVE T-1 → L-02 (pj-l-02-drive, sonnet) taking over from pj-l-02; pj-l-02 not running": {"T-1", "L-02", "pj-l-02-drive", "sonnet"},
	} {
		if got, ok := parseDrive(reply); !ok || got != want {
			t.Errorf("parseDrive(%q) = %+v, %v; want %+v", reply, got, ok, want)
		}
	}
	if _, ok := parseDrive("DISPATCHED T-1 → L-01 (queued)"); ok {
		t.Error("parseDrive read a dispatch's reply")
	}
}

// obor drive on a drive whose agent runs (T-755): serve's DRIVING reply is printed with OK (exit 0), nothing started
// (no lane wait, no herdr: the fake project has neither).
func TestDriveAgentRunningStartsNothing(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DRIVING T-07 → L-01: pj-l1-drive runs already (pane w1:p5)", "")
	rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home}, "drive", "T-07")
	if calls, _ := serve.requests(); len(calls) != 1 || calls[0].Task != "T-07" {
		t.Fatalf("precondition: calls %+v, want T-07's drive", calls)
	}
	if rc != 0 || !strings.Contains(stdout, "DRIVING T-07 → L-01: pj-l1-drive runs already") ||
		!strings.Contains(stdout, "OK T-07 → L-01: pj-l1-drive runs already (pane w1:p5)") || strings.Contains(stdout, "FAIL") {
		t.Errorf("drive while its agent runs: rc %d, %q; want the reply and OK, nothing started", rc, stdout)
	}
}

// obor tell-lead "<text>" (T-761): one request to obor serve carrying the text, the caller's role and its lane (a
// drive agent: OBOR_ROLE=driver in its lane's cwd), serve's reply printed; a usage error sends nothing; serve's refusal
// is FAIL, exit 1.
func TestTellLeadRequest(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "TOLD pj-lead (T-01; its answer arrives here as an [obor answer] line)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=driver", "OBOR_AGENT=pj-l1-drive", "OBOR_LANE=L-01"}
	for _, args := range [][]string{{"tell-lead"}, {"tell-lead", " "}, {"tell-lead", "a", "b"}} {
		if rc, stdout, _ := run(t, o1, env, args...); rc != 1 || !strings.Contains(stdout, `FAIL usage: obor tell-lead "<text>"`) {
			t.Errorf("obor %q: exit %d, %q; want the usage", args, rc, stdout)
		}
	}
	if _, n := serve.requests(); n != 0 {
		t.Fatalf("a usage error sent %d requests", n)
	}
	rc, stdout, _ := run(t, o1, env, "tell-lead", "may I change the contract?")
	if rc != 0 || stdout != "TOLD pj-lead (T-01; its answer arrives here as an [obor answer] line)\n" {
		t.Errorf("tell-lead: exit %d, %q", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 {
		t.Fatalf("%d requests, want 1", len(calls))
	}
	if c := calls[0]; c.Role != "driver" || c.Lane != "L-01" || c.Agent != "pj-l1-drive" ||
		c.Note != "may I change the contract?" || c.Root != p.Root || c.CallID == "" {
		t.Errorf("request %+v; want role driver, lane L-01, agent pj-l1-drive, the text, root, a call id", c)
	}

	home2, _ := startFakeServe(t, p.Root, wire.Status{}, "", "tell-lead: it is for a drive agent to tell its lead")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2, "OBOR_ROLE=lead"}, "tell-lead", "hi"); rc != 1 ||
		stdout != "FAIL tell-lead: it is for a drive agent to tell its lead\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}

// The drive agent's first prompt names how it reaches the lead and how the lead's answers arrive (T-761); one line.
func TestDrivePromptNamesTheLead(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	got := drivePrompt(cfg, drive{task: "T-01", lane: "L-01"})
	for _, want := range []string{`obor tell-lead "<text>"`, "[obor answer] lines", "obor land T-01"} {
		if !strings.Contains(got, want) {
			t.Errorf("drive prompt %q, want %q in it", got, want)
		}
	}
	if strings.ContainsAny(got, "\n`") {
		t.Errorf("drive prompt %q: want one line, no backticks", got)
	}
}

// The drive agent's first prompt says to commit on the branch as it goes, and that obor land commits nothing and refuses
// uncommitted changes (T-762).
func TestDrivePromptSaysCommitAsYouGo(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	got := drivePrompt(cfg, drive{task: "T-01", lane: "L-01"})
	for _, want := range []string{"commit on that branch as you go", "obor land T-01",
		"commits nothing and refuses uncommitted changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("drive prompt %q, want %q in it", got, want)
		}
	}
}
