package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"testing"
)

// T-900: a moved gate is given the base the attempt's last green gate ran on (GateInput.Green); the gate's kept result
// (gateOut.Kept) folds as a green on the new target whose base stays the kept one.

// keptScratch is schedScratch whose Gate logs each GateInput.Green and keeps a moved gate given one (as checks.Scratch
// does when nothing overlaps): green on target base2, Kept that base, partial.
type keptScratch struct {
	*schedScratch
	greens []string
}

func (k *keptScratch) Gate(_ context.Context, in port.GateInput) (port.GateOutcome, error) {
	k.greens = append(k.greens, in.Green)
	c := port.Candidate{Task: in.Task, Lane: in.Lane, SHA: "candidate2", Target: "base2", Patch: "same-diff", Checks: "1 pass", Full: true}
	o := port.GateOutcome{Kind: port.OutcomeGreen, Candidate: c}
	if in.Kind == port.InputMoved && in.Green != "" {
		o.Kept, o.Candidate.Checks, o.Candidate.Full = in.Green, "kept "+in.Green, false
	}
	return o, nil
}

// gateT1 runs T-1's next gate (p's fold) on k.
func gateT1(t *testing.T, h *harness, p *proj, k *keptScratch) *mtask {
	t.Helper()
	if !h.a.gate(context.Background(), p, &Loop{Scratch: k, Agents: k, Typer: k}, *p.folded.loop["T-1"]) {
		t.Fatal("gate: the step failed")
	}
	return p.folded.loop["T-1"]
}

// lastGateResult is T-1's last gate result in the log.
func lastGateResult(t *testing.T, h *harness) gateOut {
	t.Helper()
	recs, err := event.ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	var g gateOut
	for _, r := range recs {
		if step, _ := event.StepN(r.Step); r.Task == "T-1" && step == event.StepGate && r.Kind == event.KindResult {
			g = gateOut{}
			_ = json.Unmarshal(r.Data, &g)
		}
	}
	return g
}

func TestMovedGateGetsLastGreenBase(t *testing.T) {
	t.Parallel()
	// A crash between the decision and its record: the moved gate's intent is in the log, its result is not; serve
	// starts again and the gate runs again with the same base, to the same kept result.
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "crash"}[crash], func(t *testing.T) {
			t.Parallel()
			h, f := seedSched(t, true)
			k := &keptScratch{schedScratch: f}
			acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 0, "candidate1") // green on base1 (rev 0: the harness stores no spec, as the live gate reads)
			addRecord(t, h, "T-1", event.StepPublish, event.KindResult, gateOut{Outcome: outMoved})
			p := h.a.p
			if mt := p.folded.loop["T-1"]; mt.state != stMoved || mt.green != "base1" {
				t.Fatalf("precondition: T-1 %s, green %q; want moved, green base1", mt.state, mt.green)
			}
			if crash {
				addRecord(t, h, "T-1", nth(event.StepGate, 3), event.KindIntent, gateIn{Kind: port.InputMoved, Lane: "L-01", Input: "s1"})
				p = reloadProj(t, h)
			}
			mt := gateT1(t, h, p, k)
			if !slices.Equal(k.greens, []string{"base1"}) {
				t.Errorf("the moved gate's Green: %q; want [base1]", k.greens)
			}
			if g := lastGateResult(t, h); g.Kept != "base1" || g.Outcome != outGreen || g.Candidate.Full {
				t.Errorf("the gate's result: %+v; want green, kept base1, partial", g)
			}
			if mt.state != stGreen || mt.cand.Target != "base2" || mt.green != "base1" || mt.gates != 3 {
				t.Errorf("T-1 %s on %s, green %q, gates %d; want green on base2, green base1 (the kept base), gates 3",
					mt.state, mt.cand.Target, mt.green, mt.gates)
			}
			// The patch is unchanged: the accept is kept, then published (no review, no gate run).
			s := h.a.pickGreen(p, *mt)
			if s == nil || !s(context.Background(), &Loop{Agents: f, Typer: f}) {
				t.Fatal("pickGreen: no step, or it failed")
			}
			if mt := p.folded.loop["T-1"]; mt.state != stAccepted || mt.reviews != 1 {
				t.Errorf("after the kept gate: T-1 %s, reviews %d; want accepted, 1 review", mt.state, mt.reviews)
			}
			// It moves again: the next moved gate keeps against the base its checks ran on, not the kept merge's.
			addRecord(t, h, "T-1", nth(event.StepPublish, 2), event.KindResult, gateOut{Outcome: outMoved})
			gateT1(t, h, p, k)
			if !slices.Equal(k.greens, []string{"base1", "base1"}) {
				t.Errorf("the second moved gate's Green: %q; want base1 again", k.greens)
			}
		})
	}
}

// A last gate that is not green (a moved gate's fail, then obor resume --regate) gives the next moved gate no base:
// it runs.
func TestMovedGateAfterNonGreenGetsNoBase(t *testing.T) {
	t.Parallel()
	h, f := seedSched(t, true)
	k := &keptScratch{schedScratch: f}
	acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 1, "candidate1")
	addRecord(t, h, "T-1", event.StepPublish, event.KindResult, gateOut{Outcome: outMoved})
	addRecord(t, h, "T-1", nth(event.StepGate, 3), event.KindIntent, gateIn{Kind: port.InputMoved, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", nth(event.StepGate, 3), event.KindResult, gateOut{Outcome: outFail, Line: "FAIL lane_setup (rc 1)"})
	addRecord(t, h, "T-1", event.StepUnblocked, event.KindFact, event.FactData{Merge: true, Reason: wire.ResumeRegate, Cause: causeGate})
	p := h.a.p
	if mt := p.folded.loop["T-1"]; mt.state != stMoved || mt.green != "" || mt.cand.Target != "base1" {
		t.Fatalf("precondition: T-1 %s, green %q, candidate on %s; want moved, no green base, the old candidate on base1",
			mt.state, mt.green, mt.cand.Target)
	}
	mt := gateT1(t, h, p, k)
	if !slices.Equal(k.greens, []string{""}) {
		t.Errorf("the moved gate's Green: %q; want none", k.greens)
	}
	if g := lastGateResult(t, h); g.Kept != "" || !g.Candidate.Full {
		t.Errorf("the gate's result: %+v; want gated (nothing kept, full)", g)
	}
	if mt.green != "base2" {
		t.Errorf("green %q; want base2, the base this gate ran on", mt.green)
	}
}
