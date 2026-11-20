// Package hctest is an in-memory hc core for the hc client's tests, herdrtest's counterpart: it holds terminal
// records, answers the methods the client calls (hello, server.status, terminal.list, terminal.get, terminal.read,
// events.subscribe; terminal.send, terminal.keys and terminal.label in shell.go, with a shell that runs the programs a
// test names) on in-memory connections, and sends its subscribers events numbered by seq, heartbeats and missed, in
// the JSON hc 0.1.21 sends (docs/research/hc-probes.md P2, P5, P6, P8).
package hctest

import (
	"bufio"
	"context"
	"encoding/json"
	"maps"
	"net"
	"slices"
	"sync"
	"syscall"

	"obor/internal/hc"
)

// Methods is hello's method list of hc 0.1.21.
var Methods = []string{"hello", "server.status", "server.stop", "server.restart", "terminal.run", "templates.list",
	"terminal.list", "terminal.get", "terminal.kill", "terminal.rm", "terminal.keep", "terminal.label",
	"terminal.read", "terminal.attach", "terminal.send", "terminal.wait", "terminal.keys", "terminal.interrupt",
	"terminal.put", "machine.exec", "file.read", "file.write", "file.edit", "doc.get", "doc.set", "events.subscribe",
	"machine.add", "machine.list", "machine.connect", "machine.rename", "machine.rm", "rpc.discover"}

// Core is one in-memory core. Its fields are read under its lock: set them through its methods.
type Core struct {
	mu         sync.Mutex
	version    string
	generation int
	methods    []string
	id         string
	holders    []string
	down       bool
	recs       []hc.Record // in the order they were added
	screens    map[string]string
	fails      map[string]string // method → the data.kind it answers with
	seq        uint64
	conns      []*conn
	subs       []*conn
	requests   []string
	shell              // shell.go: what typing reaches
	pending    []event // the events an answer made, sent once the answer went out (flush)
}

// New is a core of hc 0.1.21 with one holder and no terminals.
func New() *Core {
	return &Core{version: "0.1.21", generation: hc.Generation, methods: Methods, id: "6bdfe8023479182e4e1a5ff565c060e4",
		holders: []string{"3b6b8d65e9f538dd68637cd26c3be7e2"}, screens: map[string]string{}, fails: map[string]string{},
		shell: newShell()}
}

// Client is a client of c.
func (c *Core) Client() *hc.Client { return &hc.Client{Conn: c.dial} }

// conn is the core's end of one connection; writes are one line at a time.
type conn struct {
	net.Conn
	mu sync.Mutex
}

func (k *conn) send(v any) error {
	b, _ := json.Marshal(v)
	k.mu.Lock()
	defer k.mu.Unlock()
	_, err := k.Write(append(b, '\n'))
	return err
}

// dial hands the core one end of a net.Pipe; a core that is down refuses it as a unix socket with no listener does.
func (c *Core) dial(ctx context.Context) (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	k := &conn{Conn: server}
	c.conns = append(c.conns, k)
	go c.serve(k)
	return client, nil
}

// Down stops the core: every connection closes (a subscriber reads EOF) and a dial is refused. Up starts it again.
func (c *Core) Down() {
	c.mu.Lock()
	c.down = true
	conns := c.conns
	c.conns, c.subs = nil, nil
	c.mu.Unlock()
	for _, k := range conns {
		k.Close()
	}
}

// Drop closes every subscriber's connection with the core still up, as hc drops a stalled reader: it reads EOF.
func (c *Core) Drop() {
	c.mu.Lock()
	subs := c.subs
	c.subs = nil
	c.mu.Unlock()
	for _, k := range subs {
		k.Close()
	}
}

func (c *Core) Up() {
	c.mu.Lock()
	c.down, c.seq = false, 0 // seq restarts with each core process
	c.mu.Unlock()
}

// SetVersion makes hello answer version and generation; SetMethods its method list.
func (c *Core) SetVersion(version string, generation int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version, c.generation = version, generation
}

func (c *Core) SetMethods(methods ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methods = methods
}

// SetHolders makes server.status list holders with these instances.
func (c *Core) SetHolders(instances ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holders = instances
}

// Fail makes method answer an error of data.kind kind ("" answers again).
func (c *Core) Fail(method, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fails[method] = kind
}

// SetScreen is what terminal.read of address answers.
func (c *Core) SetScreen(address, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.screens[address] = text
}

// Labels is address's labels now (nil: no such terminal).
func (c *Core) Labels(address string) map[string]string {
	return maps.Clone(c.record(address).Labels)
}

// Requests is each method the core was asked, in order.
func (c *Core) Requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
}

// Subscribers is how many connections are subscribed.
func (c *Core) Subscribers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)
}

// Add adds r (live unless its State says otherwise) and sends its created event.
func (c *Core) Add(r hc.Record) {
	if r.State == "" {
		r.State = "live"
	}
	c.mu.Lock()
	c.recs = append(c.recs, r)
	c.mu.Unlock()
	c.Event(r.Address, "created", r)
}

// Update changes address's record by fn with no event (what hc reads of a terminal between events, a cwd say).
func (c *Core) Update(address string, fn func(*hc.Record)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.recs {
		if c.recs[i].Address == address {
			fn(&c.recs[i])
		}
	}
}

// SetStatus sets address's status and sends its status event.
func (c *Core) SetStatus(address string, s *hc.Status) {
	c.Update(address, func(r *hc.Record) { r.Status, r.Reported = s, r.Reported || s != nil })
	c.Event(address, "status", map[string]any{"status": s, "reported": c.record(address).Reported, "records": map[string]any{}})
}

// SetForeground sets address's foreground and sends its foreground event.
func (c *Core) SetForeground(address string, f hc.Foreground) {
	c.Update(address, func(r *hc.Record) { r.Foreground = &f })
	c.Event(address, "foreground", f)
}

// Label sets address's labels (a value "" removes the label) and sends its labels event with all of them.
func (c *Core) Label(address string, labels map[string]string) {
	c.Update(address, func(r *hc.Record) {
		r.Labels = maps.Clone(r.Labels)
		if r.Labels == nil {
			r.Labels = map[string]string{}
		}
		for k, v := range labels {
			if v == "" {
				delete(r.Labels, k)
			} else {
				r.Labels[k] = v
			}
		}
	})
	c.Event(address, "labels", map[string]any{"labels": c.record(address).Labels})
}

// Finish ends address's program for reason (exited, killed, holder_lost, …) and sends its finished event.
func (c *Core) Finish(address, reason string) {
	c.Update(address, func(r *hc.Record) { r.State, r.Reason, r.Status, r.Foreground = "finished", reason, nil, nil })
	c.Event(address, "finished", map[string]any{"reason": reason, "exit_code": nil, "signal": nil, "finished_ms": nil})
}

// Restart replaces address's program by a fresh shell of pid (a kept terminal after its holder was lost): lost with
// reason holder_lost, then restarted with the record.
func (c *Core) Restart(address string, pid int) {
	c.Event(address, "lost", map[string]any{"reason": "holder_lost", "exit_code": nil, "signal": nil, "finished_ms": nil})
	c.Update(address, func(r *hc.Record) { r.PID, r.Status, r.Foreground = pid, nil, nil })
	c.Event(address, "restarted", c.record(address))
}

// Stop is `hc server stop`: lost with reason server_stop for each live kept terminal, then the core goes Down.
func (c *Core) Stop() {
	for _, r := range c.records() {
		if r.State == "live" {
			c.Event(r.Address, "lost", map[string]any{"reason": "server_stop", "exit_code": nil, "signal": nil, "finished_ms": nil})
		}
	}
	c.Down()
}

// Event sends one event of address with the next seq to every subscriber; it returns that seq.
func (c *Core) Event(address, typ string, data any) uint64 {
	c.mu.Lock()
	c.seq++
	seq, subs := c.seq, slices.Clone(c.subs)
	c.mu.Unlock()
	c.notify(subs, "event", map[string]any{"seq": seq, "terminal": address, "type": typ, "data": data})
	return seq
}

// Skip numbers one event that no subscriber gets: the next event or heartbeat shows the gap.
func (c *Core) Skip() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
}

// Heartbeat sends a heartbeat with the last event's seq.
func (c *Core) Heartbeat() {
	c.mu.Lock()
	seq, subs := c.seq, slices.Clone(c.subs)
	c.mu.Unlock()
	c.notify(subs, "heartbeat", map[string]any{"seq": seq})
}

// Missed sends missed with the seq and a fresh snapshot, as hc does for a subscriber that fell behind.
func (c *Core) Missed() {
	c.mu.Lock()
	seq, subs, recs := c.seq, slices.Clone(c.subs), slices.Clone(c.recs)
	c.mu.Unlock()
	c.notify(subs, "missed", map[string]any{"seq": seq, "terminals": recs})
}

func (c *Core) notify(subs []*conn, method string, params any) {
	for _, k := range subs {
		_ = k.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
}

func (c *Core) records() []hc.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.recs)
}

func (c *Core) record(address string) hc.Record {
	for _, r := range c.records() {
		if r.Address == address {
			return r
		}
	}
	return hc.Record{}
}

// serve answers one connection's requests until it closes; after events.subscribe it only carries notifications.
func (c *Core) serve(k *conn) {
	defer k.Close()
	r := bufio.NewReader(k)
	for {
		b, err := r.ReadBytes('\n')
		if err != nil {
			c.drop(k)
			return
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b, &req) != nil {
			continue
		}
		c.hold(req.Method)
		// A subscription's answer goes out before any event: the core's events wait on the connection's lock.
		k.mu.Lock()
		result, kind, msg := c.answer(k, req.Method, req.Params)
		out := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if kind != "" {
			out = map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": msg, "data": map[string]any{"kind": kind}}}
		}
		b, _ = json.Marshal(out)
		_, err = k.Write(append(b, '\n'))
		k.mu.Unlock()
		c.flush()
		if err != nil {
			c.drop(k)
			return
		}
	}
}

func (c *Core) drop(k *conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs = slices.DeleteFunc(c.subs, func(s *conn) bool { return s == k })
	c.conns = slices.DeleteFunc(c.conns, func(s *conn) bool { return s == k })
}

// answer is method's result, or the data.kind and message of its error.
func (c *Core) answer(k *conn, method string, raw json.RawMessage) (any, string, string) {
	var p struct {
		Generation int    `json:"generation"`
		Terminal   string `json:"terminal"`
	}
	_ = json.Unmarshal(raw, &p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, method)
	if kind := c.fails[method]; kind != "" {
		return nil, kind, method + " failed (" + kind + ")"
	}
	find := func() (hc.Record, bool) {
		for _, r := range c.recs {
			if r.Address == p.Terminal {
				return r, true
			}
		}
		return hc.Record{}, false
	}
	switch method {
	case "hello":
		if p.Generation != c.generation {
			return nil, "unsupported_generation", "unsupported_generation"
		}
		return map[string]any{"generation": c.generation, "version": c.version, "methods": c.methods, "framed": false,
			"features": []string{"events", "status"}, "core": map[string]any{"id": c.id, "name": "hctest"}}, "", ""
	case "server.status":
		var hs []map[string]any
		for i, h := range c.holders {
			hs = append(hs, map[string]any{"instance": h, "pid": 40000 + i, "current": i == len(c.holders)-1, "terminals": 0})
		}
		return map[string]any{"core": map[string]any{"id": c.id, "name": "hctest"}, "version": c.version, "pid": 30000,
			"uptime_ms": 1, "holders": hs, "live": 0, "finished": 0}, "", ""
	case "terminal.list":
		live := []hc.Record{}
		for _, r := range c.recs {
			if r.State == "live" {
				live = append(live, r)
			}
		}
		return map[string]any{"terminals": live, "hidden_finished": len(c.recs) - len(live), "machines": []any{}}, "", ""
	case "terminal.get":
		if r, ok := find(); ok {
			return r, "", ""
		}
	case "terminal.read":
		if _, ok := find(); ok {
			return map[string]any{"text": c.screens[p.Terminal], "complete": true, "how": "screen"}, "", ""
		}
	case "events.subscribe":
		c.subs = append(c.subs, k) // with its seq: every later event reaches it
		return map[string]any{"channel": nil, "seq": c.seq, "terminals": slices.Clone(c.recs)}, "", ""
	case "terminal.send", "terminal.keys", "terminal.label":
		return c.write(method, raw)
	case "terminal.run":
		return c.start(raw)
	default:
		return nil, "unknown_method", "no method " + method
	}
	return nil, "not_found", "no terminal " + p.Terminal
}
