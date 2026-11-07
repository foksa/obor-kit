package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"

	"obor/internal/config"
)

// The integrator's verdicts over the app socket (docs/state-model.md rows 26–31, rule 3): accept, sendback, reject
// and resolved, and hold (the job stays open, the owner decides); and the lead's or the owner's drop. They are
// validated and recorded here (obor resume: resume.go); the merge loop (loop.go) acts on them.

// The integrator's job steps (StepReview, StepResolve) are in internal/app/event; their data is JobData.

// JobData is a review or resolve prompt intent's data: the candidate's patch hash an accept must name.
type JobData struct {
	Patch string `json:"patch,omitempty"`
}

// verdictData is a verdict's intent data; verdictResult its result, the stored reply.
type verdictData struct {
	Job    string `json:"job"`
	Caller string `json:"caller"`
	Patch  string `json:"patch,omitempty"`
	Note   string `json:"note,omitempty"`
}

type verdictResult struct {
	Job   string `json:"job"`
	Reply string `json:"reply"`
	Patch string `json:"patch,omitempty"`
	Note  string `json:"note,omitempty"`
	SHA   string `json:"sha,omitempty"`  // resolved: the merge pinned at refs/obor/resolved/<task>, the gate's input
	Lead  bool   `json:"lead,omitempty"` // a sendback of the lead's or the owner's (leadSendback)
}

// verdictStep is the step of a verdict: <method>.<call id>.
func verdictStep(method, callID string) string { return method + "." + callID }

// verdictMethod is the method of a verdict step ("" when step is none).
func verdictMethod(step string) (method, callID string) {
	m, id, ok := strings.Cut(step, ".")
	if !ok || id == "" {
		return "", ""
	}
	switch m {
	case wire.MethodAccept, wire.MethodSendback, wire.MethodReject, wire.MethodResolved:
		return m, id
	}
	return "", ""
}

// foldJob folds r into p's answered calls: a verdict's result stores its reply under the call id; a hold (its
// needs-you) and a resume (its unblocked) store theirs. Which job is open is the merge loop's (mtask.openJob).
func (p *proj) foldJob(r event.Record) {
	switch {
	case r.Kind == event.KindFact && (r.Step == event.StepDispatched || r.Step == event.StepWaiting):
		var d dispatchData
		_ = json.Unmarshal(r.Data, &d)
		method := wire.MethodDispatch
		if d.Drive { // obor drive's (T-751)
			method = wire.MethodDrive
		}
		if d.Call != "" {
			p.folded.calls[verdictStep(method, d.Call)] = d.Reply
		}
	case r.Kind == event.KindFact && r.Step == event.StepSubmitted:
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		switch {
		case d.Call != "" && d.Land: // obor land's (T-751)
			p.folded.calls[verdictStep(wire.MethodLand, d.Call)] = landReply(r.Task)
		case d.Call != "":
			p.folded.calls[verdictStep(wire.MethodTaskDone, d.Call)] = taskDoneReply
		}
	case r.Kind == event.KindFact && r.Step == event.StepDelivered:
		p.folded.delivered[r.Fact().Job] = idleEnd{seq: r.Seq, at: event.RecordTime(r)}
	case r.Kind == event.KindFact && r.Step == event.StepSpecSeen: // a task_done refused for an older rev (T-821)
		if d := r.Fact(); d.Call != "" {
			p.folded.refusals[verdictStep(wire.MethodTaskDone, d.Call)] = d.Reason
		}
	case r.Kind == event.KindFact && r.Step == event.StepDropped:
		if d := r.Fact(); d.Call != "" {
			p.folded.calls[verdictStep(wire.MethodDrop, d.Call)] = dropReply(r.Task)
		}
	case r.Kind == event.KindFact && r.Step == event.StepDropping: // T-852: its DROPPING reply
		var d dropData
		_ = json.Unmarshal(r.Data, &d)
		if d.Call != "" {
			p.folded.calls[verdictStep(wire.MethodDrop, d.Call)] = d.Reply
		}
	case r.Kind == event.KindFact && (r.Step == event.StepNeedsYou || r.Step == event.StepUnblocked):
		if d := r.Fact(); d.Call != "" && r.Step == event.StepNeedsYou {
			p.folded.calls[verdictStep(wire.MethodHold, d.Call)] = holdReply(r.Task, d.Job)
		} else if d.Call != "" {
			p.folded.calls[verdictStep(wire.MethodResume, d.Call)] = resumeReply(r.Task, d.Reason, d.Cause)
		}
	case r.Kind == event.KindResult:
		method, id := verdictMethod(r.Step)
		if method == "" {
			return
		}
		var v verdictResult
		_ = json.Unmarshal(r.Data, &v)
		p.folded.calls[method+"."+id] = v.Reply
		p.folded.verdicts++ // a verdict came (rolestop.go, T-870)
	}
}

// verdict answers one verdict call: a repeated call id gets the stored reply; else the call is validated (the
// project's merge loop runs (an integrator role), the caller is the project's integrator, the job is the task's open one
// (mtask.openJob) and fits the method, an accept names its patch) and recorded, an intent and its result written and
// folded in one mu section (verdictRecord), and the merge loop woken. A refused call records nothing. A sendback past K sendbacks of the task is recorded and goes to the
// owner (row 22). A resolved first pins the merge the integrator committed in the scratch (refused when there is
// none): the verdict records its sha, the resolved gate's input.
func (a *App) verdict(ctx context.Context, method string, c wire.Call) (string, error) {
	a.mu.Lock()
	a.admitted++ // serve's restart on a new binary waits for it (follow.go)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.admitted--
		a.mu.Unlock()
	}()
	a.verdicts.Lock() // one verdict at a time: two calls never both answer one job
	defer a.verdicts.Unlock()
	if c.CallID == "" || c.Task == "" {
		return "", fmt.Errorf("%s: a call id and a task are required", method)
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	var key event.Key
	var patch string
	var open, dropped bool
	var jobSt string // the state of the task's job out: its verdicts
	var mismatch *port.Mismatch
	var input string // a resolve job's: the conflicting gate's input, the submitted sha (T-403)
	sendbacks := 0
	merging := false
	if p != nil {
		merging = p.merging
		reply, stored = p.folded.calls[verdictStep(method, c.CallID)]
		if mt := p.folded.loop[c.Task]; mt != nil {
			key, patch, open, sendbacks, input = mt.jobKey(), mt.patch, mt.openJob(), mt.sendbacks, mt.input
			jobSt, mismatch = mt.jobState(true), mt.mismatch
			dropped = mt.state == stDropped || mt.state == stDropping || p.ended[c.Task]
		}
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", method, c.Root)
	case stored:
		return reply, nil
	case !merging:
		return "", mergeOff(p)
	case c.Role != "integrator" || c.Agent == "" || c.Agent != p.integrator:
		return "", fmt.Errorf("%s: only the integrator (%s) gives verdicts, not %s (role %s)", method,
			p.integrator, Dash(c.Agent), Dash(c.Role))
	case dropped:
		return "", fmt.Errorf("%s: superseded: %s was dropped", method, c.Task)
	case !open:
		return "", fmt.Errorf("%s: no job open for %s", method, c.Task)
	case c.Job != "" && c.Job != JobID(key):
		return "", fmt.Errorf("%s: stale job %s (%s's job is %s)", method, c.Job, c.Task, JobID(key))
	}
	jobStep, _ := event.StepN(key.Step)
	switch {
	case method != wire.MethodHold && !takes(jobSt, method):
		return "", fmt.Errorf("%s: %s is a %s job (%s)", method, JobID(key), jobStep, accepts(jobSt))
	case method == wire.MethodAccept && c.Patch != patch:
		return "", fmt.Errorf("stale: re-read the candidate (%s's patch is %s, not %s)", c.Task, patch, Dash(c.Patch))
	case (method == wire.MethodSendback || method == wire.MethodReject || method == wire.MethodHold) && strings.TrimSpace(c.Note) == "":
		return "", fmt.Errorf("%s: say why", method)
	case method == wire.MethodHold:
		return a.hold(p, c, key)
	}
	var sha string
	if method == wire.MethodResolved {
		fx, err := a.loopOf(p)
		if err == nil {
			sha, err = a.resolvedCheck(ctx, p, fx, c.Task, input, mismatch)
		}
		if err != nil {
			return "", fmt.Errorf("%s: %v", method, err)
		}
	}
	in := event.Intent{Key: event.Key{Task: c.Task, Attempt: key.Attempt, Step: verdictStep(method, c.CallID)}, Class: event.ClassFact,
		Data: verdictData{Job: JobID(key), Caller: c.Agent, Patch: c.Patch, Note: c.Note}}
	reply = fmt.Sprintf("%s %s (job %s)", strings.ToUpper(method), c.Task, JobID(key))
	if method == wire.MethodSendback && sendbacks >= K {
		reply += fmt.Sprintf(": sent back %d times already, the owner decides", K)
	}
	a.mu.Lock()
	r, err := a.verdictRecord(p, in, verdictResult{Job: JobID(key), Reply: reply, Patch: c.Patch, Note: c.Note, SHA: sha})
	if err != nil {
		a.mu.Unlock()
		return "", err
	}
	var v verdictResult
	_ = json.Unmarshal(r.Data, &v)
	was := p.closed(c.Task)
	p.foldRecord(r) // its time as the log wrote it: a sendback's, the next attempt's begun
	if !was && p.closed(c.Task) {
		a.settleEnded(p, c.Task) // rejected: its cleared watchdog notifications never run (T-636)
	}
	a.mu.Unlock()
	a.logf("%s %s by %s", p.name, v.Reply, c.Agent)
	a.wake()
	return v.Reply, nil
}

// verdictRecord writes the verdict in and its result v (under mu: a verdict's effect is only its reply, so the
// intent, the result and their folds are one section, T-944): the intent folded once written, the result (the
// stored one when in has one) returned for the caller to fold in the same section.
func (a *App) verdictRecord(p *proj, in event.Intent, v verdictResult) (event.Record, error) {
	r, done, wrote, err := p.log.Begin(in, a.opt.Now())
	if err != nil || done {
		return r, err
	}
	if wrote {
		p.foldRecord(r)
	}
	if r, err = p.log.Finish(in.Key, v, true, a.opt.Now()); err != nil {
		return r, err
	}
	a.hook(hookAppended)
	return r, nil
}

// resolvedCheck is Scratch.Resolved holding p's scratch: never while a gate or a publish runs there.
func (a *App) resolvedCheck(ctx context.Context, p *proj, fx *Loop, task, input string, m *port.Mismatch) (string, error) {
	select {
	case p.scratch <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-p.scratch }()
	return fx.Scratch.Resolved(ctx, task, input, m)
}

// hold records the integrator's hold of job j (under a.verdicts): one fact, needs-you{held: why}, which also stores
// the reply under the call id. The job stays open: the integrator's verdict on it ends the hold, as obor resume does.
func (a *App) hold(p *proj, c wire.Call, job event.Key) (string, error) {
	a.mu.Lock()
	var lane string
	held, drive := false, false
	if mt := p.folded.loop[c.Task]; mt != nil {
		lane, held, drive = mt.lane, mt.needs.held() && mt.needs.fact.Fact().Cause == event.CauseHeld, mt.drive
	}
	a.mu.Unlock()
	if drive { // a drive's hold goes back to its owner (T-751): the job ends, the lead gets a line, no needs-you
		if !a.record(p, event.Record{Task: c.Task, Attempt: job.Attempt, Step: event.StepSendback, Kind: event.KindFact},
			noteData{Note: "held: " + c.Note}) {
			return "", fmt.Errorf("hold: the event log of %s refused it", p.name)
		}
		a.wake()
		return fmt.Sprintf("HOLD %s (job %s): back to its owner's drive", c.Task, JobID(job)), nil
	}
	if held {
		return "", fmt.Errorf("hold: %s is held already (the owner is told)", c.Task)
	}
	if !a.record(p, event.Record{Task: c.Task, Attempt: job.Attempt, Step: event.StepNeedsYou, Kind: event.KindFact},
		event.FactData{Lane: lane, Machine: Machine, Reason: "held: " + c.Note, Merge: true, Cause: event.CauseHeld, Job: JobID(job),
			Call: c.CallID}) {
		return "", fmt.Errorf("hold: the event log of %s refused it", p.name)
	}
	a.wake()
	return holdReply(c.Task, JobID(job)), nil
}

func holdReply(task, job string) string {
	return fmt.Sprintf("HOLD %s (job %s): the owner is told", task, job)
}

func dropReply(task string) string { return "DROPPED " + task }

// drop answers the lead's or the owner's drop of task c.Task (obor drop <T-xx>; c.Note: why) under a.verdicts: a
// repeated call id gets the stored reply; else dropDecide's refusals, and the task is marked ended (p.ended) in the
// same mu section a selected step's start check takes (begin), so no step starts after it. A task whose lane row still
// holds it (serve knows the lane: a waiting task holds none) in a loop with the lane adapters is dropping (T-852): one
// fact, dropping{lane, worker, why, force, discard}, answered DROPPING at once; the merge loop then stops its worker,
// frees its lane and records dropped (stopstep.go). The worker is a refused stop's recorded agent (a move's old
// worker, with its force; T-855), else laneWorker's. Any other: one fact, dropped{why, by}: the task leaves the loop and
// its lane row, its needs-you and timers end. The mark ends with the record (a failed write: the drop is refused).
func (a *App) drop(c wire.Call) (string, error) {
	if c.CallID == "" || c.Task == "" {
		return "", errors.New("drop: a call id and a task are required")
	}
	a.verdicts.Lock() // one verdict at a time; never held across a wait: the stop and the free are the loop's
	defer a.verdicts.Unlock()
	a.mu.Lock()
	p := a.projAt(c.Root)
	mt, err := a.dropDecide(p, c)
	lane, stored := "", ""
	if err == nil && mt.task == "" { // the call id's stored reply: DROPPED, or a dropping fact's DROPPING
		stored = p.folded.calls[verdictStep(wire.MethodDrop, c.CallID)]
	}
	if mt.lane != "" && p.laneTask(mt.lane) == c.Task {
		lane = mt.lane
	}
	a.mu.Unlock()
	if err != nil || mt.task == "" {
		return stored, err
	}
	a.hook(hookDropDecided)
	defer func() {
		a.mu.Lock()
		delete(p.ended, c.Task) // folded: the task is dropping or dropped; else the drop is refused
		a.mu.Unlock()
	}()
	if lane != "" && !a.laneAdapters(p) { // no lane adapters (a loop observing): the task dropped, its lane as it was
		lane = ""
	}
	if lane == "" {
		if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Lane: mt.lane, Machine: Machine, Agent: c.Agent, Reason: c.Note, Merge: true, Call: c.CallID}) {
			return "", fmt.Errorf("drop: the event log of %s refused it", p.name)
		}
		a.wake()
		return dropReply(c.Task), nil
	}
	worker, err := a.laneWorker(p, lane, mt)
	if err != nil {
		return "", fmt.Errorf("drop: %v", err)
	}
	force := c.Force
	if h := mt.halt; h.pending() { // a refused stop (busy refuses a running one): its recorded agent, never the lane row's (T-855)
		worker, force = h.agent, force || h.force
	}
	d := dropData{Lane: lane, Worker: worker, Agent: c.Agent, Reason: c.Note, Force: force, Discard: c.Discard,
		Call: c.CallID}
	d.Reply = dropReplyOf(c.Task, d)
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepDropping, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("drop: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// laneAdapters: p's merge loop has the lane adapters a drop frees a lane with.
func (a *App) laneAdapters(p *proj) bool {
	fx, err := a.loopOf(p)
	return err == nil && fx.Lane != nil && fx.Specs != nil && fx.Agents != nil
}

// laneWorker is the agent a drop of mt stops in lane (T-773: that task's worker, nothing keeps it): the lane row's
// agent while it is still one of the lane's names, else the lane's derived one, else the
// task's ("": none known: nothing to stop).
func (a *App) laneWorker(p *proj, lane string, mt mtask) (string, error) {
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	agent := rowAgent(cfg, lane, p.laneAgent(lane))
	if row := rowAgent(cfg, lane, p.folded.lr.rows[lane].Agent); row != "" {
		agent = row
	}
	a.mu.Unlock()
	if agent == "" {
		agent = mt.agent
	}
	return agent, nil
}

// dropDecide is drop's decision (under mu): the task to drop, marked ended; mt.task "" with no error: the call id's
// reply is stored. Refused: the merge loop off, a caller of another role, no why, the task not open, its state takes no
// drop (mtask.may: merged, dropped), busy.
func (a *App) dropDecide(p *proj, c wire.Call) (mtask, error) {
	if p == nil {
		return mtask{}, fmt.Errorf("drop: no served project at %s", c.Root)
	}
	if _, stored := p.folded.calls[verdictStep(wire.MethodDrop, c.CallID)]; stored {
		return mtask{}, nil
	}
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	switch {
	case !p.merging:
		return mtask{}, mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return mtask{}, fmt.Errorf("drop: the lead or the owner drops a task, not %s (role %s)", Dash(c.Agent), c.Role)
	case strings.TrimSpace(c.Note) == "":
		return mtask{}, errors.New("drop: say why")
	case mt.task == "":
		return mtask{}, fmt.Errorf("drop: %s is not open in obor serve", c.Task)
	case !mt.may(vDrop):
		return mtask{}, fmt.Errorf("drop: %s is %s already", c.Task, mt.state)
	}
	if why := p.busy(c.Task); why != "" {
		return mtask{}, busyErr("drop", c.Task, why)
	}
	p.ended[c.Task] = true
	return mt, nil
}

// mergeOff refuses a verdict or resume for p, whose merge loop is off: it is observe-only (RV-38 #13).
func mergeOff(p *proj) error {
	return fmt.Errorf("merge loop off for %s (no integrator role)", p.name)
}

// loopOf is p's merge-loop adapters.
func (a *App) loopOf(p *proj) (*Loop, error) {
	if a.opt.Loop == nil {
		return nil, fmt.Errorf("%s has no merge loop", p.name)
	}
	return a.opt.Loop(p.root)
}

// Dash is s, "-" for "".
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// startIntegrator starts the integrator of the project at root through agents unless it is identified in the
// project's workspace (refused while two panes claim it) (docs/state-model.md row 36; the merge loop records it), with
// l's first prompt (a job's wake, "": none) and Force: the start (its line "running" when it runs already: nothing
// started).
// l.Fresh (a job's first start, T-602): a new session, also in place of one that runs.
func (a *App) startIntegrator(ctx context.Context, agents port.Agents, root string, l port.Launch) (port.Started, error) {
	a.mu.Lock()
	p := a.projAt(root)
	var w Where
	if p != nil {
		w = a.whereIs(p, p.integrator)
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return port.Started{}, fmt.Errorf("no served project at %s", root)
	case p.integrator == "":
		return port.Started{}, fmt.Errorf("%s has no integrator", p.name)
	case w.State == Identified && !l.Fresh:
		return port.Started{Line: "running"}, nil
	case w.State == Ambiguous:
		return port.Started{}, fmt.Errorf("integrator %s", notIdentified(p.integrator, w))
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return port.Started{}, err
	}
	if rc, _ := cfg.Role("integrator"); unbridged(cfg, rc.Type) != "" {
		return port.Started{}, fmt.Errorf("integrator %s: its kind %s has no bridge (obor config validate)", p.integrator,
			unbridged(cfg, rc.Type))
	}
	l.Agent, l.Pane = p.integrator, ""
	return agents.Launch(ctx, l)
}
