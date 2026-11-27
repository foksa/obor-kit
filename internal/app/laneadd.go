// laneadd.go: serve adds a configured lane's missing worktree, as `obor setup lane <LANE>` does for it
// (Loop.EnsureLane, one code path with the CLI), off the pass: a worktree add and lanes.setup can take a minute.
package app

import (
	"context"
	"fmt"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/config"
)

// laneAdd is one lane's add: whether an attempt runs now, how many failed in a row, when the next may start and the
// timer that wakes the loop for the retry. Under mu.
type laneAdd struct {
	running bool
	fails   int
	next    time.Time
	timer   *time.Timer
}

// laneAddPass starts the add of each configured lane the lane table shows missing and no task holds, one attempt a
// lane at a time. A lane serve is adding is missing in the table until its add (worktree, links, setup) succeeded:
// setup.EnsureLane's marker (project.LaneAddMarker), durable across a restart, is the one completion predicate. A
// failed add or setup is that lane's needs-you, naming the lane, the error and `obor setup lane <LANE>`; the next try
// waits StepRetry, doubling per failure in a row up to StepRetryMax (as a cut step's, no spin), and the needs-you stays
// until the lane reads not missing. A table that does not name every configured lane (a count raised since its read) is
// read again first, so a raised count needs no other pass's scan. A missing lane a task holds is left as it is: its
// worktree is a worker's.
//
// The decision is laneAddStart's, from a fresh read of the table under a.verdicts: the cached row only picks the lanes
// to look at (a stale missing row after a success starts no second add), and the start excludes a dispatch claiming the
// lane (dispatchTask reads the table and records its claim under the same lock).
func (a *App) laneAddPass(ctx context.Context, p *proj, fx *Loop) {
	if fx.EnsureLane == nil || ctx.Err() != nil {
		return
	}
	ls := a.lanesNow(p)
	if !lanesCover(ls, fx.Cfg) {
		a.scan(ctx, p)
		var err error
		if ls, err = a.scanRead(p); err != nil {
			return // not a read of the config's lanes
		}
	}
	byName := make(map[string]port.Lane, len(ls))
	for _, l := range ls {
		byName[l.Name] = l
	}
	now := a.opt.Now()
	var look []string // the lanes whose add a fresh read decides
	for _, l := range fx.Cfg.Lanes() {
		row, ok := byName[l.Name]
		if !ok {
			continue
		}
		missing := row.State == port.LaneMissing
		a.mu.Lock()
		add := p.laneAdds[l.Name]
		held := p.laneHolds(l.Name) != ""
		// The add's goroutine writes running and next under mu: the pass decides from this snapshot, never the live add.
		var running bool
		var next time.Time
		if add != nil {
			running, next = add.running, add.next
		}
		a.mu.Unlock()
		switch {
		case running: // left to its own end
		case add == nil && !missing:
			a.settleNeeds(p, event.LaneTaskPrefix+l.Name, "") // no add of this serve's: one a restart cut, made since
		case missing && held: // its worktree is a worker's
		case missing && now.Before(next): // the backoff: no read before it ends
		default:
			look = append(look, l.Name)
		}
	}
	if len(look) > 0 {
		a.laneAddStart(ctx, p, fx, look, now)
	}
}

// laneAddStart decides each of lanes from one fresh read of the lane table, under a.verdicts (dispatch's lock): a lane
// that reads not missing has no add pending (this serve's finished, or someone made it): its retry state and needs-you
// end (clearLaneAdd); a missing one no task holds, no add of which runs or waits its backoff, starts its add, marked
// running before a.verdicts is released, so no dispatch takes it after.
func (a *App) laneAddStart(ctx context.Context, p *proj, fx *Loop, lanes []string, now time.Time) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	fresh, err := a.lanes.Lanes(ctx, p.root)
	if err != nil {
		return // cannot tell: the next pass reads again
	}
	a.hook(hookLaneAddRead)
	byName := make(map[string]port.Lane, len(fresh))
	for _, l := range fresh {
		byName[l.Name] = l
	}
	for _, lane := range lanes {
		row, ok := byName[lane]
		if !ok || row.State == port.LaneUnknown {
			continue
		}
		if row.State != port.LaneMissing {
			a.clearLaneAdd(p, lane)
			continue
		}
		a.mu.Lock()
		if p.laneAdds == nil {
			p.laneAdds = map[string]*laneAdd{}
		}
		add := p.laneAdds[lane]
		if p.laneHolds(lane) != "" || add != nil && (add.running || now.Before(add.next)) {
			a.mu.Unlock()
			continue
		}
		if add == nil {
			add = &laneAdd{}
			p.laneAdds[lane] = add
		}
		add.running = true
		if add.timer != nil {
			add.timer.Stop()
			add.timer = nil
		}
		a.mu.Unlock()
		a.effects.Add(1)
		go func() {
			defer a.effects.Done()
			a.laneAdded(p, lane, fx.EnsureLane(ctx, lane))
		}()
	}
}

// clearLaneAdd ends lane's retry state and its needs-you once a fresh read of the lane table shows it not missing (no
// add pending: this serve's add finished, or someone made it): a running add is left to its own end.
func (a *App) clearLaneAdd(p *proj, lane string) {
	a.mu.Lock()
	add := p.laneAdds[lane]
	if add != nil && add.running {
		a.mu.Unlock()
		return
	}
	if add != nil {
		if add.timer != nil {
			add.timer.Stop()
		}
		delete(p.laneAdds, lane)
	}
	a.mu.Unlock()
	a.settleNeeds(p, event.LaneTaskPrefix+lane, "")
}

// maskAdding marks unknown each lane of ls whose add runs now (under mu): its marker may not be written yet, so the
// table read before cannot say; the dispatch and drive that pick a lane apply it under a.verdicts.
func (p *proj) maskAdding(ls []port.Lane) {
	for i := range ls {
		if add := p.laneAdds[ls[i].Name]; add != nil && add.running {
			ls[i].State, ls[i].Reason = port.LaneUnknown, "serve is adding the lane's worktree"
		}
	}
}

// lanesNow is p's lane table as last read.
func (a *App) lanesNow(p *proj) []port.Lane {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]port.Lane(nil), p.lanes...)
}

// scanRead is p's lane table as the last scan (a.scan) just read it, and that read's error: the merge loop's own read
// does not install the table (decideLanes does, on the next check).
func (a *App) scanRead(p *proj) ([]port.Lane, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]port.Lane(nil), p.scan.ls...), p.scan.err
}

// lanesCover: every configured lane has a row in the table ls.
func lanesCover(ls []port.Lane, cfg *config.Config) bool {
	seen := make(map[string]bool, len(ls))
	for _, l := range ls {
		seen[l.Name] = true
	}
	for _, l := range cfg.Lanes() {
		if !seen[l.Name] {
			return false
		}
	}
	return true
}

// laneAdded ends lane's attempt (err nil: its worktree is there, its setup ran and its marker is gone): the retry state
// goes, the needs-you settles and the next read of the lane table is due, else the failure becomes the lane's needs-you
// (its marker stays: the lane reads missing) and the next try waits its delay.
func (a *App) laneAdded(p *proj, lane string, err error) {
	now := a.opt.Now()
	reason := ""
	delay := time.Duration(0)
	a.mu.Lock()
	add := p.laneAdds[lane]
	if add == nil {
		add = &laneAdd{}
		p.laneAdds[lane] = add
	}
	add.running = false
	if err == nil {
		delete(p.laneAdds, lane)
		a.markLanes(p) // the next read says the lane free
	} else {
		add.fails++
		delay = a.opt.StepRetry
		for i := 1; i < add.fails && delay < a.opt.StepRetryMax; i++ {
			delay *= 2
		}
		delay = min(delay, a.opt.StepRetryMax)
		add.next = now.Add(delay)
		add.timer = time.AfterFunc(delay, a.wake)
		reason = fmt.Sprintf("lane %s: %v (obor setup lane %s)", lane, err, lane)
	}
	a.mu.Unlock()
	if err == nil {
		a.logf("lane %s: worktree added and set up", lane)
	} else {
		a.logf("lane %s: %v (obor setup lane %s); tried again in %s", lane, err, lane, delay)
	}
	a.settleNeeds(p, event.LaneTaskPrefix+lane, reason)
	a.wake()
}
