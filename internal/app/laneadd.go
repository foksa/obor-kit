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

// laneAddPass starts the add of each configured lane whose worktree the lane table shows missing and no task holds,
// one attempt a lane at a time. A failed add or setup is that lane's needs-you, naming the lane, the error and
// `obor setup lane <LANE>`; the next try waits StepRetry, doubling per failure in a row up to StepRetryMax (as a cut
// step's, no spin). A table that does not name every configured lane (a count raised since its read) is read again
// first, so a raised count needs no other pass's scan. A missing lane a task holds is left as it is: its worktree is
// a worker's.
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
	for _, l := range fx.Cfg.Lanes() {
		row, ok := byName[l.Name]
		if !ok {
			continue
		}
		if row.State != port.LaneMissing {
			a.clearLaneAdd(p, l.Name) // added by this serve or by hand: its failure is over
			continue
		}
		lane := l.Name
		a.mu.Lock()
		if p.laneAdds == nil {
			p.laneAdds = map[string]*laneAdd{}
		}
		if p.laneHolds(lane) != "" {
			a.mu.Unlock()
			continue
		}
		add := p.laneAdds[lane]
		if add != nil && (add.running || now.Before(add.next)) {
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

// clearLaneAdd ends lane's retry state and its needs-you once its worktree is no longer missing (this serve's add
// finished, or someone made it): a running add is left to its own end.
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

// laneAdded ends lane's attempt (err nil: its worktree is there and its setup ran): the retry state goes and the next
// read of the lane table is due, else the failure becomes the lane's needs-you and the next try waits its delay.
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
