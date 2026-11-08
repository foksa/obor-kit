package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"testing"
)

// RV-133 #1 (T-959): restart_job is callerJob's answer, never the status list's first review job. A held (earlier)
// review and a later active one leave the integrator's restart with the active job — the one its job_next would hand
// it — although the status list shows the held task first (by task name) with its job open.
func TestRestartJobSkipsHeld(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	defer r.a.closeLog()
	// T-6's review job goes out after T-4's: its intent is the newer one, so it is the integrator's oldest open job
	// once T-4 is held.
	r.add("pr", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06"})
	r.add("pr", "T-6", event.StepSubmit, event.KindResult, submitOut{SHA: "s6"})
	r.add("pr", "T-6", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-06", Input: "s6"})
	r.add("pr", "T-6", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-6",
		SHA: "c-T-6", Target: "b", Patch: "p"}})
	r.add("pr", "T-6", event.StepReview, event.KindIntent, promptData{Agent: "pr-int", Job: "T-6/1/review", Patch: "p"})
	r.add("pr", "T-6", event.StepReview, event.KindResult, "sent")
	a := r.app("pr")
	if _, err := a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Agent: "pr-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-4", Job: "T-4/1/review",
		Note: "the owner decides"}); err != nil {
		t.Fatalf("hold T-4: %v", err)
	}
	var shown []string
	for _, ts := range r.status("pr").Tasks {
		shown = append(shown, ts.Task+" "+ts.State+" "+ts.Job)
	}
	if want := []string{"T-4 needs-you T-4/1/review", "T-6 awaiting-accept T-6/1/review"}; !slices.Equal(shown, want) {
		t.Fatalf("precondition: the status lists the held job first: %q, want %q", shown, want)
	}
	job, err := a.restartJob(wire.RestartJobReq{Envelope: wire.Envelope{Root: r.roots["pr"], Agent: "pr-int",
		Role: "integrator"}})
	if err != nil || job != "T-6/1/review" {
		t.Errorf("the integrator's restart job: %q, %v; want the active T-6/1/review, not the held T-4's", job, err)
	}
	if job, err := r.app("pq").restartJob(wire.RestartJobReq{Envelope: wire.Envelope{Root: r.roots["pq"],
		Agent: "pq-int", Role: "integrator"}}); job != "" || err != nil {
		t.Errorf("no open job: %q, %v; want no wake", job, err)
	}
}
