package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/testkit"
)

// nightlyFake is a Loop's Nightly: the last full check's verdict and each run's outcome scripted, the runs counted.
type nightlyFake struct {
	mu   sync.Mutex
	last string           // Last's verdict ("": none)
	outs []app.NightlyOut // each run's outcome in turn (the last one again); Verdict "": the run fails
	runs int
}

func (f *nightlyFake) nightly(at string) *app.Nightly {
	return &app.Nightly{At: at,
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

func (f *nightlyFake) set(outs ...app.NightlyOut) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outs, f.runs = outs, 0
}

// nightlyRig is a loop rig at 12:00 local time on 2026-11-10 whose Loop has f's Nightly at at.
func nightlyRig(t *testing.T, f *nightlyFake, at string) *loopRig {
	r := newLoopRig(t)
	r.clock.t = time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	r.night = f.nightly(at)
	return r
}

// T-464, T-781: while on, serve's nightly runs every night at or after merge.nightly_at, once a day, whatever merged
// (here: nothing since the last GREEN); GREEN records no needs-you.
func TestNightlyEveryNightWhileOn(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{last: "GREEN", outs: []app.NightlyOut{green}}
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
	if n, i := f.count(), r.count("0 intent nightly.2026-11-10"); n != 1 || i != 1 {
		t.Errorf("runs on 2026-11-10 = %d, intents %d; want 1, 1 (a second run the same day: none)", n, i)
	}
	if fs := nightlyFacts(r); len(fs) != 0 {
		t.Errorf("a GREEN nightly left facts: %+v", fs)
	}
	for _, s := range r.steps() {
		if strings.Contains(s, " fact merged") {
			t.Fatalf("precondition: nothing merged: %q", r.steps())
		}
	}
	for day := 11; day <= 12; day++ {
		r.later(24 * time.Hour)
		r.waitStep(fmt.Sprintf("0 result nightly.2026-11-%d", day))
	}
	if n := f.count(); n != 3 {
		t.Errorf("runs by 2026-11-12 with nothing merged = %d, want 3 (one a night)", n)
	}
}

// T-781: a RED last full check does not hold the nightly back either: it runs at its time.
func TestNightlyRunsAfterARedCheck(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{last: "RED", outs: []app.NightlyOut{green}}
	r := nightlyRig(t, f, "13:00")
	r.start()
	r.passes()
	if n := f.count(); n != 0 {
		t.Fatalf("precondition: no run before 13:00: %d", n)
	}
	r.later(time.Hour)
	r.waitStep("0 result nightly.2026-11-10")
	if n := f.count(); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
}

// T-781, T-782: off (merge.nightly_at unset: a Nightly at "") never runs, whatever the day or the time, a pending
// intent of an interrupted run too (it waits for on).
func TestNightlyOffNeverRuns(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{outs: []app.NightlyOut{green}}
	r := nightlyRig(t, f, "")
	if r.night == nil {
		t.Fatal("precondition: the loop has its nightly (T-782: off is At \"\")")
	}
	if err := app.WriteLog(r.p.Root, r.clock.Now(), event.Record{Task: event.NightlyTask, Step: "nightly.2026-11-10",
		Kind: event.KindIntent, Class: event.ClassGit}); err != nil {
		t.Fatal(err)
	}
	r.start()
	for day := 0; day < 3; day++ {
		r.later(8 * time.Hour)
		r.later(16 * time.Hour)
	}
	if n, runs := r.countPrefix("0 intent nightly."), f.count(); n != 1 || runs != 0 {
		t.Errorf("while off: intents %d (the pending one), runs %d; want 1, 0: %q", n, runs, r.steps())
	}
	if n := r.count("0 result nightly."); n != 0 {
		t.Errorf("results while off = %d, want 0: %q", n, r.steps())
	}
}

// T-782: a switched-off nightly still settles a result whose needs-you was never recorded (a crash after the result):
// the RED fact is at the next pass, and no run follows.
func TestNightlyOffSettlesAResult(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{outs: []app.NightlyOut{green}}
	r := nightlyRig(t, f, "")
	const step = "nightly.2026-11-10"
	if err := app.WriteLog(r.p.Root, r.clock.Now(),
		event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindIntent, Class: event.ClassGit},
		event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindResult,
			Data: []byte(`{"sha":"abc1234def","verdict":"RED","line":"NIGHTLY RED abc1234"}`)},
	); err != nil {
		t.Fatal(err)
	}
	if n := r.count("0 fact needs-you{full-check RED abc1234}"); n != 0 {
		t.Fatalf("precondition: the result came without its needs-you: %d facts", n)
	}
	r.start()
	const fact = "0 fact needs-you{full-check RED abc1234}"
	r.waitStep(fact)
	// record appends to the log, then folds it into the view (loop.go recordSeq): wait for the view, not only the log —
	// a loaded machine can read Status in between the two (T-889).
	r.waitFor("the nightly fact in the view", func() bool { return len(nightlyFacts(r)) == 1 })
	if fs := nightlyFacts(r); len(fs) != 1 || r.answerOf(fs[0]) != "" {
		t.Errorf("nightly facts: %+v", fs)
	}
	r.passes()
	if n, runs := r.count(fact), f.count(); n != 1 || runs != 0 {
		t.Errorf("needs-you %d, runs %d; want 1, 0 (settled while off): %q", n, runs, r.steps())
	}
}

// DD-v0.12 A4, T-782: a RED nightly's needs-you in the store ends (resumed) once the last full check is GREEN, a hand
// run's (obor nightly) too — whether the nightly is switched off or scheduled for later; a restart does not bring it
// back.
func TestNightlyRedEndedByAGreenByHand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, at string }{
		{"off", ""},
		{"scheduled before its time", "13:00"}, // the rig's clock is 12:00: serve's own run has not come
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &nightlyFake{last: "RED", outs: []app.NightlyOut{green}}
			r := nightlyRig(t, f, c.at)
			if err := app.WriteLog(r.p.Root, r.clock.Now(), nightlyFact(t, event.StepNeedsYou,
				event.FactData{Machine: app.Machine, Reason: "full-check RED abc1234"})); err != nil {
				t.Fatal(err)
			}
			r.start()
			r.passes()
			r.passes()
			if fs := nightlyFacts(r); len(fs) != 1 || r.has("0 fact resumed") {
				t.Fatalf("precondition: last-full-check RED: facts %+v, want the RED needs-you in force: %q", fs, r.steps())
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
		})
	}
}

// configWriter is a ConfigFile on the rig's config.json: Commit writes the file and counts the commits.
type configWriter struct {
	mu      sync.Mutex
	path    string
	commits []string
}

func (c *configWriter) Read(context.Context) ([]byte, string, error) {
	b, err := os.ReadFile(c.path)
	return b, "", err
}

func (c *configWriter) Commit(_ context.Context, doc []byte, msg string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commits = append(c.commits, msg)
	return "c0ffee1234567", os.WriteFile(c.path, doc, 0o644)
}

func (c *configWriter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.commits)
}

// T-781: obor nightly on|off is a config_set of merge.nightly_at; the Loop is built on each pass from the config as it
// is (serve's nightly()), so on runs that night and off runs no more, with no restart. Off when off commits nothing.
func TestNightlySwitchCountsFromTheNextPass(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{outs: []app.NightlyOut{green}}
	// No trunk (merge.target_branch ""): this plain dir has no repository, so validate starts no git process for the
	// config_set (T-1024; the trunk check is config's TestValidateTrunkRef).
	r := newLoopRig(t, withConfig(testkit.Set("merge.target_branch", "")))
	r.clock.t = time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	w := &configWriter{path: filepath.Join(r.p.Root, ".obor", "config.json")}
	r.config = w
	r.nightOf = func() *app.Nightly { // serve's nightly(): merge.nightly_at as the config has it now ("" is off, T-782)
		cfg, err := config.Load(r.p.Root)
		if err != nil {
			return nil
		}
		return f.nightly(cfg.Data.Nightly.At)
	}
	set := func(id, val string) string {
		t.Helper()
		reply, err := wire.Verdict(context.Background(), r.sock,
			&wire.ConfigSetReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: id},
				Changes: []wire.ConfigChange{{Key: "nightly.at", Value: json.RawMessage(val)}}})
		if err != nil {
			t.Fatalf("config_set %s: %v", val, err)
		}
		return reply
	}
	r.start()
	r.passes()
	if n := f.count(); n != 0 {
		t.Fatalf("precondition: off, no run: %d", n)
	}
	if reply := set("on", `"11:00"`); reply != `SET nightly.at unset → "11:00"` {
		t.Errorf("on: %q", reply)
	}
	r.waitStep("0 result nightly.2026-11-10")
	if n, c := f.count(), w.count(); n != 1 || c != 1 {
		t.Errorf("after on: runs %d, commits %d; want 1, 1", n, c)
	}
	if reply := set("off", `null`); reply != `SET nightly.at "11:00" → unset` {
		t.Errorf("off: %q", reply)
	}
	// The switch counts from the next pass (T-781). Let a pass built after the commit finish before the fake clock jumps
	// a day: a pass still in flight from before it holds the old Loop (on) but reads the jumped clock, and would run.
	r.passes()
	r.later(24 * time.Hour)
	r.passes()
	if n := f.count(); n != 1 || r.has("0 intent nightly.2026-11-11") {
		t.Errorf("off on 2026-11-11: runs %d, want still 1: %q", n, r.steps())
	}
	if reply := set("off-again", `null`); reply != "UNCHANGED nightly.at" || w.count() != 2 {
		t.Errorf("off when off: %q, commits %d; want UNCHANGED, still 2", reply, w.count())
	}
	if reply := set("on-again", `"11:00"`); !strings.HasPrefix(reply, "SET ") {
		t.Fatalf("on again: %q", reply)
	}
	r.waitStep("0 result nightly.2026-11-11")
}

// T-464: a RED nightly is the project's needs-you{full-check RED <sha7>} with a notification, once while it stays RED
// on the same sha; the next GREEN one ends it.
func TestNightlyRedNeedsYouUntilGreen(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{outs: []app.NightlyOut{red}}
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

	f.set(green)
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
	f := &nightlyFake{outs: []app.NightlyOut{{Line: "FAIL"}}}
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
	f.set(green)
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
			f := &nightlyFake{outs: []app.NightlyOut{green}}
			r := nightlyRig(t, f, "02:00")
			r.p.Dispatched("L-01")
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

// DD-v0.12 A5, against the store: a failed try in the log (serve restarted 5 min after it) is tried again 10 min after
// that failure, not at the first pass.
func TestNightlyRetryAfterARestart(t *testing.T) {
	t.Parallel()
	f := &nightlyFake{outs: []app.NightlyOut{green}}
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
