package app

import (
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
func Refuse(a *App, root string, refuse func(Record) error) {
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	p.log.mu.Lock()
	p.log.refuse = refuse
	p.log.mu.Unlock()
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

// WriteLog appends recs (stamped now) to the event log of root, whose serve is stopped (a crash left them).
func WriteLog(root string, now time.Time, recs ...Record) error {
	db, err := store.OpenRW(DBFile(root))
	if err != nil {
		return err
	}
	defer db.Close()
	log, _, err := OpenLog(db)
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
