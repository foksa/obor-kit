package app

import (
	"context"
	"testing"
)

// RV-55 #1 (T-530): an accept is of the spec rev its candidate was gated with. A green gate of another rev ends it
// (the candidate is reviewed again), even when the patch is the same; the same rev and patch keep it (row 20).

// acceptAt gates T-1 green (kind, spec rev, candidate sha), sends its review job and accepts it.
func acceptAt(t *testing.T, h *harness, f *schedScratch, gate, kind string, rev int, sha string) {
	t.Helper()
	ctx := context.Background()
	addRecord(t, h, "T-1", gate, KindIntent, gateIn{Kind: kind, Input: "s1"})
	addRecord(t, h, "T-1", gate, KindResult, gateOut{Outcome: outGreen, SpecRev: rev,
		Candidate: Candidate{Task: "T-1", SHA: sha, Target: "base1", Patch: "same-diff"}})
	p := h.a.p
	mt := *p.folded.loop["T-1"]
	if !h.a.job(ctx, p, &Loop{Agents: f, Typer: f}, mt, nth(StepReview, mt.reviews+1)) {
		t.Fatal("precondition: the review job was not sent")
	}
	if _, err := h.a.verdict(ctx, MethodAccept, Call{Root: h.root, Task: "T-1", Agent: "pj-int", Role: "integrator",
		CallID: "accept" + sha, Job: JobID(p.folded.loop["T-1"].jobKey()), Patch: "same-diff"}); err != nil {
		t.Fatal(err)
	}
	if mt := p.folded.loop["T-1"]; mt.state != stAccepted || mt.accepted != "same-diff" || mt.gateRev != rev {
		t.Fatalf("precondition: T-1 %s accepted %q at rev %d; want accepted same-diff at rev %d", mt.state,
			mt.accepted, mt.gateRev, rev)
	}
}

// movedGreen publishes T-1 into a moved target and gates it green again at rev, the patch unchanged.
func movedGreen(t *testing.T, h *harness, gate, kind string, rev int) {
	t.Helper()
	addRecord(t, h, "T-1", StepPublish, KindResult, gateOut{Outcome: outMoved})
	addRecord(t, h, "T-1", gate, KindIntent, gateIn{Kind: kind, Input: "s1"})
	addRecord(t, h, "T-1", gate, KindResult, gateOut{Outcome: outGreen, SpecRev: rev,
		Candidate: Candidate{Task: "T-1", SHA: "candidate2", Target: "base2", Patch: "same-diff"}})
}

// pickGreenOnce runs the step pickGreen selects for T-1 (live, or after a replay of the log).
func pickGreenOnce(t *testing.T, h *harness, f *schedScratch, replay bool) *mtask {
	t.Helper()
	p := h.a.p
	if replay {
		p = reloadProj(t, h)
	}
	if mt := p.folded.loop["T-1"]; mt.state != stGreen || mt.cand.SHA != "candidate2" || mt.cand.Patch != "same-diff" {
		t.Fatalf("precondition: T-1 %s at %s (%s); want green at candidate2, the patch unchanged", mt.state,
			mt.cand.SHA, mt.cand.Patch)
	}
	s := h.a.pickGreen(p, *p.folded.loop["T-1"])
	if s == nil || !s(context.Background(), &Loop{Agents: f, Typer: f}) {
		t.Fatal("pickGreen: no step, or it failed")
	}
	return p.folded.loop["T-1"]
}

func TestAcceptNewSpecRevReviewsAgain(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, gate1, gate2, kind string
	}{
		{"lane then moved", nth(StepGate, 2), nth(StepGate, 3), InputMoved},
		{"two resolved candidates", nth(StepGate, 2), nth(StepGate, 3), InputResolved},
	} {
		for _, replay := range []bool{false, true} {
			name := c.name + "/live"
			if replay {
				name = c.name + "/replay"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				h, f := seedSched(t, true)
				first := InputLane
				if c.kind == InputResolved {
					first = InputResolved
				}
				acceptAt(t, h, f, c.gate1, first, 1, "candidate1")
				movedGreen(t, h, c.gate2, c.kind, 2)
				mt := pickGreenOnce(t, h, f, replay)
				if mt.state != stAwaiting || mt.reviews != 2 || mt.job != nth(StepReview, 2) {
					t.Errorf("rev 2: T-1 %s, reviews %d, job %q; want awaiting review.2", mt.state, mt.reviews, mt.job)
				}
				if mt.accepted != "" {
					t.Errorf("rev 2: the rev 1 accept %q survived", mt.accepted)
				}
				if n := countStep(t, h, StepKept); n != 0 {
					t.Errorf("rev 2: %d kept facts; want none", n)
				}
			})
		}
	}
}

func TestAcceptSameSpecRevKept(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[replay], func(t *testing.T) {
			t.Parallel()
			h, f := seedSched(t, true)
			acceptAt(t, h, f, nth(StepGate, 2), InputLane, 1, "candidate1")
			movedGreen(t, h, nth(StepGate, 3), InputMoved, 1)
			mt := pickGreenOnce(t, h, f, replay)
			if mt.state != stAccepted || mt.reviews != 1 || countStep(t, h, StepKept) != 1 {
				t.Errorf("rev 1 again: T-1 %s, reviews %d, kept facts %d; want accepted, 1 review, 1 kept", mt.state,
					mt.reviews, countStep(t, h, StepKept))
			}
		})
	}
}

// A log written before the fix: kept recorded after a green gate of a new rev. It replays to the review, not to an
// accept of the old rev.
func TestAcceptKeptAfterRevChangeReplaysToReview(t *testing.T) {
	t.Parallel()
	h, f := seedSched(t, true)
	acceptAt(t, h, f, nth(StepGate, 2), InputLane, 1, "candidate1")
	movedGreen(t, h, nth(StepGate, 3), InputMoved, 2)
	addRecord(t, h, "T-1", StepKept, KindFact, JobData{Patch: "same-diff"})
	p := reloadProj(t, h)
	if countStep(t, h, StepKept) != 1 {
		t.Fatal("precondition: the log carries the kept fact")
	}
	mt := p.folded.loop["T-1"]
	if mt.state != stGreen || mt.accepted != "" || mt.stepName() != nth(StepReview, 2) {
		t.Errorf("replayed: T-1 %s, accepted %q, next %s; want green, no accept, review.2", mt.state, mt.accepted,
			mt.stepName())
	}
}

// countStep is the number of T-1's records of step in the log.
func countStep(t *testing.T, h *harness, step string) int {
	t.Helper()
	recs, err := ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Task == "T-1" && r.Step == step {
			n++
		}
	}
	return n
}
