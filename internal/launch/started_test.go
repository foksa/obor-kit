package launch_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
)

// A start's one success path reports the confirmed start with the agent's name, whatever session it found or none:
// serve ends the agent's skew entry at it. A fresh Codex start with no prompt writes no rollout before its first
// prompt, so the start is no session's to report. A start that failed, or one that returned no pane, reports nothing.
func TestStartReportsTheConfirmedStart(t *testing.T) {
	t.Parallel()
	t.Run("cli: fresh codex start with no prompt and no rollout found", func(t *testing.T) {
		t.Parallel()
		x := setupRunning(t, "luna")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "holds no codex transcript open", "precondition: no rollout found")
		eq(t, strings.Join(x.startReports(), " "), "pj-l-01", "the confirmed start")
		if len(x.reported) != 0 {
			t.Errorf("the start reported a session it has none of: %+v", x.reported)
		}
	})
	t.Run("cli: fresh start whose rollout is found", func(t *testing.T) {
		t.Parallel()
		x := setupRunning(t, "luna")
		x.wrap = func(h port.PaneControl) port.PaneControl {
			return onStart{h, func() {
				fresh := writeRollout(x, "cx-new", x.lane, time.Now())
				x.hold(2, fresh)
			}}
		}
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.startReports(), " "), "pj-l-01", "the confirmed start")
		if len(x.reported) != 1 || x.reported[0].ID != "cx-new" {
			t.Fatalf("precondition: the rollout found and reported, got %+v\n%s", x.reported, out)
		}
	})
	t.Run("cli: resumed start", func(t *testing.T) {
		t.Parallel()
		x := setupRunning(t, "luna")
		x.shell("w1:p3", "pj-l-01", "").Session = "cx-1"
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.startReports(), " "), "pj-l-01", "the confirmed start")
		if len(x.reported) != 1 || x.reported[0].ID != "cx-1" {
			t.Fatalf("precondition: the resumed session reported, got %+v\n%s", x.reported, out)
		}
	})
	t.Run("serve: fresh start", func(t *testing.T) {
		t.Parallel()
		x := setupRunning(t, "luna")
		x.capture = true // serve's rollout search, run by the test after the start
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: "[obor job T-1/1/work] call the obor job_next tool"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.startReports(), " "), "pj-l-01", "the confirmed start")
		if n := x.runSearches(); n != 1 {
			t.Fatalf("precondition: serve defers the rollout search, ran %d", n)
		}
	})
	t.Run("serve: resumed start", func(t *testing.T) {
		t.Parallel()
		x := setupRunning(t, "luna")
		x.capture = true
		x.shell("w1:p3", "pj-l-01", "").Session = "cx-1"
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.startReports(), " "), "pj-l-01", "the confirmed start")
		if len(x.reported) != 1 || x.reported[0].ID != "cx-1" {
			t.Fatalf("precondition: the resumed session reported, got %+v\n%s", x.reported, out)
		}
		if n := x.runSearches(); n != 0 {
			t.Errorf("a resume runs %d rollout searches, want none", n)
		}
	})
	t.Run("the project's serve gets the request", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		sock := wire.SocketPath(x.p.Root)
		if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		type call struct{ method, agent, root string }
		got := make(chan call, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			b, _ := bufio.NewReader(c).ReadBytes('\n')
			var req struct {
				Method string        `json:"method"`
				Call   wire.Envelope `json:"call"`
			}
			_ = json.Unmarshal(b, &req)
			got <- call{req.Method, req.Call.Agent, req.Call.Root}
			_, _ = c.Write([]byte("{\"reply\":\"STARTED pj-l-01\"}\n"))
		}()
		x.shell("w1:p3", "pj-l-01", "")
		var out bytes.Buffer
		l := x.launcher(&out)
		l.Started = nil // the started request to the project root's serve
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		if _, ok := result(t, &out, line, err); !ok {
			t.Fatal(out.String())
		}
		select {
		case c := <-got:
			if c.method != wire.MethodStarted || c.agent != "pj-l-01" || c.root != x.p.Root {
				t.Errorf("serve got %+v, want the started request for pj-l-01 in %s", c, x.p.Root)
			}
		case <-time.After(bound):
			t.Fatal("serve got no started request")
		}
	})
	t.Run("a failed start reports nothing", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.poll = bound // herdr's immediate answer, not the start's poll, ends the attempt
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *herdrtest.Pane) { p.OnStart = "blocked" })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: "go"})
		if ok {
			t.Fatalf("precondition: the start failed:\n%s", out)
		}
		contains(t, failLine(out), "FAIL start failed in w1:p3", "precondition: the start's failure")
		eqInt(t, len(x.startReports()), 0, "no start reported")
	})
	t.Run("a start that returns no pane reports nothing", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "working")
		out, ok, spawns := x.self("w1:p3", false, launch.Request{Agent: "pj-l-01"}, false, nil)
		if !ok || len(spawns) != 1 {
			t.Fatalf("precondition: the detached run scheduled: %v\n%s", spawns, out)
		}
		eqInt(t, len(x.startReports()), 0, "no start reported: the detached run starts the agent")
	})
}

// A refused start report is one line: serve keeps naming the last process's release until a session request or a start
// that lands says otherwise. A serve that is not listening is no warning.
func TestStartReportFailureIsSaid(t *testing.T) {
	t.Parallel()
	t.Run("refused: said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.startErr = errors.New("serve refused it")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "warning: start of pj-l-01 not reported: serve refused it", "said")
	})
	t.Run("no serve listening: nothing said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		var out bytes.Buffer
		l := x.launcher(&out)
		l.Started = nil // the started request to the project root's serve: none listens there
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		got, ok := result(t, &out, line, err)
		if !ok {
			t.Fatal(got)
		}
		lacks(t, got, "not reported", "a serve that is not listening is no warning")
	})
}
