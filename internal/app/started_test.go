package app_test

import (
	"errors"
	"testing"
	"time"
)

// started is T-1's TaskStatus.Started (0: none, or no such task).
func (r *loopRig) started() int64 {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == "T-1" {
			return t.Started
		}
	}
	return 0
}

// T-893 (T-783): serve says when it last tried to start a task's lane agent — its newest agent.start record, a refused
// try too — so the view's no-plugin warning waits for the resume's retry, not the dispatch's first try; a replay knows
// it from the log.
func TestStatusStartedIsTheLastStartTry(t *testing.T) {
	t.Parallel()
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
}
