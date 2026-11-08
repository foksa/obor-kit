package app_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

func TestStatusSocket(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
	dir, err := os.MkdirTemp("", "app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go r.app.Listen(ln)
	st, err := wire.Ask(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Project == nil || len(st.Project.Agents) != 2 || len(st.Project.Facts) != 1 ||
		st.Project.Agents[1].Status != "working" {
		t.Errorf("status over the socket = %+v", st)
	}
	if _, err := wire.Ask(context.Background(), filepath.Join(dir, "none.sock")); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Errorf("Ask without a server: %v", err)
	}
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

// T-662: obor status' running line lists only the merge loop's effects running now, and a pending line lists the
// command steps whose intent waits — a failed one with its error and the age of that failure.

// TestStatusRunningGateWhileItsEffectRuns: a gate whose effect runs now is a running step, elapsed from that start (the
// record App.do writes when it runs the effect), and nothing of it is pending.
func TestStatusRunningGateWhileItsEffectRuns(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.scratch.Arm("Gate", func(context.Context) error { // holds the gate open: its effect runs while the test looks
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
	if want := r.clock.Now().Unix(); pending[0].FailedAt != want {
		t.Errorf("the pending after_merge failed at %d, want the failed fact's time %d", pending[0].FailedAt, want)
	}
}
