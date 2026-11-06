// follow.go: serve follows a new obor binary (T-719): on its own tick (Options.Follow, T-737: herdr connected or not,
// the watchdogs' tick counts only while it is) it asks Options.Binary and takes the self-restart path (Loop.Restart),
// preferring a moment when nothing runs.
package app

import (
	"context"
	"obor/internal/app/event"
	"time"
)

// followWait is how long serve waits for an idle moment before it restarts on a new binary anyway: the restart is
// safe at any time (a cut gate runs again), idle only saves the work in flight.
const followWait = 10 * time.Minute

// follow is the binary watch's state: the tick it last asked at (App.follows), since when a new binary is seen (zero:
// none), and whether its restart failed (no further try in this serve: its needs-you says so).
type follow struct {
	tick   int
	since  time.Time
	failed bool
}

// followTicks is the binary watch's timer until ctx ends: each Options.Follow counts a tick (App.follows) and wakes
// the merge loop, whose pass asks (followBinary). It runs off herdr's subscription and the watchdogs' count, so a
// serve started without herdr, or whose stream dropped, still follows, and a pending busy restart's deadline is
// still evaluated (T-737).
func (a *App) followTicks(ctx context.Context) {
	t := time.NewTicker(a.opt.Follow)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		a.follows++
		a.mu.Unlock()
		a.wake()
	}
}

// followBinary restarts serve on a new obor binary (Options.Binary) once per tick that sees it: at once when serve is
// idle (no scratch effect or other effect running, no nightly, no verdict call admitted), else after followWait; never
// while a main-checkout writer is admitted (p.admits: a config_set's or a review close's commit, T-930, T-934),
// whatever followWait says (a writer is bounded by its own request timeout). One a.mu section decides and claims the
// restart (p.restartClaim): admit refuses while it is claimed, so no writer starts under the restart's cancel.
// While it waits, p.restarting shows it in obor status. A failed restart is a needs-you
// "serve restart on the new obor binary failed: <err>" (key serve), ended by the next serve's start; the claim clears
// with p.restarting. True: serve is ending for the restart (the pass stops here). A serve already ending (ctx done: a
// restart canceled it, and a tick and a kick were pending) restarts nothing again.
func (a *App) followBinary(ctx context.Context, p *proj) bool {
	if ctx.Err() != nil {
		return true
	}
	if a.opt.Binary == nil || a.opt.Loop == nil || a.follow.failed {
		return false
	}
	a.mu.Lock()
	tick := a.follows
	a.mu.Unlock()
	if tick == a.follow.tick {
		return false
	}
	a.follow.tick = tick
	if a.follow.since.IsZero() && !a.opt.Binary() {
		return false
	}
	now := a.opt.Now()
	a.mu.Lock()
	if a.follow.since.IsZero() {
		a.follow.since, p.restarting = now, now
	}
	since := a.follow.since
	idle := p.running.Task == "" && len(p.exec) == 0 && !p.night.running && a.admitted == 0
	claim := p.admits == 0 && (idle || now.Sub(since) >= followWait)
	p.restartClaim = claim
	a.mu.Unlock()
	if !claim {
		if since.Equal(now) {
			a.logf("%s: a new obor binary: restarting serve on it once nothing runs (at the latest in %s)", p.name, followWait)
		}
		return false
	}
	fx, err := a.opt.Loop(p.root)
	if err == nil && fx.Restart == nil {
		a.mu.Lock()
		p.restartClaim = false
		a.mu.Unlock()
		return false // no restart wired: nothing to do but show it
	}
	a.logf("%s: restarting serve on the new obor binary", p.name)
	if err == nil {
		err = fx.Restart(ctx)
	}
	if err == nil || ctx.Err() != nil {
		return true
	}
	a.follow.failed = true
	a.mu.Lock()
	p.restarting, p.restartClaim = time.Time{}, false
	a.mu.Unlock()
	a.settleNeeds(p, event.ServeTask, "serve restart on the new obor binary failed: "+err.Error())
	return false
}
