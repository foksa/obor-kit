package main

import (
	"errors"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// obor drive's request: a name is a new drive (Note, --model as its one model), a task id a drive's own task (Task;
// --model passed on, serve's retry ignores it); serve's refusal is the caller's FAIL and nothing is started: a worker's
// task's word for word.
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
		t.Errorf("a task's call %+v", c)
	}
	for _, args := range [][]string{{"drive"}, {"drive", "a", "b"}, {"drive", "--model"}, {"drive", "-x"}} {
		if rc, stdout, _ := run(t, p.Root, env, args...); rc != 1 || !strings.Contains(stdout, "FAIL usage: obor drive") {
			t.Errorf("%q: rc %d, %q; want usage", args, rc, stdout)
		}
	}
	if calls, _ := serve.requests(); len(calls) != 2 {
		t.Errorf("usage errors sent requests: %d calls", len(calls))
	}
	run(t, p.Root, env, "drive", "T-07", "--model", "opus")
	if calls, _ := serve.requests(); len(calls) != 3 || calls[2].Task != "T-07" || len(calls[2].Types) != 1 ||
		calls[2].Types[0] != "opus" {
		t.Errorf("a task's call with --model: %+v; want T-07's, its model passed on", calls)
	}
	home2, _ := startFakeServe(t, p.Root, wire.Status{}, "", "T-07 is not a drive; obor drive <name> starts one")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "drive", "T-07"); rc != 1 ||
		stdout != "FAIL T-07 is not a drive; obor drive <name> starts one\n" {
		t.Errorf("a worker's task: rc %d, %q; want serve's refusal as FAIL, nothing else", rc, stdout)
	}
}

// parseDrive reads serve's DRIVE reply, its tail ignored; a DRIVING reply (a drive whose agent runs already, or an old
// serve's take-over) is none.
func TestParseDrive(t *testing.T) {
	t.Parallel()
	for reply, want := range map[string]drive{
		"DRIVE T-3 → L-01 (pj-l-01-drive, opus:high)":                                 {"T-3", "L-01", "pj-l-01-drive", "opus:high"},
		"DRIVE T-3 → L-01 (pj-l-01-drive, opus:high) again: its agent is not running": {"T-3", "L-01", "pj-l-01-drive", "opus:high"},
	} {
		if got, ok := parseDrive(reply); !ok || got != want {
			t.Errorf("parseDrive(%q) = %+v, %v; want %+v", reply, got, ok, want)
		}
	}
	if _, ok := parseDrive("DISPATCHED T-1 → L-01 (queued)"); ok {
		t.Error("parseDrive read a dispatch's reply")
	}
	for _, reply := range []string{"DRIVING T-07 → L-01: pj-l1-drive runs already (pane w1:p5)",
		"DRIVING T-1 → L-02 (pj-l-02-drive, sonnet) taking over (stopping pj-l-02)"} {
		if _, ok := parseDrive(reply); ok {
			t.Errorf("parseDrive read %q", reply)
		}
	}
}

// obor drive on a drive whose agent runs: serve's DRIVING reply is printed with OK (exit 0), nothing started
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

// A drive whose start failed names the command that starts it again once: a start whose message carries it (a pane
// that never became a shell, a kind's startup hint) keeps its own; one that does not (a prepare timeout, a terminal
// agent-list failure, an ordinary start error) has it added; a failure with advice of its own (no model) gets none.
func TestDriveFailedNamesTheRetryOnce(t *testing.T) {
	t.Parallel()
	const ending = "the drive is recorded; obor drop T-07 ends it"
	for _, c := range []struct {
		name  string
		err   error
		named int  // occurrences of the drive's command in the FAIL line
		adds  bool // the FAIL line adds "start it again with …" around the start's message
	}{
		{"prepare timeout", errors.New("lane L-01 is not prepared for T-07 after 1m0s (obor status says why)"), 1, true},
		{"agent list failure", project.Fail("terminal agent list failed (is the terminal running?)"), 1, true},
		{"generic start error", project.Fail("terminal agent start pj-l-01-drive failed (pane w1:n1)"), 1, true},
		{"not-shell hint, which carries it", project.RetryHint(project.Fail(
			"pane w1:n1 is not an available shell after 10s: start the agent there with obor drive T-07")), 1, false},
		{"no model, its own advice", project.RetryHint(project.Fail(
			"lane L-01 runs no model: obor drop T-07, then obor drive <name> --model <model>")), 0, false},
	} {
		got := driveFailed(c.err, "T-07")
		if !strings.HasPrefix(got, "FAIL "+c.err.Error()) {
			t.Errorf("%s: FAIL line %q, want the start's own message first", c.name, got)
		}
		if n := strings.Count(got, "obor drive T-07"); n != c.named {
			t.Errorf("%s: FAIL line %q names the drive's command %d times, want %d", c.name, got, n, c.named)
		}
		if !strings.Contains(got, ending) {
			t.Errorf("%s: FAIL line %q, want %q", c.name, got, ending)
		}
		if adds := strings.Contains(got, "start it again with obor drive T-07"); adds != c.adds {
			t.Errorf("%s: FAIL line %q adds the retry: %v, want %v", c.name, got, adds, c.adds)
		}
	}
}

// obor tell-lead "<text>": one request to obor serve carrying the text, the caller's role and its lane (a
// drive agent: OBOR_ROLE=driver in its lane's cwd), serve's reply printed; a usage error sends nothing; serve's refusal
// is FAIL, exit 1.
func TestTellLeadRequest(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	o1 := p.PlainLane("L-01", "task/T-01")
	p.Config()
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "TOLD pj-lead (T-01; its answer arrives here as an [obor answer] line)", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=driver", "OBOR_AGENT=pj-l1-drive", "OBOR_LANE=L-01"} // a stale name
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
	if c := calls[0]; c.Role != "driver" || c.Lane != "L-01" || c.Agent != "pj-l-01-drive" ||
		c.Note != "may I change the contract?" || c.Root != p.Root || c.CallID == "" {
		t.Errorf("request %+v; want role driver, lane L-01, agent pj-l-01-drive (derived), the text, root, a call id", c)
	}

	home2, _ := startFakeServe(t, p.Root, wire.Status{}, "", "tell-lead: it is for a drive agent to tell its lead")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2, "OBOR_ROLE=lead"}, "tell-lead", "hi"); rc != 1 ||
		stdout != "FAIL tell-lead: it is for a drive agent to tell its lead\n" {
		t.Errorf("serve's refusal: exit %d, %q", rc, stdout)
	}
}

// The drive agent's first prompt names how it reaches the lead and how the lead's answers arrive, and says to
// commit on the branch as it goes, which obor land refuses; one line.
func TestDrivePrompt(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	got := drivePrompt(cfg, drive{task: "T-01", lane: "L-01"})
	for _, want := range []string{`obor tell-lead "<text>"`, "[obor answer] lines", "obor land T-01",
		"commit on that branch as you go", "commits nothing and refuses uncommitted changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("drive prompt %q, want %q in it", got, want)
		}
	}
	if strings.ContainsAny(got, "\n`") {
		t.Errorf("drive prompt %q: want one line, no backticks", got)
	}
}
