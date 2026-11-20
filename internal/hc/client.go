// Package hc is obor's client of herdr-core (`hc`), the second terminal adapter (docs/plan/herdr-core.md): JSON-RPC 2.0
// over the core's unix socket, one message per line, each request on its own connection after `hello {generation: 1}`.
// It implements port.Panes (Panes: the snapshot, the event stream, Notify), port.PaneControl (Client: its reads here,
// the typing and the wait in control.go) and port.AgentStarter (start.go). A terminal is a pane: its address (`t7`) is
// the pane's id, its labels obor.project and obor.agent are the pane's group and label. No project's terminal is hc
// yet.
//
// Every failure is an *Error: hc's own answer carries its data.kind (not_found → port.NotFound); a core that does not
// answer (no socket, a refused dial, a connection that went) is port.NotRunning; hc's `unavailable` (a terminal in
// recovery) is neither.
package hc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/project"
	"obor/internal/version"
)

// Generation is the protocol generation obor speaks; MinVersion the oldest hc it accepts (a newer one is fine: hc's
// API only grows).
const (
	Generation = 1
	MinVersion = "0.1.21"
)

// The labels obor reads on a terminal: the project it belongs to (the pane's Group), obor's agent name (its Label) and
// the kind obor's last start ran there (the pane's Agent when the running agent's status names no kind).
const (
	LabelProject = "obor.project"
	LabelAgent   = "obor.agent"
	LabelKind    = "obor.kind"
)

// Error is every error the client returns.
type Error struct {
	Op   string // the method, e.g. "terminal.get"
	Kind string // hc's data.kind (not_found, unavailable, blocked, …); "" when hc sent no error answer
	Msg  string
	// down: no core answered: the dial failed, or the connection or the stream went (port.NotRunning).
	down bool
	// refresh: the stream ended only so the caller reads a new snapshot and subscribes again (port.Refreshed).
	refresh bool
	// sent: the request went out whole before its answer failed to come: hc may have acted on it (a send typed).
	sent bool
}

// NotFound, Unavailable, Refreshed and Answered let the core read an *Error without importing hc (port.NotFound,
// port.NotRunning, port.Refreshed, port.Answered). A finished terminal is not found: its program ended for good (a kept
// terminal whose shell exited is never restarted), so the pane is gone.
func (e *Error) NotFound() bool    { return e.Kind == "not_found" || e.Kind == "finished" }
func (e *Error) Unavailable() bool { return e.down }
func (e *Error) Refreshed() bool   { return e.refresh }
func (e *Error) Answered() bool    { return e.Kind != "" }

func (e *Error) Error() string {
	if e.Kind != "" {
		return fmt.Sprintf("hc %s: %s (%s)", e.Op, e.Msg, e.Kind)
	}
	return fmt.Sprintf("hc %s: %s", e.Op, e.Msg)
}

// EnvNames are the environment variables that pick a caller's core, its socket (SocketPath), state and config: a serve
// launchd starts gets the ones the caller has (cmd/obor/terminal.go), so it reaches and starts the same session.
var EnvNames = []string{"HC_SOCKET", "HC_SESSION", "XDG_RUNTIME_DIR", "XDG_STATE_HOME", "XDG_CONFIG_HOME"}

// SocketPath is the core's socket for env, as hc finds it: $HC_SOCKET, else $XDG_RUNTIME_DIR/herdr-core/<session>.sock,
// else <state>/herdr-core/<session>/<session>.sock with <state> $XDG_STATE_HOME or ~/.local/state; the session is
// $HC_SESSION, else default.
func SocketPath(env project.Env) string {
	if p := env.Get("HC_SOCKET"); p != "" {
		return p
	}
	session := env.Get("HC_SESSION")
	if session == "" {
		session = "default"
	}
	if run := env.Get("XDG_RUNTIME_DIR"); run != "" {
		return filepath.Join(run, "herdr-core", session+".sock")
	}
	state := env.Get("XDG_STATE_HOME")
	if state == "" {
		home := env.Get("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "herdr-core", session, session+".sock")
}

// New is the client of env's core (SocketPath), laying terminals out in the herdr-app window on PATH: the one cmd/obor's
// terminal selector builds.
func New(env project.Env) *Client { return &Client{Path: SocketPath(env), App: &herdrapp.Client{}} }

// Client is an hc client over the core's socket at Path.
type Client struct {
	Path    string
	Timeout time.Duration // per request, its hello included (0: 10 s)
	// Conn, when set, opens each connection in place of dialing Path: a core served in memory (hctest.Core), whose
	// connections a testing/synctest bubble can wait on. Production leaves it nil.
	Conn func(ctx context.Context) (net.Conn, error)
}

func (cl *Client) timeout() time.Duration {
	if cl.Timeout > 0 {
		return cl.Timeout
	}
	return 10 * time.Second
}

// hello is hello's answer, as far as obor reads it.
type hello struct {
	Generation int      `json:"generation"`
	Version    string   `json:"version"`
	Methods    []string `json:"methods"`
}

// session is one connection past its hello.
type session struct {
	c     net.Conn
	r     *bufio.Reader
	id    int
	hello hello
}

func (cl *Client) dial(ctx context.Context, op string) (net.Conn, error) {
	var c net.Conn
	var err error
	if cl.Conn != nil {
		c, err = cl.Conn(ctx)
	} else if err = project.CheckSocketPath(cl.Path); err == nil {
		var d net.Dialer
		c, err = d.DialContext(ctx, "unix", cl.Path)
	}
	if err != nil {
		return nil, &Error{Op: op, Msg: "no hc core answers: " + err.Error(), down: true}
	}
	return c, nil
}

// open dials the core and says hello: a session once hc speaks generation 1 and is MinVersion or newer. ctx bounds
// the connection's whole life (it is closed when ctx ends) and, by its deadline (none: the client's timeout), the
// hello's and the caller's requests.
func (cl *Client) open(ctx context.Context, op string) (*session, error) {
	c, err := cl.dial(ctx, op)
	if err != nil {
		return nil, err
	}
	context.AfterFunc(ctx, func() { c.Close() })
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(cl.timeout())
	}
	_ = c.SetDeadline(deadline) // a stream clears it
	s := &session{c: c, r: bufio.NewReaderSize(c, 1<<20)}
	if err := s.call(ctx, "hello", map[string]any{"generation": Generation}, &s.hello); err != nil {
		c.Close()
		return nil, err
	}
	if err := checkHello(s.hello); err != nil {
		c.Close()
		return nil, err
	}
	return s, nil
}

// do runs fn on a new session, the whole of it bounded by the client's timeout.
func (cl *Client) do(ctx context.Context, op string, fn func(ctx context.Context, s *session) error) error {
	return cl.doFor(ctx, op, cl.timeout(), fn)
}

// doFor runs fn on a new session, the whole of it bounded by d.
func (cl *Client) doFor(ctx context.Context, op string, d time.Duration, fn func(ctx context.Context, s *session) error) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	s, err := cl.open(ctx, op)
	if err != nil {
		return err
	}
	defer s.c.Close()
	return fn(ctx, s)
}

// call sends one request on the session and decodes its result into result (nil: not read).
func (cl *Client) call(ctx context.Context, result any, method string, params any) error {
	return cl.do(ctx, method, func(ctx context.Context, s *session) error { return s.call(ctx, method, params, result) })
}

// checkHello refuses a core obor does not speak to: another generation, or a version older than MinVersion.
func checkHello(h hello) error {
	if h.Generation != Generation {
		return &Error{Op: "hello", Msg: fmt.Sprintf("hc %s speaks generation %d, obor speaks %d", h.Version, h.Generation, Generation)}
	}
	older, ok := version.Older(h.Version, MinVersion)
	if !ok {
		return &Error{Op: "hello", Msg: fmt.Sprintf("hc reports version %q, which obor cannot read", h.Version)}
	}
	if older {
		return &Error{Op: "hello", Msg: fmt.Sprintf("hc %s is older than %s, the oldest obor speaks: run `hc update`", h.Version, MinVersion)}
	}
	return nil
}

// note is the snapshot's sentence when hc lacks a method obor sends ("" when it has every one): obor status shows it.
func note(h hello) string {
	var missing []string
	for m := range Sent {
		if !slices.Contains(h.Methods, m) {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	slices.Sort(missing)
	return fmt.Sprintf("hc %s lacks %s: obor's calls to them fail", h.Version, strings.Join(missing, ", "))
}

// line is one line hc sends: an answer (id, result or error) or a notification (method, params).
type line struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Kind string `json:"kind"`
		} `json:"data"`
	} `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// call sends one request and reads lines up to its answer (a notification before it is skipped).
func (s *session) call(ctx context.Context, method string, params, result any) error {
	if params == nil {
		params = struct{}{}
	}
	s.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params})
	if _, err := s.c.Write(append(b, '\n')); err != nil {
		return connError(ctx, method, err)
	}
	for {
		b, err := s.r.ReadBytes('\n')
		if err != nil {
			e := connError(ctx, method, err)
			e.sent = true
			return e
		}
		var l line
		if err := json.Unmarshal(b, &l); err != nil {
			return &Error{Op: method, Msg: "unreadable answer: " + project.FirstLine(strings.TrimSpace(string(b)))}
		}
		if l.ID == nil || *l.ID != s.id {
			continue
		}
		if l.Error != nil {
			kind := l.Error.Data.Kind
			if kind == "" {
				kind = "code " + strconv.Itoa(l.Error.Code)
			}
			return &Error{Op: method, Kind: kind, Msg: l.Error.Message}
		}
		if result == nil {
			return nil
		}
		if len(l.Result) == 0 || string(l.Result) == "null" {
			return &Error{Op: method, Msg: "no result in the answer"}
		}
		if err := json.Unmarshal(l.Result, result); err != nil {
			return &Error{Op: method, Msg: "unreadable result: " + err.Error()}
		}
		return nil
	}
}

// connError is a connection's failure: the core went, or ctx ended (its deadline: timed out).
func connError(ctx context.Context, op string, err error) *Error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return &Error{Op: op, Msg: "timed out", down: true}
	case ctx.Err() != nil:
		return &Error{Op: op, Msg: ctx.Err().Error(), down: true}
	}
	return &Error{Op: op, Msg: "connection closed: " + err.Error(), down: true}
}

var (
	_ port.PaneControl  = (*Client)(nil)
	_ port.AgentStarter = (*Client)(nil)
)

// Pane is terminal.get: the terminal as a pane, no Group (PaneControl's answers carry none). A finished terminal is
// no pane: NotFound.
func (cl *Client) Pane(ctx context.Context, pane string) (port.Pane, error) {
	r, err := cl.live(ctx, pane)
	if err != nil {
		return port.Pane{}, err
	}
	p := r.pane()
	p.Group = ""
	return p, nil
}

// live is terminal.get of a live terminal; a finished one is NotFound.
func (cl *Client) live(ctx context.Context, pane string) (Record, error) {
	var r Record
	if err := cl.call(ctx, &r, "terminal.get", map[string]any{"terminal": pane}); err != nil {
		return Record{}, err
	}
	if r.State != "live" {
		return Record{}, &Error{Op: "terminal.get", Kind: "not_found", Msg: fmt.Sprintf("%s has finished (%s)", pane, r.Reason)}
	}
	return r, nil
}

// Panes is terminal.list: every live terminal as a pane, no Group.
func (cl *Client) Panes(ctx context.Context) ([]port.Pane, error) {
	var r struct {
		Terminals []Record `json:"terminals"`
	}
	if err := cl.call(ctx, &r, "terminal.list", nil); err != nil {
		return nil, err
	}
	out := []port.Pane{}
	for _, rec := range r.Terminals {
		if rec.State == "live" {
			p := rec.pane()
			p.Group = ""
			out = append(out, p)
		}
	}
	return out, nil
}

// Screen is terminal.read of the screen (no history above it): its last lines (lines ≤ 0: all of it).
func (cl *Client) Screen(ctx context.Context, pane string, lines int) (string, error) {
	return cl.read(ctx, pane, lines)
}

// Recent is the same read as Screen: hc's terminal.read gives one text, the screen as the program drew it, with no
// stale view of it to tell apart from the output.
func (cl *Client) Recent(ctx context.Context, pane string, lines int) (string, error) {
	return cl.read(ctx, pane, lines)
}

func (cl *Client) read(ctx context.Context, pane string, lines int) (string, error) {
	var r struct {
		Text *string `json:"text"`
	}
	if err := cl.call(ctx, &r, "terminal.read", map[string]any{"terminal": pane, "lines": 0}); err != nil {
		return "", err
	}
	if r.Text == nil {
		return "", &Error{Op: "terminal.read", Msg: "no text in the answer"}
	}
	return lastLines(*r.Text, lines), nil
}

// lastLines is text's last n lines (n ≤ 0: text).
func lastLines(text string, n int) string {
	if n <= 0 {
		return text
	}
	ls := strings.Split(text, "\n")
	if len(ls) <= n {
		return text
	}
	return strings.Join(ls[len(ls)-n:], "\n")
}

// ProcessInfo is the terminal's shell and its foreground (terminal.get's pid and foreground): hc names only the
// foreground group's leader, so Processes holds that one process. An agent's tools run outside the terminal's
// foreground group and never show; whether the agent works is its status (port.Pane.Busy), not this.
func (cl *Client) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	r, err := cl.live(ctx, pane)
	if err != nil {
		return port.ProcessInfo{}, err
	}
	info := port.ProcessInfo{Pane: r.Address, ShellPID: r.PID}
	if f := r.Foreground; f != nil {
		info.FgPGID = f.PID
		pr := port.Process{PID: f.PID, Name: f.Name, Argv: f.Argv, Cmdline: strings.Join(f.Argv, " "), Cwd: f.Cwd}
		if len(f.Argv) > 0 {
			pr.Argv0 = f.Argv[0]
		}
		info.Processes = []port.Process{pr}
	}
	return info, nil
}
