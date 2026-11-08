package main

import (
	"net"
	"sync"
	"testing"
	"time"

	"obor/internal/project"
)

// T-719: serve and web follow a new obor binary. These tests replace the package's binaryWatch and selfExec (and
// shorten serve's tick and web's watch), so none of them runs in parallel.

// execRec is a fake selfExec: it records each call and runs at (nil: nothing) at the moment of the exec.
type execRec struct {
	mu    sync.Mutex
	calls [][]string // kit, then the args
	at    func()
}

func (e *execRec) exec(kit string, args []string) error {
	if e.at != nil {
		e.at()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, append([]string{kit}, args...))
	return nil
}

func (e *execRec) got() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// follows replaces binaryWatch with changed (nil: off) and selfExec with rec for the test.
func follows(t *testing.T, changed func() bool, rec *execRec) {
	t.Helper()
	w, x := binaryWatch, selfExec
	binaryWatch = func(project.Env) func() bool { return changed }
	selfExec = rec.exec
	t.Cleanup(func() { binaryWatch, selfExec = w, x })
}

// waitUntil polls ok for up to 10 s.
func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// obor web on a new binary execs the kit's shim as `obor web --listen <the address it bound>`: an unset port pinned
// to the one it got, a set one kept, so the owner's URL stays; the listener is closed by then (the new one binds it).
func TestWebFollowsNewBinaryOnItsPort(t *testing.T) {
	every := webFollowEvery
	webFollowEvery = 10 * time.Millisecond
	t.Cleanup(func() { webFollowEvery = every })
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixed := free.Addr().String()
	free.Close()
	for _, c := range []struct{ name, listen string }{{"an unset port", "127.0.0.1:0"}, {"a set port", fixed}} {
		var addr string
		rec := &execRec{at: func() {}}
		rec.at = func() {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Errorf("%s: the address is not free at the exec: %v", c.name, err)
				return
			}
			ln.Close()
		}
		asked := 0
		follows(t, func() bool { asked++; return asked >= 3 }, &execRec{})
		selfExec = func(kit string, args []string) error {
			if len(args) == 3 {
				addr = args[2]
			}
			return rec.exec(kit, args)
		}
		done := make(chan int, 1)
		go func() { done <- webCmd([]string{"--listen", c.listen}) }()
		select {
		case rc := <-done:
			if rc != 0 {
				t.Errorf("%s: obor web returned %d, want 0 (the fake exec returns)", c.name, rc)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: obor web never followed the new binary", c.name)
		}
		got := rec.got()
		if len(got) != 1 || len(got[0]) != 4 || got[0][1] != "web" || got[0][2] != "--listen" {
			t.Fatalf("%s: exec calls %q, want one of web --listen <addr>", c.name, got)
		}
		host, port, _ := net.SplitHostPort(got[0][3])
		switch {
		case host != "127.0.0.1" || port == "" || port == "0":
			t.Errorf("%s: exec'd with --listen %q, want 127.0.0.1 and the bound port", c.name, got[0][3])
		case c.listen != "127.0.0.1:0" && got[0][3] != c.listen:
			t.Errorf("%s: exec'd with --listen %q, want the set %q", c.name, got[0][3], c.listen)
		}
		if asked < 3 {
			t.Errorf("%s: followed after %d asks, want the third (the one that said changed)", c.name, asked)
		}
	}
}
