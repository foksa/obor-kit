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
			b.nightly(context.Background(), q, &Loop{Nightly: n})
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
