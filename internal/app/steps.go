// steps.go: the merge loop's steps: scratch and after steps, submit, gate, publish, unpin, fix.
package app

import (
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"strconv"
	"strings"

	"obor/internal/store"
)

// scratchStep starts effect, mt's gate or publish (key step), in its own goroutine holding p's scratch (docs/state-model.md
// "Concurrency"): every other step of p goes on meanwhile; the task waits until the
// effect's records are folded, then the goroutine wakes the loop. A resolved verdict's check holding the scratch: the
// next pass. A publish starts only when, under the same lock that sets p.running, no call is admitted;
// admitted, it waits for the next pass. False: the task waits.
func (a *App) scratchStep(ctx context.Context, p *proj, mt mtask, step string, effect func(context.Context)) bool {
	select {
	case p.scratch <- struct{}{}:
	default:
		return false
	}
	base, _ := event.StepN(step)
	a.mu.Lock()
	// The publish's start decides, with p.running below: not pickAccepted's filter. A config_set admitted
	// first (roleworktree.go): the next pass picks it again.
	if base == event.StepPublish && p.admits > 0 {
		a.mu.Unlock()
		<-p.scratch
		return false
	}
	p.running = event.Key{Task: mt.task, Attempt: mt.attempt, Step: step}
	if base == event.StepPublish {
		p.published = make(chan struct{})
	}
	a.scratched++
	a.mu.Unlock()
	a.effects.Add(1)

	go func() {
		defer a.effects.Done()
		effect(ctx)
		a.mu.Lock()
		if p.published != nil { // the admissions waiting for it go on
			close(p.published)
			p.published = nil
		}
		p.running, p.runCmd = event.Key{}, ""
		a.mu.Unlock()
		<-p.scratch
		a.wake()
	}()
	return false
}

// afterStep runs mt's next after-merge step (rows 37–38); one that fails stays pending and no later one runs before
// it (no lane reuse after a failed stop). after_merge (G) is the gate's; agent.stop (S) acts on the lane only while
// its row holds the task (a crash, then the lane reused: its stop is skipped).
func (a *App) afterStep(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	step := mt.afterList()[mt.after]
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: step}, Class: event.ClassGit}
	var effect func(context.Context) (any, error)
	switch step {
	case event.StepAfterMerge: // no data: its effect is mt.cand
		effect = func(ctx context.Context) (any, error) { return fx.Workflow.AfterMerge(ctx, mt.cand) }
	case event.StepStop:
		in.Class, in.Data = event.ClassStart, stopData{Lane: mt.lane, Agent: mt.agent}
		effect = func(ctx context.Context) (any, error) { return a.stopWorker(ctx, p, fx, mt) }
	}
	if _, err := a.do(ctx, p, in, effect); err != nil {
		return a.failed(ctx, p, mt, step, err)
	}
	return true
}

// endServeNeeds ends a failed self-restart's needs-you at a serve's start (a new serve runs: the restart the owner
// was asked for happened).
func (a *App) endServeNeeds(p *proj) {
	a.settleNeeds(p, event.ServeTask, "")
}

// lanePassed is why mt's lane is no longer the task's ("" while its row holds the task): after-merge work on the lane
// is skipped then.
func (a *App) lanePassed(p *proj, mt mtask) string {
	a.mu.Lock()
	task := p.laneTask(mt.lane)
	a.mu.Unlock()
	if mt.lane != "" && task == mt.task {
		return ""
	}
	return "skipped: lane " + Dash(mt.lane) + " holds " + Dash(task)
}

// stopWorker stops the merged lane's worker with force (its work is in), only while the lane holds the task,
// through serve's identity (stopAgent; a stop that says not found while the agent is still identified: the snapshot
// lags, a success). No agent: "stopped: no agent (<why>)"; an error leaves the step pending (no lane reuse). The stop
// is unconditional: nothing counts the idle workers it stops.
func (a *App) stopWorker(ctx context.Context, p *proj, fx *Loop, mt mtask) (string, error) {
	if why := a.lanePassed(p, mt); why != "" {
		return why, nil
	}
	if mt.agent == "" {
		return "skipped: the lane names no worker agent", nil
	}
	line, gone, err := a.stopAgent(ctx, p, fx, mt.agent, true, true)
	if gone {
		return "stopped: no agent (" + line + ")", nil
	}
	return line, err
}

// stopAgent is the one decision "is the worker gone, else stop it": the post-merge stop (stopWorker), and
// worker.stop (stopStep: a move's and a drop's). agent's pane is resolved now (whereIs; never an address recorded
// earlier, and never a label lookup of the stop's own):
//   - Identified: stopped at that pane by ID (force as given); line is the stop's, else "stopped <agent>".
//   - Ambiguous: an error naming the panes, nothing stopped.
//   - Unavailable, serve not connected to herdr: an error (nothing is known).
//   - Unavailable otherwise: gone, line the why (no agent: nothing to stop).
//
// A stop that fails is resolved again: ambiguous now, an error naming the panes; not connected, the
// stop's error; unavailable now, gone (line the stop's error). Still identified: the stop's error, unless lags and the
// stop says not found (the snapshot lags: gone). worker.stop passes lags false (a pane identity
// still finds is not taken as gone).
func (a *App) stopAgent(ctx context.Context, p *proj, fx *Loop, agent string, force, lags bool) (line string, gone bool, err error) {
	w := a.where(p, agent)
	switch {
	case w.State == Ambiguous:
		return "", false, errors.New(notIdentified(agent, w))
	case w.State == Unavailable && w.Why == notConnected: // not "no agent": serve is cut off from herdr
		return "", false, errCutOff
	case w.State == Unavailable:
		return w.Why, true, nil
	}
	line, err = fx.Agents.Stop(ctx, agent, w.Pane.ID, force)
	if err == nil {
		if line == "" {
			line = "stopped " + agent
		}
		return line, false, nil
	}
	// Resolved again: ambiguous now is an error even when the stop's error says not found.
	switch w := a.where(p, agent); {
	case w.State == Ambiguous:
		return "", false, errors.New(notIdentified(agent, w) + ": " + err.Error())
	case w.State == Unavailable && w.Why == notConnected:
		return "", false, err
	case w.State == Unavailable, lags && port.NotFound(err):
		return err.Error(), true, nil
	}
	return "", false, err
}

// errCutOff is stopAgent's error while serve is not connected to herdr: nothing is known of the agent.
var errCutOff = errors.New("serve is " + notConnected + " to herdr")

// where is whereIs taking mu.
func (a *App) where(p *proj, agent string) Where {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.whereIs(p, agent)
}

// submitID is the identity of submit step (submit, submit.N) of attempt: its pin's name, <attempt>[.N].
func submitID(attempt int, step string) string {
	id := strconv.Itoa(attempt)
	if _, n := event.StepN(step); n > 1 {
		id += "." + strconv.Itoa(n)
	}
	return id
}

// submit pins the lane's commit (row 18): the lane's uncommitted work committed, its HEAD the lane gate's input (the
// gate never reads the live lane again). A refusal (no lane dir, not on a branch, already in the target) is final:
// the owner decides, as for a gate's fail.
func (a *App) submit(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepSubmit, mt.submits+1)}, Class: event.ClassGit,
		Data: submitData{Lane: mt.lane, Agent: mt.agent}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		title, err := titleOf(p, mt.task)
		if err != nil {
			return nil, err
		}
		sha, err := fx.Workflow.Submit(ctx, mt.task, submitID(mt.attempt, in.Step), mt.lane,
			subjectOf(title, mt.report))
		var refused port.Refused
		if errors.As(err, &refused) { // final: the owner decides
			return submitOut{Refused: refused.Line}, nil
		}
		if err != nil {
			return nil, err
		}
		return submitOut{SHA: sha}, nil
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// gate runs the attempt's next gate (row 18): submit's sha merged into the target, the resolved merge, or the
// candidate on the moved target, compiled (the slot runs no test).
func (a *App) gate(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	g := port.GateInput{Task: mt.task, Lane: mt.lane, Kind: port.InputLane, Input: mt.input}
	switch mt.state {
	case stResolved:
		g.Kind, g.Input = port.InputResolved, mt.resolved
	case stMoved:
		// The submitted sha, never the old candidate (one merge commit per task); the subject rule stays.
		g.Kind, g.Input = port.InputMoved, mt.input
		g.Resolved = mt.resolved // a replayed resolution must equal the checked one
	}
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepGate, mt.gates+1)}, Class: event.ClassGit,
		Data: gateIn{Kind: g.Kind, Lane: mt.lane, Input: g.Input}}
	g.Command = func(name string) { // obor status' running line names the command while it runs; "" between two
		a.mu.Lock()
		if p.running == in.Key {
			p.runCmd = name
		}
		a.mu.Unlock()
	}
	_, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		row, _, err := specOf(p, mt.task) // the current rev: the gate puts it into the merge's message
		if err != nil {
			return nil, err
		}
		g.Spec, g.SpecRev = row.Text, row.Rev
		// the merge's subject and body
		g.Title, g.Summary, g.Body = row.Spec.Title, summaryOf(mt.report), summarySection(mt.report)
		o, err := fx.Workflow.Gate(ctx, g)
		if err != nil {
			return nil, err
		}
		out := gateResult(mt.task, o)
		out.SpecRev = row.Rev
		return out, nil
	})
	if err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// gateResult is the gate outcome o of task as its result.
func gateResult(task string, o port.GateOutcome) gateOut {
	switch o.Kind {
	case port.OutcomeGreen:
		line := "GREEN " + task
		if o.Candidate.Checks != "" {
			line += " (" + o.Candidate.Checks + ")"
		}
		return gateOut{Outcome: outGreen, Line: line, Candidate: o.Candidate}
	case port.OutcomeRed:
		return gateOut{Outcome: outRed, Line: o.Line}
	case port.OutcomeConflict:
		if o.Mismatch != nil { // the reason and the checked resolution reach the resolve job
			return gateOut{Outcome: outConflict, Line: o.Line, Mismatch: o.Mismatch}
		}
		return gateOut{Outcome: outConflict, Line: fmt.Sprintf("CONFLICT in %s: %s", o.Dir, strings.Join(o.Files, ", "))}
	case port.OutcomeFail:
		return gateOut{Outcome: outFail, Line: o.Line}
	}
	return gateOut{Outcome: outFail, Line: fmt.Sprintf("FAIL %s: unknown gate outcome %q", task, o.Kind)}
}

// laneStep starts effect, mt's lane test (key step), in its own goroutine outside the scratch token (
// docs/state-model.md "Concurrency"): one per task and so one per lane (a lane holds one task), in parallel with the
// other lanes' and with the slot's steps; the pool slot it takes bounds how many run at once (slots.max). The task
// waits until the effect's records are folded, then the goroutine wakes the loop. False: the task waits.
func (a *App) laneStep(ctx context.Context, p *proj, mt mtask, step string, effect func(context.Context)) bool {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: step}
	a.mu.Lock()
	if _, ok := p.tests[mt.task]; ok {
		a.mu.Unlock()
		return false
	}
	p.tests[mt.task] = key
	a.mu.Unlock()
	a.effects.Add(1)
	go func() {
		defer a.effects.Done()
		effect(ctx)
		a.mu.Lock()
		delete(p.tests, mt.task)
		delete(p.testCmd, mt.task)
		a.mu.Unlock()
		a.wake()
	}()
	return false
}

// test runs the attempt's next lane test (row 18b): the class's checks on the candidate in the task's lane.
func (a *App) test(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepTest, mt.tests+1)},
		Class: event.ClassGit, Data: testIn{Candidate: mt.cand.SHA, Target: mt.cand.Target, Lane: mt.lane}}
	ti := port.TestInput{Task: mt.task, Lane: mt.lane, Branch: mt.disp.Branch, SHA: mt.cand.SHA, Target: mt.cand.Target}
	ti.Command = func(name string) { // obor status' running line names the command while it runs (as a gate's)
		a.mu.Lock()
		if p.tests[mt.task] == in.Key {
			p.testCmd[mt.task] = name
		}
		a.mu.Unlock()
	}
	_, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		o, err := fx.Workflow.Test(ctx, ti)
		if err != nil {
			return nil, err
		}
		return testResult(mt.task, o), nil
	})
	if err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// testResult is the lane test outcome o of task as its result.
func testResult(task string, o port.TestOutcome) testOut {
	switch o.Kind {
	case port.OutcomeGreen:
		return testOut{Outcome: outGreen, Line: o.Line, Checks: o.Checks}
	case port.OutcomeRed:
		return testOut{Outcome: outRed, Line: o.Line}
	case port.OutcomeFail:
		return testOut{Outcome: outFail, Line: o.Line}
	}
	return testOut{Outcome: outFail, Line: fmt.Sprintf("FAIL %s: unknown test outcome %q", task, o.Kind)}
}

// publish fast-forwards the target to the accepted candidate (row 33): merged, already in the target (a crash after
// the fast-forward), moved (re-gate), held (the main checkout has local changes to the merge's files: the owner) or
// pending (the merge asked for, not done yet: the Landed rule looks again each pass).
func (a *App) publish(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	// No data: the effect is mt.cand, and no reader reads a publish intent's data.
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepPublish, mt.pubs+1)}, Class: event.ClassGit}
	_, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		o, err := fx.Workflow.Publish(ctx, mt.cand)
		if err != nil {
			return nil, err
		}
		switch o.Kind {
		case port.PublishMerged:
			return gateOut{Outcome: outMerged, Line: "MERGED " + mt.task}, nil
		case port.PublishAlready:
			return gateOut{Outcome: outMerged, Line: "MERGED " + mt.task + " (already in the target)"}, nil
		case port.PublishMoved:
			return gateOut{Outcome: outMoved, Line: fmt.Sprintf("MOVED %s: the target is at %s, not %s", mt.task, o.Head,
				mt.cand.Target)}, nil
		case port.PublishPending:
			return gateOut{Outcome: outPending, Line: "PENDING " + mt.task + ": the merge is asked for, not done yet"}, nil
		case port.PublishHold:
			return gateOut{Outcome: outHold, Line: fmt.Sprintf("HOLD %s: the main checkout has local changes to %s",
				mt.task, strings.Join(o.Files, ", "))}, nil
		}
		return nil, fmt.Errorf("unknown publish outcome %q", o.Kind)
	})
	if err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	a.mu.Lock()
	moved := p.folded.loop[mt.task].state == stMoved
	a.mu.Unlock()
	if moved {
		return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepMoved, Kind: event.KindFact},
			noteData{Note: mt.line})
	}
	return true
}

// unpin deletes a rejected task's pins (G: an absent pin is not an error), before the owner is told.
func (a *App) unpin(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepUnpin}, Class: event.ClassGit}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		return "unpinned", fx.Workflow.Unpin(ctx, mt.task)
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// fix sends the worker its fix job key (row 24; fix, fix.N after obor resume): what to fix, and to call task_done
// again (a lane with no worker agent is a needs-you: next). Its delivery timers start (watchFix).
func (a *App) fix(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key) bool {
	at := a.opt.Now()
	if !a.sendFix(ctx, p, fx, mt, key, key) {
		return false
	}
	a.jobSent(p, mt.task, JobID(key), at)
	return true
}

// sendFix types mt's fix job's (job) wake under key (the job's own, or its retry).
func (a *App) sendFix(ctx context.Context, p *proj, fx *Loop, mt mtask, key, job event.Key) bool {
	return a.send(ctx, p, fx, mt, key, JobID(job), promptData{Agent: mt.agent, Note: mt.line}, wakeText,
		fmt.Sprintf("fix job %s not sent", JobID(job)))
}

// fixText is mt's fix job: what to fix, and to call task_done again; the role texts' paths for a worker started fresh (read); with the spec (row) when its
// rev is not the one the worker's last job carried (the lead changed it since).
func fixText(mt mtask, row store.SpecRow, read string) string {
	text := fmt.Sprintf("%s is sent back to you (attempt %d): %s\nFix it and run the tests; when finished, call "+
		"task_done again with your new report (obor serve takes it as your next attempt).", mt.task, mt.attempt, mt.line)
	text += "\n" + read
	if row.Rev > 0 && row.Rev != mt.specRev {
		text += specText(row)
	}
	return text
}
