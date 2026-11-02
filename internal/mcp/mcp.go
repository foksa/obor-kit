// Package mcp is obor's stdio MCP server: the job tools, JSON-RPC 2.0 over stdin and
// stdout, one message per line, hand-rolled (no SDK, go.mod unchanged). The tools are filtered by OBOR_ROLE, and each
// tool call is one request on obor serve's app socket (an wire.Verdict Call; T-470: the only path of the integrator's
// verdicts): the app decides, the server only renders its reply. `obor mcp` runs it; `obor mcp --check` checks it.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/project"
	"obor/internal/version"
)

// protocolVersion is the MCP revision the server answers when the client's initialize names none. A client that names
// one gets its own back: the messages and the tools are the same in every revision this server knows.
const protocolVersion = "2024-11-05"

// The tools, by their MCP names; each is one app socket method of the same name (jobs_contract.go, verdict.go).
const (
	toolJobNext  = "job_next"
	toolTaskDone = "task_done"
	toolAccept   = "accept"
	toolSendback = "sendback"
	toolReject   = "reject"
	toolResolved = "resolved"
	toolHold     = "hold"
)

// The JSON-RPC error codes the server answers.
const (
	codeParse          = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Options is one server run: the environment the agent's session exports (launch.EnvVars: a client with a stripped environment must forward them), the cwd the server was started in
// (each call's Root is that cwd's project), and the streams. Err gets the one startup warning (nil: os.Stderr).
type Options struct {
	Env project.Env
	Cwd string
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Serve runs the server on o.In/o.Out until the input ends: a response for every request, nothing for a notification.
// A line that is not JSON answers a parse error and the server goes on.
func Serve(ctx context.Context, o Options) error {
	s := &server{env: o.Env, cwd: o.Cwd, out: bufio.NewWriter(o.Out)}
	if s.role() == "" {
		w := o.Err
		if w == nil {
			w = os.Stderr
		}
		fmt.Fprintln(w, "obor mcp: OBOR_ROLE is not set: no tools (the client did not pass the agent's environment)")
	}
	in := bufio.NewReader(o.In)
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			s.handle(ctx, line)
			if ferr := s.out.Flush(); ferr != nil {
				return ferr
			}
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			return nil
		default:
			return err
		}
	}
}

// server is one run: the environment, the cwd, the output, and the last job_next reply. The integrator's verdict tools
// key their call on that job's task and job id (the app's verdicts name a task; the role text says to call job_next
// first, and job_next's reply is the only place the server learns them).
type server struct {
	env  project.Env
	cwd  string
	out  *bufio.Writer
	task string
	job  string
}

// request is one JSON-RPC message: a request (with an ID) or a notification (without).
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// response is the answer to a request; Result is left out of an error's.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handle answers one line.
func (s *server) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(response{JSONRPC: "2.0", Error: &rpcError{codeParse, "parse error: " + err.Error()}})
		return
	}
	// A notification (no id, or a null one) is never answered: notifications/initialized, cancelled, and anything the
	// server does not know.
	notification := len(req.ID) == 0 || string(req.ID) == "null"
	switch req.Method {
	case "initialize":
		if notification {
			return
		}
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: initialize(req.Params)})
	case "ping":
		if notification {
			return
		}
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		if notification {
			return
		}
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: listResult{Tools: infos(toolsFor(s.role()))}})
	case "tools/call":
		if notification {
			return
		}
		res, rerr := s.call(ctx, req.Params)
		if rerr != nil {
			s.write(response{JSONRPC: "2.0", ID: req.ID, Error: rerr})
			return
		}
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: res})
	default:
		if notification {
			return
		}
		s.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeMethodNotFound, "method not found: " + req.Method}})
	}
}

func (s *server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return // a response of strings and structs only: it cannot fail
	}
	_, _ = s.out.Write(append(b, '\n'))
}

// initialize is the handshake's result: the client's protocol version back (or the default), the tools capability and
// the server's identity.
func initialize(params json.RawMessage) initResult {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	v := p.ProtocolVersion
	if v == "" {
		v = protocolVersion
	}
	return initResult{ProtocolVersion: v, Capabilities: capabilities{Tools: map[string]any{"listChanged": false}},
		ServerInfo: serverInfo{Name: "obor", Version: version.Version}}
}

type initResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

type capabilities struct {
	Tools map[string]any `json:"tools"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// role is the session's role: OBOR_ROLE.
func (s *server) role() string { return s.env.Get("OBOR_ROLE") }

// call answers one tools/call: the tool must be in the role's table (else invalid params, the JSON-RPC answer to an
// unknown tool), its required arguments must be there, and the app's refusal — like serve being down — is a tool
// error result, never a broken server.
func (s *server) call(ctx context.Context, raw json.RawMessage) (callResult, *rpcError) {
	var p struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return callResult{}, &rpcError{codeInvalidParams, "invalid params: " + err.Error()}
		}
	}
	t, ok := lookup(s.role(), p.Name)
	if !ok {
		// Not in this role's table: the JSON-RPC answer to an unknown tool (a refusal of the app's is a tool error).
		name, role := p.Name, s.role()
		if name == "" {
			name = "-"
		}
		if role == "" {
			role = "-"
		}
		return callResult{}, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %s for role %s", name, role)}
	}
	args, err := t.arguments(p.Arguments)
	if err != nil {
		return errorResult(t.name + ": " + err.Error()), nil
	}
	root, err := project.Find(s.cwd)
	if err != nil {
		return errorResult(t.name + ": not in an obor project (no .obor/config.json)"), nil
	}
	c := wire.Call{Agent: s.env.Get("OBOR_AGENT"), Role: s.role(), Lane: s.env.Get("OBOR_LANE"),
		Pane: s.env.Get("HERDR_PANE_ID"), CallID: wire.NewCallID(), Root: root.Root}
	switch t.method {
	case wire.MethodJobNext:
		c.Job = args["job"] // the wake's job id (T-602): that job when it is open, else superseded{current}
	case wire.MethodTaskDone:
		if c.Lane == "" {
			return errorResult(t.name + ": OBOR_LANE is not set: task_done is the worker's tool"), nil
		}
		c.Report = args["report"] // serve resolves the lane to its task (T-439)
	case wire.MethodAccept:
		c.Task, c.Job, c.Patch, c.Note = s.task, s.job, args["patch"], args["note"]
	case wire.MethodResolved:
		c.Task, c.Job = s.task, s.job
	default: // sendback, reject, hold: the why is the call's note
		c.Task, c.Job, c.Note = s.task, s.job, args["why"]
	}
	reply, err := wire.Verdict(ctx, wire.SocketPath(root.Root), t.method, c)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if t.method == wire.MethodJobNext {
		text, task, job := renderJob(reply)
		s.task, s.job = task, job
		return textResult(text), nil
	}
	return textResult(reply), nil
}

// renderJob is job_next's readable text and, for a job, its task and id — the verdict tools' call names them. The two
// other states get their own one line, and a reply that is not a JobReply at all (an older serve) is passed through.
func renderJob(reply string) (text, task, job string) {
	var r wire.JobReply
	if err := json.Unmarshal([]byte(reply), &r); err != nil || r.State == "" {
		return reply, "", ""
	}
	switch r.State {
	case "job":
		head := fmt.Sprintf("JOB %s (task %s, attempt %d, step %s)", r.Job, r.Task, r.Attempt, r.Step)
		if strings.TrimSpace(r.Text) == "" {
			return head, r.Task, r.Job
		}
		return head + "\n\n" + r.Text, r.Task, r.Job
	case "superseded":
		return fmt.Sprintf("SUPERSEDED BY %s (call job_next again)", r.Current), "", ""
	default: // "none", and a state a later app may add: its own line
		return "NO JOB (" + r.State + ")", "", ""
	}
}

// callResult is tools/call's result: one text content, and isError for a refusal.
type callResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(text string) callResult {
	return callResult{Content: []content{{Type: "text", Text: text}}}
}

func errorResult(text string) callResult {
	return callResult{Content: []content{{Type: "text", Text: text}}, IsError: true}
}
