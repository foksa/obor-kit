package app_test

import (
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// A line waiting longer than 24 h (here: the lead has no inbox record, its lines held for its hooks) is the project's needs-you (a
// notification, obor status's needs-you with no command to answer it); posted at last, it ends (resumed).
func TestSendWaitingADayNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.inbox.unrecord("pj-lead")
		r.start()
		if _, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.later(23 * time.Hour)
		want := "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: " + sentLine("other", "hello") +
			" (undelivered: " + noRecordWhy + ")}" // why it waits
		if n := r.taskStep(event.MsgTask, want); n != 0 {
			t.Fatalf("a needs-you before 24 h: %q", r.steps())
		}
		r.later(2 * time.Hour)
		r.waitFor("the needs-you", func() bool { return r.taskStep(event.MsgTask, want) == 1 })
		facts := r.msgFacts()
		if len(facts) != 1 || facts[0].Step != event.StepNeedsYou || r.answerOf(facts[0]) != "" {
			t.Errorf("facts %+v, want the line's needs-you", facts)
		}
		r.passes()
		if n := r.taskStep(event.MsgTask, want); n != 1 {
			t.Errorf("needs-you recorded %d times, want once", n)
		}
		r.inbox.record("pj-lead")
		// The records the assertions below read: the line posted, the resumed fact in the log and folded out of the facts
		// in force (the log and the status fold are not updated in one step).
		r.waitFor("resumed", func() bool {
			return r.taskStep(event.MsgTask, "fact resumed") == 1 && r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 &&
				len(r.msgFacts()) == 0
		})
		if r.inbox.count("pj-lead", sentLine("other", "hello")) != 1 || len(r.msgFacts()) != 0 {
			t.Errorf("posted %d, facts %+v; want posted once, none in force", r.inbox.count("pj-lead", sentLine("other", "hello")),
				r.msgFacts())
		}
	})
}
