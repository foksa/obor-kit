package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// obor resume through the binary: usage, a task id, serve not running (one word that is no mode is a decision, RV-54
// #4), then with obor serve running its refusals reach
// the caller (not a worker's, no needs-you). T-470: the integrator's CLI verdicts (accept, reject, resolved, hold,
// sendback <T-xx>) are gone: its MCP tools are their only path; obor sendback <LANE> stays the lead's
// (TestLaneCommands).
func TestResumeAndNoVerdictVerbs(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("O-01", "task/T-01")
	// the merge loop on: a project without an integrator role refuses every verdict (T-352)
	p.Config(testkit.Set("roles.integrator", map[string]any{}))
	p.Commit(lane)
	home, err := os.MkdirTemp("", "hs") // short: the serve socket lives here
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	srv := herdrtest.Serve(t, f)
	env := []string{"OBOR_STATE_HOME=" + home, "HERDR_SOCKET_PATH=" + srv.Path, "OBOR_ROLE=integrator",
		"OBOR_AGENT=pj-int"}

	check := func(env []string, want string, args ...string) {
		t.Helper()
		rc, stdout, _ := run(t, p.Root, env, args...)
		if got := strings.TrimSpace(stdout); rc != 1 || !strings.HasPrefix(got, want) {
			t.Errorf("obor %v: exit %d, %q; want 1 and %q", args, rc, got, want)
		}
	}
	down := append(append([]string(nil), env...), callDown) // no serve yet: the calls end at once
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"resume", "T-01", " "}, `FAIL usage: obor resume <T-xx> [regate|reprompt] ["<decision>"]`}, // a blank decision
		{[]string{"resume", "T-01", "yes"}, "FAIL obor serve is not running"},                                // a one-word decision (RV-54 #4)
		{[]string{"resume", "T-01", "again"}, "FAIL obor serve is not running"},                              // not a mode: a decision
		{[]string{"resume", "T-01", "regate", " "}, "FAIL usage: obor resume"},
		{[]string{"resume"}, "FAIL usage: obor resume"},
		{[]string{"resume", "X-1"}, "FAIL X-1 is no task id (task_id_pattern T-[0-9]+[a-z]?)"},
		{[]string{"resume", "T-01", "regate"}, "FAIL obor serve is not running"},
		{[]string{"resume", "T-01", "regate", "yes", "more"}, "FAIL usage: obor resume"},
		{[]string{"resume", "T-01", "reprompt", "yes"}, "FAIL obor serve is not running"},    // a decision (T-511)
		{[]string{"resume", "T-01", "the owner says yes"}, "FAIL obor serve is not running"}, // a decision, the cause's default
		{[]string{"sendback", "T-01", "why"}, "FAIL unknown lane T-01"},                      // the lane form only
	} {
		check(down, c.want, c.args...)
	}

	var out bytes.Buffer
	cmd := exec.Command(oborBin, "serve")
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Env = p.Root, &out, &out, append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if rc, stdout, _ := run(t, p.Root, env, "serve", "status"); rc == 0 && strings.HasPrefix(stdout, "serve: pane pid ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("obor serve never answered; its output:\n%s", out.String())
		}
	}
	for _, verb := range []string{"accept", "reject", "resolved", "hold"} {
		rc, _, stderr := run(t, p.Root, env, verb, "T-01", "why")
		if rc != 2 || !strings.Contains(stderr, "unknown subcommand: "+verb) {
			t.Errorf("obor %s: exit %d, %q; want 2 and unknown subcommand (T-470)", verb, rc, stderr)
		}
		if rc, _, _ := run(t, p.Root, env, "has", verb); rc != 1 {
			t.Errorf("obor has %s: exit %d, want 1", verb, rc)
		}
	}
	worker := append(append([]string(nil), env...), "OBOR_ROLE=worker", "OBOR_AGENT=pj-o-01")
	// serve answers status before its project is loaded (its first subscribe or tick replays the log): wait for it.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, stdout, _ := run(t, p.Root, worker, "resume", "T-01"); !strings.Contains(stdout, "no served project") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("obor serve never loaded the project; its output:\n%s", out.String())
		}
	}
	check(worker, "FAIL resume: the lead or the owner resumes a task, not pj-o-01 (role worker)", "resume", "T-01")
	lead := append(append([]string(nil), env...), "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead")
	check(lead, "FAIL resume: T-01 has no needs-you of the merge loop", "resume", "T-01", "reprompt")
	for _, decision := range []string{"yes", "no", "approved"} { // RV-54 #4: serve's refusal, not the usage
		check(lead, "FAIL resume: T-01 has no needs-you of the merge loop", "resume", "T-01", decision)
	}
}
