package app_test

import (
	"encoding/json"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
)

// T-852: a move answers at once; the worker's stop is a step of the merge loop (worker.stop), done
// before the step that needs the lane, its outcome one line to the lead; a stop that fails is a needs-you obor resume
// retries. The rig: newWorkerRig (T-1 working on L-01 as sonnet, its worker pj-l-01 in w1:p1).

// stopLines is the lead's lines that tell a worker.stop's outcome.
func (r *loopRig) stopLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Stopped != "" {
			out = append(out, m.Line)
		}
	}
	return out
}

// needsLines is the lead's lines announcing a needs-you (row 47: message{needs}).
func (r *loopRig) needsLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Needs != 0 {
			out = append(out, m.Line)
		}
	}
	return out
}

// holdStops makes the rig's stops wait until release: entered gets one value per stop as it begins.
func (r *loopRig) holdStops() (entered <-chan struct{}, release func()) {
	in, gate := make(chan struct{}, 8), make(chan struct{})
	r.mu.Lock()
	r.onStop = func() error {
		in <- struct{}{}
		<-gate
		return nil
	}
	r.mu.Unlock()
	closed := false
	release = func() {
		if !closed {
			closed = true
			close(gate)
		}
	}
	r.t.Cleanup(release)
	return in, release
}

// awaitStop waits for a held stop to begin.
func awaitStop(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the merge loop never began the worker's stop")
	}
}

// before: in the log, the record a precedes the record b (both there).
func (r *loopRig) before(a, b string) bool {
	steps := r.steps()
	i, j := slices.Index(steps, a), slices.Index(steps, b)
	return i >= 0 && j >= 0 && i < j
}

// herdrDown is the error a stop gives while herdr does not answer (port.NotRunning).
type herdrDown struct{}

func (herdrDown) Error() string     { return "herdr is not running" }
func (herdrDown) Unavailable() bool { return true }

// A move answers MOVING at once, its worker's stop still to come (held here): the stop is the loop's worker.stop of
// the new attempt, obor status shows it (Stopping), and nothing of the new attempt runs before it ends; then the redo's
// lane.prepare and agent.start run, the new agent gets its work job, and the lead gets one line.
func TestMoveAnswersAtOnceThenStopsTheWorker(t *testing.T) {
	t.Parallel()
	r := newWorkerRig(t)
	entered, release := r.holdStops()
	if len(r.workerStops()) != 0 || len(r.workerLaunches()) != 1 {
		t.Fatalf("precondition: no stop, one start: %q, %d", r.workerStops(), len(r.workerLaunches()))
	}
	reply, err := r.moveTo("m1", "opus")
	if want := "MOVING T-1 on L-01: sonnet → opus (stopping pj-l-01)"; err != nil || reply != want {
		t.Fatalf("move: %q, %v; want %q", reply, err, want)
	}
	awaitStop(t, entered) // the call answered before the stop began, and the stop holds now
	if ts := r.task("T-1"); ts.Stopping != "pj-l-01" || ts.Attempt != 2 {
		t.Errorf("T-1 while its stop runs: %+v; want attempt 2 stopping pj-l-01", ts)
	}
	if r.has("2 intent lane.prepare") || len(r.workerLaunches()) != 1 || !r.has("2 intent worker.stop") {
		t.Errorf("while the stop runs: %q, %d launches; want only the stop's intent", r.steps(), len(r.workerLaunches()))
	}
	release()
	r.waitJob("pj-l-01", "T-1/2/work")
	if !r.before("2 result worker.stop", "2 intent lane.prepare") || !r.before("2 intent lane.prepare", "2 intent agent.start") {
		t.Errorf("the steps %q; want the stop's result, then lane.prepare, then agent.start", r.steps())
	}
	if !slices.Equal(r.workerStops(), []string{"pj-l-01 w1:p1"}) {
		t.Errorf("stops %q; want pj-l-01 at its pane once", r.workerStops())
	}
	if ls := r.workerLaunches(); len(ls) != 2 || ls[1].Type != "opus" {
		t.Errorf("launches %+v; want the move's opus start", ls)
	}
	r.waitFor("the stop's line", func() bool { return len(r.stopLines()) > 0 })
	r.passes()
	if got := r.stopLines(); !slices.Equal(got, []string{"[obor] T-1: stopped pj-l-01"}) {
		t.Errorf("the lead's lines %q; want one: stopped pj-l-01", got)
	}
	if ts := r.task("T-1"); ts.Stopping != "" {
		t.Errorf("T-1 after the stop: Stopping %q, want none", ts.Stopping)
	}
}

// Serve restarted between the move's record and its stop (the record in the log, no stop intent): the replay runs the
// stop once, then the new model's start.
func TestMoveRestartBeforeTheStop(t *testing.T) {
	t.Parallel()
	r := newWorkerRig(t)
	var d map[string]any
	for _, rec := range r.records() {
		if rec.Attempt == 1 && rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	if d == nil || d["branch"] == "" {
		t.Fatalf("precondition: attempt 1's dispatch fact: %v", d)
	}
	r.stop()
	d["redo"], d["types"], d["type"], d["stop"], d["call"] = true, []string{"opus"}, "opus", "pj-l-01", "m1"
	d["reply"] = "MOVING T-1 on L-01: sonnet → opus (stopping pj-l-01)"
	r.writeLog([]logRec{{2, event.StepDispatched, event.KindFact, "", d}})
	r.start()
	r.waitJob("pj-l-01", "T-1/2/work")
	if n, m := r.count("2 intent worker.stop"), r.count("2 result worker.stop"); n != 1 || m != 1 {
		t.Errorf("worker.stop intents %d, results %d; want one each", n, m)
	}
	if !slices.Equal(r.workerStops(), []string{"pj-l-01 w1:p1"}) || !r.before("2 result worker.stop", "2 intent agent.start") {
		t.Errorf("stops %q, steps %q; want one stop, before the start", r.workerStops(), r.steps())
	}
	if ls := r.workerLaunches(); len(ls) != 2 || ls[1].Type != "opus" {
		t.Errorf("launches %+v; want the replayed move's opus start", ls)
	}
	if reply, err := r.moveTo("m1", "opus"); err != nil || reply != d["reply"] {
		t.Errorf("the move's call id again: %q, %v; want its stored reply", reply, err)
	}
}

// The worker is gone before the move's stop (no agent in its pane): worker.stop records it gone (nothing stopped),
// the lead's line says so, and the new model's start runs.
func TestMoveWorkerAlreadyGone(t *testing.T) {
	t.Parallel()
	r := newWorkerRig(t)
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
	r.waitFor("pj-l-01 gone", func() bool {
		if !app.IsIdentified(r.app, r.p.Root, "pj-l-01") {
			return true
		}
		r.push()
		return false
	})
	if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVING T-1 on L-01") {
		t.Fatalf("move: %q, %v", reply, err)
	}
	r.waitFor("the new model's start", func() bool { return len(r.workerLaunches()) == 2 })
	if got := r.resultOf(2, event.StepWorkerStop); !strings.Contains(got, `"gone":true`) {
		t.Errorf("worker.stop's result %s; want gone", got)
	}
	if len(r.workerStops()) != 0 {
		t.Errorf("stops %q; want none (no agent to stop)", r.workerStops())
	}
	r.waitFor("the stop's line", func() bool { return len(r.stopLines()) > 0 })
	if got := r.stopLines(); len(got) != 1 || !strings.HasPrefix(got[0], "[obor] T-1: pj-l-01 was not running (") {
		t.Errorf("the lead's lines %q; want one: pj-l-01 was not running", got)
	}
}

// herdr down at the move's stop: the stop is refused, a needs-you{stop} the owner retries; nothing of the new attempt
// starts (no lane.prepare, no agent.start); the lead gets one line, the needs-you's, naming the retry. obor resume
// retries the stop (worker.stop.2), then the start runs and the lead gets its stopped line.
func TestMoveStopHerdrDown(t *testing.T) {
	t.Parallel()
	r := newWorkerRig(t)
	r.mu.Lock()
	r.onStop = func() error { return herdrDown{} }
	r.mu.Unlock()
	if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVING T-1 on L-01") {
		t.Fatalf("move: %q, %v", reply, err)
	}
	r.waitFor("the stop's needs-you", func() bool { return r.task("T-1").Phase == wire.PhaseNeedsYou })
	r.passes()
	if ts := r.task("T-1"); ts.Stopping != "pj-l-01" {
		t.Errorf("T-1 with its stop refused: %+v; want still stopping pj-l-01", ts)
	}
	if r.has("2 intent lane.prepare") || r.has("2 intent agent.start") || len(r.workerLaunches()) != 1 {
		t.Errorf("a refused stop went on: %q, %d launches", r.steps(), len(r.workerLaunches()))
	}
	want := "pj-l-01 not stopped: herdr is not running; obor resume T-1 retries"
	if why := r.needsYou().Fact().Reason; why != want {
		t.Errorf("needs-you %q; want %q", why, want)
	}
	r.waitFor("the needs-you's line", func() bool { return len(r.needsLines()) > 0 })
	if got, stops := r.needsLines(), r.stopLines(); len(got) != 1 || !strings.Contains(got[0], want) || len(stops) != 0 {
		t.Errorf("the lead's lines: needs-you %q, stop %q; want one, the needs-you's naming %q", got, stops, want)
	}
	r.mu.Lock()
	r.onStop = nil
	r.mu.Unlock()
	r.mustResume("r1", "", "RESUMED T-1: retry (stop)")
	r.waitJob("pj-l-01", "T-1/2/work")
	if !r.has("2 result worker.stop.2") || !r.before("2 result worker.stop.2", "2 intent agent.start") {
		t.Errorf("the steps %q; want worker.stop.2, then the start", r.steps())
	}
	r.waitFor("the stop's line", func() bool { return len(r.stopLines()) > 0 })
	if got := r.stopLines(); !slices.Equal(got, []string{"[obor] T-1: stopped pj-l-01"}) {
		t.Errorf("the lead's lines %q; want the retry's stopped line", got)
	}
}
