package app_test

import (
	"context"
	"errors"
	"obor/internal/app"
	"obor/internal/app/event"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// T-719: serve follows a new obor binary: on its tick it asks Options.Binary and takes the self-restart path
// (Loop.Restart), at once when idle, else once nothing runs or after 10 minutes; status shows it meanwhile.

// followRig is a loop rig whose Binary says isNew and whose Restart counts its calls and gives err (nil: serve ends
// gracefully, as a signal ends it).
type followRig struct {
	*loopRig
	isNew, calls atomic.Int32
	err          error
}

func newFollowRig(t *testing.T, err error) *followRig {
	r := &followRig{err: err}
	r.loopRig = newLoopRig(t, "echo 1 pass")
	r.binary = func() bool { return r.isNew.Load() != 0 }
	r.restart = func(context.Context) error {
		r.calls.Add(1)
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

// ticks waits for n more ticks of the binary watch (Options.Follow, T-737) and then one more merge-loop pass, so
// the pass of the last tick is done.
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
}

// A gate running: the restart waits for it (shown as restarting meanwhile) and goes once it has ended.
func TestFollowBinaryWaitsForIdle(t *testing.T) {
	t.Parallel()
	r := newFollowRig(t, nil)
	release := make(chan struct{})
	r.scratch.arm("Gate", func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	r.start()
	r.submit()
	r.waitFor("the gate to run", func() bool { return r.scratch.gates.Load() == 1 })
	r.isNew.Store(1)
	r.waitFor("the pending restart shown", func() bool { return r.restartingShown() || r.calls.Load() != 0 })
	if r.calls.Load() != 0 {
		t.Fatal("serve restarted with the gate running, want it to wait")
	}
	r.ticks(3)
	if r.calls.Load() != 0 {
		t.Fatalf("restarted %d times while the gate runs, want 0", r.calls.Load())
	}
	r.scratch.arm("Gate", nil)
	close(release)
	r.waitFor("the restart to end serve", r.ended)
	r.cancel = nil
	if r.calls.Load() != 1 || !r.has("1 result gate") {
		t.Errorf("restarts %d, gate result %v; want 1 after the gate's result: %q", r.calls.Load(), r.has("1 result gate"),
			r.steps())
	}
}

// T-930: an admitted main-checkout writer (a config_set's or a review close's commit, p.admits) holds the restart as a
// running gate does: shown waiting meanwhile, no restart, and once it is unadmitted the next pass restarts.
func TestFollowBinaryWaitsForAdmittedWriter(t *testing.T) {
	t.Parallel()
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
}

// Never idle: after 10 minutes the restart goes anyway (the gate is cut, as at any restart), whether herdr is up or
// down (T-737).
func TestFollowBinaryAfterTenMinutes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		down bool
	}{
		{"herdr up", false},
		{"herdr down", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newFollowRig(t, nil)
			release := make(chan struct{})
			defer close(release)
			r.scratch.arm("Gate", func(ctx context.Context) error {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return ctx.Err()
			})
			r.start()
			r.submit()
			r.waitFor("the gate to run", func() bool { return r.scratch.gates.Load() == 1 })
			r.isNew.Store(1)
			r.waitFor("the pending restart shown", func() bool { return r.restartingShown() || r.calls.Load() != 0 })
			if r.calls.Load() != 0 {
				t.Fatal("serve restarted with the gate running, want it to wait")
			}
			if c.down {
				r.down()
			}
			r.clock.Add(9 * time.Minute)
			r.ticks(3)
			if r.calls.Load() != 0 {
				t.Fatalf("restarted after 9 minutes with the gate running, want not yet")
			}
			if c.down && !r.restartingShown() {
				t.Fatalf("herdr down, gate running, before the deadline: the restart is not shown")
			}
			r.clock.Add(time.Minute)
			r.waitFor("the restart to end serve", r.ended)
			r.cancel = nil
			if r.calls.Load() != 1 || r.has("1 result gate") || (c.down && !r.neverBack()) {
				t.Errorf("%s: restarts %d, gate result %v, herdr never back %v; want 1 with the gate cut, herdr down throughout if it was:\n%s",
					c.name, r.calls.Load(), r.has("1 result gate"), r.neverBack(), r.out)
			}
		})
	}
}

// A restart that fails: one needs-you, the restarting line gone, no further try in this serve.
func TestFollowBinaryRestartFails(t *testing.T) {
	t.Parallel()
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
}

// T-737 (RV-87 #1): the binary watch runs on its own timer, not on the watchdogs' tick, which counts only while herdr
// is connected. A serve started without herdr follows a new binary.
func TestFollowBinaryHerdrDownAtStart(t *testing.T) {
	t.Parallel()
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
}

// T-737: herdr's stream lost after the connect: serve still follows a new binary.
func TestFollowBinaryStreamLost(t *testing.T) {
	t.Parallel()
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
}

// followWait is follow.go's: the wait for an idle moment before the restart goes anyway.
const followWait = 10 * time.Minute
