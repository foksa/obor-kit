package app

import (
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"testing"
)

// T-968 (RV-134 #2): the fold of a resolution's green gate (live and replayed: a serve restart replays these records).
// A conflict resolved before any accept binds the accept to the task's own patch at the rev its resolve verdict was
// made under; a spec rev the lead set since leaves it unbound (row 19 reviews it). A moved conflict's resolution keeps
// an earlier accept unless the spec rev changed, as before.

// resolvedGate folds a resolved gate's intent and green result under gate: the task's own patch, green at rev.
func (b *blockLog) resolvedGate(gate string, rev int) *blockLog {
	return b.add(1, gate, event.KindIntent, gateIn{Kind: port.InputResolved, Lane: "L-01"}).
		result(1, gate, nil, gateOut{Outcome: outGreen, SpecRev: rev,
			Candidate: port.Candidate{Task: blockTask, SHA: "candidate2", Target: "base2", Base: "base", Patch: "diff"}})
}

// firstConflictResolved: attempt 1's first gate conflicted, the resolve job ran (its verdict at rev), and the
// resolved (gate.2) green at gateRev.
func firstConflictResolved(rev, gateRev int) *blockLog {
	b := dispatched().submitted(1).gate(1, outConflict).
		add(1, event.StepResolve, event.KindIntent, JobData{Patch: "payload"}).
		verdict(1, wire.MethodResolved, event.StepResolve, verdictResult{SHA: "res", Rev: rev})
	return b.resolvedGate(nth(event.StepGate, 2), gateRev)
}

// acceptedThenMovedConflict: attempt 1's gate green at rev 0, its lane test green, the review accepted (patch diff),
// the target moved, the moved gate (gate.2) conflicted, the resolve job ran (its verdict at rev), the resolved gate.3
// green at gateRev with the same patch.
func acceptedThenMovedConflict(rev, gateRev int) *blockLog {
	b := dispatched().submitted(1)
	b.result(1, event.StepGate, gateIn{Kind: port.InputLane, Lane: "L-01"}, gateOut{Outcome: outGreen, SpecRev: 0,
		Candidate: port.Candidate{SHA: "candidate1", Target: "base1", Base: "base", Patch: "diff"}})
	b.add(1, event.StepTest, event.KindIntent, testIn{})
	b.result(1, event.StepTest, nil, testOut{Outcome: outGreen, Line: "GREEN T-1", Checks: "rc=0"})
	b.add(1, event.StepReview, event.KindIntent, JobData{Patch: "diff"})
	b.verdict(1, wire.MethodAccept, event.StepReview, verdictResult{Patch: "diff", Reply: "ACCEPTED"})
	b.result(1, event.StepPublish, nil, gateOut{Outcome: outMoved})
	b.result(1, nth(event.StepGate, 2), gateIn{Kind: port.InputMoved, Lane: "L-01", Input: "s1"}, gateOut{Outcome: outConflict})
	b.add(1, event.StepResolve, event.KindIntent, JobData{Patch: "diff"})
	b.verdict(1, wire.MethodResolved, event.StepResolve, verdictResult{SHA: "res", Rev: rev})
	return b.resolvedGate(nth(event.StepGate, 3), gateRev)
}

func TestResolvedConflictBindsAccept(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		log             *blockLog
		wantAccepted    string
		wantResolvedRev int
	}{
		{"first conflict, no spec", firstConflictResolved(0, 0), "diff", 0},
		{"first conflict, the resolution's rev", firstConflictResolved(2, 2), "diff", 2},
		{"first conflict, a rev set since", firstConflictResolved(1, 2), "", 1},
		{"accepted move, same rev", acceptedThenMovedConflict(0, 0), "diff", 0},
		{"accepted move, a new rev", acceptedThenMovedConflict(1, 1), "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// blockProj replays the records: the fold a serve restart makes of the same log.
			_, p := blockProj(t, c.log.recs)
			mt := p.folded.loop[blockTask]
			if mt.state != stCandidate { // the resolved candidate: its lane test is next (row 18b)
				t.Fatalf("after the resolved gate: %s, want candidate", mt.state)
			}
			if mt.accepted != c.wantAccepted || mt.resolvedRev != c.wantResolvedRev || mt.cand.Patch != "diff" {
				t.Fatalf("candidate: accepted %q, resolvedRev %d, patch %q; want %q, %d, diff", mt.accepted,
					mt.resolvedRev, mt.cand.Patch, c.wantAccepted, c.wantResolvedRev)
			}
			// The lane test green: row 20 keeps the bound accept, else row 19 sends the review job.
			c.log.add(1, event.StepTest, event.KindIntent, testIn{})
			c.log.result(1, event.StepTest, nil, testOut{Outcome: outGreen, Line: "GREEN T-1", Checks: "rc=0"})
			_, p = blockProj(t, c.log.recs)
			mt = p.folded.loop[blockTask]
			kept := mt.accepted != "" && mt.accepted == mt.cand.Patch
			if mt.state != stGreen || kept != (c.wantAccepted != "") {
				t.Errorf("after the test: %s, keeps %v; want green, keeps %v", mt.state, kept, c.wantAccepted != "")
			}
		})
	}
}

// A red lane test of the resolved candidate leaves the bound patch but the task red: it sends back, and a publish
// cannot start from red (row 21).
func TestResolvedConflictRedTestNeverPublishes(t *testing.T) {
	t.Parallel()
	b := firstConflictResolved(0, 0)
	b.add(1, event.StepTest, event.KindIntent, testIn{})
	b.result(1, event.StepTest, nil, testOut{Outcome: outRed, Line: "RED check"})
	_, p := blockProj(t, b.recs)
	mt := p.folded.loop[blockTask]
	if mt.state != stRed || mt.accepted != "diff" {
		t.Errorf("after the red test: %s, accepted %q; want red with the patch bound, and no publish from red", mt.state,
			mt.accepted)
	}
	if r := states[mt.state]; r.pick == nil || r.phase != wire.PhaseGating {
		t.Errorf("red's row: %+v, want the sendback path (red, gating), never an accepted publish", r)
	}
}
