package app_test

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
)

// advance moves the fake clock on by d (0: not at all), then waits for two more passes of serve's loop (wake).
func (r *loopRig) advance(d time.Duration) {
	r.t.Helper()
	n := app.StreamPasses(r.app) // T-861: passes in a resubscribe's gap skip the project
	r.clock.Add(d)
	r.waitFor("two more passes", func() bool { return app.StreamPasses(r.app) >= n+2 })
}

// needsLineFacts is the message facts of the project's log that announce a needs-you.
func (r *loopRig) needsLineFacts() []event.Record {
	var out []event.Record
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepMessage {
			var m event.Message
			if json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == "needs" {
				out = append(out, rec)
			}
		}
	}
	return out
}

// againLines is the needs-you lines that repeat fact seq: the lines ending in " (again, <age>)".
func (r *loopRig) againLines(seq int64) []string {
	var out []string
	for _, rec := range r.needsLineFacts() {
		var m event.Message
		if json.Unmarshal(rec.Data, &m) == nil && app.NeedsSeq(m.Of) == seq && strings.Contains(m.Line, " (again, ") {
			out = append(out, m.Line)
		}
	}
	return out
}

// refusedRig is the dispatch rig with T-1's lane preparation refused: T-1 has the needs-you "lane: lane_setup in L-01:
// rc 1: boom". It returns that fact's line to the lead, as serve's answers name the commands that answer it.
func refusedRig(t *testing.T) (*loopRig, string) {
	t.Helper()
	r := newDispatchRig(t)
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 fact needs-you{lane: lane_setup in L-01: rc 1: boom}")
	r.waitFor("needs-you fact folded into status", func() bool {
		for _, f := range r.app.Status().Project.Facts {
			if f.Step == event.StepNeedsYou {
				return true
			}
		}
		return false
	})
	var fact event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Step == event.StepNeedsYou {
			fact = f
		}
	}
	if fact.Seq == 0 {
		t.Fatalf("precondition: no needs-you in force: %+v", r.app.Status().Project.Facts)
	}
	ans := r.answerOf(fact)
	if ans == "" {
		t.Fatal("precondition: the needs-you has no answer commands")
	}
	return r, "[obor] needs-you T-1 (L-01): lane: lane_setup in L-01: rc 1: boom \u2192 " + ans
}

// T-497: a needs-you fact is posted to the lead once, besides the owner's notification; later passes and a restart
// post no second line.
func TestNeedsYouIsPostedToTheLeadOnce(t *testing.T) {
	t.Parallel()
	r, line := refusedRig(t)
	r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if n := len(r.needsLineFacts()); n != 1 {
		t.Fatalf("needs-you lines recorded %d, want 1: %q", n, r.steps())
	}
	r.advance(0)
	r.advance(0)
	r.stop()
	r.start()
	r.advance(0)
	r.advance(0)
	if n := r.inbox.count("pj-lead", line); n != 1 {
		t.Errorf("posted %d times across passes and a restart, want once", n)
	}
	if n := len(r.needsLineFacts()); n != 1 {
		t.Errorf("needs-you lines recorded %d after a restart, want 1", n)
	}
}

// T-497: with no inbox record for the lead the line is queued (obor status lists it) and the owner's notification is
// unchanged; a restart queues no second one, and the record posts it once.
func TestNeedsYouLineQueuedWithoutAnInbox(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.inbox.unrecord("pj-lead")
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	// The needs rule's line is the toast's record (T-949): once it is in the log, a restart sends none again.
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
	if n := countReq(r.srv, "notification.show"); n != 1 {
		t.Errorf("notifications %d, want 1", n)
	}
}

// T-497: the needs-you of a lead line waiting over a day queues no line to the lead. Its owner toast went with the
// needs rule's line (T-949): a fact the rule does not line, like this one, toasts nothing.
func TestWaitingLineNeedsYouQueuesNoLine(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.inbox.unrecord("pj-lead")
	r.start()
	if _, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "hello", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.advance(25 * time.Hour)
	want := "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: " + sentLine("other", "hello") +
		" (undelivered: no inbox recorded for pj-lead: its SessionStart hook records one)}" // why it waits (T-746)
	r.waitFor("the line's needs-you", func() bool { return r.taskStep(event.MsgTask, want) == 1 })
	r.advance(0)
	r.advance(0)
	if n, w, notes := len(r.needsLineFacts()), r.waiting(), countReq(r.srv, "notification.show"); n != 0 ||
		len(w) != 1 || !strings.HasPrefix(w[0].Line, "[obor other]") || notes != 0 {
		t.Errorf("needs-you lines %d, waiting %+v, notifications %d; want none, only the original line, none",
			n, w, notes)
	}
}
