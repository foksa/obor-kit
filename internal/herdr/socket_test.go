package herdr_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/port"
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
	srv.Protocol(herdr.Protocol - 1)
	_, err := herdr.Dial(ctx, srv.Path)
	e := herdrErr(t, err)
	if e.Code != "protocol" || !strings.Contains(e.Msg, "protocol 21, obor speaks 22") {
		t.Errorf("Dial on protocol 21: %v, want a protocol error naming both", err)
	}
	_, err = srv.Client().Snapshot(ctx)
	if e := herdrErr(t, err); e.Code != "protocol" {
		t.Errorf("Snapshot on protocol 21: %v, want a protocol error", err)
	}
	// A newer protocol is accepted: herdr ignores unknown fields, the conformance suite guards the methods.
	srv.Protocol(herdr.Protocol + 1)
	if _, err := herdr.Dial(ctx, srv.Path); err != nil {
		t.Errorf("Dial on protocol 23: %v, want it accepted", err)
	}
	if snap, err := srv.Client().Snapshot(ctx); err != nil || snap.Protocol != herdr.Protocol+1 {
		t.Errorf("Snapshot on protocol 23 = protocol %d, %v, want 23 and no error", snap.Protocol, err)
	}
}

func TestSocketPathLimit(t *testing.T) {
	t.Parallel()
	long := "/" + strings.Repeat("a", 104)
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
	if snap.Protocol != herdr.Protocol || len(snap.Panes) != 3 || snap.Panes[1] != (herdr.Pane{Pane: port.Pane{ID: "w1:p2", Label: "pj-l-02", Cwd: "/proj-wt/L-02", Launched: true}, Workspace: "w1"}) {
		t.Errorf("Snapshot = %+v", snap)
	}
	want := []port.Pane{
		{ID: "w1:p1", Status: "idle", Cwd: "/proj-wt/L-01", Agent: "claude", Session: "5e1d0c2b-aaaa", Launched: true},
		{ID: "w1:p3", Status: "working", Cwd: "/elsewhere", Agent: "claude", Launched: true},
	}
	var got []port.Pane
	for _, a := range snap.Agents {
		got = append(got, a.Pane)
	}
	if !reflect.DeepEqual(got, want) || snap.Agents[0].Name != "pj-l-01" || snap.Agents[1].Name != "" {
		t.Errorf("Snapshot agents = %+v\nwant %+v, the first named pj-l-01", snap.Agents, want)
	}
	// The agents the core is given carry their pane's label, not herdr's name; so does one pane read by its id.
	want[0].Label = "pj-l-01"
	agents, err := c.Agents(ctx)
	if err != nil || !reflect.DeepEqual(agents, want) {
		t.Errorf("Agents = %+v, %v", agents, err)
	}
	if a, err := c.Pane(ctx, "w1:p1"); err != nil || a != want[0] {
		t.Errorf("Pane of an agent's pane = %+v, %v; want %+v", a, err, want[0])
	}
	shell := port.Pane{ID: "w1:p2", Label: "pj-l-02", Cwd: "/proj-wt/L-02", Launched: true}
	if a, err := c.Pane(ctx, "w1:p2"); err != nil || a != shell {
		t.Errorf("Pane of a shell = %+v, %v; want %+v (no agent)", a, err, shell)
	}
	_, err = c.Pane(ctx, "w9:p9")
	if e := herdrErr(t, err); !port.NotFound(err) || e.Code != "pane_not_found" || e.Op != "pane.get" {
		t.Errorf("Pane of a missing pane = %v, want not found (pane_not_found) from pane.get", err)
	}

	// A pane's workspace and transcript path, from the snapshot.
	f := fixture()
	f.Group("w1", "pj")
	f.Pane("w1:p1").Transcript = "/t/s.jsonl"
	snap, err = herdrtest.Serve(t, f).Client().Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Workspaces) != 1 || snap.Workspaces[0] != (herdr.Workspace{ID: "w1", Label: "pj"}) {
		t.Errorf("workspaces = %+v", snap.Workspaces)
	}
	a := snap.Agents[0]
	if a.Workspace != "w1" || a.Transcript != "/t/s.jsonl" || snap.Panes[0].Transcript != "/t/s.jsonl" {
		t.Errorf("agent = %+v, pane = %+v: want workspace w1 and the transcript path", a, snap.Panes[0])
	}
	if snap.Agents[1].Transcript != "" {
		t.Errorf("an agent without a session has transcript %q", snap.Agents[1].Transcript)
	}

	// A session reported by id is no transcript.
	g := fixture()
	snap, err = herdrtest.Serve(t, g).Client().Snapshot(ctx)
	if err != nil || snap.Agents[0].Session != "5e1d0c2b-aaaa" || snap.Agents[0].Transcript != "" {
		t.Errorf("session by id: %+v, %v", snap.Agents[0], err)
	}
}

func TestSocketRequestParams(t *testing.T) {
	t.Parallel()
	srv := herdrtest.Serve(t, fixture())
	c := srv.Client()
	agent := map[string]any{"pane_id": "w1:p1", "name": "pj-l-01", "agent": "claude", "agent_status": "working"}

	srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": agent})
	srv.Reply("agent.start", map[string]any{"type": "agent_started", "agent": agent, "argv": []string{"claude"}})
	srv.Reply("pane.split", map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": "w1:p9"}})
	srv.Reply("pane.close", map[string]any{"type": "ok"})
	srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})

	if began, err := c.Prompt(ctx, "w1:p1", "hello", false); err != nil || !began {
		t.Errorf("Prompt = %v, %v", began, err)
	}
	if a, err := c.Start(ctx, "pj-l-04", "claude", "w1:p2", nil, "--model", "haiku"); err != nil || a.ID != "w1:p1" {
		t.Errorf("Start = %+v, %v", a, err)
	}
	if id, err := c.Split(ctx, "w1:p1", "down", "/proj-wt/L-04", "OBOR_AGENT=pj-l-04"); err != nil || id != "w1:p9" {
		t.Errorf("Split = %q, %v", id, err)
	}
	if err := c.Close(ctx, "w1:p9"); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Notify(ctx, "obor", "T-319 merged"); err != nil {
		t.Errorf("Notify: %v", err)
	}

	for _, tc := range []struct{ method, want string }{
		{"agent.prompt", `{"target":"w1:p1","text":"hello"}`},
		{"agent.start", `{"args":["--model","haiku"],"kind":"claude","name":"pj-l-04","pane_id":"w1:p2"}`},
		{"pane.split", `{"cwd":"/proj-wt/L-04","direction":"down","env":{"OBOR_AGENT":"pj-l-04"},"target_pane_id":"w1:p1"}`},
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
	began, err := c.Prompt(ctx, "w1:p1", "hi", false)
	if e := herdrErr(t, err); began || e.Kind != herdr.Failed || e.Code != "agent_blocked" || e.Op != "agent.prompt" {
		t.Errorf("blocked prompt = began %v, %v; want it refused before typing, failed agent_blocked", began, err)
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
			time.Sleep(5 * time.Second) // far past the client's bound: only its own deadline can end the call, whatever the load
		}
	})
	slow := &herdr.Socket{Path: srv.Path, Timeout: 50 * time.Millisecond}
	start := time.Now()
	_, err = slow.Agents(ctx)
	if !errors.Is(err, errUnavailable) || !strings.Contains(err.Error(), "timed out after 50ms") || time.Since(start) > 2*time.Second {
		t.Errorf("slow answer = %v after %s, want unavailable: timed out after 50ms, at once", err, time.Since(start))
	}
}

// agent.start takes the client's bound (herdr answers at once, before the agent runs); an answer later than it
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
	_, err := slow.Start(ctx, "pj-l-04", "claude", "w1:p4", nil, "--model", "opus", "--", prompt)
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

// The terminal's error codes read as the port's predicates, and nothing else: the core asks the predicate, never the
// code.
func TestErrorCodesAsPredicates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		code                     string
		busy, notReady, timedOut bool
		answered                 bool
	}{
		{"agent_pane_busy", true, false, false, true},
		{"pane_not_available", true, false, false, true},
		{"agent_not_ready", false, true, false, true},
		{"timeout", false, false, true, true},
		{"agent_name_taken", false, false, false, true},
		{"", false, false, false, false},
	} {
		err := &herdr.Error{Kind: herdr.Failed, Op: "agent.start", Code: c.code, Msg: "x"}
		if got := port.PaneBusy(err); got != c.busy {
			t.Errorf("%q: PaneBusy = %t, want %t", c.code, got, c.busy)
		}
		if got := port.NotReady(err); got != c.notReady {
			t.Errorf("%q: NotReady = %t, want %t", c.code, got, c.notReady)
		}
		if got := port.TimedOut(err); got != c.timedOut {
			t.Errorf("%q: TimedOut = %t, want %t", c.code, got, c.timedOut)
		}
		if got := port.Answered(err); got != c.answered {
			t.Errorf("%q: Answered = %t, want %t", c.code, got, c.answered)
		}
	}
	if !port.NotFound(&herdr.Error{Kind: herdr.NotFound, Op: "agent.get", Code: "agent_not_found"}) {
		t.Error("an agent_not_found answer is not NotFound")
	}
	if port.NotFound(&herdr.Error{Kind: herdr.Unavailable, Op: "agent.get", Msg: "connection closed"}) {
		t.Error("a lost connection reads as NotFound")
	}
}

// agent.start's name-taken retry: a name herdr still holds from a pane that ran the agent before, from a pane obor
// does not label for it, or one no agent holds, is cleared and the start retried once, said on the start's say.
func TestSocketStartNameTaken(t *testing.T) {
	t.Parallel()
	t.Run("a name no agent holds: cleared by name, retried once", func(t *testing.T) {
		t.Parallel()
		f := fixture()
		srv := herdrtest.Serve(t, f)
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-04 taken"})
		if count(srv, "agent.start") != 0 || count(srv, "agent.rename") != 0 {
			t.Fatal("precondition: no start or rename yet")
		}
		a, err := srv.Client().Start(ctx, "pj-l-04", "claude", "w1:p2", nil, "--model", "haiku")
		if err != nil || a.ID != "w1:p2" || f.Pane("w1:p2").Name != "pj-l-04" {
			t.Fatalf("Start = %+v, %v; want the agent started in w1:p2 after the retry", a, err)
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want the retry after the taken name", n)
		}
		cleared := false
		for _, r := range srv.Requests() {
			if r.Method == "agent.rename" && strings.Contains(string(r.Params), `"target":"pj-l-04"`) &&
				strings.Contains(string(r.Params), `"name":null`) {
				cleared = true
			}
		}
		if !cleared {
			t.Errorf("no agent.rename by name: %q", srv.Calls())
		}
	})
	t.Run("nil say: the retry runs silently", func(t *testing.T) {
		t.Parallel()
		srv := herdrtest.Serve(t, fixture())
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-04 taken"})
		if _, err := srv.Client().Start(ctx, "pj-l-04", "claude", "w1:p2", nil); err != nil {
			t.Fatalf("a start with no say = %v, want the silent retry to run", err)
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want the retry", n)
		}
	})
	t.Run("say gets the retry line, then the failed clear's warning", func(t *testing.T) {
		t.Parallel()
		srv := herdrtest.Serve(t, fixture())
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-04 taken"})
		srv.Once("agent.rename", herdrtest.Failure{Code: "agent_not_found", Message: "agent pj-l-04 not found"})
		var said []string
		say := func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }
		if _, err := srv.Client().Start(ctx, "pj-l-04", "claude", "w1:p2", say); err != nil {
			t.Fatalf("Start = %v; want the start retried after the failed clear", err)
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want one retry", n)
		}
		if len(said) != 2 {
			t.Fatalf("say got %q, want the retry line and the warning", said)
		}
		if want := "agent start: name pj-l-04 taken: clearing it and retrying"; said[0] != want {
			t.Errorf("say first line = %q, want %q", said[0], want)
		}
		if !strings.HasPrefix(said[1], "warning: herdr kept the name pj-l-04: ") ||
			!strings.Contains(said[1], "agent_not_found") {
			t.Errorf("say warning = %q, want it to name the agent and the clear's error", said[1])
		}
	})
	t.Run("a live agent elsewhere holds the name: cleared there and retried once", func(t *testing.T) {
		t.Parallel()
		f := fixture()
		srv := herdrtest.Serve(t, f)
		var said []string
		say := func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }
		a, err := srv.Client().Start(ctx, "pj-l-01", "claude", "w1:p2", say)
		if err != nil || a.ID != "w1:p2" || f.Pane("w1:p2").Name != "pj-l-01" {
			t.Fatalf("Start = %+v, %v; want the agent started in w1:p2 after the live holder's name was cleared", a, err)
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want the retry after the taken name", n)
		}
		cleared := false
		for _, r := range srv.Requests() {
			if r.Method == "agent.rename" && strings.Contains(string(r.Params), `"target":"w1:p1"`) &&
				strings.Contains(string(r.Params), `"name":null`) {
				cleared = true
			}
		}
		if !cleared {
			t.Errorf("no agent.rename of the holder w1:p1: %q", srv.Calls())
		}
		if f.Pane("w1:p1").Kind == "" {
			t.Error("the holder's agent was stopped: a rename of its name suffices")
		}
		if want := "agent start: name pj-l-01 taken (pane w1:p1): clearing it and retrying"; len(said) != 1 || said[0] != want {
			t.Errorf("say got %q, want only %q", said, want)
		}
	})
	t.Run("the pane's own lingering name: cleared by its pane, retried once", func(t *testing.T) {
		t.Parallel()
		f := fixture()
		p := f.Agent("w1:p4", "pj-l-04", "idle", "/proj-wt/L-04")
		p.Foreground = 1 // herdr has not reaped the agent's exit: it still holds the name, the shell has the fg
		srv := herdrtest.Serve(t, f)
		srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-04 taken"})
		var said []string
		say := func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }
		a, err := srv.Client().Start(ctx, "pj-l-04", "claude", "w1:p4", say)
		if err != nil || a.ID != "w1:p4" || f.Pane("w1:p4").Name != "pj-l-04" {
			t.Fatalf("Start = %+v, %v; want the agent restarted in its pane", a, err)
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want the retry after the clearing", n)
		}
		cleared := false
		for _, r := range srv.Requests() {
			if r.Method == "agent.rename" && strings.Contains(string(r.Params), `"target":"w1:p4"`) {
				cleared = true
			}
		}
		if !cleared {
			t.Errorf("the lingering name not cleared by its pane: %q", srv.Calls())
		}
		if want := "agent start: name pj-l-04 taken (pane w1:p4): clearing it and retrying"; len(said) != 1 || said[0] != want {
			t.Errorf("say got %q, want only %q", said, want)
		}
	})
	t.Run("taken twice: the refusal, no third start", func(t *testing.T) {
		t.Parallel()
		srv := herdrtest.Serve(t, fixture())
		taken := herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-l-04 taken"}
		srv.Once("agent.start", taken, taken)
		if _, err := srv.Client().Start(ctx, "pj-l-04", "claude", "w1:p2", nil); err == nil {
			t.Fatal("a start refused twice succeeded")
		}
		if n := count(srv, "agent.start"); n != 2 {
			t.Errorf("agent.start sent %d times, want one retry and no more", n)
		}
	})
}

// herdr's own waits: agent.wait ends when the agent reaches the state asked for (text already on the screen
// matches at once), times out while it does not, and ends agent_not_running when the agent leaves the pane;
// pane.wait_for_output reads the text when the regex matches. Each is one request: obor does not poll.
func TestSocketWaits(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	c := srv.Client()

	if err := c.WaitStatus(ctx, "w1:p1", []string{"idle", "done"}, time.Second); err != nil {
		t.Fatalf("Wait of an idle agent: %v", err)
	}
	if got, want := lastParams(t, srv, "agent.wait"), `{"target":"w1:p1","timeout_ms":1000,"until":["idle","done"]}`; got != want {
		t.Errorf("agent.wait params = %s\nwant %s", got, want)
	}
	// A pane that runs no agent has nothing to wait for: a not-found error, never a timeout.
	if err := c.WaitStatus(ctx, "w1:p2", []string{"idle"}, time.Second); !port.NotFound(err) || port.TimedOut(err) {
		t.Errorf("Wait at a shell = %v, want not found", err)
	}

	// The agent works on: the wait ends when it reaches done, not at the call.
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.Set("w1:p3", func(p *herdrtest.Pane) { p.Status = "done" })
	}()
	start := time.Now()
	if err := c.WaitStatus(ctx, "w1:p3", []string{"idle", "done"}, 10*time.Second); err != nil {
		t.Errorf("Wait until the working agent finished: %v", err)
	} else if waited := time.Since(start); waited < 15*time.Millisecond {
		t.Errorf("Wait answered in %s: it answered before the status changed", waited)
	}

	// It times out while the agent stays busy.
	if err := c.WaitStatus(ctx, "w1:p3", []string{"idle"}, 60*time.Millisecond); herdrErr(t, err).Code != "timeout" || !port.TimedOut(err) {
		t.Errorf("Wait on a busy agent = %v, want timeout", err)
	}

	// An agent that leaves its pane ends the wait with agent_not_running: the wait sees it once, then it is gone
	// (LeaveAfter counts checks, so the order is the fake's, not the machine's).
	f.Set("w1:p3", func(p *herdrtest.Pane) { p.LeaveAfter = 2 })
	if err := c.WaitStatus(ctx, "w1:p3", []string{"idle"}, 10*time.Second); herdrErr(t, err).Code != "agent_not_running" {
		t.Errorf("Wait on an agent that left = %v, want agent_not_running", err)
	}

	// Text on the screen matches at once; text printed during the wait matches when it comes.
	f.Set("w1:p2", func(p *herdrtest.Pane) { p.Screen = "early EARLY_42\n" })
	text, err := c.WaitForOutput(ctx, "w1:p2", "EARLY_42", time.Second)
	if err != nil || !strings.Contains(text, "EARLY_42") {
		t.Errorf("WaitForOutput of text on the screen = %q, %v", text, err)
	}
	if got, want := lastParams(t, srv, "pane.wait_for_output"),
		`{"match":{"type":"regex","value":"EARLY_42"},"pane_id":"w1:p2","source":"recent_unwrapped","timeout_ms":1000}`; got != want {
		t.Errorf("pane.wait_for_output params = %s\nwant %s", got, want)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.Set("w1:p2", func(p *herdrtest.Pane) { p.Screen += "late LATE_42\n" })
	}()
	if text, err := c.WaitForOutput(ctx, "w1:p2", "LATE_42", 10*time.Second); err != nil || !strings.Contains(text, "LATE_42") {
		t.Errorf("WaitForOutput of text printed during the wait = %q, %v", text, err)
	}
	if _, err := c.WaitForOutput(ctx, "w1:p2", "NEVER_42", 60*time.Millisecond); herdrErr(t, err).Code != "timeout" {
		t.Errorf("WaitForOutput of text never printed = %v, want timeout", err)
	}
	if n, m := count(srv, "agent.wait"), count(srv, "pane.wait_for_output"); n != 5 || m != 3 {
		t.Errorf("%d agent.wait and %d pane.wait_for_output requests, want one per wait: 5 and 3", n, m)
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
	if ev := next(t, st); ev.Kind != herdr.EventStatus || ev.Pane != "w1:p1" || ev.Status != "done" || ev.Agent != "claude" || ev.Now.Name != "pj-l-01" {
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
	if ev := next(t, st); ev.Kind != herdr.EventCreated || ev.Pane != "w1:p4" || ev.New.Pane != (port.Pane{ID: "w1:p4", Label: "new", Cwd: "/tmp", Launched: true}) {
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

func agentOf(snap herdr.Snapshot, pane string) herdr.Pane {
	for _, a := range snap.Agents {
		if a.ID == pane {
			return a
		}
	}
	return herdr.Pane{}
}

// a status event queued before the final snapshot never overrides it, on every Subscribe (the first
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

// a pane_created queued before the final snapshot does not replace the snapshot's pane (and its agent)
// with the creation's empty shell.
func TestStreamDropsCreatedOlderThanSnapshot(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	_, ran := onSnapshot(srv, 2, func() {
		pushLive(srv, herdr.EventCreated, "", "pane_created",
			map[string]any{"type": "pane_created", "pane": map[string]any{"pane_id": "w1:p1", "cwd": "/proj-wt/L-01"}})
	})
	st, err := srv.Client().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !ran() || agentOf(st.Snapshot, "w1:p1").Name != "pj-l-01" {
		t.Fatalf("precondition: race ran %v, snapshot agents %+v", ran(), st.Snapshot.Agents)
	}
	// The next lifecycle event, on the connection the queued creation came on.
	srv.Push("pane_exited", map[string]any{"type": "pane_exited", "pane_id": "w1:p2", "workspace_id": "w1"})
	if ev := next(t, st); ev != (herdr.Event{Kind: herdr.EventExited, Pane: "w1:p2"}) {
		t.Errorf("first event = %+v, want w1:p2 exited (the queued creation of w1:p1 is older than the snapshot)", ev)
	}
}

// status and lifecycle events come on separate connections, so a pane's status event may arrive after
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

// a pane created after startup is read again once its status subscription is live, so an agent that
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
		if ev.Kind != herdr.EventCreated || ev.Pane != id || ev.New.Agent != "claude" || ev.New.Status != tc.status || ev.Now.Name != "pj-"+id[3:] {
			t.Errorf("%s, agent %s in the gap: created event = %+v, want its agent and status as read after subscribing", tc.round, tc.status, ev)
		}
		srv.WaitSubscribed(t, herdr.EventStatus, id)
	}
	st.Close()
}

// A healthy stream refreshes a pane's agent and metadata: a late agent (detected, or seen by
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
		p.Kind, p.Name, p.Status, p.Session, p.Seq = "claude", "pj-l-02", "idle", "abcd-1234", 1
	}
	for _, tc := range []struct {
		name  string
		pane  string
		set   func(*herdrtest.Pane)
		event string
		data  map[string]any
		want  func(herdr.Pane) bool // nil: no event
	}{
		{"late agent detection", "w1:p2", late, "pane_agent_detected",
			map[string]any{"type": "pane_agent_detected", "pane_id": "w1:p2", "workspace_id": "w1", "agent": "claude"},
			func(a herdr.Pane) bool { return a.Name == "pj-l-02" && a.Agent == "claude" && a.Session == "abcd-1234" }},
		{"a status event brings a new agent", "w1:p2", late, herdr.EventStatus,
			map[string]any{"pane_id": "w1:p2", "workspace_id": "w1", "agent_status": "idle", "agent": "claude"},
			func(a herdr.Pane) bool { return a.Name == "pj-l-02" && a.Session == "abcd-1234" && a.Status == "idle" }},
		{"cwd change", "w1:p1", func(p *herdrtest.Pane) { p.Cwd = "/proj-wt/L-09" }, "pane_updated",
			update("/proj-wt/L-09", byID),
			func(a herdr.Pane) bool { return a.Cwd == "/proj-wt/L-09" && a.Name == "pj-l-01" }},
		{"session path replacement", "w1:p1", func(p *herdrtest.Pane) { p.Transcript = "/t/new.jsonl" }, "pane_updated",
			update("/proj-wt/L-01", map[string]any{"agent": "claude", "kind": "path", "value": "/t/new.jsonl"}),
			func(a herdr.Pane) bool { return a.Transcript == "/t/new.jsonl" && a.Name == "pj-l-01" }},
		{"RV-36 #11: a name change alone (the pane payload has no name)", "w1:p1",
			func(p *herdrtest.Pane) { p.Name = "renamed-agent" }, "pane_updated", update("/proj-wt/L-01", byID),
			func(a herdr.Pane) bool { return a.Name == "renamed-agent" && a.Cwd == "/proj-wt/L-01" }},
		{"an update that changes nothing obor watches", "w1:p1", func(*herdrtest.Pane) {}, "pane_updated",
			update("/proj-wt/L-01", byID), nil},
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
		} else if ev.Kind != herdr.EventStatus || ev.Pane != tc.pane || ev.Agent != ev.Now.Agent || !tc.want(ev.Now) {
			t.Errorf("%s: event = %+v", tc.name, ev)
		}
		st.Close()
	}
}

// a new or renamed workspace or a moved pane ends the stream (Code refresh); the next Subscribe's
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
	if err != nil || len(panes) != 5 || panes[1] != (port.Pane{ID: "w1:p2", Label: "pj-l-02", Cwd: "/proj-wt/L-02", Launched: true}) {
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

	id, err := c.Split(ctx, "w2:a", "right", "/lane", "OBOR_AGENT=pj-l-01", "OBOR_LANE=L-01")
	if err != nil || f.Pane(id) == nil || f.Pane(id).Cwd != "/lane" || f.Pane("w2:a").W != 50 {
		t.Errorf("Split = %q, %v; want a new pane in /lane, w2:a halved", id, err)
	}
	if got := lastParams(t, srv, "pane.split"); got != `{"cwd":"/lane","direction":"right","env":{"OBOR_AGENT":"pj-l-01","OBOR_LANE":"L-01"},"target_pane_id":"w2:a"}` {
		t.Errorf("pane.split params = %s", got)
	}
	_, err = c.Split(ctx, "w9:x", "down", "/x")
	notFound(t, err, "pane.split", "pane_not_found")

	root, err := c.NewTab(ctx, "w2", "pj-l-03", "/proj-wt/L-03", "OBOR_AGENT=pj-l-03")
	if err != nil || !strings.HasPrefix(root, "w2:") || f.Pane(root) == nil || f.Pane(root).Cwd != "/proj-wt/L-03" {
		t.Errorf("NewTab = %q, %v; want a root pane in w2, in /proj-wt/L-03", root, err)
	}
	if got := lastParams(t, srv, "tab.create"); got != `{"cwd":"/proj-wt/L-03","env":{"OBOR_AGENT":"pj-l-03"},"focus":false,"label":"pj-l-03","workspace_id":"w2"}` {
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

	ws, err := c.NewWorkspace(ctx, "proj", "/proj")
	if err != nil || ws == "" || f.Pane(ws+":p1") == nil || f.Pane(ws+":p1").Cwd != "/proj" {
		t.Errorf("NewWorkspace = %q, %v; want a new workspace whose root pane is in /proj", ws, err)
	}
	if got := lastParams(t, srv, "workspace.create"); got != `{"cwd":"/proj","focus":false,"label":"proj"}` {
		t.Errorf("workspace.create params = %s", got)
	}
	srv.Reply("workspace.create", map[string]any{"type": "workspace_created"})
	if _, err := c.NewWorkspace(ctx, "proj", "/proj"); !errors.Is(err, herdr.ErrBadAnswer) {
		t.Errorf("workspace.create without a workspace = %v, want a bad answer", err)
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

	// Write without submit types the text and sends no key: it waits in the pane, nothing runs.
	if err := c.Write(ctx, "w1:p1", "/exit", false); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "pane.send_input"); got != `{"pane_id":"w1:p1","text":"/exit"}` {
		t.Errorf("pane.send_input params without submit = %s", got)
	}
	if p := f.Pane("w1:p1"); p.Pending != "/exit" || p.Screen != "line 1\nline 2\n" {
		t.Fatalf("after an unsubmitted write: pending %q, screen %q; want the text waiting, nothing run", p.Pending, p.Screen)
	}
	if err := c.Write(ctx, "w1:p1", "/exit", true); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "pane.send_input"); got != `{"keys":["Enter"],"pane_id":"w1:p1","text":"/exit"}` {
		t.Errorf("pane.send_input params = %s", got)
	}
	if s, _ := c.Screen(ctx, "w1:p1", 40); !strings.Contains(s, "Exit and stop tasks") {
		t.Fatalf("after /exit the screen = %q, want the exit dialog", s)
	}
	notFound(t, c.Write(ctx, "w9:x", "ls", true), "pane.send_input", "pane_not_found")

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

	// obor's key names go out as herdr's; a digit and a key obor has no name for go as they are.
	if err := c.SendKeys(ctx, "w1:p2", port.KeyEnter, port.KeyEsc, port.KeyCtrlC, port.KeyCtrlU, "2", "tab"); err != nil {
		t.Fatal(err)
	}
	if got := lastParams(t, srv, "pane.send_keys"); got != `{"keys":["enter","esc","ctrl+c","ctrl+u","2","tab"],"pane_id":"w1:p2"}` {
		t.Errorf("pane.send_keys params of obor's key names = %s", got)
	}
}

func TestSocketRenames(t *testing.T) {
	t.Parallel()
	f := fixture()
	srv := herdrtest.Serve(t, f)
	c := srv.Client()

	if err := c.RenamePane(ctx, "w1:p2", "pj-l-05"); err != nil || f.Pane("w1:p2").Label != "pj-l-05" {
		t.Errorf("RenamePane: %v, label %q", err, f.Pane("w1:p2").Label)
	}
	if got := lastParams(t, srv, "pane.rename"); got != `{"label":"pj-l-05","pane_id":"w1:p2"}` {
		t.Errorf("pane.rename params = %s", got)
	}
	if err := c.RenamePane(ctx, "w1:p2", ""); err != nil || f.Pane("w1:p2").Label != "" {
		t.Errorf("RenamePane clear: %v, label %q", err, f.Pane("w1:p2").Label)
	}
	if got := lastParams(t, srv, "pane.rename"); got != `{"label":null,"pane_id":"w1:p2"}` {
		t.Errorf("pane.rename clear params = %s", got)
	}
	notFound(t, c.RenamePane(ctx, "w9:x", "l"), "pane.rename", "pane_not_found")
}

// Notify reads herdr's answer. shown true is success; shown false is a port.NotShown naming the reason (the
// toast's caller logs it and goes on: there is no retry decision here).
func TestNotifyReadsShownAndReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"busy", "rate_limited", "disabled", "no_foreground_client"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			srv := herdrtest.Serve(t, fixture())
			c := srv.Client()
			srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": false, "reason": reason})
			err := c.Notify(ctx, "obor", "T-1 needs you")
			var ns *port.NotShown
			if !errors.As(err, &ns) {
				t.Fatalf("Notify = %v, want a port.NotShown", err)
			}
			if ns.Reason != reason {
				t.Errorf("not shown %q, want %q", ns.Reason, reason)
			}
		})
	}
	srv := herdrtest.Serve(t, fixture())
	c := srv.Client()
	srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	if err := c.Notify(ctx, "obor", "T-1 needs you"); err != nil {
		t.Errorf("shown: %v", err)
	}
}

// the adapter reads herdr's launch fact into port.Pane.Launched from the answers herdr 0.9.5 sends (live
// H-L12, verbatim but the scratch cwd): pending while launch_pending is true (whatever its kind and status show), up
// once launch_pending is gone and interactive_ready is true; herdr leaves a false one out, which reads not pending.
func TestAgentLaunchFact(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, method, answer string
		launched             bool
		agent, status        string
	}{
		{"agent.start's at-once answer: pending", "agent.start",
			`{"type":"agent_started","agent":{"terminal_id":"term_65fc7944662481","name":"conf-ready-128","agent_status":"unknown","workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1","focused":true,"launch_pending":true,"state_change_seq":2,"cwd":"/s/proj-wt/L-01","foreground_cwd":"/s/proj-wt/L-01","revision":2}}`,
			false, "", "unknown"},
		{"pending, the kind already listed: still pending", "agent.get",
			`{"type":"agent_info","agent":{"terminal_id":"term_65fc7944662481","name":"conf-ready-128","agent":"claude","terminal_title":"✳ Claude Code","terminal_title_stripped":"Claude Code","agent_status":"unknown","agent_session":{"source":"herdr:claude","agent":"claude","kind":"id","value":"51ef0f4d-fa2f-4a99-89b5-89c2c73a6f03"},"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1","focused":true,"launch_pending":true,"state_change_seq":2,"cwd":"/s/proj-wt/L-01","foreground_cwd":"/s/proj-wt/L-01","revision":3}}`,
			false, "claude", "unknown"},
		{"up: launch_pending gone, interactive_ready", "agent.get",
			`{"type":"agent_info","agent":{"terminal_id":"term_65fc7944662481","name":"conf-ready-128","agent":"claude","terminal_title":"✳ Claude Code","terminal_title_stripped":"Claude Code","agent_status":"idle","agent_session":{"source":"herdr:claude","agent":"claude","kind":"id","value":"51ef0f4d-fa2f-4a99-89b5-89c2c73a6f03"},"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1","focused":true,"interactive_ready":true,"state_change_seq":3,"cwd":"/s/proj-wt/L-01","foreground_cwd":"/s/proj-wt/L-01","revision":3}}`,
			true, "claude", "idle"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := herdrtest.New()
			f.Shell("w1:p1", "", "/s/proj-wt/L-01")
			srv := herdrtest.Serve(t, f)
			var m map[string]any
			if err := json.Unmarshal([]byte(c.answer), &m); err != nil {
				t.Fatal(err)
			}
			srv.Reply(c.method, m)
			c0 := srv.Client()
			var a port.Pane
			var err error
			if c.method == "agent.start" {
				a, err = c0.Start(ctx, "conf-ready-128", "claude", "w1:p1", nil)
			} else {
				a, err = c0.Pane(ctx, "w1:p1")
			}
			if err != nil {
				t.Fatal(err)
			}
			// Precondition: the scripted answer is the one decoded.
			if a.ID != "w1:p1" || a.Agent != c.agent || a.Status != c.status {
				t.Fatalf("%s = %+v, want pane w1:p1 kind %q status %q", c.method, a, c.agent, c.status)
			}
			if a.Launched != c.launched {
				t.Errorf("%s Launched = %t, want %t", c.method, a.Launched, c.launched)
			}
		})
	}
}

// A client given its connections (Socket.Conn) talks to a herdr served in memory as to one on a socket: it
// answers, a dead server is Unavailable, a revived one answers again.
func TestSocketOverGivenConn(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Agent("w1:p1", "pj-l-01", "idle", "/proj-wt/L-01")
	srv := herdrtest.ServeMem(t, f)
	c := srv.Client()
	if c.Path != "" || c.Conn == nil {
		t.Fatalf("precondition: an in-memory server's client has Conn and no Path, got %+v", c)
	}
	one := func(when string) {
		t.Helper()
		snap, err := c.Snapshot(ctx)
		if err != nil {
			t.Fatalf("%s: Snapshot: %v", when, err)
		}
		if len(snap.Agents) != 1 || snap.Agents[0].Name != "pj-l-01" || snap.Agents[0].ID != "w1:p1" {
			t.Fatalf("%s: agents %+v, want pj-l-01 in w1:p1", when, snap.Agents)
		}
	}
	one("served")
	st, err := (herdr.Panes{S: c}).Subscribe(ctx) // the ping and the stream's connections: Conn too
	if err != nil {
		t.Fatalf("Subscribe over Conn: %v", err)
	}
	st.Close()
	srv.Die()
	_, err = c.Snapshot(ctx)
	if e := herdrErr(t, err); e.Kind != herdr.Unavailable || e.Op != "session.snapshot" {
		t.Fatalf("after Die: %v, want Unavailable from session.snapshot", err)
	}
	srv.Revive(t)
	one("revived")
}
