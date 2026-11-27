// drive.go: the owner's drive: `obor drive` puts a task on a lane for the owner to iterate on by hand, in a
// plain interactive agent (role driver) on its own branch; `obor land` sends the branch through the same gate,
// integrator and merge as any task.
//
// A drive is a task on a lane, so the dispatch's machinery does the work. A new drive (obor drive <name>) is a new
// task id with the spec "Owner drive: <name>", recorded as fact dispatch{drive} on the first free untyped lane: the
// merge loop prepares the lane (lane.prepare), and the task is driving; the CLI then starts the drive agent in the
// owner's tab, once the lane is prepared: serve sends no job. A drive is new work only: obor drive
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
	"obor/internal/version"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/roles"
	"obor/internal/store"
)

// driveTitle is a drive's spec title: "Owner drive: <name>".
const driveTitle = "Owner drive: "

// The points a test hooks (App.hook) to interleave a resume, a task_done and a record deterministically.
const (
	hookTaskDoneWaits  = "task_done.waits" // task_done, before it takes a.submitMu
	hookTaskDoneCheck  = "task_done.check" // task_done holds a.submitMu, its checks passed, before its record
	hookResumeWaits    = "resume.waits"    // obor resume holds a.verdicts, before it takes a.submitMu
	hookResumeLocked   = "resume.locked"   // obor resume holds a.verdicts and a.submitMu, before its decision
	hookLandCheck      = "land.check"      // obor land holds a.submitMu, its checks passed, before its record
	hookDoBegin        = "do.begin"        // a do's first section ended (its append and fold), before its effect or its stored result returns
	hookAppended       = "record.appended" // a record other than an intent is in the log, before its fold (under mu; App.appended)
	hookDropDecided    = "drop.decided"    // obor drop holds a.verdicts, the task marked ended, before its record
	hookPruneSelected  = "prune.selected"  // the nightly prune chose its tasks (mu released), before its check and deletes
	hookDispatchPicked = "dispatch.picked" // a dispatch holds a.verdicts, its lane picked and its refusals passed, before its record
	hookLaneAddRead    = "laneadd.read"    // serve's lane add holds a.verdicts, its fresh lane read done, before it starts an add
)

// hook runs the test's hook at point (none outside the tests).
func (a *App) hook(point string) {
	if f := a.hooked.Load(); f != nil {
		(*f)(point)
	}
}

// driveCall answers the socket's drive request (the owner or the lead): a call id is required and a repeated one gets
// the stored reply. DriveReq.Task: that task, a drive already (driveAgain; any other task refused: a drive is new work
// only); else a new drive named DriveReq.Name (newDrive). A task already driving (or the open drive of that
// name) whose drive agent is not running is started again (driveAgain: nothing recorded). The reply is the JSON of a
// wire.DriveReply: the CLI starts the drive agent (Running: it runs already). It decides and records under a.verdicts,
// then (the lock released) waits for a new drive's lane to be prepared, so the reply says it (Prepared).
func (a *App) driveCall(ctx context.Context, c wire.DriveReq) (string, error) {
	// A caller of another obor cannot read the reply (its text and this JSON differ across builds): refused before
	// anything is recorded.
	if c.Version != version.Version {
		return "", fmt.Errorf("drive: serve runs %s, this obor %s: serve restarts on the new binary once nothing runs; obor drive again then",
			version.Version, cmp.Or(c.Version, "older"))
	}
	reply, err := a.driveDecide(ctx, c)
	if err != nil {
		return "", err
	}
	var r wire.DriveReply
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		return "", fmt.Errorf("drive: reply %q: %v", reply, err)
	}
	if !r.Running {
		a.mu.Lock()
		p := a.projAt(c.Root)
		a.mu.Unlock()
		r.Prepared = p != nil && a.laneReady(ctx, p, r.Task)
	}
	return driveJSON(r)
}

// laneReady waits up to Options.DriveWait (a minute) for task's drive to be prepared (fold: driving), reporting whether
// it is: it waits on the log's change signal, re-checking the fold after each record, with one timer for the bound.
func (a *App) laneReady(ctx context.Context, p *proj, task string) bool {
	wait := cmp.Or(a.opt.DriveWait, time.Minute)
	deadline := a.opt.Now().Add(wait)
	bound := time.NewTimer(wait)
	defer bound.Stop()
	for {
		changed := p.log.Changed() // before the read: a record written after it closes this one
		a.mu.Lock()
		m := p.folded.loop[task]
		ready := m != nil && m.is(stDriving)
		a.mu.Unlock()
		if ready {
			return true
		}
		if !a.opt.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-changed:
		case <-bound.C:
			return false
		}
	}
}

// driveJSON is r as drive's reply.
func driveJSON(r wire.DriveReply) (string, error) {
	b, err := json.Marshal(r)
	return string(b), err
}

// driveDecide is driveCall's decision and record, under a.verdicts.
func (a *App) driveDecide(ctx context.Context, c wire.DriveReq) (string, error) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	if c.CallID == "" || c.Task == "" && strings.TrimSpace(c.Name) == "" {
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
	refused := leadOrOwner(wire.MethodDrive, "drives", c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("drive: no served project at %s", c.Root)
	case stored:
		return reply, nil
	case refused != nil:
		return "", refused
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
	case fx.Workflow == nil || fx.Specs == nil || fx.Agents == nil:
		return "", fmt.Errorf("drive off for %s (no lane adapter)", p.name)
	}
	if c.Task != "" {
		return a.driveAgain(p, c.Task)
	}
	return a.newDrive(ctx, p, fx, c)
}

// driveReply is a drive's reply as recorded: its JSON, not yet Prepared (driveCall settles that at each answer).
func driveReply(task, lane, agent, ref string) string {
	s, _ := driveJSON(wire.DriveReply{Task: task, Lane: lane, Agent: agent, Type: ref})
	return s
}

// newDrive records a new drive named c.Name (under a.verdicts): the next task id, its spec "Owner drive: <name>", fact
// dispatch{drive} on the first free lane in config order (not held for another task's dispatch), its model c.Types[0]
// (a model ref) or the hard level's first model. An open drive of that name is started again (driveAgain: its
// recorded lane and model, c.Types ignored). Refused (nothing recorded): the model is unknown or its effort not taken,
// no free lane (the lanes' states named; never a wait), the task branch has commits the target lacks.
func (a *App) newDrive(ctx context.Context, p *proj, fx *Loop, c wire.DriveReq) (string, error) {
	name := strings.Join(strings.Fields(c.Name), " ")
	cfg := fx.Cfg
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
		return "", fmt.Errorf("drive %s: no model for level hard (obor config get .agents.difficulty): pass --model <model>", name)
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
	a.mu.Lock()
	p.maskAdding(ls) // as dispatchTask's: a lane serve is adding is no drive's
	a.mu.Unlock()
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
	target, ahead, err := fx.Workflow.Target(ctx, task)
	if err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	branch := cfg.TaskBranch(task)
	if ahead {
		return "", fmt.Errorf("drive %s: %s exists with commits not in the target (merge or delete it first)", name, branch)
	}
	if _, _, err := store.SetSpec(p.log.DB(), task, "# "+task+" — "+driveTitle+name+"\n", "", callerOf(c.Envelope), a.opt.Now()); err != nil {
		return "", fmt.Errorf("drive %s: %v", name, err)
	}
	l.Dir = filepath.Clean(l.Dir)
	agent := roles.DriveAgent(cfg.AgentName("worker", l.Name))
	d := event.Dispatch{Lane: l.Name, Dir: l.Dir, Agent: agent, Branch: branch, Target: target, Effort: effort, Type: ref,
		Drive: true, Caller: callerOf(c.Envelope), Call: c.CallID, Reply: driveReply(task, l.Name, agent, ref)}
	if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("drive: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// driveAgain answers obor drive on task, a drive already (driving, or a new drive's dispatch; under a.verdicts):
// nothing recorded. Its drive agent running: Running, its pane (the CLI starts nothing);
// else the reply with Again on its recorded lane and type, and the CLI starts the agent again
// (a start that failed is retried by the same command). Refused, nothing recorded: a task that is not a drive
// (a worker's, whatever its state, or none open), a drive no longer driving (landed).
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
		return driveJSON(wire.DriveReply{Task: task, Lane: mt.lane, Agent: mt.agent, Running: true, Pane: w.Pane.ID})
	}
	return driveJSON(wire.DriveReply{Task: task, Lane: mt.lane, Agent: mt.agent, Type: mt.disp.Type, Again: true})
}

// pickDriving is a driving task's step (under mu): the lead's line once a red gate, a sendback or a hold sent it back
// (its why in line; message{drive: the attempt} marks it told), else nothing: the owner drives.
func (a *App) pickDriving(p *proj, mt mtask) step {
	lead := p.out.Lead()
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
func (a *App) landCall(c wire.LandReq) (string, error) {
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
		event.Submit{Lane: mt.lane, Agent: mt.agent, Call: c.CallID, Report: report, Land: true}) {
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

// taskNum matches a task id's trailing number and what comes before it ("T-7": "T-", 7).
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
		return "", fmt.Errorf("no next task id after %s%d (task_id_pattern %s)", prefix, top, cfg.TaskIDPattern())
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

// tellLine is the line obor tell-lead posts to the lead: the drive's task and the text, then the reply in one
// command.
func tellLine(task, text string) string {
	return "[obor " + task + " drive] " + strings.Join(strings.Fields(text), " ") + ` (reply: obor resume ` + task + ` "…")`
}

// tellLeadCall answers the socket's tell_lead request: the drive agent's text to its own project's lead, a lead
// line (fact message{driver}) posted to the lead's session as every lead line is. The caller is the drive agent (role
// driver) of an open drive on c.Lane, its lane; the lead's obor resume on that task comes back to it as a line. A call
// id is required and a repeated one gets the stored reply. Refused: any other caller (a worker, a reviewer, the lead,
// the owner), no open drive on the lane, a drive the lead cannot answer (driveAnswerable, the predicate obor
// resume uses: only while driving; else the line would advertise a reply that is refused), no text, no lead configured.
func (a *App) tellLeadCall(c wire.TellLeadReq) (string, error) {
	if c.CallID == "" {
		return "", errors.New("tell-lead: a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodTellLead, c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("tell-lead: no served project at %s", c.Root)
	case stored:
		return reply, nil
	case c.Role != roles.Driver:
		return "", fmt.Errorf("tell-lead: it is for a drive agent to tell its lead (obor tell-lead in an obor drive's pane), "+
			"not %s (role %s); the lead answers with obor resume, workers ask with a Blocked: report", Dash(c.Agent),
			Dash(c.Role))
	case strings.TrimSpace(c.Text) == "":
		return "", errors.New("tell-lead: a text is required")
	case !a.merges(p):
		return "", mergeOff(p)
	}
	a.mu.Lock()
	lead, task, why := p.out.Lead(), "", ""
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
	m := event.Message{To: lead, From: cmp.Or(c.Agent, roles.Driver), Task: task, Line: tellLine(task, c.Text),
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
