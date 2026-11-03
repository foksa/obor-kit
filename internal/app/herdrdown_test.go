package app_test

import (
	"strings"
	"testing"
	"time"
)

// T-596: herdr gone never ends serve. Its socket dies under the stream: serve stays up, logs one line when herdr
// goes and one when it is back (however many retries in between), takes a dispatch over its own socket meanwhile
// (prepared only once herdr is back: the merge loop needs a snapshot), and the fresh snapshot drives that dispatch.
func TestServeWaitsForHerdr(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Waits\n\nDifficulty: medium\n")
	if out := r.out.String(); !strings.Contains(out, "herdr: connected") || strings.Contains(out, "herdr: gone") {
		t.Fatalf("precondition: not connected, or gone already:\n%s", out)
	}

	r.srv.Die()
	r.waitFor("the gone line", func() bool { return strings.Contains(r.out.String(), "herdr: gone") })
	r.waitFor("disconnected", func() bool { return !r.app.Status().Connected })
	if reply, err := r.dispatch("d1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("dispatch with herdr down: %q, %v", reply, err)
	}
	subs := r.srv.Subscriptions()
	time.Sleep(300 * time.Millisecond) // several retries (5 ms doubling to 40 ms)
	select {
	case <-r.done:
		t.Fatalf("serve ended with herdr down:\n%s", r.out)
	default:
	}
	if p, _ := r.disp.applied(); p != 0 {
		t.Errorf("lane prepared with herdr down: %d prepares", p)
	}
	if n := strings.Count(r.out.String(), "herdr: gone"); n != 1 {
		t.Errorf("%d gone lines, want 1:\n%s", n, r.out)
	}
	if len(subs) != 0 {
		t.Errorf("subscriptions while herdr is dead: %v", subs)
	}

	r.srv.Revive(t)
	r.waitFor("the back line", func() bool { return strings.Contains(r.out.String(), "herdr: back") })
	r.waitStep("1 result work")
	r.waitJob("pj-l-01", "T-1/1/work")
	out := r.out.String()
	if n, m := strings.Count(out, "herdr: gone"), strings.Count(out, "herdr: back"); n != 1 || m != 1 {
		t.Errorf("%d gone and %d back lines, want one each:\n%s", n, m, out)
	}
	if !strings.Contains(out, "stream ended") {
		t.Errorf("the gone line does not say the stream ended:\n%s", out)
	}
	if p, starts := r.disp.applied(); p != 1 || starts != 1 {
		t.Errorf("prepares %d, starts %d after herdr came back; want 1 each", p, starts)
	}
}

// Herdr down when serve starts: serve waits for it (one gone line), and works once it is up.
func TestServeStartsWithHerdrDown(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.srv.Die()
	go func() {
		time.Sleep(300 * time.Millisecond)
		r.srv.Revive(t)
	}()
	r.start() // returns after the first check: herdr is back
	out := r.out.String()
	if n, m := strings.Count(out, "herdr: gone"), strings.Count(out, "herdr: back"); n != 1 || m != 1 {
		t.Errorf("%d gone and %d back lines, want one each:\n%s", n, m, out)
	}
	if strings.Index(out, "herdr: gone") > strings.Index(out, "herdr: connected") {
		t.Errorf("connected before gone:\n%s", out)
	}
}
