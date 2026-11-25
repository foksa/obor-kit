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
	bubble(t, func(t *testing.T) {
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
		r.waitFor("one notification", func() bool { return len(r.notifications()) == 1 })
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
	})
}

// resumeFailedDelivery is the rig at T-1's needs-you from a resume whose redelivery failed: the work job went
// undelivered, the resume sent work.2, and its wake was refused (the pane blocked).
func resumeFailedDelivery(t *testing.T) *loopRig {
	t.Helper()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerIs("working")
	r.later(5 * time.Minute)
	r.waitStep("1 result retry.work") // the wake typed: not fetched
	r.promptBlocked("pj-l-01 is blocked")
	r.later(5 * time.Minute)
	r.waitStep("1 fact needs-you{job not delivered: T-1/1/work prompted twice to pj-l-01}")
	r.mustResume("r1", "", "RESUMED T-1: retry (job not delivered)")
	r.waitPrefix("1 fact needs-you{work.2 failed: ") // work.2 refused: the resume's delivery failed
	return r
}

// A resume → the work job's redelivery fails (a needs-you) → the worker restarted fresh takes its job (job_next): the
// needs-you ends and the task works.
func TestResumeWorkerTakesItsJobEndsTheNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := resumeFailedDelivery(t)
		// The worker restarted fresh: a new agent in its pane takes the job.
		r.promptTaken()
		r.removePane("w1:p1")
		r.addPane(agentPane("w1:p1", "pj-l-01", r.lane))
		r.refresh()
		if jr := r.workerNext("n1"); jr.Job != "T-1/1/work.2" {
			t.Fatalf("job_next: %+v, want T-1/1/work.2", jr)
		}
		r.waitFor("working", func() bool { return r.taskState() == "working" })
		if f := r.app.Status().Project.Facts; len(f) != 0 {
			t.Errorf("needs-you in force after the fetch: %+v", f)
		}
	})
}

// The same needs-you ends when the worker submits (task_done) instead: the submission is the next attempt. The lane
// holds work (a submission of nothing is refused: its own needs-you), and the facts are read once the submit step ran
// (task_done answers before it does).
func TestResumeWorkerSubmitsEndsTheNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := resumeFailedDelivery(t)
		if f := r.app.Status().Project.Facts; len(f) != 1 {
			t.Fatalf("precondition: the work.2 needs-you in force, facts %+v", f)
		}
		r.work("feature.txt", "feature\n")
		if reply, err := r.taskDone(report); err != nil || reply != "submitted" {
			t.Fatalf("task_done: %q, %v", reply, err)
		}
		r.waitStep("2 result submit")
		r.passes()
		if f := r.app.Status().Project.Facts; len(f) != 0 {
			t.Errorf("needs-you in force after the submission: %+v; records:\n%s", f, strings.Join(r.steps(), "\n"))
		}
	})
}

// Dispatch's reply warns when a Claude lane's worktree has no enabled obor plugin (its worker would have no obor MCP
// tools), and still dispatches; a lane with the plugin enabled gets no warning (TestEffortPlainTypeNamesNone).
func TestDispatchWarnsAClaudeLaneWithoutThePlugin(t *testing.T) {
	bubble(t, func(t *testing.T) {
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
	})
}
