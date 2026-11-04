package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"strings"
	"time"

	"obor/internal/config"
)

// The nightly full check (T-464, T-781, T-782; docs/state-model.md row 45): merge.nightly_at is its switch, the lead's
// (obor nightly on HH:MM | off, through config_set): set, the merge loop runs what obor nightly runs every night at or
// after that time, in its own worktree (not the scratch), while no gate or publish runs. Off stops new runs, not
// settling: every pass settles a result's missing needs-you and a hand GREEN, on or off. Nothing else decides whether
// it runs (the nightly runs more than the merge gate: check_milestone, the baseline, the forced gate, e2e, the store
// backup, then merge.nightly_commands, each non-gating, T-817). The Loop is built each pass from the config as it is,
// so a switch counts from the next pass; a run in progress is not stopped by off. Its intent/result is the day's key
// nightly.<YYYY-MM-DD> (G): a restart runs a pending one again, a day with a result runs none. A RED one is the
// project's needs-you{full-check RED <sha7>} until a GREEN one ends it; a run that ends neither GREEN nor RED is a
// counted failure (fact failed), tried again nightlyRetry after the last one, the tries-th the day's needs-you{nightly
// failed: …}. A GREEN full check run by hand (obor nightly: last-full-check) ends a RED one's needs-you too.

// nightlyRetry is how long after a failed nightly run its next try waits (a transient cause: a git lock, a worktree
// another process holds, merge.window_wait).
const nightlyRetry = 10 * time.Minute

// redPrefix starts the reason of a RED nightly's needs-you.
const redPrefix = "full-check RED "

// Nightly is a project's nightly full check (merge.nightly_at); a merge loop's project always has one. Off is At "":
// no new run, but the pass still settles one (T-782).
type Nightly struct {
	At  string                                        // "HH:MM", local time: it runs every night at or after it; "": off
	Run func(ctx context.Context) (NightlyOut, error) // obor nightly's run; an error: it ended neither GREEN nor RED
	// Last is the last full check's sha and verdict (last-full-check: GREEN | RED, "" none), a hand run's too (nil:
	// not read).
	Last func() (sha, verdict string, err error)
}

// NightlyOut is a nightly run's outcome: GREEN or RED on the sha it checked, and its one line.
type NightlyOut struct {
	SHA     string `json:"sha"`
	Verdict string `json:"verdict"` // GREEN | RED
	Line    string `json:"line"`
}

// nightlyState is the nightly full check's fold: its last intent's step and whether it has a result, the counted
// failures per step and the time and reason of the last one, the last outcome while no needs-you or resumed fact has
// followed it, and whether a run is going.
type nightlyState struct {
	step      string
	done      bool
	fails     map[string]int
	failedAt  time.Time
	failedWhy string // the last failed fact's reason: "nightly.<date>: <err>" (obor status' pending line, T-662)
	unsettled *NightlyOut
	running   bool // runNightly runs (under mu; only the merge loop's pass starts one)
}

// foldNightly folds r, a record of the nightly full check. Only nightly.<date> steps are its runs: a notification of
// its needs-you (notify.<seq>, the same task key) never moves the run cursor (T-468).
func (p *proj) foldNightly(r event.Record) {
	if r.Task != event.NightlyTask {
		return
	}
	n := &p.night
	run := strings.HasPrefix(r.Step, event.StepNightly+".")
	switch {
	case r.Kind == event.KindIntent && run:
		n.step, n.done = r.Step, false
	case r.Kind == event.KindResult && run && r.Step == n.step:
		n.done = true
		var out NightlyOut
		if json.Unmarshal(r.Data, &out) == nil && out.Verdict != "" {
			n.unsettled = &out
		}
	case r.Kind == event.KindFact && r.Step == event.StepFailed:
		if n.fails == nil {
			n.fails = map[string]int{}
		}
		n.fails[r.Fact().Step]++
		n.failedAt, n.failedWhy = event.RecordTime(r), r.Fact().Reason
	case r.Kind == event.KindFact && (r.Step == event.StepNeedsYou || r.Step == event.StepResumed):
		n.unsettled = nil
	}
}

// nightly settles the outcome of p's last nightly (its needs-you) on every pass, then starts the next run in its own
// goroutine once its time has come today (or its pending intent again). Off (At ""): the settling, no run.
func (a *App) nightly(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	st, busy := p.night, p.night.running || p.running.Task != ""
	spent := st.fails[st.step] >= tries
	a.mu.Unlock()
	if st.running {
		return // the run settles its own outcome
	}
	if st.unsettled != nil { // a crash between the result and its needs-you
		a.nightlyVerdict(p, *st.unsettled)
	}
	n := fx.Nightly
	if n == nil { // a Loop without one (tests)
		return
	}
	a.handGreen(p, n)
	if busy || n.At == "" {
		return
	}
	now := a.opt.Now().Local()
	step := st.step // a pending intent runs again (G), unless it failed tries times (that day's needs-you)
	if step == "" || st.done || spent {
		h, m, ok := config.ParseClock(n.At)
		if !ok || now.Before(time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())) {
			return
		}
		step = event.StepNightly + "." + now.Format("2006-01-02")
		if step == st.step {
			return // today's has run, or failed tries times
		}
	} else if st.fails[step] > 0 && now.Sub(st.failedAt) < nightlyRetry {
		return // a failed try: the next one after nightlyRetry
	}
	if a.diskIsLow(p, fx) {
		return
	}
	a.mu.Lock()
	p.night.running = true
	a.mu.Unlock()
	a.effects.Add(1)
	go func() {
		defer a.effects.Done()
		a.runNightly(ctx, p, n, step)
		a.mu.Lock()
		p.night.running = false
		a.mu.Unlock()
		a.wake()
	}()
}

// handGreen ends a RED nightly's needs-you in force when the last full check, a hand run's too, is GREEN (resumed):
// serve's own nightly would end it only at its next time, or never while it is off.
func (a *App) handGreen(p *proj, n *Nightly) {
	if n.Last == nil {
		return
	}
	a.mu.Lock()
	red := false
	for _, f := range p.folded.active[event.NightlyTask] {
		red = red || f.Step == event.StepNeedsYou && strings.HasPrefix(f.Fact().Reason, redPrefix)
	}
	a.mu.Unlock()
	if !red {
		return
	}
	_, verdict, err := n.Last()
	if err != nil {
		a.logf("nightly of %s: %v", p.name, err)
		return
	}
	if verdict == "GREEN" {
		a.settleNeeds(p, event.NightlyTask, "")
	}
}

// runNightly runs the nightly full check under step's intent, then records its needs-you or its failure.
func (a *App) runNightly(ctx context.Context, p *proj, n *Nightly, step string) {
	in := event.Intent{Key: event.Key{Task: event.NightlyTask, Attempt: 0, Step: step}, Class: event.ClassGit, Data: struct {
		At string `json:"at"`
	}{n.At}}
	raw, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		out, err := n.Run(ctx)
		if err == nil && out.Verdict != "GREEN" && out.Verdict != "RED" {
			err = errors.New("no GREEN or RED: " + out.Line)
		}
		return out, err
	})
	if err != nil {
		if cutByServe(ctx, err) { // serve is stopping: the check was cut, not failed (T-660)
			a.shutDown(event.NightlyTask + " " + event.StepFailed + "{" + step + ": " + err.Error() + "}")
			return
		}
		a.nightlyFailed(p, step, err)
		return
	}
	var out NightlyOut
	if json.Unmarshal(raw, &out) == nil {
		a.nightlyVerdict(p, out)
	}
}

// nightlyFailed counts err, the failure of step (fact failed); the tries-th is the needs-you{nightly failed: why}.
func (a *App) nightlyFailed(p *proj, step string, err error) {
	if !a.record(p, event.Record{Task: event.NightlyTask, Step: event.StepFailed, Kind: event.KindFact},
		event.FactData{Machine: Machine, Reason: step + ": " + err.Error(), Step: step}) {
		return
	}
	a.mu.Lock()
	tell := p.night.fails[step] >= tries
	a.mu.Unlock()
	if tell {
		a.nightlyNeedsYou(p, event.StepNightly+failedSuffix+": "+err.Error())
	}
}

// nightlyVerdict settles out: RED is the project's needs-you{full-check RED <sha7>} (once; another replaces it),
// GREEN ends the one in force.
func (a *App) nightlyVerdict(p *proj, out NightlyOut) {
	if out.Verdict == "RED" {
		a.nightlyNeedsYou(p, redPrefix+out.SHA[:min(7, len(out.SHA))])
		return
	}
	a.nightlySettled(p, "")
}

// nightlyNeedsYou records the nightly's needs-you reason, ending another in force; the same one in force: nothing.
func (a *App) nightlyNeedsYou(p *proj, reason string) {
	a.nightlySettled(p, reason)
}

// nightlySettled settles the nightly's needs-you (reason "" ends it) and, once the log took it, drops the result
// that waited for it (a record folds that away itself; a settle with nothing to record leaves it).
func (a *App) nightlySettled(p *proj, reason string) {
	if !a.settleNeeds(p, event.NightlyTask, reason) {
		return
	}
	a.mu.Lock()
	p.night.unsettled = nil
	a.mu.Unlock()
}
