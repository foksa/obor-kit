// Package herdr is obor's herdr client: every herdr call obor makes goes through a *Socket, which sends one request
// over herdr's unix socket (protocol 22), decodes its JSON answer into typed results and turns every failure into an
// *Error that says what kind it is: herdr unavailable, target not found, or a bad answer.
//
// Newline-delimited JSON: {"id","method","params"} → {"id","result"} or {"id","error":{code,message}}; after
// events.subscribe the same connection also carries {"event","data"} lines. Every request opens its own short
// connection (a slow request such as agent.start or pane.wait_for_output blocks nothing else); a Stream holds the
// long-lived subscription connections.
package herdr

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/project"
)

var _ port.PaneControl = (*Socket)(nil)

// Kind is what went wrong in a herdr call.
type Kind int

const (
	Unavailable Kind = iota + 1 // no socket, herdr not running, timed out, killed
	NotFound                    // herdr answered: no such agent / pane
	BadAnswer                   // herdr answered something obor cannot read
	Failed                      // herdr answered with another error (its code in Error.Code)
)

func (k Kind) String() string {
	switch k {
	case Unavailable:
		return "herdr unavailable"
	case NotFound:
		return "not found"
	case BadAnswer:
		return "bad answer"
	}
	return "failed"
}

// Error is every error a *Socket returns.
type Error struct {
	Kind Kind
	Op   string // the request, e.g. "agent.get"
	Code string // herdr's error code, when it gave one (agent_not_found, agent_blocked, …)
	Msg  string
	// reached: the request was written to herdr's socket and no error answer came back (a write or read that failed,
	// an answer obor cannot read): herdr may have acted on it (Reached).
	reached bool
}

// Reached: a request that failed with err may still have been carried out by herdr. False only for herdr's own
// error answer and a failure before the request was sent (no socket, no connection).
func Reached(err error) bool {
	var e *Error
	return !errors.As(err, &e) || e.reached
}

// NotFound, Unavailable, Refreshed, PaneBusy, NotReady, Blocked, TimedOut and Answered let the core read an *Error
// without importing herdr (port.NotFound, port.NotRunning, port.Refreshed, port.PaneBusy, …): herdr's error codes stay
// in this package.
func (e *Error) NotFound() bool    { return e.Kind == NotFound }
func (e *Error) Unavailable() bool { return e.Kind == Unavailable }
func (e *Error) Refreshed() bool   { return e.Code == "refresh" }

// PaneBusy: herdr says the pane is not an available shell to start an agent in (agent_pane_busy, pane_not_available).
func (e *Error) PaneBusy() bool { return e.Code == "agent_pane_busy" || e.Code == "pane_not_available" }

// NotReady: herdr says the agent started but waits at a startup dialog (agent_not_ready).
func (e *Error) NotReady() bool { return e.Code == "agent_not_ready" }

// Blocked: herdr says the agent is blocked on a dialog (agent_blocked, blocked).
func (e *Error) Blocked() bool { return e.Code == "agent_blocked" || e.Code == "blocked" }

// TimedOut: herdr ended the wait at its own timeout (timeout).
func (e *Error) TimedOut() bool { return e.Code == "timeout" }

// Answered: herdr sent this error itself, its code with it (an unreadable answer or a lost connection has none).
func (e *Error) Answered() bool { return e.Code != "" }

// nameTaken: herdr refused a start name this pane ran the agent under before, or one another live agent holds
// (agent_name_taken).
func (e *Error) nameTaken() bool { return e.Code == "agent_name_taken" }

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("herdr %s: %s: %s (%s)", e.Op, e.Kind, e.Msg, e.Code)
	}
	return fmt.Sprintf("herdr %s: %s: %s", e.Op, e.Kind, e.Msg)
}

// Is matches an *Error by Kind: errors.Is(err, herdr.ErrNotFound).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Op == "" && t.Kind == e.Kind
}

// Sentinels for errors.Is.
var (
	ErrNotFound  = &Error{Kind: NotFound}
	ErrBadAnswer = &Error{Kind: BadAnswer}
)

// answerError is herdr's error object in an answer.
type answerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *answerError) toError(op string) *Error {
	kind := Failed
	if strings.HasSuffix(a.Code, "_not_found") {
		kind = NotFound
	}
	return &Error{Kind: kind, Op: op, Code: a.Code, Msg: a.Message}
}

// Protocol is the oldest herdr API protocol this client speaks; Dial refuses an older server and accepts a newer one
// (herdr's contract: clients ignore unknown fields, an unsupported method is an error). The conformance suite
// (tests/conformance, H-S1) guards the methods and params obor sends against a newer herdr's schema.
const Protocol = 22

// SocketPath is herdr's socket for env: $HERDR_SOCKET_PATH, else ~/.config/herdr/herdr.sock.
func SocketPath(env project.Env) string {
	if p, ok := env("HERDR_SOCKET_PATH"); ok && p != "" {
		return p
	}
	home, _ := env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

// Socket is a herdr client over the socket at Path. Make one with Dial (it checks the protocol).
type Socket struct {
	Path    string
	Timeout time.Duration // per request (default 10 s); slow requests take their own bound
	// Conn, when set, opens each connection in place of dialing Path: a herdr served in memory (herdrtest.ServeMem),
	// whose connections a testing/synctest bubble can wait on (a unix socket's it cannot). Production leaves it
	// nil.
	Conn func(ctx context.Context) (net.Conn, error)
}

// Dial is the socket client for path once herdr answers ping with this client's Protocol or a newer one.
func Dial(ctx context.Context, path string) (*Socket, error) {
	return (&Socket{Path: path}).checked(ctx)
}

// checked is s once herdr answers ping with this client's Protocol or a newer one.
func (s *Socket) checked(ctx context.Context) (*Socket, error) {
	if s.Conn == nil {
		if err := wire.CheckSocketPath(s.Path); err != nil {
			return nil, &Error{Kind: Unavailable, Op: "ping", Msg: err.Error()}
		}
	}
	var r struct {
		Version  string `json:"version"`
		Protocol int    `json:"protocol"`
	}
	if err := s.call(ctx, 0, &r, "ping", nil); err != nil {
		return nil, err
	}
	if err := checkProtocol("ping", r.Version, r.Protocol); err != nil {
		return nil, err
	}
	return s, nil
}

func checkProtocol(op, version string, protocol int) error {
	if protocol >= Protocol {
		return nil
	}
	return &Error{Kind: BadAnswer, Op: op, Code: "protocol",
		Msg: fmt.Sprintf("herdr %s speaks protocol %d, obor speaks %d: update obor or herdr so they match", version, protocol, Protocol)}
}

var reqSeq atomic.Int64

// request is one request line.
func request(method string, params any) []byte {
	if params == nil {
		params = struct{}{}
	}
	b, _ := json.Marshal(map[string]any{"id": "obor:" + strconv.FormatInt(reqSeq.Add(1), 10), "method": method, "params": params})
	return append(b, '\n')
}

// line is one line herdr sends: a response or an event.
type line struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *answerError    `json:"error"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
}

func (s *Socket) dial(ctx context.Context, op string) (net.Conn, error) {
	if s.Conn != nil {
		c, err := s.Conn(ctx)
		if err != nil {
			return nil, &Error{Kind: Unavailable, Op: op, Msg: err.Error()}
		}
		return c, nil
	}
	if err := wire.CheckSocketPath(s.Path); err != nil {
		return nil, &Error{Kind: Unavailable, Op: op, Msg: err.Error()}
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", s.Path)
	if err != nil {
		return nil, &Error{Kind: Unavailable, Op: op, Msg: err.Error()}
	}
	return c, nil
}

// call sends one request on its own connection, bounded by timeout (0: the client's), and decodes its result into
// result (nil: not read).
func (s *Socket) call(ctx context.Context, timeout time.Duration, result any, method string, params any) error {
	if timeout <= 0 {
		timeout = s.Timeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := s.dial(ctx, method)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if _, err := c.Write(request(method, params)); err != nil {
		return reachedError(connError(ctx, method, timeout, err))
	}
	b, err := bufio.NewReaderSize(c, 1<<16).ReadBytes('\n')
	if err != nil {
		return reachedError(connError(ctx, method, timeout, err))
	}
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return &Error{Kind: BadAnswer, Op: method, Msg: "unreadable answer: " + project.FirstLine(strings.TrimSpace(string(b))),
			reached: true}
	}
	if l.Error != nil {
		return l.Error.toError(method)
	}
	if result == nil {
		return nil
	}
	if len(l.Result) == 0 || string(l.Result) == "null" {
		return &Error{Kind: BadAnswer, Op: method, Msg: "no result in the answer", reached: true}
	}
	if err := json.Unmarshal(l.Result, result); err != nil {
		return &Error{Kind: BadAnswer, Op: method, Msg: err.Error(), reached: true}
	}
	return nil
}

// reachedError marks err (connError's) as a request herdr may have carried out (Reached).
func reachedError(err error) error {
	err.(*Error).reached = true
	return err
}

func connError(ctx context.Context, op string, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Kind: Unavailable, Op: op, Msg: fmt.Sprintf("timed out after %s", timeout)}
	}
	if ctx.Err() != nil {
		return &Error{Kind: Unavailable, Op: op, Msg: ctx.Err().Error()}
	}
	return &Error{Kind: Unavailable, Op: op, Msg: err.Error()}
}

// Snapshot is herdr's whole session at one moment (session.snapshot).
type Snapshot struct {
	Version    string
	Protocol   int
	Workspaces []Workspace
	Panes      []Pane
	Agents     []Pane
}

// Pane is a pane as herdr reports it: the core's Pane and herdr's own place of it, its workspace and tab ids. They
// stay in the adapter: the core sees the workspace's label as the pane's Group, setup never sees a tab.
type Pane struct {
	port.Pane
	Workspace, Tab string
}

// labelled is p as the core sees it, its Group the label of its workspace in labels (workspace id → label).
func (p Pane) labelled(labels map[string]string) port.Pane {
	out := p.Pane
	out.Group = labels[p.Workspace]
	return out
}

// corePanes is ps as PaneControl answers them: no Group (PaneControl's answers carry none).
func corePanes(ps []Pane) []port.Pane {
	out := make([]port.Pane, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Pane)
	}
	return out
}

// Workspace is one herdr workspace: its id and the label it shows.
type Workspace struct{ ID, Label string }

// labels is the snapshot's workspace labels by id.
func (s Snapshot) labels() map[string]string {
	out := map[string]string{}
	for _, w := range s.Workspaces {
		out[w.ID] = w.Label
	}
	return out
}

// Snapshot is session.snapshot; a server on an older protocol is an error.
func (s *Socket) Snapshot(ctx context.Context) (Snapshot, error) {
	var r struct {
		Snapshot *struct {
			Version    string `json:"version"`
			Protocol   int    `json:"protocol"`
			Workspaces []struct {
				ID    string  `json:"workspace_id"`
				Label *string `json:"label"`
			} `json:"workspaces"`
			Panes  []Wire `json:"panes"`
			Agents []Wire `json:"agents"`
		} `json:"snapshot"`
	}
	if err := s.call(ctx, 0, &r, "session.snapshot", nil); err != nil {
		return Snapshot{}, err
	}
	if r.Snapshot == nil {
		return Snapshot{}, &Error{Kind: BadAnswer, Op: "session.snapshot", Msg: "no snapshot in the answer"}
	}
	if err := checkProtocol("session.snapshot", r.Snapshot.Version, r.Snapshot.Protocol); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Version: r.Snapshot.Version, Protocol: r.Snapshot.Protocol}
	for _, w := range r.Snapshot.Workspaces {
		snap.Workspaces = append(snap.Workspaces, Workspace{ID: w.ID, Label: str(w.Label)})
	}
	for _, w := range r.Snapshot.Panes {
		snap.Panes = append(snap.Panes, w.pane())
	}
	for _, w := range r.Snapshot.Agents {
		snap.Agents = append(snap.Agents, w.pane())
	}
	return snap, nil
}

// Agents is agent.list.
func (s *Socket) Agents(ctx context.Context) ([]port.Pane, error) {
	var r struct {
		Agents []Wire `json:"agents"`
	}
	if err := s.call(ctx, 0, &r, "agent.list", nil); err != nil {
		return nil, err
	}
	out := make([]port.Pane, 0, len(r.Agents))
	for _, w := range r.Agents {
		out = append(out, w.pane().Pane)
	}
	return out, nil
}

// agentResult is the answer of agent.get, agent.prompt and agent.start: the agent as herdr sees it after the call.
func (s *Socket) agentResult(ctx context.Context, timeout time.Duration, method string, params any) (Pane, error) {
	var r struct {
		Agent *Wire `json:"agent"`
	}
	if err := s.call(ctx, timeout, &r, method, params); err != nil {
		return Pane{}, err
	}
	if r.Agent == nil {
		return Pane{}, &Error{Kind: BadAnswer, Op: method, Msg: "no agent in the answer", reached: true}
	}
	return r.Agent.pane(), nil
}

// Agent is agent.get (target: an agent name or the pane id hosting it).
func (s *Socket) Agent(ctx context.Context, target string) (port.Pane, error) {
	a, err := s.agentAt(ctx, target)
	return a.Pane, err
}

// agentAt is agent.get with herdr's own fields.
func (s *Socket) agentAt(ctx context.Context, target string) (Pane, error) {
	return s.agentResult(ctx, 0, "agent.get", map[string]any{"target": target})
}

// waitGrace is how much longer than a wait's own timeout the client waits for herdr's answer: herdr answers the
// timeout itself (Code "timeout"), and that answer must arrive before the connection's deadline.
const waitGrace = 5 * time.Second

// Wait is agent.wait: herdr answers once the agent at target (an agent name or the pane id hosting it) is in one of
// until (idle, done, …), with herdr's error code "timeout" after timeout, or agent_not_running when the agent left
// the pane first. The agent in the answer is not read.
func (s *Socket) Wait(ctx context.Context, target string, until []string, timeout time.Duration) error {
	p := map[string]any{"target": target, "until": until, "timeout_ms": timeout.Milliseconds()}
	return s.call(ctx, timeout+waitGrace, nil, "agent.wait", p)
}

// Pane is pane.get.
func (s *Socket) Pane(ctx context.Context, id string) (port.Pane, error) {
	p, err := s.paneAt(ctx, id)
	return p.Pane, err
}

// paneAt is pane.get with herdr's own fields.
func (s *Socket) paneAt(ctx context.Context, id string) (Pane, error) {
	var r struct {
		Pane *Wire `json:"pane"`
	}
	if err := s.call(ctx, 0, &r, "pane.get", map[string]any{"pane_id": id}); err != nil {
		return Pane{}, err
	}
	if r.Pane == nil {
		return Pane{}, &Error{Kind: BadAnswer, Op: "pane.get", Msg: "no pane in the answer"}
	}
	return r.Pane.pane(), nil
}

// Prompt is agent.prompt without a wait: the agent right after herdr typed the text. A blocked agent is refused before
// any input (*Error, Code agent_blocked); a working one queues it.
func (s *Socket) Prompt(ctx context.Context, target, text string) (port.Pane, error) {
	a, err := s.agentResult(ctx, 0, "agent.prompt", map[string]any{"target": target, "text": text})
	return a.Pane, err
}

// Wake is Prompt for a caller that wants no answer: the text typed into the agent in pane, refused when it is blocked.
func (s *Socket) Wake(ctx context.Context, pane, text string) error {
	_, err := s.Prompt(ctx, pane, text)
	return err
}

// AgentKeys is agent.send_keys: logical keys (enter, esc, ctrl+u, …) to the agent; herdr checks every key before it
// writes any.
func (s *Socket) AgentKeys(ctx context.Context, target string, keys ...string) error {
	return s.call(ctx, 0, nil, "agent.send_keys", map[string]any{"target": target, "keys": keys})
}

// SendKeys is pane.send_keys: keys (esc, a digit, …) to the pane, whatever runs there.
func (s *Socket) SendKeys(ctx context.Context, pane string, keys ...string) error {
	return s.call(ctx, 0, nil, "pane.send_keys", map[string]any{"pane_id": pane, "keys": keys})
}

// Type is pane.send_input: text plus Enter, submitted at once (a command line at a shell, /exit in an agent).
func (s *Socket) Type(ctx context.Context, pane, text string) error {
	return s.call(ctx, 0, nil, "pane.send_input", map[string]any{"pane_id": pane, "text": text, "keys": []string{"Enter"}})
}

// Screen is pane.read: the pane's visible text, its last lines, without ANSI codes.
func (s *Socket) Screen(ctx context.Context, pane string, lines int) (string, error) {
	var r struct {
		Read *struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	p := map[string]any{"pane_id": pane, "source": "visible", "lines": lines, "strip_ansi": true}
	if err := s.call(ctx, 0, &r, "pane.read", p); err != nil {
		return "", err
	}
	if r.Read == nil {
		return "", &Error{Kind: BadAnswer, Op: "pane.read", Msg: "no read in the answer"}
	}
	return r.Read.Text, nil
}

// WaitForOutput is pane.wait_for_output: the pane's recent unwrapped text, once a line of it matches regex (text
// already printed matches at once); herdr's error code "timeout" after timeout. Not "visible": that was stale in a
// shell after an agent exited, while "recent" had the line; unwrapped keeps a long typed line whole.
func (s *Socket) WaitForOutput(ctx context.Context, pane, regex string, timeout time.Duration) (string, error) {
	var r struct {
		Read *struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	p := map[string]any{"pane_id": pane, "source": "recent_unwrapped", "timeout_ms": timeout.Milliseconds(),
		"match": map[string]string{"type": "regex", "value": regex}}
	if err := s.call(ctx, timeout+waitGrace, &r, "pane.wait_for_output", p); err != nil {
		return "", err
	}
	if r.Read == nil {
		return "", &Error{Kind: BadAnswer, Op: "pane.wait_for_output", Msg: "no read in the answer", reached: true}
	}
	return r.Read.Text, nil
}

// RenamePane is pane.rename; label "" clears it. Labels are obor's agent identity (R-28 §2).
func (s *Socket) RenamePane(ctx context.Context, pane, label string) error {
	return s.call(ctx, 0, nil, "pane.rename", map[string]any{"pane_id": pane, "label": orNil(label)})
}

// RenameAgent is agent.rename; name "" clears it.
func (s *Socket) RenameAgent(ctx context.Context, target, name string) error {
	return s.call(ctx, 0, nil, "agent.rename", map[string]any{"target": target, "name": orNil(name)})
}

// orNil is s, nil (JSON null: herdr clears the field) when it is "".
func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Start is agent.start: name a new agent of kind in pane and type its command line (kind and args) at the pane's
// shell. herdr answers at once, before the agent runs (launch_pending, live): the answer is no proof the agent
// came up, the caller polls for it (launch.started). herdr lags in noticing an exit and may still hold the name from a
// pane that ran the agent before: a name it refuses as taken while no live agent elsewhere holds it is cleared and
// the start retried once. Sent once otherwise, never retried: a request that timed out may have been typed (Reached).
func (s *Socket) Start(ctx context.Context, name, kind, pane string, args ...string) (port.Pane, error) {
	a, err := s.start(ctx, name, kind, pane, args...)
	var e *Error
	if !errors.As(err, &e) || !e.nameTaken() {
		return a, err
	}
	holder := ""
	if h, herr := s.Agent(ctx, name); herr == nil {
		holder = h.ID
	}
	if holder != "" && holder != pane { // a live agent elsewhere holds it: the start stays refused
		return a, err
	}
	// herdr kept the name of this pane's own agent, or one no agent holds: clear it and retry.
	if holder == "" || s.RenameAgent(ctx, holder, "") != nil {
		_ = s.RenameAgent(ctx, name, "")
	}
	return s.start(ctx, name, kind, pane, args...)
}

// start is agent.start as sent.
func (s *Socket) start(ctx context.Context, name, kind, pane string, args ...string) (port.Pane, error) {
	p := map[string]any{"name": name, "kind": kind, "pane_id": pane}
	if len(args) > 0 {
		p["args"] = args
	}
	a, err := s.agentResult(ctx, 0, "agent.start", p)
	return a.Pane, err
}

// Panes is pane.list.
func (s *Socket) Panes(ctx context.Context) ([]port.Pane, error) {
	ps, err := s.panesAt(ctx)
	return corePanes(ps), err
}

// panesAt is pane.list with herdr's own fields.
func (s *Socket) panesAt(ctx context.Context) ([]Pane, error) {
	var r struct {
		Panes []Wire `json:"panes"`
	}
	if err := s.call(ctx, 0, &r, "pane.list", nil); err != nil {
		return nil, err
	}
	out := make([]Pane, 0, len(r.Panes))
	for _, w := range r.Panes {
		out = append(out, w.pane())
	}
	return out, nil
}

// Layout is pane.layout: the size of every pane in pane's tab.
func (s *Socket) Layout(ctx context.Context, pane string) ([]Rect, error) {
	var r layoutAnswer
	if err := s.call(ctx, 0, &r, "pane.layout", map[string]any{"pane_id": pane}); err != nil {
		return nil, err
	}
	return r.rects(), nil
}

// envMap is env's K=V entries as herdr's env object; nil when there are none.
func envMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

// Split is pane.split: a new pane beside pane (dir right or down) in cwd with env (K=V each), not focused; its id.
func (s *Socket) Split(ctx context.Context, pane, dir, cwd string, env ...string) (string, error) {
	p := map[string]any{"target_pane_id": pane, "direction": dir, "cwd": cwd}
	if m := envMap(env); m != nil {
		p["env"] = m
	}
	var r struct {
		Pane struct {
			ID string `json:"pane_id"`
		} `json:"pane"`
	}
	if err := s.call(ctx, 0, &r, "pane.split", p); err != nil {
		return "", err
	}
	if r.Pane.ID == "" {
		return "", &Error{Kind: BadAnswer, Op: "pane.split", Msg: "no pane id in the answer"}
	}
	return r.Pane.ID, nil
}

// NewTab is tab.create: a new tab in workspace ("": herdr's active one) labelled label, its root pane in cwd with env
// (K=V each), not focused; the root pane's id.
func (s *Socket) NewTab(ctx context.Context, workspace, label, cwd string, env ...string) (string, error) {
	p := map[string]any{"label": label, "cwd": cwd, "focus": false}
	if workspace != "" {
		p["workspace_id"] = workspace
	}
	if m := envMap(env); m != nil {
		p["env"] = m
	}
	var r struct {
		Root struct {
			ID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := s.call(ctx, 0, &r, "tab.create", p); err != nil {
		return "", err
	}
	if r.Root.ID == "" {
		return "", &Error{Kind: BadAnswer, Op: "tab.create", Msg: "no root pane id in the answer"}
	}
	return r.Root.ID, nil
}

// NewWorkspace is workspace.create: a new workspace labelled label, its first tab's root pane a shell in cwd, not
// focused; the workspace's id.
func (s *Socket) NewWorkspace(ctx context.Context, label, cwd string) (string, error) {
	var r struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := s.call(ctx, 0, &r, "workspace.create", map[string]any{"label": label, "cwd": cwd, "focus": false}); err != nil {
		return "", err
	}
	if r.Workspace.ID == "" {
		return "", &Error{Kind: BadAnswer, Op: "workspace.create", Msg: "no workspace id in the answer"}
	}
	return r.Workspace.ID, nil
}

// Close is pane.close.
func (s *Socket) Close(ctx context.Context, pane string) error {
	return s.call(ctx, 0, nil, "pane.close", map[string]any{"pane_id": pane})
}

// ProcessInfo is pane.process_info: the pane's shell and its foreground process group.
func (s *Socket) ProcessInfo(ctx context.Context, pane string) (ProcessInfo, error) {
	var r struct {
		Info *ProcessInfo `json:"process_info"`
	}
	if err := s.call(ctx, 0, &r, "pane.process_info", map[string]any{"pane_id": pane}); err != nil {
		return ProcessInfo{}, err
	}
	if r.Info == nil {
		return ProcessInfo{}, &Error{Kind: BadAnswer, Op: "pane.process_info", Msg: "no process_info in the answer"}
	}
	return *r.Info, nil
}

// Notify is notification.show: a desktop notification, best effort through herdr's foreground client. herdr's answer
// says whether it showed it; not shown, the reason is a port.NotShown error (busy, rate_limited, disabled,
// no_foreground_client): the toast's caller logs it and goes on.
func (s *Socket) Notify(ctx context.Context, title, body string) error {
	var r struct {
		Shown  *bool  `json:"shown"`
		Reason string `json:"reason"`
	}
	if err := s.call(ctx, 0, &r, "notification.show", map[string]any{"title": title, "body": body}); err != nil {
		return err
	}
	if r.Shown != nil && !*r.Shown {
		return &port.NotShown{Reason: r.Reason}
	}
	return nil
}

// Event kinds a Stream delivers.
const (
	EventStatus  = "pane.agent_status_changed" // the pane's agent, its status or its metadata changed: Now
	EventCreated = "pane.created"
	EventClosed  = "pane.closed"
	EventExited  = "pane.exited" // the pane's shell exited and herdr removed the pane (no pane.closed then)
)

// herdr events a Stream reads but delivers in another form: an update or a detected agent as an EventStatus; a moved
// pane or a new or renamed workspace ends the stream (Code refresh), so the caller works from a new Snapshot.
const (
	eventUpdated  = "pane.updated"
	eventDetected = "pane.agent_detected"
	eventMoved    = "pane.moved"
	eventSpace    = "workspace.created"
	eventRenamed  = "workspace.renamed"
)

// Event is one herdr event.
type Event struct {
	Kind   string // one of the Event* kinds
	Pane   string
	Status string // EventStatus: the new status (Now's)
	Agent  string // EventStatus: the agent kind (claude, codex…), "" when none (Now's)
	New    Pane   // EventCreated: the new pane, its agent fields as Now has them
	// Now is the pane as herdr reported it when it was read again after the event (EventStatus, EventCreated): its
	// agent entry (Name, Agent, Status, Cwd, Session, Workspace, Transcript), or the pane's fields when no agent runs.
	Now Pane
}

// eventName is herdr's event name in the dotted form: lifecycle events arrive as pane_created, status events as
// pane.agent_status_changed (the schema says pane_agent_status_changed).
func eventName(s string) string {
	if !strings.Contains(s, ".") {
		s = strings.Replace(s, "_", ".", 1)
	}
	return s
}

// Stream is a subscription to herdr's events: status changes of every pane (new panes included) and pane lifecycle.
// C closes when the stream ends: Err says why (ctx done, a closed connection, herdr's error
// line such as events_lost, or Code refresh); the caller then subscribes again and works from the new Snapshot.
//
// herdr's events carry no sequence, and arrive on several connections (some queued while the Snapshot was read), so
// a status payload may be older than the Snapshot or than the previous event. The stream therefore takes a pane's
// status, update and creation events as hints: it reads the pane again and delivers the event only when that differs
// from what the Snapshot or C last said about the pane. Every EventStatus and EventCreated thus holds state read
// after the Snapshot and after every earlier event on C; a closed or exited pane is delivered once, for a known pane.
type Stream struct {
	C <-chan Event
	// Snapshot was read after the status subscriptions were live: every later change arrives on C.
	Snapshot Snapshot

	s      *Socket
	ctx    context.Context
	cancel context.CancelFunc
	lines  chan subLine
	out    chan Event

	mu         sync.Mutex
	conns      []net.Conn
	subscribed map[string]bool // panes with a status subscription
	err        error

	seen map[string]Pane // pump's: each live pane as the Snapshot or C last said
}

type subLine struct {
	l   line
	err error
}

// Subscribe starts a Stream. Status events need one subscription per pane (herdr has no wildcard), so it subscribes
// pane lifecycle first, then the status of every pane in a snapshot, then reads the snapshot again and subscribes any
// pane it missed; a pane_created event subscribes that pane.
func (s *Socket) Subscribe(ctx context.Context) (*Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	st := &Stream{s: s, ctx: ctx, cancel: cancel, lines: make(chan subLine, 256), out: make(chan Event, 256),
		subscribed: map[string]bool{}}
	st.C = st.out
	fail := func(err error) (*Stream, error) {
		st.end(err)
		return nil, err
	}
	var lifecycle []map[string]any
	for _, typ := range []string{EventCreated, EventClosed, EventExited, eventUpdated, eventDetected, eventMoved, eventSpace, eventRenamed} {
		lifecycle = append(lifecycle, map[string]any{"type": typ})
	}
	if err := st.open(lifecycle); err != nil {
		return fail(err)
	}
	// Done once a snapshot has no pane without a live subscription. A pane closed between a snapshot and the
	// subscribe fails it (pane_not_found): the next snapshot no longer has it.
	for try := 1; ; try++ {
		snap, err := s.Snapshot(ctx)
		if err != nil {
			return fail(err)
		}
		missing := st.missing(snap.Panes)
		if len(missing) == 0 {
			st.Snapshot, st.seen = snap, observed(snap)
			break
		}
		if try == 5 {
			return fail(&Error{Kind: Unavailable, Op: "events.subscribe", Msg: "panes keep changing; no stable snapshot after 5 tries"})
		}
		if err := st.subscribePanes(missing); err != nil && !errors.Is(err, ErrNotFound) {
			return fail(err)
		}
	}
	go st.pump()
	return st, nil
}

// missing is the ids of panes without a status subscription.
func (st *Stream) missing(panes []Pane) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var ids []string
	for _, p := range panes {
		if !st.subscribed[p.ID] {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// subscribePanes subscribes the status of each pane id not subscribed yet, on one new connection.
func (st *Stream) subscribePanes(ids []string) error {
	var subs []map[string]any
	for _, id := range ids {
		subs = append(subs, map[string]any{"type": EventStatus, "pane_id": id})
	}
	if len(subs) == 0 {
		return nil
	}
	if err := st.open(subs); err != nil {
		return err
	}
	st.mu.Lock()
	for _, id := range ids {
		st.subscribed[id] = true
	}
	st.mu.Unlock()
	return nil
}

// open sends events.subscribe on a new connection and, once herdr confirms it, feeds its lines to the stream.
func (st *Stream) open(subs []map[string]any) error {
	const op = "events.subscribe"
	c, err := st.s.dial(st.ctx, op)
	if err != nil {
		return err
	}
	timeout := st.s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	r := bufio.NewReaderSize(c, 1<<20)
	confirm := func() error {
		if _, err := c.Write(request(op, map[string]any{"subscriptions": subs})); err != nil {
			return connError(st.ctx, op, timeout, err)
		}
		b, err := r.ReadBytes('\n')
		if err != nil {
			return connError(st.ctx, op, timeout, err)
		}
		var l line
		if err := json.Unmarshal(b, &l); err != nil {
			return &Error{Kind: BadAnswer, Op: op, Msg: "unreadable answer: " + project.FirstLine(strings.TrimSpace(string(b)))}
		}
		if l.Error != nil {
			return l.Error.toError(op)
		}
		return nil
	}
	if err := confirm(); err != nil {
		c.Close()
		return err
	}
	_ = c.SetDeadline(time.Time{})
	st.mu.Lock()
	if st.err != nil {
		st.mu.Unlock()
		c.Close()
		return st.err
	}
	st.conns = append(st.conns, c)
	st.mu.Unlock()
	go func() {
		for {
			b, err := r.ReadBytes('\n')
			var sl subLine
			if err != nil {
				sl.err = &Error{Kind: Unavailable, Op: op, Msg: "connection closed: " + err.Error()}
			} else if err := json.Unmarshal(b, &sl.l); err != nil {
				sl.err = &Error{Kind: BadAnswer, Op: op, Msg: "unreadable event: " + project.FirstLine(strings.TrimSpace(string(b)))}
			}
			select {
			case st.lines <- sl:
			case <-st.ctx.Done():
				return
			}
			if sl.err != nil {
				return
			}
		}
	}()
	return nil
}

// pump turns the connections' lines into Events until one ends the stream; it alone sends on and closes C.
func (st *Stream) pump() {
	defer close(st.out)
	for {
		var sl subLine
		select {
		case <-st.ctx.Done():
			st.end(&Error{Kind: Unavailable, Op: "events.subscribe", Msg: st.ctx.Err().Error()})
			return
		case sl = <-st.lines:
		}
		if sl.err != nil {
			st.end(sl.err)
			return
		}
		if sl.l.Error != nil { // e.g. events_lost: herdr dropped events for this subscriber
			st.end(sl.l.Error.toError("events.subscribe"))
			return
		}
		ev, hint, ok := st.event(sl)
		if !ok {
			continue
		}
		ev, ok, err := st.settle(ev, hint)
		if err != nil {
			st.end(err)
			return
		}
		if !ok {
			continue
		}
		select {
		case st.out <- ev:
		case <-st.ctx.Done():
		}
	}
}

// event decodes one event line, with what its payload says about the pane (hint; nil: nothing); false for a line
// that is no event obor uses.
func (st *Stream) event(sl subLine) (Event, *Pane, bool) {
	var d struct {
		Pane   string  `json:"pane_id"`
		Status string  `json:"agent_status"`
		Agent  *string `json:"agent"`
		New    *Wire   `json:"pane"`
	}
	if sl.l.Event == "" || json.Unmarshal(sl.l.Data, &d) != nil {
		return Event{}, nil, false
	}
	ev := Event{Kind: eventName(sl.l.Event), Pane: d.Pane}
	var hint *Pane
	switch ev.Kind {
	case EventStatus:
		hint = &Pane{Pane: port.Pane{ID: d.Pane, Status: d.Status, Agent: str(d.Agent)}}
	case EventCreated, eventUpdated:
		if d.New == nil || d.New.ID == "" {
			return Event{}, nil, false
		}
		// No hint for an update: a pane has no agent name, so an equal pane cannot prove the agent unchanged.
		ev.New, ev.Pane = d.New.pane(), d.New.ID
	case EventClosed, EventExited, eventDetected, eventMoved, eventSpace, eventRenamed:
	default:
		return Event{}, nil, false
	}
	return ev, hint, true
}

// settle is what C gets for ev, false for nothing (see Stream). hint is what ev's payload says about the pane: when
// it matches what C last said, nothing changed that obor watches, and the pane is not read again.
func (st *Stream) settle(ev Event, hint *Pane) (Event, bool, error) {
	last, known := st.seen[ev.Pane]
	switch ev.Kind {
	case EventCreated:
		if known { // in the Snapshot already: it, or an event since, is newer than this payload
			return ev, false, nil
		}
		// Subscribed first, then read: a change in between is in the read, a later one arrives as an event.
		if err := st.subscribePanes(st.missing([]Pane{ev.New})); err != nil {
			return ev, false, gone(err)
		}
		now, err := st.reread(ev.Pane)
		if err != nil {
			return ev, false, gone(err)
		}
		st.seen[ev.Pane] = now
		ev.New.Cwd, ev.New.Agent, ev.New.Status, ev.New.Session = now.Cwd, now.Agent, now.Status, now.Session
		ev.New.Workspace, ev.New.Transcript, ev.Now = now.Workspace, now.Transcript, now
		return ev, true, nil
	case EventStatus, eventUpdated, eventDetected:
		if !known { // closed, or not created yet as far as C said
			return ev, false, nil
		}
		if hint != nil && same(merge(*hint, last), last) {
			return ev, false, nil
		}
		now, err := st.reread(ev.Pane)
		if err != nil || same(now, last) { // a changed label too: Label is part of Agent
			return ev, false, gone(err)
		}
		st.seen[ev.Pane] = now
		return Event{Kind: EventStatus, Pane: ev.Pane, Status: now.Status, Agent: now.Agent, Now: now}, true, nil
	case EventClosed, EventExited:
		delete(st.seen, ev.Pane)
		return ev, known, nil
	case eventMoved, eventSpace, eventRenamed:
		return ev, false, &Error{Kind: Unavailable, Op: "events.subscribe", Code: "refresh",
			Msg: ev.Kind + ": subscribe again for a new snapshot"}
	}
	return ev, true, nil
}

// gone is err, nil when it says the pane is gone (its pane.closed or pane.exited follows).
func gone(err error) error {
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// merge is last with what a status event's hint says: its status and agent kind.
func merge(hint, last Pane) Pane {
	last.Status, last.Agent = hint.Status, hint.Agent
	return last
}

// reread is pane as herdr reports it now: its agent entry, or the pane when no agent runs there, with the pane's
// label (an agent entry has none; pane.rename emits no event, so a reread is where a relabel is seen).
func (st *Stream) reread(pane string) (Pane, error) {
	a, err := st.s.agentAt(st.ctx, pane)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return a, err
	}
	p, perr := st.s.paneAt(st.ctx, pane)
	if errors.Is(err, ErrNotFound) {
		return paneAgent(p), perr
	}
	if perr != nil {
		return a, perr
	}
	a.Label = p.Label
	return a, nil
}

// observed is each pane of snap as herdr reports it: its agent entry when one runs there (the pane's workspace and
// cwd where the entry has none), else the pane.
func observed(snap Snapshot) map[string]Pane {
	agents := map[string]Pane{}
	for _, a := range snap.Agents {
		agents[a.ID] = a
	}
	out := map[string]Pane{}
	for _, p := range snap.Panes {
		a, ok := agents[p.ID]
		if !ok {
			a = paneAgent(p)
		}
		a.Workspace, a.Cwd, a.Label = cmp.Or(a.Workspace, p.Workspace), cmp.Or(a.Cwd, p.Cwd), p.Label
		out[p.ID] = a
	}
	return out
}

func paneAgent(p Pane) Pane {
	return Pane{Pane: port.Pane{ID: p.ID, Status: p.Status, Cwd: p.Cwd, Agent: p.Agent, Session: p.Session,
		Transcript: p.Transcript, Label: p.Label, Launched: p.Launched}, Workspace: p.Workspace}
}

// same: a and b differ in nothing obor watches (herdr's state_change_seq is not read: it grows with every change, even
// one undone since).
func same(a, b Pane) bool { return a == b }

// end stops the stream once with err: closes every connection; pump then closes C.
func (st *Stream) end(err error) {
	st.mu.Lock()
	if st.err != nil {
		st.mu.Unlock()
		return
	}
	st.err = err
	conns := st.conns
	st.conns = nil
	st.mu.Unlock()
	st.cancel()
	for _, c := range conns {
		c.Close()
	}
}

// Err is why the stream ended (nil while it runs).
func (st *Stream) Err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.err
}

// Close ends the stream.
func (st *Stream) Close() {
	st.end(&Error{Kind: Unavailable, Op: "events.subscribe", Msg: "closed"})
}

// Process and ProcessInfo are pane.process_info: the shell and the processes of the pane's foreground process group.
type (
	Process     = port.Process
	ProcessInfo = port.ProcessInfo
)

// Wire is herdr's JSON for an agent or a pane (both carry the same agent fields): the one struct of that part of the
// protocol, so herdr's client decodes and herdrtest's fake fills the same fields, and a tag herdr changes changes both
// A null herdr sends for a cleared name, label, cwd, status or agent reads as "".
type Wire struct {
	Name         string        `json:"name"` // the agent's herdr name ("" when herdr keeps none)
	ID           string        `json:"pane_id"`
	Tab          string        `json:"tab_id"`      // pane layout lists the panes of a tab
	Terminal     string        `json:"terminal_id"` // "" when the pane has none, kept by a pane move
	Workspace    string        `json:"workspace_id"`
	Label        string        `json:"label"`
	Status       string        `json:"agent_status"`
	Cwd          string        `json:"cwd"`
	Kind         string        `json:"agent"`         // the agent kind (claude, codex…); "" for a pane with no agent
	AgentSession *AgentSession `json:"agent_session"` // nil: herdr reports no session for the agent
	Seq          int64         `json:"state_change_seq"`
	// LaunchPending: herdr accepted an agent.start in the pane and its agent is not up yet; InteractiveReady: the
	// launched agent takes input. herdr 0.9.5 leaves a false one out (live H-L12).
	LaunchPending    bool `json:"launch_pending,omitempty"`
	InteractiveReady bool `json:"interactive_ready,omitempty"`
}

// AgentSession is herdr's agent_session: the agent's session, Kind "id" with the id as Value or "path" with the
// transcript path; Agent is the kind herdr reports it for.
type AgentSession struct {
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	Value any    `json:"value"`
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (w Wire) session() string {
	if w.AgentSession == nil {
		return ""
	}
	switch v := w.AgentSession.Value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (w Wire) transcript() string {
	if w.AgentSession == nil || w.AgentSession.Kind != "path" {
		return ""
	}
	return w.session()
}

// pane is a herdr pane as the adapter reads it: herdr's pane and agent answers share this Wire.
func (w Wire) pane() Pane {
	return Pane{Pane: port.Pane{ID: w.ID, Terminal: w.Terminal, Cwd: trimSlash(w.Cwd), Label: w.Label, Agent: w.Kind,
		Name: w.Name, Status: w.Status, Transcript: w.transcript(), Session: w.session(),
		Launched: !w.LaunchPending || w.InteractiveReady},
		Workspace: w.Workspace, Tab: w.Tab}
}

func trimSlash(p string) string {
	if t := strings.TrimRight(p, "/"); t != "" {
		return t
	}
	return p
}

// Rect is a pane's size on screen in cells (pane.layout).
type Rect struct {
	Pane string
	W, H int
}

// layoutAnswer is herdr's answer to pane.layout.
type layoutAnswer struct {
	Layout struct {
		Panes []struct {
			ID   string `json:"pane_id"`
			Rect struct {
				W int `json:"width"`
				H int `json:"height"`
			} `json:"rect"`
		} `json:"panes"`
	} `json:"layout"`
}

func (r layoutAnswer) rects() []Rect {
	out := make([]Rect, 0, len(r.Layout.Panes))
	for _, p := range r.Layout.Panes {
		out = append(out, Rect{Pane: p.ID, W: p.Rect.W, H: p.Rect.H})
	}
	return out
}
