// move.go: obor dispatch T-xx --model <ref> on a task its worker works on moves it to that model (T-773): serve stops
// the worker by its identity (as a take-over does, drive.go), then records a redo dispatch of the task's next attempt
// on the same lane, branch, worktree and spec with the new model. The merge loop runs that dispatch's steps as any
// redo's: lane.prepare keeps the branch, agent.start starts the new model's agent fresh with the new attempt's work
// job. A serve restart after the record replays the dispatch, so the start runs the new model.
package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"

	"obor/internal/config"
)

// moving: c is obor dispatch's call with explicit models (--model; no --after, no lane: the CLI names none) of a task
// open in the loop that no dispatch may redo (dispatchable false): a move, or a move's refusal by its state (gating,
// merging, under review).
func (a *App) moving(p *proj, c wire.Call) bool {
	if len(c.Types) == 0 || c.Level != "" || len(c.After) > 0 || c.Lane != "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := p.folded.loop[c.Task]
	return m != nil && !m.dispatchable()
}

// moveReply is a move's reply: MOVED <task> on <lane>: <old ref> → <new ref> (attempt n).
func moveReply(task, lane, from, to string, attempt int) string {
	return fmt.Sprintf("MOVED %s on %s: %s → %s (attempt %d)", task, lane, Dash(from), to, attempt)
}

// moveCall stops worker (the decision's, with force: it may be working) outside the locks through serve's identity
// of it (stopAgent, as a take-over's stop), then records the move (dispatchDecide with stopped). No agent there is
// fine (the task is moved); a stop that fails, two panes claiming the worker or serve not connected to herdr refuses
// the move and records nothing: the task stays its worker's and a retry is the same obor dispatch. Interrupted between
// the two: the task is working with no agent, the watchdog's stall path applies and a retry moves it.
func (a *App) moveCall(ctx context.Context, c wire.Call, fx *Loop, worker string) (string, error) {
	a.mu.Lock()
	p := a.projAt(c.Root)
	a.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("dispatch: no served project at %s", c.Root)
	}
	if _, _, err := a.stopAgent(ctx, p, fx, worker, true, false); err != nil {
		return "", fmt.Errorf("dispatch %s: %s not stopped: %v (nothing recorded: %s stays %s's; the same obor dispatch "+
			"retries)", c.Task, worker, err, c.Task, worker)
	}
	a.hook(hookMoveStopped)
	reply, _, _, err := a.dispatchDecide(ctx, c, worker)
	return reply, err
}

// hookMoveStopped: a move stopped the worker, before its record's locks.
const hookMoveStopped = "move.stopped"

// moveTask decides the move of c.Task (under a.verdicts): a task its worker works on (working, or dispatched with its
// lane prepared; never a drive), no step of it running or pending, the merge loop on and not paused, to c's first
// unpaused model, a model ref of the config whose kind has a bridge and that is not the one it runs ("already on").
// stopped "": only checked, the worker to stop returned; else recorded (under a.submitMu too) once the task is checked
// again and its worker is still the one stopped: fact dispatch{redo} of the next attempt on the task's lane, branch
// and target with the new model. Refused, nothing recorded: gating, merging, under review or any other state of the
// merge loop (submitted meanwhile included).
func (a *App) moveTask(p *proj, fx *Loop, c wire.Call, stopped string) (reply, worker string, err error) {
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	merging, pause := p.merging, p.paused(a.opt.Now())
	running := p.held[c.Task]
	if p.ended[c.Task] {
		running = "a drop"
	}
	pending := ""
	if p.running.Task == c.Task {
		pending = p.running.Step
	}
	dir := p.laneDir(mt)
	a.mu.Unlock()
	switch {
	case !merging:
		return "", "", mergeOff(p)
	case pause != nil:
		return "", "", pausedErr(c.Task, pause)
	case fx.Lane == nil || fx.Specs == nil || fx.Agents == nil:
		return "", "", fmt.Errorf("dispatch off for %s (no lane adapter)", p.name)
	case mt.task == "":
		return "", "", fmt.Errorf("dispatch %s: %s is not open in obor serve", c.Task, c.Task)
	case mt.drive || mt.state != stWorking && !(mt.state == stDispatched && mt.base != ""):
		return "", "", fmt.Errorf("dispatch %s: %s is %s: obor dispatch --model moves a task its worker works on, not "+
			"one gating, merging or under review", c.Task, c.Task, mt.state)
	case running != "":
		return "", "", fmt.Errorf("dispatch %s: %s is running; retry when it settles", c.Task, running)
	case pending != "":
		return "", "", fmt.Errorf("dispatch %s: %s has %s pending: retry once it settles", c.Task, c.Task, pending)
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", "", err
	}
	for _, t := range c.Types {
		if err := cfg.CheckModelRef(t); err != nil {
			return "", "", fmt.Errorf("dispatch %s: %v", c.Task, err)
		}
	}
	paused := a.pausedModels(p, c.Types)
	ref := ""
	for _, t := range c.Types {
		if typ, _ := config.ParseModelRef(t); paused[typ] == "" {
			ref = t
			break
		}
	}
	if ref == "" {
		return "", "", fmt.Errorf("dispatch %s: %s", c.Task, noLane(paused, c.Types))
	}
	typ, effort := config.ParseModelRef(ref)
	if kind := unbridged(cfg, typ); kind != "" {
		return "", "", fmt.Errorf("dispatch %s: model %s's kind %s has no bridge (obor config validate)", c.Task, typ, kind)
	}
	if ref == mt.disp.Type {
		return "", "", fmt.Errorf("dispatch %s: %s is already on %s (lane %s)", c.Task, c.Task, ref, mt.lane)
	}
	worker = mt.agent
	if worker == "" {
		worker = cfg.AgentName("worker", mt.lane)
	}
	if stopped == "" {
		return "", worker, nil
	}
	if worker != stopped {
		return "", "", fmt.Errorf("dispatch %s: %s's worker is %s now, not %s as stopped: retry", c.Task, c.Task, worker,
			stopped)
	}
	attempt := mt.attempt + 1
	d := dispatchData{Lane: mt.lane, Dir: dir, Agent: worker, Branch: mt.disp.Branch, Target: mt.disp.Target, Redo: true,
		Force: c.Force, Types: c.Types, Effort: effort, Type: ref, Caller: callerOf(c), Call: c.CallID,
		Reply: moveReply(c.Task, mt.lane, mt.disp.Type, ref, attempt)}
	if !a.record(p, event.Record{Task: c.Task, Attempt: attempt, Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, worker, nil
}
