package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/herdr"
	"obor/internal/testkit"
)

// fakeServe answers a project's app socket (<root>/.obor/state/serve.sock): status with st, every other request with
// reply (or err), and records the requests.
type fakeServe struct {
	mu      sync.Mutex
	calls   []wire.Call
	methods []string // the method of each call
	n       int      // requests of any method
	ln      net.Listener
}

// stop closes the socket (its file removed): serve down.
func (f *fakeServe) stop() { _ = f.ln.Close() }

// startFakeServe is a fakeServe on root's socket, and a fresh state home.
func startFakeServe(t *testing.T, root string, st wire.Status, reply, errText string) (home string, f *fakeServe) {
	t.Helper()
	return t.TempDir(), startFakeServeAt(t, wire.SocketPath(root), st, reply, errText)
}

// startFakeServeAt is startFakeServe's server listening at sock (a previous one there is no longer reached).
func startFakeServeAt(t *testing.T, sock string, st wire.Status, reply, errText string) *fakeServe {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeServe{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, _ := bufio.NewReader(c).ReadBytes('\n')
			var req struct {
				Method string     `json:"method"`
				Call   *wire.Call `json:"call"`
			}
			_ = json.Unmarshal(b, &req)
			f.mu.Lock()
			f.n++
			if req.Call != nil {
				f.calls, f.methods = append(f.calls, *req.Call), append(f.methods, req.Method)
			}
			f.mu.Unlock()
			ans := map[string]any{"reply": reply, "error": errText}
			if req.Method == "status" {
				ans = map[string]any{"status": st}
			}
			out, _ := json.Marshal(ans)
			_, _ = c.Write(append(out, '\n'))
			c.Close()
		}
	}()
	return f
}

func (f *fakeServe) requests() (calls []wire.Call, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wire.Call(nil), f.calls...), f.n
}

// servedProject is a project whose merge loop is obor serve's (an integrator role), with lane L-01; edits change the
// config further.
func servedProject(t *testing.T, edits ...func(map[string]any)) *testkit.Project {
	t.Helper()
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "task/T-01")
	p.Config(append([]func(map[string]any){testkit.Set("roles.integrator", "")}, edits...)...)
	return p
}

// T-408, removed by v0.10.0 and unknown since v0.12: a config that still sets dispatch.serve does not load, so obor
// config validate FAILs on it and obor dispatch asks serve nothing.
func TestDispatchObsoleteSwitch(t *testing.T) {
	t.Parallel()
	p := servedProject(t, testkit.Set("dispatch.serve", false))
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "DISPATCHED T-07 → L-01 (queued)", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	if rc, _, stderr := run(t, p.Root, env, "config", "validate"); rc != 1 || !strings.Contains(stderr, `unknown field "dispatch"`) {
		t.Errorf("validate: exit %d, stderr %q; want an unknown field FAIL", rc, stderr)
	}
	if rc, _, _ := run(t, p.Root, env, "dispatch", "T-07"); rc == 0 {
		t.Errorf("dispatch: exit 0 with an unloadable config")
	}
	if calls, _ := serve.requests(); len(calls) != 0 {
		t.Errorf("serve's requests %+v, want none", calls)
	}
}

// serve's merge loop gets the workflow workflow.kind selects (T-1029: scratch-ff, the default) and the dispatch's spec
// reader and kit; a kind serve has no implementation of fails the loop's construction.
func TestMergeLoopWorkflowWiring(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	for _, kind := range []string{"", "scratch-ff"} {
		if kind != "" {
			p.Config(testkit.Set("workflow.kind", kind))
		}
		l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})(p.Root)
		if err != nil {
			t.Fatalf("kind %q: %v", kind, err)
		}
		wf, ok := l.Workflow.(checks.ScratchFF)
		if !ok || wf.Review == nil || l.Specs == nil || l.Kit == "" {
			t.Errorf("kind %q: Workflow %T, Specs %v, Kit %q; want scratch-ff with its review git", kind, l.Workflow, l.Specs, l.Kit)
		}
	}
	p.Config(testkit.Set("workflow.kind", "x"))
	if _, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})(p.Root); err == nil ||
		!strings.Contains(err.Error(), "serve knows scratch-ff") {
		t.Errorf("kind x: %v; want refused naming the known kinds", err)
	}
}
