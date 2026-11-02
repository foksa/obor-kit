package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// T-719: serve, monitor and web follow a new obor binary. These tests replace the package's binaryWatch and selfExec
// (and shorten serve's tick and web's watch), so none of them runs in parallel.

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

// A serve in a pane (no launchd agent of its own) on a new binary: on its tick, idle, it ends as at a signal and then
// execs the kit's shim as `obor serve`, same process: its socket gone by then, its flock still held until the exec
// releases it (close-on-exec); a serve started after it takes the lock.
func TestServePaneExecsOnNewBinary(t *testing.T) {
	p := servedProject(t)
	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	srv := herdrtest.Serve(t, f)
	bin := t.TempDir() // no launchd agent for this project (darwin's launchctl answers "not found")
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\nexit 113\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	kit := t.TempDir()
	if err := os.MkdirAll(filepath.Join(kit, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kit, "bin", "obor"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := p.Env("HERDR_SOCKET_PATH="+srv.Path, "OBOR_PLUGIN_ROOT="+kit)
	sock := wire.SocketPath(p.Root)
	var changed sync.Mutex
	isNew := false
	var sockLeft, lockHeld bool
	rec := &execRec{at: func() {
		_, err := os.Stat(sock)
		sockLeft = err == nil
		ok, _ := project.NewLock(app.FlockFile(p.Root)).Try()
		lockHeld = !ok
	}}
	follows(t, func() bool { changed.Lock(); defer changed.Unlock(); return isNew }, rec)
	tick := serveTick
	serveTick = 20 * time.Millisecond
	t.Cleanup(func() { serveTick = tick })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := make(chan int, 1)
	go func() { rc <- serve(ctx, nil, env, p.Root) }()
	waitUntil(t, "serve answers", func() bool {
		ask, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		_, err := wire.Ask(ask, sock)
		return err == nil
	})
	time.Sleep(100 * time.Millisecond) // several ticks: no binary change, no exec
	if n := len(rec.got()); n != 0 {
		t.Fatalf("precondition: no exec while the binary is the same: %d", n)
	}
	changed.Lock()
	isNew = true
	changed.Unlock()
	select {
	case code := <-rc:
		if code != 0 {
			t.Errorf("serve returned %d after its exec, want 0 (the fake exec returns)", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve never restarted on the new binary")
	}
	if got, want := rec.got(), [][]string{{kit, "serve"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("exec calls %q, want %q", got, want)
	}
	if sockLeft || !lockHeld {
		t.Errorf("at the exec: socket left %v, flock held %v; want the socket gone and the flock held", sockLeft, lockHeld)
	}
	// The exec'd serve: it takes the lock and serves again (here: this process, the watch off).
	follows(t, nil, &execRec{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	rc2 := make(chan int, 1)
	go func() { rc2 <- serve(ctx2, nil, env, p.Root) }()
	waitUntil(t, "the next serve answers", func() bool {
		ask, stop := context.WithTimeout(ctx2, time.Second)
		defer stop()
		_, err := wire.Ask(ask, sock)
		return err == nil
	})
	cancel2()
	if code := <-rc2; code != 0 {
		t.Errorf("the next serve returned %d, want 0", code)
	}
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

// The monitor asks for a new binary at most every 2 s; on one its run ends, and follow leaves the alternate screen
// and closes the store before it execs the kit's shim as `obor monitor`.
func TestMonitorFollowsNewBinary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	p := testkit.New(t)
	p.Config()
	m := monitorOf(t, p, now, 24)
	asks := 0
	isNew := false
	m.binary = func() bool { asks++; return isNew }
	var buf bytes.Buffer
	s := &screen{w: nopFlush{&buf}, tty: true}
	s.enter()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if m.run(ctx, s, nil) {
		t.Fatal("run ended for a new binary while there is none")
	}
	if asks != 1 {
		t.Errorf("asked %d times with the clock still, want 1 (at most every %s)", asks, monitorEvery)
	}
	isNew = true
	now = now.Add(monitorEvery)
	m.now = func() time.Time { return now }
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !m.run(ctx, s, nil) {
		t.Fatal("run did not end for the new binary")
	}
	var order []string
	rec := &execRec{at: func() {
		order = append(order, "exec")
		if !strings.HasSuffix(buf.String(), altOff) {
			t.Errorf("at the exec the alternate screen is not left: %q", buf.String())
		}
	}}
	follows(t, nil, rec)
	if rc := m.follow(s, "/kit"); rc != 0 {
		t.Errorf("follow returned %d, want 0 (the fake exec returns)", rc)
	}
	if got, want := rec.got(), [][]string{{"/kit", "monitor"}}; !reflect.DeepEqual(got, want) || len(order) != 1 {
		t.Errorf("exec calls %q, want %q", got, want)
	}
	if m.db != nil {
		t.Error("the store is still open after follow")
	}
	n := strings.Count(buf.String(), altOff)
	s.leave() // monitorCmd's deferred leave: nothing more
	if strings.Count(buf.String(), altOff) != n || n != 1 {
		t.Errorf("left the alternate screen %d times, then %d; want once", n, strings.Count(buf.String(), altOff))
	}
}

// nopFlush is a frameWriter over a buffer.
type nopFlush struct{ *bytes.Buffer }

func (nopFlush) Flush() error { return nil }

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
