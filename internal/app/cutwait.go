// cutwait.go: the wait before a step whose attempt was not counted is tried again.
package app

import (
	"context"
	"time"

	"obor/internal/app/event"
)

// cutWait is the wait of one step (its intent's key) whose attempts end with nothing recorded while serve runs: n
// such attempts in a row, and whether the wait after the last one is over (its timer says so and wakes the loop). It
// lives in memory only: a restart tries the step at once.
type cutWait struct {
	n     int
	over  bool
	timer *time.Timer
}

// uncounted: key's attempt ended with nothing recorded (what: the record it would have been), its intent pending.
// Serve stopping (its ctx ended): the one line of shutDown, and the next serve's replay runs it. Serve running (the
// effect's own cancellation, herdr not connected, an agent herdr does not know yet): the step is not tried again
// before a delay, Options.StepRetry doubling per such attempt in a row up to Options.StepRetryMax (cutEnd ends the
// row), so a step that is cut at once never runs pass after pass.
func (a *App) uncounted(ctx context.Context, p *proj, key event.Key, what string) {
	if ctx.Err() != nil {
		a.shutDown(what)
		return
	}
	a.mu.Lock()
	w := p.cuts[key]
	if w == nil {
		w = &cutWait{}
		p.cuts[key] = w
	} else {
		w.timer.Stop()
	}
	w.n++
	w.over = false
	d := a.opt.StepRetry
	for i := 1; i < w.n && d < a.opt.StepRetryMax; i++ {
		d *= 2
	}
	d = min(d, a.opt.StepRetryMax)
	w.timer = time.AfterFunc(d, func() {
		a.mu.Lock()
		w.over = true
		a.mu.Unlock()
		a.wake()
	})
	a.mu.Unlock()
	a.logf("%s not recorded: tried again in %s", what, d)
}

// cutWaits: key's step waits after an uncounted attempt (under mu).
func (p *proj) cutWaits(key event.Key) bool {
	w := p.cuts[key]
	return w != nil && !w.over
}

// taskCutWaits: a step of task waits after an uncounted attempt (under mu).
func (p *proj) taskCutWaits(task string) bool {
	for key, w := range p.cuts {
		if key.Task == task && !w.over {
			return true
		}
	}
	return false
}

// cutEnd ends the wait and the row of uncounted attempts of key's step (under mu): its result is in the log.
func (p *proj) cutEnd(key event.Key) {
	if w := p.cuts[key]; w != nil {
		w.timer.Stop()
		delete(p.cuts, key)
	}
}

// cutEndFact ends the waits the fact r decides (under mu): a counted failure ends its step's, and any fact of a
// task other than the lines' ends that task's (a resume, a new decision on it: the step is tried at once). The lines
// share one task key, so a line's wait ends only with its own failure or result.
func (p *proj) cutEndFact(r event.Record) {
	if r.Task == event.MsgTask {
		if r.Step == event.StepFailed {
			p.cutEnd(event.Key{Task: r.Task, Step: r.Fact().Step})
		}
		return
	}
	for key := range p.cuts {
		if key.Task == r.Task {
			p.cutEnd(key)
		}
	}
}
