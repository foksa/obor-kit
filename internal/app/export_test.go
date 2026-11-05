package app

import (
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

// NeedsLines is needsLines for the external tests.
var NeedsLines = (*App).needsLines

// DiskEvery is how often the free space is read on a pass (disk.go).
const DiskEvery = diskEvery

// SetHook sets a's test hook: f runs at each point App.hook names (drive.go's hook points), on the caller's goroutine.
func SetHook(a *App, f func(point string)) { a.hooked.Store(&f) }

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
