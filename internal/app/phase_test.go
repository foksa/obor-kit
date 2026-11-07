package app

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// statePhase is each state's phase with no live overlay (T-788): the states table's phase column, spelled out so a
// changed row is a changed test.
var statePhase = map[string]string{
	stWaiting: wire.PhaseWaiting, stDispatched: wire.PhaseDispatching, stWorking: wire.PhaseWorking,
	stDriving: wire.PhaseDriving, stSubmitted: wire.PhaseSubmitted, stCandidate: wire.PhaseTesting, stRed: wire.PhaseGating,
	stResolved: wire.PhaseGating, stMoved: wire.PhaseGating, stGreen: wire.PhaseReview, stConflict: wire.PhaseReview,
	stAwaiting: wire.PhaseReviewing, stResolving: wire.PhaseReviewing, stAccepted: wire.PhasePublish,
	stPublished: wire.PhasePublish, stMerged: wire.PhaseMerged, stRejected: wire.PhaseRejected, stDropped: wire.PhaseDropped,
	stDropping: wire.PhaseDropping,
}

// T-788 (S5): every state of the table, with and without each live overlay, has exactly one phase: a needs-you in
// force wins (held only for the integrator's hold verdict, C7), then the gate or publish running or pending, else the
// state's row. A green task is in review. T-827: the merge loop's needs-you is a block on any
// state (due or held), and wins over every overlay but on an ended state (merged, rejected), whose row it leaves.
func TestPhaseOfEveryStateAndOverlay(t *testing.T) {
	t.Parallel()
	if len(statePhase) != len(states) {
		t.Fatalf("precondition: %d phases for %d states", len(statePhase), len(states))
	}
	for st, row := range states {
		want, ok := statePhase[st]
		if !ok || row.phase != want {
			t.Errorf("%s: row phase %q, want %q", st, row.phase, want)
		}
		mt := &mtask{task: "T-1", lane: "L-01", state: st}
		due := &mtask{task: "T-1", lane: "L-01", state: st, needs: block{cause: causeGate, why: "gate: x"}}
		held := &mtask{task: "T-1", lane: "L-01", state: st, needs: heldOf(causeStalled)}
		for _, c := range []struct {
			name string
			o    overlays
			want string
		}{
			{"no overlay", overlays{}, want},
			{"a watchdog's needs-you", overlays{blocked: true}, wire.PhaseNeedsYou},
			{"its gate running", overlays{step: wire.PhaseGating}, wire.PhaseGating},
			{"its gate pending", overlays{step: wire.PhaseGatePending}, wire.PhaseGatePending},
			{"its lane test running", overlays{step: wire.PhaseTesting}, wire.PhaseTesting},
			{"its lane test pending", overlays{step: wire.PhaseTestPending}, wire.PhaseTestPending},
			{"its publish running", overlays{step: wire.PhaseMerging}, wire.PhaseMerging},
			{"its publish pending", overlays{step: wire.PhasePublishPending}, wire.PhasePublishPending},
			{"a needs-you and a step", overlays{blocked: true, step: wire.PhaseGating}, wire.PhaseNeedsYou},
		} {
			if got := phase(mt, c.o); got != c.want {
				t.Errorf("%s, %s: phase %q, want %q", st, c.name, got, c.want)
			}
			blocked := wire.PhaseNeedsYou // the loop's needs-you wins over every overlay
			if row.ended {
				blocked = c.want
			}
			if got := phase(held, c.o); got != blocked {
				t.Errorf("%s held, %s: phase %q, want %q", st, c.name, got, blocked)
			}
			if got := phase(due, c.o); !row.ended && got != wire.PhaseNeedsYou {
				t.Errorf("%s due, %s: phase %q, want needs-you", st, c.name, got)
			}
		}
	}
	// held is the integrator's hold verdict alone (C7): another cause of the loop's needs-you is needs-you.
	held := &mtask{task: "T-1", state: stAwaiting, needs: heldOf(event.CauseHeld)}
	if got := phase(held, overlays{step: wire.PhaseGating}); got != wire.PhaseHeld {
		t.Errorf("the integrator's hold: phase %q, want held", got)
	}
	other := &mtask{task: "T-1", state: stAwaiting, needs: heldOf(causeStalled)}
	if got := phase(other, overlays{}); got != wire.PhaseNeedsYou {
		t.Errorf("a stalled job's needs-you: phase %q, want needs-you, not held", got)
	}
	// The pipeline's stage is the phase's: the merge loop's states have one, the worker's, the drive's, a drop's and the ended none.
	for st, ph := range statePhase {
		inLoop := !strings.Contains(" waiting dispatched working driving merged rejected dropped dropping ", " "+st+" ")
		if (wire.PhaseStage(ph) != "") != inLoop {
			t.Errorf("%s: phase %s, stage %q", st, ph, wire.PhaseStage(ph))
		}
	}
}

// T-788: the step overlay is the task's open gate or publish: gating or merging while its effect runs (a running one
// wins over a pending one), else gate pending or publish pending; another step's intent (a prompt) or a task-less one
// (nightly) gives none. A watchdog's needs-you blocks its task and any task on its lane.
func TestPhaseOverlaysFromTheLog(t *testing.T) {
	t.Parallel()
	rec := func(task, step string) event.Record {
		return event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindIntent}
	}
	gate1, pub2 := rec("T-1", "gate"), rec("T-2", "publish.2")
	p := &proj{exec: map[event.Key]time.Time{gate1.Key(): time.Now()}}
	got := p.stepPhases([]event.Record{rec("T-1", "gate.2"), gate1, pub2, rec("T-3", "prompt.work"),
		rec("T-4", "publish"), rec("", "nightly.1")})
	want := map[string]string{"T-1": wire.PhaseGating, "T-2": wire.PhasePublishPending, "T-4": wire.PhasePublishPending}
	if len(got) != len(want) {
		t.Errorf("step phases %v, want %v", got, want)
	}
	for task, w := range want {
		if got[task] != w {
			t.Errorf("%s: step phase %q, want %q", task, got[task], w)
		}
	}
	p.exec[pub2.Key()] = time.Now()
	if got := p.stepPhases([]event.Record{pub2}); got["T-2"] != wire.PhaseMerging {
		t.Errorf("its publish running: %q, want merging", got["T-2"])
	}

	stalled := event.Record{Task: "T-9", Step: event.StepNeedsYou, Kind: event.KindFact,
		Data: []byte(`{"lane":"L-01","reason":"stalled"}`)}
	p = &proj{folded: folded{active: map[string][]event.Record{"T-9": {stalled}}}}
	if !p.blocked(&mtask{task: "T-1", lane: "L-01"}) || !p.blocked(&mtask{task: "T-9"}) {
		t.Error("a needs-you on the lane or the task: not blocked")
	}
	if p.blocked(&mtask{task: "T-1", lane: "L-02"}) {
		t.Error("a needs-you on another lane and task: blocked")
	}
	p.folded.active["T-9"] = []event.Record{{Task: "T-9", Step: event.StepStalled, Kind: event.KindFact}}
	if p.blocked(&mtask{task: "T-9"}) {
		t.Error("a stalled fact (no needs-you yet): blocked")
	}
}

// T-788: serve's status ships the phase: a green task reads review (not the lane table's done of before), and its
// lane row reads it finished, so a dispatch of it is no redo in its lane.
func TestStatusShipsTheGreenTasksPhase(t *testing.T) {
	h, _ := seedQuestions(t, "Summary: done.")
	var ts wire.TaskStatus
	for _, x := range h.a.Status().Project.Tasks {
		if x.Task == "T-1" {
			ts = x
		}
	}
	if ts.State != stGreen || ts.Phase != wire.PhaseReview || wire.PhaseStage(ts.Phase) != wire.StageReview {
		t.Errorf("T-1: %+v; want green, phase review, stage review", ts)
	}
	h.a.mu.Lock()
	finished := h.a.p.finishedIn("T-1")
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-2"}
	other := h.a.p.finishedIn("T-1")
	h.a.mu.Unlock()
	if !finished || other {
		t.Errorf("finishedIn: %v with T-1's row, %v with none; want true, false", finished, other)
	}
}

// T-797 (RV-102 #4): the merge stage is the task's own, not the display phase's: a watchdog's needs-you (a stalled
// worker, a question) turns a working or dispatching task's phase to needs-you but leaves it out of the merge loop;
// the merge loop's needs-you (held, red past K) keeps its stage; a gate or publish step gives its stage.
func TestStageIgnoresTheWatchdogsNeedsYou(t *testing.T) {
	t.Parallel()
	a := &App{}
	stalled := event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact,
		Data: []byte(`{"lane":"L-01","reason":"stalled"}`)}
	p := &proj{folded: folded{active: map[string][]event.Record{"T-1": {stalled}}}}
	for _, c := range []struct {
		name  string
		mt    *mtask
		steps map[string]string
		phase string
		stage string
	}{
		{"working, watchdog needs-you", &mtask{task: "T-1", lane: "L-01", state: stWorking}, nil, wire.PhaseNeedsYou, ""},
		{"dispatching, watchdog needs-you", &mtask{task: "T-1", lane: "L-01", state: stDispatched}, nil, wire.PhaseNeedsYou, ""},
		{"merge loop held", &mtask{task: "T-1", lane: "L-01", state: stAwaiting, needs: heldOf(event.CauseHeld)},
			nil, wire.PhaseHeld, wire.StageHeld},
		{"merge loop red past K", &mtask{task: "T-1", lane: "L-01", state: stRed, needs: heldOf(causeRed)},
			nil, wire.PhaseNeedsYou, wire.StageHeld},
		{"needs-you on a dispatch", &mtask{task: "T-1", lane: "L-01", state: stDispatched,
			needs: heldOf(causeStalled)}, nil, wire.PhaseNeedsYou, ""},
		{"needs-you on an undelivered fix job", &mtask{task: "T-1", lane: "L-01", state: stWorking,
			needs: heldOf(causeStalled), attemptVars: attemptVars{work: "fix"}}, nil, wire.PhaseNeedsYou, ""},
		{"gating, watchdog needs-you", &mtask{task: "T-1", lane: "L-01", state: stSubmitted},
			map[string]string{"T-1": wire.PhaseGating}, wire.PhaseNeedsYou, wire.StageGating},
	} {
		if got := a.phaseOf(p, c.mt, c.steps); got != c.phase {
			t.Errorf("%s: precondition phase %q, want %q", c.name, got, c.phase)
		}
		if got := a.stageOf(p, c.mt, c.steps); got != c.stage {
			t.Errorf("%s: stage %q, want %q", c.name, got, c.stage)
		}
	}
}
