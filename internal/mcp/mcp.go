// Package mcp is obor's stdio MCP server: the job tools on the official Go SDK (github.com/modelcontextprotocol/go-sdk),
// which speaks the protocol and checks the arguments against each tool's schema. The tools are filtered by
// OBOR_ROLE, and each tool call is one request on obor serve's app socket (a wire.Verdict Call: the only path
// of the integrator's verdicts): the app decides, the server only renders its reply. `obor mcp` runs it; `obor mcp
// --check` checks it.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/version"
)

// Options is one server run: the environment the agent's session exports (launch.EnvVars: a client with a stripped
// environment must forward them), the cwd the server was started in (each call's Root is that cwd's project), and the
// streams. Err gets the one startup warning (nil: os.Stderr).
type Options struct {
	Env project.Env
	Cwd string
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Serve runs the server on o.In/o.Out, one JSON-RPC message per line, until the input ends.
func Serve(ctx context.Context, o Options) error {
	s := &server{ctx: ctx, env: o.Env, cwd: o.Cwd}
	if s.role() == "" {
		w := o.Err
		if w == nil {
			w = os.Stderr
		}
		fmt.Fprintln(w, "obor mcp: OBOR_ROLE is not set: no tools (the client did not pass the agent's environment)")
	}
	err := s.sdkServer().Run(ctx, &sdk.IOTransport{Reader: io.NopCloser(o.In), Writer: nopWriteCloser{o.Out}})
	if errors.Is(err, io.EOF) {
		return nil // the client closed stdin: the session's normal end
	}
	return err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// server is one run: the context of its tool calls (the SDK detaches a handler's context from the run's, and a call's
// deadline is the run's), the environment, the cwd, and the last job_next reply. The integrator's verdict tools
// key their call on that job's task and job id (the app's verdicts name a task; the role text says to call job_next
// first, and job_next's reply is the only place the server learns them). The SDK runs calls concurrently: mu guards
// task and job.
type server struct {
	ctx  context.Context
	env  project.Env
	cwd  string
	mu   sync.Mutex
	task string
	job  string
}

// sdkServer is the SDK server with the role's tools; the tools capability is announced even when the role has none.
func (s *server) sdkServer() *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "obor", Version: version.Version},
		&sdk.ServerOptions{Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}}})
	s.register(srv)
	return srv
}

// role is the session's role: OBOR_ROLE.
func (s *server) role() string { return s.env.Get("OBOR_ROLE") }

// fetched is the task and job id of the last job_next reply.
func (s *server) fetched() (task, job string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task, s.job
}

// self is the caller's agent name: project.Self of the server's cwd in repo r under the current config, read at each
// call (derived, never OBOR_AGENT, so a rename applies at once). No name: missing says which input lacked it
// (the refusal names it, not serve's "not -").
func (s *server) self(r *project.Repo) (name, missing string) {
	cfg, _, err := config.LoadHook(r.Root)
	if err != nil {
		return "", "the config does not load: " + err.Error()
	}
	role := project.DetectRole(r, cfg, s.env)
	if name := project.SelfAgent(cfg, role); name != "" {
		return name, ""
	}
	if roles.LaneBound(role.Role) && role.Lane == "" {
		return "", fmt.Sprintf("role %s without a lane: OBOR_LANE is not set and the cwd %s is no lane worktree",
			role.Role, s.cwd)
	}
	return "", fmt.Sprintf("role %s (lane %q) names no agent in the config", role.Role, role.Lane)
}

// call is one tool call, the tool's name its app socket method: request builds the method's request from the
// arguments, then the caller's identity is derived and the app asked. Every refusal — a bad argument, no project, no
// identity, the app's own, serve being down — is the error, the tool's error text; never a broken server.
func (s *server) call(method string, request func() (wire.Req, error)) (string, error) {
	r, err := request()
	if err != nil {
		return "", fmt.Errorf("%s: %w", method, err)
	}
	root, err := project.Find(s.cwd)
	if err != nil {
		return "", errors.New(method + ": not in an obor project (no .obor/config.json)")
	}
	agent, missing := s.self(root)
	if agent == "" {
		return "", errors.New(method + ": no caller identity: " + missing)
	}
	if d, ok := r.(*wire.TaskDoneReq); ok && d.Lane == "" {
		return "", errors.New(method + ": OBOR_LANE is not set: task_done is the worker's tool")
	}
	*r.Env() = wire.Envelope{Root: root.Root, Agent: agent, Role: s.role(), CallID: wire.NewCallID()}
	reply, err := wire.Verdict(s.ctx, wire.SocketPath(root.Root), r)
	if err != nil {
		return "", err
	}
	if method != wire.MethodJobNext {
		return reply, nil
	}
	text, task, job, err := renderJob(reply)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.task, s.job = task, job
	s.mu.Unlock()
	return text, nil
}

// renderJob is job_next's readable text and, for a job, its task and id — the verdict tools' call names them. The two
// other states get their own one line; an answer that is no job reply is an error, never a line.
func renderJob(reply string) (text, task, job string, err error) {
	var r wire.JobReply
	if err := json.Unmarshal([]byte(reply), &r); err != nil || r.State == "" {
		return "", "", "", fmt.Errorf("job_next: the answer is not a job reply: %s", reply)
	}
	switch r.State {
	case "job":
		head := fmt.Sprintf("JOB %s (task %s, attempt %d, step %s)", r.Job, r.Task, r.Attempt, r.Step)
		if strings.TrimSpace(r.Text) == "" {
			return head, r.Task, r.Job, nil
		}
		return head + "\n\n" + r.Text, r.Task, r.Job, nil
	case "superseded":
		return fmt.Sprintf("SUPERSEDED BY %s (call job_next again)", r.Current), "", "", nil
	default: // "none", and a state a later app may add: its own line
		return "NO JOB (" + r.State + ")", "", "", nil
	}
}
