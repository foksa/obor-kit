// watchdog.go: the watchdogs (check, plan, decide, judge, sample) over the agents herdr reports.
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"sort"
	"time"

	"obor/internal/agents"
)

// check is one decision over the project (full: a tick's or a connect's, counted for the tests, which scans its lanes
// first; else the refresher's, on the table it just scanned for the pane events and own operations that marked it:
// lanescan.go, T-879). It asks herdr about the agents whose idle timers are due and records the facts due. Only the
// steps that decide hold mu.
func (a *App) check(ctx context.Context, full bool) {
	a.mu.Lock()
	if !a.connected {
		a.mu.Unlock()
		return
	}
	p := a.p
	if p != nil {
		a.reconcile(p)
	}
	a.mu.Unlock()
	if p != nil {
		if full {
			a.scan(ctx, p)
		}
		a.decideLanes(ctx, p)
	}
	a.mu.Lock()
	if full {
		a.checks++
	}
	a.mu.Unlock()
	a.wake()
}

// decideLanes decides on p's lane table as last scanned: it asks herdr about the agents whose idle timers are due and
// records the facts due. A table that is no observation (its scan failed, or it is older than lanesBound) is not
// decided on, and every open task's elapsed-time baselines drop (forget): no time it did not observe counts.
func (a *App) decideLanes(ctx context.Context, p *proj) {
	now := a.opt.Now()
	a.mu.Lock()
	ls, ok := p.scan.observed(now, a.lanesBound())
	if !ok {
		for _, w := range p.tasks {
			w.forget()
		}
		a.mu.Unlock()
		return
	}
	if p.folded.lr.err != nil { // the last write of the lanes table failed: again
		if err := p.syncLanes(); err != nil {
			a.logf("lanes table of %s: %v", p.root, err)
		}
	}
	ask := a.plan(p, ls, now)
	transcripts := map[string]string{}
	for _, o := range ask {
		transcripts[o.ID] = a.transcript(p, o)
	}
	a.mu.Unlock()
	ev := map[string]*evidence{}
	for _, o := range ask {
		e := a.evidence(ctx, o.ID)
		if turnsRead(o.Agent) {
			e.turn, e.turned = a.panes.Turn(ctx, o.ID, transcripts[o.ID])
		}
		ev[o.ID] = e
	}
	a.mu.Lock()
	a.decide(p, ev, now)
	a.mu.Unlock()
}

// openTasks is p's open tasks, sorted.
func (p *proj) openTasks() []string {
	tasks := make([]string, 0, len(p.tasks))
	for task := range p.tasks {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	return tasks
}

// plan installs the lanes read and observes each open task's agent (a task on an unknown lane: none, T-897, its
// baselines dropped, T-879): another agent or pane, none, or a status that is not ready resets the stall timer (see
// sample). It is the panes to sample: every ready agent with an open task, at every check, and a stopped worker of a
// kind whose turns are read whatever its status (its next turn ends the stop: turnStop).
func (a *App) plan(p *proj, ls []port.Lane, now time.Time) []port.Pane {
	var ask []port.Pane
	p.setLanes(ls)
	a.reconcile(p) // the lanes' agents are candidates
	for _, task := range p.openTasks() {
		w := p.tasks[task]
		if w.unread() { // no observation: its facts stay (T-897), no time counts until a read succeeds (T-879)
			w.forget()
			continue
		}
		at, _ := a.agentOf(p, w.lane)
		if at.State != Identified {
			w.quietSince, w.seenWrite, w.blocked = time.Time{}, time.Time{}, time.Time{}
			if (w.agent.ID != "" || at.State == Ambiguous) && w.lost.IsZero() {
				w.lost = now
			}
			continue
		}
		pane := at.Pane
		if pane.ID != w.agent.ID || pane.Agent != w.agent.Agent {
			w.quietSince, w.seenWrite, w.blocked = time.Time{}, time.Time{}, time.Time{}
		}
		w.agent, w.lost = pane, time.Time{}
		switch {
		case Ready(pane.Status):
			w.blocked = time.Time{}
			ask = append(ask, pane)
			continue
		case pane.Status == "blocked":
			w.quietSince, w.seenWrite = time.Time{}, time.Time{}
			if w.blocked.IsZero() {
				w.blocked = now
			}
		default:
			w.quietSince, w.seenWrite, w.blocked = time.Time{}, time.Time{}, time.Time{}
		}
		if p.stops(task) && turnsRead(pane.Agent) {
			ask = append(ask, pane)
		}
	}
	return ask
}

// decide records the facts due for p's open tasks (its lanes just read), with the evidence sampled.
func (a *App) decide(p *proj, ev map[string]*evidence, now time.Time) {
	for _, task := range p.openTasks() {
		w := p.tasks[task]
		if w.unread() { // an unknown lane is never judged (T-897)
			continue
		}
		if a.turnStop(p, task, w, ev) {
			continue
		}
		v := a.judge(p, task, w, now, ev)
		if v.step != "" && a.fact(p, task, w, v) && v.step == event.StepStalled {
			w.quietSince = now // the escalation counts from the stall
		}
	}
}

// verdict is what judge finds for one open task.
type verdict struct {
	step, reason string    // the fact due ("": none)
	cause        string    // its cause (a worker's stop: causeStopped)
	pane         port.Pane // the pane the fact names
}

// judge is the watchdogs' decision for task (docs/state-model.md rows 10–15), from the observed panes, the task's
// facts and ev, the evidence sampled by pane. Its lane's panes running no agent after one ran for the task: the pane
// exited, or the agent did and its shell stays (after Gone: a restart takes a moment). Working: activity, the stall
// ends. Blocked for Idle since the agent was first seen blocked for the task: the owner is needed. Ready: sample.
func (a *App) judge(p *proj, task string, w *watch, now time.Time, ev map[string]*evidence) verdict {
	at, panes := a.agentOf(p, w.lane)
	if at.State == Ambiguous { // two panes claim the lane's agent: the owner says which, after Gone
		v := verdict{step: event.StepNeedsYou, pane: w.agent, reason: "agent " + notIdentified(cmp.Or(w.lane.Agent, "the lane's agent"), at)}
		if now.Sub(w.lost) < a.opt.Gone || p.has(task, event.StepNeedsYou, v.reason) {
			return verdict{}
		}
		return v
	}
	if at.State != Identified {
		v := verdict{pane: w.agent, reason: "pane exited"}
		if panes > 0 {
			v.reason = "agent exited"
			if now.Sub(w.lost) < a.opt.Gone {
				return verdict{}
			}
		}
		if w.agent.ID == "" || p.has(task, event.StepNeedsYou, v.reason) {
			return verdict{}
		}
		v.step = event.StepNeedsYou
		return v
	}
	o := at.Pane
	v := verdict{pane: o}
	switch {
	case o.Status == "working":
		if p.watched(task) && !p.stops(task) { // a worker's question stays: working does not end it (T-511); nor does
			// it end a stop: only the next turn's hook does (turnCall, T-540)
			v.step = event.StepResumed
		}
	case o.Status == "blocked":
		if !p.has(task, event.StepNeedsYou, "blocked") && now.Sub(w.blocked) >= a.opt.Idle {
			v.step, v.reason = event.StepNeedsYou, "blocked"
		}
	case Ready(o.Status):
		if e := ev[o.ID]; e != nil {
			a.sample(p, task, w, now, e, &v)
		}
	}
	return v
}

// reasonStalled is the reason of the watchdog's needs-you on a stalled worker (needs-you{stalled}; obor resume ends it,
// T-746).
const reasonStalled = "stalled"

// sample is the stall timer (rows 10, 12, 14), a state machine over observed quiet time. Its state per open task is
// quietSince (zero: no baseline) and seenWrite, the newest transcript write accounted for (zero: not sampled yet; the
// first sample only sets it, to the transcript's write or, none known, its own time, so an old transcript is never
// activity), with the stalled{idle} and needs-you{stalled} facts in force. A ready agent is sampled at every check;
// a sample is
//   - active: a child runs (Busy true), or the transcript was written after seenWrite (then seenWrite = that write),
//     even when Busy failed: a fresh write is evidence;
//   - quiet: no child runs, no newer transcript;
//   - unknown: Busy failed, no newer transcript.
//
// Reset events take no sample and drop the baseline and seenWrite (plan, setLanes, load): the status became working
// or blocked, the agent or its pane changed, the task changed, a replay.
//
//	active                                       → quietSince = 0; stalled or escalated in force: fact resumed (asks
//	                                             stays: obor resume ends it, T-511)
//	unknown, or a reset event                    → quietSince = 0 (unknown keeps seenWrite)
//	quiet, no baseline                           → quietSince = now (no fact)
//	quiet, asks in force                         → nothing (the worker waits on its question, row 16a)
//	quiet, not stalled, now − quietSince ≥ Idle  → fact stalled, quietSince = now
//	quiet, stalled, not escalated, ≥ Idle        → fact needs-you{stalled}
//	otherwise                                    → nothing
//
// So a timer counts only time between quiet samples: the first quiet one is a baseline, never a verdict, and status
// age, the task's start or a pane's joining only reset it, never count.
func (a *App) sample(p *proj, task string, w *watch, now time.Time, e *evidence, v *verdict) {
	stalled := p.has(task, event.StepStalled, "idle")
	escalated := p.has(task, event.StepNeedsYou, reasonStalled)
	asked := p.asks(task) || p.stops(task) // a stopped worker waits on its API error, not stalled (T-532)
	active, due := w.sample(now, e, a.opt.Idle)
	switch {
	case active:
		if (stalled || escalated) && !p.stops(task) { // a stop ends only with the next turn's hook (turnCall)
			v.step = event.StepResumed
		}
	case !due, asked: // a worker waiting on its question is not stalled: the owner is told already
	case !stalled:
		v.step, v.reason = event.StepStalled, "idle"
	case !escalated:
		v.step, v.reason = event.StepNeedsYou, reasonStalled
	}
}

// sample is one sample e at now of q's quiet time: active (the baseline drops), else due when it has been quiet for
// idle since the baseline. The first quiet sample only sets the baseline; an unknown one drops it.
func (q *quietTimer) sample(now time.Time, e *evidence, idle time.Duration) (active, due bool) {
	wrote := e.known && !q.seenWrite.IsZero() && e.last.After(q.seenWrite)
	switch {
	case wrote, e.known && q.seenWrite.IsZero():
		q.seenWrite = e.last
	case q.seenWrite.IsZero():
		q.seenWrite = now // no transcript known yet: a later one is a write
	}
	switch {
	case e.busy || wrote: // active
		q.quietSince = time.Time{}
		return true, false
	case e.err != nil: // unknown is never idle
		q.quietSince = time.Time{}
	case q.quietSince.IsZero():
		q.quietSince = now
	default:
		return false, now.Sub(q.quietSince) >= idle
	}
	return false, false
}

// evidence is what herdr says of an agent's activity: busy (a child runs), quiet (none runs, and its transcript, when
// known, is old) or unknown (the process check failed).
type evidence struct {
	busy  bool
	err   error     // Busy failed
	last  time.Time // the transcript's last write
	known bool      // last is known
	// turn is the last turn the transcript records, of a kind whose turns are read (turned: it is known; T-563)
	turn   agents.Turn
	turned bool
}

// evidence asks herdr about pane's activity (outside mu).
func (a *App) evidence(ctx context.Context, pane string) *evidence {
	e := &evidence{}
	e.busy, e.err = a.panes.Busy(ctx, pane)
	if e.err != nil {
		e.busy = false
		a.logf("process info of %s: %v (activity unknown)", pane, e.err)
	}
	e.last, e.known = a.panes.Activity(ctx, pane)
	return e
}

// fact records v's step for task and folds it in (false: the log failed). A needs-you's line and toast go with the
// next rule pass (rules.go, the needs rule).
func (a *App) fact(p *proj, task string, w *watch, v verdict) bool {
	data, _ := json.Marshal(event.FactData{Lane: w.lane.Name, Machine: Machine, Workspace: a.spaces[v.pane.Workspace],
		WorkspaceID: v.pane.Workspace, Pane: v.pane.ID, Agent: a.shownAs(p, v.pane), Reason: v.reason,
		Cause: v.cause}) // strings: cannot fail
	now := a.opt.Now()
	r, err := p.log.Append(event.Record{Task: task, Step: v.step, Kind: event.KindFact, Data: data}, now)
	if err != nil {
		a.logf("FAIL event log of %s: %v", p.name, err)
		return false
	}
	p.fold(r)
	a.logf("%s %s %s (%s, pane %s)", p.name, task, r, w.lane.Name, v.pane.ID)
	return true
}
