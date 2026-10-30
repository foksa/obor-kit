package app_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
)

// needsLineFacts is the message facts of the project's log that announce a needs-you.
func (r *loopRig) needsLineFacts() []app.Record {
	var out []app.Record
	for _, rec := range r.records() {
		if rec.Task == app.MsgTask && rec.Kind == app.KindFact && rec.Step == app.StepMessage {
			var m app.Message
			if json.Unmarshal(rec.Data, &m) == nil && m.Needs != 0 {
				out = append(out, rec)
			}
		}
	}
	return out
}

// refusedRig is the dispatch rig with T-1's lane preparation refused: T-1 has the needs-you "lane: lane_setup in O-01:
// rc 1: boom". It returns that fact's line to the lead, as serve's answers name the commands that answer it.
func refusedRig(t *testing.T) (*loopRig, string) {
	t.Helper()
	r := newDispatchRig(t)
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return app.Refused{Line: "lane_setup in O-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 fact needs-you{lane: lane_setup in O-01: rc 1: boom}")
	r.waitFor("needs-you fact folded into status", func() bool {
		for _, f := range r.app.Status().Project.Facts {
			if f.Step == app.StepNeedsYou {
				return true
			}
		}
		return false
	})
	var fact app.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Step == app.StepNeedsYou {
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
	return r, "[obor] needs-you T-1 (O-01): lane: lane_setup in O-01: rc 1: boom \u2192 " + ans
}

// T-497: a needs-you fact is posted to the lead once, besides the owner's notification; later passes and a restart
// post no second line.
func TestNeedsYouIsPostedToTheLeadOnce(t *testing.T) {
	r, line := refusedRig(t)
	r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if n := len(r.needsLineFacts()); n != 1 {
		t.Fatalf("needs-you lines recorded %d, want 1: %q", n, r.steps())
	}
	r.passes()
	r.passes()
	r.stop()
	r.start()
	r.passes()
	r.passes()
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
	r := newDispatchRig(t)
	r.inbox.unrecord("pj-lead")
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return app.Refused{Line: "lane_setup in O-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	r.waitFor("the line queued", func() bool { return len(r.waiting()) == 1 })
	w := r.waiting()[0]
	if w.To != "pj-lead" || !strings.HasPrefix(w.Line, "[obor] needs-you T-1 (O-01): lane: lane_setup in O-01: rc 1: boom") {
		t.Fatalf("waiting %+v, want the needs-you line to pj-lead", w)
	}
	r.stop()
	r.start()
	r.passes()
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

// T-497: the needs-you of a lead line waiting over a day notifies the owner but queues no line to the lead.
func TestWaitingLineNeedsYouQueuesNoLine(t *testing.T) {
	r := lineRig(t, "idle", "idle")
	r.inbox.unrecord("pj-lead")
	r.start()
	if _, err := r.line(app.MethodSend, "s1", app.Call{Note: "hello", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.later(25 * time.Hour)
	const want = "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: [obor other] hello}"
	r.waitFor("the line's needs-you", func() bool { return r.taskStep(app.MsgTask, want) == 1 })
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	r.passes()
	r.passes()
	if n, w := len(r.needsLineFacts()), r.waiting(); n != 0 || len(w) != 1 || !strings.HasPrefix(w[0].Line, "[obor other]") {
		t.Errorf("needs-you lines %d, waiting %+v; want none and only the original line", n, w)
	}
}

// T-497: needsLines passes running at once (the merge loop's and the watchdog's) queue one line per needs-you: while
// the first one's record is still being written, the others queue nothing; a refused record is tried again.
func TestNeedsYouLineQueuedOnceUnderConcurrentPasses(t *testing.T) {
	r := newDispatchRig(t)
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return app.Refused{Line: "lane_setup in O-01: rc 1: boom"} }
	})
	r.start()
	var held, release = make(chan struct{}), make(chan struct{})
	var first, refused sync.Once
	app.Refuse(r.app, r.p.Root, func(rec app.Record) error {
		if rec.Task != app.MsgTask || rec.Step != app.StepMessage {
			return nil
		}
		var err error
		refused.Do(func() { err = errors.New("log refused") }) // the first try fails, the second is held
		if err != nil {
			return err
		}
		first.Do(func() { close(held); <-release })
		return nil
	})
	r.mustDispatch("d1")
	r.waitStep("1 fact needs-you{lane: lane_setup in O-01: rc 1: boom}")
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		t.Fatalf("precondition: no line reached its record: %q", r.steps())
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); app.NeedsLines(r.app) }()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done: // none of them tried to record the claimed fact's line
	case <-time.After(5 * time.Second):
		t.Error("a concurrent pass recorded (or waited on) a line the first pass had claimed")
	}
	close(release)
	r.waitFor("the line queued", func() bool { return len(r.needsLineFacts()) >= 1 })
	r.passes()
	r.passes()
	if n := len(r.needsLineFacts()); n != 1 {
		t.Errorf("needs-you lines recorded %d, want exactly 1: %q", n, r.steps())
	}
}
