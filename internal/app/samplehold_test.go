package app_test

import (
	"context"
	"testing"
	"time"

	"obor/internal/app/wire"
)

// T-861: the stall timer's sample of the idle integrator's open job (a herdr read on most passes) holds no task: a hold
// and a resume given while it reads are answered, not refused "T-1 has review running; retry when it settles"
// (TestHoldAndResume's and TestJobRecovery/unblocked_refused's load failures: the refused resume left the latter
// waiting a minute for a crash). A sample that comes back due after the resume changed the task nudges nothing: the
// resume's new job goes out, the old one gets no reminder.
func TestJobSampleHoldsNoVerb(t *testing.T) {
	t.Parallel()
	r, _ := reviewed(t)
	r.integratorIs("idle")
	r.quietBaseline("T-1/1/review")
	release, reached := r.srv.Hold("pane.process_info") // the sample's child-process read
	defer release()
	<-reached
	r.clock.Add(10 * time.Minute) // the sample in flight comes back due: quiet with the job for Idle
	if reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodHold, wire.Call{Root: r.p.Root, Agent: "pj-int",
		Role: "integrator", CallID: "h1", Task: "T-1", Note: "owner"}); err != nil {
		t.Fatalf("hold while the job is sampled: %q, %v", reply, err)
	}
	if reply, err := r.resumeWith("r1", "", "go on"); err != nil || reply != "RESUMED T-1: reprompt (held)" {
		t.Fatalf("resume while the job is sampled: %q, %v; want RESUMED", reply, err)
	}
	release()
	r.waitWake("pj-int", "T-1/1/review.2")
	r.passes()
	if r.has("1 intent nudge.review") || r.jobPrompts("T-1/1/review.2") != 1 {
		t.Errorf("want review.2 sent once and no nudge of the resumed job: %q", r.steps())
	}
}
