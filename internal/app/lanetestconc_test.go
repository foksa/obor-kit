package app

import (
	"encoding/json"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// T-961: a lane test runs outside the scratch token (docs/state-model.md "Concurrency"): while pj's T-1 test is held,
// T-5 gates in the scratch, tests in its own lane and reaches its review; obor status shows T-1 testing, its test on
// the running line, and a drop of it is refused while the test runs. Released, T-1's test folds once: green, its
// review next.
func TestLaneTestRunsBesideTheSlot(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	f := r.fakes["pj"]
	f.hold, f.holdTest, f.unbased = "", "T-1", false
	r.run()
	defer r.stop()
	for got := ""; got != "test T-1"; {
		got = r.entered("pj") // each gate's task first (T-1's, T-5's may come before or after T-1's test)
	}
	r.waitFor("T-5's review job while T-1 tests", func() bool { return r.state("pj", "T-5") == stAwaiting })
	if n := f.count(f.gates, "T-5"); n != 1 {
		t.Errorf("T-5 gates %d, want 1 (the scratch free while T-1 tests)", n)
	}
	ps := r.status("pj")
	var t1 wire.TaskStatus
	for _, ts := range ps.Tasks {
		if ts.Task == "T-1" {
			t1 = ts
		}
	}
	if t1.State != stCandidate || t1.Phase != wire.PhaseTesting || t1.Running != event.StepTest || t1.Stage != wire.StageGating {
		t.Errorf("T-1 status %+v; want candidate, phase testing, running test, stage gating", t1)
	}
	listed := false
	for _, s := range ps.Running {
		listed = listed || s.Step == event.StepTest && s.Task == "T-1"
	}
	if !listed {
		t.Errorf("the running line %+v lists no test of T-1", ps.Running)
	}
	if _, _, err := r.a.dropDecide(r.proj("pj"), wire.DropReq{Envelope: wire.Envelope{CallID: "d1"}, Task: "T-1",
		Why: "why"}); err == nil ||
		!strings.Contains(err.Error(), "T-1 has test running") {
		t.Errorf("drop of T-1 while its test runs: %v; want refused", err)
	}

	close(f.release)
	r.waitFor("T-1 tested green", func() bool { // its review job waits behind T-5's (one integrator)
		st := r.state("pj", "T-1")
		return st == stGreen || st == stAwaiting
	})
	if n := r.count("pj", "T-1", event.KindResult, event.StepTest); n != 1 {
		t.Errorf("T-1 test results %d, want 1", n)
	}
	if n := f.count(f.tests, "T-1"); n != 1 {
		t.Errorf("T-1 tests %d, want 1", n)
	}
}

// T-961: a green gate folds by its kind and its age (row 18): a lane or resolved merge the slot compiled is a candidate
// (its lane test next), a moved one is green (no test on a move), and a gate before T-961 (its candidate has no Base:
// it ran the checks itself) folds green as it did then. The test's result folds the candidate: green (its checks kept
// for the review job), red (a sendback next) or fail (a needs-you).
func TestGateAndTestResultsFold(t *testing.T) {
	t.Parallel()
	type rec struct {
		step, kind string
		data       any
	}
	gate := func(kind, base string) []rec {
		return []rec{{event.StepGate, event.KindIntent, gateIn{Kind: kind, Lane: "L-01", Input: "s1"}},
			{event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Line: "GREEN T-1",
				Candidate: port.Candidate{Task: "T-1", SHA: "c1", Target: "t1", Base: base, Patch: "p"}}}}
	}
	test := func(outcome string) rec {
		return rec{event.StepTest, event.KindResult, testOut{Outcome: outcome, Line: outcome + " line", Checks: "check 1 pass"}}
	}
	for _, c := range []struct {
		name   string
		recs   []rec
		state  string
		tested string
		paused bool
	}{
		{"a lane gate: its lane test next", gate(port.InputLane, "b0"), stCandidate, "", false},
		{"a resolved gate: tested once", gate(port.InputResolved, "b0"), stCandidate, "", false},
		{"a moved gate: no test", gate(port.InputMoved, "b0"), stGreen, "", false},
		{"a gate before T-961: green as then", gate(port.InputLane, ""), stGreen, "", false},
		{"a green test", append(gate(port.InputLane, "b0"), test(outGreen)), stGreen, "check 1 pass", false},
		{"a red test", append(gate(port.InputLane, "b0"), test(outRed)), stRed, "", false},
		{"a failed test", append(gate(port.InputLane, "b0"), test(outFail)), stCandidate, "", true},
	} {
		p := &proj{folded: newFolded()}
		all := append([]rec{{event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"}},
			{event.StepSubmit, event.KindResult, submitOut{SHA: "s1"}}}, c.recs...)
		for _, x := range all {
			raw, _ := json.Marshal(x.data)
			p.foldLoop(event.Record{Task: "T-1", Attempt: 1, Step: x.step, Kind: x.kind, Data: raw})
		}
		mt := p.folded.loop["T-1"]
		if mt.state != c.state || mt.tested != c.tested || mt.paused() != c.paused {
			t.Errorf("%s: state %s, tested %q, paused %v; want %s, %q, %v", c.name, mt.state, mt.tested, mt.paused(),
				c.state, c.tested, c.paused)
		}
		if c.paused && !strings.HasPrefix(mt.needs.why, "test: ") {
			t.Errorf("%s: the needs-you %q, want it to name the test", c.name, mt.needs.why)
		}
	}
}
