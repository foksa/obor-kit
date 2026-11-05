package app_test

import (
	"obor/internal/app"
	"obor/internal/app/wire"
	"strings"
	"sync"
	"testing"
	"time"
)

// T-755 (RV-92 #2 #3 #4): a drive's start is retryable (obor drive on a drive already starts its agent again, nothing
// recorded), and a take-over's record shares task_done's lock (its worker's stop is the merge loop's, T-852:
// stopstep_test.go).

// newTakeOverRig is the dispatch rig with T-1 dispatched to L-01 and its worker pj-l-01 working on it.
func newTakeOverRig(t *testing.T) *loopRig {
	t.Helper()
	r := newDispatchRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
	r.mustDispatch("d1")
	r.waitJob("pj-l-01", "T-1/1/work")
	r.waitFor("working", func() bool { return r.stateOf("T-1") == "working" })
	// The worker's start pushed a refresh, so serve subscribes again: wait for that stream, the one a take-over decides
	// by serve's identity on (T-758, T-759).
	r.connectedAgain()
	return r
}

// A new drive whose agent never started (the CLI's start failed): obor drive <T-xx> and obor drive <name> answer the
// drive's DRIVE reply on its recorded lane and type, so the CLI starts the agent again; nothing is recorded, nothing
// launched. Once its agent runs: DRIVING, naming it (the CLI starts nothing). The same after a replay.
func TestDriveStartRetried(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t)
	// Every drive below decides by serve's identity: wait for serve's herdr connection as an event, not a sample
	// (T-759).
	r.connectedAgain()
	want := "DRIVE T-1 → L-01 (pj-l-01-drive, " + r.hardType() + ")"
	if reply, err := r.drive("v1", "web"); err != nil || reply != want {
		t.Fatalf("drive: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	n := len(r.records())
	for i, name := range []string{"T-1", "web"} {
		reply, err := r.drive("r"+name, name)
		if err != nil || reply != want+" again: its agent is not running" {
			t.Errorf("retry %d (obor drive %s), its agent not running: %q, %v; want %q again", i+1, name, reply, err, want)
		}
	}
	r.passes()
	if len(r.records()) != n || r.count("1 fact dispatch") != 1 {
		t.Errorf("a retry recorded: %q", r.steps()[n:])
	}
	r.driveAgentUp()
	for _, name := range []string{"T-1", "web"} {
		reply, err := r.drive("u"+name, name)
		if err != nil || reply != "DRIVING T-1 → L-01: pj-l-01-drive runs already (pane w1:p5)" {
			t.Errorf("obor drive %s while its agent runs: %q, %v; want DRIVING naming it", name, reply, err)
		}
	}
	if len(r.records()) != n || r.launchesOf("pj-l-01-drive") != 0 || r.launchesOf("pj-l-01") != 0 {
		t.Errorf("recorded %q or launched (drive %d, worker %d)", r.steps()[n:], r.launchesOf("pj-l-01-drive"),
			r.launchesOf("pj-l-01"))
	}
	r.stop()
	r.start() // the replay
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.connectedAgain() // the replay's serve, before a drive that asks its identity
	r.driveAgentUp()
	if reply, err := r.drive("w1", "web"); err != nil || !strings.HasPrefix(reply, "DRIVING T-1 → L-01: pj-l-01-drive") {
		t.Errorf("obor drive web after the replay: %q, %v; want DRIVING", reply, err)
	}
}

// A take-over whose drive agent did not start: obor drive <T-xx> again starts it on the same lane and type (the
// recorded drive's), no second take-over: no second drive fact, no second stop of the worker. After a replay too.
func TestDriveTakeOverStartRetried(t *testing.T) {
	t.Parallel()
	r := newTakeOverRig(t)
	if reply, err := r.drive("v1", "T-1"); err != nil || !strings.HasPrefix(reply, "DRIVING T-1 → L-01 (pj-l-01-drive, sonnet) taking over") {
		t.Fatalf("take-over: %q, %v", reply, err)
	}
	r.waitFor("T-1 driving, its worker stopped", func() bool {
		return r.stateOf("T-1") == wire.TaskDriving && r.task("T-1").Stopping == ""
	})
	stops := len(r.workerStops())
	if stops != 1 {
		t.Fatalf("precondition: worker stops %q, want pj-l-01's", r.workerStops())
	}
	want := "DRIVE T-1 → L-01 (pj-l-01-drive, sonnet) again: its agent is not running"
	if reply, err := r.drive("v2", "T-1"); err != nil || reply != want {
		t.Errorf("retry: %q, %v; want %q", reply, err, want)
	}
	r.stop()
	r.start()
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	if reply, err := r.drive("v3", "T-1"); err != nil || reply != want {
		t.Errorf("retry after the replay: %q, %v; want %q", reply, err, want)
	}
	if n := r.count("1 fact drive"); n != 1 || len(r.workerStops()) != stops {
		t.Errorf("drive facts %d (want 1), stops %q (want one)", n, r.workerStops())
	}
}

// task_done and a take-over interleaved (hooks at their decisions): one wins, never both. A take-over while task_done
// decides (it holds the lock): submitted, the take-over refused by its state (no drive fact). The take-over holding
// the lock when task_done comes: driving, task_done refused (no submitted fact). Each outcome survives a replay.
func TestDriveTakeOverAndTaskDoneInterleaved(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		submitted bool // task_done wins
		hook      func(r *loopRig, done chan<- error) func(string)
	}{
		{"take-over during task_done's decision", true, func(r *loopRig, done chan<- error) func(string) {
			var once sync.Once
			return func(point string) {
				if point == "task_done.check" { // the take-over waits for the lock task_done holds
					once.Do(func() { go func() { _, err := r.drive("v1", "T-1"); done <- err }() })
				}
			}
		}},
		{"task_done during the take-over's record", false, func(r *loopRig, done chan<- error) func(string) {
			var once sync.Once
			return func(point string) {
				if point == "drive.locked" {
					once.Do(func() { go func() { _, err := r.taskDone(report); done <- err }() })
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newTakeOverRig(t)
			r.work("feature.txt", "feature\n")
			done := make(chan error, 1)
			app.SetHook(r.app, c.hook(r, done))
			var first error // the call the test makes
			if c.name == "take-over during task_done's decision" {
				_, first = r.taskDone(report)
			} else {
				_, first = r.drive("v1", "T-1")
			}
			var second error // the call the hook made
			select {
			case second = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the interleaved call never answered")
			}
			app.SetHook(r.app, func(string) {})
			driveErr, doneErr := first, second
			if c.name == "take-over during task_done's decision" {
				driveErr, doneErr = second, first
			}
			check := func(when string) {
				t.Helper()
				r.passes()
				drives, submits := r.count("1 fact drive"), r.count("1 fact submitted")
				if c.submitted {
					if doneErr != nil || driveErr == nil || !strings.Contains(driveErr.Error(), "not one gating") {
						t.Errorf("%s: task_done %v, take-over %v; want submitted, the take-over refused", when, doneErr, driveErr)
					}
					if submits != 1 || drives != 0 || r.stateOf("T-1") == wire.TaskDriving {
						t.Errorf("%s: %d submitted, %d drive, T-1 %s: want submitted only", when, submits, drives, r.stateOf("T-1"))
					}
					return
				}
				if driveErr != nil || doneErr == nil {
					t.Errorf("%s: take-over %v, task_done %v; want driving, task_done refused", when, driveErr, doneErr)
				}
				if drives != 1 || submits != 0 || r.stateOf("T-1") != wire.TaskDriving {
					t.Errorf("%s: %d drive, %d submitted, T-1 %s: want driving only", when, drives, submits, r.stateOf("T-1"))
				}
			}
			check("live")
			r.stop()
			r.start()
			check("after the replay")
		})
	}
}
