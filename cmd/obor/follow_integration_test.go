//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
)

// A serve in a pane (no launchd agent of its own) on a new binary: on its tick, idle, it ends as at a signal and then
// execs the kit's shim as `obor serve`, same process: its socket gone by then, its flock still held until the exec
// releases it (close-on-exec); a serve started after it takes the lock.
func TestServePaneExecsOnNewBinary(t *testing.T) {
	p := servedGitProject(t)
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
