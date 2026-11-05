package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
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
// retries. obor serve is the only dispatcher (T-408); a loop without the dispatch adapters (Loop.Lane, Loop.Specs)
// refuses the request and ignores a dispatched task until its task_done submits it.

// The causes of the dispatch's needs-you; obor resume retries each (the step runs again, a refused one under its next
// key, an undelivered job sent anew).
const (
	causeLane   = "lane"
	causeStart  = "start"
	causeNoPane = "no pane in lane"
	causeAfter  = "after"
)

// Specs is what a dispatch reads of the project's task ids and git (wired by obor serve).
type Specs interface {
	// Check is why task cannot be dispatched by its id or its spec ("" when it can): a bad task id, no stored spec
	// (text "": the store has none, T-440). An error: it cannot tell.
	Check(ctx context.Context, task, text string) (refusal string, err error)
	// Target is the target branch's sha now, and whether task's branch has commits the target lacks.
	Target(ctx context.Context, task string) (sha string, ahead bool, err error)
}

// Records' data.
type (
	dispatchData struct {
		port.Dispatch          // first: the record's lane, dir, agent, branch, target, redo
		Force         bool     `json:"force,omitempty"`
		Level         string   `json:"level,omitempty"`  // the spec's difficulty level the models came from
		Types         []string `json:"types,omitempty"`  // model refs, `<model>[:<effort>]` (T-695, T-765)
		Effort        string   `json:"effort,omitempty"` // the effort the lane's agent starts with ("": its kind's default; T-695)
		// Type is the model ref the dispatch chose, `<model>[:<effort>]` (T-721): the lane row's type. "": a record
		// from before T-721 or a waiting one.
		Type  string   `json:"type,omitempty"`
		After []string `json:"after,omitempty"` // waiting: the tasks it waits for
		// Drive: the owner's drive (obor drive, T-751): no worker, no work job; after lane.prepare the task is driving.
		Drive bool `json:"drive,omitempty"`
		// Fix: a move's (T-778) outstanding fix of the attempt it moved, a sendback's or a red gate's text (Answer: the
		// answer to the worker's question, an older log's obor answer); the moved attempt's work job carries it.
		Fix    string `json:"fix,omitempty"`
		Answer bool   `json:"answer,omitempty"`
		// Stop: the worker the merge loop stops (with force) before the step that needs the lane (T-852): a move's (the
		// redo's, before lane.prepare), a take-over's (fact drive, before the drive agent starts). "": none.
		Stop   string `json:"stop,omitempty"`
		Caller string `json:"caller,omitempty"`
		Call   string `json:"call,omitempty"`
		Reply  string `json:"reply"`
	}
	prepareOut struct {
		Base    string `json:"base,omitempty"`
		Head    string `json:"head,omitempty"` // "": a result from before T-439
		Refused string `json:"refused,omitempty"`
	}
	// startData is agent.start's intent: the lane's pane and the agent in it at intent time (Ran; Session herdr's
	// session id of it, "": none known), what a replay compares the snapshot with. (Before T-371 the intent held the
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
	// the identity table (foldStart).
	startOut struct {
		Line     string `json:"line,omitempty"`
		Agent    string `json:"agent,omitempty"`
		Pane     string `json:"pane,omitempty"`
		Terminal string `json:"terminal,omitempty"`
		Kind     string `json:"kind,omitempty"`
		Session  string `json:"sid,omitempty"`
		Refused  string `json:"refused,omitempty"`
	}
)

// dispatchCall answers the socket's dispatch request: a call id is required and a repeated one gets the stored reply,
// as does a dispatch of a waiting task (whatever its call id). --model on a task its worker works on moves it (T-773,
// move.go): the move is recorded at once, and the merge loop stops the worker before the new model's start (T-852).
// It decides and records under a.verdicts, a move under a.submitMu too (lock order a.verdicts, then a.submitMu:
// task_done's lock).
func (a *App) dispatchCall(ctx context.Context, c wire.Call) (string, error) {
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
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("dispatch: the lead or the owner dispatches, not %s (role %s)", Dash(c.Agent), c.Role)
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
	if errors.As(err, &nl) {
		reply, err = a.waitLane(p, c, nl)
	}
	return reply, err
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
func resolveLevel(cfg *config.Config, c *wire.Call, text string) error {
	if len(c.Types) > 0 {
		return nil
	}
	level, ok := spec.Difficulty(text)
	if !ok {
		return fmt.Errorf("the spec has no valid %q line under its title (%s): add it and obor spec set %s, or dispatch "+
			"with --model a,b", "Difficulty: <level>", strings.Join(spec.Levels, ", "), c.Task)
	}
	all := specFirst(cfg, text, knownModels(cfg, cfg.DifficultyList(level)))
	if len(all) == 0 {
		return fmt.Errorf("no model of level %s is known to this project (obor config get difficulty): dispatch with "+
			"--model a,b", level)
	}
	c.Level, c.Types = level, all
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
// the level's first unpaused one (T-689).
func specFirst(cfg *config.Config, text string, refs []string) []string {
	out := knownModels(cfg, spec.Models(text))
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
// (waiting tasks oldest first, passOrder).
func (a *App) waitLane(p *proj, c wire.Call, nl noLaneError) (string, error) {
	mt, held := a.held(p, c.Task)
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.shown())
	}
	d := dispatchData{Force: c.Force, Level: nl.level, Types: nl.types, Caller: callerOf(c),
		Call: c.CallID, Reply: fmt.Sprintf("WAITING %s (queued): %s", c.Task, nl.laneWait()) + levelNote(nl.level, nl.types) + idleNote(nl.ls, "", held)}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: event.StepWaiting, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// callerOf is who made c's request: its agent, else the owner.
func callerOf(c wire.Call) string {
	if c.Agent == "" {
		return "owner"
	}
	return c.Agent
}

// dispatchTask passes c's refusals (under a.verdicts) and records fact dispatch for
// it: the reply DISPATCHED <task> → <lane> (queued) and the lanes still idle (idleNote). With c.After not all merged
// it records fact waiting instead (waitTask). A refusal records nothing.
func (a *App) dispatchTask(ctx context.Context, p *proj, fx *Loop, c wire.Call) (string, error) {
	a.mu.Lock()
	merging, pause := p.merging, p.paused(a.opt.Now())
	a.mu.Unlock()
	switch {
	case !merging:
		return "", mergeOff(p)
	case pause != nil:
		return "", pausedErr(c.Task, pause)
	case fx.Lane == nil || fx.Specs == nil:
		return "", fmt.Errorf("dispatch off for %s (no lane adapter)", p.name)
	}
	// A merged task takes no new attempt, whatever flags the request has (T-792); a rejected or dropped one may.
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
	if why, err := fx.Specs.Check(ctx, c.Task, row.Text); err != nil || why != "" {
		if err != nil {
			why = err.Error()
		}
		return "", fmt.Errorf("dispatch %s: %s", c.Task, why)
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	if err := resolveLevel(cfg, &c, row.Text); err != nil {
		return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
	}
	// Refused before a lane is picked (nothing recorded): a model ref naming no model of this config, or an effort
	// its kind does not take (T-695).
	for _, t := range c.Types {
		if err := cfg.CheckModelRef(t); err != nil {
			return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
		}
	}
	ls, err := a.lanes.Lanes(ctx, p.root)
	if err != nil {
		return "", fmt.Errorf("dispatch: lanes of %s: %v", p.name, err)
	}
	if len(c.After) > 0 {
		if reply, err := a.waitTask(p, c, ls); err != nil || reply != "" {
			return reply, err
		}
	}
	mt, held := a.held(p, c.Task)
	a.mu.Lock()
	finished := p.finishedIn(c.Task)
	a.mu.Unlock()
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
	target, ahead, err := fx.Specs.Target(ctx, c.Task)
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
	a.mu.Lock()
	l.Agent = rowAgent(cfg, l.Name, l.Agent)
	names := cfg.LaneAgentAliases(l.Name, "")
	a.placement(p, l.Dir, append([]string{l.Agent}, names...)...)
	a.mu.Unlock()
	agent := laneAgent(l, cfg.AgentName("worker", l.Name))
	caller := callerOf(c)
	d := dispatchData{Lane: l.Name, Dir: l.Dir, Agent: agent, Branch: branch, Target: target, Redo: redo, Force: c.Force,
		Level: c.Level, Types: c.Types, Effort: effort, Type: ref, Caller: caller, Call: c.CallID,
		Reply: fmt.Sprintf("DISPATCHED %s → %s (queued)", c.Task, l.Name) + typeNote(typ) + effortNote(effort) +
			idleNote(ls, l.Name, held) + note}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// unbridged is the kind of model typ when obor has no bridge for it (obor config validate refuses it; serve refuses
// to start one, DD-v0.13 D1); "": a bridged kind, or no such model (the start says so).
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

// rowAgent is the agent a lane's row names when it is still one of the lane's names under cfg (a name the config gave
// it; the rule a pane's label meets too), else "": serve rebuilds the rows from the log, whose old names (before an
// obor setup rename) must not pick the worker.
func rowAgent(cfg *config.Config, lane, agent string) string {
	if agent != "" && slices.Contains(cfg.LaneAgentAliases(lane, ""), agent) {
		return agent
	}
	return ""
}

// laneAgent is the name agent.start gives lane l's worker: the lane row's agent (rowAgent), else its derived name
// (<prefix>-<lane lower>, T-777: a lane pane's old label is no name of the worker any more). herdr's live agent name is
// never read (RV-52 #2).
func laneAgent(l port.Lane, derived string) string {
	if l.Agent != "" {
		return l.Agent
	}
	return derived
}

// held is task's loop record (zero: none) and the lanes other tasks' open dispatches hold (lane → task).
func (a *App) held(p *proj, task string) (mtask, map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held := map[string]string{}
	var mt mtask
	newest := map[string]int64{} // a lane two dispatches name: the newest dispatch's task holds it (T-615)
	for _, t := range p.loopTasks() {
		if m := p.folded.loop[t]; t == task {
			mt = *m
		} else if _, ok := held[m.lane]; m.dispatching() && (!ok || m.dispSeq > newest[m.lane]) {
			held[m.lane], newest[m.lane] = t, m.dispSeq
		}
	}
	return mt, held
}

// freeFor: lane l can take a new dispatch: git says it is free (force: dirty-free too) and no other task's dispatch
// holds it (held, a.held's).
func freeFor(l port.Lane, held map[string]string, force bool) bool {
	return held[l.Name] == "" && (l.State == port.LaneFree || force && l.State == port.LaneDirtyFree)
}

// idleNote is a dispatch reply's tail "; idle: <LANE>, …": the lanes of ls still free after it (not taken, freeFor
// without force: dirty-free is not idle); "" when none (T-483).
func idleNote(ls []port.Lane, taken string, held map[string]string) string {
	var idle []string
	for _, l := range ls {
		if l.Name != taken && freeFor(l, held, false) {
			idle = append(idle, l.Name)
		}
	}
	if len(idle) == 0 {
		return ""
	}
	return "; idle: " + strings.Join(idle, ", ")
}

// typeNote is a dispatch reply's " as <model>" (the model its dispatch picked); "" for none.
func typeNote(typ string) string {
	if typ == "" {
		return ""
	}
	return " as " + typ
}

// pausedModels is the models of types (model refs) that obor pause --model holds now (T-765), each with its reason
// "<model> paused: <why> (until HH:MM|cleared)": dispatch skips them, a level's next model is used.
func (a *App) pausedModels(p *proj, types []string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]string{}
	for _, t := range types {
		typ, _ := config.ParseModelRef(t)
		if ps := p.modelPaused(typ, a.opt.Now()); ps != nil && out[typ] == "" {
			out[typ] = fmt.Sprintf("%s paused: %s (until %s)", typ, ps.Why, untilText(ps.Until))
		}
	}
	return out
}

// effortNote is a dispatch reply's " effort <effort>" (T-695); "" for none.
func effortNote(effort string) string {
	if effort == "" {
		return ""
	}
	return " effort " + effort
}

// pickLane is c's lane in ls and the model ref its dispatch runs (every lane is pooled, T-765): the lane it names
// (c's first model), else the lane that holds c.Task (working or blocked on it, and the task not finished: a redo,
// T-545) with ran, the model ref the task's dispatch ran (effort kept, T-738), unless c's explicit models (no level)
// name another (c's first), else for each model of c in order, skipped while paused, the first lane in config order
// that is free (dirty-free with force) and not held for another task's dispatch; else why not. A redo that changes the
// model it runs is held to the new model's pause like a fresh dispatch of it (T-742).
func pickLane(ls []port.Lane, c wire.Call, held map[string]string, paused map[string]string, ran string,
	finished bool) (port.Lane, string, string) {
	first := ""
	for _, t := range c.Types {
		if typ, _ := config.ParseModelRef(t); paused[typ] == "" {
			first = t
			break
		}
	}
	if c.Lane != "" {
		for _, l := range ls {
			if l.Name == c.Lane {
				if t := held[l.Name]; t != "" {
					return port.Lane{}, "", "lane " + l.Name + " is dispatched to " + t
				}
				if first == "" {
					return port.Lane{}, "", noLane(paused, c.Types)
				}
				l.Dir = filepath.Clean(l.Dir)
				return l, first, ""
			}
		}
		return port.Lane{}, "", "unknown lane " + c.Lane + " (not in the config's lanes)"
	}
	for _, l := range ls {
		if l.Task == c.Task && !finished && held[l.Name] == "" && l.State == port.LaneWorking {
			l.Dir = filepath.Clean(l.Dir)
			explicit := c.Level == "" && len(c.Types) > 0
			ref := ran
			if explicit || ran == "" {
				ref = first
			}
			ranType, _ := config.ParseModelRef(ran)
			if typ, _ := config.ParseModelRef(ref); ref == "" || typ != ranType && paused[typ] != "" {
				return port.Lane{}, "", noLane(paused, c.Types)
			}
			return l, ref, ""
		}
	}
	if first != "" {
		for _, l := range ls {
			if freeFor(l, held, c.Force) {
				l.Dir = filepath.Clean(l.Dir)
				return l, first, ""
			}
		}
	}
	return port.Lane{}, "", noLane(paused, c.Types) + unknownNote(ls)
}

// unknownNote is a lane refusal's tail naming the lanes whose state is unknown (T-871): dispatch never picks one, and
// its reply says which lane and why. "" when none.
func unknownNote(ls []port.Lane) string {
	var out []string
	for _, l := range ls {
		if l.State == port.LaneUnknown {
			out = append(out, l.Name+" unknown: "+l.Reason)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "; " + strings.Join(out, "; ")
}

// laneWhy is a lane's state for a refusal: its state, and for an unknown one why (T-871).
func laneWhy(l port.Lane) string {
	if l.State == port.LaneUnknown {
		return "unknown: " + l.Reason
	}
	return Dash(l.State)
}

// noLane is pickLane's refusal when none of types gets a lane: the models, and those paused.
func noLane(paused map[string]string, types []string) string {
	var at []string
	for _, t := range types {
		if typ, _ := config.ParseModelRef(t); paused[typ] != "" && !slices.Contains(at, paused[typ]) {
			at = append(at, paused[typ])
		}
	}
	why := "no free lane for the models " + strings.Join(types, ",")
	if len(at) > 0 {
		why += " (" + strings.Join(at, ", ") + ")"
	}
	return why
}

// refusedCause is the cause of mt's refused dispatch step: the lane's prepare (no base yet), else the agent's start.
func (m *mtask) refusedCause() string {
	if m.base == "" {
		return causeLane
	}
	return causeStart
}

// dispatching: mt is a dispatch not yet submitted (its lane is reserved for it), a drive (T-751), also while a
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
	if d.Stop != "" { // a move: the old worker stopped first (T-852)
		next.halt.ask(d.Stop, true)
	}
	p.folded.loop[r.Task] = next
	delete(p.waitLine, r.Task) // the wait ended: a later wait's refusals start afresh
}

// waitTask records fact waiting for c (under a.verdicts) when a task of c.After is not merged in the loop: the reply
// WAITING <task> after <tasks> (queued) and every idle lane (idleNote: it takes none); "" with no error when all are
// merged (a plain dispatch). Refused: a task it names is itself, unknown (no spec and no loop record), dropped or
// rejected, or waiting (itself or through the tasks it waits for) for this one (a cycle); an unknown lane; the task
// open in the loop (as a dispatch).
func (a *App) waitTask(p *proj, c wire.Call, ls []port.Lane) (string, error) {
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
	caller := callerOf(c)
	d := dispatchData{Lane: c.Lane, Force: c.Force, Level: c.Level, Types: c.Types, After: c.After, Caller: caller, Call: c.CallID,
		Reply: fmt.Sprintf("WAITING %s after %s (queued)", c.Task, strings.Join(open, ", ")) + idleNote(ls, "", held)}
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
			if fx.Scratch == nil {
				return false
			}
			if landed, err := fx.Scratch.Landed(ctx, dep); err != nil || !landed {
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
	_, err := a.dispatchTask(ctx, p, fx, wire.Call{Root: p.root, Agent: d.Caller, CallID: d.Call, Task: mt.task,
		Lane: d.Lane, Level: d.Level, Types: d.Types, Force: d.Force})
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
	laneWait := errors.As(err, &nl)
	if laneWait {
		line = nl.laneWait() // the status line reads it after the level (T-723)
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

// nextDispatch is a dispatched task's next step (under mu): a move's stop of the old worker (pickStop), prepare the
// lane, start the worker, send the work job, then watch it until it is delivered. Nothing without the dispatch
// adapters.
func (a *App) nextDispatch(p *proj, mt mtask) step {
	if !p.dispatching {
		return nil
	}
	if s := a.pickStop(p, mt); s != nil || mt.halt.pending() { // a move's: the old worker stopped first (T-852)
		return s
	}
	switch {
	case mt.refused != "":
		return func(context.Context, *Loop) bool { return a.needsYou(p, &mt, mt.refusedCause(), mt.refused) }
	case mt.base == "":
		return func(ctx context.Context, fx *Loop) bool { return a.prepare(ctx, p, fx, mt) }
	case !mt.started:
		return a.nextStart(p, mt)
	case mt.work == "":
		return func(ctx context.Context, fx *Loop) bool {
			return a.sendWork(ctx, p, fx, mt, nth(event.StepWork, mt.works+1))
		}
	case p.log.Open(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}): // its prompt failed: sent again under its key
		return func(ctx context.Context, fx *Loop) bool { return a.sendWork(ctx, p, fx, mt, mt.work) }
	}
	return a.watchWork(p, mt)
}

// prepare runs lane.prepare (row 3): the lane on the task branch at the fact's target (a redo keeps the branch),
// lane_ready / lane_setup; its result is the lane's row (lanes.go). A refusal is final: needs-you{lane: why}.
func (a *App) prepare(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepPrepare, mt.prepares+1)}, Class: event.ClassGit,
		Data: port.PrepareInput{Task: mt.task, Dispatch: mt.disp.Dispatch, At: mt.at}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		out, err := fx.Lane.Prepare(ctx, in.Data.(port.PrepareInput))
		var refused port.Refused
		if errors.As(err, &refused) {
			return prepareOut{Refused: refused.Line}, nil
		}
		if err != nil {
			return nil, err
		}
		return prepareOut{Base: out.Base, Head: out.Head}, nil
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// nextStart is agent.start's step (row 4, under mu): the snapshot's pane in the lane (an agent's, else one at its
// shell) and the agent in it are its intent's data. A pending start keeps its recorded pane while herdr still has it; a
// pane it names that is gone (its tab closed, a new labelled one made by obor setup pane) is chosen again, as a first
// start's is, and never the recorded id (T-783; one that runs an agent is cannot tell, startWorker). No pane in the
// lane for Gone: herdr's panes are read again (a cd or a rename in one gives no event, T-745, RV-101 #2) and the agent
// starts if that finds one; still none: needs-you{no pane in lane}. A failed read is no answer: nothing is recorded and
// the next pass reads again.
func (a *App) nextStart(p *proj, mt mtask) step {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepStartWork, mt.starts+1)}
	d := startData{Lane: mt.lane, Agent: mt.agent}
	pending := false
	if r, ok := p.log.Intent(key); ok { // a pending start: its recorded snapshot
		_ = json.Unmarshal(r.Data, &d)
		pending = true
	}
	if !pending || a.obs[d.Pane] == nil {
		o := a.placement(p, mt.disp.Dir, mt.agent)
		if o == nil {
			return a.waitPane(p, mt)
		}
		d.Pane, d.Ran, d.Session = o.pane.ID, o.pane.Agent != "", o.pane.Session
	}
	delete(p.nopane, mt.task)
	// The work job the start runs under: one open from an earlier start, else the next.
	work := event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepWork, mt.works+1)}
	if mt.work != "" && !p.log.Done(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}) {
		work.Step = mt.work
	}
	return func(ctx context.Context, fx *Loop) bool { return a.startWorker(ctx, p, fx, mt, key, d, work) }
}

// waitPane is nextStart's wait for a pane of the lane (under mu): nothing until opt.Gone has passed since the first pass
// without one, then herdr's panes are read again before the owner is told; the agent starts in a pane the read finds
// (the next pass: the wait ends only once the snapshot, with the read's cwd and label, places it there), and none is
// needs-you{no pane in lane}.
func (a *App) waitPane(p *proj, mt mtask) step {
	now := a.opt.Now()
	since, ok := p.nopane[mt.task]
	if !ok {
		p.nopane[mt.task] = now
		return nil
	}
	if now.Sub(since) < a.opt.Gone {
		return nil
	}
	return func(ctx context.Context, _ *Loop) bool {
		// The snapshot's cwd can be stale (a cd in the pane gives no event, T-745): read herdr's panes again before
		// the owner is told there is none. A failed or canceled read records nothing, so the task keeps waiting on
		// the same clock and the next pass tries again (RV-89 #1).
		panes, err := a.rereadPanes(ctx)
		if err != nil {
			return false
		}
		// The list answers which pane is in the lane now, and nothing else: a pane the stream closed since the read
		// is not raised again, and every other field and pane stays the stream's (RV-89 #2). One the stream still
		// has: its cwd is what changed, and its label where the snapshot's holds it back (labelled for another
		// agent: a rename gives no event either, RV-101 #2); the next pass starts the agent in it.
		a.mu.Lock()
		o := a.paneIn(p, panes, mt.disp.Dir, mt.agent)
		usable := false
		if o != nil && a.obs[o.ID] != nil { // not closed by the stream since the read: that wins
			s := &a.obs[o.ID].pane
			s.Cwd = o.Cwd
			if otherAgent(p.root, s.Label, []string{mt.agent}) {
				s.Label = o.Label
			}
			usable = a.placement(p, mt.disp.Dir, mt.agent) != nil
		}
		if o == nil || usable { // the read settled the wait: a pane the next pass starts in, or none to wait for
			delete(p.nopane, mt.task)
		}
		a.mu.Unlock()
		if o == nil {
			return a.needsYou(p, &mt, causeNoPane, "no pane in lane "+mt.lane+" ("+mt.disp.Dir+"): open one there")
		}
		return usable
	}
}

// placement is paneIn over the snapshot (under mu): the pane of p's workspace to start the lane at dir's agent in; nil:
// none. A place to launch into (Launch stops what runs there), never an identity: who runs in a pane is whoIs's.
func (a *App) placement(p *proj, dir string, names ...string) *seen {
	panes := make([]port.Pane, 0, len(a.obs))
	for _, o := range a.obs {
		panes = append(panes, o.pane)
	}
	if pane := a.paneIn(p, panes, dir, names...); pane != nil {
		return a.obs[pane.ID]
	}
	return nil
}

// paneIn is the pane of panes (the snapshot's, or a fresh read's) to start the lane at dir's agent in, ranked: the one
// the table identifies as one of names (whoIs, DD-v0.13 A5: the lane's agent, not a second session someone opened
// there) > the one labelled one of names (the label is the pane's identity, R-28 §2: `obor setup pane` labels a pane
// before any agent ran there, T-783) > the one running an agent > the one at its shell; ties by lowest id. A pane
// labelled for another agent of the project is never chosen (its label is that agent's, and the launch would refuse
// it); a label the config does not know (an old alias, T-777) names nobody and does not hold the pane back. nil: none
// (under mu). A place to launch into (Launch stops what runs there), never an identity: who runs in a pane is
// whoIs's.
func (a *App) paneIn(p *proj, panes []port.Pane, dir string, names ...string) *port.Pane {
	var best *port.Pane
	bestRank := 0
	for i := range panes {
		if !inDir(panes[i].Cwd, dir) || a.projectOf(panes[i]) != p || otherAgent(p.root, panes[i].Label, names) {
			continue
		}
		r := 1
		agent, identified := p.reg.whoIs(panes[i])
		switch {
		case identified && agent != "" && slices.Contains(names, agent):
			r = 4
		case slices.Contains(names, panes[i].Label):
			r = 3
		case panes[i].Agent != "":
			r = 2
		}
		if best == nil || r > bestRank || r == bestRank && panes[i].ID < best.ID {
			best, bestRank = &panes[i], r
		}
	}
	return best
}

// otherAgent is whether label names an agent of root's project other than names (under mu): a lane's, a role's or an
// agents entry's name, as launch resolves who a label is (config.AgentLookup). A label the config does not know — an
// alias T-777 dropped — names nobody.
func otherAgent(root, label string, names []string) bool {
	if label == "" || slices.Contains(names, label) {
		return false
	}
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return false
	}
	_, ok := cfg.AgentLookup(label, "", nil)
	return ok
}

// startWorker runs agent.start (S): a fresh session of the lane's agent in its pane (Agents.Launch, force as the
// dispatch said). A replay (the intent pending) first reads the snapshot (startSeen): started → the result, no second
// start; not started → it starts again (Restart stops what is there); cannot tell → refused: needs-you{start: cannot
// tell …}, never a second start. A replay whose intent names another pane than d has its recorded pane gone, and its
// start chose one again (nextStart): nothing of the intent's pane is left to compare with, so it starts as a first
// start does there (T-783) when that pane is at its shell; one that runs an agent is cannot tell (moved): the intent
// stays the gone pane's (its key is pending), so a start there that ran and left no result cannot be told from any
// other agent, and a second start would stop a worker already at its job (RV-101 #1). A launch refusal (background
// tasks without force, the name held by another pane) is final: needs-you{start: why}. It runs under work job's intent
// and carries its wake (startJob).
func (a *App) startWorker(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key, d startData, work event.Key) bool {
	pending := p.log.Open(key)
	var rec startData
	if pending {
		if r, ok := p.log.Intent(key); ok {
			_ = json.Unmarshal(r.Data, &rec)
		}
	}
	replay := pending && rec.Pane == d.Pane
	moved := pending && rec.Pane != d.Pane // the recorded pane is gone: d.Pane was chosen again
	in := event.Intent{Key: key, Class: event.ClassStart, Data: d}
	return a.startJob(ctx, p, mt, in, work, promptData{Agent: mt.agent}, func(ctx context.Context, first string) (any, error) {
		if moved {
			if runs := a.runsIn(d.Pane); runs != "" {
				return startOut{Refused: "cannot tell whether the agent in " + d.Pane + " is the one started: the start " +
					"was recorded in " + rec.Pane + ", gone now, and " + d.Pane + " runs " + runs + ": look at the pane, " +
					"then obor resume " + mt.task}, nil
			}
		}
		if replay {
			switch a.startSeen(p, d) {
			case seenStarted: // the start that ran carried the same first prompt
				return startOut{Line: "started (seen in the snapshot)", Agent: d.Agent, Pane: d.Pane}, nil
			case seenUnknown:
				return startOut{Refused: "cannot tell whether the agent in " + d.Pane + " is the one started (herdr " +
					"reports no session id): look at the pane, then obor resume " + mt.task}, nil
			}
		}
		typ, _ := config.ParseModelRef(mt.disp.Type) // the dispatch's type, not the lane's configured one (T-722)
		s, err := fx.Agents.Launch(ctx, port.Launch{Agent: d.Agent, Pane: d.Pane, Fresh: true, Force: mt.disp.Force,
			Prompt: first, Effort: mt.disp.Effort, Type: typ})
		switch {
		case err != nil && startRefused(err):
			return startOut{Refused: err.Error()}, nil
		case err != nil:
			return nil, err
		}
		return startOut{Line: s.Line, Agent: d.Agent, Pane: d.Pane, Terminal: s.Terminal, Kind: s.Kind, Session: s.Session}, nil
	})
}

// What the snapshot says of a pending agent.start (startSeen).
const (
	seenNotStarted = iota
	seenStarted
	seenUnknown
)

// startSeen is the case table of a pending agent.start (docs/state-model.md row 4): pane d.Pane (of p) runs no
// agent now → not started; it runs one and none ran at the intent → started; one ran then too: its session id
// differs from the recorded one → started, the same → not started, unknown on either side → cannot tell. A session
// is herdr's session id (Pane.Session), never whether its transcript file is found.
func (a *App) startSeen(p *proj, d startData) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.obs[d.Pane]
	switch {
	case o == nil || o.pane.Agent == "" || a.projectOf(o.pane) != p:
		return seenNotStarted
	case !d.Ran:
		return seenStarted
	case d.Session == "" || o.pane.Session == "":
		return seenUnknown
	case o.pane.Session != d.Session:
		return seenStarted
	}
	return seenNotStarted
}

// runsIn is what runs in pane now, as the snapshot has it: "" for a pane at its shell (or gone), else its agent kind,
// its label and herdr's session id where known ("claude (label pj-l-01, session s1)").
func (a *App) runsIn(pane string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.obs[pane]
	if o == nil || o.pane.Agent == "" {
		return ""
	}
	var of []string
	if o.pane.Label != "" {
		of = append(of, "label "+o.pane.Label)
	}
	if o.pane.Session != "" {
		of = append(of, "session "+o.pane.Session)
	}
	if len(of) == 0 {
		return o.pane.Agent
	}
	return o.pane.Agent + " (" + strings.Join(of, ", ") + ")"
}

// startRefused: the launch refused for good: the agent has background tasks (no force), or its name is held by
// another pane.
func startRefused(err error) bool {
	return port.ErrorCode(err) == "agent_name_taken" || strings.Contains(err.Error(), "has background tasks")
}

// sendWork sends the worker its work job's wake (row 5) under step (work, work.N), job <task>/<attempt>/<step>: a job
// its start did not carry (startJob). A prompt that fails stays pending and is sent again under its key.
func (a *App) sendWork(ctx context.Context, p *proj, fx *Loop, mt mtask, step string) bool {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: step}
	at := a.opt.Now()
	if !a.sendWorkJob(ctx, p, fx, mt, key, key, fmt.Sprintf("work job %s not sent to %s", JobID(key), mt.agent)) {
		return false
	}
	a.jobSent(p, mt.task, JobID(key), at)
	return true
}

// sendWorkJob types mt's work job's (job) wake under key (the job's own, or its retry).
func (a *App) sendWorkJob(ctx context.Context, p *proj, fx *Loop, mt mtask, key, job event.Key, what string) bool {
	return a.send(ctx, p, fx, mt, key, JobID(job), promptData{Agent: mt.agent}, wakeText, what)
}

// workText is mt's worker prompt, from the project's config and the obor kit at kit as they are now: the prompt
// template (its prompt_extra the dispatch's model's kind's), then the worker's role texts (T-603: one read of them instead of two turns).
func workText(p *proj, mt mtask, kit string) (string, error) {
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	typ, _ := config.ParseModelRef(mt.disp.Type) // T-721, T-738
	lt, _ := cfg.Model(typ)
	text, err := workerPrompt(cfg, kit, mt.task, lt)
	if err != nil {
		return "", err
	}
	roles, read, err := roleTexts(cfg, kit)
	if err != nil {
		return "", err
	}
	if len(read) > 0 {
		text += " Read your role text before you act: " + strings.Join(read, ", ") + " (too large to carry here)."
	}
	if roles != "" {
		text += "\n\n" + roles
	}
	return text, nil
}

// roleTextMax is the largest role text the work job inlines: a bigger one is named for the worker to read, which
// costs fewer tokens than carrying it in the job.
const roleTextMax = 12 << 10

// roleFile is one role text of the work job: the line naming it and its path.
type roleFile struct{ title, path string }

// roleTexts is the work job's role texts as they are now: the obor kit's templates/agents/worker.md and the project's
// worker doc (<paths.agents>/worker.md) when the main checkout has one. Each comes under a line naming its path; one
// larger than roleTextMax is not inlined, its path is returned in read instead. An empty "## This project" section
// (the template's fill-in note and nothing else) is not sent.
func roleTexts(cfg *config.Config, kit string) (text string, read []string, err error) {
	files := []roleFile{{"Your role text", filepath.Join(kit, "templates", "agents", "worker.md")}}
	doc := filepath.Join(cfg.Root, cfg.Data.Paths["agents"], "worker.md")
	if _, err := os.Stat(doc); err == nil {
		files = append(files, roleFile{"This project's worker rules", doc})
	}
	var parts []string
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			return "", nil, fmt.Errorf("cannot read the role text %s", f.path)
		}
		body := dropEmptyProjectSection(string(b))
		if len(body) > roleTextMax {
			read = append(read, f.path)
			continue
		}
		parts = append(parts, f.title+" ("+f.path+"):\n"+body)
	}
	return strings.Join(parts, "\n\n"), read, nil
}

// dropEmptyProjectSection removes a trailing "## This project" section that holds only blank lines and HTML comments
// (the template's fill-in note for a new project): an empty section is not sent in the job.
func dropEmptyProjectSection(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "## This project" {
			continue
		}
		comment := false
		for _, rest := range lines[i+1:] {
			t := strings.TrimSpace(rest)
			switch {
			case comment:
				comment = !strings.Contains(t, "-->")
			case strings.HasPrefix(t, "<!--"):
				comment = !strings.Contains(t, "-->")
			case t != "":
				return text
			}
		}
		return strings.TrimRight(strings.Join(lines[:i], "\n"), " \t\n")
	}
	return text
}

// watchWork is the work job's delivery timer (rows 6–8, under mu): delivered once the worker's job_next fetched it
// (fact delivered: the task is working); undelivered for deliveryWait: retry.work, then the owner. The timer is not
// recorded: a replay starts it afresh.
func (a *App) watchWork(p *proj, mt mtask) step {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}
	t := a.timerOf(p, key)
	return a.undelivered(p, mt, t, key, mt.agent, func(ctx context.Context, fx *Loop, retry event.Key) bool {
		return a.sendWorkJob(ctx, p, fx, mt, retry, key, fmt.Sprintf("work job %s not sent again to %s", JobID(key),
			mt.agent))
	})
}

// workerPrompt is the worker prompt, the work job's text job_next hands out: templates/worker-prompt.txt rendered (its
// lines joined by spaces), the model's kind's prompt_extra (kinds.<kind>.prompt_extra); no backticks (herdr gotcha). It carries no sender tag of its
// own. The worker's role texts follow it (workText). {{base}} is the target branch (branches.main). Placeholders:
//
//	{{task}} {{base}}
//	{{done_sections}}  done_file_sections, comma-separated: the sections task_done's report must have
func workerPrompt(cfg *config.Config, kit, task string, lt config.Model) (string, error) {
	file := filepath.Join(kit, "templates", "worker-prompt.txt")
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("cannot read the worker prompt %s", file)
	}
	fill := strings.NewReplacer("{{task}}", task, "{{base}}", cfg.Data.Branches.Main,
		"{{done_sections}}", strings.Join(cfg.Data.DoneFileSections, ", "))
	var parts []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts = append(parts, fill.Replace(line))
	}
	if lt.PromptExtra != "" {
		parts = append(parts, lt.PromptExtra)
	}
	return strings.ReplaceAll(strings.Join(parts, " "), "`", "'"), nil
}

// pluginNote warns, in dispatch's reply, that a Claude lane's worktree has no enabled obor plugin (T-705): its worker
// would run without obor's hooks and MCP tools (no job_next).
func pluginNote(cfg *config.Config, l port.Lane, typ string) string {
	kind := project.RunningKind(cfg, config.AgentInfo{Type: typ})
	if k, known := agents.Lookup(kind); !known || !k.Caps().ProjectPlugin || config.PluginEnabledIn(l.Dir) {
		return ""
	}
	return fmt.Sprintf("\nWARN lane %s has no enabled obor plugin (%s/.claude/settings.json): obor setup lane or obor init links it", l.Name, l.Dir)
}
