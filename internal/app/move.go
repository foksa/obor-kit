// move.go: obor dispatch T-xx --model <ref> on a task its worker works on moves it to that model (T-773): serve records
// a redo dispatch of the task's next attempt on the same lane, branch, worktree and spec with the new model, its old
// worker to stop (T-852), and answers at once. The merge loop runs that dispatch's steps as any redo's, the stop first:
// worker.stop stops the old worker through serve's identity of it (stopstep.go), lane.prepare keeps the branch,
// agent.start starts the new model's agent fresh with the new attempt's work job. A serve restart after the record
// replays the dispatch: the stop runs once, then the start runs the new model.
package app

import (
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

// moveReply is a move's reply: MOVING <task> on <lane>: <old ref> → <new ref> (stopping <worker>).
func moveReply(task, lane, from, to, worker string) string {
	return fmt.Sprintf("MOVING %s on %s: %s → %s (stopping %s)", task, lane, Dash(from), to, worker)
}

// moveTask decides and records the move of c.Task (under a.verdicts and a.submitMu): a task its worker works on
// (working, or dispatched with its lane prepared; never a drive), no step of it running or pending, the merge loop on
// and not paused, to c's first unpaused model, a model ref of the config whose kind has a bridge and that is not the
// one it runs ("already on"). Recorded: fact dispatch{redo, stop: its worker} of the next attempt on the task's lane,
// branch and target with the new model, with the moved attempt's outstanding fix (a sendback's or red gate's text,
// T-778). Refused, nothing recorded: gating, merging, under review or any other state of the merge loop (submitted
// meanwhile included).
func (a *App) moveTask(p *proj, fx *Loop, c wire.Call) (string, error) {
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	merging, pause := p.merging, p.paused(a.opt.Now())
	busy := p.busy(c.Task)
	dir := p.laneDir(mt)
	a.mu.Unlock()
	switch {
	case !merging:
		return "", mergeOff(p)
	case pause != nil:
		return "", pausedErr(c.Task, pause)
	case fx.Workflow == nil || fx.Specs == nil || fx.Agents == nil:
		return "", fmt.Errorf("dispatch off for %s (no lane adapter)", p.name)
	case mt.task == "":
		return "", fmt.Errorf("dispatch %s: %s is not open in obor serve", c.Task, c.Task)
	case mt.drive || !mt.may(vMove) || mt.is(stDispatched) && mt.base == "":
		return "", fmt.Errorf("dispatch %s: %s is %s: obor dispatch --model moves a task its worker works on, not "+
			"one gating, merging or under review", c.Task, c.Task, mt.shown())
	case busy != "":
		return "", busyErr("dispatch", c.Task, busy)
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	for _, t := range c.Types {
		if err := cfg.CheckModelRef(t); err != nil {
			return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
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
		return "", fmt.Errorf("dispatch %s: %s", c.Task, noLane(paused, c.Types))
	}
	typ, effort := config.ParseModelRef(ref)
	if kind := unbridged(cfg, typ); kind != "" {
		return "", fmt.Errorf("dispatch %s: model %s's kind %s has no bridge (obor config validate)", c.Task, typ, kind)
	}
	if ref == mt.disp.Type {
		return "", fmt.Errorf("dispatch %s: %s is already on %s (lane %s)", c.Task, c.Task, ref, mt.lane)
	}
	worker := mt.agent
	if worker == "" {
		worker = cfg.AgentName("worker", mt.lane)
	}
	attempt := mt.attempt + 1
	d := dispatchData{Lane: mt.lane, Dir: dir, Agent: worker, Branch: mt.disp.Branch, Target: mt.disp.Target, Redo: true,
		Force: c.Force, Types: c.Types, Effort: effort, Type: ref, Stop: worker, Caller: callerOf(c),
		Call: c.CallID, Reply: moveReply(c.Task, mt.lane, mt.disp.Type, ref, worker)}
	if mt.is(stWorking) && mt.work == "" { // a sent-back attempt (sendback, red gate): its fix moves with it
		d.Fix = mt.line
	} else { // the dispatched attempt itself: a fix a move gave it is still outstanding
		d.Fix = mt.disp.Fix
	}
	if !a.record(p, event.Record{Task: c.Task, Attempt: attempt, Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}
