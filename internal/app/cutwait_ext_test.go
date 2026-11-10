package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
)

// A step cut while serve runs (its effect returns context.Canceled, serve's ctx alive) is not counted and is tried
// again only after the delay, not once per pass; the delay doubles per uncounted attempt up to its cap; a counted
// failure ends the row: the step is tried at once, and a cut after it waits the first delay again.
func TestCutStepWaitsBeforeRetry(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.tick, r.stepRetry, r.stepRetryMax = time.Second, 5*time.Second, 20*time.Second
		r.work("feature.txt", "feature\n")
		cut := context.Canceled
		errs := []error{cut, cut, cut, cut, cut, errors.New("boom"), cut, nil}
		var mu sync.Mutex
		var at []time.Time // each try of the gate, on the bubble's clock
		r.scratch.Arm("Gate", func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			at = append(at, time.Now())
			if len(at) > len(errs) {
				return nil
			}
			return errs[len(at)-1]
		})
		tries := func() []time.Time {
			mu.Lock()
			defer mu.Unlock()
			return append([]time.Time(nil), at...)
		}
		r.start()
		r.submit()
		r.waitFor("the gate's first try", func() bool { return len(tries()) == 1 })

		passes := app.StreamPasses(r.app)
		time.Sleep(4 * time.Second)
		if n, ran := len(tries()), app.StreamPasses(r.app)-passes; n != 1 || ran < 2 {
			t.Fatalf("4s after the cut try: %d tries over %d passes; want 1 try while at least 2 passes ran", n, ran)
		}

		for limit := time.Now().Add(5 * time.Minute); len(tries()) < len(errs) && time.Now().Before(limit); {
			time.Sleep(100 * time.Millisecond)
		}
		got := tries()
		if len(got) != len(errs) {
			t.Fatalf("%d tries of the gate, want %d; records %q\napp output:\n%s", len(got), len(errs), r.steps(), r.out)
		}
		// the wait after each try: the delay doubling to its cap, none after the counted failure, the first delay again
		want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 20 * time.Second, 20 * time.Second,
			0, 5 * time.Second}
		for i, w := range want {
			if gap := got[i+1].Sub(got[i]); gap < w || gap >= w+2*time.Second {
				t.Errorf("try %d came %s after try %d, want %s (the waits: %s)", i+2, gap, i+1, w, gaps(got))
			}
		}
		r.waitStep("1 result gate")
		failed := 0
		for _, s := range r.steps() {
			if strings.HasPrefix(s, "1 fact failed") {
				failed++
			}
		}
		if failed != 1 {
			t.Errorf("%d failed facts, want 1 (the cut tries are not counted, the boom is): %q", failed, r.steps())
		}
		out := r.out.String()
		for _, d := range []string{"5s", "10s", "20s"} {
			if line := "T-1 failed{gate: context canceled} not recorded: tried again in " + d; !strings.Contains(out, line) {
				t.Errorf("no line %q in the app's output:\n%s", line, out)
			}
		}
		if strings.Contains(out, "shutting down") {
			t.Errorf("a cut with serve running was logged as serve's stop:\n%s", out)
		}
	})
}

// gaps is the time between each two tries.
func gaps(at []time.Time) string {
	var s []string
	for i := 1; i < len(at); i++ {
		s = append(s, at[i].Sub(at[i-1]).String())
	}
	return strings.Join(s, " ")
}

// A line's post cut while serve runs (a cancellation before a byte went out) records no failure and is posted again
// only after the delay, not once per pass; the line then goes out once.
func TestCutPostWaitsBeforeRetry(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.tick, r.stepRetry, r.stepRetryMax = time.Second, 5*time.Second, 20*time.Second
		r.inbox.mu.Lock()
		r.inbox.cancels = 2
		r.inbox.mu.Unlock()
		r.start()
		line := sentLine("other", "hi")
		if _, err := r.line("s1", &wire.SendReq{Text: "hi", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the post tried", func() bool { return r.inbox.tries("pj-lead", line) == 1 })
		passes := app.StreamPasses(r.app)
		time.Sleep(4 * time.Second)
		if n, ran := r.inbox.tries("pj-lead", line), app.StreamPasses(r.app)-passes; n != 1 || ran < 2 {
			t.Fatalf("4s after the cut post: %d tries over %d passes; want 1 try while at least 2 passes ran", n, ran)
		}
		r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		if n := r.inbox.tries("pj-lead", line); n != 3 {
			t.Errorf("%d tries of the post, want 3 (two cut, one sent)", n)
		}
		if n := r.msgFailed(); n != 0 {
			t.Errorf("the cut posts recorded %d failed facts: %q", n, r.steps())
		}
		out := r.out.String()
		for _, d := range []string{"5s", "10s"} {
			if !strings.Contains(out, ": context canceled} not recorded: tried again in "+d) {
				t.Errorf("no line of a wait of %s in the app's output:\n%s", d, out)
			}
		}
	})
}

// A nightly run cut while serve runs (it returns a cancellation) is not counted and runs again only after the
// delay, not once per pass.
func TestCutNightlyWaitsBeforeRetry(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		f := &nightlyFake{outs: []app.NightlyOut{green}}
		r := nightlyRig(t, f, "02:00")
		r.tick, r.stepRetry, r.stepRetryMax = time.Second, 5*time.Second, 20*time.Second
		var mu sync.Mutex
		var at []time.Time // each run, on the bubble's clock
		run := r.night.Run
		r.night.Run = func(ctx context.Context) (app.NightlyOut, error) {
			mu.Lock()
			at = append(at, time.Now())
			n := len(at)
			mu.Unlock()
			if n <= 2 {
				return app.NightlyOut{}, context.Canceled
			}
			return run(ctx)
		}
		runs := func() []time.Time {
			mu.Lock()
			defer mu.Unlock()
			return append([]time.Time(nil), at...)
		}
		r.start()
		r.waitFor("the nightly's first run", func() bool { return len(runs()) == 1 })
		passes := app.StreamPasses(r.app)
		time.Sleep(4 * time.Second)
		if n, ran := len(runs()), app.StreamPasses(r.app)-passes; n != 1 || ran < 2 {
			t.Fatalf("4s after the cut run: %d runs over %d passes; want 1 run while at least 2 passes ran", n, ran)
		}
		r.waitStep("0 result nightly.2026-11-10")
		got := runs()
		if len(got) != 3 {
			t.Fatalf("%d runs, want 3 (two cut, one green): %q", len(got), r.steps())
		}
		for i, w := range []time.Duration{5 * time.Second, 10 * time.Second} {
			if gap := got[i+1].Sub(got[i]); gap < w || gap >= w+2*time.Second {
				t.Errorf("run %d came %s after run %d, want %s (the waits: %s)", i+2, gap, i+1, w, gaps(got))
			}
		}
		if r.hasPrefix("0 fact failed") {
			t.Errorf("a cut run was counted: %q", r.steps())
		}
	})
}
