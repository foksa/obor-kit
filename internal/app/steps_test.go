package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// While pj's T-1 gate is held, T-2's dispatch in pj runs lane.prepare, agent.start and its work prompt, pq's
// T-3 gates, and a verdict on pr's T-4 publishes it; pj's T-5 does not gate (one scratch). Released, T-1's gate folds
// once (one intent, one result: its review job follows), then T-5 gates; pj's scratch calls never overlapped.
func TestHeldGateBlocksNothingElse(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: filepath.Join(filepath.Dir(r.roots["pj"]),
		"pj-wt", "L-02"), Agent: "pj-l-02", Target: "target", Branch: "task/T-2"})
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s; want T-1", task)
	}
	if got := r.running("pj", "T-1"); got != event.StepGate {
		t.Errorf("obor status --facts: T-1 running %q; want gate", got)
	}

	if task := r.entered("pq"); task != "T-3" {
		t.Fatalf("pq's gate: %s; want T-3", task)
	}
	r.waitFor("pq's T-3 gate result and pj's T-2 work prompt", func() bool {
		return r.count("pq", "T-3", event.KindResult, event.StepGate) == 1 && r.count("pj", "T-2", event.KindResult, event.StepWork) == 1
	})
	for _, step := range []string{event.StepPrepare, event.StepStartWork} {
		if r.count("pj", "T-2", event.KindResult, step) != 1 {
			t.Errorf("T-2's %s: no result while T-1's gate is held", step)
		}
	}
	if _, err := r.app("pr").verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Role: "integrator", Agent: "pr-int", CallID: "v1"}, Verb: wire.MethodAccept, Task: "T-4", Job: "T-4/1/review",
		Patch: "p"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("pr's T-4 published and merged", func() bool { return r.state("pr", "T-4") == stMerged })
	if n := r.fakes["pr"].count(r.fakes["pr"].pubs, "T-4"); n != 1 {
		t.Errorf("T-4 published %d times; want 1", n)
	}

	// Still held: T-1 has an intent and no result, T-5 has not gated.
	if r.count("pj", "T-1", event.KindIntent, event.StepGate) != 1 || r.count("pj", "T-1", event.KindResult, event.StepGate) != 0 {
		t.Fatalf("held: T-1's gate intent %d result %d; want 1, 0", r.count("pj", "T-1", event.KindIntent, event.StepGate),
			r.count("pj", "T-1", event.KindResult, event.StepGate))
	}
	if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-5"); n != 0 {
		t.Fatalf("T-5 gated %d times while T-1's gate holds the scratch", n)
	}

	close(r.fakes["pj"].release)
	if task := r.entered("pj"); task != "T-5" {
		t.Fatalf("pj's next gate: %s; want T-5", task)
	}
	r.waitFor("T-1 awaiting its review, T-5 gated", func() bool {
		return r.state("pj", "T-1") == stAwaiting && r.count("pj", "T-5", event.KindResult, event.StepGate) == 1
	})
	if i, res := r.count("pj", "T-1", event.KindIntent, event.StepGate), r.count("pj", "T-1", event.KindResult, event.StepGate); i != 1 || res != 1 {
		t.Errorf("T-1's gate: %d intents, %d results; want one each", i, res)
	}
	if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-1"); n != 1 {
		t.Errorf("T-1 gated %d times; want 1", n)
	}
	if got := r.running("pj", "T-1"); got != "" {
		t.Errorf("T-1 still shown running %q", got)
	}
	f := r.fakes["pj"]
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.max != 1 {
		t.Errorf("pj's scratch calls at once: %d; want 1", f.max)
	}
}

// A stop (a crash's cut) with T-1's gate running leaves its intent pending, no failure counted; a replay
// re-runs the gate once under the same key: one intent, one result.
func TestGateCutReplayRunsOnce(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.run()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s; want T-1", task)
	}
	r.stop()
	if r.count("pj", "T-1", event.KindIntent, event.StepGate) != 1 || r.count("pj", "T-1", event.KindResult, event.StepGate) != 0 ||
		r.count("pj", "T-1", event.KindFact, event.StepFailed) != 0 {
		t.Fatalf("cut: want T-1's gate intent pending, no result, no failure counted")
	}

	p, err := r.a.load(r.roots["pj"]) // the restart: pj's log replayed
	if err != nil {
		t.Fatal(err)
	}
	r.a.mu.Lock()
	r.a.p = p
	r.a.mu.Unlock()
	close(r.fakes["pj"].release)
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's gate after the replay: %s; want T-1", task)
	}
	r.waitFor("T-1's gate result", func() bool { return r.count("pj", "T-1", event.KindResult, event.StepGate) == 1 })
	if i := r.count("pj", "T-1", event.KindIntent, event.StepGate); i != 1 {
		t.Errorf("T-1's gate intents: %d; want 1 (the replay reuses the pending key)", i)
	}
	if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-1"); n != 2 {
		t.Errorf("T-1's Gate calls: %d; want 2 (the cut one, the replay's)", n)
	}
}

// A lane test runs outside the scratch token (docs/state-model.md "Concurrency"): while pj's T-1 test is held,
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

// A green gate folds by its kind and its age (row 18): a lane or resolved merge the slot compiled is a candidate
// (its lane test next), a moved one is green (no test on a move), and a gate of a candidate without a Base:
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
