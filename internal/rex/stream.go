package rex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/linestream"
	"obor/internal/project"
)

// Subscribe is one `rex events --all --json` over the whole server. The stream runs first; a server.status call whose
// client_joined line the stream shows proves it live (sync), and only then is the snapshot read, so no event between
// the two is lost: every later line is applied to the stream's copy of each terminal block and sent on as the whole
// pane when the pane changed. A block's root record, its foreground and its session's label are EventStatus; a block
// first seen (block_created, or any event of a block not yet known: a session's first blocks come with no
// block_created) is EventCreated after its session's blocks are read again; a block's program exiting is EventExited
// (no program runs in that block again); block_closed is EventClosed. A relabel sends only session_view_changed, so
// that event, too, reads the session's blocks again.
//
// The stream ends, C closed and Err set, as a refresh (port.Refreshed): the CLI exiting for any reason (status 4 "server
// went away" when the server stops), or a session's blocks that cannot be read again. A line that does not decode ends
// it as an error that is no refresh; Close as port.NotRunning.
func (p Panes) Subscribe(ctx context.Context) (*port.Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	src, err := p.C.events(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	ls := linestream.Start[port.Event](ctx, cancel, src.read, 256, &Error{Op: "events", Msg: "closed", down: true})
	fail := func(err error) (*port.Stream, error) {
		cancel()
		return nil, err
	}
	st, err := p.C.sync(ctx, src)
	if err != nil {
		return fail(err)
	}
	var v serverVersion
	if err := p.C.call(ctx, &v, "server.version", nil); err != nil {
		return fail(err)
	}
	ss, terms, err := p.C.reads().all(ctx)
	if err != nil {
		return fail(err)
	}
	str := &stream{ctx: ctx, kinds: p.C.kinds, reads: p.C.reads(), labels: map[string]string{}, terms: map[string]term{}}
	for _, s := range ss {
		str.labels[s.ID] = s.Label
	}
	for _, t := range terms {
		str.terms[t.Block.ID] = t
	}
	ls.Run(linestream.Rules[port.Event]{Apply: str.apply, End: src.end})
	return &port.Stream{Snapshot: snapshot(p.C.kinds(), v, st, terms), C: ls.C(), Err: ls.Err, Close: ls.Close}, nil
}

// source is one `rex events --all --json` process: its stdout's lines with the client lines dropped (clientLine), and
// how it ended.
type source struct {
	r     *bufio.Reader
	joins *joins
	done  chan struct{} // closed when read hits the end of the output

	exited chan struct{} // closed once the process was waited for
	code   int           // its exit status (-1: killed), set before exited closes
	stderr string        // its stderr, set before exited closes
}

// events starts the stream's process, bounded by ctx (its end kills it). Its stdout is a pipe of its own, so the
// process is waited for whether or not its output is read to the end.
func (cl *Client) events(ctx context.Context) (*source, error) {
	cmd, err := cl.command(ctx, "events", "events", "--all", "--json")
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, &Error{Op: "events", Msg: err.Error()}
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = pw, &stderr
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, &Error{Op: "events", Msg: "rex did not start: " + err.Error()}
	}
	pw.Close()
	src := &source{r: bufio.NewReaderSize(pr, 1<<20), joins: newJoins(), done: make(chan struct{}), exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		src.code, src.stderr = cmd.ProcessState.ExitCode(), stderr.String()
		pr.Close()
		close(src.exited)
	}()
	return src, nil
}

// read is the next line that is no client line; a client_joined's pid goes to joins.
func (s *source) read() ([]byte, error) {
	for {
		b, err := s.r.ReadBytes('\n')
		if err != nil {
			close(s.done)
			return nil, err
		}
		typ, pid, ok := clientLine(b)
		if !ok {
			return b, nil
		}
		if typ == EventClientJoined {
			s.joins.saw(pid)
		}
	}
}

// exitWait is how long the stream waits for the process to exit once its output ended.
const exitWait = 5 * time.Second

// wait is the process's exit status and stderr once it exited (ok false: it has not within exitWait).
func (s *source) wait() (code int, stderr string, ok bool) {
	select {
	case <-s.exited:
		return s.code, s.stderr, true
	case <-time.After(exitWait):
		return 0, "", false
	}
}

// end is the stream's end once the output ended: a refresh, whatever the process said (endError).
func (s *source) end(error) error {
	code, stderr, ok := s.wait()
	if !ok {
		return &Error{Op: "events", Msg: "rex events closed its output: subscribe again", refresh: true}
	}
	return endError(code, stderr)
}

// endError is the stream's end when the CLI exited with code: a refresh, with its status and stderr's first line
// (status 4: "server went away" when the server stopped; a new subscribe then finds no server, port.NotRunning).
func endError(code int, stderr string) *Error {
	msg := fmt.Sprintf("rex events exited (status %d)", code)
	if l := strings.TrimPrefix(project.FirstLine(strings.TrimSpace(stderr)), "Error: "); l != "" {
		msg += ": " + l
	}
	return &Error{Op: "events", Msg: msg + ": subscribe again", refresh: true}
}

// clientLine reads b's event type: ok when it is a client line (a CLI joining, leaving, attaching to a session or
// dropping: every `rex` call adds a pair under --all), with the joining client's pid for a client_joined.
func clientLine(b []byte) (typ string, pid int, ok bool) {
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			Client struct {
				Principal struct {
					PID int `json:"pid"`
				} `json:"principal"`
			} `json:"client"`
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(b, &l) != nil {
		return "", 0, false
	}
	typ = l.Type
	if typ == EventSession {
		typ = l.Payload.Event.Type
	}
	return typ, l.Payload.Client.Principal.PID, slices.Contains(clientEvents, typ)
}

// joins are the pids of the clients the stream saw join, until the subscribe's sync found its own.
type joins struct {
	mu   sync.Mutex
	seen map[int]bool  // nil once synced
	wake chan struct{} // closed and replaced at each join
}

func newJoins() *joins { return &joins{seen: map[int]bool{}, wake: make(chan struct{})} }

func (j *joins) saw(pid int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.seen == nil {
		return
	}
	j.seen[pid] = true
	close(j.wake)
	j.wake = make(chan struct{})
}

// wait is nil once pid joined; errLate when ctx ends first; errEnded when done closes first.
func (j *joins) wait(ctx context.Context, pid int, done <-chan struct{}) error {
	for {
		j.mu.Lock()
		if j.seen[pid] {
			j.seen = nil
			j.mu.Unlock()
			return nil
		}
		wake := j.wake
		j.mu.Unlock()
		select {
		case <-wake:
		case <-done:
			return errEnded
		case <-ctx.Done():
			return errLate
		}
	}
}

var (
	errLate  = errors.New("no join yet")
	errEnded = errors.New("the stream ended")
)

// syncWait is how long a sync call waits for its join to show on the stream before it calls again: a stream that
// subscribed after the call's client joined never shows that join.
const syncWait = time.Second

// sync calls server.status until the stream shows the call's client_joined (by its pid): the stream was live before
// that call, so a snapshot read after it misses no event. Its answer is the snapshot's server.status. It gives up
// after the client's timeout, or when the stream's process exits (its failure: exit status 4 is no server).
func (cl *Client) sync(ctx context.Context, src *source) (serverStatus, error) {
	deadline := time.Now().Add(cl.timeout())
	for {
		var st serverStatus
		var pid int
		if err := cl.callStarted(ctx, &st, "server.status", nil, func(p int) { pid = p }); err != nil {
			return serverStatus{}, err
		}
		wctx, cancel := context.WithTimeout(ctx, syncWait)
		err := src.joins.wait(wctx, pid, src.done)
		cancel()
		switch {
		case err == nil:
			return st, nil
		case errors.Is(err, errEnded):
			code, stderr, _ := src.wait()
			return serverStatus{}, exitError("events", code, stderr)
		case ctx.Err() != nil:
			return serverStatus{}, &Error{Op: "events", Msg: ctx.Err().Error(), down: true}
		case time.Now().After(deadline):
			return serverStatus{}, &Error{Op: "events", Msg: fmt.Sprintf("the stream showed no server.status call's client within %s", cl.timeout())}
		}
	}
}

// stream is one subscription's copy of each terminal block, which its lines are applied to.
type stream struct {
	ctx   context.Context
	kinds func() agents.Kinds // the client's (Client.kinds), read at each event
	reads reads               // the calls the stream makes: a session's blocks again (resync), the sessions for a new one's label

	labels map[string]string // each session's label, by its id
	terms  map[string]term   // each terminal block as the snapshot or the last event left it, by its id
}

// line is one line of the stream; event a session_event's event; blockEvent a block_event's payload.
type line struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type event struct {
	SessionID string `json:"session_id"`
	Event     struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	} `json:"event"`
}

type blockEvent struct {
	BlockID string          `json:"block_id"`
	Creator string          `json:"creator_name"`
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
}

// apply reads one line: the events to send, or the error that ends the stream.
func (st *stream) apply(b []byte) ([]port.Event, error) {
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, &Error{Op: "events", Msg: "unreadable event: " + project.FirstLine(string(b))}
	}
	bad := func(err error) error {
		return &Error{Op: "events", Msg: fmt.Sprintf("unreadable %s event: %v", l.Type, err)}
	}
	switch l.Type {
	case EventSessionCreated:
		var d struct {
			Session Session `json:"session"`
		}
		if err := json.Unmarshal(l.Payload, &d); err != nil {
			return nil, bad(err)
		}
		st.labels[d.Session.ID] = d.Session.Label
		return st.resync(d.Session.ID)
	case EventSessionDestroyed:
		var d struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(l.Payload, &d); err != nil {
			return nil, bad(err)
		}
		delete(st.labels, d.SessionID)
		return st.closeSession(d.SessionID, nil), nil
	case EventSession:
		var e event
		if err := json.Unmarshal(l.Payload, &e); err != nil {
			return nil, bad(err)
		}
		evs, err := st.event(e.SessionID, e.Event.Type, e.Event.Payload)
		var re *Error
		if err != nil && !errors.As(err, &re) { // a payload that does not decode
			err = &Error{Op: "events", Msg: fmt.Sprintf("unreadable %s event of %s: %v", e.Event.Type, e.SessionID, err)}
		}
		return evs, err
	}
	return nil, nil
}

// event applies one event of session sid.
func (st *stream) event(sid, typ string, data json.RawMessage) ([]port.Event, error) {
	switch typ {
	case EventBlock:
		var be blockEvent
		if err := json.Unmarshal(data, &be); err != nil {
			return nil, err
		}
		return st.blockEvent(sid, be)
	case EventBlockCreated, EventViewChanged:
		return st.resync(sid)
	case EventBlockClosed:
		var d struct {
			BlockID string `json:"block_id"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		if _, ok := st.terms[d.BlockID]; !ok {
			return nil, nil
		}
		delete(st.terms, d.BlockID)
		return []port.Event{{Kind: port.EventClosed, Pane: d.BlockID}}, nil
	case EventLabelChanged:
		var d struct {
			Label string `json:"label"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		st.labels[sid] = d.Label
		var out []port.Event
		for _, id := range st.blocksOf(sid) {
			t := st.terms[id]
			t.Group = d.Label
			out = append(out, st.update(t)...)
		}
		return out, nil
	}
	return nil, nil
}

// blockEvent applies one event of a terminal block: its root record, its foreground, its program's exit. An event
// of a block not known reads its session's blocks again (resync).
func (st *stream) blockEvent(sid string, be blockEvent) ([]port.Event, error) {
	if be.Creator != Terminal {
		return nil, nil
	}
	t, known := st.terms[be.BlockID]
	if !known {
		if be.Name == EventChildExited {
			return nil, nil
		}
		return st.resync(sid)
	}
	switch be.Name {
	case EventStatusChanged:
		var d struct {
			ID     string `json:"id"`
			Record Record `json:"record"`
		}
		if err := json.Unmarshal(be.Payload, &d); err != nil {
			return nil, err
		}
		if d.ID != "" {
			return nil, nil
		}
		t.Status = &d.Record
	case EventStatusRemoved:
		var d struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(be.Payload, &d); err != nil {
			return nil, err
		}
		if !slices.Contains(d.IDs, "") {
			return nil, nil
		}
		t.Status = nil
	case EventProcessChanged:
		var d struct {
			Foreground *Process  `json:"foreground"`
			Ancestors  []Process `json:"ancestors"`
		}
		if err := json.Unmarshal(be.Payload, &d); err != nil {
			return nil, err
		}
		t.Proc.Foreground, t.Proc.Ancestors = d.Foreground, d.Ancestors
	case EventChildExited:
		delete(st.terms, be.BlockID)
		return []port.Event{{Kind: port.EventExited, Pane: be.BlockID}}, nil
	default:
		return nil, nil
	}
	return st.update(t), nil
}

// update stores t: an EventStatus with the whole pane when its pane changed.
func (st *stream) update(t term) []port.Event {
	old := st.terms[t.Block.ID]
	st.terms[t.Block.ID] = t
	p := t.pane(st.kinds())
	if p == old.pane(st.kinds()) {
		return nil
	}
	return []port.Event{{Kind: port.EventStatus, Pane: p.ID, Status: p.Status, Agent: p.Agent, New: p}}
}

// resync reads session sid's terminal blocks again: a block not known is EventCreated, a known one's label (and its
// session's) an EventStatus when its pane changed, a known one no longer listed EventClosed. A session that is gone
// closes its panes; a read that fails ends the stream as a refresh.
func (st *stream) resync(sid string) ([]port.Event, error) {
	refresh := func(err error) error {
		return &Error{Op: "events", Msg: fmt.Sprintf("reading %s's blocks again failed (%v): subscribe again", sid, err), refresh: true}
	}
	label, ok := st.labels[sid]
	if !ok {
		ss, err := st.reads.sessions(st.ctx)
		if err != nil {
			return nil, refresh(err)
		}
		i := slices.IndexFunc(ss, func(s Session) bool { return s.ID == sid })
		if i < 0 {
			return st.closeSession(sid, nil), nil
		}
		label = ss[i].Label
		st.labels[sid] = label
	}
	known := map[string]term{}
	for _, id := range st.blocksOf(sid) {
		known[id] = st.terms[id]
	}
	ts, err := st.reads.session(st.ctx, Session{ID: sid, Label: label}, known)
	switch {
	case port.NotFound(err):
		return st.closeSession(sid, nil), nil
	case err != nil:
		return nil, refresh(err)
	}
	listed := map[string]bool{}
	var out []port.Event
	for _, t := range ts {
		listed[t.Block.ID] = true
		if _, ok := known[t.Block.ID]; ok {
			out = append(out, st.update(t)...)
			continue
		}
		st.terms[t.Block.ID] = t
		out = append(out, port.Event{Kind: port.EventCreated, Pane: t.Block.ID, New: t.pane(st.kinds())})
	}
	return append(out, st.closeSession(sid, listed)...), nil
}

// closeSession closes session sid's known blocks that keep does not hold: EventClosed each, in block id order.
func (st *stream) closeSession(sid string, keep map[string]bool) []port.Event {
	var out []port.Event
	for _, id := range st.blocksOf(sid) {
		if !keep[id] {
			delete(st.terms, id)
			out = append(out, port.Event{Kind: port.EventClosed, Pane: id})
		}
	}
	return out
}

// blocksOf are session sid's known block ids, sorted.
func (st *stream) blocksOf(sid string) []string {
	var ids []string
	for id, t := range st.terms {
		if t.Session == sid {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
