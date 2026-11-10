package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
)

// serve follows a new obor binary: on its tick it asks Options.Binary and takes the self-restart path (Loop.Restart), at
// once when idle, else at the first pass where nothing runs (no deadline); status shows it meanwhile.

// followRig is a loop rig whose Binary says isNew and whose Restart counts its calls and gives err (nil: serve ends
// gracefully, as a signal ends it).
type followRig struct {
	*loopRig
	isNew, calls atomic.Int32
	err          error
	// in, hold: when hold is set, Restart sends on in once called and waits for hold to close before it acts.
	in, hold chan struct{}
}

func newFollowRig(t *testing.T, err error) *followRig {
	r := &followRig{err: err}
	r.loopRig = newLoopRig(t)
	r.binary = func() bool { return r.isNew.Load() != 0 }
	r.restart = func(context.Context) error {
		r.calls.Add(1)
		if r.hold != nil {
			r.in <- struct{}{}
			<-r.hold
		}
		if r.err != nil {
			return r.err
		}
		r.cancel()
		return nil
	}
	r.work("feature.txt", "feature\n")
	return r
}

// ended: the restart ended serve.
func (r *followRig) ended() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// restartingShown: obor status' running line shows the pending self-restart.
func (r *followRig) restartingShown() bool {
	st := r.app.Status()
	if st.Project == nil {
		return false
	}
	for _, s := range st.Project.Running {
		if s.Step == event.StepServeRestart {
			return true
		}
	}
	return false
}

// ticks waits for n more ticks of the binary watch (Options.Follow) and then one more merge-loop pass, so the pass of the
// last tick is done.
func (r *followRig) ticks(n int) {
	r.t.Helper()
	follows := app.Follows(r.app)
	r.waitFor("ticks", func() bool { return app.Follows(r.app) >= follows+n })
	_, passes := app.Counts(r.app)
	r.waitFor("a pass", func() bool { _, p := app.Counts(r.app); return p > passes })
}

// down is herdr gone under a connected serve: it waits until serve says it is disconnected and has logged herdr gone
// (its next subscribe failed: a restart before that ends serve without the line).
func (r *followRig) down() {
	r.t.Helper()
	if !r.app.Status().Connected {
		r.t.Fatal("precondition: serve is not connected to herdr")
	}
	r.srv.Die()
	r.waitFor("disconnected", func() bool { return !r.app.Status().Connected && strings.Contains(r.out.String(), "herdr: gone") })
}

// neverBack: herdr did not come back (serve logged no connect after the gone line).
func (r *followRig) neverBack() bool {
	out := r.out.String()
	return strings.Contains(out, "herdr: gone") && !strings.Contains(out, "herdr: back") && !r.app.Status().Connected
}

// Idle: no restart while the binary is the same; a new one restarts serve on the next tick, once.
func TestFollowBinaryIdle(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		r.start()
		r.ticks(3)
		if r.calls.Load() != 0 || r.restartingShown() {
			t.Fatalf("the same binary: restarts %d, restarting shown %v; want none", r.calls.Load(), r.restartingShown())
		}
		r.isNew.Store(1)
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if r.calls.Load() != 1 {
			t.Errorf("restarts %d, want 1", r.calls.Load())
		}
	})
}

// A gate running: the restart waits for it (shown as restarting meanwhile) and goes once it has ended.
func TestFollowBinaryWaitsForIdle(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		release := make(chan struct{})
		r.scratch.Arm("Gate", func(ctx context.Context) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		r.start()
		r.submit()
		r.waitFor("the gate to run", func() bool { return r.scratch.Gates.Load() == 1 })
		r.isNew.Store(1)
		r.waitFor("the pending restart shown", func() bool { return r.restartingShown() || r.calls.Load() != 0 })
		if r.calls.Load() != 0 {
			t.Fatal("serve restarted with the gate running, want it to wait")
		}
		r.ticks(3)
		if r.calls.Load() != 0 {
			t.Fatalf("restarted %d times while the gate runs, want 0", r.calls.Load())
		}
		r.scratch.Arm("Gate", nil)
		close(release)
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if r.calls.Load() != 1 || !r.has("1 result gate") {
			t.Errorf("restarts %d, gate result %v; want 1 after the gate's result: %q", r.calls.Load(), r.has("1 result gate"),
				r.steps())
		}
	})
}

// A lane test running holds the restart as a gate does (it runs minutes): the pending restart is shown meanwhile, no
// restart while it runs, and it goes once the test has ended — the test is not cut.
func TestFollowBinaryWaitsForLaneTest(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		started, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		r.scratch.OnTest(func(ctx context.Context, _ string) {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		r.start()
		r.submit()
		select {
		case <-started:
		case <-time.After(r.patience()):
			t.Fatalf("the lane test never began: %q", r.steps())
		}
		r.isNew.Store(1)
		r.waitFor("the pending restart shown", func() bool { return r.restartingShown() || r.calls.Load() != 0 })
		if r.calls.Load() != 0 {
			t.Fatal("serve restarted with a lane test running, want it to wait")
		}
		r.ticks(3)
		if r.calls.Load() != 0 {
			t.Fatalf("restarted %d times while the lane test runs, want 0", r.calls.Load())
		}
		close(release)
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if r.calls.Load() != 1 || !r.has("1 result test") {
			t.Errorf("restarts %d, test result %v; want 1 after the test ended: %q", r.calls.Load(), r.has("1 result test"),
				r.steps())
		}
	})
}

// An admitted main-checkout writer (a config_set's or a review close's commit, p.admits) holds the restart as a running
// gate does: shown waiting meanwhile, no restart, and once it is unadmitted the next pass restarts.
func TestFollowBinaryWaitsForAdmittedWriter(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		r.start()
		release, err := app.Admit(r.app, r.p.Root)
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		unadmit := func() { once.Do(release) }
		defer unadmit()
		r.isNew.Store(1)
		r.waitFor("the pending restart shown", func() bool { return r.restartingShown() || r.calls.Load() != 0 })
		if r.calls.Load() != 0 {
			t.Fatal("serve restarted with a main-checkout writer admitted, want it to wait")
		}
		r.ticks(3)
		if r.calls.Load() != 0 || !r.restartingShown() {
			t.Fatalf("an admitted writer: restarts %d, restarting shown %v; want 0, shown", r.calls.Load(), r.restartingShown())
		}
		unadmit()
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if r.calls.Load() != 1 {
			t.Errorf("unadmitted: restarts %d, want the next pass's 1", r.calls.Load())
		}
	})
}

// The restart claimed (Restart called), an admit is refused with the message and nothing admitted; the restart runs on.
func TestFollowBinaryClaimRefusesAdmit(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		r.in, r.hold = make(chan struct{}, 1), make(chan struct{})
		r.start()
		r.isNew.Store(1)
		select {
		case <-r.in:
		case <-time.After(10 * time.Second):
			t.Fatal("the restart was never called")
		}
		if n := app.Admits(r.app, r.p.Root); n != 0 {
			t.Fatalf("precondition: %d admissions in force", n)
		}
		release, err := app.Admit(r.app, r.p.Root)
		const want = "serve is restarting on a new obor binary: try again in a moment"
		if err == nil || err.Error() != want {
			if release != nil {
				release()
			}
			t.Errorf("admit under a claimed restart: err %v, want %q", err, want)
		}
		if n := app.Admits(r.app, r.p.Root); n != 0 {
			t.Errorf("admissions after the refused admit: %d, want 0", n)
		}
		close(r.hold)
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if r.calls.Load() != 1 {
			t.Errorf("restarts %d, want 1", r.calls.Load())
		}
	})
}

// A restart that fails: one needs-you, the restarting line gone, no further try in this serve.
func TestFollowBinaryRestartFails(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, errors.New("no kit shim /kit/bin/obor to exec: restart serve by hand"))
		r.start()
		r.isNew.Store(1)
		const want = "serve restart on the new obor binary failed: no kit shim /kit/bin/obor to exec: restart serve by hand"
		r.waitFor("the needs-you", func() bool { return r.has("0 fact needs-you{" + want + "}") })
		r.ticks(3)
		if r.calls.Load() != 1 || r.restartingShown() || r.ended() {
			t.Errorf("restarts %d, restarting shown %v, ended %v; want 1, false, false", r.calls.Load(), r.restartingShown(),
				r.ended())
		}
		if n := strings.Count(strings.Join(r.steps(), "\n"), "needs-you{"+want+"}"); n != 1 {
			t.Errorf("needs-you recorded %d times, want 1: %q", n, r.steps())
		}
		// The claim cleared with the failure: an admit works again.
		release, err := app.Admit(r.app, r.p.Root)
		if err != nil {
			t.Fatalf("admit after the failed restart: %v, want admitted", err)
		}
		if n := app.Admits(r.app, r.p.Root); n != 1 {
			t.Errorf("admissions after the admit: %d, want 1", n)
		}
		release()
	})
}

// The binary watch runs on its own timer, not on the watchdogs' tick, which counts only while herdr is connected. A serve
// started without herdr follows a new binary.
func TestFollowBinaryHerdrDownAtStart(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		r.srv.Die()
		r.launch()
		// The new binary appears once serve has seen herdr gone: a follow tick may come before serve's first subscribe.
		r.waitFor("herdr gone", func() bool { return strings.Contains(r.out.String(), "herdr: gone") })
		r.isNew.Store(1)
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if checks, _ := app.Counts(r.app); checks != 0 || strings.Contains(r.out.String(), "herdr: connected") {
			t.Fatalf("precondition: herdr was there (%d checks):\n%s", checks, r.out)
		}
		if r.calls.Load() != 1 || !r.neverBack() {
			t.Errorf("restarts %d, herdr never back %v; want 1 with herdr down throughout:\n%s", r.calls.Load(),
				r.neverBack(), r.out)
		}
	})
}

// Herdr's stream lost after the connect: serve still follows a new binary.
func TestFollowBinaryStreamLost(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newFollowRig(t, nil)
		r.start()
		r.ticks(3)
		if r.calls.Load() != 0 {
			t.Fatalf("precondition: restarted %d times on the same binary", r.calls.Load())
		}
		r.down()
		r.isNew.Store(1)
		r.waitFor("the restart to end serve", r.ended)
		r.cancel = nil
		if r.calls.Load() != 1 || !r.neverBack() {
			t.Errorf("restarts %d, herdr never back %v; want 1 with herdr down:\n%s", r.calls.Load(), r.neverBack(), r.out)
		}
	})
}
