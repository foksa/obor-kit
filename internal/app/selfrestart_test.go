package app_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/app"
	"obor/internal/testkit"
)

// T-652: after a green after_merge of a project with commands.after_merge.restart_serve, serve restarts itself on the
// new build (Loop.Restart) once the step's result is in the log.

// restartRig is a loop rig whose after_merge is cmd, restart_serve flag, and whose Restart counts its calls,
// notes whether after_merge's result was in the log by then, then gives err (nil: a restart: serve ends itself
// gracefully, as a signal ends it, T-656). running is the status' running line during the restart (T-657).
type restartRig struct {
	*loopRig
	calls       atomic.Int32
	resultFirst atomic.Bool
	running     []app.RunningStep
	err         error
}

func newRestartRig(t *testing.T, cmd string, flag bool, err error) *restartRig {
	r := &restartRig{err: err}
	r.loopRig = newLoopRig(t, "echo 1 pass", testkit.Set("commands.after_merge",
		map[string]any{"cmd": cmd, "restart_serve": flag}))
	r.restart = func(context.Context) error {
		r.calls.Add(1)
		r.resultFirst.Store(r.has("1 result after_merge"))
		if st := r.app.Status(); st.Project != nil {
			r.running = st.Project.Running
		}
		if r.err != nil {
			return r.err
		}
		r.cancel() // the self-restart: serve ends itself gracefully (T-656)
		return nil
	}
	r.work("feature.txt", "feature\n")
	return r
}

// The flag set: after_merge's result is recorded before the restart is asked for; the new serve's replay neither
// merges again nor reruns the after-merge command (nor restarts again), and it goes on with the worker's stop.
func TestSelfRestartAfterMerge(t *testing.T) {
	t.Parallel()
	r := newRestartRig(t, "true", true, nil)
	r.start()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.waitFor("the restart to end serve", func() bool {
		select {
		case <-r.done:
			return true
		default:
			return false
		}
	})
	r.cancel = nil
	if r.calls.Load() != 1 || !r.resultFirst.Load() {
		t.Fatalf("restart calls %d, after_merge result first %v; want 1, true: %q", r.calls.Load(), r.resultFirst.Load(),
			r.steps())
	}
	// T-657: while the restart is in flight, a status answered says so.
	if n := len(r.running); n != 1 || r.running[n-1].Step != app.StepServeRestart || r.running[n-1].Since == 0 {
		t.Errorf("running line during the restart: %+v, want restarting serve", r.running)
	}
	if r.has("1 intent agent.stop") {
		t.Fatalf("agent.stop ran before the restart: %q", r.steps())
	}
	r.start()
	r.merged(1)
	if r.calls.Load() != 1 || r.scratch.afterMerges.Load() != 1 || r.scratch.publishes.Load() != 1 || r.mainMerges() != 1 {
		t.Errorf("after the replay: restarts %d, after_merge runs %d, publishes %d, merges %d; want 1 each",
			r.calls.Load(), r.scratch.afterMerges.Load(), r.scratch.publishes.Load(), r.mainMerges())
	}
	if r.count("1 intent after_merge") != 1 || r.count("1 result after_merge") != 1 || r.count("1 fact merged") != 1 {
		t.Errorf("want one after_merge intent and result, one merged fact: %q", r.steps())
	}
}

// Without the flag (and with it, when after_merge did not run green): no restart.
func TestSelfRestartNotAsked(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		flag bool
		cmd  string
	}{{"flag unset", false, "true"}, {"after_merge failed", true, "echo broke; exit 2"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRestartRig(t, c.cmd, c.flag, nil)
			r.start()
			r.submit()
			r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
			r.merged(1)
			if r.scratch.afterMerges.Load() != 1 || r.calls.Load() != 0 {
				t.Errorf("after_merge runs %d, restarts %d; want 1, 0: %q", r.scratch.afterMerges.Load(), r.calls.Load(),
					r.steps())
			}
		})
	}
}

// A restart that fails: serve goes on (the worker's stop runs) and the lead is told
// "serve restart after T-1 failed: <err>"; the next serve's start ends it.
func TestSelfRestartFails(t *testing.T) {
	t.Parallel()
	r := newRestartRig(t, "true", true, errors.New("obor.serve.pj is not installed (obor serve install)"))
	r.start()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.merged(1)
	const want = "serve restart after T-1 failed: obor.serve.pj is not installed (obor serve install)"
	if r.calls.Load() != 1 || !r.resultFirst.Load() {
		t.Fatalf("restart calls %d, after_merge result first %v; want 1, true", r.calls.Load(), r.resultFirst.Load())
	}
	r.waitFor("the needs-you in force", func() bool {
		for _, f := range r.app.Status().Project.Facts {
			if f.Task == app.ServeTask && f.Step == app.StepNeedsYou && f.Fact().Reason == want {
				return true
			}
		}
		return false
	})
	r.stop()
	r.start()
	r.waitFor("the needs-you to end at the next start", func() bool {
		for _, f := range r.app.Status().Project.Facts {
			if f.Task == app.ServeTask {
				return false
			}
		}
		return true
	})
	if n := strings.Count(strings.Join(r.steps(), "\n"), "fact needs-you{"+want+"}"); n != 1 {
		t.Errorf("needs-you recorded %d times, want 1: %q", n, r.steps())
	}
}
