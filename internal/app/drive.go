// drive.go: the owner's drive (T-751): `obor drive` puts a task on a lane for the owner to iterate on by hand, in a
// plain interactive agent (role driver) on its own branch; `obor land` sends the branch through the same gate,
// integrator and merge as any task.
//
// A drive is a task on a lane, so the dispatch's machinery does the work. A new drive (obor drive <name>) is a new
// task id with the spec "Owner drive: <name>", recorded as fact dispatch{drive} on the first free untyped lane: the
// merge loop prepares the lane (lane.prepare), and the task is driving. A take-over (obor drive <T-xx>) of a task its
// worker works on is fact drive{lane, agent}: the task is driving in its lane, branch and spec, and serve stops the
// worker. In both the CLI then starts the drive agent in the owner's tab: serve sends no job. While
// driving, serve sends the task no jobs, its watchdog does not watch it, and a merge watch has no line for it.
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

// driveCall answers the socket's drive request (the owner or the lead): a call id is required and a repeated one gets
// the stored reply. Call.Task: a take-over of that task (takeOver); else a new drive named Call.Note (newDrive). A task
// already driving (or the open drive of that name) whose drive agent is not running is started again (driveAgain:
// nothing recorded). The reply is DRIVE <task> → <lane> (<drive agent>, <type ref>), a take-over's followed by
// " taking over from <worker>; <the stop's line>": the CLI starts the drive agent; DRIVING …: it runs already.
//
// A take-over stops the worker first, with force (it may be working), outside the locks, through serve's identity of
// it (stopAgent, as the post-merge stop: its pane by ID, never a label lookup, RV-93 #1), and records fact drive only
// then (RV-92 #4): no agent there is "<worker> not running" and the take-over records; a stop that fails, two
// panes claiming the worker, serve not connected to herdr, or a stop saying not found while serve still identifies the
// worker refuses the take-over and records nothing, the task
// stays its worker's and a retry is a new obor drive <T-xx>. Interrupted between the two: the task is working with no
// agent, the watchdog's stall path applies and a retry takes over. The record rechecks the task under a.submitMu,
// task_done's and land's lock (RV-92 #3): a task submitted meanwhile is refused, a task driving refuses task_done.
func (a *App) driveCall(ctx context.Context, c wire.Call) (string, error) {
	reply, worker, fx, err := a.driveRecord(ctx, c, "")
	if err != nil || worker == "" {
		return reply, err
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	a.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("drive: no served project at %s", c.Root)
	}
	line, gone, err := a.stopAgent(ctx, p, fx, worker, true, false)
	switch {
	case err != nil:
		return "", fmt.Errorf("drive %s: %s not stopped: %v (nothing recorded: %s stays %s's; obor drive %s retries)",
			c.Task, worker, err, c.Task, worker, c.Task)
	case gone:
		line = worker + " not running"
	}
	a.hook(hookDriveStopped)
	if reply, _, _, err = a.driveRecord(ctx, c, worker); err != nil {
		return "", err
	}
	return reply + "; " + line, nil
}

// The points a test hooks (App.hook) to interleave a take-over and a task_done deterministically.
const (
	hookDriveStopped  = "drive.stopped"   // a take-over stopped the worker, before its record's locks
	hookDriveLocked   = "drive.locked"    // a take-over holds a.submitMu, before its recheck
	hookTaskDoneCheck = "task_done.check" // task_done holds a.submitMu, its checks passed, before its record
)

// hook runs the test's hook at point (none outside the tests).
func (a *App) hook(point string) {
	if f := a.hooked.Load(); f != nil {
		(*f)(point)
	}
}

// driveRecord is driveCall's decision and record under a.verdicts: the reply, and for a take-over the worker to stop
// (with the loop's adapters). stopped "": the decision (a take-over only checked, its worker returned to stop); else
// the take-over's record after stopped's stop, under a.submitMu too (lock order a.verdicts, then a.submitMu).
func (a *App) driveRecord(ctx context.Context, c wire.Call, stopped string) (reply, worker string, fx *Loop, err error) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	if stopped != "" {
		a.submitMu.Lock()
		defer a.submitMu.Unlock()
		a.hook(hookDriveLocked)
	}
	if c.CallID == "" || c.Task == "" && strings.TrimSpace(c.Note) == "" {
		return "", "", nil, errors.New("drive: a call id and a name or a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var stored bool
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(wire.MethodDrive, c.CallID)]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", "", nil, fmt.Errorf("drive: no served project at %s", c.Root)
	case stored:
		return reply, "", nil, nil
	case c.Role != "" && c.Role != "lead":
		return "", "", nil, fmt.Errorf("drive: the owner or the lead drives, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	if fx, err = a.loopOf(p); err != nil {
		return "", "", nil, err
	}
	a.mu.Lock()
	merging, pause := p.merging, p.paused(a.opt.Now())
	a.mu.Unlock()
	switch {
	case !merging:
		return "", "", nil, mergeOff(p)
	case pause != nil:
		return "", "", nil, pausedErr(Dash(c.Task), pause)
	case fx.Lane == nil || fx.Specs == nil || fx.Agents == nil:
		return "", "", nil, fmt.Errorf("drive off for %s (no lane adapter)", p.name)
	}
	if c.Task != "" {
		reply, worker, err = a.takeOver(p, c, stopped)
		return reply, worker, fx, err
	}
	reply, err = a.newDrive(ctx, p, fx, c)
	return reply, "", fx, err
}

// driveReply is a drive's reply: DRIVE <task> → <lane> (<agent>, <type ref>).
func driveReply(task, lane, agent, ref string) string {
	return fmt.Sprintf("DRIVE %s → %s (%s, %s)", task, lane, agent, Dash(ref))
}

// newDrive records a new drive named c.Note (under a.verdicts): the next task id, its spec "Owner drive: <name>", fact
// dispatch{drive} on the first free untyped lane in config order (not held for another task's dispatch), its type
// c.Types[0] (a type reference) or the hard level's first type. An open drive of that name is started again
// (driveAgain: its recorded lane and type, c.Types ignored). Refused (nothing recorded): the type is unknown or its effort not taken, no free untyped lane (the lanes' states named; never a wait),
// the task branch has commits the target lacks. A drive counts toward no lane_types max.
func (a *App) newDrive(ctx context.Context, p *proj, fx *Loop, c wire.Call) (string, error) {
	name := strings.Join(strings.Fields(c.Note), " ")
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	if t := a.driveNamed(p, name); t != "" {
		return a.driveAgain(p, cfg, t)
	}
	ref := ""
	if len(c.Types) > 0 {
		ref = c.Types[0]
	} else if types, _ := cfg.DifficultyTypes("hard"); len(types) > 0 {
		ref = types[0]
	}
	if ref == "" {
		return "", fmt.Errorf("drive %s: no lane type for level hard (obor config get difficulty): pass --type <type>", name)
	}
	if err := cfg.CheckTypeRef(ref); err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	typ, effort := config.ParseTypeRef(ref)
	if kind := unbridged(cfg, typ); kind != "" {
		return "", fmt.Errorf("drive %s: type %s's kind %s has no bridge (obor config validate)", name, typ, kind)
	}
	ls, err := a.lanes.Lanes(p.root)
	if err != nil {
		return "", fmt.Errorf("drive: lanes of %s: %v", p.name, err)
	}
	_, held := a.held(p, "")
	var l port.Lane
	for _, x := range ls {
		if x.Type == "" && x.State == "free" && held[x.Name] == "" {
			l = x
			break
		}
	}
	if l.Name == "" {
		return "", fmt.Errorf("drive %s: no free untyped lane (%s)", name, laneStates(ls, held))
	}
	task, err := a.nextTaskID(p, cfg)
	if err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	target, ahead, err := fx.Specs.Target(ctx, task)
	if err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	branch := strings.ReplaceAll(cfg.Data.Branches.Task, "{id}", task)
	if ahead {
		return "", fmt.Errorf("drive %s: %s exists with commits not in the target (merge or delete it first)", name, branch)
	}
	if _, _, err := store.SetSpec(p.log.DB(), task, "# "+task+" — "+driveTitle+name+"\n", callerOf(c), a.opt.Now()); err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	a.mu.Lock()
	l.Dir = filepath.Clean(l.Dir)
	l.Agent = rowAgent(cfg, l.Name, l.Agent)
	names := cfg.LaneAgentAliases(l.Name, "")
	var pane port.Pane
	if o := a.placement(p, l.Dir, append([]string{l.Agent}, names...)...); o != nil {
		pane = o.pane
	}
	a.mu.Unlock()
	agent := roles.DriveAgent(laneAgent(l, pane, names))
	d := dispatchData{Lane: l.Name, Dir: l.Dir, Agent: agent, Branch: branch, Target: target, Effort: effort, Type: ref,
		Drive: true, Caller: callerOf(c), Call: c.CallID, Reply: driveReply(task, l.Name, agent, ref)}
	if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("drive: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// takeOver decides the owner's take-over of c.Task (under a.verdicts): fact drive{lane, agent: the drive agent} on a
// task its worker works on (working, or dispatched with its lane prepared, or a needs-you either left), its lane,
// branch, spec and type kept. stopped "": only checked, the worker to stop returned; else recorded (under a.submitMu
// too), the task checked again and its worker still stopped. A drive already (driving, or a new drive's dispatch) is
// started again (driveAgain). Refused: not open, gating, merging, under review or any other state of the merge loop
// (submitted meanwhile included); a step of it running; another worker than the one stopped.
func (a *App) takeOver(p *proj, c wire.Call, stopped string) (reply, worker string, err error) {
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", "", err
	}
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	running := p.held[c.Task]
	if p.ended[c.Task] {
		running = "a drop"
	}
	dir := p.laneDir(mt)
	a.mu.Unlock()
	st := mt.kept()
	switch {
	case mt.task == "":
		return "", "", fmt.Errorf("drive %s: %s is not open in obor serve", c.Task, c.Task)
	case mt.drive && (st == stDriving || st == stDispatched):
		reply, err = a.driveAgain(p, cfg, c.Task)
		return reply, "", err
	case st != stWorking && !(st == stDispatched && mt.base != ""):
		return "", "", fmt.Errorf("drive %s: %s is %s: obor drive takes over a task its worker works on, not one gating, "+
			"merging or under review", c.Task, c.Task, mt.state)
	case running != "":
		return "", "", fmt.Errorf("drive %s: %s is running; retry when it settles", c.Task, running)
	}
	worker = mt.agent
	if worker == "" {
		worker = cfg.AgentName("worker", mt.lane)
	}
	if stopped == "" {
		return "", worker, nil
	}
	if worker != stopped {
		return "", "", fmt.Errorf("drive %s: %s's worker is %s now, not %s as stopped: retry", c.Task, c.Task, worker, stopped)
	}
	agent := roles.DriveAgent(worker)
	ref := store.LaneTypeRef(mt.disp.Type, configType(cfg, mt.lane))
	d := dispatchData{Lane: mt.lane, Dir: dir, Agent: agent, Branch: mt.disp.Branch, Target: mt.disp.Target,
		Effort: mt.disp.Effort, Type: ref, Drive: true, Caller: callerOf(c), Call: c.CallID,
		Reply: driveReply(c.Task, mt.lane, agent, ref) + " taking over from " + worker}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepDrive, Kind: event.KindFact}, d) {
		return "", "", fmt.Errorf("drive: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, worker, nil
}

// driveAgain answers obor drive on task, a drive already (driving, or a new drive's dispatch; under a.verdicts):
// nothing recorded. Its drive agent running: DRIVING <task> → <lane>: <agent> runs already (the CLI starts nothing);
// else DRIVE <task> → <lane> (<agent>, <type ref>) on its recorded lane and type, and the CLI starts the agent again
// (RV-92 #2: a start that failed is retried by the same command).
func (a *App) driveAgain(p *proj, cfg *config.Config, task string) (string, error) {
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[task]; m != nil {
		mt = *m
	}
	w := a.whereIs(p, mt.agent)
	a.mu.Unlock()
	if st := mt.kept(); !mt.drive || st != stDriving && st != stDispatched {
		return "", fmt.Errorf("drive %s: %s is %s, not driving", task, task, Dash(mt.state))
	}
	if w.State == Identified {
		return fmt.Sprintf("DRIVING %s → %s: %s runs already (pane %s)", task, mt.lane, mt.agent, w.Pane.ID), nil
	}
	ref := store.LaneTypeRef(mt.disp.Type, configType(cfg, mt.lane))
	return driveReply(task, mt.lane, mt.agent, ref) + " again: its agent is not running", nil
}

// foldDrive folds fact drive (a take-over): the task is driving, the drive agent's, its needs-you ended; its lane,
// branch, spec and attempt stay.
func (p *proj) foldDrive(r event.Record) {
	mt := p.folded.loop[r.Task]
	if mt == nil || mt.state == stDropped || states[mt.state].ended {
		return
	}
	var d dispatchData
	_ = json.Unmarshal(r.Data, &d)
	mt.drive, mt.agent, mt.state, mt.needs, mt.left = true, d.Agent, stDriving, event.Record{}, ""
	mt.attemptVars = attemptVars{}
	mt.disp.Drive = true
	if d.Type != "" {
		mt.disp.Type = d.Type // a retry's start (driveAgain) takes the recorded type
	}
}

// pickDriving is a driving task's step (under mu): the lead's line once a red gate, a sendback or a hold sent it back
// (its why in line; message{drive: the attempt} marks it told), else nothing: the owner drives.
func (a *App) pickDriving(p *proj, mt mtask) step {
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
	var running string
	if p != nil {
		if reply, stored := p.folded.calls[verdictStep(wire.MethodLand, c.CallID)]; stored {
			a.mu.Unlock()
			return reply, nil
		}
		if m := p.folded.loop[c.Task]; m != nil {
			mt = *m
		}
		if running = p.held[c.Task]; p.ended[c.Task] {
			running = "a drop"
		}
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
	case mt.state != stDriving:
		return "", fmt.Errorf("land: %s is %s, not driving", c.Task, mt.state)
	case report == "":
		return "", fmt.Errorf("land %s: a summary is required", c.Task)
	case running != "":
		return "", fmt.Errorf("land %s: %s is running; retry when it settles", c.Task, running)
	}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepSubmitted, Kind: event.KindFact},
		submitData{Lane: mt.lane, Agent: mt.agent, Call: c.CallID, Report: report, Land: true}) {
		return "", fmt.Errorf("land: the event log of %s refused it", p.name)
	}
	a.wake()
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

// laneStates is a drive refusal's list of the lanes and their states: "O-01 (sonnet) working T-3, O-02 free, …"; a
// lane held for another task's dispatch says so.
func laneStates(ls []port.Lane, held map[string]string) string {
	var out []string
	for _, l := range ls {
		s := l.Name
		if l.Type != "" {
			s += " (" + l.Type + ")"
		}
		s += " " + Dash(l.State)
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
	return "[obor " + task + " drive] " + strings.Join(strings.Fields(text), " ") + ` (reply: obor answer ` + task + ` "…")`
}

// tellLeadCall answers the socket's tell_lead request (T-761): the drive agent's text to its own project's lead, a lead
// line (fact message{driver}) posted to the lead's session as every lead line is. The caller is the drive agent (role
// driver) of an open drive on c.Lane, its lane; the lead's obor answer on that task comes back to it as a line. A call
// id is required and a repeated one gets the stored reply. Refused: any other caller (a worker, a reviewer, the lead,
// the owner), no open drive on the lane, no text, no lead configured.
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
			"not %s (role %s); the lead answers with obor answer, workers ask with a Blocked: report", Dash(c.Agent),
			Dash(c.Role))
	case strings.TrimSpace(c.Note) == "":
		return "", errors.New("tell-lead: a text is required")
	case !a.merges(p):
		return "", mergeOff(p)
	}
	a.mu.Lock()
	lead, task := p.out.lead(), ""
	for _, t := range p.loopTasks() {
		if m := p.folded.loop[t]; m.drive && m.lane == c.Lane && !states[m.state].ended {
			task = t
		}
	}
	a.mu.Unlock()
	switch {
	case task == "":
		return "", fmt.Errorf("tell-lead: no open drive in lane %s (obor status lists the drives)", Dash(c.Lane))
	case lead == "":
		return "", errors.New("tell-lead: no lead configured (roles.lead.agent)")
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
