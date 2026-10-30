package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/project"
	"obor/internal/testkit"
)

// fixture is an obor project for the server's cwd: a main checkout (a .git marker is all project.Find reads) with the
// worker's lane dir standing in for the worktree, and a state home (where obor serve's socket is).
type fixture struct {
	t    *testing.T
	root string
	lane string
	home string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// A short base: the project's serve socket path must fit a unix socket (104 bytes), and the test's temp dir is long.
	base, err := os.MkdirTemp("", "hm")
	if err == nil {
		t.Cleanup(func() { os.RemoveAll(base) })
		base, err = filepath.EvalSymlinks(base)
	}
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: filepath.Join(base, "proj"), lane: filepath.Join(base, "proj", "lane-O-01"), home: t.TempDir()}
	for _, d := range []string{f.root + "/.git", f.lane, f.root + "/.obor/state"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) write(path, text string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// env is the server's environment: the state home, plus kv ("VAR=value").
func (f *fixture) env(kv ...string) project.Env {
	m := map[string]string{"OBOR_STATE_HOME": f.home, "OBOR_PLUGIN_ROOT": "/kit"}
	for _, e := range kv {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return testkit.MapEnv(m)
}

// worker is the environment of the lane's worker agent.
func (f *fixture) worker() project.Env {
	return f.env("OBOR_ROLE=worker", "OBOR_AGENT=pj-o-01", "OBOR_LANE=O-01", "HERDR_PANE_ID=w1:p1")
}

// fakeServe is obor serve's app socket in a test: it records each request line and answers reply/refusal, like the
// app's own Listen.
type fakeServe struct {
	mu     sync.Mutex
	reqs   []wireRequest
	answer func(method string, c app.Call) (string, string)
}

type wireRequest struct {
	Method string   `json:"method"`
	Call   app.Call `json:"call"`
}

// serveFake listens at path and answers until the test ends.
func serveFake(t *testing.T, path string, a *fakeServe) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req wireRequest
				if json.Unmarshal(b, &req) != nil {
					_, _ = c.Write([]byte(`{"error":"unreadable request"}` + "\n"))
					return
				}
				reply, refusal := a.answer(req.Method, req.Call)
				a.mu.Lock()
				a.reqs = append(a.reqs, req)
				a.mu.Unlock()
				ans := map[string]string{"reply": reply}
				if refusal != "" {
					ans = map[string]string{"error": refusal}
				}
				out, _ := json.Marshal(ans)
				_, _ = c.Write(append(out, '\n'))
			}()
		}
	}()
}

// requests is the recorded requests, in order.
func (a *fakeServe) requests() []wireRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]wireRequest(nil), a.reqs...)
}

// client is a scripted MCP client over the server's pipes.
type client struct {
	t    *testing.T
	in   io.WriteCloser
	enc  *json.Encoder
	dec  *bufio.Reader
	done chan error
}

// answerWait bounds one line through the pipes: a server that answers nothing fails the test there, not at go test's
// timeout.
const answerWait = 10 * time.Second

// start runs a server on pipes with env and cwd; the client writes requests and reads the one line of each answer.
func start(t *testing.T, env project.Env, cwd string) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := Serve(context.Background(), Options{Env: env, Cwd: cwd, In: inR, Out: outW})
		outW.Close()
		done <- err
	}()
	t.Cleanup(func() { outR.Close() })
	t.Cleanup(func() { inW.Close() })
	return &client{t: t, in: inW, enc: json.NewEncoder(inW), dec: bufio.NewReader(outR), done: done}
}

// answer is one answer line of the server.
type answer struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// send writes one raw line to the server: a server that does not read fails the test in a moment, not at go test's
// timeout.
func (c *client) send(line string) {
	c.t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.in, line)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			c.t.Fatal(err)
		}
	case <-time.After(answerWait):
		c.t.Fatalf("the server read nothing within %s", answerWait)
	}
}

// read is the next answer line.
func (c *client) read() answer {
	c.t.Helper()
	type line struct {
		b   []byte
		err error
	}
	done := make(chan line, 1)
	go func() {
		b, err := c.dec.ReadBytes('\n')
		done <- line{b, err}
	}()
	var l line
	select {
	case l = <-done:
	case <-time.After(answerWait):
		c.t.Fatalf("no answer within %s", answerWait)
	}
	if l.err != nil {
		c.t.Fatalf("no answer: %v", l.err)
	}
	var r answer
	if err := json.Unmarshal(l.b, &r); err != nil {
		c.t.Fatalf("answer %q: %v", l.b, err)
	}
	if r.JSONRPC != "2.0" {
		c.t.Fatalf("answer %q: jsonrpc is not 2.0", l.b)
	}
	return r
}

// request sends one request (params nil: none) and reads its answer.
func (c *client) request(id int, method string, params any) answer {
	c.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(string(b) + "\n")
	return c.read()
}

// notify sends one notification: the server answers nothing.
func (c *client) notify(method string) {
	c.t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(string(b) + "\n")
}

// result is a request's result decoded into out, failing on an error answer.
func (c *client) result(id int, method string, params, out any) {
	c.t.Helper()
	r := c.request(id, method, params)
	if r.Error != nil {
		c.t.Fatalf("%s: error %d: %s", method, r.Error.Code, r.Error.Message)
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		c.t.Fatalf("%s: result %q: %v", method, r.Result, err)
	}
}

// toolResult is tools/call's result.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// tool calls the tool and fails on a JSON-RPC error (an unknown tool), so a test reads only the tool's own answer.
func (c *client) tool(id int, name string, args map[string]any) toolResult {
	c.t.Helper()
	r := c.request(id, "tools/call", map[string]any{"name": name, "arguments": args})
	if r.Error != nil {
		c.t.Fatalf("tools/call %s: error %d: %s", name, r.Error.Code, r.Error.Message)
	}
	var res toolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		c.t.Fatalf("tools/call %s: result %q: %v", name, r.Result, err)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		c.t.Fatalf("tools/call %s: content %+v; want one text", name, res.Content)
	}
	return res
}

// stop ends the server (its stdin closes) and returns its error.
func (c *client) stop() error {
	c.in.Close()
	select {
	case err := <-c.done:
		return err
	case <-time.After(answerWait):
		c.t.Fatalf("the server did not end at EOF within %s", answerWait)
		return nil
	}
}

// TestHandshake: initialize names the server and its tools capability (the client's protocol version back), ping is an
// empty result, and notifications/initialized is answered with nothing at all — the next answer is the ping's.
func TestHandshake(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := start(t, f.worker(), f.root)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	c.result(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}}, &init)
	if init.ProtocolVersion != "2025-06-18" || init.ServerInfo.Name != "obor" || init.Capabilities.Tools == nil {
		t.Errorf("initialize: %+v; want the client's version, obor, the tools capability", init)
	}
	c.notify("notifications/initialized")
	r := c.request(2, "ping", nil)
	if string(r.ID) != "2" || string(r.Result) != "{}" {
		t.Errorf("ping: id %s, result %s; want id 2 and {} (and no answer to the notification)", r.ID, r.Result)
	}
	if err := c.stop(); err != nil {
		t.Errorf("the server at EOF: %v; want no error", err)
	}
}

// TestToolsListPerRole: tools/list is the role's table — the worker's two, the integrator's six, nothing at all for the
// lead, an unknown role or no role (an empty list, not null).
func TestToolsListPerRole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, tc := range []struct {
		env   []string
		names string
	}{
		{[]string{"OBOR_ROLE=worker"}, `["job_next","task_done"]`},
		{[]string{"OBOR_ROLE=integrator"}, `["job_next","accept","sendback","reject","resolved","hold"]`},
		{[]string{"OBOR_ROLE=lead"}, `[]`},
		{[]string{"OBOR_ROLE=deep_dive"}, `[]`},
		{nil, `[]`},
	} {
		c := start(t, f.env(tc.env...), f.root)
		r := c.request(1, "tools/list", nil)
		var got struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				InputSchema struct {
					Type       string                    `json:"type"`
					Properties map[string]map[string]any `json:"properties"`
					Required   []string                  `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		}
		if r.Error != nil {
			t.Fatalf("%v: tools/list: %s", tc.env, r.Error.Message)
		}
		if err := json.Unmarshal(r.Result, &got); err != nil {
			t.Fatalf("%v: %v", tc.env, err)
		}
		names := make([]string, 0, len(got.Tools))
		for _, tool := range got.Tools {
			names = append(names, tool.Name)
			if tool.Description == "" || tool.InputSchema.Type != "object" || tool.InputSchema.Properties == nil {
				t.Errorf("%v: tool %+v; want a description and an object schema", tc.env, tool)
			}
			if props := tool.InputSchema.Properties; tool.Name == "task_done" && (props["report"] == nil || len(props) != 1 ||
				len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "report") {
				t.Errorf("task_done's schema: %+v; want report (required)", tool.InputSchema)
			}
			if tool.Name == "accept" {
				if props := tool.InputSchema.Properties; props["patch"] == nil || props["note"] == nil ||
					len(props) != 2 || len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "patch" {
					t.Errorf("accept's schema: %+v; want patch (required) and note", tool.InputSchema)
				}
			}
		}
		if b, _ := json.Marshal(names); string(b) != tc.names {
			t.Errorf("%v: tools %s; want %s", tc.env, b, tc.names)
		}
		if tc.names == `[]` && !strings.Contains(string(r.Result), `"tools":[]`) {
			t.Errorf("%v: an empty table is tools: [], not null: %s", tc.env, r.Result)
		}
	}
}

// TestWorkerTools: job_next renders the JobReply readably and task_done sends the lane's task and its report; each call carries the
// agent's identity (root, agent, role, lane, pane) and its own call id.
func TestWorkerTools(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := &fakeServe{answer: func(method string, c app.Call) (string, string) {
		switch method {
		case app.MethodJobNext:
			return `{"state":"job","job":"T-01/1/work","task":"T-01","attempt":1,"step":"work","text":"do the work"}`, ""
		default:
			return "submitted", ""
		}
	}}
	serveFake(t, app.SocketPath(f.root), a)
	c := start(t, f.worker(), f.lane)

	got := c.tool(1, "job_next", nil)
	if got.IsError || got.Content[0].Text != "JOB T-01/1/work (task T-01, attempt 1, step work)\n\ndo the work" {
		t.Errorf("job_next: %+v; want the job line and its text", got)
	}
	if got := c.tool(9, "task_done", nil); !got.IsError || !strings.Contains(got.Content[0].Text, `"report" argument is required`) {
		t.Errorf("task_done without a report: %+v; want a tool error naming it", got)
	}
	got = c.tool(2, "task_done", map[string]any{"report": "Summary: x"})
	if got.IsError || got.Content[0].Text != "submitted" {
		t.Errorf("task_done: %+v; want submitted", got)
	}
	reqs := a.requests()
	if len(reqs) != 2 {
		t.Fatalf("the app got %d requests; want 2: %+v", len(reqs), reqs)
	}
	for i, call := range []app.Call{reqs[0].Call, reqs[1].Call} {
		if call.Root != f.root || call.Agent != "pj-o-01" || call.Role != "worker" || call.Lane != "O-01" ||
			call.Pane != "w1:p1" {
			t.Errorf("call %d: %+v; want root %s, the agent, worker, O-01, w1:p1", i, call, f.root)
		}
		if len(call.CallID) != 16 {
			t.Errorf("call %d: call id %q; want a fresh 16-hex id", i, call.CallID)
		}
	}
	if reqs[0].Method != app.MethodJobNext || reqs[1].Method != app.MethodTaskDone {
		t.Errorf("methods %s, %s; want %s, %s", reqs[0].Method, reqs[1].Method, app.MethodJobNext, app.MethodTaskDone)
	}
	if reqs[0].Call.CallID == reqs[1].Call.CallID {
		t.Errorf("two tool calls share the call id %s", reqs[0].Call.CallID)
	}
	if reqs[1].Call.Task != "" || reqs[1].Call.Report != "Summary: x" {
		t.Errorf("task_done's task %q, report %q; want no task (serve resolves the lane, T-439) and the report",
			reqs[1].Call.Task, reqs[1].Call.Report)
	}
}

// TestJobNextStates: a superseded job and none each get their own one line, and a reply that is not a JobReply (an
// older serve) is passed through.
func TestJobNextStates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cases := []struct{ reply, want string }{
		{`{"state":"none"}`, "NO JOB (none)"},
		{`{"state":"superseded","current":"T-01/2/work"}`, "SUPERSEDED BY T-01/2/work (call job_next again)"},
		{`a plain line`, "a plain line"},
	}
	var mu sync.Mutex
	next := 0
	a := &fakeServe{answer: func(string, app.Call) (string, string) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(cases) {
			return "", "unexpected request"
		}
		next++
		return cases[next-1].reply, ""
	}}
	serveFake(t, app.SocketPath(f.root), a)
	c := start(t, f.worker(), f.lane)
	for i, tc := range cases {
		if got := c.tool(i+1, "job_next", nil); got.IsError || got.Content[0].Text != tc.want {
			t.Errorf("job_next %s: %+v; want %q", tc.reply, got, tc.want)
		}
	}
}

// TestIntegratorVerdicts: job_next first (the review job names the task), then each verdict tool is one app request
// with that task and job, the caller's identity, and its own arguments.
func TestIntegratorVerdicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := &fakeServe{answer: func(method string, c app.Call) (string, string) {
		if method == app.MethodJobNext {
			return `{"state":"job","job":"T-05/1/review","task":"T-05","attempt":1,"step":"review","text":"review it"}`, ""
		}
		return strings.ToUpper(method) + " T-05", ""
	}}
	serveFake(t, app.SocketPath(f.root), a)
	c := start(t, f.env("OBOR_ROLE=integrator", "OBOR_AGENT=pj-int", "HERDR_PANE_ID=w1:p2"), f.root)
	if got := c.tool(1, "job_next", nil); got.IsError || !strings.Contains(got.Content[0].Text, "T-05/1/review") {
		t.Fatalf("job_next: %+v; want the review job", got)
	}
	for i, tc := range []struct {
		tool  string
		args  map[string]any
		patch string
		note  string
	}{
		{"accept", map[string]any{"patch": "abc123", "note": "looks right"}, "abc123", "looks right"},
		{"sendback", map[string]any{"why": "tests fail"}, "", "tests fail"},
		{"reject", map[string]any{"why": "wrong approach"}, "", "wrong approach"},
		{"resolved", nil, "", ""},
		{"hold", map[string]any{"why": "which branch?"}, "", "which branch?"},
	} {
		got := c.tool(i+2, tc.tool, tc.args)
		if got.IsError || got.Content[0].Text != strings.ToUpper(tc.tool)+" T-05" {
			t.Errorf("%s: %+v; want the app's reply", tc.tool, got)
		}
	}
	reqs := a.requests()
	if len(reqs) != 6 {
		t.Fatalf("the app got %d requests; want 6 (job_next and five verdicts): %+v", len(reqs), reqs)
	}
	for i, tc := range []struct {
		tool  string
		patch string
		note  string
	}{{"accept", "abc123", "looks right"}, {"sendback", "", "tests fail"}, {"reject", "", "wrong approach"},
		{"resolved", "", ""}, {"hold", "", "which branch?"}} {
		got := reqs[i+1]
		if got.Method != tc.tool || got.Call.Patch != tc.patch || got.Call.Note != tc.note {
			t.Errorf("%s: %+v; want method %s, patch %q, note %q", tc.tool, got, tc.tool, tc.patch, tc.note)
		}
		if got.Call.Task != "T-05" || got.Call.Job != "T-05/1/review" {
			t.Errorf("%s: task %q, job %q; want the fetched job's T-05 and T-05/1/review", tc.tool, got.Call.Task, got.Call.Job)
		}
		if got.Call.Agent != "pj-int" || got.Call.Role != "integrator" || got.Call.Lane != "" || got.Call.Pane != "w1:p2" ||
			got.Call.Root != f.root {
			t.Errorf("%s: %+v; want the integrator's identity", tc.tool, got.Call)
		}
	}
}

// TestMalformedLineAndUnknownMethod: a line that is not JSON is a parse error with a null id and the server goes on; an
// unknown method is method-not-found.
func TestMalformedLineAndUnknownMethod(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := start(t, f.worker(), f.root)
	c.send("not json\n")
	r := c.read()
	if r.Error == nil || r.Error.Code != codeParse || string(r.ID) != "null" {
		t.Errorf("malformed line: %+v (id %s); want parse error %d and a null id", r, r.ID, codeParse)
	}
	if r := c.request(2, "ping", nil); r.Error != nil || string(r.Result) != "{}" {
		t.Errorf("after a malformed line: %+v; want the server answering", r)
	}
	r = c.request(3, "resources/list", nil)
	if r.Error == nil || r.Error.Code != codeMethodNotFound || !strings.Contains(r.Error.Message, "resources/list") {
		t.Errorf("unknown method: %+v; want method not found %d", r, codeMethodNotFound)
	}
}

// TestToolErrorsAreResults: serve being down, the app's refusal and a bad argument are tool error results (the
// server keeps serving), and a tool of another role is the JSON-RPC invalid-params error.
func TestToolErrorsAreResults(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := start(t, f.worker(), f.lane)
	if got := c.tool(1, "job_next", nil); !got.IsError || !strings.Contains(got.Content[0].Text, "obor serve is not running") {
		t.Errorf("serve down: %+v; want a tool error naming it", got)
	}
	r := c.request(2, "tools/call", map[string]any{"name": "accept", "arguments": map[string]any{"patch": "abc"}})
	if r.Error == nil || r.Error.Code != codeInvalidParams || !strings.Contains(r.Error.Message, "unknown tool accept") {
		t.Errorf("the worker's accept: %+v; want invalid params %d", r, codeInvalidParams)
	}

	// The app's refusal reaches the agent as the tool's error text, unchanged.
	a := &fakeServe{answer: func(method string, c app.Call) (string, string) {
		switch method {
		case app.MethodJobNext:
			return `{"state":"job","job":"T-05/1/review","task":"T-05","attempt":1,"step":"review"}`, ""
		}
		return "", "accept: only the integrator (pj-int) gives verdicts, not pj-x (role integrator)"
	}}
	serveFake(t, app.SocketPath(f.root), a)
	i := start(t, f.env("OBOR_ROLE=integrator", "OBOR_AGENT=pj-x"), f.root)
	i.tool(1, "job_next", nil)
	if got := i.tool(2, "accept", map[string]any{"patch": "abc"}); !got.IsError ||
		got.Content[0].Text != "accept: only the integrator (pj-int) gives verdicts, not pj-x (role integrator)" {
		t.Errorf("the app's refusal: %+v; want it as the tool's error text", got)
	}
	// A requirement the tool itself checks: no patch, no request.
	before := len(a.requests())
	if got := i.tool(3, "accept", nil); !got.IsError || !strings.Contains(got.Content[0].Text, `"patch" argument is required`) {
		t.Errorf("accept without a patch: %+v; want the argument named", got)
	}
	if after := len(a.requests()); after != before {
		t.Errorf("accept without a patch reached the app (%d requests after %d)", after, before)
	}
}

// TestToolPreconditions: outside a project and without a lane there is nothing to ask about — the tool says so itself,
// the app is never bothered.
func TestToolPreconditions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := start(t, f.worker(), testkit.Dir(t))
	if got := c.tool(1, "job_next", nil); !got.IsError || !strings.Contains(got.Content[0].Text, "not in an obor project") {
		t.Errorf("outside a project: %+v; want a tool error naming it", got)
	}
	n := start(t, f.env("OBOR_ROLE=worker", "OBOR_AGENT=pj-o-01"), f.lane)
	if got := n.tool(1, "task_done", map[string]any{"report": "x"}); !got.IsError || !strings.Contains(got.Content[0].Text, "OBOR_LANE is not set") {
		t.Errorf("task_done without a lane: %+v; want a tool error naming OBOR_LANE", got)
	}
}

// TestCheck: the doctor line is the handshake's own output, per role.
func TestCheck(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, tc := range []struct {
		env  project.Env
		want string
	}{
		{f.worker(), "MCP OK (worker: job_next, task_done)"},
		{f.env("OBOR_ROLE=integrator"), "MCP OK (integrator: job_next, accept, sendback, reject, resolved, hold)"},
		{f.env("OBOR_ROLE=lead"), "MCP OK (lead: no tools)"},
		{f.env(), "MCP OK (-: no tools)"},
	} {
		got, err := Check(tc.env)
		if err != nil || got != tc.want {
			t.Errorf("Check: %q, %v; want %q", got, err, tc.want)
		}
	}
}

// TestNoRoleWarns: a server without OBOR_ROLE says so on stderr (a silent empty list cost a live run) and still serves
// the empty table.
func TestNoRoleWarns(t *testing.T) {
	t.Parallel()
	var errOut bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := Serve(context.Background(), Options{Env: func(string) (string, bool) { return "", false }, In: in, Out: &out, Err: &errOut}); err != nil {
		t.Fatal(err)
	}
	const want = "obor mcp: OBOR_ROLE is not set: no tools (the client did not pass the agent's environment)\n"
	if errOut.String() != want {
		t.Errorf("stderr %q; want %q", errOut.String(), want)
	}
	if !strings.Contains(out.String(), `"tools":[]`) {
		t.Errorf("tools/list: %s; want tools: []", out.String())
	}
}
