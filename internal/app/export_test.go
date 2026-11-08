package app

import (
	"context"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"strings"
	"time"

	"obor/internal/store"
)

// StartIntegrator is startIntegrator for the external tests.
var StartIntegrator = (*App).startIntegrator

// JobTimer is the timer of task's integrator job in a's project at root: its job id ("": none), since when the
// integrator is seen gone (zero: it runs) and the quiet time's baseline (zero: none).
func JobTimer(a *App, root, task string) (job string, gone, quiet time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.projAt(root).timers[task]
	if t == nil {
		return "", time.Time{}, time.Time{}
	}
	return t.job, t.gone, t.quietSince
}

// Settled: no step of task runs in a's project at root (none held, no gate or publish effect of it in flight).
func Settled(a *App, root, task string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	return p == nil || p.held[task] == "" && p.running.Task != task
}

// Refuse makes the event log of a's project at root refuse each append refuse returns an error for (crash tests: an
// effect ran, its result never reached the log).
func Refuse(a *App, root string, refuse func(event.Record) error) {
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	p.log.Mu.Lock()
	p.log.Refuse = refuse
	p.log.Mu.Unlock()
}

// Quiet is the stall timer of task's watch in a's project at root: its baseline (zero: none) and the newest
// transcript write accounted for (zero: not sampled yet).
func Quiet(a *App, root, task string) (quietSince, seenWrite time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil || p.tasks[task] == nil {
		return time.Time{}, time.Time{}
	}
	return p.tasks[task].quietSince, p.tasks[task].seenWrite
}

// Counts is how many full checks and merge loop passes a has done.
func Counts(a *App) (checks, passes int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checks, a.passes
}

// StreamPasses is how many merge loop passes a has done connected to one herdr stream from start to end (T-861):
// the passes a test that moved the clock or changed herdr waits on.
func StreamPasses(a *App) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.onStream
}

// Follows is how many binary-watch ticks a has counted (Options.Follow).
func Follows(a *App) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.follows
}

// Admits is the admissions in force on a's project at root (p.admits, T-934).
func Admits(a *App, root string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.projAt(root).admits
}

// Admit is one admission in force on a's project at root, as a config_set's or a review close's call holds it until
// its commit is in; release ends it. The project must be served and no publish running (T-930).
func Admit(a *App, root string) (release func(), err error) {
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	if p == nil {
		return nil, errors.New("no served project at " + root)
	}
	return a.admit(context.Background(), p)
}

// WriteLog appends recs (stamped now) to the event log of root, whose serve is stopped (a crash left them).
func WriteLog(root string, now time.Time, recs ...event.Record) error {
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		return err
	}
	defer db.Close()
	log, _, err := event.OpenLog(db)
	for _, r := range recs {
		if err != nil {
			break
		}
		_, err = log.Append(r, now)
	}
	return err
}

// DiskEvery is how often the free space is read on a pass (disk.go).
const DiskEvery = diskEvery

// IsIdentified is whether serve identifies agent in a's project at root (whereIs: a running agent it can address).
func IsIdentified(a *App, root, agent string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	return p != nil && a.whereIs(p, agent).State == Identified
}

// WhereOf is whereIs(agent) in a's project at root as a line: "identified <pane> <source>", "ambiguous <panes>",
// "unavailable <why>".
func WhereOf(a *App, root, agent string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil {
		return "no project"
	}
	w := a.whereIs(p, agent)
	switch w.State {
	case Identified:
		return "identified " + w.Pane.ID + " " + w.Source
	case Ambiguous:
		return "ambiguous " + strings.Join(w.Panes, " ")
	}
	return "unavailable " + w.Why
}

// WatchedPane is the pane the watchdog last saw task's agent in ("": none yet, or not watched).
func WatchedPane(a *App, root, task string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil || p.tasks[task] == nil {
		return ""
	}
	return p.tasks[task].agent.ID
}

// WorkerStartSettle is how long the worker start rule waits on a worker's pane at its shell (T-874).
const WorkerStartSettle = workerStartSettle

// CachedLane is lane name as a's lane table last scanned it in the project at root (T-879; ok false: not in it).
func CachedLane(a *App, root, name string) (l port.Lane, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil {
		return port.Lane{}, false
	}
	for _, x := range p.scan.ls {
		if x.Name == name {
			return x, true
		}
	}
	return port.Lane{}, false
}

// NeedsSeq is the needs-you seq a needs rule line's did key names (0: another line).
func NeedsSeq(of event.Of) int64 {
	if of.Kind != ruleNeeds {
		return 0
	}
	return of.Seq / needsSpan
}

// ReplayLanes loads the project at root as serve does on start, with no herdr and no lanes source, and closes it: its
// log's replay writes the lanes table (T-439), the rows the lane table and dispatch read (T-1015's fold test).
func ReplayLanes(root string, now time.Time) {
	a := New(Options{Root: root, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
	a.loadProject()
	a.closeLog()
}

// PickLane is pickLane for the external tests: the lane a dispatch takes of ls, its model ref, why none.
var PickLane = pickLane
