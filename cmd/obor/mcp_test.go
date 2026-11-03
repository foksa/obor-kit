package main

import (
	"encoding/json"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// obor mcp --check: the doctor line of the role's tools (the server itself: internal/mcp's tests); a bad flag is usage.
// obor has mcp: the binary knows the command (bin/go-commands lists it for the shim).
func TestMcpCheck(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	for _, c := range []struct {
		env []string
		out string
	}{
		{[]string{"OBOR_ROLE=worker"}, "MCP OK (worker: job_next, task_done)\n"},
		{[]string{"OBOR_ROLE=integrator"}, "MCP OK (integrator: job_next, accept, sendback, reject, resolved, hold)\n"},
		{[]string{"OBOR_ROLE=lead"}, "MCP OK (lead: no tools)\n"},
		{nil, "MCP OK (-: no tools)\n"},
	} {
		rc, stdout, stderr := run(t, dir, c.env, "mcp", "--check")
		if rc != 0 || stdout != c.out || stderr != "" {
			t.Errorf("obor mcp --check %v: exit %d, %q, %q; want 0, %q", c.env, rc, stdout, stderr, c.out)
		}
	}
	if rc, _, stderr := run(t, dir, nil, "mcp", "--bogus"); rc != 2 || !strings.Contains(stderr, "usage: obor mcp [--check]") {
		t.Errorf("obor mcp --bogus: exit %d, %q; want 2 and the usage", rc, stderr)
	}
	if rc, stdout, stderr := run(t, dir, nil, "has", "mcp"); rc != 0 || stdout+stderr != "" {
		t.Errorf("obor has mcp: exit %d, %q %q; want 0 and nothing", rc, stdout, stderr)
	}
}

// obor mcp through the binary: one JSON-RPC message per line on stdin and stdout (the protocol, never the out buffer),
// the handshake and the role's tools, and a job_next with obor serve down as a tool error. The notification is answered
// with nothing, and EOF ends the server with exit 0.
func TestMcpBinaryServes(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	env := []string{"OBOR_ROLE=worker", "OBOR_AGENT=pj-l-01", "OBOR_LANE=L-01", "OBOR_STATE_HOME=" + p.Base + "/no-serve", callDown}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"job_next","arguments":{}}}`,
		"",
	}, "\n")
	rc, stdout, stderr := runIn(t, p.Root, env, in, "mcp")
	if rc != 0 || stderr != "" {
		t.Fatalf("obor mcp: exit %d, stderr %q", rc, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("obor mcp: %d answer lines; want 3 (the notification gets none):\n%s", len(lines), stdout)
	}
	type answer struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	answers := make([]answer, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &answers[i]); err != nil {
			t.Fatalf("answer %d %q: %v", i, line, err)
		}
		if answers[i].ID != i+1 {
			t.Fatalf("answer %d has id %d; want %d", i, answers[i].ID, i+1)
		}
	}
	if !strings.Contains(lines[0], `"protocolVersion":"2025-06-18"`) || !strings.Contains(lines[0], `"serverInfo":{"name":"obor"`) {
		t.Errorf("initialize: %s; want the client's version and obor", lines[0])
	}
	if !strings.Contains(lines[1], `"name":"job_next"`) || !strings.Contains(lines[1], `"name":"task_done"`) ||
		strings.Contains(lines[1], `"name":"accept"`) {
		t.Errorf("tools/list: %s; want the worker's two tools only", lines[1])
	}
	var tool struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if err := json.Unmarshal(answers[2].Result, &tool); err != nil || !tool.IsError || len(tool.Content) != 1 ||
		!strings.Contains(tool.Content[0].Text, "obor serve is not running") {
		t.Errorf("job_next, serve down: %s; want a tool error naming it", answers[2].Result)
	}
}
