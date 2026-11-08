package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Rows 6–8: no delivery 5 min after the work job → retry.work; 5 more → needs-you{job not delivered}; obor resume
// sends the job anew (work.2), and its delivery makes the task working.
func TestDispatchDelivery(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.later(4 * time.Minute)
	if r.has("1 intent retry.work") {
		t.Fatalf("retried before 5 min: %q", r.steps())
	}
	r.later(time.Minute)
	r.waitStep("1 result retry.work")
	if n := r.workPrompts(); n != 1 { // the first rode on the start
		t.Errorf("work prompts after the retry %d, want 1", n)
	}
	if got := r.cleared("pj-l-01"); len(got) != 1 || !got[0] {
		t.Errorf("input line cleared before prompts %v, want [true] (the retry clears)", got)
	}
	r.later(5 * time.Minute)
	r.waitStep("1 fact needs-you{job not delivered: T-1/1/work prompted twice to pj-l-01}")
	r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	r.needsYouState()
	r.mustResume("r1", "", "RESUMED T-1: retry (job not delivered)")
	r.waitJob("pj-l-01", "T-1/1/work.2")
	r.waitStep("1 result work.2")
	r.workerIs("working")
	r.waitStep("1 fact delivered")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.passes()
	if n := r.workPrompts(); n != 2 || r.has("1 intent retry.work.2") {
		t.Errorf("work prompts %d (want 2), or a retry of the delivered job: %q", n, r.steps())
	}
}

// T-705: dispatch's reply warns when a Claude lane's worktree has no enabled obor plugin (its worker would have no
// obor MCP tools), and still dispatches; a lane with the plugin enabled gets no warning (TestEffortPlainTypeNamesNone).
func TestDispatchWarnsAClaudeLaneWithoutThePlugin(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	settings := filepath.Join(r.p.LaneDir("L-01"), ".claude", "settings.json")
	if err := os.Remove(settings); err != nil {
		t.Fatalf("precondition: the lane had the plugin enabled: %v", err)
	}
	reply, err := r.dispatchLevel("d1", "opus")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued)") ||
		!strings.Contains(reply, "WARN lane L-01 has no enabled obor plugin") {
		t.Fatalf("dispatch without the plugin: %q, %v; want DISPATCHED … and the WARN", reply, err)
	}
}
