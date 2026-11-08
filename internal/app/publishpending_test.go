package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// pendingRig is T-1 accepted and its publish answered pending (port.PublishPending, T-1029: the merge asked for, not
// done; no workflow returns it yet, the fake is scripted): the task waits in state pending; the candidate it published.
func pendingRig(t *testing.T) (*loopRig, string) {
	t.Helper()
	r, job := reviewed(t)
	cand := r.scratch.Pin("candidate/T-1")
	if cand == "" {
		t.Fatalf("precondition: T-1 has a candidate pin: %q", r.scratch.Pins())
	}
	r.scratch.Script("Publish", "T-1", port.PublishPending)
	r.accept("c1", r.patch(job))
	r.waitFor("T-1 pending", func() bool { return r.stateOf("T-1") == "pending" })
	return r, cand
}

// T-1029: a pending publish is not merged yet: the merge loop's publish step leaves the task pending (no merged fact,
// no second publish, no needs-you), each pass's Landed rule looks again, and once the target holds the merge the task
// is merged at its candidate (not a merge outside the app) and goes on to its after-merge steps.
func TestPublishPendingWaitsForLanded(t *testing.T) {
	t.Parallel()
	r, cand := pendingRig(t)
	r.later(time.Minute)
	r.passes()
	if r.has("1 fact merged") || r.scratch.Publishes.Load() != 1 || r.mainMerges() != 0 || r.hasPrefix("1 fact needs-you") {
		t.Fatalf("pending: want no merge, the one publish, no needs-you; publishes %d, merges %d: %q",
			r.scratch.Publishes.Load(), r.mainMerges(), r.steps())
	}
	if r.stateOf("T-1") != "pending" {
		t.Fatalf("after passes: state %q, want still pending", r.stateOf("T-1"))
	}
	r.scratch.Land("T-1") // the forge merged it
	r.merged(1)
	var merged event.MergedData
	for _, rec := range r.records() {
		if rec.Step == event.StepMerged && rec.Kind == event.KindFact {
			_ = json.Unmarshal(rec.Data, &merged)
		}
	}
	if merged.Head != cand || merged.Outside {
		t.Errorf("merged fact %+v, want head %s (the candidate), not outside", merged, cand)
	}
	if r.scratch.Publishes.Load() != 1 {
		t.Errorf("publishes %d, want the one: the Landed rule merged it, no second publish", r.scratch.Publishes.Load())
	}
}

// T-1029: obor status shows a pending task as it shows a published one: the publish phase and stage, its state
// pending.
func TestPublishPendingStatusLikePublished(t *testing.T) {
	t.Parallel()
	r, _ := pendingRig(t)
	got := r.task("T-1")
	if got.State != "pending" || got.Phase != wire.PhasePublish || got.Stage != wire.StagePublish || got.Ended {
		t.Errorf("status of a pending T-1: state %q phase %q stage %q ended %v; want pending, publish, publish, open",
			got.State, got.Phase, got.Stage, got.Ended)
	}
}
