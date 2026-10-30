package herdr_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

// lastParams is the params of the last request of method, as compact JSON with sorted keys.
func lastParams(t *testing.T, srv *herdrtest.Server, method string) string {
	t.Helper()
	reqs := srv.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method {
			var v any
			if err := json.Unmarshal(reqs[i].Params, &v); err != nil {
				t.Fatalf("%s params %s: %v", method, reqs[i].Params, err)
			}
			b, _ := json.Marshal(v)
			return string(b)
		}
	}
	t.Fatalf("no %s request in %+v", method, reqs)
	return ""
}

func count(srv *herdrtest.Server, method string) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

func herdrErr(t *testing.T, err error) *herdr.Error {
	t.Helper()
	var e *herdr.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want a *herdr.Error", err, err)
	}
	return e
}

func TestSocketDialChecksProtocol(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	if _, err := herdr.Dial(ctx, srv.Path); err != nil {
		t.Fatalf("Dial on protocol %d: %v", herdr.Protocol, err)
	}
	if lastParams(t, srv, "ping") != "{}" {
		t.Errorf("ping params = %s, want {}", lastParams(t, srv, "ping"))
	}
	srv.Protocol(herdr.Protocol + 1)
	_, err := herdr.Dial(ctx, srv.Path)
	e := herdrErr(t, err)
	if e.Code != "protocol" || !strings.Contains(e.Msg, "protocol 23, obor speaks 22") {
		t.Errorf("Dial on protocol 23: %v, want a protocol error naming both", err)
	}
	_, err = srv.Client().Snapshot(ctx)
	if e := herdrErr(t, err); e.Code != "protocol" {
		t.Errorf("Snapshot on protocol 23: %v, want a protocol error", err)
	}
}

func TestSocketPathLimit(t *testing.T) {
	t.Parallel()
	if err := herdr.CheckSocketPath("/" + strings.Repeat("a", 103)); err != nil {
		t.Errorf("104-byte path: %v, want ok", err)
	}
	long := "/" + strings.Repeat("a", 104)
	if err := herdr.CheckSocketPath(long); err == nil || !strings.Contains(err.Error(), "105 bytes, longer than the 104") {
		t.Errorf("105-byte path: %v, want a clear error", err)
	}
	_, err := herdr.Dial(ctx, long)
	if !errors.Is(err, errUnavailable) || !strings.Contains(err.Error(), "104") {
		t.Errorf("Dial on a 105-byte path: %v, want unavailable naming the limit", err)
	}
}

func TestSocketPathFromEnv(t *testing.T) {
	t.Parallel()
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	if p := herdr.SocketPath(env(map[string]string{"HERDR_SOCKET_PATH": "/s/h.sock", "HOME": "/home/x"})); p != "/s/h.sock" {
		t.Errorf("with HERDR_SOCKET_PATH: %s", p)
	}
	if p := herdr.SocketPath(env(map[string]string{"HOME": "/home/x"})); p != "/home/x/.config/herdr/herdr.sock" {
		t.Errorf("without: %s", p)
	}
}

func TestSocketSnapshotAndAgents(t *testing.T) {
	t.Parallel()
	c := herdrtest.Serve(t, fixture()).Client()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Protocol != herdr.Protocol || len(snap.Panes) != 3 || snap.Panes[1] != (herdr.Pane{ID: "w1:p2", Label: "pj-o-02", Cwd: "/proj-wt/O-02"}) {
		t.Errorf("Snapshot = %+v", snap)
	}
	want := []herdr.Agent{
		{Name: "pj-o-01", Pane: "w1:p1", Status: "idle", Cwd: "/proj-wt/O-01", Kind: "claude", Session: "5e1d0c2b-aaaa", Seq: 1},
		{Pane: "w1:p3", Status: "working", Cwd: "/elsewhere", Kind: "claude", Seq: 1},
	}
	if !reflect.DeepEqual(snap.Agents, want) {
		t.Errorf("Snapshot agents = %+v\nwant %+v", snap.Agents, want)
	}
	agents, err := c.Agents(ctx)
	if err != nil || !reflect.DeepEqual(agents, want) {
		t.Errorf("Agents = %+v, %v", agents, err)
	}
	if a, err := c.Agent(ctx, "pj-o-01"); err != nil || a != want[0] {
		t.Errorf("Agent = %+v, %v", a, err)
	}
	_, err = c.Agent(ctx, "nope")
	if e := herdrErr(t, err); !errors.Is(err, herdr.ErrNotFound) || e.Code != "agent_not_found" || e.Op != "agent.get" {
		t.Errorf("Agent(nope) = %v, want not found (agent_not_found) from agent.get", err)
	}
}

func TestSocketRequestParams(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	c := srv.Client()
	agent := map[string]any{"pane_id": "w1:p1", "name": "pj-o-01", "agent": "claude", "agent_status": "working"}

	srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": agent})
	srv.Reply("agent.start", map[string]any{"type": "agent_started", "agent": agent, "argv": []string{"claude"}})
	srv.Reply("pane.split", map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": "w1:p9"}})
	srv.Reply("pane.close", map[string]any{"type": "ok"})
	srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})

	a, err := c.Prompt(ctx, "pj-o-01", "hello")
	if err != nil || a.Status != "working" || a.Pane != "w1:p1" {
		t.Errorf("Prompt = %+v, %v", a, err)
	}
	if a, err := c.Start(ctx, "pj-o-04", "claude", "w1:p2", "--model", "haiku"); err != nil || a.Name != "pj-o-01" {
		t.Errorf("Start = %+v, %v", a, err)
	}
	if id, err := c.Split(ctx, "w1:p1", "down", "/proj-wt/O-04", "OBOR_AGENT=pj-o-04"); err != nil || id != "w1:p9" {
		t.Errorf("Split = %q, %v", id, err)
	}
	if err := c.Close(ctx, "w1:p9"); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Notify(ctx, "obor", "T-319 merged"); err != nil {
		t.Errorf("Notify: %v", err)
	}

	for _, tc := range []struct{ method, want string }{
		{"agent.prompt", `{"target":"pj-o-01","text":"hello"}`},
		{"agent.start", `{"args":["--model","haiku"],"kind":"claude","name":"pj-o-04","pane_id":"w1:p2"}`},
		{"pane.split", `{"cwd":"/proj-wt/O-04","direction":"down","env":{"OBOR_AGENT":"pj-o-04"},"target_pane_id":"w1:p1"}`},
		{"pane.close", `{"pane_id":"w1:p9"}`},
		{"notification.show", `{"body":"T-319 merged","title":"obor"}`},
	} {
		if got := lastParams(t, srv, tc.method); got != tc.want {
			t.Errorf("%s params = %s\nwant %s", tc.method, got, tc.want)
		}
	}
}

func TestSocketErrors(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	c := srv.Client()
	srv.Fail("agent.prompt", "agent_blocked", "agent is blocked")
	_, err := c.Prompt(ctx, "pj-o-01", "hi")
	if e := herdrErr(t, err); e.Kind != herdr.Failed || e.Code != "agent_blocked" || e.Op != "agent.prompt" {
		t.Errorf("blocked prompt = %v, want failed agent_blocked", err)
	}
	if err := c.Close(ctx, "w1:p1"); err == nil || herdrErr(t, err).Code != "not_scripted" {
		t.Errorf("unscripted method = %v, want the fake's not_scripted error", err)
	}

	gone := &herdr.Socket{Path: srv.Path + ".gone"}
	if _, err := gone.Agents(ctx); !errors.Is(err, errUnavailable) {
		t.Errorf("no server = %v, want unavailable", err)
	}

	srv.Before(func(r herdrtest.Request) {
		if r.Method == "agent.list" {
			time.Sleep(300 * time.Millisecond)
		}
	})
	slow := &herdr.Socket{Path: srv.Path, Timeout: 50 * time.Millisecond}
	start := time.Now()
	_, err = slow.Agents(ctx)
	if !errors.Is(err, errUnavailable) || !strings.Contains(err.Error(), "timed out after 50ms") || time.Since(start) > 250*time.Millisecond {
		t.Errorf("slow answer = %v after %s, want unavailable: timed out after 50ms, at once", err, time.Since(start))
	}
}

// T-544: agent.start takes the client's bound (herdr answers at once, before the agent runs); an answer later than it
// is a timeout that may have typed the command (Reached), never a second send; a long prompt goes in one request, whole.
func TestSocketStartSentOnce(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	srv.Before(func(r herdrtest.Request) {
		if r.Method == "agent.start" {
			time.Sleep(300 * time.Millisecond)
		}
	})
	prompt := strings.Repeat("a long brief, one paragraph it's ", 60) // ~2 KB, one line
	slow := &herdr.Socket{Path: srv.Path, Timeout: 50 * time.Millisecond}
	if count(srv, "agent.start") != 0 {
		t.Fatal("precondition: no start yet")
	}
	_, err := slow.Start(ctx, "pj-o-04", "claude", "w1:p4", "--model", "opus", "--", prompt)
	if !errors.Is(err, errUnavailable) || !strings.Contains(err.Error(), "timed out after 50ms") {
		t.Fatalf("a start answered after the bound = %v, want unavailable: timed out after 50ms", err)
	}
	if !herdr.Reached(err) {
		t.Errorf("a timed-out start is not marked Reached: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // past the late answer: anything resent would be in by now
	if n := count(srv, "agent.start"); n != 1 {
		t.Fatalf("agent.start sent %d times, want once", n)
	}
	var p struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(lastParams(t, srv, "agent.start")), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Args) != 4 || p.Args[3] != prompt {
		t.Errorf("the prompt arrived as %d args, last %d bytes; want 4 args, the prompt whole (%d bytes)",
			len(p.Args), len(p.Args[len(p.Args)-1]), len(prompt))
	}
}

// Once scripts the next calls' failures, one each: an unreadable line, then herdr's own error, then the fake's answer.
func TestSocketOnceFailure(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	c := srv.Client()
	srv.Once("agent.list", herdrtest.Failure{Garbage: true}, herdrtest.Failure{Code: "internal", Message: "boom"})
	if _, err := c.Agents(ctx); herdrErr(t, err).Kind != herdr.BadAnswer || !strings.Contains(err.Error(), "unreadable answer") {
		t.Errorf("a garbage line = %v, want a bad answer", err)
	}
	if _, err := c.Agents(ctx); herdrErr(t, err).Code != "internal" || !strings.Contains(err.Error(), "boom") {
		t.Errorf("herdr's error = %v, want internal: boom", err)
	}
	if agents, err := c.Agents(ctx); err != nil || len(agents) != 2 {
		t.Errorf("after the scripted failures the fake answers = %+v, %v", agents, err)
	}
}

func next(t *testing.T, st *herdr.Stream) herdr.Event {
	t.Helper()
	select {
	case ev, ok := <-st.C:
		if !ok {
			t.Fatalf("stream ended: %v", st.Err())
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	return herdr.Event{}
}

func ended(t *testing.T, st *herdr.Stream) error {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-st.C:
			if !ok {
				if st.Err() == nil {
					t.Fatal("C closed with no Err")
				}
				return st.Err()
			}
		case <-deadline:
			t.Fatal("stream did not end within 5 s")
		}
	}
}

func TestSubscribeEvents(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if len(st.Snapshot.Panes) != 3 {
		t.Fatalf("Snapshot panes = %+v", st.Snapshot.Panes)
	}
	for _, typ := range []string{"pane.created", "pane.closed", "pane.exited"} {
		srv.WaitSubscribed(t, typ, "")
	}
	for _, p := range []string{"w1:p1", "w1:p2", "w1:p3"} {
		srv.WaitSubscribed(t, "pane.agent_status_changed", p)
	}

	// dotted status event (as herdr sends it) and the schema's underscored name; herdr changes first, as live
	f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "done" })
	if n := srv.Push("pane.agent_status_changed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent_status": "done", "agent": "claude"}); n != 1 {
		t.Fatalf("status push reached %d connections, want 1", n)
	}
	if ev := next(t, st); ev.Kind != herdr.EventStatus || ev.Pane != "w1:p1" || ev.Status != "done" || ev.Agent != "claude" || ev.Now.Name != "pj-o-01" {
		t.Errorf("status event = %+v", ev)
	}
	f.Set("w1:p3", func(p *herdrtest.Pane) { p.Status = "idle" })
	srv.Push("pane_agent_status_changed", map[string]any{"pane_id": "w1:p3", "workspace_id": "w1", "agent_status": "idle"})
	if ev := next(t, st); ev.Kind != herdr.EventStatus || ev.Pane != "w1:p3" || ev.Status != "idle" || ev.Agent != "claude" {
		t.Errorf("underscored status event = %+v (agent: herdr's, read again)", ev)
	}

	// a new pane (underscored, as herdr sends lifecycle events) gets its own status subscription
	f.Shell("w1:p4", "new", "/tmp")
	srv.Push("pane_created", map[string]any{"type": "pane_created", "pane": map[string]any{"pane_id": "w1:p4", "label": "new", "cwd": "/tmp"}})
	if ev := next(t, st); ev.Kind != herdr.EventCreated || ev.Pane != "w1:p4" || ev.New != (herdr.Pane{ID: "w1:p4", Label: "new", Cwd: "/tmp"}) {
		t.Errorf("created event = %+v", ev)
	}
	srv.WaitSubscribed(t, "pane.agent_status_changed", "w1:p4")
	f.Set("w1:p4", func(p *herdrtest.Pane) { p.Kind, p.Status = "codex", "working" })
	srv.Push("pane.agent_status_changed", map[string]any{"pane_id": "w1:p4", "workspace_id": "w1", "agent_status": "working", "agent": "codex"})
	if ev := next(t, st); ev.Kind != herdr.EventStatus || ev.Pane != "w1:p4" || ev.Status != "working" || ev.Agent != "codex" {
		t.Errorf("new pane's status event = %+v", ev)
	}

	srv.Push("pane_closed", map[string]any{"type": "pane_closed", "pane_id": "w1:p4", "workspace_id": "w1"})
	if ev := next(t, st); ev != (herdr.Event{Kind: herdr.EventClosed, Pane: "w1:p4"}) {
		t.Errorf("closed event = %+v", ev)
	}
	srv.Push("pane_exited", map[string]any{"type": "pane_exited", "pane_id": "w1:p2", "workspace_id": "w1"})
	if ev := next(t, st); ev != (herdr.Event{Kind: herdr.EventExited, Pane: "w1:p2"}) {
		t.Errorf("exited event = %+v", ev)
	}
	if st.Err() != nil {
		t.Errorf("Err while running = %v", st.Err())
	}
}

func TestSubscribeClosesCreateRace(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	// w1:p9 appears after the first snapshot (between it and the status subscribe): no pane_created reaches us.
	snaps := 0
	srv.Before(func(r herdrtest.Request) {
		if r.Method == "session.snapshot" {
			if snaps++; snaps == 2 {
				f.Shell("w1:p9", "", "/tmp")
			}
		}
	})
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if count(srv, "session.snapshot") != 3 {
		t.Errorf("snapshots = %d, want 3 (first, then one with w1:p9, then a stable one)", count(srv, "session.snapshot"))
	}
	found := false
	for _, p := range st.Snapshot.Panes {
		found = found || p.ID == "w1:p9"
	}
	if !found {
		t.Errorf("w1:p9 not in Snapshot %+v", st.Snapshot.Panes)
	}
	// Subscribe returned after herdr confirmed it; the fake registers a subscription just after its reply.
	srv.WaitSubscribed(t, herdr.EventStatus, "w1:p9")
}

func TestSubscribeRetriesClosedPane(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	// w1:p2 closes between the snapshot and the subscribe: herdr refuses the subscribe with pane_not_found.
	srv.Before(func(r herdrtest.Request) {
		if r.Method == "events.subscribe" && strings.Contains(string(r.Params), "w1:p2") && f.Pane("w1:p2") != nil {
			f.Remove("w1:p2")
		}
	})
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe = %v, want a retry from a new snapshot", err)
	}
	defer st.Close()
	if len(st.Snapshot.Panes) != 2 || st.Snapshot.Panes[1].ID != "w1:p3" {
		t.Errorf("Snapshot panes = %+v, want w1:p1, w1:p3", st.Snapshot.Panes)
	}
	srv.WaitSubscribed(t, herdr.EventStatus, "w1:p1")
	srv.WaitSubscribed(t, herdr.EventStatus, "w1:p3")
}

func TestStreamEnds(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())

	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv.PushError("events_lost", "subscriber fell behind")
	if e := herdrErr(t, ended(t, st)); e.Code != "events_lost" || e.Op != "events.subscribe" {
		t.Errorf("after an error line: Err = %v, want events_lost", e)
	}

	st, err = srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv.DropConns()
	if err := ended(t, st); !errors.Is(err, errUnavailable) || !strings.Contains(err.Error(), "connection closed") {
		t.Errorf("after herdr dropped the connection: Err = %v, want unavailable: connection closed", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	st, err = srv.Client().Subscribe(cctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := ended(t, st); !errors.Is(err, errUnavailable) {
		t.Errorf("after ctx cancel: Err = %v, want unavailable", err)
	}
	st.Close() // a second end is a no-op
}

func TestSubscribeWatch(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	resume := herdr.Watch{Pane: "w1:p1", Source: "recent", Match: herdr.Match{Value: "claude --resume", Regex: true}}
	st, err := srv.Client().Subscribe(ctx, resume)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv.WaitSubscribed(t, herdr.EventOutput, "w1:p1")
	found := false
	for _, r := range srv.Requests() {
		found = found || string(r.Params) == `{"subscriptions":[{"match":{"type":"regex","value":"claude --resume"},"pane_id":"w1:p1","source":"recent","type":"pane.output_matched"}]}`
	}
	if !found {
		t.Errorf("no output_matched subscription with the watch's params in %+v", srv.Requests())
	}

	help := herdr.Watch{Pane: "w1:p3", Source: "visible", Match: herdr.Match{Value: "Press Ctrl-C again"}}
	if err := st.Watch(help); err != nil {
		t.Fatal(err)
	}
	srv.WaitSubscribed(t, herdr.EventOutput, "w1:p3")
	srv.Push("pane.output_matched", map[string]any{"pane_id": "w1:p3", "matched_line": "Press Ctrl-C again to exit", "read": map[string]any{"text": "…\nPress Ctrl-C again to exit"}})
	if ev := next(t, st); ev.Kind != herdr.EventOutput || ev.Pane != "w1:p3" || ev.Line != "Press Ctrl-C again to exit" || ev.Watch != help || !strings.HasSuffix(ev.Text, "again to exit") {
		t.Errorf("output event = %+v, want the help watch's", ev)
	}
	srv.Push("pane.output_matched", map[string]any{"pane_id": "w1:p1", "matched_line": "claude --resume abc", "read": map[string]any{"text": "x"}})
	if ev := next(t, st); ev.Watch != resume || ev.Line != "claude --resume abc" {
		t.Errorf("output event = %+v, want the resume watch's", ev)
	}
}

// onSnapshot runs fn while herdr answers the n-th session.snapshot of each Subscribe (reset starts the count again);
// fn's pushes then sit queued on the stream's connections behind that snapshot. The fixture's Subscribe reads two:
// the first finds every pane unsubscribed, the second is the final one.
func onSnapshot(srv *herdrtest.Server, n int, fn func()) (reset func(), ran func() bool) {
	var mu sync.Mutex
	snaps, done := 0, false
	srv.Before(func(r herdrtest.Request) {
		if r.Method != "session.snapshot" {
			return
		}
		mu.Lock()
		snaps++
		hit := snaps == n
		mu.Unlock()
		if hit {
			fn()
			mu.Lock()
			done = true
			mu.Unlock()
		}
	})
	return func() { mu.Lock(); snaps, done = 0, false; mu.Unlock() },
		func() bool { mu.Lock(); defer mu.Unlock(); return done }
}

// pushLive is Push once a subscription of typ for pane is live (the server adds it just after confirming it); 0 when
// none is within 5 s. Safe off the test's goroutine.
func pushLive(srv *herdrtest.Server, typ, pane, event string, data map[string]any) int {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		for _, sub := range srv.Subscriptions() {
			if sub.Type == typ && sub.Pane == pane {
				return srv.Push(event, data)
			}
		}
	}
	return 0
}

func agentOf(snap herdr.Snapshot, pane string) herdr.Agent {
	for _, a := range snap.Agents {
		if a.Pane == pane {
			return a
		}
	}
	return herdr.Agent{}
}

// RV-36 #2: a status event queued before the final snapshot never overrides it, on every Subscribe (the first
// connection, and the reconnects after events_lost and after a lost connection all subscribe the same way).
func TestStreamDropsStatusOlderThanSnapshot(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	var mu sync.Mutex
	pushed := 0
	reset, ran := onSnapshot(srv, 2, func() {
		// w1:p1 was blocked, and idle again before this snapshot: the snapshot says idle, the queued event blocked.
		n := pushLive(srv, herdr.EventStatus, "w1:p1", herdr.EventStatus,
			map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent_status": "blocked", "agent": "claude"})
		mu.Lock()
		pushed = n
		mu.Unlock()
	})
	var st *herdr.Stream
	for _, round := range []string{"first connection", "after events_lost", "after a lost connection"} {
		switch round {
		case "after events_lost":
			srv.PushError("events_lost", "subscriber fell behind")
			ended(t, st)
		case "after a lost connection":
			srv.DropConns()
			ended(t, st)
		}
		// The old stream's subscriptions gone from the server (it drops a closed connection's when it reads its end):
		// else the queued event can go to the old connection, not to the new one.
		for deadline := time.Now().Add(5 * time.Second); len(srv.Subscriptions()) > 0; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the old stream's subscriptions are still live: %+v", round, srv.Subscriptions())
			}
		}
		f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "idle" })
		reset()
		var err error
		if st, err = srv.Client().Subscribe(ctx); err != nil {
			t.Fatalf("%s: %v", round, err)
		}
		mu.Lock()
		n := pushed
		mu.Unlock()
		if !ran() || n != 1 || agentOf(st.Snapshot, "w1:p1").Status != "idle" {
			t.Fatalf("%s: precondition: race ran %v, queued event reached %d connections, snapshot %+v", round, ran(), n, st.Snapshot.Agents)
		}
		// The first change after the snapshot, on the connection the queued event came on.
		f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
		srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent_status": "working", "agent": "claude"})
		if ev := next(t, st); ev.Kind != herdr.EventStatus || ev.Status != "working" {
			t.Errorf("%s: first event = %+v, want w1:p1 working (the queued blocked is older than the snapshot)", round, ev)
		}
	}
	st.Close()
}

// RV-36 #2: a pane_created queued before the final snapshot does not replace the snapshot's pane (and its agent)
// with the creation's empty shell.
func TestStreamDropsCreatedOlderThanSnapshot(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	_, ran := onSnapshot(srv, 2, func() {
		pushLive(srv, herdr.EventCreated, "", "pane_created",
			map[string]any{"type": "pane_created", "pane": map[string]any{"pane_id": "w1:p1", "cwd": "/proj-wt/O-01"}})
	})
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !ran() || agentOf(st.Snapshot, "w1:p1").Name != "pj-o-01" {
		t.Fatalf("precondition: race ran %v, snapshot agents %+v", ran(), st.Snapshot.Agents)
	}
	// The next lifecycle event, on the connection the queued creation came on.
	srv.Push("pane_exited", map[string]any{"type": "pane_exited", "pane_id": "w1:p2", "workspace_id": "w1"})
	if ev := next(t, st); ev != (herdr.Event{Kind: herdr.EventExited, Pane: "w1:p2"}) {
		t.Errorf("first event = %+v, want w1:p2 exited (the queued creation of w1:p1 is older than the snapshot)", ev)
	}
}

// RV-36 #2: status and lifecycle events come on separate connections, so a pane's status event may arrive after
// its pane_closed; it is not delivered for a pane C already closed, nor for one herdr no longer has.
func TestStreamStatusAfterCloseOnAnotherConnection(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv.WaitSubscribed(t, herdr.EventStatus, "w1:p3")
	srv.WaitSubscribed(t, herdr.EventClosed, "")
	f.Remove("w1:p3")
	srv.Push("pane_closed", map[string]any{"type": "pane_closed", "pane_id": "w1:p3", "workspace_id": "w1"})
	srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p3", "workspace_id": "w1", "agent_status": "idle", "agent": "claude"})
	// w1:p1 and w1:p3 share a status connection: w1:p1's event comes after w1:p3's.
	f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
	srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent_status": "working", "agent": "claude"})
	closed, status := false, false
	for !closed || !status {
		ev := next(t, st)
		switch {
		case ev == herdr.Event{Kind: herdr.EventClosed, Pane: "w1:p3"}:
			closed = true
		case ev.Kind == herdr.EventStatus && ev.Pane == "w1:p1" && ev.Status == "working":
			status = true
		default:
			t.Errorf("event = %+v, want only w1:p3 closed and w1:p1 working", ev)
		}
	}
}

// RV-36 #3: a pane created after startup is read again once its status subscription is live, so an agent that
// started (and reached any status) before that is not lost; panes created after a reconnect too.
func TestStreamNewPaneReadAfterSubscribe(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	var mu sync.Mutex
	gap := map[string]string{} // pane → the status its agent reaches before the subscription is live
	srv.Before(func(r herdrtest.Request) {
		if r.Method != "events.subscribe" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for id, status := range gap {
			if strings.Contains(string(r.Params), `"`+id+`"`) {
				f.Set(id, func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status, p.Seq = "claude", "pj-"+id[3:], status, 1 })
				delete(gap, id)
			}
		}
	})
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ round, status string }{
		{"first connection", "working"}, {"first connection", "idle"}, {"first connection", "blocked"},
		{"after a reconnect", "idle"},
	} {
		if tc.round == "after a reconnect" && i == 3 {
			srv.DropConns()
			ended(t, st)
			if st, err = srv.Client().Subscribe(ctx); err != nil {
				t.Fatal(err)
			}
		}
		id := "w1:q" + strconv.Itoa(i)
		f.Shell(id, "", "/proj-wt/O-0"+strconv.Itoa(i))
		mu.Lock()
		gap[id] = tc.status
		mu.Unlock()
		pushLive(srv, herdr.EventCreated, "", "pane_created",
			map[string]any{"type": "pane_created", "pane": map[string]any{"pane_id": id, "cwd": "/proj-wt/O-0" + strconv.Itoa(i)}})
		ev := next(t, st)
		mu.Lock()
		raced := gap[id] == ""
		mu.Unlock()
		if !raced {
			t.Fatalf("%s %s: precondition: the agent did not start before the subscription", tc.round, tc.status)
		}
		if ev.Kind != herdr.EventCreated || ev.Pane != id || ev.New.Kind != "claude" || ev.New.Status != tc.status || ev.Now.Name != "pj-"+id[3:] {
			t.Errorf("%s, agent %s in the gap: created event = %+v, want its agent and status as read after subscribing", tc.round, tc.status, ev)
		}
		srv.WaitSubscribed(t, herdr.EventStatus, id)
	}
	st.Close()
}

// RV-36 #4 / Fable A3: a healthy stream refreshes a pane's agent and metadata: a late agent (detected, or seen by
// a status event) brings its name and session; an update brings cwd, session and name changes; an update that
// changes nothing obor watches is not delivered.
func TestStreamRefreshesPaneMetadata(t *testing.T) {
	t.Parallel()
	update := func(cwd string, session map[string]any) map[string]any {
		pane := map[string]any{"pane_id": "w1:p1", "cwd": cwd, "agent": "claude", "agent_status": "idle", "agent_session": session}
		return map[string]any{"type": "pane_updated", "pane": pane}
	}
	byID := map[string]any{"agent": "claude", "kind": "id", "value": "5e1d0c2b-aaaa"}
	late := func(p *herdrtest.Pane) {
		p.Kind, p.Name, p.Status, p.Session, p.Seq = "claude", "pj-o-02", "idle", "abcd-1234", 1
	}
	for _, tc := range []struct {
		name  string
		pane  string
		set   func(*herdrtest.Pane)
		event string
		data  map[string]any
		want  func(herdr.Agent) bool // nil: no event
	}{
		{"late agent detection", "w1:p2", late, "pane_agent_detected",
			map[string]any{"type": "pane_agent_detected", "pane_id": "w1:p2", "workspace_id": "w1", "agent": "claude"},
			func(a herdr.Agent) bool { return a.Name == "pj-o-02" && a.Kind == "claude" && a.Session == "abcd-1234" }},
		{"a status event brings a new agent", "w1:p2", late, herdr.EventStatus,
			map[string]any{"pane_id": "w1:p2", "workspace_id": "w1", "agent_status": "idle", "agent": "claude"},
			func(a herdr.Agent) bool { return a.Name == "pj-o-02" && a.Session == "abcd-1234" && a.Status == "idle" }},
		{"cwd change", "w1:p1", func(p *herdrtest.Pane) { p.Cwd = "/proj-wt/O-09" }, "pane_updated",
			update("/proj-wt/O-09", byID),
			func(a herdr.Agent) bool { return a.Cwd == "/proj-wt/O-09" && a.Name == "pj-o-01" }},
		{"session path replacement", "w1:p1", func(p *herdrtest.Pane) { p.Transcript = "/t/new.jsonl" }, "pane_updated",
			update("/proj-wt/O-01", map[string]any{"agent": "claude", "kind": "path", "value": "/t/new.jsonl"}),
			func(a herdr.Agent) bool { return a.Transcript == "/t/new.jsonl" && a.Name == "pj-o-01" }},
		{"RV-36 #11: a name change alone (the pane payload has no name)", "w1:p1",
			func(p *herdrtest.Pane) { p.Name = "renamed-agent" }, "pane_updated", update("/proj-wt/O-01", byID),
			func(a herdr.Agent) bool { return a.Name == "renamed-agent" && a.Cwd == "/proj-wt/O-01" }},
		{"an update that changes nothing obor watches", "w1:p1", func(*herdrtest.Pane) {}, "pane_updated",
			update("/proj-wt/O-01", byID), nil},
	} {
		f := fixture()
		srv := herdrtest.Serve(t, f)
		st, err := srv.Client().Subscribe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		srv.WaitSubscribed(t, herdr.EventStatus, tc.pane)
		srv.WaitSubscribed(t, herdr.EventCreated, "")
		f.Set(tc.pane, tc.set)
		if n := srv.Push(tc.event, tc.data); n != 1 {
			t.Fatalf("%s: precondition: push reached %d connections", tc.name, n)
		}
		if tc.want == nil { // the next lifecycle event, on the connection the update came on
			srv.Push("pane_exited", map[string]any{"type": "pane_exited", "pane_id": "w1:p2", "workspace_id": "w1"})
		}
		ev := next(t, st)
		if tc.want == nil {
			if ev != (herdr.Event{Kind: herdr.EventExited, Pane: "w1:p2"}) {
				t.Errorf("%s: event = %+v, want none before w1:p2 exited", tc.name, ev)
			}
		} else if ev.Kind != herdr.EventStatus || ev.Pane != tc.pane || ev.Agent != ev.Now.Kind || !tc.want(ev.Now) {
			t.Errorf("%s: event = %+v", tc.name, ev)
		}
		st.Close()
	}
}

// RV-36 #4: a new or renamed workspace or a moved pane ends the stream (Code refresh); the next Subscribe's
// snapshot has them.
func TestStreamRefreshEndsOnLayoutChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, event string
		data        map[string]any
		change      func(*herdrtest.Fake)
		check       func(herdr.Snapshot) bool
	}{
		{"new workspace", "workspace_created", map[string]any{"type": "workspace_created", "workspace": map[string]any{"workspace_id": "w2", "label": "pj2"}},
			func(f *herdrtest.Fake) {
				f.Locked(func() { f.Workspaces = append(f.Workspaces, herdr.Workspace{ID: "w2", Label: "pj2"}) })
			},
			func(s herdr.Snapshot) bool { return len(s.Workspaces) == 2 && s.Workspaces[1].Label == "pj2" }},
		{"renamed workspace", "workspace_renamed", map[string]any{"type": "workspace_renamed", "workspace_id": "w1", "label": "pj-new"},
			func(f *herdrtest.Fake) { f.Locked(func() { f.Workspaces[0].Label = "pj-new" }) },
			func(s herdr.Snapshot) bool { return s.Workspaces[0].Label == "pj-new" }},
		{"moved pane", "pane_moved", map[string]any{"type": "pane_moved", "previous_pane_id": "w1:p2", "previous_workspace_id": "w1",
			"pane": map[string]any{"pane_id": "w2:p1", "workspace_id": "w2"}},
			func(f *herdrtest.Fake) { f.Set("w1:p2", func(p *herdrtest.Pane) { p.ID, p.Workspace = "w2:p1", "w2" }) },
			func(s herdr.Snapshot) bool { return s.Panes[1].ID == "w2:p1" && s.Panes[1].Workspace == "w2" }},
	} {
		f := fixture()
		f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "pj"}}
		srv := herdrtest.Serve(t, f)
		st, err := srv.Client().Subscribe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if tc.check(st.Snapshot) {
			t.Fatalf("%s: precondition: the first snapshot has the change already", tc.name)
		}
		srv.WaitSubscribed(t, herdr.EventCreated, "")
		srv.Before(func(r herdrtest.Request) {
			if r.Method == "session.snapshot" {
				srv.Before(nil)
				tc.change(f)
			}
		})
		// the change lands in herdr before the next snapshot; the event comes first, as live
		if n := srv.Push(tc.event, tc.data); n != 1 {
			t.Fatalf("%s: precondition: push reached %d connections", tc.name, n)
		}
		if e := herdrErr(t, ended(t, st)); e.Code != "refresh" {
			t.Errorf("%s: Err = %v, want Code refresh", tc.name, e)
		}
		st, err = srv.Client().Subscribe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !tc.check(st.Snapshot) {
			t.Errorf("%s: snapshot after the refresh = %+v", tc.name, st.Snapshot)
		}
		st.Close()
	}
}

// notFound fails unless err is herdr's not-found answer code from op.
func notFound(t *testing.T, err error, op, code string) {
	t.Helper()
	if e := herdrErr(t, err); !errors.Is(err, herdr.ErrNotFound) || e.Code != code || e.Op != op {
		t.Errorf("%s on a missing target = %v, want not found (%s)", op, err, code)
	}
}

func TestSocketPanesLayoutTabs(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Tab("w2:t1", 100, 40, "w2:a", "w2:b")
	srv := herdrtest.Serve(t, f)
	c := srv.Client()

	panes, err := c.Panes(ctx)
	if err != nil || len(panes) != 5 || panes[1] != (herdr.Pane{ID: "w1:p2", Label: "pj-o-02", Cwd: "/proj-wt/O-02"}) {
		t.Errorf("Panes = %+v, %v", panes, err)
	}
	if lastParams(t, srv, "pane.list") != "{}" {
		t.Errorf("pane.list params = %s", lastParams(t, srv, "pane.list"))
	}

	rects, err := c.Layout(ctx, "w2:a")
	if want := []herdr.Rect{{Pane: "w2:a", W: 100, H: 40}, {Pane: "w2:b", W: 100, H: 40}}; err != nil || !reflect.DeepEqual(rects, want) {
		t.Errorf("Layout = %+v, %v; want %+v", rects, err, want)
	}
	if got := lastParams(t, srv, "pane.layout"); got != `{"pane_id":"w2:a"}` {
		t.Errorf("pane.layout params = %s", got)
	}
	_, err = c.Layout(ctx, "w9:x")
	notFound(t, err, "pane.layout", "pane_not_found")

	id, err := c.Split(ctx, "w2:a", "right", "/lane", "OBOR_AGENT=pj-o-01", "OBOR_LANE=O-01")
	if err != nil || f.Pane(id) == nil || f.Pane(id).Cwd != "/lane" || f.Pane("w2:a").W != 50 {
		t.Errorf("Split = %q, %v; want a new pane in /lane, w2:a halved", id, err)
	}
	if got := lastParams(t, srv, "pane.split"); got != `{"cwd":"/lane","direction":"right","env":{"OBOR_AGENT":"pj-o-01","OBOR_LANE":"O-01"},"target_pane_id":"w2:a"}` {
		t.Errorf("pane.split params = %s", got)
	}
	_, err = c.Split(ctx, "w9:x", "down", "/x")
	notFound(t, err, "pane.split", "pane_not_found")

	root, err := c.NewTab(ctx, "w2", "pj-o-03", "/proj-wt/O-03", "OBOR_AGENT=pj-o-03")
	if err != nil || !strings.HasPrefix(root, "w2:") || f.Pane(root) == nil || f.Pane(root).Cwd != "/proj-wt/O-03" {
		t.Errorf("NewTab = %q, %v; want a root pane in w2, in /proj-wt/O-03", root, err)
	}
	if got := lastParams(t, srv, "tab.create"); got != `{"cwd":"/proj-wt/O-03","env":{"OBOR_AGENT":"pj-o-03"},"focus":false,"label":"pj-o-03","workspace_id":"w2"}` {
		t.Errorf("tab.create params = %s", got)
	}
	if _, err := c.NewTab(ctx, "", "lead", "/proj"); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "tab.create"); got != `{"cwd":"/proj","focus":false,"label":"lead"}` {
		t.Errorf("tab.create params without a workspace or env = %s", got)
	}
	srv.Reply("tab.create", map[string]any{"type": "tab_created"})
	if _, err := c.NewTab(ctx, "", "lead", "/proj"); !errors.Is(err, herdr.ErrBadAnswer) {
		t.Errorf("tab.create without a root pane = %v, want a bad answer", err)
	}
}

func TestSocketPlugins(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Plugins = []herdrtest.Plugin{{ID: "other", Root: "/o"}}
	srv := herdrtest.Serve(t, f)
	c := srv.Client()

	if err := c.LinkPlugin(ctx, "/kit/plugin"); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "plugin.link"); got != `{"path":"/kit/plugin"}` {
		t.Errorf("plugin.link params = %s", got)
	}
	plugins, err := c.Plugins(ctx)
	if want := []herdr.Plugin{{ID: "other", Root: "/o"}, {ID: "obor.events", Root: "/kit/plugin"}}; err != nil || !reflect.DeepEqual(plugins, want) {
		t.Errorf("Plugins after a link = %+v, %v; want %+v", plugins, err, want)
	}
	if err := c.UnlinkPlugin(ctx, "obor.events"); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "plugin.unlink"); got != `{"plugin_id":"obor.events"}` {
		t.Errorf("plugin.unlink params = %s", got)
	}
	if plugins, err := c.Plugins(ctx); err != nil || !reflect.DeepEqual(plugins, []herdr.Plugin{{ID: "other", Root: "/o"}}) {
		t.Errorf("Plugins after the unlink = %+v, %v", plugins, err)
	}
	srv.Reply("plugin.list", map[string]any{"type": "plugin_list", "plugins": []any{map[string]any{"id": "old", "plugin_root": "/r"}}})
	if plugins, err := c.Plugins(ctx); err != nil || !reflect.DeepEqual(plugins, []herdr.Plugin{{ID: "old", Root: "/r"}}) {
		t.Errorf("Plugins from an older herdr's id = %+v, %v", plugins, err)
	}
	srv.Reply("plugin.list", map[string]any{"type": "plugin_list"})
	if _, err := c.Plugins(ctx); !errors.Is(err, herdr.ErrBadAnswer) {
		t.Errorf("Plugins with no list = %v, want a bad answer", err)
	}
}

func TestSocketPaneInput(t *testing.T) {
	t.Parallel()
	f := fixture()
	f.Pane("w1:p1").Screen = "line 1\nline 2\n"
	f.Pane("w1:p1").OnQuit = "dialog"
	srv := herdrtest.Serve(t, f)
	c := srv.Client()

	if s, err := c.Screen(ctx, "w1:p1", 40); err != nil || s != "line 1\nline 2\n" {
		t.Errorf("Screen = %q, %v", s, err)
	}
	if got := lastParams(t, srv, "pane.read"); got != `{"lines":40,"pane_id":"w1:p1","source":"visible","strip_ansi":true}` {
		t.Errorf("pane.read params = %s", got)
	}
	_, err := c.Screen(ctx, "w9:x", 40)
	notFound(t, err, "pane.read", "pane_not_found")

	if err := c.Type(ctx, "w1:p1", "/exit"); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "pane.send_input"); got != `{"keys":["Enter"],"pane_id":"w1:p1","text":"/exit"}` {
		t.Errorf("pane.send_input params = %s", got)
	}
	if s, _ := c.Screen(ctx, "w1:p1", 40); !strings.Contains(s, "Exit and stop tasks") {
		t.Fatalf("after /exit the screen = %q, want the exit dialog", s)
	}
	notFound(t, c.Type(ctx, "w9:x", "ls"), "pane.send_input", "pane_not_found")

	if err := c.SendKeys(ctx, "w1:p1", "1"); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "pane.send_keys"); got != `{"keys":["1"],"pane_id":"w1:p1"}` {
		t.Errorf("pane.send_keys params = %s", got)
	}
	if p := f.Pane("w1:p1"); p.Kind != "" {
		t.Errorf("after the dialog's 1 the pane hosts %q, want the agent gone", p.Kind)
	}
	notFound(t, c.SendKeys(ctx, "w9:x", "esc"), "pane.send_keys", "pane_not_found")
}

func TestSocketRenames(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	c := srv.Client()

	if err := c.RenamePane(ctx, "w1:p2", "pj-o-05"); err != nil || f.Pane("w1:p2").Label != "pj-o-05" {
		t.Errorf("RenamePane: %v, label %q", err, f.Pane("w1:p2").Label)
	}
	if got := lastParams(t, srv, "pane.rename"); got != `{"label":"pj-o-05","pane_id":"w1:p2"}` {
		t.Errorf("pane.rename params = %s", got)
	}
	if err := c.RenamePane(ctx, "w1:p2", ""); err != nil || f.Pane("w1:p2").Label != "" {
		t.Errorf("RenamePane clear: %v, label %q", err, f.Pane("w1:p2").Label)
	}
	if got := lastParams(t, srv, "pane.rename"); got != `{"label":null,"pane_id":"w1:p2"}` {
		t.Errorf("pane.rename clear params = %s", got)
	}
	notFound(t, c.RenamePane(ctx, "w9:x", "l"), "pane.rename", "pane_not_found")

	if err := c.RenameAgent(ctx, "w1:p3", "pj-o-03"); err != nil || f.Pane("w1:p3").Name != "pj-o-03" {
		t.Errorf("RenameAgent: %v, name %q", err, f.Pane("w1:p3").Name)
	}
	if got := lastParams(t, srv, "agent.rename"); got != `{"name":"pj-o-03","target":"w1:p3"}` {
		t.Errorf("agent.rename params = %s", got)
	}
	if err := c.RenameAgent(ctx, "pj-o-03", ""); err != nil || f.Pane("w1:p3").Name != "" {
		t.Errorf("RenameAgent clear: %v, name %q", err, f.Pane("w1:p3").Name)
	}
	if got := lastParams(t, srv, "agent.rename"); got != `{"name":null,"target":"pj-o-03"}` {
		t.Errorf("agent.rename clear params = %s", got)
	}
	notFound(t, c.RenameAgent(ctx, "nope", "x"), "agent.rename", "agent_not_found")
}
