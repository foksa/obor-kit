// follow.go: serve follows a new obor binary: on its own tick (Options.Follow: herdr connected or not,
// the watchdogs' tick counts only while it is) it asks Options.Binary and takes the self-restart path (Loop.Restart),
// preferring a moment when nothing runs, and waiting for one at most followWait.
package app

import (
	"context"
	"obor/internal/app/event"
	"time"
)

// follow is the binary watch's state: the tick it last asked at (App.follows) and whether its restart failed (no
// further try in this serve: its needs-you says so).
type follow struct {
	tick   int
	failed bool
}

// followTicks is the binary watch's timer until ctx ends: each Options.Follow counts a tick (App.follows) and wakes
// the merge loop, whose pass asks (followBinary). It runs off herdr's subscription and the watchdogs' count, so a
// serve started without herdr, or whose stream dropped, still follows.
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

// followWait bounds a pending self-restart's wait for work that may never end: an effect stuck in the terminal, a lane
// test whose command hangs, a nightly full check. Past it the restart goes anyway and cuts that work; the log keeps its
// intent, so the next serve runs the gate, the test or the nightly again. It is longer than the work a project sets for
// itself (merge.check.timeout is 20 minutes by default), so a slow gate, test or nightly is not cut; nothing cuts a
// main-checkout writer or a verdict call (p.admits, a.admitted), each bounded by its own request.
const followWait = 30 * time.Minute

// followBinary restarts serve on a new obor binary (Options.Binary): each tick asks, and once one is seen the restart
// goes at the first pass where serve is idle (p.restartBusy: no slot step, no effect, no lane test, no nightly; no
// verdict call admitted), and at the latest followWait after it was seen. Nothing new is held back for it. A task
// waiting on a person holds nothing: a needs-you in force is no work of the task's (p.restartBusy), so the owner may
// take hours. Never while a main-checkout writer is admitted (p.admits: a config_set's or a review close's commit),
// whatever runs (a writer is bounded by its own request timeout). One a.mu section decides and claims the restart
// (p.restartClaim): admit refuses while it is claimed, so no writer starts under the restart's cancel. While it waits,
// p.restarting shows it in obor status. A failed restart is a needs-you
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
	first := p.restarting.IsZero() // nothing pending yet: a tick may see the new binary now
	a.mu.Unlock()
	if tick != a.follow.tick { // ask once per tick: a stat of the binary, not one per pass
		a.follow.tick = tick
		if first && !a.opt.Binary() {
			return false
		}
	} else if first {
		return false
	}
	a.mu.Lock()
	now := a.opt.Now()
	if p.restarting.IsZero() {
		p.restarting = now
	}
	// A lane test counts as running too (p.tests): it runs minutes, and a test whose start has not reached
	// p.exec yet is still no idle moment. A new one starts beside the pending restart, and the restart goes
	// in the first pass (the gap) after the last ends, or at followWait (p.restartBusy).
	claim := p.admits == 0 && a.admitted == 0 && (!p.restartBusy() || now.Sub(p.restarting) >= followWait)
	p.restartClaim = claim
	a.mu.Unlock()
	if !claim {
		if first {
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

// restartBusy is the work a pending self-restart waits for (under mu): a slot step, an effect running (p.exec: a gate,
// a lane test, a prompt, a start, the nightly's run), a lane test (p.tests), the nightly. Work of a task a person must
// answer is no work at all: a needs-you in force means its next step is not started (the merge loop picks none of a
// paused task, the worker start rule needs no needs-you), so whatever an effect of it still holds replays after the
// restart.
func (p *proj) restartBusy() bool {
	if p.night.running || (p.running.Task != "" && !p.onAPerson(p.running.Task)) {
		return true
	}
	for task := range p.tests {
		if !p.onAPerson(task) {
			return true
		}
	}
	for k := range p.exec {
		if !p.onAPerson(k.Task) {
			return true
		}
	}
	return false
}
