package app_test

import (
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"
)

// T-916 (RV-123 #2): an old log's take-over (fact drive with a stop, T-852; no record writes it since T-912) keeps its
// worker's stop: a pending or refused worker.stop is retried at replay, as before T-912. The rig: newWorkerRig.

// oldTakeOver stops the app and writes the take-over fact an old build recorded over T-1's dispatch.
func (r *loopRig) oldTakeOver() {
	r.t.Helper()
	d := r.factData("T-1", event.StepDispatched)
	r.stop()
	d["drive"], d["agent"], d["stop"], d["call"] = true, "pj-l-01-drive", "pj-l-01", "v1"
	d["reply"] = "DRIVING T-1 → L-01 (pj-l-01-drive, sonnet) taking over (stopping pj-l-01)"
	r.writeLog([]logRec{{1, event.StepDrive, event.KindFact, "", d}})
}

// An old take-over with its stop pending (tried, cut: an intent without a result): the replay selects worker.stop, it
// runs, the task is driving, and the lead gets the completion line as before T-912.
func TestOldDrivePendingStopRetriedAtReplay(t *testing.T) {
	t.Parallel()
	r := newWorkerRig(t)
	r.oldTakeOver()
	tried, settle := r.cutStops()
	r.start()
	r.connectedAgain()
	awaitStop(t, tried)
	if ts := r.task("T-1"); ts.State != wire.TaskDriving || ts.Stopping != "pj-l-01" || r.has("1 result worker.stop") {
		t.Fatalf("precondition: T-1 %+v, steps %q; want driving, stopping pj-l-01, an intent without a result", ts, r.steps())
	}
	r.stop()
	settle()
	r.start()
	r.connectedAgain()
	r.waitFor("the pending stop run", func() bool { return r.task("T-1").Stopping == "" })
	if ts := r.task("T-1"); ts.State != wire.TaskDriving {
		t.Errorf("T-1 %+v; want driving", ts)
	}
	if got := r.workerStops(); !slices.Contains(got, "pj-l-01 w1:p1") || !r.has("1 result worker.stop") {
		t.Errorf("stops %q, steps %q; want pj-l-01 stopped", got, r.steps())
	}
	r.waitFor("the stop's line", func() bool { return len(r.stopLines()) > 0 })
	if got := r.stopLines(); !slices.Equal(got, []string{"[obor] T-1: stopped pj-l-01"}) {
		t.Errorf("the lead's lines %q; want one: stopped pj-l-01", got)
	}
}

// An old take-over whose stop was refused (a needs-you), then obor resume T-1: the stop is tried again, and a restart
// between them keeps the refusal.
func TestOldDriveRefusedStopRetriedByResume(t *testing.T) {
	t.Parallel()
	r := newWorkerRig(t)
	r.oldTakeOver()
	r.failStops(true)
	r.start()
	r.connectedAgain()
	r.waitFor("the stop's needs-you", func() bool { return r.task("T-1").Phase == wire.PhaseNeedsYou })
	r.stop()
	r.start()
	r.connectedAgain()
	if ts := r.task("T-1"); ts.Phase != wire.PhaseNeedsYou || ts.Stopping != "pj-l-01" {
		t.Fatalf("precondition: T-1 after the replay %+v; want the stop's needs-you, stopping pj-l-01", ts)
	}
	tries := len(r.workerStops())
	r.failStops(false)
	r.mustResume("r1", "", "RESUMED T-1: retry (stop)")
	r.waitFor("the retried stop done", func() bool { return r.task("T-1").Stopping == "" })
	if ts := r.task("T-1"); ts.State != wire.TaskDriving || len(r.workerStops()) <= tries {
		t.Errorf("T-1 %+v, stops %q (%d before); want driving, the stop tried again", ts, r.workerStops(), tries)
	}
}

// A new drive (no halt) is unchanged: driving, no worker.stop step, no stop line, after a replay too.
func TestNewDriveHasNoStop(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t)
	r.connectedAgain()
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatalf("drive: %v", err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.stop()
	r.start()
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	if ts := r.task("T-1"); ts.Stopping != "" || ts.Phase == wire.PhaseNeedsYou {
		t.Errorf("T-1 %+v; want driving, nothing stopping", ts)
	}
	if len(r.workerStops()) != 0 || strings.Contains(strings.Join(r.steps(), " "), "worker.stop") || len(r.stopLines()) != 0 {
		t.Errorf("stops %q, steps %q; want none", r.workerStops(), r.steps())
	}
}
