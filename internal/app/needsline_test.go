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

// advance moves the fake clock on by d (0: not at all), then waits for two more passes of serve's loop (wake).
func (r *loopRig) advance(d time.Duration) {
	r.t.Helper()
	_, n := app.Counts(r.app)
	r.clock.Add(d)
	r.waitFor("two more passes", func() bool { _, passes := app.Counts(r.app); return passes >= n+2 })
}

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

// againLines is the needs-you lines that repeat fact seq: the lines ending in " (again, <age>)".
func (r *loopRig) againLines(seq int64) []string {
	var out []string
	for _, rec := range r.needsLineFacts() {
		var m app.Message
		if json.Unmarshal(rec.Data, &m) == nil && m.Needs == seq && strings.Contains(m.Line, " (again, ") {
			out = append(out, m.Line)
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

// T-497: the needs-you of a lead line waiting over a day notifies the owner but queues no line to the lead.
func TestWaitingLineNeedsYouQueuesNoLine(t *testing.T) {
	r := lineRig(t, "idle", "idle")
	r.inbox.unrecord("pj-lead")
	r.start()
	if _, err := r.line(app.MethodSend, "s1", app.Call{Note: "hello", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.advance(25 * time.Hour)
	const want = "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: [obor other] hello}"
	r.waitFor("the line's needs-you", func() bool { return r.taskStep(app.MsgTask, want) == 1 })
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	r.advance(0)
	r.advance(0)
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
	case <-time.After(r.patience()):
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
	case <-time.After(30 * time.Second): // a "nothing happens" check: a fixed, generous bound
		t.Error("a concurrent pass recorded (or waited on) a line the first pass had claimed")
	}
	close(release)
	r.waitFor("the line queued", func() bool { return len(r.needsLineFacts()) >= 1 })
	r.advance(0)
	r.advance(0)
	if n := len(r.needsLineFacts()); n != 1 {
		t.Errorf("needs-you lines recorded %d, want exactly 1: %q", n, r.steps())
	}
}

// T-621: a needs-you line still unanswered is posted to the lead again 30 min after it, then every 60, the same line
// with " (again, <its age>)".
func TestNeedsYouLineRepeatsWhileItStays(t *testing.T) {
	r, line := refusedRig(t)
	seq := r.needsYou().Seq
	r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.advance(29 * time.Minute)
	if got := r.againLines(seq); len(got) != 0 {
		t.Fatalf("at 29 min: again lines %q, want none", got)
	}
	r.advance(time.Minute) // 30
	r.waitFor("the first repeat", func() bool { return len(r.againLines(seq)) == 1 })
	if got := r.againLines(seq)[0]; got != line+" (again, 30m)" {
		t.Errorf("at 30 min: line %q, want the same line with (again, 30m)", got)
	}
	r.advance(59 * time.Minute) // 89
	if got := r.againLines(seq); len(got) != 1 {
		t.Fatalf("at 89 min: again lines %q, want still one", got)
	}
	r.advance(time.Minute) // 90
	r.waitFor("the second repeat", func() bool { return len(r.againLines(seq)) == 2 })
	if got := r.againLines(seq)[1]; got != line+" (again, 1h)" {
		t.Errorf("at 90 min: second line %q, want (again, 1h)", got)
	}
}

// T-621: what ends a needs-you ends its repeats: resumed at 45 after one repeat, nothing follows.
func TestNeedsYouLineRepeatsEndWithTheNeedsYou(t *testing.T) {
	r, line := refusedRig(t)
	seq := r.needsYou().Seq
	// The repeat counts from the first line's delivery: the clock moves only once that is recorded (else, under load,
	// the delivery is stamped at 30 and the repeat is not due until 60).
	r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.advance(30 * time.Minute)
	r.waitFor("the first repeat", func() bool { return len(r.againLines(seq)) == 1 })
	r.advance(15 * time.Minute) // 45
	r.disp.set(func(d *dispatchFakes) { delete(d.fail, "prepare") })
	r.mustResume("r1", "", "RESUMED T-1: retry (lane)")
	r.waitFor("the needs-you ended", func() bool { return r.taskState() != "needs-you" })
	r.advance(60 * time.Minute) // 105: the repeat that was due at 90 never comes
	if got := r.againLines(seq); len(got) != 1 {
		t.Errorf("after the resume: again lines %q, want only the one before it", got)
	}
}

// T-621: a replay starts the clock from the needs-you fact's time: a restart at 40 posts the repeat the first line
// owed at 30 (once, not every missed one), and the next is 60 min after that, not 30 after the restart.
func TestNeedsYouLineRepeatsReplayFromTheFactTime(t *testing.T) {
	r, line := refusedRig(t)
	seq := r.needsYou().Seq
	r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.stop()
	r.clock.Add(40 * time.Minute)
	r.start()
	r.waitFor("the repeat after the restart", func() bool { return len(r.againLines(seq)) == 1 })
	if got := r.againLines(seq)[0]; got != line+" (again, 40m)" {
		t.Errorf("after a restart at 40 min: line %q, want (again, 40m)", got)
	}
	r.advance(30 * time.Minute) // 70: a clock restarted at 40 would post its own repeat now
	if got := r.againLines(seq); len(got) != 1 {
		t.Errorf("at 70 min: again lines %q, want one (the clock counts from the fact's time)", got)
	}
	r.advance(30 * time.Minute) // 100: 60 after the repeat
	r.waitFor("the next repeat", func() bool { return len(r.againLines(seq)) == 2 })
	if got := r.againLines(seq)[1]; got != line+" (again, 1h)" {
		t.Errorf("at 100 min: line %q, want (again, 1h)", got)
	}
}

// undeliveredRig is the refused-lane rig with no inbox record for the lead: T-1's needs-you line is queued, not
// delivered. It returns the needs-you's seq.
func undeliveredRig(t *testing.T) (*loopRig, int64) {
	t.Helper()
	r := newDispatchRig(t)
	r.inbox.unrecord("pj-lead")
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return app.Refused{Line: "lane_setup in O-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitFor("the line queued", func() bool { return len(r.waiting()) == 1 && len(r.needsLineFacts()) == 1 })
	return r, r.needsYou().Seq
}

// T-624: while the lead has no inbox record the first line waits undelivered; two repeat intervals later still only
// that line is queued. Once it is delivered at t, the first repeat is due at t + 30 and the next at t + 30 + 60.
func TestNeedsYouLineRepeatWaitsForTheDeliveryOfThePreviousLine(t *testing.T) {
	r, seq := undeliveredRig(t)
	r.advance(2 * time.Hour) // two repeat intervals and more, nothing delivered
	if n, w := len(r.needsLineFacts()), r.waiting(); n != 1 || len(w) != 1 {
		t.Fatalf("after 2 h undelivered: %d lines recorded, waiting %+v; want exactly the first line", n, w)
	}
	r.inbox.record("pj-lead") // delivered at t = 2 h
	r.waitFor("the first line posted", func() bool { return len(r.waiting()) == 0 })
	r.advance(29 * time.Minute)
	if got := r.againLines(seq); len(got) != 0 {
		t.Fatalf("29 min after the delivery: again lines %q, want none", got)
	}
	r.advance(time.Minute) // t + 30
	r.waitFor("the first repeat", func() bool { return len(r.againLines(seq)) == 1 })
	r.waitFor("the repeat posted", func() bool { return len(r.waiting()) == 0 })
	r.advance(59 * time.Minute) // t + 89
	if got := r.againLines(seq); len(got) != 1 {
		t.Fatalf("89 min after the delivery: again lines %q, want one", got)
	}
	r.advance(time.Minute) // t + 90
	r.waitFor("the second repeat", func() bool { return len(r.againLines(seq)) == 2 })
}

// T-624: a repeat waiting undelivered blocks the next one too (the deadline counts from delivery, not queuing), and a
// restart keeps both facts: no line is queued behind a waiting one after the replay.
func TestNeedsYouLineRepeatReplayKeepsTheWait(t *testing.T) {
	r, seq := undeliveredRig(t)
	r.inbox.record("pj-lead")
	r.waitFor("the first line posted", func() bool { return len(r.waiting()) == 0 })
	r.inbox.unrecord("pj-lead")
	r.advance(30 * time.Minute)
	r.waitFor("the first repeat queued", func() bool { return len(r.againLines(seq)) == 1 })
	r.advance(5 * time.Hour)
	r.stop()
	r.start()
	r.advance(0)
	if got, w := r.againLines(seq), r.waiting(); len(got) != 1 || len(w) != 1 {
		t.Errorf("after 5 h and a restart: again lines %q, waiting %+v; want the one repeat, still waiting", got, w)
	}
}
