package app

import (
	"testing"

	"obor/internal/app/event"
)

// The open-work read serve's restore passes (launch.Restorer.OpenWork): a role agent with an undelivered brief has
// work open; the finished line that ends the brief closes it.
func TestOpenWorkFollowsTheBrief(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect()
	if open, err := h.a.OpenWork("pj-rev", "reviewer"); err != nil || open {
		t.Fatalf("open work before its brief = %v, %v; want none", open, err)
	}
	h.brief("pj-rev", "[obor pj-lead] review this")
	if q := h.proj().out.Queued(); len(q) != 1 || q[0].Msg().To != "pj-rev" {
		t.Fatalf("precondition: the brief's line waits undelivered: %d lines", len(q))
	}
	if open, err := h.a.OpenWork("pj-rev", "reviewer"); err != nil || !open {
		t.Fatalf("open work with an undelivered brief = %v, %v; want open", open, err)
	}
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
		event.Message{To: "pj-lead", From: "obor", Ends: "pj-rev", Line: "[obor] pj-rev finished"}) {
		t.Fatal("the finished line was not recorded")
	}
	if open, err := h.a.OpenWork("pj-rev", "reviewer"); err != nil || open {
		t.Fatalf("open work after its brief finished = %v, %v; want none", open, err)
	}
}
