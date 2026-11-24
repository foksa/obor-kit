package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
)

// A full check's non-gating step shifts: a step that failed and did not, or with another fail count; a passing step,
// the same failure, and a summary that only names a changed pass/skip count are none; a step reporting its fails with
// rc 0 counts as failed. Its summary's own commas and parentheses are not step boundaries.
func TestNonGatingShifts(t *testing.T) {
	t.Parallel()
	const (
		live15  = "agy 1.5.15 live tier not recorded: conformance: 11 pass, 8 fail, 0 skip, 0 warn"
		live14  = "agy 1.5.14 live tier not recorded: conformance: 0 pass, 1 fail, 0 skip, 0 warn"
		oneFail = "conformance: 3 pass, 1 fail, 0 skip, 0 warn"
	)
	for _, c := range []struct {
		name     string
		now, was string
		want     []nonGatingShift
	}{
		{"a step that failed and did not before", "WARN conformance_live (rc 2, non-gating): " + live15 + ", 12s",
			"conformance_live (non-gating) agy 1.5.14 live tier skipped, 12s",
			[]nonGatingShift{{step: "conformance_live", summary: live15}}},
		{"the same step with a different fail count", "WARN conformance_live (rc 2, non-gating): " + live15,
			"conformance_live (non-gating) " + live14,
			[]nonGatingShift{{step: "conformance_live", summary: live15, was: live14}}},
		{"the same fail count, more passes", "WARN conformance_live (rc 2, non-gating): " + oneFail,
			"WARN conformance_live (rc 2, non-gating): conformance: 1 pass, 1 fail, 0 skip, 0 warn", nil},
		{"the same failure", "WARN race (rc 2, non-gating): 1 data race report(s), 0 package(s) with failing tests under -race",
			"WARN race (rc 2, non-gating): 1 data race report(s), 0 package(s) with failing tests under -race", nil},
		{"a fail count with rc 0", "conformance (non-gating) " + oneFail,
			"conformance (non-gating) conformance: 4 pass, 0 fail, 0 skip, 0 warn",
			[]nonGatingShift{{step: "conformance", summary: oneFail}}},
		{"no previous check", "WARN prune (non-gating): no serve listening", "",
			[]nonGatingShift{{step: "prune", summary: "no serve listening"}}},
		{"one shifted step beside a passing one", "conformance (non-gating) conformance: 65 pass, 0 fail, 0 skip, 0 warn, " +
			"WARN conformance_live (rc 2, non-gating): " + live15 + ", 12s",
			"conformance (non-gating) conformance: 65 pass, 0 fail, 0 skip, 0 warn, conformance_live (non-gating) agy 1.5.14 live tier skipped, 12s",
			[]nonGatingShift{{step: "conformance_live", summary: live15}}},
		{"nothing failed", "check 7 pass, 0 fail, conformance (non-gating) conformance: 65 pass, 0 fail, 0 skip, 0 warn, 12s",
			"check 7 pass, 0 fail, 12s", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := nonGatingShifts(c.now, c.was); !slices.Equal(got, c.want) {
				t.Errorf("shifts %+v, want %+v", got, c.want)
			}
		})
	}
}

// A GREEN check's line names each changed step, its summary, the previous one (ok when it did not fail), and serve's
// log once; no shift, no line.
func TestNightlyGreenLine(t *testing.T) {
	t.Parallel()
	const now = "check 7 pass, 0 fail, conformance (non-gating) 65 pass, 0 fail, 1 skip, 0 warn, " +
		"WARN conformance_live (rc 2, non-gating): agy 1.5.15 live tier not recorded: conformance: 11 pass, 8 fail, 0 skip, 0 warn, " +
		"WARN prune (non-gating): no serve listening, 12s"
	const was = "check 7 pass, 0 fail, conformance (non-gating) 65 pass, 0 fail, 1 skip, 0 warn, " +
		"conformance_live (non-gating) agy 1.5.14 live tier not recorded: conformance: 0 pass, 1 fail, 0 skip, 0 warn, 12s"
	out := NightlyOut{SHA: "abc1234def", Verdict: "GREEN", Summary: now, Was: was}
	want := "[obor] nightly GREEN abc1234: conformance_live agy 1.5.15 live tier not recorded: conformance: 11 pass, 8 fail, " +
		"0 skip, 0 warn (was: agy 1.5.14 live tier not recorded: conformance: 0 pass, 1 fail, 0 skip, 0 warn); " +
		"prune no serve listening (was: ok) — log: " + filepath.Join("/p", ".obor", "state", "logs", "serve.log")
	if got := nightlyGreenLine("/p", out); got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	out.Was = now
	if got := nightlyGreenLine("/p", out); got != "" {
		t.Errorf("the same failure lined %q, want none", got)
	}
}

// replayProj reloads p from its log in a new App, as a restart does.
func replayProj(t *testing.T, r *concRig, p *proj) (*App, *proj) {
	t.Helper()
	b := newTestApp(Options{Now: r.a.opt.Now, Loop: r.a.opt.Loop}, &fakePanes{}, &fakeLanes{})
	q, err := b.load(p.root)
	if err != nil {
		t.Fatal(err)
	}
	b.p = q
	t.Cleanup(func() { q.log.Close(); b.closeLog() })
	return b, q
}

// notifyNightly appends an old log's notification intent of p's nightly needs-you in force, with its result when done
// (nothing runs these steps now; the replay must still ignore them).
func notifyNightly(t *testing.T, r *concRig, p *proj, done bool) {
	t.Helper()
	if len(p.folded.active[event.NightlyTask]) == 0 {
		t.Fatal("precondition: no nightly needs-you in force")
	}
	step := "notify." + fmt.Sprint(p.folded.active[event.NightlyTask][0].Seq)
	if _, err := p.log.Append(event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindIntent, Class: event.ClassFact}, r.a.opt.Now()); err != nil {
		t.Fatal(err)
	}
	if done {
		raw, _ := json.Marshal("notified")
		if _, err := p.log.Append(event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindResult, Data: raw}, r.a.opt.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// The nightly's notification records (notify.<seq>, task key nightly) never move its run cursor: a
// restart replays the run's own step, and a day with a result, or with tries failures, runs no further check.
func TestNightlyNotificationsKeepRunCursor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		build func(t *testing.T, r *concRig, p *proj, day string) // the log before the restart
		done  bool                                                // the replayed run has a result
		fails int                                                 // the replayed day's counted failures
	}{
		{"completed RED + pending notification", func(t *testing.T, r *concRig, p *proj, day string) {
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindResult}, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			r.a.nightlyVerdict(p, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			notifyNightly(t, r, p, false)
		}, true, 0},
		{"three failures + completed notification", func(t *testing.T, r *concRig, p *proj, day string) {
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			for i := 0; i < tries; i++ {
				r.a.nightlyFailed(p, day, errors.New("cannot take the lock"))
			}
			notifyNightly(t, r, p, true)
		}, false, tries},
		{"a pending notification after a failed run", func(t *testing.T, r *concRig, p *proj, day string) {
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			for i := 0; i < tries; i++ {
				r.a.nightlyFailed(p, day, errors.New("cannot take the lock"))
			}
			notifyNightly(t, r, p, false)
		}, false, tries},
		{"a prior day's notification interleaved with a later run", func(t *testing.T, r *concRig, p *proj, day string) {
			prior := event.StepNightly + "." + r.a.opt.Now().Local().AddDate(0, 0, -1).Format("2006-01-02")
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: prior, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: prior, Kind: event.KindResult}, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			r.a.nightlyVerdict(p, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			notifyNightly(t, r, p, false)
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindResult}, NightlyOut{SHA: "abc1234", Verdict: "RED"})
		}, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			p := r.proj("pj")
			now := r.a.opt.Now().Local()
			if now.Hour() < 2 {
				t.Fatalf("precondition: the rig's clock %v is before merge.nightly_at 02:00", now)
			}
			day := event.StepNightly + "." + now.Format("2006-01-02")
			c.build(t, r, p, day)
			b, q := replayProj(t, r, p)
			if q.night.step != day || q.night.done != c.done || q.night.fails[day] != c.fails {
				t.Errorf("replayed run step=%s done=%v fails=%d; want %s %v %d", q.night.step, q.night.done,
					q.night.fails[day], day, c.done, c.fails)
			}
			runs := 0
			n := &Nightly{At: "02:00", Run: func(context.Context) (NightlyOut, error) {
				runs++
				return NightlyOut{SHA: "abc1234", Verdict: "GREEN"}, nil
			}}
			b.nightly(context.Background(), q, withAppCfg(b, &Loop{Nightly: n}))
			b.effects.Wait()
			if runs != 0 {
				t.Errorf("nightly ran %d more times after the restart, want 0", runs)
			}
			recs, err := event.ReadLog(p.root)
			if err != nil {
				t.Fatal(err)
			}
			for _, rec := range recs {
				if rec.Task == event.NightlyTask && rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "notify.") && string(rec.Data) != `"notified"` {
					t.Errorf("a nightly outcome recorded under its notification's key %s: %s", rec.Step, rec.Data)
				}
			}
		})
	}
}
