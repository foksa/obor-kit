package app

import (
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

func TestLoopTasksSortedByName(t *testing.T) {
	t.Parallel()
	p := twoInLane()
	p.folded.loop["T-10"] = &mtask{task: "T-10"}
	for range orderTries {
		if got := p.loopTasks(); len(got) != 3 || got[0] != "T-1" || got[1] != "T-10" || got[2] != "T-2" {
			t.Fatalf("loopTasks = %v; want [T-1 T-10 T-2]", got)
		}
	}
}

// firstConflictResolved: attempt 1's first gate conflicted, the resolve job ran, and the resolved (gate.2) is green at
// gateRev.
func firstConflictResolved(gateRev int) *blockLog {
	b := dispatched().submitted(1).gate(1, outConflict).
		add(1, event.StepResolve, event.KindIntent, JobData{Patch: "payload"}).
		verdict(1, wire.MethodResolved, event.StepResolve, verdictResult{SHA: "res"})
	return b.resolvedGate(nth(event.StepGate, 2), gateRev)
}

// acceptedThenMovedConflict: attempt 1's gate green at rev 0, its lane test green, the review accepted (patch diff),
// the target moved, the moved gate (gate.2) conflicted, the resolve job ran, the resolved gate.3 green at gateRev with
// the same patch.
func acceptedThenMovedConflict(gateRev int) *blockLog {
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
	b.verdict(1, wire.MethodResolved, event.StepResolve, verdictResult{SHA: "res"})
	return b.resolvedGate(nth(event.StepGate, 3), gateRev)
}

func TestResolvedConflictKeepsPriorAcceptOnly(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name         string
		log          *blockLog
		wantAccepted string
	}{
		{"first conflict, no spec", firstConflictResolved(0), ""},
		{"first conflict, a spec rev", firstConflictResolved(2), ""},
		{"accepted move, same rev", acceptedThenMovedConflict(0), "diff"},
		{"accepted move, a new rev", acceptedThenMovedConflict(1), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// blockProj replays the records: the fold a serve restart makes of the same log.
			_, p := blockProj(t, c.log.recs)
			mt := p.folded.loop[blockTask]
			if mt.state != stCandidate { // the resolved candidate: its lane test is next (row 18b)
				t.Fatalf("after the resolved gate: %s, want candidate", mt.state)
			}
			if mt.accepted != c.wantAccepted || mt.cand.Patch != "diff" {
				t.Fatalf("candidate: accepted %q, patch %q; want %q, diff", mt.accepted, mt.cand.Patch, c.wantAccepted)
			}
			// The lane test green: row 20 keeps a bound accept, else row 19 sends the review job (a first conflict
			// has none to keep).
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

// A red lane test of the resolved candidate leaves the task red: it sends back, and a publish cannot start from red
// (row 21). A first-gate conflict never had an accept to bind.
func TestResolvedConflictRedTestNeverPublishes(t *testing.T) {
	t.Parallel()
	b := firstConflictResolved(0)
	b.add(1, event.StepTest, event.KindIntent, testIn{})
	b.result(1, event.StepTest, nil, testOut{Outcome: outRed, Line: "RED check"})
	_, p := blockProj(t, b.recs)
	mt := p.folded.loop[blockTask]
	if mt.state != stRed || mt.accepted != "" {
		t.Errorf("after the red test: %s, accepted %q; want red with no accept, and no publish from red", mt.state,
			mt.accepted)
	}
	if r := states[mt.state]; r.pick == nil || r.phase != wire.PhaseGating {
		t.Errorf("red's row: %+v, want the sendback path (red, gating), never an accepted publish", r)
	}
}
