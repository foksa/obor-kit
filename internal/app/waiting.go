// waiting.go: a dispatch that waits for the tasks it names (--after) or for a lane: the waiting fact's fold, the pick of the
// next waiting task, and the wait's refusals and dispatch (waitTask, pickWaiting, waitDispatch, waitsOn).
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"time"

	"obor/internal/spec"
)

// waitTask records fact waiting for c (under a.verdicts) when a task of c.After is not merged in the loop: the reply
// WAITING <task> after <tasks> (queued) and every idle lane (idleNote: it takes none); "" with no error when all are
// merged (a plain dispatch). Refused: a task it names is itself, unknown (no spec and no loop record), dropped or
// rejected, or waiting (itself or through the tasks it waits for) for this one (a cycle); an unknown lane; the task
// open in the loop (as a dispatch).
func (a *App) waitTask(p *proj, c wire.DispatchReq, ls []port.Lane, keep bool) (string, error) {
	var open []string
	for _, dep := range c.After {
		_, spec, err := specOf(p, dep)
		if err != nil {
			return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
		}
		a.mu.Lock()
		m := p.folded.loop[dep]
		cycle := p.waitsOn(dep, c.Task, map[string]bool{})
		a.mu.Unlock()
		switch {
		case dep == c.Task:
			return "", fmt.Errorf("dispatch %s: it cannot wait for itself", c.Task)
		case cycle:
			return "", fmt.Errorf("dispatch %s: %s waits for %s itself: a cycle never dispatches", c.Task, dep, c.Task)
		case m == nil && !spec:
			return "", fmt.Errorf("dispatch %s: %s is no task (no spec in obor serve's store)", c.Task, dep)
		case m != nil && states[m.state].failed:
			return "", fmt.Errorf("dispatch %s: %s is %s: it never merges", c.Task, dep, m.state)
		case m == nil || m.state != stMerged:
			open = append(open, dep)
		}
	}
	if len(open) == 0 {
		return "", nil
	}
	if c.Lane != "" && !slices.ContainsFunc(ls, func(l port.Lane) bool { return l.Name == c.Lane }) {
		return "", fmt.Errorf("dispatch %s: unknown lane %s (not in the config's lanes)", c.Task, c.Lane)
	}
	mt, held := a.held(p, c.Task)
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.shown())
	}
	caller := callerOf(c.Envelope)
	d := dispatchData{Lane: c.Lane, Force: c.Force, Level: c.Level, Types: c.Types, After: c.After, Keep: keep,
		Caller: caller, Call: c.CallID, Reply: fmt.Sprintf("WAITING %s after %s (queued)", c.Task, strings.Join(open, ", ")) + idleNote(ls, "", held)}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: event.StepWaiting, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// dispatchable: a dispatch (or a waiting one) of mt's task may be recorded: none yet, ended (merged, rejected,
// dropped), waiting, or a needs-you holding its dispatch or its wait.
func (m *mtask) dispatchable() bool {
	if st := m.state; st == "" || st == stWaiting || states[st].ended {
		return true
	}
	return m.redoable()
}

// nextAttempt is the attempt a dispatch of mt's task records: a waiting task's own (its wait recorded the next), else
// the next.
func (m *mtask) nextAttempt() int {
	if m.state == stWaiting {
		return m.attempt
	}
	return m.attempt + 1
}

// foldWaiting folds fact waiting: the task waits for its After tasks (no lane held: the lane is the dispatch's).
func (p *proj) foldWaiting(r event.Record) {
	var d dispatchData
	_ = json.Unmarshal(r.Data, &d)
	next := &mtask{task: r.Task, attempt: r.Attempt, state: stWaiting, disp: d, dispSeq: r.Seq} // its seq: oldest first
	if mt := p.folded.loop[r.Task]; mt != nil && !states[mt.state].ended {
		next.sendbacks = mt.sendbacks
	}
	p.folded.loop[r.Task] = next
	delete(p.waitLine, r.Task) // a new wait: no refusal of the one it replaces (its reason, its hour)
}

// waitHigh is whether task's waiting dispatch takes the next free lane before every ordinary waiting one (T-1030): its
// stored spec is high (highPriority). A project with no log or store (a hand-built one in a test) has no specs. Under mu.
func (p *proj) waitHigh(task string) bool {
	if p.log == nil || p.log.DB() == nil {
		return false
	}
	row, ok, err := specOf(p, task)
	return err == nil && ok && highPriority(row.Spec, p.promReview())
}

// highPriority is whether spec's task is high (T-1030): its `Priority: high` line, or a Fixes: of rv, the review the
// promotion waits for ("" none).
func highPriority(sp spec.Spec, rv string) bool {
	return sp.Priority == spec.PriorityHigh || rv != "" && sp.FixesReview == rv
}

// promReview is the review the promotion waits for (T-1030): p.prom.waitRV, set while a GREEN run waits on its close
// (promoteHeads, T-977: what obor status shows as the promotion's run and Review); "" when nothing waits, promotion
// off or nothing to promote. Under mu.
func (p *proj) promReview() string { return p.prom.waitRV }

// pickWaiting is a waiting task's next step (under mu; readiness rule 1): nothing while a task it waits for is not
// merged; one of them dropped or rejected: needs-you{after}; all merged: the dispatch (waitDispatch). A task it waits
// for with no loop record (done by hand, or merged before serve held it) is merged when the target holds its merge
// evidence (Scratch.Landed, asked by the step).
func (a *App) pickWaiting(p *proj, mt mtask) step {
	if !p.dispatching {
		return nil
	}
	ready, unheld := true, []string(nil)
	for _, dep := range mt.disp.After {
		switch m := p.folded.loop[dep]; {
		case m != nil && states[m.state].failed:
			why := fmt.Sprintf("after: %s waits for %s, which is %s: obor drop %s, or dispatch it again without it",
				mt.task, dep, m.state, mt.task)
			return func(context.Context, *Loop) bool {
				a.verdicts.Lock()
				defer a.verdicts.Unlock()
				return a.waitCurrent(p, mt) && a.needsYou(p, &mt, causeAfter, why)
			}
		case m == nil:
			unheld = append(unheld, dep)
		case m.state != stMerged:
			ready = false
		}
	}
	if !ready {
		return nil
	}
	return func(ctx context.Context, fx *Loop) bool {
		for _, dep := range unheld {
			if fx.Workflow == nil {
				return false
			}
			if landed, err := fx.Workflow.Landed(ctx, dep); err != nil || !landed {
				if err != nil {
					a.logf("merge loop of %s: %s waits for %s: %v", p.name, mt.task, dep, err)
				}
				return false
			}
		}
		return a.waitDispatch(ctx, p, fx, mt)
	}
}

// waitHeld is how long one refusal of a waiting task's dispatch may hold before it is the owner's needs-you.
const waitHeld = time.Hour

// waitRefusal is a waiting task's last refused dispatch and since when that same refusal holds.
type waitRefusal struct {
	line  string
	since time.Time
}

// waitDispatch dispatches the waiting mt as its request said (lane or types, force; the caller and call id kept): true
// once fact dispatch is in. A refusal (no free lane, the lane busy …) keeps it waiting, logged when it changes and
// shown in obor status (TaskStatus.Why); the same refusal for waitHeld is needs-you{after: …} (obor resume: it waits
// again, its hour from the start). Nothing when mt's request was replaced (waitCurrent); the bookkeeping holds
// a.verdicts too, so a replacement cannot come between the refusal and its needs-you.
func (a *App) waitDispatch(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	d := mt.disp
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	if !a.waitCurrent(p, mt) {
		return false
	}
	_, err := a.dispatchTask(ctx, p, fx, wire.DispatchReq{Envelope: wire.Envelope{Root: p.root, Agent: d.Caller,
		CallID: d.Call}, Task: mt.task, Lane: d.Lane, Level: d.Level, Types: d.Types, Force: d.Force})
	if err == nil {
		a.mu.Lock()
		delete(p.waitLine, mt.task)
		a.mu.Unlock()
		return true
	}
	now := a.opt.Now()
	a.mu.Lock()
	w, ok := p.waitLine[mt.task]
	line := err.Error()
	var nl noLaneError
	var qe queueError
	laneWait := errors.As(err, &nl)
	switch {
	case laneWait:
		line = nl.laneWait() // the status line reads it after the level (T-723)
	case errors.As(err, &qe): // queued behind a keep lane's task (T-1031): the wait itself, as a busy lane's
		line, laneWait = qe.why(), true
	}
	changed := !ok || w.line != line
	if changed {
		w = waitRefusal{line: line, since: now}
		p.waitLine[mt.task] = w
	}
	held := now.Sub(w.since) >= waitHeld && !laneWait // waiting for a busy lane is the wait itself
	if held {                                         // told: after a resume the refusal's hour starts again
		delete(p.waitLine, mt.task)
	}
	a.mu.Unlock()
	if changed {
		a.logf("%s %s waiting: %s", p.name, mt.task, w.line)
	}
	if held {
		a.needsYou(p, &mt, causeAfter, fmt.Sprintf("after: %s is not dispatched for %s since what it waits for merged: %s",
			mt.task, Age(waitHeld), w.line))
	}
	return false
}

// waitCurrent: the waiting request mt was selected for is still the one in force (under a.verdicts, which an obor
// dispatch replacing it takes): its call id and attempt (a replacement keeps the attempt; RV-56 #1). A stale step
// records nothing: the next pass picks the request in force.
func (a *App) waitCurrent(p *proj, mt mtask) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := p.folded.loop[mt.task]
	return m != nil && m.is(stWaiting) && m.attempt == mt.attempt && m.disp.Call == mt.disp.Call
}

// waitsOn: task waits (or its wait's needs-you is open), itself or through the tasks it waits for, for target (under
// mu).
func (p *proj) waitsOn(task, target string, seen map[string]bool) bool {
	m := p.folded.loop[task]
	if m == nil || m.state != stWaiting || seen[task] {
		return false
	}
	seen[task] = true
	for _, dep := range m.disp.After {
		if dep == target || p.waitsOn(dep, target, seen) {
			return true
		}
	}
	return false
}
