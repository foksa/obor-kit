package app_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app/wire"
)

// T-711: the socket's exit request (obor stop) ends serve gracefully through Options.Exit: refused for a worker, for
// another project's root and by a serve with no Exit (nothing ended); the owner's ends it, replying EXITING.
func TestExitEndsServe(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	exit := func(role, root string) (string, error) {
		c := wire.Call{Root: root, Role: role, CallID: "e1", Note: "obor stop"}
		if role != "" {
			c.Agent = "pj-" + role
		}
		return wire.Verdict(context.Background(), r.sock, wire.MethodExit, c)
	}
	if _, err := exit("", r.p.Root); err == nil || !strings.Contains(err.Error(), "cannot end itself") {
		t.Errorf("no Exit: %v, want refused", err)
	}
	r.stop()

	var ended atomic.Int32
	r.exit = func() { ended.Add(1); r.cancel() }
	r.start()
	if _, err := exit("worker", r.p.Root); err == nil || !strings.Contains(err.Error(), "the lead or the owner") {
		t.Errorf("a worker's exit: %v, want refused", err)
	}
	if _, err := exit("", t.TempDir()); err == nil || !strings.Contains(err.Error(), "no served project") {
		t.Errorf("another root: %v, want refused", err)
	}
	select {
	case <-r.done:
		t.Fatal("serve ended on a refused exit")
	default:
	}
	if reply, err := exit("lead", r.p.Root); err != nil || reply != "EXITING" {
		t.Fatalf("the lead's exit: %q, %v", reply, err)
	}
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not end after EXITING")
	}
	r.cancel = nil
	if n := ended.Load(); n != 1 {
		t.Errorf("Exit called %d times, want 1", n)
	}
}
