package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// triageProject is a served project with roles.triage configured (axis-alies' shape): its triage agent is pj-triage.
func triageProject(t *testing.T) *testkit.Project {
	t.Helper()
	return servedProject(t, testkit.Set("roles.triage", "opus"))
}

// T-796: a triage session (OBOR_ROLE=triage, as setup and obor restart export it, in the main checkout) has the
// derived identity: obor whoami says role triage, and a CLI call to serve is the triage agent's, not the owner's.
func TestTriageWhoamiAndCaller(t *testing.T) {
	t.Parallel()
	p := triageProject(t)
	cfg, err := config.LoadDaemon(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if c := caller(p.Env(), p.Root, cfg); c.Role != "" || c.Agent != "" {
		t.Fatalf("precondition: no OBOR_ROLE in the main checkout is the owner (no role, no name): %+v", c)
	}
	text, j := whoamiIn(t, p.Root, "OBOR_ROLE=triage")
	if !strings.Contains(text, "role: triage\n") || !strings.Contains(text, "source: env\n") {
		t.Errorf("whoami with OBOR_ROLE=triage: %q", text)
	}
	if j["role"] != "triage" {
		t.Errorf("whoami --json role %v, want triage", j["role"])
	}
	if c := caller(p.Env("OBOR_ROLE=triage"), p.Root, cfg); c.Role != "triage" || c.Agent != "pj-triage" || c.Root != p.Root {
		t.Errorf("triage's call: %+v; want role triage, agent pj-triage (not the owner)", c)
	}
}

// T-796: SessionStart of a triage session records its session (serve gets pj-triage's) and its project inbox (slot
// triage), so a brief to pj-triage is posted to the session, not typed.
func TestTriageSessionStartRecordsItsInbox(t *testing.T) {
	t.Parallel()
	p := triageProject(t)
	dir, err := os.MkdirTemp("", "tri") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		c.Close()
		got <- string(b)
	}()
	if _, ok := inboxed(p.Root)("pj-triage"); ok {
		t.Fatal("precondition: no inbox record of pj-triage")
	}

	_, serve := startFakeServe(t, p.Root, wire.Status{}, "SESSION pj-triage sid-t1", "")
	env := []string{"OBOR_ROLE=triage", "OBOR_STATE_HOME=" + t.TempDir(), "HERDR_PANE_ID=w1:p9",
		"CLAUDE_CODE_MESSAGING_SOCKET=" + sock, "CLAUDE_CODE_MESSAGING_TOKEN=tok-t"}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "cwd": p.Root, "session_id": "sid-t1",
		"source": "startup"})
	rc, stdout, stderr, _ := shimRun(t, p.Root, string(event), env, oborBin, "hook", "session-start")
	if rc != 0 {
		t.Fatalf("session-start: exit %d, %s", rc, stderr)
	}
	if !strings.Contains(stdout, "obor: role triage,") {
		t.Errorf("session-start output names no triage role:\n%s", stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.methods[0] != wire.MethodSession || calls[0].Agent != "pj-triage" ||
		calls[0].Session != "sid-t1" || calls[0].Root != p.Root {
		t.Fatalf("serve got %v %+v; want pj-triage's session sid-t1", serve.methods, calls)
	}
	if s, ok := inboxed(p.Root)("pj-triage"); !ok || s != "sid-t1" {
		t.Fatalf("inboxed(pj-triage) = %q, %v; want the session's record", s, ok)
	}

	const line = "[obor pj-lead] bug B-03: retest"
	if began, err := (poster{p.Root}).Post(context.Background(), agents.Default, "pj-triage", line); !began || err != nil {
		t.Fatalf("brief to pj-triage: began %v, %v; want posted to its inbox", began, err)
	}
	select {
	case b := <-got:
		if !strings.Contains(b, `"token":"tok-t"`) || !strings.Contains(b, "bug B-03: retest") {
			t.Errorf("the triage session read %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the triage session read nothing")
	}
	if _, err := project.ReadInbox(p.Root, "triage"); err != nil {
		t.Errorf("inbox slot triage: %v", err)
	}
}

// T-796: the stop-failure hook of a triage session names the triage agent and its role to serve.
func TestTriageStopFailureNamesIt(t *testing.T) {
	t.Parallel()
	p := triageProject(t)
	_, serve := startFakeServe(t, p.Root, wire.Status{}, "STOPPED pj-triage", "")
	event, _ := json.Marshal(map[string]any{"hook_event_name": "StopFailure", "cwd": p.Root, "error": "rate_limit"})
	if rc, stdout, _, _ := shimRun(t, p.Root, string(event), []string{"OBOR_ROLE=triage"}, oborBin, "hook",
		"stop-failure"); rc != 0 || stdout != "" {
		t.Errorf("exit %d, stdout %q; want 0, nothing", rc, stdout)
	}
	calls, _ := serve.requests()
	if len(calls) != 1 || serve.methods[0] != wire.MethodStopped || calls[0].Agent != "pj-triage" ||
		calls[0].Role != "triage" || calls[0].Error != "rate_limit" {
		t.Errorf("serve got %v %+v; want one stopped request of pj-triage (role triage)", serve.methods, calls)
	}
}
