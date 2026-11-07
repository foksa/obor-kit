// drive.go: the owner's drive (T-751): `obor drive` puts a task on a lane for the owner to iterate on by hand, in a
// plain interactive agent (role driver) on its own branch; `obor land` sends the branch through the same gate,
// integrator and merge as any task.
//
// A drive is a task on a lane, so the dispatch's machinery does the work. A new drive (obor drive <name>) is a new
// task id with the spec "Owner drive: <name>", recorded as fact dispatch{drive} on the first free untyped lane: the
// merge loop prepares the lane (lane.prepare), and the task is driving; the CLI then starts the drive agent in the
// owner's tab, once the lane is prepared: serve sends no job. A drive is new work only (owner 11-05, T-912): obor drive
// <T-xx> on a task that is not a drive is refused, never a take-over of a worker's task. While driving, serve sends
// the task no jobs, its watchdog does not watch it, and a merge watch has no line for it.
// obor land submits it (fact submitted{land}, the owner's summary as its report); from there it is an ordinary
// submitted task. A red gate, the integrator's sendback or hold go back to driving with one line to the lead
// (pickDriving), never to a worker; after the merge, agent.stop stops the drive agent and the lane is free.
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"obor/internal/config"
	"obor/internal/roles"
	"obor/internal/store"
)

// driveTitle is a drive's spec title: "Owner drive: <name>".
const driveTitle = "Owner drive: "

// The points a test hooks (App.hook) to interleave a resume, a task_done and a record deterministically.
const (
	hookTaskDoneWaits = "task_done.waits" // task_done, before it takes a.submitMu
	hookTaskDoneCheck = "task_done.check" // task_done holds a.submitMu, its checks passed, before its record
	hookResumeWaits   = "resume.waits"    // obor resume holds a.verdicts, before it takes a.submitMu
	hookResumeLocked  = "resume.locked"   // obor resume holds a.verdicts and a.submitMu, before its decision
	hookLandCheck     = "land.check"      // obor land holds a.submitMu, its checks passed, before its record
	hookAppended      = "record.appended" // a record or a verdict's result is in the log, before its fold (under mu)
	hookDropDecided   = "drop.decided"    // obor drop holds a.verdicts, the task marked ended, before its record
)

// hook runs the test's hook at point (none outside the tests).
func (a *App) hook(point string) {
	if f := a.hooked.Load(); f != nil {
		(*f)(point)
	}
}

// driveCall answers the socket's drive request (the owner or the lead): a call id is required and a repeated one gets
// the stored reply. Call.Task: that task, a drive already (driveAgain; any other task refused: a drive is new work
// only, T-912); else a new drive named Call.Note (newDrive). A task already driving (or the open drive of that name)
// whose drive agent is not running is started again (driveAgain: nothing recorded). The reply is DRIVE <task> → <lane>
// (<drive agent>, <type ref>): the CLI starts the drive agent; DRIVING …: <agent> runs already: it runs.
// It decides and records under a.verdicts.
func (a *App) driveCall(ctx context.Context, c wire.Call) (string, error) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	if c.CallID == "" || c.Task == "" && strings.TrimSpace(c.Note) == "" {
		return "", errors.New("drive: a call id and a name or a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(wire.MethodDrive, c.CallID)]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("drive: no served project at %s", c.Root)
	case stored:
		return reply, nil
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("drive: the owner or the lead drives, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	fx, err := a.loopOf(p)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	merging, pause := p.merging, p.paused(a.opt.Now())
	a.mu.Unlock()
	switch {
	case !merging:
		return "", mergeOff(p)
	case pause != nil:
		return "", pausedErr(Dash(c.Task), pause)
	case fx.Lane == nil || fx.Specs == nil || fx.Agents == nil:
		return "", fmt.Errorf("drive off for %s (no lane adapter)", p.name)
	}
	if c.Task != "" {
		return a.driveAgain(p, c.Task)
	}
	return a.newDrive(ctx, p, fx, c)
}

// driveReply is a drive's reply: DRIVE <task> → <lane> (<agent>, <type ref>).
func driveReply(task, lane, agent, ref string) string {
	return fmt.Sprintf("DRIVE %s → %s (%s, %s)", task, lane, agent, Dash(ref))
}

// newDrive records a new drive named c.Note (under a.verdicts): the next task id, its spec "Owner drive: <name>", fact
// dispatch{drive} on the first free lane in config order (not held for another task's dispatch), its model c.Types[0]
// (a model ref) or the hard level's first model. An open drive of that name is started again (driveAgain: its
// recorded lane and model, c.Types ignored). Refused (nothing recorded): the model is unknown or its effort not taken,
// no free lane (the lanes' states named; never a wait), the task branch has commits the target lacks.
func (a *App) newDrive(ctx context.Context, p *proj, fx *Loop, c wire.Call) (string, error) {
	name := strings.Join(strings.Fields(c.Note), " ")
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	if t := a.driveNamed(p, name); t != "" {
		return a.driveAgain(p, t)
	}
	ref := ""
	if len(c.Types) > 0 {
		ref = c.Types[0]
	} else if refs := knownModels(cfg, cfg.DifficultyList("hard")); len(refs) > 0 {
		ref = refs[0]
	}
	if ref == "" {
		return "", fmt.Errorf("drive %s: no model for level hard (obor config get difficulty): pass --model <model>", name)
	}
	if err := cfg.CheckModelRef(ref); err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	typ, effort := config.ParseModelRef(ref)
	if kind := unbridged(cfg, typ); kind != "" {
		return "", fmt.Errorf("drive %s: model %s's kind %s has no bridge (obor config validate)", name, typ, kind)
	}
	ls, err := a.lanes.Lanes(ctx, p.root)
	if err != nil {
		return "", fmt.Errorf("drive: lanes of %s: %v", p.name, err)
	}
	_, held := a.held(p, "")
	var l port.Lane
	for _, x := range ls {
		if freeFor(x, held, false) {
			l = x
			break
		}
	}
	if l.Name == "" {
		return "", fmt.Errorf("drive %s: no free lane (%s)", name, laneStates(ls, held))
	}
	task, err := a.nextTaskID(p, cfg)
	if err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	target, ahead, err := fx.Specs.Target(ctx, task)
	if err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	branch := cfg.TaskBranch(task)
	if ahead {
		return "", fmt.Errorf("drive %s: %s exists with commits not in the target (merge or delete it first)", name, branch)
	}
	if _, _, err := store.SetSpec(p.log.DB(), task, "# "+task+" — "+driveTitle+name+"\n", "", callerOf(c), a.opt.Now()); err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	a.mu.Lock()
	l.Dir = filepath.Clean(l.Dir)
	l.Agent = rowAgent(cfg, l.Name, l.Agent)
	names := cfg.LaneAgentAliases(l.Name, "")
	a.placement(p, l.Dir, append([]string{l.Agent}, names...)...)
	a.mu.Unlock()
	agent := roles.DriveAgent(laneAgent(l, cfg.AgentName("worker", l.Name)))
	d := dispatchData{Lane: l.Name, Dir: l.Dir, Agent: agent, Branch: branch, Target: target, Effort: effort, Type: ref,
		Drive: true, Caller: callerOf(c), Call: c.CallID, Reply: driveReply(task, l.Name, agent, ref)}
	if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("drive: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// driveAgain answers obor drive on task, a drive already (driving, or a new drive's dispatch; under a.verdicts):
// nothing recorded. Its drive agent running: DRIVING <task> → <lane>: <agent> runs already (the CLI starts nothing);
// else DRIVE <task> → <lane> (<agent>, <type ref>) on its recorded lane and type, and the CLI starts the agent again
// (RV-92 #2: a start that failed is retried by the same command). Refused, nothing recorded: a task that is not a drive
// (a worker's, whatever its state, or none open: T-912), a drive no longer driving (landed).
func (a *App) driveAgain(p *proj, task string) (string, error) {
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[task]; m != nil {
		mt = *m
	}
	w := a.whereIs(p, mt.agent)
	a.mu.Unlock()
	if !mt.drive {
		return "", fmt.Errorf("%s is not a drive; obor drive <name> starts one", task)
	}
	if st := mt.state; st != stDriving && st != stDispatched {
		return "", fmt.Errorf("drive %s: %s is %s, not driving", task, task, Dash(mt.shown()))
	}
	if w.State == Identified {
		return fmt.Sprintf("DRIVING %s → %s: %s runs already (pane %s)", task, mt.lane, mt.agent, w.Pane.ID), nil
	}
	ref := mt.disp.Type
	return driveReply(task, mt.lane, mt.agent, ref) + " again: its agent is not running", nil
}

// foldOldDrive folds fact drive, a past take-over of a worker's task (T-751, T-852): old logs only; goes with the log
// snapshot (owner 11-05; T-912: no record writes it now). It is the one reader of the record, called by foldRecord
// after foldLanes, and folds it as the take-over did: its call's stored reply; the task driving, the drive agent's, its
// needs-you ended, its lane, branch, spec and attempt kept, its worker's stop asked for (halt); the lane row's agent
// the drive agent.
func (p *proj) foldOldDrive(r event.Record) {
	if r.Kind != event.KindFact || r.Step != event.StepDrive {
		return
	}
	var d dispatchData
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodDrive, d.Call)] = d.Reply
	}
	if row, ok := p.folded.lr.rows[d.Lane]; ok && row.Task == r.Task {
		row.Agent = d.Agent
		p.folded.lr.rows[d.Lane] = row
	}
	mt := p.folded.loop[r.Task]
	if mt == nil || states[mt.state].ended {
		return
	}
	mt.unblock()
	mt.drive, mt.agent, mt.state = true, d.Agent, stDriving
	mt.attemptVars, mt.refused = attemptVars{}, ""
	if d.Stop != "" {
		mt.halt.ask(d.Stop, true)
	}
	mt.disp.Drive = true
	if d.Type != "" {
		mt.disp.Type = d.Type // a retry's start (driveAgain) takes the recorded type
	}
}

// pickDriving is a driving task's step (under mu): an old take-over's stop of the worker (pickStop; old logs only, goes
// with the log snapshot, as foldOldDrive), the lead's line once a red gate, a sendback or a hold sent it back (its why
// in line; message{drive: the attempt} marks it told), else nothing: the owner drives.
func (a *App) pickDriving(p *proj, mt mtask) step {
	if s := a.pickStop(p, mt); s != nil || mt.halt.pending() { // an old take-over's: the worker stopped first (T-852)
		return s
	}
	lead := p.out.lead()
	if mt.line == "" || mt.lined || lead == "" {
		return nil
	}
	return func(context.Context, *Loop) bool {
		m := event.Message{To: lead, From: "obor", Task: mt.task, Drive: mt.attempt,
			Line: "[obor] " + mt.task + " back to its owner's drive: " + strings.Join(strings.Fields(mt.line), " ")}
		return a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
	}
}

// foldDriveLine folds a lead line that tells a drive came back (message{drive}): told for that attempt.
func (p *proj) foldDriveLine(m event.Message) {
	if mt := p.folded.loop[m.Task]; mt != nil && m.Drive != 0 && mt.attempt == m.Drive {
		mt.lined = true
	}
}

// landReply is a land's reply.
func landReply(task string) string {
	return "LANDED " + task + ": submitted (gate, review and merge follow)"
}

// landCall answers the socket's land request (the owner, the lead or the drive agent): c.Task's drive submitted on its
// owner's behalf, c.Report (the summary) as its report: fact submitted{land}, then it is an ordinary submitted task. A
// repeated call id gets the stored reply. Refused: not driving, no summary, a step of it running. The CLI refused a
// dirty worktree before.
func (a *App) landCall(c wire.Call) (string, error) {
	a.submitMu.Lock() // one submission decision at a time (task_done's lock)
	defer a.submitMu.Unlock()
	report := strings.TrimSpace(c.Report)
	if c.CallID == "" || c.Task == "" {
		return "", errors.New("land: a call id and a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var mt mtask
	var busy string
	if p != nil {
		if reply, stored := p.folded.calls[verdictStep(wire.MethodLand, c.CallID)]; stored {
			a.mu.Unlock()
			return reply, nil
		}
		if m := p.folded.loop[c.Task]; m != nil {
			mt = *m
		}
		busy = p.busy(c.Task)
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("land: no served project at %s", c.Root)
	case c.Role != "" && c.Role != "lead" && c.Role != roles.Driver:
		return "", fmt.Errorf("land: the owner, the lead or the drive agent lands a drive, not %s (role %s)", Dash(c.Agent), c.Role)
	case !p.merging:
		return "", mergeOff(p)
	case mt.task == "":
		return "", fmt.Errorf("land: %s is not open in obor serve", c.Task)
	case !mt.may(vLand):
		return "", fmt.Errorf("land: %s is %s, not driving", c.Task, mt.shown())
	case report == "":
		return "", fmt.Errorf("land %s: a summary is required", c.Task)
	case busy != "":
		return "", busyErr("land", c.Task, busy)
	}
	a.hook(hookLandCheck)
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepSubmitted, Kind: event.KindFact},
		submitData{Lane: mt.lane, Agent: mt.agent, Call: c.CallID, Report: report, Land: true}) {
		return "", fmt.Errorf("land: the event log of %s refused it", p.name)
	}
	a.wake()
	if ok, state := a.receipt(p, c.Task, wire.MethodLand, c.CallID); !ok { // the fold ignored it: the task was dropped after the decision
		return "", fmt.Errorf("land: superseded: %s is %s", c.Task, state)
	}
	return landReply(c.Task), nil
}

// driveNamed is the open drive named name ("": none; under neither lock: it takes mu).
func (a *App) driveNamed(p *proj, name string) string {
	a.mu.Lock()
	var open []string
	for _, t := range p.loopTasks() {
		if m := p.folded.loop[t]; m.drive && !states[m.state].ended {
			open = append(open, t)
		}
	}
	a.mu.Unlock()
	for _, t := range open {
		if title, err := titleOf(p, t); err == nil && title == driveTitle+name {
			return t
		}
	}
	return ""
}

// taskNum is a task id's trailing number and what comes before it (T-751's "T-", 751).
var taskNum = regexp.MustCompile(`^(.*?)([0-9]+)$`)

// nextTaskID is the id after the highest one p knows (its stored specs, its loop's tasks): the same prefix, the number
// one higher ("T-1" when it knows none); an error when that id does not match task_id_pattern.
func (a *App) nextTaskID(p *proj, cfg *config.Config) (string, error) {
	rows, err := store.ReadSpecs(p.log.DB())
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Task)
	}
	a.mu.Lock()
	ids = append(ids, p.loopTasks()...)
	a.mu.Unlock()
	prefix, top := "T-", 0
	for _, id := range ids {
		m := taskNum.FindStringSubmatch(id)
		if m == nil || !cfg.TaskIDOK(id) {
			continue
		}
		if n, err := strconv.Atoi(m[2]); err == nil && n > top {
			prefix, top = m[1], n
		}
	}
	id := prefix + strconv.Itoa(top+1)
	if !cfg.TaskIDOK(id) {
		return "", fmt.Errorf("no next task id after %s%d (task_id_pattern %s)", prefix, top, cfg.Data.TaskIDPattern)
	}
	return id, nil
}

// laneStates is a drive refusal's list of the lanes and their states: "L-01 working T-3, L-02 free, …"; a lane held
// for another task's dispatch says so.
func laneStates(ls []port.Lane, held map[string]string) string {
	var out []string
	for _, l := range ls {
		s := l.Name + " " + Dash(l.State)
		if l.Task != "" {
			s += " " + l.Task
		} else if t := held[l.Name]; t != "" {
			s += " held for " + t
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return "no lanes"
	}
	return strings.Join(out, ", ")
}

// tellLine is the line obor tell-lead posts to the lead (T-761): the drive's task and the text, then the reply in one
// command.
func tellLine(task, text string) string {
	return "[obor " + task + " drive] " + strings.Join(strings.Fields(text), " ") + ` (reply: obor resume ` + task + ` "…")`
}

// tellLeadCall answers the socket's tell_lead request (T-761): the drive agent's text to its own project's lead, a lead
// line (fact message{driver}) posted to the lead's session as every lead line is. The caller is the drive agent (role
// driver) of an open drive on c.Lane, its lane; the lead's obor resume on that task comes back to it as a line. A call
// id is required and a repeated one gets the stored reply. Refused: any other caller (a worker, a reviewer, the lead,
// the owner), no open drive on the lane, a drive the lead cannot answer (T-764: driveAnswerable, the predicate obor
// resume uses: only while driving; else the line would advertise a reply that is refused), no text, no lead configured.
func (a *App) tellLeadCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New("tell-lead: a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodTellLead, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("tell-lead: no served project at %s", c.Root)
	case stored:
		return reply, nil
	case c.Role != roles.Driver:
		return "", fmt.Errorf("tell-lead: it is for a drive agent to tell its lead (obor tell-lead in an obor drive's pane), "+
			"not %s (role %s); the lead answers with obor resume, workers ask with a Blocked: report", Dash(c.Agent),
			Dash(c.Role))
	case strings.TrimSpace(c.Note) == "":
		return "", errors.New("tell-lead: a text is required")
	case !a.merges(p):
		return "", mergeOff(p)
	}
	a.mu.Lock()
	lead, task, why := p.out.lead(), "", ""
	for _, t := range p.loopTasks() {
		if m := p.folded.loop[t]; m.drive && m.lane == c.Lane && !states[m.state].ended {
			task, why = t, driveAnswerable(*m)
		}
	}
	a.mu.Unlock()
	switch {
	case task == "":
		return "", fmt.Errorf("tell-lead: no open drive in lane %s (obor status lists the drives)", Dash(c.Lane))
	case why != "":
		return "", fmt.Errorf("tell-lead: %s", why)
	case lead == "":
		return "", errors.New("tell-lead: no lead configured (the config's prefix names it)")
	}
	m := event.Message{To: lead, From: cmp.Or(c.Agent, roles.Driver), Task: task, Line: tellLine(task, c.Note),
		Driver: true, Call: c.CallID, Reply: "TOLD " + lead + " (" + task + "; its answer arrives here as an [obor answer] line)"}
	if !a.inboxed(lead) {
		m.Reply = "TOLD " + lead + " (" + task + "; queued: no session recorded for " + lead + ")"
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("tell-lead: the event log of %s refused it", p.name)
	}
	a.wake()
	return m.Reply, nil
}
