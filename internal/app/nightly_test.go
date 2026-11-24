package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"obor/internal/app/event"
)

// A GREEN check's line: one clause per non-gating step whose outcome differs from the previous check's — failing
// where it passed or did not run (the first check ever: no previous), or failing with another fail count, or with no
// count on either side a different report — with its report, its previous outcome (ok, or the report then) and its
// log, or its summary line when no log could be written; the same failure (the same count, or the same report when
// neither has one), a passing step and a failing gating step line nothing.
func TestNightlyGreenLine(t *testing.T) {
	t.Parallel()
	n := func(v int) *int { return &v }
	const log = "/p/.obor/state/logs/nightly-conformance.log"
	failing := func(fails *int, report, log string) CheckStep {
		return CheckStep{Name: "conformance", Fails: fails, Report: report, Log: log,
			Line: "WARN conformance (rc 1, non-gating): " + report}
	}
	passing := CheckStep{Name: "conformance", OK: true, Fails: n(0), Report: "4 pass, 0 fail", Line: "conformance (non-gating) 4 pass, 0 fail"}
	race := CheckStep{Name: "race", Report: "1 data race report(s)", Line: "WARN race (rc 2, non-gating): 1 data race report(s)",
		Log: "/p/.obor/state/logs/nightly-race.log"}
	race2 := CheckStep{Name: "race", Report: "2 data race report(s)",
		Line: "WARN race (rc 2, non-gating): 2 data race report(s)", Log: "/p/.obor/state/logs/nightly-race.log"}
	budget := CheckStep{Name: "check_budget", Report: "budget OK: 12 packages",
		Line: "check_budget (non-gating) budget OK: 12 packages", Log: "/p/.obor/state/logs/nightly-check_budget.log"}
	budgetBad := CheckStep{Name: "check_budget", Report: "FAIL budget: internal/app 3.2s over 2.0s",
		Line: "WARN check_budget (rc 1, non-gating): FAIL budget: internal/app 3.2s over 2.0s",
		Log:  "/p/.obor/state/logs/nightly-check_budget.log"}
	prune := CheckStep{Name: "prune", Report: "no serve listening", Line: "WARN prune (non-gating): no serve listening",
		Log: "/p/.obor/state/logs/serve.log"}
	const head = "[obor] nightly GREEN abc1234: "
	for _, c := range []struct {
		name     string
		now, was []CheckStep
		want     string
	}{
		{"newly failing", []CheckStep{failing(n(1), "3 pass, 1 fail", log)}, []CheckStep{passing},
			head + "conformance 3 pass, 1 fail (was: ok), log " + log},
		{"first check ever", []CheckStep{failing(n(1), "3 pass, 1 fail", log)}, nil,
			head + "conformance 3 pass, 1 fail (was: ok), log " + log},
		{"another fail count", []CheckStep{failing(n(8), "11 pass, 8 fail", log)}, []CheckStep{failing(n(1), "0 pass, 1 fail", log)},
			head + "conformance 11 pass, 8 fail (was: 0 pass, 1 fail), log " + log},
		{"a worse race report, no count on either side", []CheckStep{race2}, []CheckStep{race},
			head + "race 2 data race report(s) (was: 1 data race report(s)), log /p/.obor/state/logs/nightly-race.log"},
		{"a changed budget report, no count on either side", []CheckStep{budgetBad}, []CheckStep{budget},
			head + "check_budget FAIL budget: internal/app 3.2s over 2.0s (was: budget OK: 12 packages), log /p/.obor/state/logs/nightly-check_budget.log"},
		{"the same fail count, more passes", []CheckStep{failing(n(1), "9 pass, 1 fail", log)},
			[]CheckStep{failing(n(1), "3 pass, 1 fail", log)}, ""},
		{"the same race report twice, no count", []CheckStep{race}, []CheckStep{race}, ""},
		{"a passing step", []CheckStep{passing}, []CheckStep{failing(n(1), "3 pass, 1 fail", log)}, ""},
		{"a failing gating step", []CheckStep{{Name: "check", Gating: true, Report: "rc=1", Log: log}}, nil, ""},
		{"a zero-exit step reporting failures: its log", []CheckStep{{Name: "conformance", Fails: n(2), Report: "5 pass, 2 fail",
			Line: "conformance (non-gating) 5 pass, 2 fail", Log: log}}, []CheckStep{passing},
			head + "conformance 5 pass, 2 fail (was: ok), log " + log},
		{"no log written: its summary line", []CheckStep{failing(n(1), "3 pass, 1 fail", "")}, []CheckStep{passing},
			head + "conformance 3 pass, 1 fail (was: ok), no log: WARN conformance (rc 1, non-gating): 3 pass, 1 fail"},
		{"two changed steps beside an unchanged one", []CheckStep{failing(n(1), "3 pass, 1 fail", log), race, prune},
			[]CheckStep{passing, race},
			head + "conformance 3 pass, 1 fail (was: ok), log " + log + "; prune no serve listening (was: ok), log /p/.obor/state/logs/serve.log"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := nightlyGreenLine(FullCheck{SHA: "abc1234def", Verdict: "GREEN", Steps: c.now, Was: c.was})
			if got != c.want {
				t.Errorf("line %q, want %q", got, c.want)
			}
		})
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
