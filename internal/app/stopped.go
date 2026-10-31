package app

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"obor/internal/agents"
)

// A Claude agent whose turn ended on an API error (T-532; docs/state-model.md row 50): Claude Code fires StopFailure
// instead of Stop, and its hook (`obor hook stop-failure`) sends serve the agent, the error (rate_limit, overloaded,
// billing_error, … unknown: any value, no mapping) and its details. Serve records fact stopped{agent, error, details}:
//   - a role agent (the reviewer, the deep dive, the advisor, the integrator, …): the fact carries the lead's line
//     "[obor] <agent> stopped: <error> (<details>)" and ends the agent's open brief, so no finished line follows for it;
//   - a worker with an open task: the fact carries the task and the watchdog's needs-you{<agent> stopped: <error>
//     (<details>)} of it (cause stopped), folded from the fact itself, live and on replay (T-540: one record, no second
//     append to lose); its needs-you line "[obor] needs-you <T-xx> (<LANE>): …" to the lead as any other's.
//
// The stop ends with the agent's next turn. A role agent's next brief is a new one, its finished line as usual. A
// worker's needs-you ends (fact resumed, cause stopped) on evidence of a new turn: its next UserPromptSubmit or Stop
// hook sends serve a turn request (turnCall). Its pane's working status never ends it (judge): herdr's status and the
// time it was seen say nothing of which turn it is (T-540).
//
// A worker of a kind with no such hook whose transcript records its turns (agents.Caps.Turns: Codex, T-563) is read
// instead: idle with an open task, its transcript's last turn ended on an error (a usage limit) → the same fact
// stopped, its needs-you and lead line, with the turn (turnStop); once per turn, as its call id is the turn's. A later
// turn in the transcript (its start, read whatever the pane's status) ends it: fact resumed (cause stopped).
//
// A role agent's stop on a usage limit with its brief open (T-569) keeps the brief open: the stop is on it (Brief,
// Reset) until the agent's next turn (fact turned: its UserPromptSubmit or Stop hook's turn request, or a Codex
// rollout's later turn, roleTurns), and serve wakes it after the reset (stopwake.go). Its finished line comes after.
//
// A turn's start and its end are different evidence (T-571; RV-60 #1, #3). The evidence: a hook's turn request
// (UserPromptSubmit: the start; Stop: the end, Call.Ended), or a transcript's last turn (Codex: task_started the start;
// task_complete, turn_aborted the end, with its error; agents.Turn.Ended). What it does is decided in one place,
// turnDecide, from the state it meets: a stop in force; for a role agent's open brief, a turn followed (one started
// since the stop, its end not seen yet) and the brief's work seen (its pane working since its line):
//
//	stop  followed  evidence     work seen | act
//	yes   -         start        -         | resume: the stop ends, the brief stays open, its finish not armed
//	yes   -         end          -         | finish: the stop ends, the turn's work is done (its line when idle)
//	yes   -         end, error   -         | stop: a new stop (a usage limit: the brief stays open)
//	no    yes       start        -         | none: the turn followed still runs
//	no    yes       end          -         | finish
//	no    yes       end, error   -         | stop
//	no    no        start, end   -         | none: seen work arms the finish by itself
//	no    no        end, error   yes       | stop
//	no    no        end, error   no        | none: no turn of the brief
//
// A role agent's resume is fact turned{worked: false} (the brief follows the turn), its finish fact turned{worked:
// true}, its stop fact stopped; the folds read those facts, live and on replay alike. A worker has no brief: never a
// turn followed, its open task its work; resume and finish are both fact resumed (cause stopped), and a new stop met
// with one in force ends that one first (fact resumed), the next check recording the new one. A transcript's error end
// counts once (its turn's call id) and only with the pane idle.

const (
	MethodStopped = "stopped" // Call.Agent the agent, Call.Role its role, Call.Lane a worker's lane, Call.Error, Call.Details
	MethodTurn    = "turn"    // an agent's next turn began or ended (its UserPromptSubmit or Stop hook): Call.Agent, Call.Lane
	StepStopped   = "stopped" // fact: an agent's turn ended on an API error (data: stoppedData)
	StepTurned    = "turned"  // fact: a briefed role agent's stop ended with its next turn (data: turnedData)

	causeStopped = "stopped" // a watchdog needs-you's cause: the worker's turn ended on an API error
)

// stoppedData is fact stopped's data: the agent, the error and its details, and, for a role agent, the lead's line
// (Message: to the lead, the line, the brief it ends), with the call that recorded it and its reply. A worker's: its
// open task (Message.Task) and the needs-you of it the fact puts in force (NeedsYou, a watchdog fact's data). A role
// agent's usage limit with its brief open: the brief it stops (Brief, its seq), the transcript's turn (Turn, Codex)
// and the reset (Reset, Unix seconds; 0: unknown).
type stoppedData struct {
	Agent    string    `json:"agent"`
	Error    string    `json:"error"`
	Details  string    `json:"details,omitempty"`
	NeedsYou *FactData `json:"needs_you,omitempty"`
	Brief    int64     `json:"brief,omitempty"`
	Turn     string    `json:"turn,omitempty"`
	Reset    int64     `json:"reset,omitempty"`
	Message
}

// turnedData is fact turned's data: the role agent, the seq of its brief, and whether the evidence was the turn's end
// (Worked: the brief's work is done, its finished line follows when idle; false: the turn has only begun, the stop in
// force ended, the brief follows the turn until its end; turnDecide).
type turnedData struct {
	To     string `json:"to"`
	Brief  int64  `json:"brief"`
	Worked bool   `json:"worked,omitempty"`
}

// stoppedCall answers the socket's stopped request (the StopFailure hook of agent c.Agent). A call id is required and
// a repeated one gets the stored reply. The lead's own stop (and a session of no role) is recorded, no line.
func (a *App) stoppedCall(c Call) (string, error) {
	if c.CallID == "" || c.Agent == "" {
		return "", errors.New(MethodStopped + ": a call id and the agent are required")
	}
	p, reply, stored := a.callProj(MethodStopped, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodStopped, c.Root)
	case stored:
		return reply, nil
	}
	d, said := newStop(c.Agent, c.Error, c.Details, c.CallID)

	a.mu.Lock()
	task, w := "", (*watch)(nil)
	if c.Role == "worker" {
		task, w = p.stoppedTask(c)
	}
	role := c.Role != "worker" && c.Role != "lead" && c.Role != "none" && c.Role != "" && c.Agent != p.out.lead()
	if role {
		kind := ""
		if w := a.whereIs(p, c.Agent); w.State == Identified {
			kind = w.Pane.Agent
		}
		a.roleStop(p, &d, said, kind, time.Time{})
	}
	if w != nil {
		pane := w.agent
		if at, _ := a.agentOf(p, w.lane); at.State == Identified {
			pane = at.Pane
		}
		a.workerStop(p, &d, said, task, w, pane)
		d.NeedsYou.Limit, d.NeedsYou.Reset = limitOf(pane.Agent, d.Error, c.Details, time.Time{}, a.opt.Now())
	}
	a.mu.Unlock()

	if !a.record(p, Record{Task: MsgTask, Step: StepStopped, Kind: KindFact}, d) {
		return "", fmt.Errorf("%s: the event log of %s refused it", MethodStopped, p.name)
	}
	a.wake()
	return d.Reply, nil
}

// newStop is fact stopped's data for agent's error ("": unknown) and details, recorded by call id, and the stop as
// its lines say it: "<agent> stopped: <error> (<details>)".
func newStop(agent, errText, details, callID string) (stoppedData, string) {
	if errText == "" {
		errText = "unknown"
	}
	said := agent + " stopped: " + errText
	if details != "" {
		said += " (" + details + ")"
	}
	return stoppedData{Agent: agent, Error: errText, Details: details,
		Message: Message{From: "obor", Call: callID, Reply: "STOPPED " + agent + " (recorded)"}}, said
}

// workerStop makes d the stop of the worker with open task task (its watch w, its agent in pane): the task, and the
// needs-you of it that the fact puts in force (under mu).
func (a *App) workerStop(p *proj, d *stoppedData, said, task string, w *watch, pane Pane) {
	d.Task, d.Reply = task, "STOPPED "+d.Agent+" (needs-you "+task+")"
	d.NeedsYou = &FactData{Lane: w.lane.Name, Machine: Machine, Workspace: a.spaces[pane.Workspace],
		WorkspaceID: pane.Workspace, Pane: pane.ID, Agent: a.shownAs(p, pane), Reason: said, Cause: causeStopped}
}

// roleStop makes d the stop of role agent d.Agent (not the lead), its kind kind ("": the default), the reset its
// transcript read turn (zero: none): the lead's line; with a brief open, a usage limit stops the brief (Brief, Reset:
// it stays open, its agent woken after the reset), any other error ends it (no finished line for it). Under mu.
func (a *App) roleStop(p *proj, d *stoppedData, said, kind string, turn time.Time) {
	d.To, d.Line = p.out.lead(), "[obor] "+said
	d.Reply = "STOPPED " + d.Agent + " (the lead is told)"
	b := p.out.brief(d.Agent)
	if b == nil {
		return
	}
	limit, reset := limitOf(kind, d.Error, d.Details, turn, a.opt.Now())
	if !limit {
		d.Ends = d.Agent // the brief is over: this line, not a finished line
		return
	}
	d.Brief, d.Reset = b.seq, reset
}

// limitOf reads an agent's stop on errText (details), seen at at, by its agent's kind (agents.Kind.Reset): limit,
// it is a usage or rate limit; reset, when it resets as Unix seconds (the transcript's, turn, when it records one;
// 0: unknown).
func limitOf(kind, errText, details string, turn, at time.Time) (limit bool, reset int64) {
	k, ok := agents.Lookup(cmp.Or(kind, agents.Default))
	if !ok {
		return false, 0
	}
	r, limit := k.Reset(errText, details, at)
	if !turn.IsZero() {
		r = turn
	}
	if !limit || r.IsZero() {
		return limit, 0
	}
	return true, r.Unix()
}

// turnsRead: the agents of kind have their turns read from their transcripts (agents.Caps.Turns).
func turnsRead(kind string) bool {
	k, ok := agents.Lookup(kind)
	return ok && k.Caps().Turns
}

// turnAct is what a turn's evidence does (turnDecide).
type turnAct int

const (
	turnNone   turnAct = iota // nothing
	turnResume                // the stop ends; the turn has only begun
	turnFinish                // the stop, or the turn followed, ends with the turn's end: the work is done
	turnStop                  // a new stop
)

// turnCase is a turn's evidence and the state it meets (see above): a stop in force, a turn followed (a role agent's
// brief), the work seen (a worker: always); the evidence the turn's end (false: its start), on an error.
type turnCase struct {
	stopped, followed, worked bool
	ended, failed             bool
}

// turnDecide is the table above: what the evidence of c does.
func turnDecide(c turnCase) turnAct {
	switch {
	case !c.ended && c.stopped:
		return turnResume
	case !c.ended:
		return turnNone
	case c.failed && (c.stopped || c.followed || c.worked):
		return turnStop
	case !c.failed && (c.stopped || c.followed):
		return turnFinish
	}
	return turnNone
}

// counts: a transcript's turn t is evidence now (pane's status, done: its stop is recorded already): an error end only
// with the pane idle, once.
func counts(t agents.Turn, status string, done bool) bool {
	return t.Error == "" || (Ready(status) && !done)
}

// turnStop decides task's stop from its worker's transcript (ev, sampled by pane), for a kind whose turns are read
// (under mu; true: a fact was recorded, nothing else is decided for task in this check), by turnDecide:
//   - a stop in force read from the transcript, and a later turn there (another id), its start or its end → fact
//     resumed (cause stopped);
//   - none in force, its last turn ended on an error, the worker idle, no stop recorded for that turn → fact stopped
//     (call id "turn.<id>": a replay folds it into the calls, so the turn is never recorded twice).
//
// A stop the hook reported (no turn) is left to its hook (turnCall).
func (a *App) turnStop(p *proj, task string, w *watch, ev map[string]*evidence) bool {
	at, _ := a.agentOf(p, w.lane)
	if at.State != Identified {
		return false
	}
	o := at.Pane
	e := ev[o.ID]
	if e == nil || !e.turned {
		return false
	}
	f, stopped := p.stop(task)
	if stopped && (f.Fact().Turn == "" || f.Fact().Turn == e.turn.ID) {
		return false
	}
	callID := "turn." + e.turn.ID
	_, done := p.folded.calls[verdictStep(MethodStopped, callID)]
	if !counts(e.turn, o.Status, done) {
		return false
	}
	act := turnDecide(turnCase{stopped: stopped, worked: true, ended: e.turn.Ended, failed: e.turn.Error != ""})
	if act == turnNone {
		return false
	}
	if act != turnStop || stopped { // the stop in force ends (a new stop: the next check records it)
		return a.fact(p, task, w, verdict{step: StepResumed, cause: causeStopped, pane: o})
	}
	d, said := newStop(cmp.Or(w.lane.Agent, a.shownAs(p, o)), e.turn.Error, e.turn.Details, callID)
	a.workerStop(p, &d, said, task, w, o)
	d.NeedsYou.Turn = e.turn.ID
	d.NeedsYou.Limit, d.NeedsYou.Reset = limitOf(o.Agent, e.turn.Error, e.turn.Details, e.turn.Reset, a.opt.Now())
	data, _ := json.Marshal(d) // plain structs: cannot fail
	r, err := p.log.Append(Record{Task: MsgTask, Step: StepStopped, Kind: KindFact, Data: data}, a.opt.Now())
	if err != nil {
		a.logf("FAIL event log of %s: %v", p.name, err)
		return false
	}
	p.foldRecord(r)
	a.logf("%s %s %s (%s, turn %s)", p.name, task, r, w.lane.Name, e.turn.ID)
	return true
}

// foldStop puts in force the worker's needs-you a stopped fact r with data d carries (foldMsg): a fact of d's task
// with r's seq and time, unless the task has the same one in force.
func (p *proj) foldStop(r Record, d stoppedData) {
	if d.Task == "" || d.NeedsYou == nil || p.has(d.Task, StepNeedsYou, d.NeedsYou.Reason) {
		return
	}
	data, _ := json.Marshal(d.NeedsYou) // strings: cannot fail
	p.folded.active[d.Task] = append(p.folded.active[d.Task], Record{Seq: r.Seq, TS: r.TS, Task: d.Task, Step: StepNeedsYou,
		Kind: KindFact, Data: data})
}

// turnCall answers the socket's turn request (a worker's UserPromptSubmit or Stop hook): a new turn of agent c.Agent
// began or ended (c.Ended), so its task's stop, in force, ends (turnDecide): fact resumed (cause stopped). Nothing in
// force (the usual case), or no open task: nothing recorded. A repeat finds the stop ended: no call id needed.
func (a *App) turnCall(c Call) (string, error) {
	if c.Agent == "" {
		return "", errors.New(MethodTurn + ": the agent is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(c.Root)
	if p == nil {
		return "", fmt.Errorf("%s: no served project at %s", MethodTurn, c.Root)
	}
	if c.Role != "worker" {
		return a.roleTurn(p, c)
	}
	task, w := p.stoppedTask(c)
	if w == nil || turnDecide(turnCase{stopped: p.stops(task), worked: true, ended: c.Ended}) == turnNone {
		return "TURN " + c.Agent + " (no stop)", nil
	}
	v := verdict{step: StepResumed, cause: causeStopped, pane: w.agent}
	if at, _ := a.agentOf(p, w.lane); at.State == Identified {
		v.pane = at.Pane
	}
	if !a.fact(p, task, w, v) {
		return "", fmt.Errorf("%s: the event log of %s refused it", MethodTurn, p.name)
	}
	return "TURN " + c.Agent + " (the stop of " + task + " ended)", nil
}

// roleTurn is turnCall for role agent c.Agent (under mu, released while the fact is recorded), by turnDecide: its
// brief's stop in force ends at the turn's start (fact turned: the brief follows the turn, its finished line waits for
// the turn's end or its work), the turn's end finishes it (fact turned, worked: the finished line when idle), also when
// the start was missed (RV-60 #3). Neither: nothing.
func (a *App) roleTurn(p *proj, c Call) (string, error) {
	b := p.out.brief(c.Agent)
	if b == nil {
		return "TURN " + c.Agent + " (no stop)", nil
	}
	act := turnDecide(turnCase{stopped: b.stop != nil, followed: b.followed, worked: b.worked, ended: c.Ended})
	if act == turnNone {
		return "TURN " + c.Agent + " (no stop)", nil
	}
	reply := "TURN " + c.Agent + " (the stop of its brief ended)"
	if act == turnFinish {
		reply = "TURN " + c.Agent + " (its brief's turn ended)"
	}
	d := turnedData{To: c.Agent, Brief: b.seq, Worked: act == turnFinish}
	a.mu.Unlock()
	ok := a.record(p, Record{Task: MsgTask, Step: StepTurned, Kind: KindFact}, d)
	a.mu.Lock()
	if !ok {
		return "", fmt.Errorf("%s: the event log of %s refused it", MethodTurn, p.name)
	}
	return reply, nil
}

// stoppedTask is the open task of the worker c names (its lane, else the lane its agent works in) and its watch; ""
// and nil when it has none (under mu). The loop decides which task that is (workerTask): a watch is used only if it is
// that task's, and a watch of another task on its lane is stale (the lane's old task ended and this one was dispatched
// since the watchdog's last look, T-617), dropped as the next check would. A task dispatched since that look has no
// watch yet: one is installed as the check would (T-612: a worker may stop before that look). With no such loop task
// the watch on c's lane stands.
func (p *proj) stoppedTask(c Call) (string, *watch) {
	if task, lane := p.workerTask(c); task != "" {
		if w := p.tasks[task]; w != nil && w.lane.Name == lane {
			return task, w
		}
		for other, w := range p.tasks {
			if w.lane.Name == lane {
				delete(p.tasks, other)
			}
		}
		l := Lane{Name: lane}
		for _, row := range p.lanes {
			if row.Name == lane {
				l = row
				break
			}
		}
		l.Task = task
		w := &watch{lane: l}
		p.tasks[task] = w
		return task, w
	}
	for _, task := range p.openTasks() {
		w := p.tasks[task]
		if (c.Lane != "" && w.lane.Name == c.Lane) || (c.Lane == "" && w.lane.Agent == c.Agent) {
			return task, w
		}
	}
	return "", nil
}

// workerTask is the current task of the worker c names and its lane, as the loop holds it (under mu): the task whose
// attempt awaits its worker's task_done on c's lane, else on the lane its agent works in; "" when none does. Of two
// such tasks the newest dispatch (highest dispSeq) is the current one, as T-615 settles a lane's task.
func (p *proj) workerTask(c Call) (task, lane string) {
	var newest int64
	for _, t := range p.loopTasks() {
		mt := p.folded.loop[t]
		if !mt.awaitsTaskDone() || mt.lane == "" {
			continue
		}
		if (c.Lane != "" && mt.lane != c.Lane) || (c.Lane == "" && p.laneAgent(mt.lane) != c.Agent) {
			continue
		}
		if task == "" || mt.dispSeq > newest {
			task, lane, newest = t, mt.lane, mt.dispSeq
		}
	}
	return task, lane
}

// isStop: the fact f is a worker's stop (needs-you, cause stopped).
func isStop(f Record) bool {
	return f.Step == StepNeedsYou && f.Fact().Cause == causeStopped
}

// stops: task has a worker's stop in force.
func (p *proj) stops(task string) bool {
	_, ok := p.stop(task)
	return ok
}

// stop is task's worker's stop in force (ok false: none).
func (p *proj) stop(task string) (Record, bool) {
	for _, f := range p.folded.active[task] {
		if isStop(f) {
			return f, true
		}
	}
	return Record{}, false
}
