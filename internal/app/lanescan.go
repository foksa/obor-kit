// lanescan.go: the lane table as a cached observation (T-879). A pane event never scans git: it marks the table dirty;
// the refresher (its own goroutine) scans a dirty table, one scan per pass, so a burst of events shares one scan, and
// the run loop then decides on the table it read: a judgement never rests on a table older than the event that asked
// for it. The tick and a connect scan before they decide; obor's own operations (every effect App.do ran: a dispatch's
// prepare, a gate, a publish, a drop's stop, a lane's free) mark it too. A kernel step (dispatch, drive, task_done)
// reads git itself: a cached "free" is never the authority for it.
package app

import (
	"context"
	"obor/internal/app/port"
	"path/filepath"
	"time"
)

// laneScan is a project's lane table as last scanned (under mu).
type laneScan struct {
	ls    []port.Lane // the last good read (nil: none yet)
	at    time.Time   // when ls was read (zero: never)
	err   error       // the last scan's error (nil: it read ls)
	dirty bool        // a scan is due: a pane event or an own operation since the last scan began
	// seq numbers the scans as they start; stored is the newest stored: an older scan ending later is dropped.
	seq, stored int
}

// lanesBound is how old the lane table may be and still be an observation: past it, the watchdogs know nothing of
// the lanes (a failed or missed scan; T-879), as for an unknown lane.
func (a *App) lanesBound() time.Duration { return 2 * a.opt.Tick }

// scan reads p's lanes now (outside mu) and stores them, the newest scan winning; a failed read keeps the last good
// table and its read time, with the error. It covers the marks made before it (a tick's scan leaves the refresher
// nothing to do). The merge loop's lanes source follows each read (loopStatus).
func (a *App) scan(ctx context.Context, p *proj) {
	a.mu.Lock()
	p.scan.dirty = false
	p.scan.seq++
	seq, at := p.scan.seq, a.opt.Now()
	a.mu.Unlock()
	ls, err := a.lanes.Lanes(ctx, p.root)
	if p.merging {
		a.loopStatus(ctx, p, &p.badLanes, err)
	}
	if err != nil {
		a.logf("lanes of %s: %v", p.root, err)
	}
	for i := range ls {
		ls[i].Dir = filepath.Clean(ls[i].Dir)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if seq < p.scan.stored {
		return
	}
	p.scan.stored, p.scan.err = seq, err
	if err == nil {
		p.scan.ls, p.scan.at = ls, at
	}
}

// observed is p's lane table as an observation at now (under mu): false when the last scan failed or the table is
// older than bound (none read yet included).
func (s *laneScan) observed(now time.Time, bound time.Duration) ([]port.Lane, bool) {
	if s.err != nil || s.at.IsZero() || now.Sub(s.at) > bound {
		return nil, false
	}
	return s.ls, true
}

// markLanes marks p's lane table dirty and kicks the refresher (under mu).
func (a *App) markLanes(p *proj) {
	p.scan.dirty = true
	select {
	case a.rescan <- struct{}{}:
	default:
	}
}

// rescanDue is one refresher pass: p's lane table, when dirty, is scanned (true), else nothing. Marks made while it
// scans make the next pass's scan.
func (a *App) rescanDue(ctx context.Context) bool {
	a.mu.Lock()
	p := a.p
	due := p != nil && p.scan.dirty
	a.mu.Unlock()
	if due {
		a.scan(ctx, p)
	}
	return due
}

// refresher scans the project's dirty lane table, one pass at a time, until ctx ends (a goroutine of Serve's own);
// after each scan it hands the run loop a decision on the fresh table (scanned: kicks while one waits coalesce).
func (a *App) refresher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.rescan:
		}
		if a.rescanDue(ctx) {
			select {
			case a.scanned <- struct{}{}:
			default:
			}
		}
	}
}

// lanesNote is obor status' line on p's lane table when it is older than the tick ("": it is not; under mu).
func (a *App) lanesNote(p *proj, now time.Time) string {
	s := p.scan
	if s.at.IsZero() && s.err == nil || now.Sub(s.at) <= a.opt.Tick {
		return ""
	}
	line := "lane table never read"
	if !s.at.IsZero() {
		line = "lane table read " + Age(now.Sub(s.at)) + " ago"
	}
	if s.err != nil {
		line += " (last scan: " + s.err.Error() + ")"
	}
	return line
}
