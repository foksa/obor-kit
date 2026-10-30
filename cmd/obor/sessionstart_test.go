package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"obor/internal/app"
	"obor/internal/testkit"
)

// T-581: an OpenCode lane's session-start has no OBOR_AGENT (its plugin runs in a shared process): obor reports the
// session to serve under the lane's agent, and writes no .sid (it resumes no OpenCode session).
func TestSessionStartOpencodeLaneReportsNoSid(t *testing.T) {
	t.Parallel()
	p := servedProject(t, testkit.Set("lane_types.opus", `{"kind":"opencode"}`))
	lane := p.LaneDir("O-01")
	state := t.TempDir()
	env := []string{"OBOR_STATE_HOME=" + state}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": lane, "session_id": "sid-oc",
		"transcript_path": "/data/opencode/opencode.db", "source": "startup"})

	_, serve := startFakeServe(t, p.Root, app.Status{}, "SESSION pj-o-01 sid-oc", "")
	if rc, _, _, _ := shimRun(t, lane, string(event), env, herdBin, "hook", "session-start"); rc != 0 {
		t.Errorf("exit %d, want 0", rc)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 {
		t.Fatalf("serve got %d requests, want one", len(calls))
	}
	c := calls[0]
	if serve.methods[0] != app.MethodSession || c.Agent != "pj-o-01" || c.Kind != "opencode" || c.Session != "sid-oc" ||
		c.Transcript != "/data/opencode/opencode.db" || c.Lane != "O-01" || c.Root != p.Root {
		t.Errorf("serve got %s %+v", serve.methods[0], c)
	}
	if _, err := os.Stat(filepath.Join(state, "agents", "pj-o-01.sid")); err == nil {
		t.Error("an OpenCode session wrote a .sid")
	}
}

// T-577: obor hook session-start sends serve one session request with the agent, its kind, the session id and its
// transcript_path, the pane, lane and task; serve down: the <agent>.sid it writes as before is the fallback, and the
// hook still exits 0.
func TestSessionStartHookReports(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	lane := p.LaneDir("O-01")
	state := t.TempDir()
	env := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-o-01", "OBOR_LANE=O-01", "OBOR_STATE_HOME=" + state,
		"HERDR_PANE_ID=w1:p3"}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": lane, "session_id": "sid-77",
		"transcript_path": "/claude/projects/x/sid-77.jsonl", "source": "startup"})

	if _, err := os.Stat(app.SocketPath(p.Root)); err == nil {
		t.Fatal("precondition: no serve socket")
	}
	if rc, _, _, _ := shimRun(t, lane, string(event), env, herdBin, "hook", "session-start"); rc != 0 {
		t.Errorf("serve down: exit %d, want 0", rc)
	}
	if b, err := os.ReadFile(filepath.Join(state, "agents", "pj-o-01.sid")); err != nil || string(b) != "sid-77\n" {
		t.Fatalf("serve down: .sid %q (%v), want the fallback sid-77", b, err)
	}

	_, serve := startFakeServe(t, p.Root, app.Status{}, "SESSION pj-o-01 sid-77", "")
	if rc, _, _, _ := shimRun(t, lane, string(event), env, herdBin, "hook", "session-start"); rc != 0 {
		t.Errorf("serve up: exit %d, want 0", rc)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 {
		t.Fatalf("serve got %d requests, want one", len(calls))
	}
	c := calls[0]
	if serve.methods[0] != app.MethodSession || c.Agent != "pj-o-01" || c.Kind != "claude" || c.Session != "sid-77" ||
		c.Transcript != "/claude/projects/x/sid-77.jsonl" || c.Pane != "w1:p3" || c.Lane != "O-01" || c.Root != p.Root ||
		c.Started == 0 {
		t.Errorf("serve got %s %+v", serve.methods[0], c)
	}
}
