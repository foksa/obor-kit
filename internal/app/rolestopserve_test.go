package app_test

import (
	"obor/internal/app/event"
	"slices"
	"strings"
	"testing"
	"time"
)

// T-672: a stopped integrator with no open job is the normal state, also across a serve restart: no timer, no
// job.lost, no needs-you, and obor status shows it as stopped.
func TestRoleStopIntegratorAcrossRestart(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	r.accept("c1", r.patch(job))
	r.merged(1)
	r.waitFor("pj-int stopped after its verdict", func() bool {
		return slices.Contains(r.app.Status().Project.Stopped, "pj-int")
	})
	starts := r.starts.Load()

	r.stop()
	r.start()
	for i := 0; i < 3; i++ {
		r.later(2 * time.Minute) // past Gone (1 min) each time
	}
	st := r.app.Status().Project
	if !slices.Equal(st.Stopped, []string{"pj-int"}) {
		t.Errorf("status stopped %q after the restart, want [pj-int]", st.Stopped)
	}
	for _, f := range st.Facts {
		t.Errorf("fact in force after the restart: %s %s", f.Task, f)
	}
	for _, s := range r.steps() {
		if strings.Contains(s, event.StepLost) || strings.Contains(s, event.StepNeedsYou) {
			t.Errorf("record %q after the stop", s)
		}
	}
	if n := r.starts.Load(); n != starts {
		t.Errorf("integrator starts %d after the restart, want %d (no job: nothing starts it)", n, starts)
	}
	ints := 0
	for _, s := range r.stopped() {
		if strings.HasPrefix(s, "pj-int ") {
			ints++
		}
	}
	if ints != 1 {
		t.Errorf("stops of pj-int %d, want 1 (the replay stops it no second time): %q", ints, r.stopped())
	}
}
