package herdrtest

import (
	"bufio"
	"encoding/json"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/herdr"
)

// Server is a fake herdr socket: newline JSON over a unix socket in a temp dir, answering as herdr's API does. It
// answers ping, session.snapshot, agent.list, agent.get, pane.list, pane.get, pane.process_info, pane.layout and
// pane.read from F's panes, plays agent.prompt and agent.send_keys on them (Pane.OnPrompt, Prompts, Draft, Submitted,
// OnEnter; a "fail-typed" prompt is typed, then its connection drops unanswered; OnEnter "fail" refuses an Enter),
// pane.send_input, pane.send_keys, pane.rename, agent.rename, agent.start, pane.split and tab.create, plugin.list,
// plugin.link and plugin.unlink on F's Plugins, notification.show, and takes events.subscribe (a pane_id that F has
// not is pane_not_found); every other method answers what Reply, Fail or Once scripted (a scripted failure comes
// first, then a scripted answer).
// Push sends an event to the connections subscribed to it, PushError an error line to every subscribed connection.
type Server struct {
	Path string
	F    *Fake

	mu       sync.Mutex
	protocol int
	replies  map[string]map[string]any // method → its result, or {"error": …}
	once     map[string][]Failure      // method → the next calls' failures
	held     map[string]*hold          // method → its answers wait for release
	before   func(Request)
	requests []Request
	subs     []*subscriber
	conns    map[net.Conn]bool
	ln       net.Listener
}

// Failure makes calls fail: herdr's error answer (Code, Message), or a line obor cannot read (Garbage). The fake's
// panes are not touched.
type Failure struct {
	Code, Message string
	Garbage       bool
}

// hold is a held answer: reached closes when one is held, c once it may be sent.
type hold struct {
	c         chan struct{}
	reached   chan struct{}
	once      sync.Once
	reachOnce sync.Once
}

// Request is one request the server received.
type Request struct {
	Method string
	Params json.RawMessage
}

// Subscription is one entry of an events.subscribe.
type Subscription struct {
	Type string `json:"type"`
	Pane string `json:"pane_id"`
}

type subscriber struct {
	conn *conn
	subs []Subscription
}

// conn is one client connection; writes are serialised (responses and pushed events share it).
type conn struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *conn) write(v any) {
	b, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.c.Write(append(b, '\n'))
}

// writeText writes a line as it is (a fake herdr's garbage).
func (c *conn) writeText(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.c.Write([]byte(s))
}

// takeFailure is the next scripted failure of method, if any.
func (s *Server) takeFailure(method string) (Failure, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.once[method]
	if len(q) == 0 {
		return Failure{}, false
	}
	s.once[method] = q[1:]
	return q[0], true
}

// wait holds method's answer until its release (see Hold).
func (s *Server) wait(method string) {
	s.mu.Lock()
	h := s.held[method]
	s.mu.Unlock()
	if h == nil {
		return
	}
	h.reachOnce.Do(func() { close(h.reached) })
	<-h.c
}

// Serve starts a server on f's panes in a new temp dir (short: macOS limits a socket path to 104 bytes); the test's
// cleanup stops it.
func Serve(t testing.TB, f *Fake) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "herdr")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Path: filepath.Join(dir, "herdr.sock"), F: f, protocol: herdr.Protocol,
		replies: map[string]map[string]any{}, once: map[string][]Failure{}, held: map[string]*hold{},
		conns: map[net.Conn]bool{}}
	if err := herdr.CheckSocketPath(s.Path); err != nil {
		t.Fatal(err)
	}
	if err := s.listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Die()
		os.RemoveAll(dir)
	})
	return s
}

// listen opens the socket at Path and accepts on it until Die.
func (s *Server) listen() error {
	ln, err := net.Listen("unix", s.Path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = true
			s.mu.Unlock()
			go s.serve(&conn{c: c})
		}
	}()
	return nil
}

// Die is herdr gone: its socket closed and removed, every connection dropped (a no-op when already down).
func (s *Server) Die() {
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
		_ = os.Remove(s.Path)
	}
	s.DropConns()
}

// Revive is herdr back after Die: the socket at the same Path again, on the same panes.
func (s *Server) Revive(t testing.TB) {
	t.Helper()
	if err := s.listen(); err != nil {
		t.Fatal(err)
	}
}

// Client is a socket client on this server.
func (s *Server) Client() *herdr.Socket { return &herdr.Socket{Path: s.Path, Timeout: 5 * time.Second} }

// Protocol makes ping and session.snapshot answer protocol p.
func (s *Server) Protocol(p int) {
	s.mu.Lock()
	s.protocol = p
	s.mu.Unlock()
}

// Reply makes method answer result (herdr's result object, e.g. {"type":"ok"}).
func (s *Server) Reply(method string, result map[string]any) {
	s.mu.Lock()
	s.replies[method] = map[string]any{"result": result}
	s.mu.Unlock()
}

// Fail makes method answer herdr's error (code, message).
func (s *Server) Fail(method, code, msg string) {
	s.mu.Lock()
	s.replies[method] = map[string]any{"error": map[string]string{"code": code, "message": msg}}
	s.mu.Unlock()
}

// Clear drops what Reply or Fail scripted for method: the fake answers it from F again.
func (s *Server) Clear(method string) {
	s.mu.Lock()
	delete(s.replies, method)
	s.mu.Unlock()
}

// Once makes the next calls of method fail with these, one each (the fake's panes are not touched).
func (s *Server) Once(method string, failures ...Failure) {
	s.mu.Lock()
	s.once[method] = append(s.once[method], failures...)
	s.mu.Unlock()
}

// Hold delays the answers of method until release: the fake acts as it answers, so herdr's state changes while the
// caller still has no answer (T-404). reached closes once an answer is held (the fake has acted). release may be
// called more than once.
func (s *Server) Hold(method string) (release func(), reached <-chan struct{}) {
	h := &hold{c: make(chan struct{}), reached: make(chan struct{})}
	s.mu.Lock()
	s.held[method] = h
	s.mu.Unlock()
	return func() { h.once.Do(func() { close(h.c) }) }, h.reached
}

// Calls is every request so far, one line each: its method and params (see Request.String).
func (s *Server) Calls() []string {
	s.mu.Lock()
	reqs := append([]Request(nil), s.requests...)
	s.mu.Unlock()
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.String())
	}
	return out
}

// String is the request as a test reads it: its method, then its params as sorted key=value pairs
// ("pane.send_input pane_id=w1:p3 text=/exit").
func (r Request) String() string {
	var m map[string]any
	if len(r.Params) == 0 || json.Unmarshal(r.Params, &m) != nil || len(m) == 0 {
		return r.Method
	}
	keys := slices.Sorted(maps.Keys(m))
	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, r.Method)
	for _, k := range keys {
		parts = append(parts, k+"="+param(m[k]))
	}
	return strings.Join(parts, " ")
}

// param is v as a call line shows it: a string as it is, a list in [a b].
func param(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = param(e)
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Before runs fn before each request is answered (e.g. to change F between a snapshot and a subscribe).
func (s *Server) Before(fn func(Request)) {
	s.mu.Lock()
	s.before = fn
	s.mu.Unlock()
}

// Requests is every request so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Subscriptions is every live subscription.
func (s *Server) Subscriptions() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Subscription
	for _, sub := range s.subs {
		out = append(out, sub.subs...)
	}
	return out
}

// WaitSubscribed waits (up to 5 s, else the test fails) until a subscription of type typ for pane ("" for a lifecycle
// type) is live.
func (s *Server) WaitSubscribed(t testing.TB, typ, pane string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		for _, sub := range s.Subscriptions() {
			if sub.Type == typ && sub.Pane == pane {
				return
			}
		}
	}
	t.Fatalf("no %s subscription for pane %q; live: %+v", typ, pane, s.Subscriptions())
}

// Push sends event (herdr's name, dotted or underscored) with data to every connection subscribed to it: its type,
// and for a per-pane type data's pane_id. It returns how many connections got it.
func (s *Server) Push(event string, data map[string]any) int {
	typ := event
	if !strings.Contains(typ, ".") {
		typ = strings.Replace(typ, "_", ".", 1)
	}
	pane, _ := data["pane_id"].(string)
	s.mu.Lock()
	var to []*conn
	for _, sub := range s.subs {
		for _, x := range sub.subs {
			if x.Type == typ && (x.Pane == "" || x.Pane == pane) {
				to = append(to, sub.conn)
				break
			}
		}
	}
	s.mu.Unlock()
	for _, c := range to {
		c.write(map[string]any{"event": event, "data": data})
	}
	return len(to)
}

// PushError sends herdr's error line (e.g. events_lost) to every subscribed connection.
func (s *Server) PushError(code, msg string) {
	s.mu.Lock()
	subs := append([]*subscriber(nil), s.subs...)
	s.mu.Unlock()
	for _, sub := range subs {
		sub.conn.write(map[string]any{"id": "", "error": map[string]string{"code": code, "message": msg}})
	}
}

// DropConns closes every client connection (herdr restarting).
func (s *Server) DropConns() {
	s.mu.Lock()
	conns := s.conns
	s.conns = map[net.Conn]bool{}
	s.subs = nil
	s.mu.Unlock()
	for c := range conns {
		c.Close()
	}
}

func (s *Server) serve(c *conn) {
	defer func() {
		c.c.Close()
		s.mu.Lock()
		delete(s.conns, c.c)
		for i := 0; i < len(s.subs); i++ {
			if s.subs[i].conn == c {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				i--
			}
		}
		s.mu.Unlock()
	}()
	r := bufio.NewReader(c.c)
	for {
		b, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			c.write(map[string]any{"id": "", "error": map[string]string{"code": "invalid_request", "message": err.Error()}})
			continue
		}
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: req.Method, Params: req.Params})
		before := s.before
		s.mu.Unlock()
		if before != nil {
			before(Request{Method: req.Method, Params: req.Params})
		}
		if fail, ok := s.takeFailure(req.Method); ok && fail.Garbage {
			c.writeText("not json\n")
			continue
		} else if ok {
			c.write(map[string]any{"id": req.ID, "error": map[string]string{"code": fail.Code, "message": fail.Message}})
			continue
		}
		answer, after := s.answer(c, req.Method, req.Params)
		if answer == nil { // no answer: the connection drops (herdr died mid-request)
			return
		}
		answer["id"] = req.ID
		s.wait(req.Method)
		c.write(answer)
		if after != nil {
			after()
		}
	}
}

func fail(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

func ok(result map[string]any) map[string]any { return map[string]any{"result": result} }

// answer is the response to one request, without its id, and what to do once it is sent (herdr confirms a subscription
// before its first event).
func (s *Server) answer(c *conn, method string, params json.RawMessage) (map[string]any, func()) {
	s.mu.Lock()
	scripted, isScripted := s.replies[method]
	protocol := s.protocol
	s.mu.Unlock()
	if isScripted {
		out := map[string]any{}
		for k, v := range scripted {
			out[k] = v
		}
		return out, nil
	}
	f := s.F
	switch method {
	case "ping":
		return ok(map[string]any{"type": "pong", "version": "fake", "protocol": protocol}), nil
	case "session.snapshot":
		f.mu.Lock()
		panes, agents, spaces := []any{}, []any{}, []any{}
		for _, w := range f.Workspaces {
			spaces = append(spaces, map[string]any{"workspace_id": w.ID, "label": w.Label})
		}
		for _, p := range f.Panes {
			panes = append(panes, p.paneJSON())
			if p.Kind != "" {
				agents = append(agents, p.agentJSON())
			}
		}
		f.mu.Unlock()
		return ok(map[string]any{"type": "session_snapshot", "snapshot": map[string]any{"version": "fake", "protocol": protocol,
			"workspaces": spaces, "tabs": []any{}, "layouts": []any{}, "panes": panes, "agents": agents}}), nil
	case "agent.list":
		f.mu.Lock()
		agents := []any{}
		for _, p := range f.Panes {
			if p.Kind != "" {
				agents = append(agents, p.agentJSON())
			}
		}
		f.mu.Unlock()
		return ok(map[string]any{"type": "agent_list", "agents": agents}), nil
	case "agent.get":
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.agentPane(p.Target)
		if a == nil {
			return fail("agent_not_found", "agent target "+p.Target+" not found"), nil
		}
		return ok(map[string]any{"type": "agent_info", "agent": a.agentJSON()}), nil
	case "pane.get":
		var p struct {
			Pane string `json:"pane_id"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		pane := f.pane(p.Pane)
		if pane == nil {
			return fail("pane_not_found", "pane "+p.Pane+" not found"), nil
		}
		return ok(map[string]any{"type": "pane_info", "pane": pane.paneJSON()}), nil
	case "pane.process_info":
		var p struct {
			Pane string `json:"pane_id"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		pane := f.pane(p.Pane)
		if pane == nil {
			return fail("pane_not_found", "pane "+p.Pane+" not found"), nil
		}
		return ok(map[string]any{"type": "pane_process_info", "process_info": pane.processInfo()}), nil
	case "agent.prompt":
		var p struct {
			Target, Text string
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.agentPane(p.Target)
		if a == nil {
			return fail("agent_not_found", "agent target "+p.Target+" not found"), nil
		}
		outcome := a.OnPrompt
		if len(a.Prompts) > 0 {
			outcome, a.Prompts = a.Prompts[0], a.Prompts[1:]
		}
		switch outcome {
		case "refused":
			return fail("agent_blocked", "agent "+p.Target+" is blocked"), nil
		case "fail-typed": // typed, then the connection drops before the answer
			a.Draft += p.Text
			return nil, nil
		case "typed":
			a.Draft += p.Text
		default:
			a.submit(p.Text)
			if outcome == "quick" {
				a.Status, a.Seq = "idle", a.Seq+1
			}
		}
		return ok(map[string]any{"type": "agent_prompted", "agent": a.agentJSON()}), nil
	case "agent.send_keys":
		var p struct {
			Target string
			Keys   []string
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.agentPane(p.Target)
		if a == nil {
			return fail("agent_not_found", "agent target "+p.Target+" not found"), nil
		}
		if a.OnEnter == "fail" && slices.Contains(p.Keys, "enter") {
			return fail("keys_failed", "cannot send keys to "+p.Target), nil
		}
		a.Keys = append(a.Keys, p.Keys...)
		for _, k := range p.Keys {
			switch {
			case k == "enter" && a.Draft != "" && a.OnEnter != "lost":
				a.submit("")
			case k == "ctrl+u":
				a.Draft = ""
			}
		}
		return ok(map[string]any{"type": "ok"}), nil
	case "pane.list":
		f.mu.Lock()
		panes := []any{}
		for _, p := range f.Panes {
			panes = append(panes, p.paneJSON())
		}
		f.mu.Unlock()
		return ok(map[string]any{"type": "pane_list", "panes": panes}), nil
	case "pane.layout", "pane.read", "pane.send_input", "pane.send_keys", "pane.rename":
		var p struct {
			Pane  string   `json:"pane_id"`
			Text  string   `json:"text"`
			Keys  []string `json:"keys"`
			Label *string  `json:"label"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		pane := f.pane(p.Pane)
		if pane == nil {
			return fail("pane_not_found", "pane "+p.Pane+" not found"), nil
		}
		switch method {
		case "pane.layout":
			return ok(map[string]any{"type": "pane_layout", "layout": f.layout(pane)}), nil
		case "pane.read":
			return ok(map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": pane.ID, "source": "visible",
				"text": pane.Screen}}), nil
		case "pane.send_input":
			// The text is typed into the pane and waits there: only Enter submits it (running it as the command recipe
			// does), and the other keys act on it as typed keys do (Escape clears it).
			pane.Pending = p.Text
			for _, k := range p.Keys {
				if strings.EqualFold(k, "enter") {
					text := pane.Pending
					pane.Pending = ""
					pane.run(text)
					continue
				}
				pane.sendKeys([]string{k})
			}
		case "pane.send_keys":
			pane.sendKeys(p.Keys)
		case "pane.rename":
			pane.Label = ""
			if p.Label != nil {
				pane.Label = *p.Label
			}
			return ok(map[string]any{"type": "pane_info", "pane": pane.paneJSON()}), nil
		}
		return ok(map[string]any{"type": "ok"}), nil
	case "agent.rename":
		var p struct {
			Target string  `json:"target"`
			Name   *string `json:"name"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.agentPane(p.Target)
		if a == nil {
			return fail("agent_not_found", "agent target "+p.Target+" not found"), nil
		}
		a.Name = ""
		if p.Name != nil {
			a.Name = *p.Name
		}
		return ok(map[string]any{"type": "agent_info", "agent": a.agentJSON()}), nil
	case "agent.start":
		var p struct {
			Name string   `json:"name"`
			Kind string   `json:"kind"`
			Pane string   `json:"pane_id"`
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		t := f.pane(p.Pane)
		if t == nil {
			return fail("pane_not_found", "pane "+p.Pane+" not found"), nil
		}
		if !t.ready() {
			return fail("agent_pane_busy", "agent target pane "+t.ID+" is not an available shell"), nil
		}
		if t.Kind != "" && t.foreground() != 1 {
			return fail("pane_not_available", "pane "+t.ID+" already hosts an agent"), nil
		}
		for _, a := range p.Args { // herdr refuses an argument it cannot encode for the shell
			if strings.ContainsAny(a, "\r\n") {
				return fail("invalid_agent_argument", "agent arguments cannot be encoded safely for the target shell"), nil
			}
		}
		for _, o := range f.Panes {
			if o != t && o.Kind != "" && o.Name == p.Name {
				return fail("agent_name_taken", "agent name "+p.Name+" is taken by pane "+o.ID), nil
			}
		}
		if t.OnStart == "never" {
			return ok(map[string]any{"type": "agent_started", "agent": map[string]any{"name": p.Name, "pane_id": t.ID,
				"agent_status": "unknown", "launch_pending": true}}), nil
		}
		t.Kind, t.Name, t.Status, t.Seq, t.Foreground = p.Kind, p.Name, "idle", t.Seq+1, 0
		if t.OnStart == "unnamed" {
			t.Name = ""
		} else if t.OnStart != "" {
			t.Name, t.Status, t.Seq = "", t.OnStart, t.Seq+1
			return fail("timeout", "agent "+p.Name+" not ready for input within 30000ms"), nil
		}
		return ok(map[string]any{"type": "agent_started", "agent": t.agentJSON()}), nil
	case "pane.split":
		var p struct {
			Pane string `json:"target_pane_id"`
			Dir  string `json:"direction"`
			Cwd  string `json:"cwd"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		t := f.pane(p.Pane)
		if t == nil {
			return fail("pane_not_found", "pane "+p.Pane+" not found"), nil
		}
		return ok(map[string]any{"type": "pane_info", "pane": f.split(t, p.Dir, p.Cwd).paneJSON()}), nil
	case "tab.create":
		var p struct {
			Workspace string `json:"workspace_id"`
			Cwd       string `json:"cwd"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		n := f.newTab(p.Workspace, p.Cwd)
		return ok(map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": n.Tab}, "root_pane": n.paneJSON()}), nil
	case "plugin.list":
		f.mu.Lock()
		defer f.mu.Unlock()
		return ok(map[string]any{"type": "plugin_list", "plugins": f.pluginList()}), nil
	case "plugin.link", "plugin.unlink":
		var p struct {
			Path string `json:"path"`
			ID   string `json:"plugin_id"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		if method == "plugin.link" {
			f.link(p.Path)
			return ok(map[string]any{"type": "plugin_linked"}), nil
		}
		f.unlink(p.ID)
		return ok(map[string]any{"type": "plugin_unlinked", "removed": true}), nil
	case "notification.show":
		return ok(map[string]any{"type": "notification_show", "shown": true, "reason": "shown"}), nil
	case "events.subscribe":
		var p struct {
			Subs []Subscription `json:"subscriptions"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		for _, x := range p.Subs {
			if x.Pane != "" && f.pane(x.Pane) == nil {
				f.mu.Unlock()
				return fail("pane_not_found", "pane "+x.Pane+" not found"), nil
			}
		}
		f.mu.Unlock()
		return ok(map[string]any{"type": "subscription_started"}), func() {
			s.mu.Lock()
			s.subs = append(s.subs, &subscriber{conn: c, subs: p.Subs})
			s.mu.Unlock()
		}
	}
	return fail("not_scripted", "the fake herdr socket has no answer for "+method), nil
}

// Remove drops pane id from the fake (closed behind the client's back).
func (f *Fake) Remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.Panes {
		if p.ID == id {
			f.Panes = append(f.Panes[:i], f.Panes[i+1:]...)
			return
		}
	}
}

// Set changes pane id under the fake's lock (while a server answers from it); false when there is no such pane.
func (f *Fake) Set(id string, fn func(*Pane)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pane(id)
	if p != nil {
		fn(p)
	}
	return p != nil
}

// Locked runs fn under the fake's lock (to change F's other fields while a server answers from it).
func (f *Fake) Locked(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// submit is the agent taking its input box with text: one message, the agent working (seq+1).
func (p *Pane) submit(text string) {
	p.Submitted = append(p.Submitted, p.Draft+text)
	p.Status, p.Seq, p.Draft = "working", p.Seq+1, ""
}
