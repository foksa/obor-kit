package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"

	"obor/internal/config"
)

// The nightly full check (T-464; docs/state-model.md row 45): once a day, at or after merge.nightly_at, when a
// partial merge happened since the last full check (or the last one is RED) and no gate or publish runs, the merge
// loop runs what obor nightly runs, in its own worktree (not the scratch). Its intent/result is the day's key
// nightly.<YYYY-MM-DD> (G): a restart runs a pending one again, a day with a result runs none. A RED one is the
// project's needs-you{full-check RED <sha7>} until a GREEN one ends it; a run that ends neither GREEN nor RED is a
// counted failure (fact failed), tried again nightlyRetry after the last one, the tries-th the day's needs-you{nightly
// failed: …}. A day not due is asked once (remembered in memory); a GREEN full check run by hand (obor nightly, obor
// milestone-check: last-full-check) ends a RED one's needs-you too.

// NightlyTask is the task key of a project's nightly full check: its steps and its needs-you, the project's.
const NightlyTask = "nightly"

// StepNightly is the nightly full check's intent/result (G), one key a day: nightly.<YYYY-MM-DD> → NightlyOut.
const StepNightly = "nightly"

// nightlyRetry is how long after a failed nightly run its next try waits (a transient cause: a git lock, a worktree
// another process holds, merge.window_wait).
const nightlyRetry = 10 * time.Minute

// redPrefix starts the reason of a RED nightly's needs-you.
const redPrefix = "full-check RED "

// Nightly is a project's nightly full check (merge.nightly_at). Nil in Loop: off.
type Nightly struct {
	At string // "HH:MM", local time
	// Due: a partial merge of merges (the log's, in seq order) since the last full check, or the last one RED. fresh:
	// the log has neither a merge nor a nightly run (a merges.log before the store is not read: due once, T-525).
	Due func(merges []Merge, fresh bool) (bool, error)
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

// Merge is one merged fact of the merge loop's (a merge outside the app has no sha and is none): its task, sha, time
// and whether its gate ran the full check (false in a log before T-525: partial).
type Merge struct {
	Task, SHA string
	At        time.Time
	Full      bool
}

// merge is r as a Merge (false: no merged fact, or a merge outside the app).
func merge(r Record) (Merge, bool) {
	if r.Kind != KindFact || r.Step != StepMerged {
		return Merge{}, false
	}
	var d mergedData
	if json.Unmarshal(r.Data, &d) != nil || d.Head == "" {
		return Merge{}, false
	}
	return Merge{Task: r.Task, SHA: d.Head, At: recordTime(r), Full: d.Full}, true
}

// ReadMerges is root's merges, read-only in seq order (obor flakes); none when serve never made its store.
func ReadMerges(root string) ([]Merge, error) {
	recs, err := ReadLog(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var ms []Merge
	for _, r := range recs {
		if m, ok := merge(r); ok {
			ms = append(ms, m)
		}
	}
	return ms, err
}

// nightlyState is the nightly full check's fold: its last intent's step and whether it has a result, the counted
// failures per step and the time of the last one, the last outcome while no needs-you or resumed fact has followed it,
// whether a run is going, the day's step Due last answered false for, and the project's merges (Due's input).
type nightlyState struct {
	merges    []Merge
	step      string
	done      bool
	fails     map[string]int
	failedAt  time.Time
	unsettled *NightlyOut
	running   bool   // runNightly runs (under mu; only the merge loop's pass starts one)
	notDue    string // in memory only: a restart asks again
}

// foldNightly folds r, a record of the nightly full check or a merged fact (its merges). Only nightly.<date> steps
// are its runs: a notification of its needs-you (notify.<seq>, the same task key) never moves the run cursor (T-468).
func (p *proj) foldNightly(r Record) {
	if m, ok := merge(r); ok {
		p.night.merges = append(p.night.merges, m)
	}
	if r.Task != NightlyTask {
		return
	}
	n := &p.night
	run := strings.HasPrefix(r.Step, StepNightly+".")
	switch {
	case r.Kind == KindIntent && run:
		n.step, n.done = r.Step, false
	case r.Kind == KindResult && run && r.Step == n.step:
		n.done = true
		var out NightlyOut
		if json.Unmarshal(r.Data, &out) == nil && out.Verdict != "" {
			n.unsettled = &out
		}
	case r.Kind == KindFact && r.Step == StepFailed:
		if n.fails == nil {
			n.fails = map[string]int{}
		}
		n.fails[r.Fact().Step]++
		n.failedAt = recordTime(r)
	case r.Kind == KindFact && (r.Step == StepNeedsYou || r.Step == StepResumed):
		n.unsettled = nil
	}
}

// nightly starts p's nightly full check in its own goroutine when it is due, after settling the outcome of the last
// one (its needs-you).
func (a *App) nightly(ctx context.Context, p *proj, fx *Loop) {
	n := fx.Nightly
	if n == nil {
		return
	}
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
	a.handGreen(p, n)
	if busy {
		return
	}
	now := a.opt.Now().Local()
	step := st.step // a pending intent runs again (G), unless it failed tries times (that day's needs-you)
	if step == "" || st.done || spent {
		h, m, ok := config.ParseClock(n.At)
		if !ok || now.Before(time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())) {
			return
		}
		step = StepNightly + "." + now.Format("2006-01-02")
		if step == st.step || step == st.notDue {
			return // today's has run, or failed tries times, or is not due
		}
		due, err := n.Due(st.merges, len(st.merges) == 0 && st.step == "")
		if err != nil {
			a.logf("nightly of %s: %v", p.name, err)
			return
		}
		if !due {
			a.mu.Lock()
			p.night.notDue = step
			a.mu.Unlock()
			return
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
// without a partial merge since, serve's own nightly would not run to end it.
func (a *App) handGreen(p *proj, n *Nightly) {
	if n.Last == nil {
		return
	}
	a.mu.Lock()
	red := false
	for _, f := range p.folded.active[NightlyTask] {
		red = red || f.Step == StepNeedsYou && strings.HasPrefix(f.Fact().Reason, redPrefix)
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
		a.record(p, Record{Task: NightlyTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine})
	}
}

// runNightly runs the nightly full check under step's intent, then records its needs-you or its failure.
func (a *App) runNightly(ctx context.Context, p *proj, n *Nightly, step string) {
	in := Intent{Key: Key{NightlyTask, 0, step}, Class: ClassGit, Data: struct {
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
		if ctx.Err() == nil {
			a.nightlyFailed(p, step, err)
		}
		return
	}
	var out NightlyOut
	if json.Unmarshal(raw, &out) == nil {
		a.nightlyVerdict(p, out)
	}
}

// nightlyFailed counts err, the failure of step (fact failed); the tries-th is the needs-you{nightly failed: why}.
func (a *App) nightlyFailed(p *proj, step string, err error) {
	if !a.record(p, Record{Task: NightlyTask, Step: StepFailed, Kind: KindFact},
		FactData{Machine: Machine, Reason: step + ": " + err.Error(), Step: step}) {
		return
	}
	a.mu.Lock()
	tell := p.night.fails[step] >= tries
	a.mu.Unlock()
	if tell {
		a.nightlyNeedsYou(p, StepNightly+failedSuffix+": "+err.Error())
	}
}

// nightlyVerdict settles out: RED is the project's needs-you{full-check RED <sha7>} (once; another replaces it),
// GREEN ends the one in force.
func (a *App) nightlyVerdict(p *proj, out NightlyOut) {
	if out.Verdict == "RED" {
		a.nightlyNeedsYou(p, redPrefix+out.SHA[:min(7, len(out.SHA))])
		return
	}
	a.mu.Lock()
	held := len(p.folded.active[NightlyTask]) > 0
	a.mu.Unlock()
	if !held {
		a.mu.Lock()
		p.night.unsettled = nil
		a.mu.Unlock()
		return
	}
	a.record(p, Record{Task: NightlyTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine})
}

// nightlyNeedsYou records the nightly's needs-you reason, ending another in force; the same one in force: nothing.
func (a *App) nightlyNeedsYou(p *proj, reason string) {
	a.mu.Lock()
	held, same := len(p.folded.active[NightlyTask]) > 0, p.has(NightlyTask, StepNeedsYou, reason)
	if same {
		p.night.unsettled = nil
	}
	a.mu.Unlock()
	if same {
		return
	}
	if held && !a.record(p, Record{Task: NightlyTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine}) {
		return
	}
	a.record(p, Record{Task: NightlyTask, Step: StepNeedsYou, Kind: KindFact}, FactData{Machine: Machine, Reason: reason})
}
