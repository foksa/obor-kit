package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"obor/internal/app"
)

// T-660: a failure or needs-you an error would record is not recorded when the error is serve's own cancellation (its
// ctx ended: a restart, a stop). serve logged it as "shutting down: <what> not recorded" instead, and the intent stays
// pending, so the next serve's replay runs it as after a crash.

// msgFailed is how many delivery failures of the project's lines are in the log.
func (r *loopRig) msgFailed() int {
	n := 0
	for _, rec := range r.records() {
		if rec.Task == app.MsgTask && rec.Kind == app.KindFact && rec.Step == app.StepFailed {
			n++
		}
	}
	return n
}

// A line's delivery cut by serve's stop (its ctx ends mid-post, before a byte went out; RV: T-660's own
// msg failed{deliver.<seq>: … operation was canceled}) records no fact failed: nothing went out, nothing was refused.
// The intent stays pending and the next serve's replay delivers the line once.
func TestShutdownDeliveryNotRecorded(t *testing.T) {
	r := lineRig(t, "idle", "idle")
	r.inbox.cut(true) // the next post alone waits for serve's ctx and fails as a cancellation
	r.start()
	line := sentLine("other", "hi")
	if _, err := r.line(app.MethodSend, "s1", app.Call{Note: "hi", From: "other"}); err != nil {
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
}

// A pass cut by serve's stop (here the checkout reports context.Canceled as serve is cancelled: T-660's own
// loop needs-you{loop: … context canceled}) records no loop needs-you and no resumed after it: serve's own
// cancellation is not a failure of the loop. A real error still records as before (T-425).
func TestShutdownLoopNotRecorded(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	set := func(err error) {
		r.mu.Lock()
		r.checkoutErr = err
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

	set(errors.New("main checkout is on side, not main"))
	r.waitStep("0 fact needs-you{loop: main checkout is on side, not main}")
}
