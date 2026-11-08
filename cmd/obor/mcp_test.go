package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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
		{[]string{"OBOR_ROLE=integrator"}, "MCP OK (integrator: accept, hold, job_next, reject, resolved, sendback)\n"},
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
// with nothing, and EOF (after the answers: the SDK drops a call still running at EOF, as a client that hung up wants)
// ends the server with exit 0. It runs as a lane worker's does: in the lane, no OBOR_AGENT.
func TestMcpBinaryServes(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "")
	p.Config() // the caller's name derives from it (T-786, T-791)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, oborBin, "mcp")
	cmd.Dir, cmd.WaitDelay = lane, 5*time.Second
	cmd.Env = append(os.Environ(), "OBOR_ROLE=worker", "OBOR_LANE=L-01", "OBOR_STATE_HOME="+p.Base+"/no-serve", callDown)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stdin, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"job_next","arguments":{}}}`,
		"",
	}, "\n"))
	type answer struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	lines := map[int]string{} // by id: the SDK answers calls concurrently, in any order
	answers := map[int]answer{}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 1<<20)
	for len(lines) < 3 && sc.Scan() {
		var a answer
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil || a.ID < 1 || a.ID > 3 || lines[a.ID] != "" {
			t.Fatalf("answer %q: %v; want one answer each for ids 1-3", sc.Text(), err)
		}
		lines[a.ID], answers[a.ID] = sc.Text(), a
	}
	stdin.Close()
	if len(lines) != 3 {
		t.Fatalf("obor mcp: %d answers; want 3: %v", len(lines), lines)
	}
	if sc.Scan() {
		t.Errorf("obor mcp: a fourth line %q; want none (the notification gets no answer)", sc.Text())
	}
	if err := cmd.Wait(); err != nil || stderr.Len() > 0 {
		t.Fatalf("obor mcp at EOF: %v, stderr %q; want exit 0 and no stderr", err, stderr.String())
	}
	if !strings.Contains(lines[1], `"protocolVersion":"2025-06-18"`) || !strings.Contains(lines[1], `"serverInfo":{"name":"obor"`) {
		t.Errorf("initialize: %s; want the client's version and obor", lines[1])
	}
	if !strings.Contains(lines[2], `"name":"job_next"`) || !strings.Contains(lines[2], `"name":"task_done"`) ||
		strings.Contains(lines[2], `"name":"accept"`) {
		t.Errorf("tools/list: %s; want the worker's two tools only", lines[2])
	}
	var tool struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	if err := json.Unmarshal(answers[3].Result, &tool); err != nil || !tool.IsError || len(tool.Content) != 1 ||
		!strings.Contains(tool.Content[0].Text, "obor serve is not running") {
		t.Errorf("job_next, serve down: %s; want a tool error naming it", answers[3].Result)
	}
}
