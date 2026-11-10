package view

import (
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// A blocked worker's needs-you carries the pane's question and where a person answers it, in the needs-you view and
// the task detail; a needs-you without a question carries its reason alone.
func TestNeedsYouCarriesABlockedPanesQuestion(t *testing.T) {
	ps := wire.ProjectStatus{
		Facts: []event.Record{
			{Seq: 1, Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact,
				Data: []byte(`{"lane":"L-01","pane":"w1:p1","reason":"blocked","question":"Do you want to proceed?\n  1. Yes\n  2. No"}`)},
			{Seq: 2, Task: "T-2", Step: event.StepNeedsYou, Kind: event.KindFact,
				Data: []byte(`{"lane":"L-02","reason":"asks: may I edit x.go?"}`)},
		},
		Answers: map[int64]string{1: `obor resume T-1 "<answer>"`},
	}
	v := Project{NeedsYou: needsYou(ps)}
	if n := v.NeedsYou; len(n) != 2 {
		t.Fatalf("needs-you %+v, want T-1 and T-2", n)
	}
	blocked, asked := v.NeedsYou[0], v.NeedsYou[1]
	want := "blocked: Do you want to proceed? 1. Yes 2. No (answer in pane w1:p1)"
	if blocked.Reason != want {
		t.Errorf("the blocked needs-you %q, want %q", blocked.Reason, want)
	}
	if got, ask := asked.Reason, "asks: may I edit x.go?"; got != ask {
		t.Errorf("the needs-you without a question %q, want %q", got, ask)
	}

	td, ok := Detail{Project: v}.Task("T-1")
	if !ok || len(td.NeedsYou) != 1 || td.NeedsYou[0].Reason != want {
		t.Errorf("the task detail's needs-you %+v (found %v), want %q", td.NeedsYou, ok, want)
	}
}
