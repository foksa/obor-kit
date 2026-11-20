package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// serve says when it last tried to start a task's lane agent — its newest agent.start record, a refused try too — so
// the view's no-plugin warning waits for the resume's retry, not the dispatch's first try; a replay knows it from the
// log.
func TestStatusStartedIsTheLastStartTry(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.disp.set(func(d *dispatchFakes) {
			d.fail["start"] = func() error { return errors.New("pj-l-01 has background tasks (--force stops them)") }
		})
		r.start()
		first := r.clock.Now().Unix()
		r.mustDispatch("d1")
		r.waitPrefix("1 fact needs-you{start: pj-l-01 has background tasks")
		r.needsYouState()
		if got := r.started(); got != first {
			t.Fatalf("after the refused start: started %d, want the try's time %d", got, first)
		}
		r.clock.Add(10 * time.Minute)
		r.disp.set(func(d *dispatchFakes) { delete(d.fail, "start") })
		retry := r.clock.Now().Unix()
		r.mustResume("r1", "", "RESUMED T-1: retry (start)")
		r.waitStep("1 result agent.start.2")
		r.waitFor("the retry's start", func() bool { return r.started() == retry })
		r.stop()
		r.launch()
		r.firstCheck()
		if got := r.started(); got != retry {
			t.Errorf("after a replay: started %d, want the retry's %d", got, retry)
		}
	})
}

// obor status' a pending line lists the command steps whose intent waits — a failed one with its error and the age of
// that failure. (The steps a running line lists, and their elapsed time, are TestRunningStepsAreTheCommandStepsWhoseEffectRuns'.)

// TestStatusPendingAfterMergeFailure: a failed after_merge from the real loop, its intent held for obor resume, is a
// pending step with the failure's reason — not a running one with a growing elapsed. (The reason's fold and the failure's
// time are TestPendingStepsWaitForTheirEffect's.)
func TestStatusPendingAfterMergeFailure(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		r.armScratch("AfterMerge") // every try fails: git I/O: index.lock exists
		r.start()
		r.submit()
		r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
		r.waitPrefix("1 fact needs-you{after_merge failed: git I/O: index.lock exists")
		r.waitFor("the needs-you in force", func() bool { return len(r.app.Status().Project.Facts) == 1 })
		st := r.app.Status()
		for _, s := range st.Project.Running {
			if s.Task == "T-1" && s.Step == event.StepAfterMerge {
				t.Errorf("running steps %+v: after_merge waits for obor resume, it does not run", st.Project.Running)
			}
		}
		var pending []wire.PendingStep
		for _, p := range st.Project.Pending {
			if p.Task == "T-1" && p.Step == event.StepAfterMerge {
				pending = append(pending, p)
			}
		}
		if len(pending) != 1 {
			t.Fatalf("pending steps %+v, want after_merge T-1", st.Project.Pending)
		}
		if !strings.Contains(pending[0].Error, "index.lock exists") {
			t.Errorf("the pending after_merge's error is %q, want the failed fact's reason", pending[0].Error)
		}
	})
}
