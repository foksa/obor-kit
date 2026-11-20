package hc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"obor/internal/app/port"
	"obor/internal/project"
)

// HeartbeatLost is how long the stream waits for any line before it takes the core for gone: hc sends a heartbeat
// every 15 s, so three missed ones.
const HeartbeatLost = 45 * time.Second

// Subscribe is one events.subscribe over the whole core (no label filter: hc numbers its events across every
// terminal, so only an unfiltered stream can tell a lost event by its seq). Its snapshot is the subscription's own;
// each later event is applied to the stream's copy of the record and sent on as the whole pane when the pane changed:
// a terminal's creation as EventCreated, its status, foreground (cwd) and labels (group, label) as EventStatus, its
// end as EventExited (for good: a kept terminal whose shell exited is finished and never restarted, so the restore
// places a new pane), its removal as EventClosed. A labels event without obor.project is the pane leaving its group
// (Group ""), sent like any other change.
//
// The stream ends, C closed and Err set:
//   - as a refresh (port.Refreshed): hc's `missed` (it dropped events for a slow reader), or a seq gap in an event or
//     a heartbeat;
//   - as a lost connection (port.NotRunning): the connection closed (a restart or an update, a stop, a stalled reader
//     hc dropped), no line for HeartbeatLost, `lost` with reason server_stop (a stop), or `restarted` (a holder was
//     lost and a kept terminal runs a fresh shell: serve's restore after a reconnect restarts its agent).
func (p Panes) Subscribe(ctx context.Context) (*port.Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	s, err := p.C.open(ctx, "events.subscribe")
	if err != nil {
		cancel()
		return nil, err
	}
	var st serverStatus
	var sub struct {
		Seq       *uint64  `json:"seq"`
		Terminals []Record `json:"terminals"`
	}
	if err := s.call(ctx, "server.status", nil, &st); err != nil {
		cancel()
		return nil, err
	}
	if err := s.call(ctx, "events.subscribe", nil, &sub); err != nil {
		cancel()
		return nil, err
	}
	if sub.Seq == nil {
		cancel()
		return nil, &Error{Op: "events.subscribe", Msg: "no seq in the answer"}
	}
	_ = s.c.SetDeadline(time.Time{})
	str := &stream{ctx: ctx, cancel: cancel, s: s, seq: *sub.Seq, recs: map[string]Record{},
		lines: make(chan []byte, 64), out: make(chan port.Event)}
	for _, r := range sub.Terminals {
		if r.State == "live" {
			str.recs[r.Address] = r
		}
	}
	go str.read()
	go str.run()
	return &port.Stream{Snapshot: snapshot(s.hello, st, sub.Terminals), C: str.out, Err: str.Err, Close: str.Close}, nil
}

// stream is one subscription's connection and its copy of each live terminal's record.
type stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	s      *session
	lines  chan []byte // read's: each line, closed when the connection ends
	out    chan port.Event

	seq  uint64            // the last event's
	recs map[string]Record // run's: each live terminal as the snapshot or the last event left it

	mu      sync.Mutex
	err     error
	readErr error // the connection's end, set before lines closes
}

// read feeds the connection's lines to run until it ends.
func (st *stream) read() {
	defer close(st.lines)
	for {
		b, err := st.s.r.ReadBytes('\n')
		if err != nil {
			st.mu.Lock()
			st.readErr = err
			st.mu.Unlock()
			return
		}
		select {
		case st.lines <- b:
		case <-st.ctx.Done():
			return
		}
	}
}

// run turns the lines into events until one ends the stream; it alone sends on and closes out.
func (st *stream) run() {
	defer close(st.out)
	quiet := time.NewTimer(HeartbeatLost)
	defer quiet.Stop()
	for {
		var b []byte
		var ok bool
		select {
		case <-st.ctx.Done():
			st.end(&Error{Op: "events.subscribe", Msg: "closed", down: true})
			return
		case <-quiet.C:
			st.end(&Error{Op: "events.subscribe", Msg: fmt.Sprintf("no heartbeat for %s", HeartbeatLost), down: true})
			return
		case b, ok = <-st.lines:
		}
		if !ok {
			st.mu.Lock()
			err := st.readErr
			st.mu.Unlock()
			st.end(connError(st.ctx, "events.subscribe", err))
			return
		}
		quiet.Reset(HeartbeatLost)
		ev, send, err := st.apply(b)
		if err != nil {
			st.end(err)
			return
		}
		if !send {
			continue
		}
		select {
		case st.out <- ev:
		case <-st.ctx.Done():
		}
	}
}

// notification is one line after the subscription's answer: an event, a heartbeat or a missed.
type notification struct {
	Method string `json:"method"`
	Params struct {
		Seq      uint64          `json:"seq"`
		Type     string          `json:"type"`
		Terminal string          `json:"terminal"`
		Data     json.RawMessage `json:"data"`
	} `json:"params"`
}

// apply reads one line: the event to send (false: none), or the error that ends the stream.
func (st *stream) apply(b []byte) (port.Event, bool, error) {
	var n notification
	if err := json.Unmarshal(b, &n); err != nil {
		return port.Event{}, false, &Error{Op: "events.subscribe", Msg: "unreadable event: " + project.FirstLine(string(b))}
	}
	refresh := func(msg string) error { return &Error{Op: "events.subscribe", Msg: msg, refresh: true} }
	switch n.Method {
	case "missed":
		return port.Event{}, false, refresh("hc dropped events for this subscriber (missed): subscribe again")
	case "heartbeat":
		if n.Params.Seq > st.seq {
			return port.Event{}, false, refresh(fmt.Sprintf("heartbeat at seq %d after event %d: events were lost", n.Params.Seq, st.seq))
		}
		return port.Event{}, false, nil
	case "event":
	default:
		return port.Event{}, false, nil
	}
	if n.Params.Seq != st.seq+1 {
		return port.Event{}, false, refresh(fmt.Sprintf("event seq %d after %d: events were lost", n.Params.Seq, st.seq))
	}
	st.seq = n.Params.Seq
	return st.event(n.Params.Terminal, n.Params.Type, n.Params.Data)
}

// event applies one event of terminal t to its record.
func (st *stream) event(t, typ string, data json.RawMessage) (port.Event, bool, error) {
	bad := func(err error) error {
		return &Error{Op: "events.subscribe", Msg: fmt.Sprintf("unreadable %s event of %s: %v", typ, t, err)}
	}
	old, known := st.recs[t]
	now := old
	switch typ {
	case "created":
		var r Record
		if err := json.Unmarshal(data, &r); err != nil {
			return port.Event{}, false, bad(err)
		}
		st.recs[t] = r
		return port.Event{Kind: port.EventCreated, Pane: t, New: r.pane()}, true, nil
	case "status":
		var d struct {
			Status   *Status `json:"status"`
			Reported bool    `json:"reported"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return port.Event{}, false, bad(err)
		}
		now.Status, now.Reported = d.Status, d.Reported
	case "foreground":
		var f Foreground
		if err := json.Unmarshal(data, &f); err != nil {
			return port.Event{}, false, bad(err)
		}
		now.Foreground = &f
	case "labels":
		var d struct {
			Labels map[string]string `json:"labels"`
			Record *Record           `json:"record"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return port.Event{}, false, bad(err)
		}
		if d.Record != nil { // a terminal newly matching a filter carries its whole record
			now, known = *d.Record, true
			if _, had := st.recs[t]; !had {
				st.recs[t] = now
				return port.Event{Kind: port.EventCreated, Pane: t, New: now.pane()}, true, nil
			}
		}
		now.Labels = d.Labels
	case "finished":
		delete(st.recs, t)
		return port.Event{Kind: port.EventExited, Pane: t}, known, nil
	case "removed":
		delete(st.recs, t)
		return port.Event{Kind: port.EventClosed, Pane: t}, known, nil
	case "restarted":
		return port.Event{}, false, &Error{Op: "events.subscribe", down: true,
			Msg: t + "'s holder was lost and hc runs a fresh shell in it: subscribe again"}
	case "lost":
		var d struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return port.Event{}, false, bad(err)
		}
		if d.Reason == "server_stop" {
			return port.Event{}, false, &Error{Op: "events.subscribe", Msg: "hc is stopping (server_stop)", down: true}
		}
		return port.Event{}, false, nil // its restarted or finished follows
	default:
		return port.Event{}, false, nil
	}
	if !known {
		return port.Event{}, false, nil
	}
	st.recs[t] = now
	p := now.pane()
	if p == old.pane() {
		return port.Event{}, false, nil
	}
	return port.Event{Kind: port.EventStatus, Pane: t, Status: p.Status, Agent: p.Agent, New: p}, true, nil
}

// end stops the stream once with err: run then closes out.
func (st *stream) end(err error) {
	st.mu.Lock()
	if st.err == nil {
		st.err = err
	}
	st.mu.Unlock()
	st.cancel()
}

// Err is why the stream ended (nil while it runs).
func (st *stream) Err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.err
}

// Close ends the stream.
func (st *stream) Close() {
	st.end(&Error{Op: "events.subscribe", Msg: "closed", down: true})
}
