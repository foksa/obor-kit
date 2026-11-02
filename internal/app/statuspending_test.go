// T-662: obor status' running line lists only the merge loop's effects running now, and a pending line lists the
// command steps whose intent waits — a failed one with its error and the age of that failure.
package app_test

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStatusRunningGateWhileItsEffectRuns: a gate whose effect runs now is a running step, elapsed from that start (the
// record App.do writes when it runs the effect), and nothing of it is pending.
func TestStatusRunningGateWhileItsEffectRuns(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.scratch.arm("Gate", func(context.Context) error { // holds the gate open: its effect runs while the test looks
		once.Do(func() { close(started) })
		<-release
		return nil
	})
	r.start()
	r.submit()
	defer close(release)
	select {
	case <-started:
	case <-time.After(r.patience()):
		t.Fatalf("the gate never started; records:\n%s", strings.Join(r.steps(), "\n"))
	}
	st := r.app.Status()
	if st.Project == nil {
		t.Fatal("serve holds no project")
	}
	var gate []wire.RunningStep
	for _, s := range st.Project.Running {
		if s.Task == "T-1" {
			gate = append(gate, s)
		}
	}
	if len(gate) != 1 || gate[0].Step != event.StepGate {
		t.Fatalf("running steps %+v, want one gate for T-1", st.Project.Running)
	}
	if want := r.clock.Now().Unix(); gate[0].Since != want {
		t.Errorf("the gate's since is %d, want its effect's start %d", gate[0].Since, want)
	}
	if len(st.Project.Pending) != 0 {
		t.Errorf("pending steps %+v, want none: the gate runs", st.Project.Pending)
	}
}

// TestStatusPendingAfterMergeFailure: a failed after_merge, its intent held for obor resume, is a pending step with the
// failure's reason and time — not a running one with a growing elapsed.
func TestStatusPendingAfterMergeFailure(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
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
	if want := r.clock.Now().Unix(); pending[0].FailedAt != want {
		t.Errorf("the pending after_merge failed at %d, want the failed fact's time %d", pending[0].FailedAt, want)
	}
}
