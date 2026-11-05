package app_test

import (
	"obor/internal/app/wire"
	"strings"
	"testing"
)

// T-755 (RV-92 #2 #4): a drive's start is retryable (obor drive on a drive already starts its agent again, nothing
// recorded).

// newWorkerRig is the dispatch rig with T-1 dispatched to L-01 and its worker pj-l-01 working on it.
func newWorkerRig(t *testing.T) *loopRig {
	t.Helper()
	r := newDispatchRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
	r.mustDispatch("d1")
	r.waitJob("pj-l-01", "T-1/1/work")
	r.waitFor("working", func() bool { return r.stateOf("T-1") == "working" })
	// The worker's start pushed a refresh, so serve subscribes again: wait for that stream, the one a move or a drop
	// decides by serve's identity on (T-758, T-759).
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
