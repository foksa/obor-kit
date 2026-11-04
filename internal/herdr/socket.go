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

// NotFound, Unavailable and ErrorCode let the core read an *Error without importing herdr (port.NotFound,
// port.NotRunning, port.ErrorCode).
func (e *Error) NotFound() bool    { return e.Kind == NotFound }
func (e *Error) Unavailable() bool { return e.Kind == Unavailable }
func (e *Error) ErrorCode() string { return e.Code }

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

// Protocol is the herdr API protocol this client speaks; Dial refuses a server with another one.
const Protocol = 22

// MaxSocketPath is the longest unix socket path macOS accepts (sun_path); Linux allows 108.
const MaxSocketPath = 104

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

// CheckSocketPath fails when path is too long to be a unix socket address.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf("socket path is %d bytes, longer than the %d a unix socket allows: %s", len(path), MaxSocketPath, path)
	}
	return nil
}

// Socket is a herdr client over the socket at Path. Make one with Dial (it checks the protocol).
type Socket struct {
	Path    string
	Timeout time.Duration // per request (default 10 s); slow requests take their own bound
}

// Dial is the socket client for path once herdr answers ping with this client's Protocol.
func Dial(ctx context.Context, path string) (*Socket, error) {
	if err := CheckSocketPath(path); err != nil {
		return nil, &Error{Kind: Unavailable, Op: "ping", Msg: err.Error()}
	}
	s := &Socket{Path: path}
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
	if protocol == Protocol {
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
	if err := CheckSocketPath(s.Path); err != nil {
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
	Agents     []Agent
}

// VersionProtocol reads the herdr session snapshot's version and protocol without checking compatibility.
// Serve uses it to report the server it found before its protocol check stops dispatch.
func (s *Socket) VersionProtocol(ctx context.Context) (string, int, error) {
	var r struct {
		Snapshot *struct {
			Version  string `json:"version"`
			Protocol int    `json:"protocol"`
		} `json:"snapshot"`
	}
	if err := s.call(ctx, 0, &r, "session.snapshot", nil); err != nil {
		return "", 0, err
	}
	if r.Snapshot == nil {
		return "", 0, &Error{Kind: BadAnswer, Op: "session.snapshot", Msg: "no snapshot in the answer"}
	}
	return r.Snapshot.Version, r.Snapshot.Protocol, nil
}

// Workspace is one herdr workspace: its id and the label it shows.
type Workspace struct{ ID, Label string }

// Snapshot is session.snapshot; a server on another protocol is an error.
func (s *Socket) Snapshot(ctx context.Context) (Snapshot, error) {
	var r struct {
		Snapshot *struct {
			Version    string `json:"version"`
			Protocol   int    `json:"protocol"`
			Workspaces []struct {
				ID    string  `json:"workspace_id"`
				Label *string `json:"label"`
			} `json:"workspaces"`
			Panes  []wire `json:"panes"`
			Agents []wire `json:"agents"`
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
		snap.Agents = append(snap.Agents, w.agent())
	}
	return snap, nil
}

// Agents is agent.list.
func (s *Socket) Agents(ctx context.Context) ([]Agent, error) {
	var r struct {
		Agents []wire `json:"agents"`
	}
	if err := s.call(ctx, 0, &r, "agent.list", nil); err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(r.Agents))
	for _, w := range r.Agents {
		out = append(out, w.agent())
	}
	return out, nil
}

// agentResult is the answer of agent.get, agent.prompt and agent.start: the agent as herdr sees it after the call.
func (s *Socket) agentResult(ctx context.Context, timeout time.Duration, method string, params any) (Agent, error) {
	var r struct {
		Agent *wire `json:"agent"`
	}
	if err := s.call(ctx, timeout, &r, method, params); err != nil {
		return Agent{}, err
	}
	if r.Agent == nil {
		return Agent{}, &Error{Kind: BadAnswer, Op: method, Msg: "no agent in the answer", reached: true}
	}
	return r.Agent.agent(), nil
}

// Agent is agent.get (target: an agent name or the pane id hosting it).
func (s *Socket) Agent(ctx context.Context, target string) (Agent, error) {
	return s.agentResult(ctx, 0, "agent.get", map[string]any{"target": target})
}

// Pane is pane.get.
func (s *Socket) Pane(ctx context.Context, id string) (Pane, error) {
	var r struct {
		Pane *wire `json:"pane"`
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
func (s *Socket) Prompt(ctx context.Context, target, text string) (Agent, error) {
	return s.agentResult(ctx, 0, "agent.prompt", map[string]any{"target": target, "text": text})
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
// shell. herdr answers at once, before the agent runs (launch_pending, live T-544): the answer is no proof the agent
// came up, the caller polls for it (launch.started). Sent once, never retried: a request that timed out may have
// been typed (Reached).
func (s *Socket) Start(ctx context.Context, name, kind, pane string, args ...string) (Agent, error) {
	p := map[string]any{"name": name, "kind": kind, "pane_id": pane}
	if len(args) > 0 {
		p["args"] = args
	}
	return s.agentResult(ctx, 0, "agent.start", p)
}

// Panes is pane.list.
func (s *Socket) Panes(ctx context.Context) ([]Pane, error) {
	var r struct {
		Panes []wire `json:"panes"`
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
// focused; the workspace's id (T-632).
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

// Plugins is plugin.list. herdr's plugin registry is user-global: every session sees the same plugins.
func (s *Socket) Plugins(ctx context.Context) ([]Plugin, error) {
	var r pluginsAnswer
	if err := s.call(ctx, 0, &r, "plugin.list", nil); err != nil {
		return nil, err
	}
	return r.plugins("plugin.list")
}

// LinkPlugin is plugin.link (herdr keeps the path as resolved now).
func (s *Socket) LinkPlugin(ctx context.Context, dir string) error {
	return s.call(ctx, 0, nil, "plugin.link", map[string]any{"path": dir})
}

// UnlinkPlugin is plugin.unlink.
func (s *Socket) UnlinkPlugin(ctx context.Context, id string) error {
	return s.call(ctx, 0, nil, "plugin.unlink", map[string]any{"plugin_id": id})
}

// Close is pane.close.
func (s *Socket) Close(ctx context.Context, pane string) error {
	return s.call(ctx, 0, nil, "pane.close", map[string]any{"pane_id": pane})
}

// Match is a pattern in a pane's text: a substring, or a regex.
type Match struct {
	Value string
	Regex bool
}

func (m Match) param() map[string]string {
	if m.Regex {
		return map[string]string{"type": "regex", "value": m.Value}
	}
	return map[string]string{"type": "substring", "value": m.Value}
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

// Notify is notification.show: a desktop notification.
func (s *Socket) Notify(ctx context.Context, title, body string) error {
	return s.call(ctx, 0, nil, "notification.show", map[string]any{"title": title, "body": body})
}

// Event kinds a Stream delivers.
const (
	EventStatus  = "pane.agent_status_changed" // the pane's agent, its status or its metadata changed: Now
	EventOutput  = "pane.output_matched"
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
	Line   string // EventOutput: the matching line
	Text   string // EventOutput: the pane's text it matched in
	Watch  Watch  // EventOutput: the watch that matched
	New    Pane   // EventCreated: the new pane, its agent fields as Now has them
	// Now is the pane as herdr reported it when it was read again after the event (EventStatus, EventCreated): its
	// agent entry (Name, Kind, Status, Cwd, Session, Workspace, Transcript), or the pane's fields when no agent runs.
	Now Agent
}

// Watch asks for an EventOutput each time pane's text from Source starts to match Match. herdr samples the screen: it
// fires when a sample goes from no match to a match (text already there fires once at once), not per printed line.
type Watch struct {
	Pane   string
	Source string // herdr's read source: "visible", "recent" or "recent_unwrapped"
	Match  Match
}

// eventName is herdr's event name in the dotted form: lifecycle events arrive as pane_created, status events as
// pane.agent_status_changed (the schema says pane_agent_status_changed).
func eventName(s string) string {
	if !strings.Contains(s, ".") {
		s = strings.Replace(s, "_", ".", 1)
	}
	return s
}

// Stream is a subscription to herdr's events: status changes of every pane (new panes included), pane lifecycle and
// the output Watches. C closes when the stream ends: Err says why (ctx done, a closed connection, herdr's error
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

	seen map[string]Agent // pump's: each live pane as the Snapshot or C last said
}

type subLine struct {
	l     line
	watch *Watch // the output watch of the connection it came on
	err   error
}

// Subscribe starts a Stream. Status events need one subscription per pane (herdr has no wildcard), so it subscribes
// pane lifecycle first, then the status of every pane in a snapshot, then reads the snapshot again and subscribes any
// pane it missed; a pane_created event subscribes that pane.
func (s *Socket) Subscribe(ctx context.Context, watches ...Watch) (*Stream, error) {
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
	if err := st.open(lifecycle, nil); err != nil {
		return fail(err)
	}
	if len(watches) > 0 {
		if err := st.Watch(watches...); err != nil {
			return fail(err)
		}
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

// Watch adds output watches to the stream, each on its own connection (its events then name it).
func (st *Stream) Watch(watches ...Watch) error {
	for _, w := range watches {
		sub := map[string]any{"type": EventOutput, "pane_id": w.Pane, "source": w.Source, "match": w.Match.param()}
		if err := st.open([]map[string]any{sub}, &w); err != nil {
			return err
		}
	}
	return nil
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
	if err := st.open(subs, nil); err != nil {
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
func (st *Stream) open(subs []map[string]any, watch *Watch) error {
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
			sl.watch = watch
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
func (st *Stream) event(sl subLine) (Event, *Agent, bool) {
	var d struct {
		Pane   string  `json:"pane_id"`
		Status string  `json:"agent_status"`
		Agent  *string `json:"agent"`
		Line   string  `json:"matched_line"`
		Read   struct {
			Text string `json:"text"`
		} `json:"read"`
		New *wire `json:"pane"`
	}
	if sl.l.Event == "" || json.Unmarshal(sl.l.Data, &d) != nil {
		return Event{}, nil, false
	}
	ev := Event{Kind: eventName(sl.l.Event), Pane: d.Pane}
	var hint *Agent
	switch ev.Kind {
	case EventStatus:
		hint = &Agent{Pane: d.Pane, Status: d.Status, Kind: str(d.Agent)}
	case EventOutput:
		ev.Line, ev.Text = d.Line, d.Read.Text
		if sl.watch != nil {
			ev.Watch = *sl.watch
		}
	case EventCreated, eventUpdated:
		if d.New == nil || d.New.Pane == "" {
			return Event{}, nil, false
		}
		// No hint for an update: a pane has no agent name, so an equal pane cannot prove the agent unchanged.
		ev.New, ev.Pane = d.New.pane(), d.New.Pane
	case EventClosed, EventExited, eventDetected, eventMoved, eventSpace, eventRenamed:
	default:
		return Event{}, nil, false
	}
	return ev, hint, true
}

// settle is what C gets for ev, false for nothing (see Stream). hint is what ev's payload says about the pane: when
// it matches what C last said, nothing changed that obor watches, and the pane is not read again.
func (st *Stream) settle(ev Event, hint *Agent) (Event, bool, error) {
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
		ev.New.Cwd, ev.New.Kind, ev.New.Status, ev.New.Session = now.Cwd, now.Kind, now.Status, now.Session
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
		return Event{Kind: EventStatus, Pane: ev.Pane, Status: now.Status, Agent: now.Kind, Now: now}, true, nil
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
func merge(hint, last Agent) Agent {
	last.Status, last.Kind = hint.Status, hint.Kind
	return last
}

// reread is pane as herdr reports it now: its agent entry, or the pane when no agent runs there, with the pane's
// label (an agent entry has none; pane.rename emits no event, so a reread is where a relabel is seen).
func (st *Stream) reread(pane string) (Agent, error) {
	a, err := st.s.Agent(st.ctx, pane)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return a, err
	}
	p, perr := st.s.Pane(st.ctx, pane)
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
func observed(snap Snapshot) map[string]Agent {
	agents := map[string]Agent{}
	for _, a := range snap.Agents {
		agents[a.Pane] = a
	}
	out := map[string]Agent{}
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

func paneAgent(p Pane) Agent {
	return Agent{Pane: p.ID, Status: p.Status, Cwd: p.Cwd, Kind: p.Kind, Session: p.Session, Workspace: p.Workspace,
		Transcript: p.Transcript, Label: p.Label}
}

// same: a and b differ in nothing obor watches (Seq grows with every change, even one undone since).
func same(a, b Agent) bool {
	a.Seq, b.Seq = 0, 0
	return a == b
}

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

// Agent and Pane are what agent.list/agent.get and pane.list/pane.get answer: the core's types, so a *Socket is an
// port.PaneControl.
type (
	Agent = port.AgentInfo
	Pane  = port.PaneInfo
)

// Process and ProcessInfo are pane.process_info: the shell and the processes of the pane's foreground process group.
type (
	Process     = port.Process
	ProcessInfo = port.ProcessInfo
)

// wire is herdr's JSON for an agent or a pane (both carry the same agent fields).
type wire struct {
	Name    *string `json:"name"`
	Pane    string  `json:"pane_id"`
	Tab     string  `json:"tab_id"`
	Term    string  `json:"terminal_id"`
	Space   string  `json:"workspace_id"`
	Label   *string `json:"label"`
	Status  *string `json:"agent_status"`
	Cwd     *string `json:"cwd"`
	Kind    *string `json:"agent"`
	Session *struct {
		Kind  string `json:"kind"`
		Value any    `json:"value"`
	} `json:"agent_session"`
	Seq int64 `json:"state_change_seq"`
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (w wire) session() string {
	if w.Session == nil {
		return ""
	}
	switch v := w.Session.Value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (w wire) transcript() string {
	if w.Session == nil || w.Session.Kind != "path" {
		return ""
	}
	return w.session()
}

func (w wire) agent() Agent {
	return Agent{Name: str(w.Name), Pane: w.Pane, Status: str(w.Status), Cwd: trimSlash(str(w.Cwd)), Kind: str(w.Kind),
		Session: w.session(), Seq: w.Seq, Workspace: w.Space, Transcript: w.transcript()}
}

func (w wire) pane() Pane {
	return Pane{ID: w.Pane, Tab: w.Tab, Terminal: w.Term, Label: str(w.Label), Cwd: trimSlash(str(w.Cwd)), Kind: str(w.Kind), Status: str(w.Status),
		Session: w.session(), Workspace: w.Space, Transcript: w.transcript()}
}

func trimSlash(p string) string {
	if t := strings.TrimRight(p, "/"); t != "" {
		return t
	}
	return p
}

// Rect and Plugin are the core's types (pane.layout, plugin.list), so setup need not import herdr.
type (
	Rect   = port.Rect
	Plugin = port.Plugin
)

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

// pluginsAnswer is herdr's answer to plugin.list.
type pluginsAnswer struct {
	Plugins *[]struct {
		ID    string `json:"plugin_id"`
		OldID string `json:"id"` // an older herdr
		Root  string `json:"plugin_root"`
	} `json:"plugins"`
}

func (r pluginsAnswer) plugins(op string) ([]Plugin, error) {
	if r.Plugins == nil {
		return nil, &Error{Kind: BadAnswer, Op: op, Msg: "no plugins in the answer"}
	}
	out := make([]Plugin, 0, len(*r.Plugins))
	for _, p := range *r.Plugins {
		if p.ID == "" {
			p.ID = p.OldID
		}
		out = append(out, Plugin{ID: p.ID, Root: p.Root})
	}
	return out, nil
}
