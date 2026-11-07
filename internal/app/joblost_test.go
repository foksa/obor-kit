package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"testing"
	"time"
)

// T-956: the integrator's job is lost only on proof its pane exited. A pass between two herdr streams (a refresh's gap,
// T-759) knows nothing of that pane, so it neither starts nor moves the Gone clock, however far the clock moves; an
// integrator whose pane is really gone still loses the job after Gone.

// reviewOut puts T-1's review job out to pj-int.
func reviewOut(r *concRig) {
	r.add("pj", "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p"})
	r.add("pj", "T-1", event.StepReview, event.KindResult, "sent")
}

// lostStep is T-1's job watch step in pj now, run (it records the lost fact when it decides one): whether it did.
func lostStep(r *concRig, p *proj) bool {
	r.a.mu.Lock()
	step := r.a.watchJob(p, *p.folded.loop["T-1"])
	r.a.mu.Unlock()
	return step != nil && step(context.Background(), &Loop{})
}

func TestIntegratorJobLostOnlyOnProof(t *testing.T) {
	t.Parallel()
	t.Run("between two streams: no lost", func(t *testing.T) {
		t.Parallel()
		r := newConcRig(t)
		p := r.proj("pj")
		greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p"})
		reviewOut(r)
		now := r.a.opt.Now()
		r.a.opt.Now = func() time.Time { return now }
		if w := r.a.whereIs(p, p.integrator); w.State != Identified {
			t.Fatalf("precondition: the integrator is not identified: %+v", w)
		}
		r.a.mu.Lock()
		r.a.connected = false
		r.a.mu.Unlock()
		if lostStep(r, p) {
			t.Fatal("a lost step on the first pass between streams")
		}
		now = now.Add(2 * r.a.opt.Gone)
		if lostStep(r, p) {
			t.Errorf("a lost step %s between streams, want none", 2*r.a.opt.Gone)
		}
		same(t, r.count("pj", "T-1", event.KindFact, event.StepLost), 0, "job.lost facts")
	})
	t.Run("its pane gone: lost after Gone", func(t *testing.T) {
		t.Parallel()
		r := newConcRig(t)
		p := r.proj("pj")
		greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p"})
		reviewOut(r)
		now := r.a.opt.Now()
		r.a.opt.Now = func() time.Time { return now }
		r.a.mu.Lock()
		delete(r.a.obs, "w1:p9")
		r.a.mu.Unlock()
		if w := r.a.whereIs(p, p.integrator); w.State != Unavailable || w.Why == notConnected {
			t.Fatalf("precondition: %+v, want a pane that exited", w)
		}
		if lostStep(r, p) {
			t.Fatal("a lost step before Gone")
		}
		now = now.Add(r.a.opt.Gone)
		if !lostStep(r, p) {
			t.Fatal("no lost step after Gone with the pane gone")
		}
		same(t, r.count("pj", "T-1", event.KindFact, event.StepLost), 1, "job.lost facts")
	})
}
