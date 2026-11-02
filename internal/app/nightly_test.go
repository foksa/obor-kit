package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
)

// nightlyFake is a Loop's Nightly: due, the last full check's verdict and each run's outcome scripted, the runs and
// the questions whether it is due counted.
type nightlyFake struct {
	mu   sync.Mutex
	due  bool
	last string           // Last's verdict ("": none)
	outs []app.NightlyOut // each run's outcome in turn (the last one again); Verdict "": the run fails
	runs int
	asks int
	// The last question's input: the merges and fresh Due was asked with.
	merges []event.Merge
	fresh  bool
}

func (f *nightlyFake) nightly(at string) *app.Nightly {
	return &app.Nightly{At: at,
		Due: func(merges []event.Merge, fresh bool) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.asks++
			f.merges, f.fresh = merges, fresh
			return f.due, nil
		},
		Last: func() (string, string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return "abc1234def", f.last, nil
		},
		Run: func(context.Context) (app.NightlyOut, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := f.outs[min(f.runs, len(f.outs)-1)]
			f.runs++
			if out.Verdict == "" {
				return out, errors.New("cannot take the lock")
			}
			return out, nil
		}}
}

// nightlyFacts is the nightly's facts in force in r's status (T-1's watchdog has its own once the clock jumps).
func nightlyFacts(r *loopRig) []event.Record {
	var out []event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == event.NightlyTask {
			out = append(out, f)
		}
	}
	return out
}

// redNotes is how many notifications the fake herdr showed of a full check RED.
func redNotes(r *loopRig) int {
	n := 0
	for _, req := range r.srv.Requests() {
		if req.Method == "notification.show" && strings.Contains(string(req.Params), "full-check RED") {
			n++
		}
	}
	return n
}

func (f *nightlyFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func (f *nightlyFake) set(due bool, outs ...app.NightlyOut) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.due = due
	if len(outs) > 0 {
		f.outs, f.runs = outs, 0
	}
}

var (
	green = app.NightlyOut{SHA: "abc1234def", Verdict: "GREEN", Line: "NIGHTLY GREEN abc1234"}
	red   = app.NightlyOut{SHA: "abc1234def", Verdict: "RED", Line: "NIGHTLY RED abc1234"}
)

// nightlyRig is a loop rig at 12:00 local time on 2026-11-10 whose Loop has f's Nightly at at.
func nightlyRig(t *testing.T, f *nightlyFake, at string) *loopRig {
	r := newLoopRig(t, "echo 1 pass")
	r.clock.t = time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	r.night = f.nightly(at)
	return r
}

// T-464: serve's nightly runs once a day at or after merge.nightly_at, only while due; GREEN records no needs-you.
// DD-v0.12 A5: a day not due is asked once (a merge later that day waits for the next day).
func TestNightlyOncePerDayAtItsTime(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{due: true, outs: []app.NightlyOut{green}}
	r := nightlyRig(t, f, "13:00")
	r.start()
	r.passes()
	if n := f.count(); n != 0 || r.has("0 intent nightly.2026-11-10") {
		t.Fatalf("before 13:00: %d runs, want 0: %q", n, r.steps())
	}
	r.later(time.Hour)
	r.waitStep("0 result nightly.2026-11-10")
	for i := 0; i < 3; i++ {
		r.passes()
	}
	if n := f.count(); n != 1 {
		t.Errorf("runs on 2026-11-10 = %d, want 1", n)
	}
	if fs := nightlyFacts(r); len(fs) != 0 {
		t.Errorf("a GREEN nightly left facts: %+v", fs)
	}

	f.set(false)
	r.later(24 * time.Hour)
	r.passes()
	if n := f.count(); n != 1 {
		t.Errorf("not due on 2026-11-11: runs = %d, want still 1", n)
	}
	f.mu.Lock()
	asks := f.asks
	f.mu.Unlock()
	f.set(true)
	r.passes()
	r.passes()
	f.mu.Lock()
	again := f.asks - asks
	f.mu.Unlock()
	if n := f.count(); n != 1 || again != 0 {
		t.Errorf("due later on 2026-11-11: runs = %d, asked again %d times; want 1, 0 (not due is the day's answer)", n, again)
	}
	r.later(24 * time.Hour)
	r.waitStep("0 result nightly.2026-11-12")
	if n := f.count(); n != 2 {
		t.Errorf("due on 2026-11-12: runs = %d, want 2", n)
	}
}

// T-525: Due is asked with the log's merges, folded from fact merged (its sha, task and the gate's Full), and fresh
// only while the log has neither a merge nor a nightly run (a merges.log before the store is not read: due once).
func TestNightlyDueFromMergedFacts(t *testing.T) {
	t.Parallel()
	asked := func(f *nightlyFake) ([]event.Merge, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.merges, f.fresh
	}
	t.Run("fresh store", func(t *testing.T) {
		t.Parallel()
		f := &nightlyFake{due: true, outs: []app.NightlyOut{green}}
		r := nightlyRig(t, f, "13:00")
		r.start()
		r.later(time.Hour)
		r.waitStep("0 result nightly.2026-11-10")
		if ms, fresh := asked(f); len(ms) != 0 || !fresh {
			t.Errorf("first ask: merges %+v, fresh %v; want none, fresh", ms, fresh)
		}
		r.later(24 * time.Hour)
		r.waitStep("0 result nightly.2026-11-11")
		if ms, fresh := asked(f); len(ms) != 0 || fresh {
			t.Errorf("after a nightly: merges %+v, fresh %v; want none, not fresh", ms, fresh)
		}
	})
	t.Run("a merge", func(t *testing.T) {
		t.Parallel()
		f := &nightlyFake{due: false, outs: []app.NightlyOut{green}}
		r := nightlyRig(t, f, "13:00")
		r.scratch.full.Store(true)
		r.work("feature.txt", "feature\n")
		r.start()
		r.submit()
		r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
		r.merged(1)
		head := r.p.Git(r.p.Root, "rev-parse", "main")
		f.mu.Lock()
		asks := f.asks
		f.mu.Unlock()
		if f.count() != 0 || asks != 0 {
			t.Fatalf("precondition: no nightly asked before 13:00: %q", r.steps())
		}
		r.later(time.Hour)
		r.waitFor("the nightly's question", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.asks == 1 })
		ms, fresh := asked(f)
		if len(ms) != 1 || ms[0].Task != "T-1" || ms[0].SHA != head || !ms[0].Full || ms[0].At.IsZero() || fresh {
			t.Errorf("merges %+v, fresh %v; want T-1's full merge at %s, not fresh", ms, fresh, head)
		}
	})
}

// T-464: a RED nightly is the project's needs-you{full-check RED <sha7>} with a notification, once while it stays RED
// on the same sha; the next GREEN one ends it.
func TestNightlyRedNeedsYouUntilGreen(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{due: true, outs: []app.NightlyOut{red}}
	r := nightlyRig(t, f, "02:00")
	r.start()
	const fact = "0 fact needs-you{full-check RED abc1234}"
	r.waitStep(fact)
	r.waitFor("the notification", func() bool { return redNotes(r) == 1 })
	if fs := nightlyFacts(r); len(fs) != 1 || r.answerOf(fs[0]) != "" {
		t.Errorf("nightly facts: %+v", fs)
	}

	r.later(24 * time.Hour) // RED again on the same sha: no second fact, no second notification
	r.waitStep("0 result nightly.2026-11-11")
	r.passes()
	if n, m := r.count(fact), redNotes(r); n != 1 || m != 1 {
		t.Errorf("RED twice on one sha: %d needs-you, %d notifications; want 1, 1: %q", n, m, r.steps())
	}

	f.set(true, green)
	r.later(24 * time.Hour)
	r.waitFor("the fact to end", func() bool { return len(nightlyFacts(r)) == 0 })
	if !r.has("0 fact resumed") || f.count() != 1 {
		t.Errorf("GREEN on 2026-11-12: resumed %v, runs %d; want true, 1: %q", r.has("0 fact resumed"), f.count(), r.steps())
	}
}

// T-464: a run that ends neither GREEN nor RED is a counted failure, tried again 10 min after the last one (DD-v0.12
// A5: not at the next pass); the 3rd is the day's needs-you{nightly failed: …} and that day runs no more; the next
// day tries again.
func TestNightlyFailuresThenNeedsYou(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{due: true, outs: []app.NightlyOut{{Line: "FAIL"}}}
	r := nightlyRig(t, f, "02:00")
	r.start()
	const failed = "0 fact failed{nightly.2026-11-10: cannot take the lock}"
	r.waitStep(failed)
	for try := 2; try <= 3; try++ {
		r.later(9 * time.Minute)
		if n := f.count(); n != try-1 {
			t.Fatalf("9 min after try %d: runs %d, want %d (no retry before 10 min): %q", try-1, n, try-1, r.steps())
		}
		r.later(time.Minute)
		r.waitFor("the next try", func() bool { return r.count(failed) == try })
	}
	r.waitStep("0 fact needs-you{nightly failed: cannot take the lock}")
	for i := 0; i < 3; i++ {
		r.passes()
	}
	if n, fails := f.count(), r.count("0 fact failed{nightly.2026-11-10: cannot take the lock}"); n != 3 || fails != 3 {
		t.Errorf("runs %d, failed facts %d; want 3, 3: %q", n, fails, r.steps())
	}
	f.set(true, green)
	r.later(24 * time.Hour)
	r.waitFor("the fact to end", func() bool { return len(nightlyFacts(r)) == 0 })
	if !r.has("0 result nightly.2026-11-11") || f.count() != 1 {
		t.Errorf("2026-11-11: runs %d, want 1: %q", f.count(), r.steps())
	}
}

// T-464: a restart replays the nightly per its intent/result (G): a pending intent (serve stopped mid-run) runs again
// under its key, once; a day with a result runs none.
func TestNightlyReplay(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		recs []event.Record
		runs int
	}{
		{"pending intent runs again once", []event.Record{
			{Task: event.NightlyTask, Step: "nightly.2026-11-10", Kind: event.KindIntent, Class: event.ClassGit}}, 1},
		{"a day with a result runs none", []event.Record{
			{Task: event.NightlyTask, Step: "nightly.2026-11-10", Kind: event.KindIntent, Class: event.ClassGit},
			{Task: event.NightlyTask, Step: "nightly.2026-11-10", Kind: event.KindResult,
				Data: []byte(`{"sha":"abc1234def","verdict":"GREEN","line":"x"}`)}}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &nightlyFake{due: true, outs: []app.NightlyOut{green}}
			r := nightlyRig(t, f, "02:00")
			r.p.Dispatched("O-01")
			if err := app.WriteLog(r.p.Root, r.clock.Now(), c.recs...); err != nil {
				t.Fatal(err)
			}
			r.start()
			for i := 0; i < 3; i++ {
				r.passes()
			}
			if n, results := f.count(), r.count("0 result nightly.2026-11-10"); n != c.runs || results != 1 {
				t.Errorf("runs %d, results %d; want %d, 1: %q", n, results, c.runs, r.steps())
			}
			if n := r.count("0 intent nightly.2026-11-10"); n != 1 {
				t.Errorf("intents = %d, want 1 (its key reused)", n)
			}
		})
	}
}

// nightlyFact is a nightly fact of step with data d, as the store holds it.
func nightlyFact(t *testing.T, step string, d event.FactData) event.Record {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindFact, Data: b}
}

// DD-v0.12 A4: a RED nightly's needs-you in the store ends (resumed) once the last full check is GREEN, a hand run's
// (obor nightly) too, though no partial merge makes serve's own nightly due; a restart does not bring it back.
func TestNightlyRedEndedByAGreenByHand(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{due: false, last: "RED", outs: []app.NightlyOut{green}}
	r := nightlyRig(t, f, "02:00")
	if err := app.WriteLog(r.p.Root, r.clock.Now(), nightlyFact(t, event.StepNeedsYou,
		event.FactData{Machine: app.Machine, Reason: "full-check RED abc1234"})); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.passes()
	r.passes()
	if fs := nightlyFacts(r); len(fs) != 1 || r.has("0 fact resumed") {
		t.Fatalf("last-full-check RED: facts %+v, want the RED needs-you in force: %q", fs, r.steps())
	}
	r.stop()
	f.mu.Lock()
	f.last = "GREEN" // obor nightly by hand, GREEN
	f.mu.Unlock()
	r.start()
	r.waitFor("the needs-you to end", func() bool { return len(nightlyFacts(r)) == 0 })
	if n := r.count("0 fact resumed"); n != 1 || f.count() != 0 {
		t.Errorf("resumed %d, runs %d; want 1, 0 (the hand run's GREEN ends it): %q", n, f.count(), r.steps())
	}
	r.stop()
	r.start()
	r.passes()
	if fs, n := nightlyFacts(r), r.count("0 fact resumed"); len(fs) != 0 || n != 1 {
		t.Errorf("after a restart: facts %+v, resumed %d; want none, 1", fs, n)
	}
}

// DD-v0.12 A5, against the store: a failed try in the log (serve restarted 5 min after it) is tried again 10 min after
// that failure, not at the first pass.
func TestNightlyRetryAfterARestart(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{due: true, outs: []app.NightlyOut{green}}
	r := nightlyRig(t, f, "02:00")
	const step = "nightly.2026-11-10"
	if err := app.WriteLog(r.p.Root, r.clock.Now().Add(-5*time.Minute),
		event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindIntent, Class: event.ClassGit},
		nightlyFact(t, event.StepFailed, event.FactData{Machine: app.Machine, Reason: step + ": a git lock", Step: step}),
	); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.later(4 * time.Minute)
	if n := f.count(); n != 0 {
		t.Fatalf("runs %d at 9 min after the failure, want 0: %q", n, r.steps())
	}
	r.later(time.Minute)
	r.waitStep("0 result " + step)
	if n, i := f.count(), r.count("0 intent "+step); n != 1 || i != 1 {
		t.Errorf("runs %d, intents %d; want 1, 1 (its key reused)", n, i)
	}
}
