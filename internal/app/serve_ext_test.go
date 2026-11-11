package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
)

// herdr gone never ends serve. Its socket dies under the stream: serve stays up, logs one line when herdr goes and
// one when it is back (however many retries in between), takes a dispatch over its own socket meanwhile (prepared only
// once herdr is back: the merge loop needs a snapshot), and the fresh snapshot drives that dispatch.
func TestServeWaitsForHerdr(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.start()
		r.setSpec("s1", "# T-1 — Waits\n\nDifficulty: medium\n")
		if out := r.out.String(); !strings.Contains(out, "terminal: connected") || strings.Contains(out, "terminal: gone") {
			t.Fatalf("precondition: not connected, or gone already:\n%s", out)
		}

		r.srv.Die()
		r.waitFor("the gone line", func() bool { return strings.Contains(r.out.String(), "terminal: gone") })
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
		if n := strings.Count(r.out.String(), "terminal: gone"); n != 1 {
			t.Errorf("%d gone lines, want 1:\n%s", n, r.out)
		}
		if len(subs) != 0 {
			t.Errorf("subscriptions while herdr is dead: %v", subs)
		}

		r.srv.Revive(t)
		r.waitFor("the back line", func() bool { return strings.Contains(r.out.String(), "terminal: back") })
		r.waitStep("1 result work")
		r.waitJob("pj-l-01", "T-1/1/work")
		out := r.out.String()
		if n, m := strings.Count(out, "terminal: gone"), strings.Count(out, "terminal: back"); n != 1 || m != 1 {
			t.Errorf("%d gone and %d back lines, want one each:\n%s", n, m, out)
		}
		if !strings.Contains(out, "stream ended") {
			t.Errorf("the gone line does not say the stream ended:\n%s", out)
		}
		if p, starts := r.disp.applied(); p != 1 || starts != 1 {
			t.Errorf("prepares %d, starts %d after herdr came back; want 1 each", p, starts)
		}
	})
}

// Herdr down when serve starts: serve waits for it (one gone line), and works once it is up.
func TestServeStartsWithHerdrDown(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.srv.Die()
		r.launch()
		// Revive only once serve has logged herdr gone: serve's replay alone takes its time, so a fixed wait could
		// connect to a live herdr and the gone/back lines never happen.
		out := r.out
		go func() {
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				if strings.Contains(out.String(), "terminal: gone") {
					r.srv.Revive(t)
					return
				}
			}
		}()
		r.firstCheck() // returns after the first check: herdr is back
		got := out.String()
		if n, m := strings.Count(got, "terminal: gone"), strings.Count(got, "terminal: back"); n != 1 || m != 1 {
			t.Errorf("%d gone and %d back lines, want one each:\n%s", n, m, got)
		}
		if strings.Index(got, "terminal: gone") > strings.Index(got, "terminal: connected") {
			t.Errorf("connected before gone:\n%s", got)
		}
	})
}

// A line's delivery cut by serve's stop (its ctx ends mid-post, before a byte went out): nothing went out, nothing
// was refused, so no fact failed is recorded. The intent stays pending and the next serve's replay delivers the line
// once.
func TestShutdownDeliveryNotRecorded(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.inbox.cut(true) // the next post alone waits for serve's ctx and fails as a cancellation
		r.start()
		line := sentLine("other", "hi")
		if _, err := r.line("s1", &wire.SendReq{Text: "hi", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the post tried", func() bool { return r.inbox.tries("pj-lead", line) == 1 })
		r.stop()

		if n := r.msgFailed(); n != 0 {
			t.Fatalf("the cut post recorded %d failed facts: %q", n, r.steps())
		}
		if out := r.out.String(); !strings.Contains(out, "shutting down: msg failed{deliver.") ||
			!strings.Contains(out, ": context canceled} not recorded") {
			t.Errorf("the cut post's not-recorded line is missing:\n%s", out)
		}
		if w := r.waiting(); len(w) != 1 || w[0].Line != line {
			t.Fatalf("after the cut: waiting %+v, want the line still queued", w)
		}
		if got := r.lineResults(); len(got) != 0 {
			t.Fatalf("after the cut: delivery results %q, want none (the intent is pending)", got)
		}

		r.start() // the replay: the pending delivery runs again
		r.waitFor("the line posted once", func() bool { return len(r.waiting()) == 0 })
		if n := r.inbox.count("pj-lead", line); n != 1 {
			t.Errorf("posted %d times across the restart, want once", n)
		}
		if got := r.lineResults(); len(got) != 1 || got[0] != "sent" {
			t.Errorf("delivery results %q, want one sent", got)
		}
	})
}

// A pass cut by serve's stop (here the loop's adapters report context.Canceled as serve is cancelled) records no loop
// needs-you and no resumed after it: serve's own cancellation is not a failure of the loop. A real error still
// records.
func TestShutdownLoopNotRecorded(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		set := func(err error) {
			r.mu.Lock()
			r.loopErr = err
			r.mu.Unlock()
		}
		set(context.Canceled)
		r.work("feature.txt", "feature\n")
		r.start()
		r.passes()
		if r.hasPrefix("0 fact needs-you{loop:") || r.has("0 fact resumed") {
			t.Fatalf("a pass cut by serve's stop recorded a loop fact: %q", r.steps())
		}
		if out := r.out.String(); !strings.Contains(out, "shutting down: loop needs-you{loop: context canceled} not recorded") {
			t.Errorf("the cut pass's not-recorded line is missing:\n%s", out)
		}

		set(errors.New("config: bad json"))
		r.waitStep("0 fact needs-you{loop: config: bad json}")
	})
}
