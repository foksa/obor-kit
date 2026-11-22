package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// A needs-you fact is posted to the lead's inbox once, besides the owner's notification; later passes and a restart
// post no second line (how many lines the rule records is TestNeedsRule's).
func TestNeedsYouIsPostedToTheLeadOnce(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r, line := refusedRig(t)
		r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		r.waitFor("the notification", func() bool { return len(r.notifications()) == 1 })
		r.advance(0)
		r.advance(0)
		r.stop()
		r.start()
		r.advance(0)
		r.advance(0)
		if n := r.inbox.count("pj-lead", line); n != 1 {
			t.Errorf("posted %d times across passes and a restart, want once", n)
		}
	})
}

// With no inbox record for the lead the line is queued (obor status lists it) and the owner's notification is
// unchanged; a restart queues no second one, and the record posts it once.
func TestNeedsYouLineQueuedWithoutAnInbox(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.inbox.unrecord("pj-lead")
		r.disp.set(func(d *dispatchFakes) {
			d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
		})
		r.start()
		r.mustDispatch("d1")
		r.waitFor("the notification", func() bool { return len(r.notifications()) == 1 })
		// The needs rule's line is the toast's record: once it is in the log, a restart sends none again.
		r.waitFor("the needs line recorded", func() bool { return len(r.needsLineFacts()) == 1 })
		r.waitFor("the line queued", func() bool { return len(r.waiting()) == 1 })
		w := r.waiting()[0]
		if w.To != "pj-lead" || !strings.HasPrefix(w.Line, "[obor] needs-you T-1 (L-01): lane: lane_setup in L-01: rc 1: boom") {
			t.Fatalf("waiting %+v, want the needs-you line to pj-lead", w)
		}
		r.stop()
		r.start()
		r.advance(0)
		if n, ww := len(r.needsLineFacts()), r.waiting(); len(ww) != 1 || n != 1 {
			t.Errorf("after a restart: waiting %+v, %d facts; want one line queued once", ww, n)
		}
		if n := r.inbox.count("pj-lead", w.Line); n != 0 {
			t.Errorf("posted %d times with no record", n)
		}
		r.inbox.record("pj-lead")
		r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", w.Line) == 1 })
		if n := len(r.notifications()); n != 1 {
			t.Errorf("notifications %d, want 1", n)
		}
	})
}

// The needs-you of a lead line waiting over a day toasts nothing and leaves only the original line queued. (That the
// rule lines no needs-you of the project's own lines is TestNeedsRule's.)
func TestWaitingLineNeedsYouQueuesNoLine(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "working") // the reviewer works: no done reminder among the lines this test counts
		r.inbox.unrecord("pj-lead")
		r.start()
		if _, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.advance(25 * time.Hour)
		want := "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: " + sentLine("other", "hello") +
			" (undelivered: no inbox recorded for pj-lead: its hooks hand it its lines at its next prompt or turn end)}" // why it waits
		r.waitFor("the line's needs-you", func() bool { return r.taskStep(event.MsgTask, want) == 1 })
		r.advance(0)
		r.advance(0)
		if w, notes := r.waiting(), len(r.notifications()); len(w) != 1 ||
			!strings.HasPrefix(w[0].Line, "[obor other]") || notes != 0 {
			t.Errorf("waiting %+v, notifications %d; want only the original line, none", w, notes)
		}
	})
}
