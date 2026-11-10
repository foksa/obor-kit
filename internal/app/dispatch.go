// dispatch.go: the dispatch request: the types, dispatchCall, the queue and lane waits it answers with, the level and
// model checks, dispatchTask and the fold of the dispatch fact (the lane pick, waiting, start and work text sit beside it).
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

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/spec"
)

// Dispatch as state (docs/state-model.md rows 2–9). A dispatch is a task record,
// not a command: the request (obor dispatch over the socket) passes its refusals and writes fact dispatch{lane, attempt,
// target}; the task is dispatched. A request naming tasks it waits for (--after) that are not all merged writes fact
// waiting instead: the merge loop dispatches it once they are and a lane is free (pickWaiting, readiness rule 1). The
// merge loop then runs the dispatch's steps, each its own intent and result under the failure rule (App.do):
// lane.prepare (G: the lane on the task branch at the target), agent.start (S: a fresh worker in the lane's pane) and
// the work job's wake (job <task>/<attempt>/work: the start's first prompt, else typed; prompt.go). The worker's job_next is fact delivered: the task is working, and its task_done then
// submits it. Undelivered 5 min: retry.work, 5 more: needs-you{job not delivered}. A
// step's final refusal (Prepare's or the launch's) or its third counted failure is a needs-you that obor resume
// retries. obor serve is the only dispatcher; a loop without the dispatch adapters (Loop.Workflow, Loop.Specs)
// refuses the request and ignores a dispatched task until its task_done submits it.

// The causes of the dispatch's needs-you; obor resume retries each (the step runs again, a refused one under its next
// key, an undelivered job sent anew).
const (
	causeLane   = "lane"
	causeStart  = "start"
	causeNoPane = "no pane in lane"
	causeAfter  = "after"
)

// Specs is what a dispatch reads of the project's task ids (wired by obor serve); the target it reads is the
// workflow's (port.Workflow.Target).
type Specs interface {
	// Check is why task cannot be dispatched by its id or its spec ("" when it can): a bad task id, no stored spec
	// (text "": the store has none). An error: it cannot tell.
	Check(ctx context.Context, task, text string) (refusal string, err error)
}

// Records' data.
type (
	dispatchData struct {
		port.Dispatch          // first: the record's lane, dir, agent, branch, target, redo
		Force         bool     `json:"force,omitempty"`
		Level         string   `json:"level,omitempty"`  // the spec's difficulty level the models came from
		Types         []string `json:"types,omitempty"`  // model refs, `<model>[:<effort>]`
		Effort        string   `json:"effort,omitempty"` // the effort the lane's agent starts with ("": its kind's default)
		// Type is the model ref the dispatch chose, `<model>[:<effort>]`: the lane row's type. "": a record
		// that records none, or a waiting one.
		Type  string   `json:"type,omitempty"`
		After []string `json:"after,omitempty"` // waiting: the tasks it waits for
		// Drive: the owner's drive (obor drive): no worker, no work job; after lane.prepare the task is driving.
		Drive bool `json:"drive,omitempty"`
		// Fix: a move's outstanding fix of the attempt it moved, a sendback's or a red gate's text; the moved
		// attempt's work job carries it.
		Fix string `json:"fix,omitempty"`
		// Stop: the worker the merge loop stops (with force) before the step that needs the lane: a move's (the
		// redo's, before lane.prepare), or a keep lane's live agent whose recorded start does not run this dispatch's
		// model (keepStop: stopped between tasks, the dispatch's model started). "": none.
		Stop string `json:"stop,omitempty"`
		// Keep: the lane is a keep lane (config.LaneKeep), as the config said at the dispatch: the merged task's
		// after-merge steps keep its agent (afterList), the start runs only when the lane has no live agent
		// (nextDispatch: only when its recorded start runs this dispatch's model), and a waiting one queues for its
		// lane in the scheduler's order (keepAhead).
		Keep   bool   `json:"keep,omitempty"`
		Caller string `json:"caller,omitempty"`
		Call   string `json:"call,omitempty"`
		Reply  string `json:"reply"`
	}
	prepareOut struct {
		Base    string `json:"base,omitempty"`
		Refused string `json:"refused,omitempty"`
	}
	// startData is agent.start's intent: the lane's pane and the agent in it at intent time (Ran; Session herdr's
	// session id of it, "": none known), what a replay compares the snapshot with. (An older intent held the
	// transcript under "session": such an intent has no session id, a replay of it cannot tell.) It runs under its
	// job's intent and carries its wake (startJob); older intents carry the job's id as "wake", ignored.
	startData struct {
		Lane    string `json:"lane"`
		Agent   string `json:"agent"`
		Pane    string `json:"pane"`
		Ran     bool   `json:"ran,omitempty"`
		Session string `json:"sid,omitempty"`
	}
	// startOut is agent.start's result, and the integrator's start.<job> (not "running"): not refused, its job's
	// delivery (startJob). Agent, Pane, Terminal, Kind, Session: the agent started there, the pane's terminal id, its
	// kind and herdr's session id of it ("": none reported, or a replay that saw it in the snapshot): the start row of
	// the identity table (foldStart). Config is the config revision the start was launched under (config.Config.Rev).
	// The start's row keeps it, so a keep lane reuses the agent only while the config is that same config
	// (startFits). agent.start's alone: the integrator's start.<job>, and an older record, carry none.
	startOut struct {
		Line     string `json:"line,omitempty"`
		Agent    string `json:"agent,omitempty"`
		Pane     string `json:"pane,omitempty"`
		Terminal string `json:"terminal,omitempty"`
		Kind     string `json:"kind,omitempty"`
		Session  string `json:"sid,omitempty"`
		Config   string `json:"config,omitempty"`
		Refused  string `json:"refused,omitempty"`
	}
)

// dispatchCall answers the socket's dispatch request: a call id is required and a repeated one gets the stored reply,
// as does a dispatch of a waiting task (whatever its call id). --model on a task its worker works on moves it
// (move.go): the move is recorded at once, and the merge loop stops the worker before the new model's start.
// It decides and records under a.verdicts, a move under a.submitMu too (lock order a.verdicts, then a.submitMu:
// task_done's lock).
func (a *App) dispatchCall(ctx context.Context, c wire.DispatchReq) (string, error) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	if c.CallID == "" || c.Task == "" {
		return "", errors.New("dispatch: a call id and a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(wire.MethodDispatch, c.CallID)]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("dispatch: no served project at %s", c.Root)
	case stored:
		return reply, nil
	}
	if err := leadOr(p.root, wire.MethodDispatch, c.Envelope, fmt.Sprintf("dispatch: the lead or the owner dispatches, not %s (role %s)",
		Dash(c.Agent), c.Role)); err != nil {
		return "", err
	}
	fx, err := a.loopOf(p)
	if err != nil {
		return "", err
	}
	if a.moving(p, c) {
		a.submitMu.Lock()
		defer a.submitMu.Unlock()
		return a.moveTask(p, fx, c)
	}
	reply, err = a.dispatchTask(ctx, p, fx, c)
	var nl noLaneError
	var qe queueError
	switch {
	case errors.As(err, &nl):
		reply, err = a.waitLane(p, c, nl)
	case errors.As(err, &qe):
		reply, err = a.waitQueue(p, c, qe)
	}
	return reply, err
}

// queueError is dispatchTask's answer to a dispatch naming a keep lane that is busy: a task holds it (its
// dispatch, or its work not landed yet), or an older dispatch waits for it; the task queues behind (waitQueue records
// it waiting, waitDispatch tries again in dispatch order). Level and types are the request's, resolved; ls the lanes read.
type queueError struct {
	task, lane, behind string
	level              string
	types              []string
	ls                 []port.Lane
}

func (e queueError) Error() string { return "dispatch " + e.task + ": " + e.why() }

// why is the queued task's wait: "behind <task> on keep lane <lane>".
func (e queueError) why() string { return "behind " + e.behind + " on keep lane " + e.lane }

// waitQueue records fact waiting for c.Task's request (under a.verdicts) queued on the keep lane qe names: the reply
// WAITING <task> (queued): behind <task> on keep lane <lane>. As waitLane's, it replaces a waiting fact of the task;
// the merge loop dispatches it when the lane frees and no wait the scheduler puts first is eligible for it (keepAhead).
func (a *App) waitQueue(p *proj, c wire.DispatchReq, qe queueError) (string, error) {
	mt, held := a.held(p, c.Task)
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.shown())
	}
	d := dispatchData{Force: c.Force, Level: qe.level, Types: qe.types, Keep: true, Caller: callerOf(c.Envelope), Call: c.CallID}
	d.Lane = qe.lane
	d.Reply = fmt.Sprintf("WAITING %s (queued): %s", c.Task, qe.why()) + idleNote(qe.ls, "", held)
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: event.StepWaiting, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.mu.Lock()
	p.waitLine[c.Task] = waitRefusal{line: qe.why(), since: a.opt.Now()} // obor status' why until the loop tries it
	a.mu.Unlock()
	a.wake()
	return d.Reply, nil
}

// keepBusy is the task a dispatch of c naming keep lane c.Lane queues behind ("": the lane takes it now): a dispatch
// waiting for the lane that the scheduler puts first (ahead, keepAhead's), else the task whose dispatch holds it
// (held), else the task the lane works on (its row's task not landed: submitted, in the gate, in review), unless that
// is c.Task itself, not finished (a redo, pickLane's).
func keepBusy(ls []port.Lane, c wire.DispatchReq, held map[string]string, finished bool, ahead string) string {
	if ahead != "" {
		return ahead
	}
	if t := held[c.Lane]; t != "" {
		return t
	}
	for _, l := range ls {
		if l.Name == c.Lane && l.State == port.LaneWorking && l.Task != "" && (l.Task != c.Task || finished) {
			return l.Task
		}
	}
	return ""
}

// afterMerged: every task m waits for (--after) merged in the loop (under mu).
func (p *proj) afterMerged(m *mtask) bool {
	for _, dep := range m.disp.After {
		if d := p.folded.loop[dep]; d == nil || d.state != stMerged {
			return false
		}
	}
	return true
}

// laneQueues is each keep lane's queue (under mu): the tasks waiting for it, in the pass's order (passOrder: the
// order keepAhead admits them in; obor status' lane line and the view, its owner of a task's keep lane).
func (p *proj) laneQueues() map[string][]string {
	var out map[string][]string
	for _, t := range p.passOrder() { // the waiting ones last, a high one first, then oldest wait first
		if m := p.folded.loop[t]; m.state == stWaiting && m.disp.Keep && m.disp.Lane != "" {
			if out == nil {
				out = map[string][]string{}
			}
			out[m.disp.Lane] = append(out[m.disp.Lane], t)
		}
	}
	return out
}

// noLaneError is dispatchTask's refusal of a level dispatch (c.Level) when none of the level's models gets a lane
// (each paused, or no lane free): the task waits (waitLane; for a waiting task, waitDispatch tries again).
type noLaneError struct {
	task, level string
	types       []string
	ls          []port.Lane
	paused      map[string]string // model → "<model> paused: <why> (until …)"
}

func (e noLaneError) Error() string { return "dispatch " + e.task + ": " + e.laneWait() }

// laneWait is why the task waits: "<model> paused: …" when every model of its request is paused (each named), else
// "waiting for a free lane".
func (e noLaneError) laneWait() string {
	var paused []string
	for _, t := range e.types {
		typ, _ := config.ParseModelRef(t)
		if why := e.paused[typ]; why == "" {
			return LaneWaitFree
		} else if !slices.Contains(paused, why) {
			paused = append(paused, why)
		}
	}
	if len(paused) == 0 {
		return LaneWaitFree
	}
	return strings.Join(paused, ", ")
}

// LaneWaitFree is a lane wait's reason when a model of the request is not paused: no lane is free (obor status shows
// it so).
const LaneWaitFree = "waiting for a free lane"

// levelNote is a lane wait's " (<level>: <models>)".
func levelNote(level string, types []string) string {
	return " (" + level + ": " + strings.Join(types, ", ") + ")"
}

// resolveLevel gives c the model refs of its spec's difficulty level (c.Level, c.Types; the spec's `Model:` refs
// first) when it names none (--model); else it leaves it (a waiting task's request carries its level and models). Why
// not: no level in the spec, or neither a `Model:` ref nor a ref of the level names a model of the config.
func resolveLevel(cfg *config.Config, c *wire.DispatchReq, sp spec.Spec) error {
	if len(c.Types) > 0 {
		return nil
	}
	if !sp.LevelKnown {
		return fmt.Errorf("the spec has no valid %q line under its title (%s): add it and obor spec set %s, or dispatch "+
			"with --model a,b", "Difficulty: <level>", strings.Join(spec.Levels, ", "), c.Task)
	}
	all := specFirst(cfg, sp, knownModels(cfg, cfg.DifficultyList(sp.Level)))
	if len(all) == 0 {
		return fmt.Errorf("no model of level %s is known to this project (obor config get .agents.difficulty): dispatch with "+
			"--model a,b", sp.Level)
	}
	c.Level, c.Types = sp.Level, all
	return nil
}

// knownModels keeps the refs of refs that name a model of cfg.
func knownModels(cfg *config.Config, refs []string) []string {
	var out []string
	for _, t := range refs {
		if typ, _ := config.ParseModelRef(t); typ != "" {
			if _, ok := cfg.Model(typ); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// specFirst is refs with the spec's `Model:` refs in front (those naming a model of cfg): a named model wins, else
// the level's first unpaused one.
func specFirst(cfg *config.Config, sp spec.Spec, refs []string) []string {
	out := knownModels(cfg, sp.Models)
	for _, t := range refs {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// waitLane records fact waiting for c.Task's request (under a.verdicts) when none of its level's models gets a lane
// (nl): the reply WAITING <task> (queued): <why> (<level>: <models>) and the lanes still idle. It replaces a waiting
// fact of the task (a new request: models, after). The merge loop dispatches it when a lane frees or a pause ends
// (passOrder: a high task first, then oldest wait first).
func (a *App) waitLane(p *proj, c wire.DispatchReq, nl noLaneError) (string, error) {
	mt, held := a.held(p, c.Task)
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.shown())
	}
	d := dispatchData{Force: c.Force, Level: nl.level, Types: nl.types, Caller: callerOf(c.Envelope),
		Call: c.CallID, Reply: fmt.Sprintf("WAITING %s (queued): %s", c.Task, nl.laneWait()) + levelNote(nl.level, nl.types) + idleNote(nl.ls, "", held)}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: event.StepWaiting, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// callerOf is who made c's request: its agent, else the owner.
func callerOf(c wire.Envelope) string {
	if c.Agent == "" {
		return "owner"
	}
	return c.Agent
}

// dispatchTask passes c's refusals (under a.verdicts) and records fact dispatch for
// it: the reply DISPATCHED <task> → <lane> (queued) and the lanes still idle (idleNote). With c.After not all merged
// it records fact waiting instead (waitTask). A refusal records nothing.
func (a *App) dispatchTask(ctx context.Context, p *proj, fx *Loop, c wire.DispatchReq) (string, error) {
	a.mu.Lock()
	merging, pause := p.merging, p.paused(a.opt.Now())
	a.mu.Unlock()
	switch {
	case !merging:
		return "", mergeOff(p)
	case pause != nil:
		return "", pausedErr(c.Task, pause)
	case fx.Workflow == nil || fx.Specs == nil:
		return "", fmt.Errorf("dispatch off for %s (no lane adapter)", p.name)
	}
	// A merged task takes no new attempt, whatever flags the request has; a rejected or dropped one may.
	if mt, _ := a.held(p, c.Task); mt.state == stMerged {
		sha := mt.head
		if len(sha) > 7 {
			sha = sha[:7]
		}
		if sha == "" {
			sha = "outside obor"
		}
		return "", fmt.Errorf("%s merged %s: a new attempt needs a new task id", c.Task, sha)
	}
	row, _, err := specOf(p, c.Task)
	if err != nil {
		return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
	}
	if why := a.specDropped(p, c.Task); why != "" { // a spec-only drop, until a new rev
		return "", fmt.Errorf("dispatch %s: %s", c.Task, why)
	}
	if why, err := fx.Specs.Check(ctx, c.Task, row.Text); err != nil || why != "" {
		if err != nil {
			why = err.Error()
		}
		return "", fmt.Errorf("dispatch %s: %s", c.Task, why)
	}
	if err := a.fixesCheck(p, c.Task, row.Spec); err != nil { // a fix links to the open review only
		return "", err
	}
	cfg, err := config.Load(p.root)
	if err != nil {
		return "", err
	}
	if err := resolveLevel(cfg, &c, row.Spec); err != nil {
		return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
	}
	// Refused before a lane is picked (nothing recorded): a model ref naming no model of this config, or an effort
	// its kind does not take.
	for _, t := range c.Types {
		if err := cfg.CheckModelRef(t); err != nil {
			return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
		}
	}
	ls, err := a.lanes.Lanes(ctx, p.root)
	if err != nil {
		return "", fmt.Errorf("dispatch: lanes of %s: %v", p.name, err)
	}
	keep := c.Lane != "" && cfg.KeepLane(c.Lane) // a dispatch naming a busy keep lane queues
	if len(c.After) > 0 {
		if reply, err := a.waitTask(p, c, ls, keep); err != nil || reply != "" {
			return reply, err
		}
	}
	mt, held := a.held(p, c.Task)
	a.mu.Lock()
	finished := p.finishedIn(c.Task)
	a.mu.Unlock()
	if keep {
		a.mu.Lock()
		ahead := p.keepAhead(c.Task, c.Lane, a.opt.Now())
		a.mu.Unlock()
		if behind := keepBusy(ls, c, held, finished, ahead); behind != "" {
			return "", queueError{task: c.Task, lane: c.Lane, behind: behind, level: c.Level, types: c.Types, ls: ls}
		}
	}
	paused := a.pausedModels(p, c.Types)
	l, ref, why := pickLane(ls, c, held, paused, mt.disp.Type, finished)
	if why != "" && c.Level != "" && c.Lane == "" {
		return "", noLaneError{task: c.Task, level: c.Level, types: c.Types, ls: ls, paused: paused}
	}
	if why != "" {
		return "", fmt.Errorf("dispatch %s: %s", c.Task, why)
	}
	typ, effort := config.ParseModelRef(ref)
	if kind := unbridged(cfg, typ); kind != "" {
		return "", fmt.Errorf("dispatch %s: model %s's kind %s has no bridge (obor config validate)", c.Task, typ, kind)
	}
	note := pluginNote(cfg, l, typ)
	redo := l.Task == c.Task && !finished && l.State == port.LaneWorking
	switch {
	case redo:
	case l.State == port.LaneDirtyFree && !c.Force:
		return "", fmt.Errorf("dispatch %s: lane %s is dirty-free (uncommitted changes; --force to dispatch anyway)",
			c.Task, l.Name)
	case l.State == port.LaneWorking && l.Task != "":
		return "", fmt.Errorf("dispatch %s: lane %s is working on %s", c.Task, l.Name, l.Task)
	case l.State != port.LaneFree && l.State != port.LaneDirtyFree:
		return "", fmt.Errorf("dispatch %s: lane %s is %s", c.Task, l.Name, laneWhy(l))
	}
	target, ahead, err := fx.Workflow.Target(ctx, c.Task)
	if err != nil {
		return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
	}
	branch := cfg.TaskBranch(c.Task)
	if ahead && !redo {
		return "", fmt.Errorf("dispatch %s: %s exists with commits not in the target (merge or delete it first)", c.Task,
			branch)
	}
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.shown())
	}
	agent := cfg.AgentName("worker", l.Name) // derived (a lane pane's old label is no name of the worker)
	caller := callerOf(c.Envelope)
	d := dispatchData{Lane: l.Name, Dir: l.Dir, Agent: agent, Branch: branch, Target: target, Redo: redo, Force: c.Force,
		Level: c.Level, Types: c.Types, Effort: effort, Type: ref, Keep: cfg.KeepLane(l.Name), Stop: a.keepStop(p, cfg, l, agent, ref),
		Caller: caller, Call: c.CallID,
		Reply: fmt.Sprintf("DISPATCHED %s → %s (queued)", c.Task, l.Name) + typeNote(typ) + effortNote(effort) +
			idleNote(ls, l.Name, held) + note}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// unbridged is the kind of model typ when obor has no bridge for it (obor config validate refuses it; serve refuses
// to start one); "": a bridged kind, or no such model (the start says so).
func unbridged(cfg *config.Config, typ string) string {
	lt, ok := cfg.Model(typ)
	if !ok {
		return ""
	}
	if _, ok := agents.Lookup(lt.Kind); !ok {
		return Dash(lt.Kind)
	}
	return ""
}

// refusedCause is the cause of mt's refused dispatch step: the lane's prepare (no base yet), else the agent's start.
func (m *mtask) refusedCause() string {
	if m.base == "" {
		return causeLane
	}
	return causeStart
}

// dispatching: mt is a dispatch not yet submitted (its lane is reserved for it), a drive, also while a
// needs-you holds one.
func (m *mtask) dispatching() bool {
	return m.state == stDispatched || m.state == stWorking && m.work != "" || m.is(stDriving)
}

// redoable: a needs-you holds mt's dispatch (a new dispatch of the task redoes it).
func (m *mtask) redoable() bool {
	return m.paused() && (m.state == stDispatched || m.state == stWorking && m.work != "")
}

// foldDispatch folds fact dispatch: a new attempt of the task, dispatched. A redo of a needs-you keeps the sendbacks.
func (p *proj) foldDispatch(r event.Record) {
	var d dispatchData
	_ = json.Unmarshal(r.Data, &d)
	at, _ := time.Parse(time.RFC3339, r.TS)
	next := &mtask{task: r.Task, lane: d.Lane, agent: d.Agent, attempt: r.Attempt, state: stDispatched, disp: d, at: at,
		dispSeq: r.Seq, drive: d.Drive}
	if mt := p.folded.loop[r.Task]; mt != nil && !states[mt.state].ended {
		next.sendbacks = mt.sendbacks
	}
	if d.Stop != "" { // a move: the old worker stopped first
		next.halt.ask(d.Stop, true)
	}
	p.folded.loop[r.Task] = next
	delete(p.waitLine, r.Task) // the wait ended: a later wait's refusals start afresh
}
