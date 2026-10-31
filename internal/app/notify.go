// notify.go: the herdr notification when a task needs the owner.
package app

import (
	"context"
	"fmt"
	"strconv"
)

// notifyKey is the key of the notification of the needs-you fact r.
func notifyKey(r Record) Key {
	return Key{Task: r.Task, Step: "notify." + strconv.FormatInt(r.Seq, 10)}
}

// notify first queues the lead's line of each such fact (needsLines), then shows the owner each needs-you fact in force that has no notification recorded: just recorded, replayed
// without one (a crash before it), or failed (again after a backoff: 1, 2, 4 … minutes, at most 30). It sends one at
// a time, the oldest due first, and picks each under mu just before its send: a fact cleared
// while an earlier one was sent is not sent. A recorded result is never sent again; a send whose result failed to
// persist, or whose fact cleared while it was in flight, is (at least once, docs/state-model.md).
func (a *App) notify(ctx context.Context) {
	a.needsLines()
	a.watchLines()
	if !a.notifying.TryLock() { // the pass running picks each next fact afresh; one it missed goes at the next check
		return
	}
	defer a.notifying.Unlock()
	tried := map[Key]bool{} // the notifications tried in this pass
	for {
		a.mu.Lock()
		p, r, ok := a.nextNotify(tried)
		a.mu.Unlock()
		if !ok {
			return
		}
		key := notifyKey(r)
		tried[key] = true
		_, err := p.log.Do(ctx, Intent{Key: key, Class: ClassFact}, a.opt.Now, func(ctx context.Context) (any, error) {
			body := fmt.Sprintf("%s in %s: %s", r.Task, r.Fact().Lane, r.Fact().Reason)
			if r.Task == LoopTask || r.Task == NightlyTask || r.Task == MsgTask || r.Task == DiskTask {
				body = r.Fact().Reason
			}
			if err := a.panes.Notify(ctx, "obor: "+p.name+" needs you", body); err != nil {
				return nil, err
			}
			return "shown", nil
		})
		a.mu.Lock()
		if err != nil && p.inForce(r) { // a fact cleared while it was sent is never retried
			rt := p.retry[key]
			rt.tries++
			rt.at = a.opt.Now().Add(notifyWait(rt.tries))
			p.retry[key] = rt
			a.logf("notification of %s %s: %v; again in %s", r.Task, r, err, notifyWait(rt.tries))
		} else {
			delete(p.retry, key)
		}
		a.mu.Unlock()
	}
}

// nextNotify is the oldest needs-you fact in force whose notification is due and not tried in this pass (under mu).
func (a *App) nextNotify(tried map[Key]bool) (*proj, Record, bool) {
	p := a.p
	if !a.connected || p == nil {
		return nil, Record{}, false
	}
	now := a.opt.Now()
	var br Record
	found := false
	for _, r := range p.needsYou() {
		key := notifyKey(r)
		if tried[key] || now.Before(p.retry[key].at) {
			continue
		}
		if !found || r.Seq < br.Seq {
			br, found = r, true
		}
	}
	return p, br, found
}

// needsYou is p's needs-you facts in force: the watchdogs' and the merge loop's.
func (p *proj) needsYou() []Record {
	var out []Record
	for _, fs := range p.folded.active {
		for _, r := range fs {
			if r.Step == StepNeedsYou {
				out = append(out, r)
			}
		}
	}
	for _, mt := range p.folded.loop {
		if mt.needs.Seq != 0 {
			out = append(out, mt.needs)
		}
	}
	return out
}
