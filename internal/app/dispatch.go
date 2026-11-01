package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// retries. obor serve is the only dispatcher (T-408); a loop without the dispatch adapters (Loop.Lane, Loop.Specs)
// refuses the request and ignores a dispatched task until its task_done submits it.

// MethodDispatch is the app socket's dispatch request: Call.Task onto Call.Lane, or the first free lane of Call.Types.
const MethodDispatch = "dispatch"

// The dispatch's steps.
const (
	StepDispatched = "dispatch"     // fact: the request passed its refusals (data: dispatchData)
	StepWaiting    = "waiting"      // fact: the request waits for its After tasks to merge (data: dispatchData)
	StepPrepare    = "lane.prepare" // intent/result (G), lane.prepare.N after a refusal: PrepareInput → prepareOut
	StepStartWork  = "agent.start"  // intent/result (S), agent.start.N after a refusal: startData → startOut
	StepWork       = "work"         // prompt intent (P), work.N after obor resume: the worker's job
)

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
		Lane   string   `json:"lane"`
		Dir    string   `json:"dir"`
		Agent  string   `json:"agent,omitempty"`
		Branch string   `json:"branch"`
		Target string   `json:"target"`
		Redo   bool     `json:"redo,omitempty"`
		Force  bool     `json:"force,omitempty"`
		Level  string   `json:"level,omitempty"` // the spec's difficulty level the types came from
		Types  []string `json:"types,omitempty"`
		After  []string `json:"after,omitempty"` // waiting: the tasks it waits for
		Caller string   `json:"caller,omitempty"`
		Call   string   `json:"call,omitempty"`
		Reply  string   `json:"reply"`
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
// as does a dispatch of a waiting task (whatever its call id).
func (a *App) dispatchCall(ctx context.Context, c Call) (string, error) {
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
		reply, stored = p.folded.calls[verdictStep(MethodDispatch, c.CallID)]
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
	reply, err = a.dispatchTask(ctx, p, fx, c)
	var nl noLaneError
	if errors.As(err, &nl) {
		return a.waitLane(p, c, nl)
	}
	return reply, err
}

// noLaneError is dispatchTask's refusal of a level dispatch (c.Level) when no lane of the level's types is free: the
// task waits for one (waitLane; for a waiting task, waitDispatch tries again).
type noLaneError struct {
	task, level string
	types       []string
	ls          []Lane
	note        string // the level's fallback, when it has no lane of its own (T-629)
}

func (e noLaneError) Error() string {
	return fmt.Sprintf("dispatch %s: waiting for a %s lane (%s)", e.task, e.level, strings.Join(e.types, ", "))
}

// laneWait is the status line of a task waiting for a lane of its level.
func (e noLaneError) laneWait() string {
	return fmt.Sprintf("waiting for a %s lane (%s)", e.level, strings.Join(e.types, ", "))
}

// resolveLevel gives c the lane types of its spec's difficulty level (c.Level, c.Types) when it names neither a lane
// nor types (--types), else leaves it (a waiting task's request carries its level and types): the types a lane has,
// and note the level's fallback when none of its own types has one (T-629). Why not: no level in the spec, or none of
// its types in the project.
func resolveLevel(cfg *config.Config, c *Call, text string) (note string, err error) {
	if c.Lane != "" || len(c.Types) > 0 {
		return "", nil
	}
	level, ok := spec.Difficulty(text)
	if !ok {
		return "", fmt.Errorf("the spec has no valid %q line under its title (%s): add it and obor spec set %s, or dispatch "+
			"with --types a,b", "Difficulty: <level>", strings.Join(spec.Levels, ", "), c.Task)
	}
	types, _ := cfg.DifficultyTypes(level)
	if len(types) == 0 {
		return "", fmt.Errorf("no lane type of level %s in this project (obor config get difficulty): dispatch with --types a,b", level)
	}
	c.Level, c.Types = level, types
	if len(cfg.DifficultyLevelTypes(level)) == 0 {
		note = fmt.Sprintf(" (level %s has no lane: using %s)", level, strings.Join(types, ", "))
	}
	return note, nil
}

// waitLane records fact waiting for c.Task's request (under a.verdicts) when no lane of its level is free (nl): the
// reply WAITING <task> (queued): waiting for a <level> lane (<types>) and the lanes idle of other types. It replaces a
// waiting fact of the task (a new request: types, after). The merge loop dispatches it when a lane of the level frees.
func (a *App) waitLane(p *proj, c Call, nl noLaneError) (string, error) {
	mt, held := a.held(p, c.Task)
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.state)
	}
	d := dispatchData{Force: c.Force, Level: nl.level, Types: nl.types, Caller: callerOf(c),
		Call: c.CallID, Reply: fmt.Sprintf("WAITING %s (queued): %s", c.Task, nl.laneWait()) + idleNote(nl.ls, "", held) + nl.note}
	if !a.record(p, Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: StepWaiting, Kind: KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// callerOf is who made c's request: its agent, else the owner.
func callerOf(c Call) string {
	if c.Agent == "" {
		return "owner"
	}
	return c.Agent
}

// dispatchTask passes c's refusals (under a.verdicts) and records fact dispatch for
// it: the reply DISPATCHED <task> → <lane> (queued) and the lanes still idle (idleNote). With c.After not all merged
// it records fact waiting instead (waitTask). A refusal records nothing.
func (a *App) dispatchTask(ctx context.Context, p *proj, fx *Loop, c Call) (string, error) {
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
	note, err := resolveLevel(cfg, &c, row.Text)
	if err != nil {
		return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
	}
	ls, err := a.lanes.Lanes(p.root)
	if err != nil {
		return "", fmt.Errorf("dispatch: lanes of %s: %v", p.name, err)
	}
	if len(c.After) > 0 {
		if reply, err := a.waitTask(p, c, ls); err != nil || reply != "" {
			return reply, err
		}
	}
	mt, held := a.held(p, c.Task)
	l, why := pickLane(ls, c, held)
	if why != "" && c.Level != "" {
		return "", noLaneError{task: c.Task, level: c.Level, types: c.Types, ls: ls, note: note}
	}
	if why != "" {
		return "", fmt.Errorf("dispatch %s: %s", c.Task, why)
	}
	if kind := unbridged(cfg, l.Type); kind != "" {
		return "", fmt.Errorf("dispatch %s: lane %s's kind %s has no bridge (obor config validate)", c.Task, l.Name, kind)
	}
	redo := l.Task == c.Task && (l.State == "working" || l.State == "blocked")
	switch {
	case redo:
	case l.State == "dirty-free" && !c.Force:
		return "", fmt.Errorf("dispatch %s: lane %s is dirty-free (uncommitted changes; --force to dispatch anyway)",
			c.Task, l.Name)
	case l.State == "working" && l.Task != "":
		return "", fmt.Errorf("dispatch %s: lane %s is working on %s", c.Task, l.Name, l.Task)
	case l.State != "free" && l.State != "dirty-free":
		return "", fmt.Errorf("dispatch %s: lane %s is %s", c.Task, l.Name, Dash(l.State))
	}
	target, ahead, err := fx.Specs.Target(ctx, c.Task)
	if err != nil {
		return "", fmt.Errorf("dispatch %s: %v", c.Task, err)
	}
	branch := strings.ReplaceAll(cfg.Data.Branches.Task, "{id}", c.Task)
	if ahead && !redo {
		return "", fmt.Errorf("dispatch %s: %s exists with commits not in the target (merge or delete it first)", c.Task,
			branch)
	}
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.state)
	}
	a.mu.Lock()
	var pane Pane
	l.Agent = rowAgent(cfg, l.Name, l.Agent)
	names := cfg.LaneAgentAliases(l.Name, "")
	if o := a.placement(p, l.Dir, append([]string{l.Agent}, names...)...); o != nil {
		pane = o.pane
	}
	a.mu.Unlock()
	agent := laneAgent(l, pane, names)
	caller := callerOf(c)
	d := dispatchData{Lane: l.Name, Dir: l.Dir, Agent: agent, Branch: branch, Target: target, Redo: redo, Force: c.Force,
		Level: c.Level, Types: c.Types, Caller: caller, Call: c.CallID,
		Reply: fmt.Sprintf("DISPATCHED %s → %s (queued)", c.Task, l.Name) + idleNote(ls, l.Name, held) + note}
	if !a.record(p, Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: StepDispatched, Kind: KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// unbridged is the kind of lane type typ when obor has no bridge for it (obor config validate refuses it; serve
// refuses to start one, DD-v0.13 D1); "": a bridged kind, or no such type (the start says so).
func unbridged(cfg *config.Config, typ string) string {
	lt, ok := cfg.LaneType(typ)
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

// laneAgent is the name agent.start gives lane l's worker: the lane row's agent (rowAgent), else the lane pane's label when that
// is one of the lane's names (hk-o3 stays hk-o3: a lane named by its compact alias keeps it; a pane labelled for
// something else, a shell's "notes", is not the worker's), else the lane's default name (roles.AgentName: the
// configured agent, else <prefix>-<lane lower>). herdr's live agent name is never read (RV-52 #2): it never
// overrides the label.
func laneAgent(l Lane, pane Pane, names []string) string {
	if l.Agent != "" {
		return l.Agent
	}
	if pane.Label != "" && slices.Contains(names, pane.Label) {
		return pane.Label
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
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

// idleNote is a dispatch reply's tail "; idle: <LANE> (<type>), …": the lanes of ls still free after it (not taken,
// not held for another task's dispatch; dirty-free is not idle); "" when none (T-483).
func idleNote(ls []Lane, taken string, held map[string]string) string {
	var idle []string
	for _, l := range ls {
		if l.State == "free" && l.Name != taken && held[l.Name] == "" {
			idle = append(idle, l.Name+" ("+Dash(l.Type)+")")
		}
	}
	if len(idle) == 0 {
		return ""
	}
	return "; idle: " + strings.Join(idle, ", ")
}

// pickLane is c's lane in ls: the lane it names, else the lane that holds c.Task (working or blocked on it: a redo,
// whatever its type, T-545), else the first lane of its first type that has a free one (types in the order given,
// lanes in config order) (dirty-free with force) and not held for another task's dispatch; else why not.
func pickLane(ls []Lane, c Call, held map[string]string) (Lane, string) {
	if c.Lane != "" {
		for _, l := range ls {
			if l.Name == c.Lane {
				if t := held[l.Name]; t != "" {
					return Lane{}, "lane " + l.Name + " is dispatched to " + t
				}
				l.Dir = filepath.Clean(l.Dir)
				return l, ""
			}
		}
		return Lane{}, "unknown lane " + c.Lane + " (not in the config's lanes)"
	}
	for _, l := range ls {
		if l.Task == c.Task && held[l.Name] == "" && (l.State == "working" || l.State == "blocked") {
			l.Dir = filepath.Clean(l.Dir)
			return l, ""
		}
	}
	for _, t := range c.Types {
		for _, l := range ls {
			if l.Type == t && held[l.Name] == "" && (l.State == "free" || c.Force && l.State == "dirty-free") {
				l.Dir = filepath.Clean(l.Dir)
				return l, ""
			}
		}
	}
	return Lane{}, "no free lane of the types " + strings.Join(c.Types, ",")
}

// dispatching: mt is a dispatch not yet submitted (its lane is reserved for it), or a needs-you it left.
func (m *mtask) dispatching() bool {
	return m.state == stDispatched || m.state == stWorking && m.work != "" || m.redoable()
}

// redoable: mt is a needs-you its dispatch left (a new dispatch of the task redoes it).
func (m *mtask) redoable() bool {
	return m.state == stNeedsYou && (m.left == stDispatched || m.left == stWorking && m.work != "")
}

// foldDispatch folds fact dispatch: a new attempt of the task, dispatched. A redo of a needs-you keeps the sendbacks.
func (p *proj) foldDispatch(r Record) {
	var d dispatchData
	_ = json.Unmarshal(r.Data, &d)
	at, _ := time.Parse(time.RFC3339, r.TS)
	next := &mtask{task: r.Task, lane: d.Lane, agent: d.Agent, attempt: r.Attempt, state: stDispatched, disp: d, at: at,
		dispSeq: r.Seq}
	if mt := p.folded.loop[r.Task]; mt != nil && mt.state != stMerged && mt.state != stRejected && mt.state != stDropped {
		next.sendbacks = mt.sendbacks
	}
	p.folded.loop[r.Task] = next
	delete(p.waitLine, r.Task) // the wait ended: a later wait's refusals start afresh
}

// waitTask records fact waiting for c (under a.verdicts) when a task of c.After is not merged in the loop: the reply
// WAITING <task> after <tasks> (queued) and every idle lane (idleNote: it takes none); "" with no error when all are
// merged (a plain dispatch). Refused: a task it names is itself, unknown (no spec and no loop record), dropped or
// rejected, or waiting (itself or through the tasks it waits for) for this one (a cycle); an unknown lane; the task
// open in the loop (as a dispatch).
func (a *App) waitTask(p *proj, c Call, ls []Lane) (string, error) {
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
		case m != nil && (m.state == stDropped || m.state == stRejected):
			return "", fmt.Errorf("dispatch %s: %s is %s: it never merges", c.Task, dep, m.state)
		case m == nil || m.state != stMerged:
			open = append(open, dep)
		}
	}
	if len(open) == 0 {
		return "", nil
	}
	if c.Lane != "" && !slices.ContainsFunc(ls, func(l Lane) bool { return l.Name == c.Lane }) {
		return "", fmt.Errorf("dispatch %s: unknown lane %s (not in the config's lanes)", c.Task, c.Lane)
	}
	mt, held := a.held(p, c.Task)
	if !mt.dispatchable() {
		return "", fmt.Errorf("dispatch %s: %s is open in obor serve (%s): obor resume or obor drop it first", c.Task,
			c.Task, mt.state)
	}
	caller := callerOf(c)
	d := dispatchData{Lane: c.Lane, Force: c.Force, Level: c.Level, Types: c.Types, After: c.After, Caller: caller, Call: c.CallID,
		Reply: fmt.Sprintf("WAITING %s after %s (queued)", c.Task, strings.Join(open, ", ")) + idleNote(ls, "", held)}
	if !a.record(p, Record{Task: c.Task, Attempt: mt.nextAttempt(), Step: StepWaiting, Kind: KindFact}, d) {
		return "", fmt.Errorf("dispatch: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// dispatchable: a dispatch (or a waiting one) of mt's task may be recorded: none yet, ended (merged, rejected,
// dropped), waiting, or a needs-you its dispatch or its wait left.
func (m *mtask) dispatchable() bool {
	switch m.kept() {
	case "", stMerged, stRejected, stDropped, stWaiting:
		return true
	}
	return m.redoable()
}

// nextAttempt is the attempt a dispatch of mt's task records: a waiting task's own (its wait recorded the next), else
// the next.
func (m *mtask) nextAttempt() int {
	if m.kept() == stWaiting {
		return m.attempt
	}
	return m.attempt + 1
}

// foldWaiting folds fact waiting: the task waits for its After tasks (no lane held: the lane is the dispatch's).
func (p *proj) foldWaiting(r Record) {
	var d dispatchData
	_ = json.Unmarshal(r.Data, &d)
	next := &mtask{task: r.Task, attempt: r.Attempt, state: stWaiting, disp: d}
	if mt := p.folded.loop[r.Task]; mt != nil && mt.state != stMerged && mt.state != stRejected && mt.state != stDropped {
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
		case m != nil && (m.state == stDropped || m.state == stRejected):
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
	_, err := a.dispatchTask(ctx, p, fx, Call{Root: p.root, Agent: d.Caller, CallID: d.Call, Task: mt.task,
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
	changed := !ok || w.line != err.Error()
	var nl noLaneError
	laneWait := errors.As(err, &nl)
	if changed {
		w = waitRefusal{line: err.Error(), since: now}
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
	return m != nil && m.state == stWaiting && m.attempt == mt.attempt && m.disp.Call == mt.disp.Call
}

// waitsOn: task waits (or its wait's needs-you is open), itself or through the tasks it waits for, for target (under
// mu).
func (p *proj) waitsOn(task, target string, seen map[string]bool) bool {
	m := p.folded.loop[task]
	if m == nil || m.kept() != stWaiting || seen[task] {
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

// nextDispatch is a dispatched task's next step (under mu): prepare the lane, start the worker, send the work job,
// then watch it until it is delivered. Nothing without the dispatch adapters.
func (a *App) nextDispatch(p *proj, mt mtask) step {
	if !p.dispatching {
		return nil
	}
	switch {
	case mt.refused != "":
		return func(context.Context, *Loop) bool { return a.needsYou(p, &mt, mt.cause, mt.refused) }
	case mt.base == "":
		return func(ctx context.Context, fx *Loop) bool { return a.prepare(ctx, p, fx, mt) }
	case !mt.started:
		return a.nextStart(p, mt)
	case mt.work == "":
		return func(ctx context.Context, fx *Loop) bool { return a.sendWork(ctx, p, fx, mt, nth(StepWork, mt.works+1)) }
	case p.log.Open(Key{mt.task, mt.attempt, mt.work}): // its prompt failed: sent again under its key
		return func(ctx context.Context, fx *Loop) bool { return a.sendWork(ctx, p, fx, mt, mt.work) }
	}
	return a.watchWork(p, mt)
}

// prepare runs lane.prepare (row 3): the lane on the task branch at the fact's target (a redo keeps the branch),
// lane_ready / lane_setup; its result is the lane's row (lanes.go). A refusal is final: needs-you{lane: why}.
func (a *App) prepare(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	d := mt.disp
	in := Intent{Key: Key{mt.task, mt.attempt, nth(StepPrepare, mt.prepares+1)}, Class: ClassGit,
		Data: PrepareInput{Task: mt.task, Lane: d.Lane, Dir: d.Dir, Branch: d.Branch, Target: d.Target, Agent: d.Agent,
			Redo: d.Redo, At: mt.at}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		out, err := fx.Lane.Prepare(ctx, in.Data.(PrepareInput))
		var refused Refused
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
// shell) and the agent in it are its intent's data. No pane in the lane for Gone: needs-you{no pane in lane}.
func (a *App) nextStart(p *proj, mt mtask) step {
	key := Key{mt.task, mt.attempt, nth(StepStartWork, mt.starts+1)}
	d := startData{Lane: mt.lane, Agent: mt.agent}
	if r, ok := p.log.Intent(key); ok { // a pending start: its recorded snapshot
		_ = json.Unmarshal(r.Data, &d)
	} else if o := a.placement(p, mt.disp.Dir, mt.agent); o != nil {
		d.Pane, d.Ran, d.Session = o.pane.ID, o.pane.Agent != "", o.pane.Session
	} else {
		now := a.opt.Now()
		since, ok := p.nopane[mt.task]
		if !ok {
			p.nopane[mt.task] = now
			return nil
		}
		if now.Sub(since) < a.opt.Gone {
			return nil
		}
		return func(context.Context, *Loop) bool {
			a.mu.Lock()
			delete(p.nopane, mt.task)
			a.mu.Unlock()
			return a.needsYou(p, &mt, causeNoPane, "no pane in lane "+mt.lane+" ("+mt.disp.Dir+"): open one there")
		}
	}
	delete(p.nopane, mt.task)
	// The work job the start runs under: one open from an earlier start, else the next.
	work := Key{mt.task, mt.attempt, nth(StepWork, mt.works+1)}
	if mt.work != "" && !p.log.Done(Key{mt.task, mt.attempt, mt.work}) {
		work.Step = mt.work
	}
	return func(ctx context.Context, fx *Loop) bool { return a.startWorker(ctx, p, fx, mt, key, d, work) }
}

// placement is the pane of p's workspace to start the agent of the lane at dir in: the one the table identifies as one
// of names (whoIs, DD-v0.13 A5: the lane's agent, not a second session someone opened there), else the lowest-id one
// there running an agent, else the lowest-id one there (at its shell); nil: none (under mu). A place to launch into
// (Launch stops what runs there), never an identity: who runs in a pane is whoIs's.
func (a *App) placement(p *proj, dir string, names ...string) *seen {
	var best *seen
	bestRank := 0
	for _, o := range a.obs {
		if !inDir(o.pane.Cwd, dir) || a.projectOf(o.pane) != p {
			continue
		}
		r := 1
		if agent, ok := p.reg.whoIs(o.pane); ok && agent != "" && slices.Contains(names, agent) {
			r = 3
		} else if o.pane.Agent != "" {
			r = 2
		}
		if best == nil || r > bestRank || r == bestRank && o.pane.ID < best.pane.ID {
			best, bestRank = o, r
		}
	}
	return best
}

// startWorker runs agent.start (S): a fresh session of the lane's agent in its pane (Agents.Launch, force as the
// dispatch said). A replay (the intent pending) first reads the snapshot (startSeen): started → the result, no second
// start; not started → it starts again (Restart stops what is there); cannot tell → refused: needs-you{start: cannot
// tell …}, never a second start. A launch refusal (background tasks without force, the name held by another pane) is
// final: needs-you{start: why}. It runs under work job's intent and carries its wake (startJob).
func (a *App) startWorker(ctx context.Context, p *proj, fx *Loop, mt mtask, key Key, d startData, work Key) bool {
	replay := p.log.Open(key)
	in := Intent{Key: key, Class: ClassStart, Data: d}
	return a.startJob(ctx, p, mt, in, work, promptData{Agent: mt.agent}, func(ctx context.Context, first string) (any, error) {
		if replay {
			switch a.startSeen(p, d) {
			case seenStarted: // the start that ran carried the same first prompt
				return startOut{Line: "started (seen in the snapshot)", Agent: d.Agent, Pane: d.Pane}, nil
			case seenUnknown:
				return startOut{Refused: "cannot tell whether the agent in " + d.Pane + " is the one started (herdr " +
					"reports no session id): look at the pane, then obor resume " + mt.task}, nil
			}
		}
		s, err := fx.Agents.Launch(ctx, Launch{Agent: d.Agent, Pane: d.Pane, Fresh: true, Force: mt.disp.Force,
			Prompt: first})
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

// startRefused: the launch refused for good: the agent has background tasks (no force), or its name is held by
// another pane.
func startRefused(err error) bool {
	return ErrorCode(err) == "agent_name_taken" || strings.Contains(err.Error(), "has background tasks")
}

// sendWork sends the worker its work job's wake (row 5) under step (work, work.N), job <task>/<attempt>/<step>: a job
// its start did not carry (startJob). A prompt that fails stays pending and is sent again under its key.
func (a *App) sendWork(ctx context.Context, p *proj, fx *Loop, mt mtask, step string) bool {
	key := Key{mt.task, mt.attempt, step}
	at := a.opt.Now()
	if !a.sendWorkJob(ctx, p, fx, mt, key, key, fmt.Sprintf("work job %s not sent to %s", JobID(key), mt.agent)) {
		return false
	}
	a.mu.Lock()
	p.timers[mt.task] = &jobTimer{job: JobID(key), sent: at}
	a.mu.Unlock()
	return true
}

// sendWorkJob types mt's work job's (job) wake under key (the job's own, or its retry).
func (a *App) sendWorkJob(ctx context.Context, p *proj, fx *Loop, mt mtask, key, job Key, what string) bool {
	return a.send(ctx, p, fx, mt, key, JobID(job), promptData{Agent: mt.agent}, wakeText, what)
}

// workText is mt's worker prompt, from the project's config and the obor kit at kit as they are now: the prompt
// template, then the worker's role texts (T-603: one read of them instead of two turns).
func workText(p *proj, mt mtask, kit string) (string, error) {
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	var lt config.LaneType
	for _, l := range cfg.Lanes() {
		if l.Name == mt.lane {
			lt, _ = cfg.LaneType(l.Type)
		}
	}
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
	key := Key{mt.task, mt.attempt, mt.work}
	t := a.timerOf(p, key)
	return a.undelivered(p, mt, t, key, mt.agent, func(ctx context.Context, fx *Loop, retry Key) bool {
		return a.sendWorkJob(ctx, p, fx, mt, retry, key, fmt.Sprintf("work job %s not sent again to %s", JobID(key),
			mt.agent))
	})
}

// workerPrompt is the worker prompt, the work job's text job_next hands out: templates/worker-prompt.txt rendered (its
// lines joined by spaces), the lane type's prompt_extra; no backticks (herdr gotcha). It carries no sender tag of its
// own. The worker's role texts follow it (workText). {{base}} is the target branch (branches.main). Placeholders:
//
//	{{task}} {{base}}
//	{{done_sections}}  done_file_sections, comma-separated: the sections task_done's report must have
func workerPrompt(cfg *config.Config, kit, task string, lt config.LaneType) (string, error) {
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
