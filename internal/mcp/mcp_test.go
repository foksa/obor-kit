package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"obor/internal/app/wire"
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
	f := &fixture{t: t, root: filepath.Join(base, "proj"), lane: filepath.Join(base, "proj", "lane-L-01"), home: t.TempDir()}
	for _, d := range []string{f.root + "/.git", f.lane, f.root + "/.obor/state"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.write(f.root+"/.obor/config.json", `{"version": 2, "project": {"agent_prefix": "pj"}, "lanes": {"count": 1}}`) // the callers' names derive from it (T-786)
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

// worker is the environment of the lane's worker agent, a stale OBOR_AGENT included (T-786: never read).
func (f *fixture) worker() project.Env {
	return f.env("OBOR_ROLE=worker", "OBOR_AGENT=pj-old-name", "OBOR_LANE=L-01", "OBOR_PANE=w1:p1")
}

// fakeServe is obor serve's app socket in a test: it records each request line and answers reply/refusal, like the
// app's own Listen.
type fakeServe struct {
	mu     sync.Mutex
	reqs   []wireRequest
	answer func(method string, c toolCall) (string, string)
}

type wireRequest struct {
	Method string   `json:"method"`
	Call   toolCall `json:"call"`
}

// toolCall is the call of any tool's request, every key the tools' request types send (wire.JobNextReq,
// wire.TaskDoneReq, wire.VerdictReq), for the fake to answer and the tests to read.
type toolCall struct {
	wire.Envelope
	Task   string `json:"task"`
	Job    string `json:"job,omitempty"`
	Patch  string `json:"patch,omitempty"`
	Note   string `json:"note,omitempty"`
	Lane   string `json:"lane,omitempty"`
	Pane   string `json:"pane,omitempty"`
	Report string `json:"report,omitempty"`
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

// client is an SDK client of a server run on pipes, as an agent's CLI starts it.
type client struct {
	t    *testing.T
	cs   *sdk.ClientSession
	done chan error
}

// answerWait bounds one answer through the pipes: a server that answers nothing fails the test there, not at go
// test's timeout. It outlasts a tool's wait for a serve that is not there (10 s, T-656).
const answerWait = 20 * time.Second

// start runs a server on pipes with env and cwd and connects a client (the initialize handshake).
func start(t *testing.T, env project.Env, cwd string) *client {
	return startCtx(t, context.Background(), env, cwd)
}

// startCtx is start with ctx, which its tool calls have: a short one ends a call to a serve that is not running at
// once, not at the deadline table's 15 s (T-685).
func startCtx(t *testing.T, ctx context.Context, env project.Env, cwd string) *client {
	t.Helper()
	return serveOn(t, ctx, Options{Env: env, Cwd: cwd, Err: io.Discard})
}

// serveOn runs Serve with o on pipes (its In and Out) and connects a client.
func serveOn(t *testing.T, ctx context.Context, o Options) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	o.In, o.Out = inR, outW
	done := make(chan error, 1)
	go func() {
		err := Serve(ctx, o)
		outW.Close()
		done <- err
	}()
	wait, cancel := context.WithTimeout(context.Background(), answerWait)
	defer cancel()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).
		Connect(wait, &sdk.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return &client{t: t, cs: cs, done: done}
}

// toolResult is a tool's answer: its one text, and whether it is an error.
type toolResult struct {
	Text    string
	IsError bool
}

// call calls the tool: its answer, or the JSON-RPC error (an unknown tool).
func (c *client) call(name string, args map[string]any) (toolResult, error) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), answerWait)
	defer cancel()
	p := &sdk.CallToolParams{Name: name}
	if args != nil {
		p.Arguments = args
	}
	res, err := c.cs.CallTool(ctx, p)
	if err != nil {
		return toolResult{}, err
	}
	if len(res.Content) != 1 {
		c.t.Fatalf("tools/call %s: content %+v; want one text", name, res.Content)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		c.t.Fatalf("tools/call %s: content %T; want text", name, res.Content[0])
	}
	return toolResult{Text: text.Text, IsError: res.IsError}, nil
}

// tool calls the tool and fails on a JSON-RPC error, so a test reads only the tool's own answer.
func (c *client) tool(name string, args map[string]any) toolResult {
	c.t.Helper()
	res, err := c.call(name, args)
	if err != nil {
		c.t.Fatalf("tools/call %s: %v", name, err)
	}
	return res
}

// stop ends the session (the server's stdin closes) and returns the server's error.
func (c *client) stop() error {
	c.cs.Close()
	select {
	case err := <-c.done:
		return err
	case <-time.After(answerWait):
		c.t.Fatalf("the server did not end at EOF within %s", answerWait)
		return nil
	}
}

// TestToolsListPerRole: tools/list is the role's table — the worker's two, the integrator's six (by name, the SDK's
// order), nothing at all for the lead, an unknown role or no role — each with its description and argument schema
// (open, as before the SDK: no additionalProperties, and properties even when there are none).
func TestToolsListPerRole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, tc := range []struct {
		env   []string
		names string
	}{
		{[]string{"OBOR_ROLE=worker"}, `["job_next","task_done"]`},
		{[]string{"OBOR_ROLE=integrator"}, `["accept","hold","job_next","reject","resolved","sendback"]`},
		{[]string{"OBOR_ROLE=lead"}, `[]`},
		{[]string{"OBOR_ROLE=deep_dive"}, `[]`},
		{[]string{"OBOR_ROLE=triage"}, `[]`}, // T-796: a role of its own, still no tools
		{nil, `[]`},
	} {
		c := start(t, f.env(tc.env...), f.root)
		ctx, cancel := context.WithTimeout(context.Background(), answerWait)
		list, err := c.cs.ListTools(ctx, nil)
		cancel()
		if err != nil {
			t.Fatalf("%v: tools/list: %v", tc.env, err)
		}
		names := make([]string, 0, len(list.Tools))
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
			var sc struct {
				Type       string                    `json:"type"`
				Properties map[string]map[string]any `json:"properties"`
				Required   []string                  `json:"required"`
				Additional any                       `json:"additionalProperties"`
			}
			b, _ := json.Marshal(tool.InputSchema)
			if err := json.Unmarshal(b, &sc); err != nil || tool.Description == "" || sc.Type != "object" ||
				sc.Properties == nil || sc.Additional != nil {
				t.Errorf("%v: tool %s, schema %s; want a description and an open object schema with properties",
					tc.env, tool.Name, b)
			}
			want := map[string]string{"job_next": `{"job":""}`, "task_done": `{"report":"report"}`,
				"accept": `{"note":"","patch":"patch"}`, "sendback": `{"why":"why"}`, "reject": `{"why":"why"}`,
				"hold": `{"why":"why"}`, "resolved": `{}`}[tool.Name]
			got := map[string]string{} // each property: required ones by name, optional ones ""
			for name, p := range sc.Properties {
				if p["type"] != "string" || p["description"] == "" {
					t.Errorf("%s: property %s %v; want a described string", tool.Name, name, p)
				}
				got[name] = ""
			}
			for _, name := range sc.Required {
				got[name] = name
			}
			if g, _ := json.Marshal(got); string(g) != want {
				t.Errorf("%s: properties %s; want %s (required ones by name)", tool.Name, g, want)
			}
		}
		if b, _ := json.Marshal(names); string(b) != tc.names {
			t.Errorf("%v: tools %s; want %s", tc.env, b, tc.names)
		}
	}
}

// TestWorkerTools: job_next renders the JobReply readably and task_done sends the lane's task and its report; each call carries the
// agent's identity (root, its derived agent name whatever a stale OBOR_AGENT says, role, lane, pane) and its own call id.
func TestWorkerTools(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := &fakeServe{answer: func(method string, c toolCall) (string, string) {
		switch method {
		case wire.MethodJobNext:
			return `{"state":"job","job":"T-01/1/work","task":"T-01","attempt":1,"step":"work","text":"do the work"}`, ""
		default:
			return "submitted", ""
		}
	}}
	serveFake(t, wire.SocketPath(f.root), a)
	c := start(t, f.worker(), f.lane)

	got := c.tool("job_next", nil)
	if got.IsError || got.Text != "JOB T-01/1/work (task T-01, attempt 1, step work)\n\ndo the work" {
		t.Errorf("job_next: %+v; want the job line and its text", got)
	}
	// No report is the SDK's schema refusal, an empty one obor's own; neither reaches the app.
	if got := c.tool("task_done", nil); !got.IsError || !strings.Contains(got.Text, "report") {
		t.Errorf("task_done without a report: %+v; want a tool error naming it", got)
	}
	if got := c.tool("task_done", map[string]any{"report": ""}); !got.IsError ||
		got.Text != `task_done: the "report" argument is required` {
		t.Errorf("task_done with an empty report: %+v; want the tool error naming it", got)
	}
	got = c.tool("task_done", map[string]any{"report": "Summary: x", "from": "a newer client"})
	if got.IsError || got.Text != "submitted" {
		t.Errorf("task_done (an argument it does not know ignored): %+v; want submitted", got)
	}
	reqs := a.requests()
	if len(reqs) != 2 {
		t.Fatalf("the app got %d requests; want 2: %+v", len(reqs), reqs)
	}
	// job_next names the caller's pane; task_done does not (serve reads the pane from its lane's watch).
	for i, call := range []toolCall{reqs[0].Call, reqs[1].Call} {
		pane := []string{"w1:p1", ""}[i]
		if call.Root != f.root || call.Agent != "pj-l-01" || call.Role != "worker" || call.Lane != "L-01" ||
			call.Pane != pane {
			t.Errorf("call %d: %+v; want root %s, the agent, worker, L-01, pane %q", i, call, f.root, pane)
		}
		if len(call.CallID) != 16 {
			t.Errorf("call %d: call id %q; want a fresh 16-hex id", i, call.CallID)
		}
	}
	if reqs[0].Method != wire.MethodJobNext || reqs[1].Method != wire.MethodTaskDone {
		t.Errorf("methods %s, %s; want %s, %s", reqs[0].Method, reqs[1].Method, wire.MethodJobNext, wire.MethodTaskDone)
	}
	if reqs[0].Call.CallID == reqs[1].Call.CallID {
		t.Errorf("two tool calls share the call id %s", reqs[0].Call.CallID)
	}
	if reqs[1].Call.Task != "" || reqs[1].Call.Report != "Summary: x" {
		t.Errorf("task_done's task %q, report %q; want no task (serve resolves the lane, T-439) and the report",
			reqs[1].Call.Task, reqs[1].Call.Report)
	}
	if err := c.stop(); err != nil {
		t.Errorf("the server at EOF: %v; want no error", err)
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
	a := &fakeServe{answer: func(string, toolCall) (string, string) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(cases) {
			return "", "unexpected request"
		}
		next++
		return cases[next-1].reply, ""
	}}
	serveFake(t, wire.SocketPath(f.root), a)
	c := start(t, f.worker(), f.lane)
	for _, tc := range cases {
		if got := c.tool("job_next", nil); got.IsError || got.Text != tc.want {
			t.Errorf("job_next %s: %+v; want %q", tc.reply, got, tc.want)
		}
	}
}

// TestIntegratorVerdicts: job_next first (the review job names the task), then each verdict tool is one app request
// with that task and job, the caller's identity, and its own arguments.
func TestIntegratorVerdicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := &fakeServe{answer: func(method string, c toolCall) (string, string) {
		if method == wire.MethodJobNext {
			return `{"state":"job","job":"T-05/1/review","task":"T-05","attempt":1,"step":"review","text":"review it"}`, ""
		}
		return strings.ToUpper(method) + " T-05", ""
	}}
	serveFake(t, wire.SocketPath(f.root), a)
	c := start(t, f.env("OBOR_ROLE=integrator", "OBOR_AGENT=pj-old-int", "OBOR_PANE=w1:p2"), f.root)
	if got := c.tool("job_next", nil); got.IsError || !strings.Contains(got.Text, "T-05/1/review") {
		t.Fatalf("job_next: %+v; want the review job", got)
	}
	for _, tc := range []struct {
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
		got := c.tool(tc.tool, tc.args)
		if got.IsError || got.Text != strings.ToUpper(tc.tool)+" T-05" {
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
		if got.Call.Agent != "pj-int" || got.Call.Role != "integrator" || got.Call.Lane != "" || got.Call.Pane != "" ||
			got.Call.Root != f.root {
			t.Errorf("%s: %+v; want the integrator's identity", tc.tool, got.Call)
		}
	}
}

// TestToolErrorsAreResults: serve being down, the app's refusal and a bad argument are tool error results (the
// server keeps serving), and a tool of another role is the JSON-RPC invalid-params error.
func TestToolErrorsAreResults(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The server's ctx is the tool calls': a short deadline ends the call to a serve that is not running soon, not at
	// the deadline table's 15 s (T-685). It counts from here, before the server starts, and the call retries until it
	// less askMargin (200 ms): 800 ms leaves the call 600 ms to begin under a loaded suite (at 200 ms it began past the
	// deadline once and answered "did not answer in 0s", T-872). The run ends with it: the other answers get their own.
	down, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	if got := startCtx(t, down, f.worker(), f.lane).tool("job_next", nil); !got.IsError ||
		!strings.Contains(got.Text, "obor serve is not running") {
		t.Errorf("serve down: %+v; want a tool error naming it", got)
	}
	_, err := start(t, f.worker(), f.lane).call("accept", map[string]any{"patch": "abc"})
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams || !strings.Contains(rpcErr.Message, `unknown tool "accept"`) {
		t.Errorf("the worker's accept: %v; want invalid params %d naming the tool", err, jsonrpc.CodeInvalidParams)
	}

	// The app's refusal reaches the agent as the tool's error text, unchanged.
	a := &fakeServe{answer: func(method string, c toolCall) (string, string) {
		switch method {
		case wire.MethodJobNext:
			return `{"state":"job","job":"T-05/1/review","task":"T-05","attempt":1,"step":"review"}`, ""
		}
		return "", "accept: only the integrator (pj-int) gives verdicts, not pj-x (role integrator)"
	}}
	serveFake(t, wire.SocketPath(f.root), a)
	i := start(t, f.env("OBOR_ROLE=integrator", "OBOR_AGENT=pj-x"), f.root)
	i.tool("job_next", nil)
	if got := i.tool("accept", map[string]any{"patch": "abc"}); !got.IsError ||
		got.Text != "accept: only the integrator (pj-int) gives verdicts, not pj-x (role integrator)" {
		t.Errorf("the app's refusal: %+v; want it as the tool's error text", got)
	}
	// A requirement the tool itself checks: no patch, no request.
	before := len(a.requests())
	if got := i.tool("accept", nil); !got.IsError || !strings.Contains(got.Text, "patch") {
		t.Errorf("accept without a patch: %+v; want the argument named", got)
	}
	if got := i.tool("accept", map[string]any{"patch": ""}); !got.IsError || got.Text != `accept: the "patch" argument is required` {
		t.Errorf("accept with an empty patch: %+v; want the argument named", got)
	}
	if got := i.tool("sendback", map[string]any{"why": 7}); !got.IsError || !strings.Contains(got.Text, "why") {
		t.Errorf("sendback with a number: %+v; want the argument named", got)
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
	if got := c.tool("job_next", nil); !got.IsError || !strings.Contains(got.Text, "not in an obor project") {
		t.Errorf("outside a project: %+v; want a tool error naming it", got)
	}
	n := start(t, f.env("OBOR_ROLE=worker"), f.lane)
	if got := n.tool("task_done", map[string]any{"report": "x"}); !got.IsError || !strings.Contains(got.Text, "OBOR_LANE is not set") {
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
		{f.env("OBOR_ROLE=integrator"), "MCP OK (integrator: accept, hold, job_next, reject, resolved, sendback)"},
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
	c := serveOn(t, context.Background(), Options{Env: func(string) (string, bool) { return "", false }, Err: &errOut})
	ctx, cancel := context.WithTimeout(context.Background(), answerWait)
	defer cancel()
	list, err := c.cs.ListTools(ctx, nil)
	if err != nil || list.Tools == nil || len(list.Tools) != 0 {
		t.Errorf("tools/list: %+v, %v; want an empty table", list, err)
	}
	if err := c.stop(); err != nil {
		t.Errorf("the server at EOF: %v; want no error", err)
	}
	const want = "obor mcp: OBOR_ROLE is not set: no tools (the client did not pass the agent's environment)\n"
	if errOut.String() != want {
		t.Errorf("stderr %q; want %q", errOut.String(), want)
	}
}

// worktree makes dir a linked worktree of the fixture's main checkout (the .git file and its gitdir's commondir are
// all project.Find reads), as `git worktree add` leaves a lane.
func (f *fixture) worktree(dir string) {
	f.t.Helper()
	gitDir := filepath.Join(f.root, ".git", "worktrees", filepath.Base(dir))
	for _, d := range []string{dir, gitDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	f.write(filepath.Join(dir, ".git"), "gitdir: "+gitDir+"\n")
	f.write(filepath.Join(gitDir, "commondir"), "../..\n")
}

// TestJobNextPerKind (T-791): the MCP server started as each kind starts it — its cwd and environment as the live
// processes have them (ps eww / lsof of the running `obor mcp`s, 2026-11-04) — names the lane's worker, so job_next
// returns the lane's job and the call carries the pane (OBOR_PANE, T-1037). No shape carries OBOR_AGENT (T-786 stopped
// exporting it).
func TestJobNextPerKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	lane := filepath.Join(filepath.Dir(f.root), "proj-wt", "L-01") // the default lanes_dir, ../<name>-wt
	f.worktree(lane)
	a := &fakeServe{answer: func(_ string, c toolCall) (string, string) {
		if c.Agent != "pj-l-01" {
			return "", "job_next: not your job: T-01/1/work is pj-l-01's in lane L-01, not " + c.Agent
		}
		return `{"state":"job","job":"T-01/1/work","task":"T-01","attempt":1,"step":"work"}`, ""
	}}
	serveFake(t, wire.SocketPath(f.root), a)
	pane := []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01", "OBOR_PANE=w1:p2"}
	for _, tc := range []struct {
		kind string
		env  project.Env
	}{
		// opencode: `opencode serve --stdio` (the TUI's child, in the lane) starts the server with the pane's whole
		// environment plus the registration's own (OBOR_NO_SELF_HOST=1).
		{"opencode", f.env(append(pane, "OBOR_NO_SELF_HOST=1", "OBOR_OPENCODE_MODEL=opencode-go/m", "PWD="+lane)...)},
		// claude: --mcp-config's server, in the lane, with the pane's environment.
		{"claude", f.env(append(pane, "OBOR_NO_SELF_HOST=1", "PWD="+lane)...)},
		// codex: a stripped environment, only the variables mcp_servers.obor.env_vars forwards (agents.EnvVars).
		{"codex", testkit.MapEnv(map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01",
			"OBOR_PANE": "w1:p2", "OBOR_STATE_HOME": f.home})},
	} {
		if _, ok := tc.env("OBOR_AGENT"); ok {
			t.Fatalf("%s: the shape exports OBOR_AGENT", tc.kind)
		}
		before := len(a.requests())
		got := start(t, tc.env, lane).tool("job_next", nil)
		if got.IsError || got.Text != "JOB T-01/1/work (task T-01, attempt 1, step work)" {
			t.Errorf("%s: job_next %+v; want the lane's job", tc.kind, got)
		}
		reqs := a.requests()
		if len(reqs) != before+1 {
			t.Fatalf("%s: the app got %d requests; want 1", tc.kind, len(reqs)-before)
		}
		if c := reqs[before].Call; c.Agent != "pj-l-01" || c.Role != "worker" || c.Lane != "L-01" || c.Pane != "w1:p2" ||
			c.Root != f.root {
			t.Errorf("%s: call %+v; want pj-l-01, worker, L-01, pane w1:p2 (OBOR_PANE), root %s", tc.kind, c, f.root)
		}
	}
}

// TestNoCallerIdentity (T-791): a worker's server with no lane (no OBOR_LANE, its cwd the main checkout) refuses the
// call itself, naming what was missing, and never asks serve with an empty name.
func TestNoCallerIdentity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := &fakeServe{answer: func(string, toolCall) (string, string) { return "", "unexpected request" }}
	serveFake(t, wire.SocketPath(f.root), a)
	got := start(t, f.env("OBOR_ROLE=worker", "OBOR_PANE=w1:p2"), f.root).tool("job_next", nil)
	want := "job_next: no caller identity: role worker without a lane: OBOR_LANE is not set and the cwd " + f.root +
		" is no lane worktree"
	if !got.IsError || got.Text != want {
		t.Errorf("job_next: %+v; want the error %q", got, want)
	}
	if n := len(a.requests()); n != 0 {
		t.Errorf("the app got %d requests; want none", n)
	}
	// A config that does not load is the missing input too.
	f.write(f.root+"/.obor/config.json", `{`)
	got = start(t, f.worker(), f.lane).tool("job_next", nil)
	if !got.IsError || !strings.HasPrefix(got.Text, "job_next: no caller identity: the config does not load: ") {
		t.Errorf("job_next with a broken config: %+v; want the refusal naming the config", got)
	}
}
