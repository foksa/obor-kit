package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// answer is reply's line decoded.
func answer(t *testing.T, line string) map[string]any {
	t.Helper()
	b := reply([]byte(line))
	if b == nil {
		t.Fatalf("reply(%s) = nil, want an answer", line)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("reply(%s) is not JSON: %s", line, b)
	}
	return m
}

func TestReplyInitializeEchoesProtocolAndOffersTools(t *testing.T) {
	m := answer(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`)
	if m["id"] != float64(1) || m["jsonrpc"] != "2.0" {
		t.Fatalf("id/jsonrpc not echoed: %v", m)
	}
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", m)
	}
	if r["protocolVersion"] != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want the client's 2025-11-25", r["protocolVersion"])
	}
	if caps, _ := r["capabilities"].(map[string]any); caps["tools"] == nil {
		t.Errorf("capabilities = %v, want tools", r["capabilities"])
	}
}

func TestReplyInitializeWithoutVersionNamesOne(t *testing.T) {
	m := answer(t, `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{}}`)
	if m["id"] != "a" {
		t.Fatalf("string id not echoed: %v", m)
	}
	if v := m["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want the default 2025-06-18", v)
	}
}

func TestReplyToolsListHasPing(t *testing.T) {
	m := answer(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, _ := m["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "ping" {
		t.Fatalf("tools = %v, want one tool ping", tools)
	}
	if tools[0].(map[string]any)["inputSchema"] == nil {
		t.Errorf("ping has no inputSchema: %v", tools[0])
	}
}

func TestReplyPingAndUnknownMethod(t *testing.T) {
	if m := answer(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`); m["result"] == nil || m["error"] != nil {
		t.Errorf("ping: %v, want an empty result", m)
	}
	m := answer(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ping"}}`)
	e, _ := m["error"].(map[string]any)
	if e["code"] != float64(-32601) || m["result"] != nil {
		t.Errorf("unknown method: %v, want error -32601 and no result", m)
	}
}

func TestReplyIgnoresNotificationsAndNoise(t *testing.T) {
	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`not json`,
		``,
	} {
		if b := reply([]byte(line)); b != nil {
			t.Errorf("reply(%q) = %s, want nil", line, b)
		}
	}
}

func TestServeAnswersEachRequestLineInOrder(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"x"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d answer lines, want 2 (the notification gets none): %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"id":1`) || !strings.Contains(lines[1], `"id":2`) {
		t.Errorf("answers out of order: %q", lines)
	}
}

func TestWriteRecordNamesFileByModeNameAndPid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out") // write makes it
	r := Record{Mode: "hook", Name: "Session Start/x", PID: 42, PPID: 7, Cwd: "/w", Argv: []string{"obor", "hook"},
		Env: map[string]string{"OBOR_ROLE": "worker"}, Stdin: `{"session_id":"s"}`}
	if err := write(dir, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "hook-Session_Start_x-42.json"))
	if err != nil {
		t.Fatalf("record file: %v", err)
	}
	var got Record
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.PPID != 7 || got.Env["OBOR_ROLE"] != "worker" || got.Stdin != r.Stdin || got.Cwd != "/w" {
		t.Errorf("record = %+v, want %+v", got, r)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestNewRecordSeesThisProcess(t *testing.T) {
	t.Setenv("OBOR_CONF_PROBE", "1")
	r := newRecord("mcp", "x", "")
	if r.PID != os.Getpid() || r.PPID != os.Getppid() {
		t.Errorf("pid/ppid = %d/%d, want %d/%d", r.PID, r.PPID, os.Getpid(), os.Getppid())
	}
	if r.Env["OBOR_CONF_PROBE"] != "1" {
		t.Errorf("env lacks OBOR_CONF_PROBE: %v", r.Env)
	}
	if wd, _ := os.Getwd(); r.Cwd != wd {
		t.Errorf("cwd = %s, want %s", r.Cwd, wd)
	}
}
