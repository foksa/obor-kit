package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/store"
	"obor/internal/testkit"
)

// sentCall is a request's call as the fake serve reads it off the socket: every key a CLI request sends (the
// wire.*Req types share them, T-1055), one field per key, named as the key, so a test checks the bytes serve gets.
type sentCall struct {
	wire.Envelope
	Task    string              `json:"task"`
	Job     string              `json:"job"`
	Note    string              `json:"note"`
	To      string              `json:"to"`
	Lane    string              `json:"lane"`
	Types   []string            `json:"types"`
	Force   bool                `json:"force"`
	After   []string            `json:"after"`
	Accept  []string            `json:"accept"`
	Discard bool                `json:"discard"`
	Report  string              `json:"report"`
	Spec    string              `json:"spec"`
	Changes []wire.ConfigChange `json:"changes"`
	Until   int64               `json:"until"`
	Model   string              `json:"model"`
	Clear   bool                `json:"clear"`
	From    string              `json:"from"`
	Fresh   bool                `json:"fresh"`
	Error   string              `json:"error"`
	Details string              `json:"details"`
	Ended   bool                `json:"ended"`
	Session *store.Session      `json:"session_fact"`
	Seq     int64               `json:"seq"`
}

// fakeServe answers a project's app socket (<root>/.obor/state/serve.sock): status with st, every other request with
// reply (or err), and records the requests.
type fakeServe struct {
	mu      sync.Mutex
	calls   []sentCall
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
				Method string    `json:"method"`
				Call   *sentCall `json:"call"`
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

func (f *fakeServe) requests() (calls []sentCall, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentCall(nil), f.calls...), f.n
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

// triageProject is a served project with roles.triage configured (axis-alies' shape): its triage agent is pj-triage.
func triageProject(t *testing.T) *testkit.Project {
	t.Helper()
	return servedProject(t, testkit.Set("roles.triage", "opus"))
}
