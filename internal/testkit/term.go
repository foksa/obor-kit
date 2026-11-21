package testkit

// Term: an in-memory terminal for the core's tests, in the port's words only. A flow test states a scenario as
// port.Pane, port.Event and a call of a port operation, which every terminal adapter has to meet; the adapters' own
// wires are their packages' tests. Term has no behaviour of its own: a call answers from the table, a change sends the
// event that says it, and the hook a test sets decides the rest.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"obor/internal/app/port"
)

// The operations Term logs, as the port names them.
const (
	OpSubscribe   = "subscribe"    // Panes.Subscribe
	OpSnapshot    = "snapshot"     // Panes.Snapshot, and the snapshot a Subscribe reads
	OpNotify      = "notify"       // Panes.Notify
	OpPane        = "pane"         // PaneControl.Pane: one pane and its agent
	OpPanes       = "panes"        // PaneControl.Panes
	OpProcessInfo = "process-info" // PaneControl.ProcessInfo
	OpScreen      = "screen"       // PaneControl.Screen
	OpRecent      = "recent"       // PaneControl.Recent
	OpWaitStatus  = "wait-status"  // PaneControl.WaitStatus
	OpRename      = "rename"       // PaneControl.RenamePane
	OpWrite       = "write"        // PaneControl.Write
	OpKeys        = "keys"         // PaneControl.SendKeys
	OpNamed       = "named"        // PaneControl.Named
	OpPrompt      = "prompt"       // Prompter.Prompt
	OpStart       = "start"        // AgentStarter.Start
)

// Call is one call the terminal got: its operation and its arguments (Text: a write's, a prompt's or a rename's text;
// Submit: a write's submit, a prompt's clear; Title and Body: a notification's; Start: an agent start's).
type Call struct {
	Op, Pane, Text string
	Keys           []string
	Submit         bool
	Title, Body    string
	Start          port.AgentStart
}

func (c Call) String() string {
	out := c.Op
	for _, f := range []struct{ k, v string }{{"pane", c.Pane}, {"text", c.Text}, {"keys", strings.Join(c.Keys, ",")},
		{"title", c.Title}, {"body", c.Body}, {"label", c.Start.Label}, {"kind", c.Start.Kind}} {
		if f.v != "" {
			out += " " + f.k + "=" + f.v
		}
	}
	if c.Submit {
		out += " submit"
	}
	return out
}

// TermError is an error the terminal answers with: each flag makes it match one of the port's predicates.
type TermError struct {
	Msg     string
	Refresh bool // port.Refreshed: the stream ended so the caller reads a new snapshot
	Gone    bool // port.NotRunning: the terminal is not reachable
	Answer  bool // port.Answered: the terminal's own answer
	Missing bool // port.NotFound
	Timeout bool // port.TimedOut
}

func (e *TermError) Error() string     { return "term: " + e.Msg }
func (e *TermError) Refreshed() bool   { return e.Refresh }
func (e *TermError) Unavailable() bool { return e.Gone }
func (e *TermError) Answered() bool    { return e.Answer }
func (e *TermError) NotFound() bool    { return e.Missing }
func (e *TermError) TimedOut() bool    { return e.Timeout }

// Refresh is the error a stream ends with so the caller reads a new snapshot and subscribes again (port.Refreshed).
func Refresh() error { return &TermError{Msg: "refresh", Refresh: true} }

// Term is an in-memory terminal: a pane table (each pane as the port has it, with the name the terminal runs its agent
// under, its screen and its process info), one stream and a call log. Make it with NewTerm.
type Term struct {
	mu     sync.Mutex
	panes  []port.Pane
	names  map[string]string           // by pane: the terminal's own name of its agent
	screen map[string]string           // by pane: its visible text (Screen, Recent)
	info   map[string]port.ProcessInfo // by pane: its process info
	stream *termStream                 // the live stream (nil: none)
	calls  []Call
	before func(Call) error
}

// NewTerm is an empty terminal.
func NewTerm() *Term {
	return &Term{names: map[string]string{}, screen: map[string]string{}, info: map[string]port.ProcessInfo{}}
}

// Add puts p in the table (a pane of that id is replaced) and sends its created event.
func (t *Term) Add(p port.Pane) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.put(p)
	t.send(port.Event{Kind: port.EventCreated, Pane: p.ID, New: p})
}

// Set changes pane id by fn and sends the status event carrying the new pane; false when there is no such pane.
func (t *Term) Set(id string, fn func(*port.Pane)) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.edit(id, fn)
	if ok {
		t.send(port.Event{Kind: port.EventStatus, Pane: id, Status: p.Status, Agent: p.Agent, New: p})
	}
	return ok
}

// Remove drops pane id from the table and sends its closed event (exited: its exited event).
func (t *Term) Remove(id string, exited bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.drop(id)
	kind := port.EventClosed
	if exited {
		kind = port.EventExited
	}
	t.send(port.Event{Kind: kind, Pane: id})
}

// Put, Edit and Drop are Add, Set and Remove with no event: the stream missed the change, the next snapshot has it.

// Put puts p in the table (a pane of that id is replaced).
func (t *Term) Put(p port.Pane) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.put(p)
}

// Edit changes pane id by fn; false when there is no such pane.
func (t *Term) Edit(id string, fn func(*port.Pane)) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.edit(id, fn)
	return ok
}

// Drop drops pane id from the table.
func (t *Term) Drop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.drop(id)
}

// SetName sets the name the terminal runs pane id's agent under ("" none; Named answers by it).
func (t *Term) SetName(id, name string) { t.mu.Lock(); t.names[id] = name; t.mu.Unlock() }

// SetScreen sets what pane id shows (Screen and Recent answer it).
func (t *Term) SetScreen(id, text string) { t.mu.Lock(); t.screen[id] = text; t.mu.Unlock() }

// SetInfo sets pane id's process info (ProcessInfo answers it).
func (t *Term) SetInfo(id string, info port.ProcessInfo) {
	t.mu.Lock()
	t.info[id] = info
	t.mu.Unlock()
}

// Table is the pane table now.
func (t *Term) Table() []port.Pane {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.panes)
}

// Name is the name the terminal runs pane id's agent under ("" none).
func (t *Term) Name(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.names[id]
}

// End ends the live stream with err (nil: closed); false when no stream is live.
func (t *Term) End(err error) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stream
	if s == nil {
		return false
	}
	t.stream = nil
	s.mu.Lock()
	s.end, s.err = true, err
	s.mu.Unlock()
	s.signal()
	return true
}

// Streaming: a stream is live.
func (t *Term) Streaming() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stream != nil
}

// Before runs fn with each call before it is answered (nil: none); an error fails the call. fn may block.
func (t *Term) Before(fn func(Call) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.before = fn
}

// Calls is the calls of op so far, in order ("": every call).
func (t *Term) Calls(op string) []Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Call
	for _, c := range t.calls {
		if op == "" || c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// call logs c and runs the hook: the error the call answers (nil: answer it).
func (t *Term) call(c Call) error {
	t.mu.Lock()
	t.calls = append(t.calls, c)
	before := t.before
	t.mu.Unlock()
	if before != nil {
		return before(c)
	}
	return nil
}

func (t *Term) index(id string) int {
	return slices.IndexFunc(t.panes, func(p port.Pane) bool { return p.ID == id })
}

func (t *Term) put(p port.Pane) {
	if i := t.index(p.ID); i >= 0 {
		t.panes[i] = p
		return
	}
	t.panes = append(t.panes, p)
}

func (t *Term) edit(id string, fn func(*port.Pane)) (port.Pane, bool) {
	i := t.index(id)
	if i < 0 {
		return port.Pane{}, false
	}
	fn(&t.panes[i])
	return t.panes[i], true
}

func (t *Term) drop(id string) {
	if i := t.index(id); i >= 0 {
		t.panes = slices.Delete(t.panes, i, i+1)
	}
}

// pane is pane id (under mu), or the not-found error.
func (t *Term) pane(id string) (port.Pane, error) {
	if i := t.index(id); i >= 0 {
		return t.panes[i], nil
	}
	return port.Pane{}, &TermError{Msg: "no pane " + id, Missing: true, Answer: true}
}

// Panes is the terminal's panes and events (port.Panes).
func (t *Term) Panes() port.Panes { return termPanes{t} }

// Control is the terminal's pane operations (port.PaneControl) and its agent start (port.AgentStarter).
func (t *Term) Control() TermControl { return TermControl{t} }

// ProcessInfo is pane's process info as set (SetInfo).
func (t *Term) ProcessInfo(_ context.Context, pane string) (port.ProcessInfo, error) {
	if err := t.call(Call{Op: OpProcessInfo, Pane: pane}); err != nil {
		return port.ProcessInfo{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.pane(pane); err != nil {
		return port.ProcessInfo{}, err
	}
	return t.info[pane], nil
}

// Screen is pane's text as set (SetScreen).
func (t *Term) Screen(_ context.Context, pane string, _ int) (string, error) {
	return t.text(Call{Op: OpScreen, Pane: pane})
}

func (t *Term) text(c Call) (string, error) {
	if err := t.call(c); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.pane(c.Pane); err != nil {
		return "", err
	}
	return t.screen[c.Pane], nil
}

// Write logs text typed into pane (submit: with Enter).
func (t *Term) Write(_ context.Context, pane, text string, submit bool) error {
	return t.call(Call{Op: OpWrite, Pane: pane, Text: text, Submit: submit})
}

// SendKeys logs keys sent to pane.
func (t *Term) SendKeys(_ context.Context, pane string, keys ...string) error {
	return t.call(Call{Op: OpKeys, Pane: pane, Keys: keys})
}

// Prompt logs text typed to the agent in pane (clear: its input line cleared first); began is false only when the
// call failed.
func (t *Term) Prompt(_ context.Context, pane, text string, clear bool) (bool, error) {
	if err := t.call(Call{Op: OpPrompt, Pane: pane, Text: text, Submit: clear}); err != nil {
		return false, err
	}
	return true, nil
}

// termPanes is port.Panes on a Term.
type termPanes struct{ t *Term }

// Subscribe opens the stream: the table's snapshot, then the events of the table's changes until End or Close.
func (p termPanes) Subscribe(context.Context) (*port.Stream, error) {
	t := p.t
	if err := t.call(Call{Op: OpSubscribe}); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &termStream{c: make(chan port.Event), wake: make(chan struct{}, 1), closed: make(chan struct{})}
	t.stream = s
	go s.pump()
	return &port.Stream{Snapshot: port.Snapshot{Panes: slices.Clone(t.panes)}, C: s.c,
		Err: func() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err },
		Close: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.stream == s {
				t.stream = nil
			}
			s.once.Do(func() { close(s.closed) })
		}}, nil
}

// Snapshot is the table now.
func (p termPanes) Snapshot(context.Context) (port.Snapshot, error) {
	if err := p.t.call(Call{Op: OpSnapshot}); err != nil {
		return port.Snapshot{}, err
	}
	return port.Snapshot{Panes: p.t.Table()}, nil
}

// Notify logs a notification.
func (p termPanes) Notify(_ context.Context, title, body string) error {
	return p.t.call(Call{Op: OpNotify, Title: title, Body: body})
}

// send queues ev on the live stream (under mu; none live: dropped, the next snapshot has the change).
func (t *Term) send(ev port.Event) {
	if s := t.stream; s != nil {
		s.mu.Lock()
		s.q = append(s.q, ev)
		s.mu.Unlock()
		s.signal()
	}
}

// termStream is one stream: its queued events, delivered in order on c; c closes after the queue once
// the stream ended, or when it is closed.
type termStream struct {
	c      chan port.Event
	wake   chan struct{}
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex // guards q, end and err
	q      []port.Event
	end    bool
	err    error
}

func (s *termStream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *termStream) pump() {
	defer close(s.c)
	for {
		s.mu.Lock()
		switch {
		case len(s.q) > 0:
			ev := s.q[0]
			s.q = s.q[1:]
			s.mu.Unlock()
			select {
			case s.c <- ev:
			case <-s.closed:
				return
			}
			continue
		case s.end:
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-s.closed:
			return
		}
	}
}

// TermControl is port.PaneControl and port.AgentStarter on a Term.
type TermControl struct{ *Term }

var (
	_ port.PaneControl  = TermControl{}
	_ port.AgentStarter = TermControl{}
	_ port.Prompter     = (*Term)(nil)
)

// Pane is pane id from the table, its Group not set.
func (c TermControl) Pane(_ context.Context, id string) (port.Pane, error) {
	if err := c.call(Call{Op: OpPane, Pane: id}); err != nil {
		return port.Pane{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.pane(id)
	p.Group = ""
	return p, err
}

// Panes is the table.
func (c TermControl) Panes(context.Context) ([]port.Pane, error) {
	if err := c.call(Call{Op: OpPanes}); err != nil {
		return nil, err
	}
	return c.Table(), nil
}

// Recent is pane's text as set (SetScreen).
func (c TermControl) Recent(_ context.Context, pane string, _ int) (string, error) {
	return c.text(Call{Op: OpRecent, Pane: pane})
}

// WaitStatus answers at once: nil when pane's agent is in one of until now, a timeout when it runs in another status.
func (c TermControl) WaitStatus(_ context.Context, pane string, until []string, _ time.Duration) error {
	if err := c.call(Call{Op: OpWaitStatus, Pane: pane, Text: strings.Join(until, ",")}); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.pane(pane)
	switch {
	case err != nil:
		return err
	case p.Agent == "":
		return &TermError{Msg: "no agent in " + pane, Missing: true, Answer: true}
	case !slices.Contains(until, p.Status):
		return &TermError{Msg: fmt.Sprintf("%s is %s", pane, p.Status), Timeout: true, Answer: true}
	}
	return nil
}

// RenamePane sets pane's label (no event).
func (c TermControl) RenamePane(_ context.Context, pane, label string) error {
	if err := c.call(Call{Op: OpRename, Pane: pane, Text: label}); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.index(pane)
	if i < 0 {
		_, err := c.pane(pane)
		return err
	}
	c.panes[i].Label = label
	return nil
}

// Named is the pane whose agent runs under name.
func (c TermControl) Named(_ context.Context, name string) (port.Pane, error) {
	if err := c.call(Call{Op: OpNamed, Text: name}); err != nil {
		return port.Pane{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.panes {
		if p.Agent != "" && name != "" && c.names[p.ID] == name {
			p.Group = ""
			return p, nil
		}
	}
	return port.Pane{}, &TermError{Msg: "no agent named " + name, Missing: true, Answer: true}
}

// Start runs s's agent in its pane, as an Edit (no event): the pane's agent is s.Kind, idle, under the name s.Label.
func (c TermControl) Start(_ context.Context, s port.AgentStart) (port.Pane, error) {
	if err := c.call(Call{Op: OpStart, Pane: s.Pane, Start: s}); err != nil {
		return port.Pane{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.edit(s.Pane, func(p *port.Pane) { p.Agent, p.Status = s.Kind, "idle" })
	if !ok {
		_, err := c.pane(s.Pane)
		return port.Pane{}, err
	}
	c.names[s.Pane] = s.Label
	p.Group = ""
	return p, nil
}
