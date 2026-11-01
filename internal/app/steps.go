// steps.go: the merge loop's steps: scratch and after steps, submit, gate, publish, unpin, fix.
package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/store"
)

// scratchStep starts effect, mt's gate or publish (key step), in its own goroutine holding p's scratch (docs/state-model.md
// "Concurrency"): every other step of p goes on meanwhile; the task waits until the
// effect's records are folded, then the goroutine wakes the loop. A resolved verdict's check holding the scratch: the
// next pass. False: the task waits.
func (a *App) scratchStep(ctx context.Context, p *proj, mt mtask, step string, effect func(context.Context)) bool {
	select {
	case p.scratch <- struct{}{}:
	default:
		return false
	}
	a.mu.Lock()
	p.running = Key{mt.task, mt.attempt, step}
	a.scratched++
	a.mu.Unlock()
	a.effects.Add(1)
	go func() {
		defer a.effects.Done()
		effect(ctx)
		a.mu.Lock()
		p.running = Key{}
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
	step := afterSteps[mt.after]
	in := Intent{Key: Key{mt.task, mt.attempt, step}, Class: ClassGit}
	var effect func(context.Context) (any, error)
	ran := "" // after_merge's line when it ran in this pass (a stored result: "")
	switch step {
	case StepAfterMerge:
		in.Data = mt.cand
		effect = func(ctx context.Context) (any, error) {
			line, err := fx.Scratch.AfterMerge(ctx, mt.cand)
			ran = line
			return line, err
		}
	case StepStop:
		in.Class, in.Data = ClassStart, stopData{Lane: mt.lane, Agent: mt.agent}
		effect = func(ctx context.Context) (any, error) { return a.stopWorker(ctx, p, fx, mt) }
	}
	if _, err := a.do(ctx, p, in, effect); err != nil {
		return a.failed(ctx, p, mt, step, err)
	}
	if ran == "ran" {
		a.restartServe(ctx, p, fx, mt.task)
	}
	return true
}

// ServeTask is the task key of a failed self-restart's needs-you (T-652): the project's serve, not a task's.
const ServeTask = "serve"

// restartServe restarts serve on the new build after task's green after_merge, when the project's
// commands.after_merge.restart_serve is set (T-652). The step's result is in the log already, so the new serve finds
// it done: no replay of the merge or the command, and work in flight here folds as at any restart. A restart that
// fails (serve still runs): needs-you "serve restart after <task> failed: <err>", which the next serve start ends.
func (a *App) restartServe(ctx context.Context, p *proj, fx *Loop, task string) {
	if fx.Restart == nil {
		return
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		a.logf("%s %s: serve restart not tried: %v", p.name, task, err)
		return
	}
	if cmd, _ := cfg.Command(StepAfterMerge); !cmd.RestartServe {
		return
	}
	a.logf("%s %s: after_merge ran: restarting serve on the new build", p.name, task)
	err = fx.Restart(ctx)
	if err == nil || ctx.Err() != nil { // restarted, or this serve is stopping (being restarted)
		return
	}
	a.mu.Lock()
	held := len(p.folded.active[ServeTask]) > 0
	a.mu.Unlock()
	if held && !a.record(p, Record{Task: ServeTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine}) {
		return
	}
	a.record(p, Record{Task: ServeTask, Step: StepNeedsYou, Kind: KindFact},
		FactData{Machine: Machine, Reason: "serve restart after " + task + " failed: " + err.Error()})
}

// endServeNeeds ends a failed self-restart's needs-you at a serve's start (a new serve runs: the restart the owner
// was asked for happened).
func (a *App) endServeNeeds(p *proj) {
	a.mu.Lock()
	held := len(p.folded.active[ServeTask]) > 0
	a.mu.Unlock()
	if held {
		a.record(p, Record{Task: ServeTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine})
	}
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

// stopWorker stops the merged lane's worker with force (its work is in, T-262) unless stop_idle_workers is false, only
// while the lane holds the task. Its pane is resolved now (whereIs; never an address recorded earlier, RV-53 #1):
// Identified, stopped there; Ambiguous, an error naming the panes (the step stays pending, no lane reuse); Unavailable,
// nothing to stop, unless serve is not connected to herdr (an error: the step waits, DD-v0.13 A2). A stop that fails
// is resolved again: still identified, an error (not found: the snapshot lags, a success); ambiguous now, an error
// naming the panes; unavailable now, nothing to stop (not connected: an error).
func (a *App) stopWorker(ctx context.Context, p *proj, fx *Loop, mt mtask) (string, error) {
	if why := a.lanePassed(p, mt); why != "" {
		return why, nil
	}
	if mt.agent == "" {
		return "skipped: the lane names no worker agent", nil
	}
	if cfg, err := config.LoadDaemon(p.root); err == nil && cfg.Data.StopIdleWorkers != nil && !*cfg.Data.StopIdleWorkers {
		return "kept: stop_idle_workers is false", nil
	}
	w := a.where(p, mt.agent)
	switch w.State {
	case Ambiguous:
		return "", errors.New(notIdentified(mt.agent, w))
	case Unavailable:
		if w.Why == notConnected { // not "no agent": serve is cut off from herdr, the step waits (DD-v0.13 A2)
			return "", errors.New("not stopped: " + w.Why)
		}
		return "stopped: no agent (" + w.Why + ")", nil
	}
	line, err := fx.Agents.Stop(ctx, mt.agent, w.Pane.ID, true)
	if err == nil {
		if line == "" {
			line = "stopped " + mt.agent
		}
		return line, nil
	}
	// Resolved again (RV-53-verify V2): ambiguous now is an error even when the stop's error says not found.
	switch w := a.where(p, mt.agent); {
	case w.State == Ambiguous:
		return "", errors.New(notIdentified(mt.agent, w) + ": " + err.Error())
	case w.State == Unavailable && w.Why == notConnected:
		return "", err
	case w.State == Unavailable, NotFound(err):
		return "stopped: no agent (" + err.Error() + ")", nil
	}
	return "", err
}

// where is whereIs taking mu.
func (a *App) where(p *proj, agent string) Where {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.whereIs(p, agent)
}

// scratchBusy: another task of p holds the scratch, a conflict open or resolved there (rule: one merge in the scratch
// at a time), also while a needs-you pauses that work (RV-38 #9). It is released when the task abandons it (reject,
// regate, a new attempt) or merges.
func (p *proj) scratchBusy(task string) bool {
	for t, mt := range p.folded.loop {
		if t != task && states[mt.kept()].scratch {
			return true
		}
	}
	return false
}

// submitID is the identity of submit step (submit, submit.N) of attempt: its pin's name, <attempt>[.N].
func submitID(attempt int, step string) string {
	id := strconv.Itoa(attempt)
	if _, n := stepN(step); n > 1 {
		id += "." + strconv.Itoa(n)
	}
	return id
}

// submit pins the lane's commit (row 18): the lane's uncommitted work committed, its HEAD the lane gate's input (the
// gate never reads the live lane again). A refusal (no lane dir, not on a branch, already in the target) is final:
// the owner decides, as for a gate's fail.
func (a *App) submit(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := Intent{Key: Key{mt.task, mt.attempt, nth(StepSubmit, mt.submits+1)}, Class: ClassGit,
		Data: submitData{Lane: mt.lane, Agent: mt.agent}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		title, err := titleOf(p, mt.task)
		if err != nil {
			return nil, err
		}
		sha, err := fx.Scratch.Submit(ctx, mt.task, submitID(mt.attempt, in.Step), mt.lane,
			subjectOf(title, mt.report), mt.agent)
		var refused Refused
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
// candidate on the moved target.
func (a *App) gate(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	g := GateInput{Task: mt.task, Lane: mt.lane, Kind: InputLane, Input: mt.input}
	switch mt.state {
	case stResolved:
		g.Kind, g.Input = InputResolved, mt.resolved
	case stMoved:
		// T-403: the submitted sha, never the old candidate (one merge commit per task); the subject rule stays (T-360).
		g.Kind, g.Input = InputMoved, mt.input
	}
	in := Intent{Key: Key{mt.task, mt.attempt, nth(StepGate, mt.gates+1)}, Class: ClassGit,
		Data: gateIn{Kind: g.Kind, Lane: mt.lane, Input: g.Input}}
	_, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		row, _, err := specOf(p, mt.task) // the current rev: the gate puts it into the merge's message (T-521)
		if err != nil {
			return nil, err
		}
		g.Spec, g.SpecRev = row.Text, row.Rev
		if g.Kind != InputResolved { // a resolved gate writes no new subject
			g.Summary = subjectOf(spec.Title(row.Text), mt.report)
		}
		o, err := fx.Scratch.Gate(ctx, g)
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
func gateResult(task string, o GateOutcome) gateOut {
	switch o.Kind {
	case OutcomeGreen:
		line := "GREEN " + task
		if o.Candidate.Checks != "" {
			line += " (" + o.Candidate.Checks + ")"
		}
		return gateOut{Outcome: outGreen, Line: line, Candidate: o.Candidate}
	case OutcomeRed:
		return gateOut{Outcome: outRed, Line: o.Line}
	case OutcomeConflict:
		return gateOut{Outcome: outConflict, Line: fmt.Sprintf("CONFLICT in %s: %s", o.Dir, strings.Join(o.Files, ", "))}
	case OutcomeFail:
		return gateOut{Outcome: outFail, Line: o.Line}
	}
	return gateOut{Outcome: outFail, Line: fmt.Sprintf("FAIL %s: unknown gate outcome %q", task, o.Kind)}
}

// publish fast-forwards the target to the accepted candidate (row 33): merged, already in the target (a crash after
// the fast-forward), moved (re-gate) or held (the main checkout has local changes to the merge's files: the owner).
func (a *App) publish(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := Intent{Key: Key{mt.task, mt.attempt, nth(StepPublish, mt.pubs+1)}, Class: ClassGit, Data: mt.cand}
	_, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		o, err := fx.Scratch.Publish(ctx, mt.cand)
		if err != nil {
			return nil, err
		}
		switch o.Kind {
		case PublishMerged:
			return gateOut{Outcome: outMerged, Line: "MERGED " + mt.task}, nil
		case PublishAlready:
			return gateOut{Outcome: outMerged, Line: "MERGED " + mt.task + " (already in the target)"}, nil
		case PublishMoved:
			return gateOut{Outcome: outMoved, Line: fmt.Sprintf("MOVED %s: the target is at %s, not %s", mt.task, o.Head,
				mt.cand.Target)}, nil
		case PublishHold:
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
		return a.record(p, Record{Task: mt.task, Attempt: mt.attempt, Step: StepMoved, Kind: KindFact},
			noteData{Note: mt.line})
	}
	return true
}

// unpin deletes a rejected task's pins (G: an absent pin is not an error), before the owner is told.
func (a *App) unpin(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := Intent{Key: Key{mt.task, mt.attempt, StepUnpin}, Class: ClassGit}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		return "unpinned", fx.Scratch.Unpin(ctx, mt.task)
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// fix sends the worker its fix job key (row 24; fix, fix.N after obor resume): what to fix, and to call task_done
// again (a lane with no worker agent is a needs-you: next). Its delivery timers start (watchFix).
func (a *App) fix(ctx context.Context, p *proj, fx *Loop, mt mtask, key Key) bool {
	at := a.opt.Now()
	if !a.sendFix(ctx, p, fx, mt, key, key) {
		return false
	}
	a.mu.Lock()
	p.timers[mt.task] = &jobTimer{job: JobID(key), sent: at}
	a.mu.Unlock()
	return true
}

// sendFix types mt's fix job's (job) wake under key (the job's own, or its retry).
func (a *App) sendFix(ctx context.Context, p *proj, fx *Loop, mt mtask, key, job Key) bool {
	return a.send(ctx, p, fx, mt, key, JobID(job), promptData{Agent: mt.agent, Note: mt.line}, wakeText,
		fmt.Sprintf("fix job %s not sent", JobID(job)))
}

// fixText is mt's fix job: what to fix, and to call task_done again; the lead's decision on the task's open questions
// when it has one (T-611); the role texts' paths for a worker started fresh (read, T-603); with the spec (row) when its
// rev is not the one the worker's last job carried (the lead changed it since).
func fixText(mt mtask, row store.SpecRow, read string) string {
	text := fmt.Sprintf("%s is sent back to you (attempt %d): %s\nFix it and run the tests; when finished, call "+
		"task_done again with your new report (obor serve takes it as your next attempt).", mt.task, mt.attempt, mt.line)
	if mt.answer {
		text = fmt.Sprintf("The answer to your question on %s (attempt %d): %s\nGo on with the task; when finished, "+
			"call task_done with your report (obor serve takes it as your next attempt).", mt.task, mt.attempt, mt.line)
	}
	if mt.openDecision != "" { // the lead already answered them: the hold did not (T-611)
		text += fmt.Sprintf("\nLead's decision on your open questions (attempt %d): %s — do not list these again "+
			"under Open questions.", mt.openAt, mt.openDecision)
	}
	text += "\n" + read
	if row.Rev > 0 && row.Rev != mt.specRev {
		text += specText(row)
	}
	return text
}
