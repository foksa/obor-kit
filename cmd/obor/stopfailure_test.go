package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-532: obor hook stop-failure (Claude Code's StopFailure: a turn ended on an API error) appends the payload's raw
// error and error_details with a timestamp to .obor/state/stop-failure.log and sends serve one stopped request with
// the agent, its role and lane, the error and its details. Serve down or refusing: exit 0, nothing on stdout, the log
// line written all the same. Outside an obor project: nothing.
func TestStopFailureHook(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	lane := p.LaneDir("O-01")
	logPath := filepath.Join(p.Root, ".obor/state/stop-failure.log")
	env := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-o-01"}
	down := append(append([]string(nil), env...), callDown) // the first call: no serve yet (T-685)
	event, _ := json.Marshal(map[string]any{"hook_event_name": "StopFailure", "cwd": lane, "error": "rate_limit",
		"error_details": "429 Too Many Requests"})
	logLines := func() []map[string]any {
		t.Helper()
		b, _ := os.ReadFile(logPath)
		var out []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("log line %q: %v", l, err)
			}
			out = append(out, m)
		}
		return out
	}

	if _, err := os.Stat(wire.SocketPath(p.Root)); err == nil {
		t.Fatal("precondition: no serve socket")
	}
	if rc, stdout, _, _ := shimRun(t, lane, string(event), down, oborBin, "hook", "stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("serve down: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	ls := logLines()
	if len(ls) != 1 || ls[0]["error"] != "rate_limit" || ls[0]["error_details"] != "429 Too Many Requests" ||
		ls[0]["agent"] != "pj-o-01" || ls[0]["at"] == nil {
		t.Fatalf("serve down: log %v, want one line with the error, its details, the agent and a time", ls)
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "STOPPED pj-o-01 (needs-you T-01)", "")
	if rc, stdout, _, _ := shimRun(t, lane, string(event), env, oborBin, "hook", "stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("serve up: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 {
		t.Fatalf("serve got %d requests, want one", len(calls))
	}
	c := calls[0]
	if serve.methods[0] != wire.MethodStopped || c.Agent != "pj-o-01" || c.Role != "worker" || c.Lane != "O-01" ||
		c.Error != "rate_limit" || c.Details != "429 Too Many Requests" || c.CallID == "" || c.Root != p.Root {
		t.Errorf("serve got %s %+v", serve.methods[0], c)
	}
	if n := len(logLines()); n != 2 {
		t.Errorf("log lines %d, want 2", n)
	}

	// A refusal: still exit 0, nothing on stdout.
	startFakeServe(t, p.Root, wire.Status{}, "", "stopped: no served project")
	if rc, stdout, _, _ := shimRun(t, lane, string(event), env, oborBin, "hook", "stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("serve refusing: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}

	outside := t.TempDir()
	event, _ = json.Marshal(map[string]any{"cwd": outside, "error": "rate_limit"})
	if rc, stdout, stderr, _ := shimRun(t, outside, string(event), env, oborBin, "hook", "stop-failure"); rc != 0 ||
		stdout != "" || stderr != "" {
		t.Errorf("outside an obor project: exit %d, %q, %q; want silent", rc, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(outside, ".obor")); err == nil {
		t.Error("outside an obor project: a .obor dir was written")
	}
}

// T-540: a worker's UserPromptSubmit and Stop hooks each tell serve its next turn began or ended (one turn request
// with the agent, its role and lane), which ends a stop in force; the hooks' own output is unchanged (the Stop hook
// still blocks with no task_done). A role agent's hooks send the same (T-569: its brief's stop ends). Each says its
// phase (T-571): the Stop hook's is the turn's end, UserPromptSubmit's its start. SessionStart and a lead's hooks send
// none; serve down: exit 0 all the same.
func TestTurnHooks(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	lane := p.LaneDir("O-01")
	worker := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-o-01"}
	down := append(append([]string(nil), worker...), callDown) // the first turn: no serve yet (T-685)
	event := func(name string, cwd ...string) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": name, "cwd": append(cwd, lane)[0],
			"prompt": "[obor job T-01/1/work] go"})
		return string(b)
	}

	if rc, stdout, _, _ := shimRun(t, lane, event("UserPromptSubmit"), down, oborBin, "hook", "user-prompt-submit"); rc != 0 ||
		stdout != "" {
		t.Errorf("serve down: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "TURN pj-o-01 (no stop)", "")
	if rc, stdout, _, _ := shimRun(t, lane, event("UserPromptSubmit"), worker, oborBin, "hook", "user-prompt-submit"); rc != 0 ||
		stdout != "" {
		t.Errorf("user-prompt-submit: exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.methods[0] != wire.MethodTurn || calls[0].Agent != "pj-o-01" || calls[0].Role != "worker" ||
		calls[0].Lane != "O-01" || calls[0].Root != p.Root || calls[0].Ended {
		t.Fatalf("after user-prompt-submit serve got %v %+v, want one turn request", serve.methods, calls)
	}

	p.Dispatched("O-01") // a task without task_done: the Stop hook blocks, and tells serve all the same
	rc, stdout, _, _ := shimRun(t, lane, event("Stop"), worker, oborBin, "hook", "stop")
	if rc != 0 || !strings.Contains(stdout, `"decision":"block"`) {
		t.Errorf("stop: exit %d, stdout %q; want the block verdict", rc, stdout)
	}
	if calls, _ = serve.requests(); len(calls) != 2 || serve.methods[1] != wire.MethodTurn || calls[1].Agent != "pj-o-01" ||
		!calls[1].Ended {
		t.Fatalf("after stop serve got %v %+v, want a second turn request, its end", serve.methods, calls)
	}

	shimRun(t, lane, event("SessionStart"), worker, oborBin, "hook", "session-start")
	lead := []string{"OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}
	shimRun(t, p.Root, event("UserPromptSubmit", p.Root), lead, oborBin, "hook", "user-prompt-submit")
	shimRun(t, p.Root, event("Stop", p.Root), lead, oborBin, "hook", "stop")
	if calls, _ = serve.requests(); len(calls) != 2 {
		t.Fatalf("session-start and the lead's hooks sent %v, want none", serve.methods[2:])
	}

	reviewer := []string{"OBOR_ROLE=reviewer", "OBOR_AGENT=pj-rev"}
	shimRun(t, p.Root, event("UserPromptSubmit", p.Root), reviewer, oborBin, "hook", "user-prompt-submit")
	shimRun(t, p.Root, event("Stop", p.Root), reviewer, oborBin, "hook", "stop")
	calls, _ = serve.requests()
	if len(calls) != 4 {
		t.Fatalf("the reviewer's hooks sent %v, want two turn requests", serve.methods[2:])
	}
	for i, c := range calls[2:] {
		if serve.methods[2+i] != wire.MethodTurn || c.Agent != "pj-rev" || c.Role != "reviewer" || c.Root != p.Root ||
			c.Ended != (i == 1) {
			t.Errorf("the reviewer's hook %d sent %s %+v, want a turn request (the second its end)", i, serve.methods[2+i], c)
		}
	}
}

// T-581: an OpenCode lane's hooks run in a shared process with no OBOR_AGENT: from the lane's cwd they name the lane's
// agent (serve gets the turn, the turn's end and the stop-failure); outside a lane they name none and send nothing.
func TestOpencodeHooksResolveTheLaneAgent(t *testing.T) {
	t.Parallel()
	p := servedProject(t, testkit.Set("lane_types.opus", `{"kind":"opencode"}`))
	lane := p.LaneDir("O-01")
	env := []string{"OBOR_STATE_HOME=" + t.TempDir()}
	turn := func(cwd string) map[string]any {
		return map[string]any{"hook_event_name": "UserPromptSubmit", "cwd": cwd, "prompt": "hi"}
	}
	stop := func(cwd string) map[string]any {
		return map[string]any{"hook_event_name": "Stop", "cwd": cwd, "session_id": "s1", "stop_hook_active": false}
	}
	failure := func(cwd string) map[string]any {
		return map[string]any{"hook_event_name": "StopFailure", "cwd": cwd, "error": "rate_limit", "error_details": "429"}
	}
	run := func(cwd, hook string, event map[string]any) {
		t.Helper()
		b, _ := json.Marshal(event)
		if rc, _, _, _ := shimRun(t, cwd, string(b), env, oborBin, "hook", hook); rc != 0 {
			t.Errorf("%s in %s: exit %d, want 0", hook, cwd, rc)
		}
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "TURN pj-o-01 (no stop)", "")
	run(lane, "user-prompt-submit", turn(lane))
	run(lane, "stop", stop(lane))
	run(lane, "stop-failure", failure(lane))
	calls, _ := serve.requests()
	if len(calls) != 3 || serve.methods[0] != wire.MethodTurn || serve.methods[1] != wire.MethodTurn ||
		serve.methods[2] != wire.MethodStopped {
		t.Fatalf("serve got %v %+v, want two turns and a stopped", serve.methods, calls)
	}
	for _, c := range calls {
		if c.Agent != "pj-o-01" || c.Role != "worker" || c.Lane != "O-01" || c.Root != p.Root {
			t.Errorf("call %+v: want the lane's agent, role and lane", c)
		}
	}
	if calls[0].Ended || !calls[1].Ended || calls[2].Error != "rate_limit" || calls[2].Details != "429" {
		t.Errorf("calls %+v: want the turn's start, its end and the error", calls)
	}

	// The main checkout is no lane: no agent, so nothing is sent.
	run(p.Root, "user-prompt-submit", turn(p.Root))
	run(p.Root, "stop", stop(p.Root))
	run(p.Root, "stop-failure", failure(p.Root))
	if calls, _ = serve.requests(); len(calls) != 3 {
		t.Errorf("outside a lane serve got %v %+v, want none", serve.methods[3:], calls[3:])
	}
}
