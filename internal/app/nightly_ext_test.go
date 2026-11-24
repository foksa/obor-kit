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

// nightlyFake is a Loop's Nightly: the last full check and each run's outcome scripted, the runs counted.
type nightlyFake struct {
	mu   sync.Mutex
	sha  string           // Last's sha ("" abc1234def)
	last string           // Last's verdict ("" none)
	at   time.Time        // Last's time
	was  string           // Last's summary (the full check before a run)
	outs []app.NightlyOut // each run's outcome in turn (the last one again); Verdict "": the run fails
	runs int
}

func (f *nightlyFake) nightly(at string) *app.Nightly {
	return &app.Nightly{At: at,
		Last: func() (app.FullCheck, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			sha := f.sha
			if sha == "" {
				sha = "abc1234def"
			}
			return app.FullCheck{SHA: sha, Verdict: f.last, At: f.at, Summary: f.was}, nil
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

// nightlyFacts is the nightly's facts in force in r's status.
func nightlyFacts(r *loopRig) []event.Record {
	var out []event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == event.NightlyTask {
			out = append(out, f)
		}
	}
	return out
}

// redNotes is how many notifications the terminal was asked to show of a full check RED.
func redNotes(r *loopRig) int {
	n := 0
	for _, note := range r.notifications() {
		if strings.Contains(note.Body, "full-check RED") {
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

// While on, serve's nightly runs every night at or after merge.nightly_at, once a day, whatever merged (here: nothing
// since the last GREEN); GREEN records no needs-you.
func TestNightlyEveryNightWhileOn(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
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
	})
}

// A RED last full check does not hold the nightly back either: it runs at its time.
func TestNightlyRunsAfterARedCheck(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
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
	})
}

// Off (merge.nightly_at unset: a Nightly at "") never runs, whatever the day or the time, a pending intent of an
// interrupted run too (it waits for on).
func TestNightlyOffNeverRuns(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
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
	})
}

// A switched-off nightly still settles a result whose needs-you was never recorded (a crash after the result): the RED
// fact is at the next pass, and no run follows.
func TestNightlyOffSettlesAResult(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
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
		// record appends to the log, then folds it into the view (loop.go recordSeq): wait for the view, not only the
		// log — a read of Status can come between the two.
		r.waitFor("the nightly fact in the view", func() bool { return len(nightlyFacts(r)) == 1 })
		if fs := nightlyFacts(r); len(fs) != 1 || r.answerOf(fs[0]) != `obor resume nightly "<why>"` {
			t.Errorf("nightly facts: %+v (their answers %q)", fs, r.answerOf(fs[0]))
		}
		r.passes()
		if n, runs := r.count(fact), f.count(); n != 1 || runs != 0 {
			t.Errorf("needs-you %d, runs %d; want 1, 0 (settled while off): %q", n, runs, r.steps())
		}
	})
}

// A RED nightly's needs-you in the store ends (resumed) once the last full check is GREEN, a hand run's (obor nightly)
// too — whether the nightly is switched off or scheduled for later; a restart does not bring it back.
func TestNightlyRedEndedByAGreenByHand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, at string }{
		{"off", ""},
		{"scheduled before its time", "13:00"}, // the rig's clock is 12:00: serve's own run has not come
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
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
		})
	}
}

// redFact records a RED nightly's needs-you in r's store at r's clock: the fact a RED run leaves.
func redFact(t *testing.T, r *loopRig, reason string) {
	t.Helper()
	if err := app.WriteLog(r.p.Root, r.clock.Now(),
		nightlyFact(t, event.StepNeedsYou, event.FactData{Machine: app.Machine, Reason: reason})); err != nil {
		t.Fatal(err)
	}
}

// A hand full check settles the RED nightly's needs-you by the last full check: RED at another sha replaces it (the
// needs-you names the last full check), RED at that sha records nothing.
func TestNightlyHandRedFollowsTheLastCheck(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, sha, want string
		resumed         int
	}{
		{"another sha replaces it", "bdee5eb1234", "full-check RED bdee5eb", 1},
		{"the same sha changes nothing", "abc1234def", "full-check RED abc1234", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				f := &nightlyFake{last: "RED", sha: c.sha}
				r := nightlyRig(t, f, "")
				redFact(t, r, "full-check RED abc1234")
				r.start()
				r.passes()
				r.passes()
				if fs := nightlyFacts(r); len(fs) != 1 || fs[0].Fact().Reason != c.want {
					t.Errorf("nightly facts %+v, want one %q: %q", fs, c.want, r.steps())
				}
				if n := r.count("0 fact needs-you{" + c.want + "}"); n != 1 {
					t.Errorf("needs-you %q recorded %d times, want 1 (the old one ends, the new one is once): %q", c.want, n, r.steps())
				}
				if n := r.count("0 fact resumed"); n != c.resumed {
					t.Errorf("resumed %d, want %d: %q", n, c.resumed, r.steps())
				}
			})
		})
	}
}

// redLines is the lead's needs-you lines about a full check RED, in the log's order.
func redLines(r *loopRig) []string {
	var out []string
	for _, rec := range r.needsLineFacts() {
		var m event.Message
		if json.Unmarshal(rec.Data, &m) == nil && strings.Contains(m.Line, "full-check RED") {
			out = append(out, m.Line)
		}
	}
	return out
}

// obor resume nightly "<why>" ends the RED nightly's needs-you with the why on the fact: status stops listing it, the
// owner's notification stops repeating, and a full check newer than the acknowledgement raises a fresh one.
func TestNightlyRedAckedByResume(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		f := &nightlyFake{last: "RED"}
		r := nightlyRig(t, f, "")
		const red = "full-check RED abc1234"
		redFact(t, r, red)
		r.start()
		// Precondition: the needs-you is in force, its line posted and delivered, its toast shown.
		r.waitFor("the needs-you in force", func() bool { return len(nightlyFacts(r)) == 1 })
		fact := nightlyFacts(r)[0]
		if ans := r.answerOf(fact); ans != `obor resume nightly "<why>"` {
			t.Fatalf("obor status answers %q, want obor resume nightly", ans)
		}
		r.waitFor("the needs line recorded", func() bool { return len(redLines(r)) == 1 })
		var line string
		for _, rec := range r.needsLineFacts() {
			var m event.Message
			if json.Unmarshal(rec.Data, &m) == nil && strings.Contains(m.Line, red) {
				line = m.Line
			}
		}
		if !strings.HasSuffix(line, ` → obor resume nightly "<why>"`) {
			t.Fatalf("the needs line %q, want the full check RED and obor resume nightly's hint", line)
		}
		r.waitFor("the lead's line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		r.waitFor("the toast", func() bool { return redNotes(r) == 1 })

		const why = "the flake is fixed in bdee5eb"
		ack := func(callID string) (string, error) {
			return r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
				CallID: callID}, Task: event.NightlyTask, Text: why})
		}
		reply, err := ack("ack1")
		if want := "RESUMED nightly: " + red + " (" + why + ")"; err != nil || reply != want {
			t.Fatalf("obor resume nightly: %q, %v; want %q", reply, err, want)
		}
		if again, err := ack("ack1"); err != nil || again != reply {
			t.Errorf("a repeated call id: %q, %v; want the stored reply %q", again, err, reply)
		}
		// The status the lead and the web read lists the needs-you no more.
		if fs := nightlyFacts(r); len(fs) != 0 {
			t.Errorf("after the resume, obor status lists %+v; want no nightly needs-you", fs)
		}
		// The resumed fact carries the needs-you it ended and the why.
		var acks []event.Record
		for _, rec := range r.records() {
			if rec.Task == event.NightlyTask && rec.Kind == event.KindFact && rec.Step == event.StepResumed {
				acks = append(acks, rec)
			}
		}
		if len(acks) != 1 || acks[0].Fact().Reason != red || acks[0].Fact().Note != why {
			t.Errorf("resumed facts %+v, want one %q with the why %q", acks, red, why)
		}
		// No repeat line, no second toast: the owner is not notified again.
		r.later(31 * time.Minute)
		r.later(2 * time.Hour)
		if n, toasts := len(redLines(r)), redNotes(r); n != 1 || toasts != 1 {
			t.Errorf("after the ack: %d RED lines, %d toasts; want 1, 1 (no repeat): %q", n, toasts, r.steps())
		}

		// A full check newer than the acknowledgement is RED again: a fresh needs-you, its own line and toast.
		f.mu.Lock()
		f.sha, f.at = "bdee5eb1234", r.clock.Now()
		f.mu.Unlock()
		r.passes()
		r.waitFor("the fresh needs-you", func() bool { return len(nightlyFacts(r)) == 1 })
		if fs := nightlyFacts(r); fs[0].Fact().Reason != "full-check RED bdee5eb" {
			t.Errorf("the fresh needs-you %+v, want full-check RED bdee5eb", fs)
		}
		r.waitFor("the fresh needs-you's toast", func() bool { return redNotes(r) == 2 })
		r.waitFor("the fresh needs-you's line", func() bool { return len(redLines(r)) == 2 })
	})
}

// obor resume nightly's refusals, nothing recorded: no why, a flag, no nightly needs-you in force (naming the one that
// is, when another is), and a caller that is no lead or owner.
func TestResumeNightlyRefusals(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		f := &nightlyFake{last: "RED"}
		r := nightlyRig(t, f, "")
		r.start()
		r.passes()
		ask := func(callID, role, text, flag string) error {
			_, err := r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-l-01", Role: role,
				CallID: callID}, Task: event.NightlyTask, Text: text, Flag: flag})
			return err
		}
		for _, c := range []struct{ name, text, flag, want string }{
			{"no why", " ", "", "resume nightly: say the why"},
			{"a flag", "the flake is fixed", wire.ResumeRegate, "resume nightly: no flag"},
			{"no nightly needs-you in force", "the flake is fixed", "", "resume nightly: no nightly needs-you in force"},
		} {
			if err := ask(c.name, "lead", c.text, c.flag); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %v; want the refusal %q", c.name, err, c.want)
			}
		}
		if n := r.count("0 fact resumed"); n != 0 {
			t.Errorf("the refusals recorded %d resumed facts: %q", n, r.steps())
		}
		// A nightly needs-you of another kind is in force: the refusal names it.
		r.stop()
		if err := app.WriteLog(r.p.Root, r.clock.Now(),
			nightlyFact(t, event.StepNeedsYou, event.FactData{Machine: app.Machine,
				Reason: "nightly failed: cannot take the lock"})); err != nil {
			t.Fatal(err)
		}
		r.start()
		r.passes()
		if err := ask("other", "lead", "the lock is free", ""); err == nil ||
			!strings.Contains(err.Error(), "the nightly needs-you in force is nightly failed: cannot take the lock") {
			t.Errorf("another nightly needs-you in force: %v; want the refusal naming it", err)
		}
		if err := ask("worker", "worker", "the lock is free", ""); err == nil ||
			!strings.Contains(err.Error(), "the lead or the owner resumes a task") {
			t.Errorf("a worker's call: %v; want the refusal naming the role", err)
		}
	})
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

// obor nightly on|off is a config_set of merge.nightly_at; the Loop is built on each pass from the config as it is
// (serve's nightly()), so on runs that night and off runs no more, with no restart. Off when off commits nothing.
func TestNightlySwitchCountsFromTheNextPass(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		f := &nightlyFake{outs: []app.NightlyOut{green}}
		// No trunk (merge.target_branch ""): this plain dir has no repository, so validate starts no git process for the
		// config_set (the trunk check is config's TestValidateTrunkRef).
		r := newLoopRig(t, withConfig(testkit.Set("merge.target_branch", "")))
		r.clock.t = time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
		w := &configWriter{path: filepath.Join(r.p.Root, ".obor", "config.json")}
		r.config = w
		r.nightOf = func() *app.Nightly { // serve's nightly(): merge.nightly_at as the config has it now ("" is off)
			cfg, err := config.Load(r.p.Root)
			if err != nil {
				return nil
			}
			return f.nightly(cfg.Data.Nightly.At)
		}
		set := func(id, val string) string {
			t.Helper()
			reply, err := r.ask(
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
		// The switch counts from the next pass. Let a pass built after the commit finish before the fake clock jumps a
		// day: a pass still in flight from before it holds the old Loop (on) but reads the jumped clock, and would run.
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
	})
}

// A RED nightly is the project's needs-you{full-check RED <sha7>} with a notification, once while it stays RED on the
// same sha; the next GREEN one ends it.
func TestNightlyRedNeedsYouUntilGreen(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		f := &nightlyFake{outs: []app.NightlyOut{red}}
		r := nightlyRig(t, f, "02:00")
		r.start()
		const fact = "0 fact needs-you{full-check RED abc1234}"
		r.waitStep(fact)
		r.waitFor("the notification", func() bool { return redNotes(r) == 1 })
		if fs := nightlyFacts(r); len(fs) != 1 || r.answerOf(fs[0]) != `obor resume nightly "<why>"` {
			t.Errorf("nightly facts: %+v (their answers %q)", fs, r.answerOf(fs[0]))
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
	})
}

// A run that ends neither GREEN nor RED is a counted failure, tried again 10 min after the last one (not at the next
// pass); the 3rd is the day's needs-you{nightly failed: …} and that day runs no more; the next day tries again.
func TestNightlyFailuresThenNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
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
	})
}

// A restart replays the nightly per its intent/result: a pending intent (serve stopped mid-run) runs again under its
// key, once; a day with a result runs none.
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
			bubble(t, func(t *testing.T) {
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
		})
	}
}

// greenLines are the lead's nightly-GREEN lines the rig's inbox posted, in order.
func greenLines(r *loopRig) []string {
	r.inbox.mu.Lock()
	defer r.inbox.mu.Unlock()
	var out []string
	for _, p := range r.inbox.posts {
		if strings.HasPrefix(p, "pj-lead: [obor] nightly GREEN ") {
			out = append(out, p)
		}
	}
	return out
}

// A GREEN full check lines the lead once when a non-gating step's failure is new, or the same step's fail count
// changed, naming the step, its summary, the previous summary (ok: it did not fail) and serve's log; the same failure
// as the previous check lines nothing, a first check with no previous lines its failure with ok, and a RED check keeps
// its needs-you alone.
func TestNightlyGreenNonGatingChangeLinesTheLead(t *testing.T) {
	t.Parallel()
	const (
		passing = "check 7 pass, 0 fail, conformance (non-gating) 65 pass, 0 fail, 1 skip, 0 warn, " +
			"conformance_live (non-gating) agy 1.5.14 live tier skipped, 12s"
		failing = "check 7 pass, 0 fail, conformance (non-gating) 65 pass, 0 fail, 1 skip, 0 warn, " +
			"WARN conformance_live (rc 2, non-gating): agy 1.5.15 live tier not recorded: conformance: 11 pass, 8 fail, 0 skip, 0 warn, 12s"
		fewerFails = "check 7 pass, 0 fail, conformance (non-gating) 65 pass, 0 fail, 1 skip, 0 warn, " +
			"conformance_live (non-gating) agy 1.5.14 live tier not recorded: conformance: 0 pass, 1 fail, 0 skip, 0 warn, 12s"
		live = "agy 1.5.15 live tier not recorded: conformance: 11 pass, 8 fail, 0 skip, 0 warn"
		was1 = "agy 1.5.14 live tier not recorded: conformance: 0 pass, 1 fail, 0 skip, 0 warn"
	)
	for _, c := range []struct {
		name    string
		was     string // last-full-check's summary before the run
		now     string // the run's summary
		verdict string
		want    string // the line after the log path; "": none
	}{
		{"a step that failed and did not before", passing, failing, "GREEN",
			"conformance_live " + live + " (was: ok)"},
		{"the same step with a different fail count", fewerFails, failing, "GREEN",
			"conformance_live " + live + " (was: " + was1 + ")"},
		{"the same failure as the previous check", failing, failing, "GREEN", ""},
		{"no previous full check", "", failing, "GREEN", "conformance_live " + live + " (was: ok)"},
		{"a RED check keeps its needs-you", passing, failing, "RED", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				out := app.NightlyOut{SHA: "abc1234def", Verdict: c.verdict, Summary: c.now}
				if c.verdict == "RED" {
					out.Line = "NIGHTLY RED abc1234"
				} else {
					out.Line = "NIGHTLY GREEN abc1234"
				}
				f := &nightlyFake{last: "GREEN", was: c.was, outs: []app.NightlyOut{out}}
				r := nightlyRig(t, f, "02:00")
				r.start()
				r.waitStep("0 result nightly.2026-11-10")
				if c.verdict == "RED" {
					r.waitStep("0 fact needs-you{full-check RED abc1234}")
				} else {
					r.passes()
				}
				if c.want == "" {
					r.passes()
					if lines := greenLines(r); len(lines) != 0 {
						t.Fatalf("the lead's lines %q, want none", lines)
					}
					return
				}
				want := "pj-lead: [obor] nightly GREEN abc1234: " + c.want + " — log: " +
					filepath.Join(r.p.Root, ".obor", "state", "logs", "serve.log")
				line := strings.TrimPrefix(want, "pj-lead: ")
				r.waitFor("the lead's line", func() bool { return r.inbox.count("pj-lead", line) == 1 })
				if lines := greenLines(r); len(lines) != 1 || lines[0] != want {
					t.Errorf("the lead's lines %q, want one %q", lines, want)
				}
			})
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

// Against the store: a failed try in the log (serve restarted 5 min after it) is tried again 10 min after that failure,
// not at the first pass.
func TestNightlyRetryAfterARestart(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
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
	})
}
