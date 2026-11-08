package app

import (
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/store"
)

// T-1026: obor review --after T-1, T-1 a spec stored and never dispatched, then dropped (T-1016's record: the drop at
// its rev): an open request ends with the line a dropped dispatched task gives, also after a replay, and a new request
// --after T-1 is refused as for a dropped task.
func TestReviewAfterASpecOnlyDrop(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	if _, _, err := store.SetSpec(r.p.log.DB(), "T-1", "# T-1 — Stored\n\nDifficulty: medium\n", "", "lead", r.now); err != nil {
		t.Fatal(err)
	}
	if got := r.mustCall("r1", []string{"T-1"}, "", false); got != "REVIEW REQUESTED (after T-1)" {
		t.Fatalf("reply %q", got)
	}
	r.pass()
	if r.p.folded.review == nil || len(r.agents.launches) != 0 {
		t.Fatalf("precondition: request %v, launches %+v; want it open, waiting on T-1", r.p.folded.review, r.agents.launches)
	}
	r.a.record(r.p, event.Record{Task: "T-1", Step: event.StepDropped, Kind: event.KindFact},
		event.FactData{Reason: "superseded", Merge: true, Rev: 1})
	r.pass()
	r.pass()
	if lines := r.leadLines(); len(lines) != 1 || lines[0] != "[obor] review request ended: T-1 dropped" {
		t.Errorf("lead lines %q, want the request ended on T-1's drop", lines)
	}
	if r.p.folded.review != nil || len(r.agents.launches) != 0 {
		t.Errorf("request %v, launches %+v; want ended, none", r.p.folded.review, r.agents.launches)
	}
	r.reload()
	r.pass()
	if lines := r.leadLines(); len(lines) != 1 || r.p.folded.review != nil {
		t.Errorf("after a replay: lines %q, request %v", lines, r.p.folded.review)
	}
	if _, err := r.call("r2", []string{"T-1"}, "", false); err == nil || !strings.Contains(err.Error(), "review: T-1 dropped already") {
		t.Errorf("a new request --after the dropped T-1: %v; want refused", err)
	}
}
