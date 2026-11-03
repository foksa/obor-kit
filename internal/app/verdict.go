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
	"strings"
	"time"

	"obor/internal/config"
)

// The integrator's verdicts over the app socket (docs/state-model.md rows 26–31, rule 3): accept, sendback, reject
// and resolved, and hold (the job stays open, the owner decides); and the owner's resume of a needs-you (row 40). They
// are validated and recorded here; the merge loop (loop.go) acts on them.

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
	case wire.MethodAccept, wire.MethodSendback, wire.MethodReject, wire.MethodResolved, wire.MethodAnswer:
		return m, id
	}
	return "", ""
}

// foldJob folds r into p's answered calls: a verdict's result stores its reply under the call id; a hold (its
// needs-you) and a resume (its unblocked) store theirs. Which job is open is the merge loop's (mtask.openJob).
func (p *proj) foldJob(r event.Record) {
	switch {
	case r.Kind == event.KindFact && (r.Step == event.StepDispatched || r.Step == event.StepWaiting || r.Step == event.StepDrive):
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
		p.folded.delivered[r.Fact().Job] = r.Seq
	case r.Kind == event.KindFact && r.Step == event.StepDropped:
		if d := r.Fact(); d.Call != "" {
			p.folded.calls[verdictStep(wire.MethodDrop, d.Call)] = dropReply(r.Task)
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
		if method == wire.MethodAnswer {
			p.answered(r.Task)
		}
	}
}

// verdict answers one verdict call: a repeated call id gets the stored reply; else the call is validated (the
// project's merge loop runs (an integrator role), the caller is the project's integrator, the job is the task's open one
// (mtask.openJob) and fits the method, an accept names its patch) and recorded, a fact under Log.Do, and the merge
// loop woken. A refused call records nothing. A sendback past K sendbacks of the task is recorded and goes to the
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
	if method == wire.MethodAnswer {
		return a.answer(c)
	}
	if method == wire.MethodSendback && c.Role != "integrator" {
		return a.leadSendback(ctx, method, c)
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	var key event.Key
	var patch string
	var open, dropped bool
	var jobSt string // the state of the task's job out: its verdicts
	var input string // a resolve job's: the conflicting gate's input, the submitted sha (T-403)
	sendbacks := 0
	merging := false
	if p != nil {
		merging = p.merging
		reply, stored = p.folded.calls[verdictStep(method, c.CallID)]
		if mt := p.folded.loop[c.Task]; mt != nil {
			key, patch, open, sendbacks, input = mt.jobKey(), mt.patch, mt.openJob(), mt.sendbacks, mt.input
			jobSt = mt.jobState(true)
			dropped = mt.state == stDropped || p.ended[c.Task]
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
			sha, err = a.resolvedCheck(ctx, p, fx, c.Task, input)
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
	raw, err := p.log.Do(ctx, in, a.opt.Now, func(context.Context) (any, error) {
		return verdictResult{Job: JobID(key), Reply: reply, Patch: c.Patch, Note: c.Note, SHA: sha}, nil
	})
	if err != nil {
		return "", err
	}
	var v verdictResult
	_ = json.Unmarshal(raw, &v)
	a.mu.Lock()
	was := p.closed(c.Task)
	p.foldRecord(event.Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: event.KindResult, Data: raw,
		TS: a.opt.Now().UTC().Format(time.RFC3339)}) // a sendback's time: the next attempt's begun
	if !was && p.closed(c.Task) {
		a.settleEnded(p, c.Task) // rejected: its cleared watchdog notifications never run (T-636)
	}
	a.mu.Unlock()
	a.logf("%s %s by %s", p.name, v.Reply, c.Agent)
	a.wake()
	return v.Reply, nil
}

// leadSendback answers the lead's or the owner's sendback of a task (c.Note: why; RV-45 #1), under a.verdicts: the
// integrator's sendback transition, from a green candidate, an open job (awaiting-accept, resolving) or a needs-you
// after a red gate or a hold. A repeated call id gets the stored reply. The decision holds the task (p.held) until the
// verdict has folded, so no step of the old attempt starts meanwhile. The fold ends the job, the candidate and the
// needs-you and starts the next attempt; the merge loop sends the worker its fix job. K is the integrator's bound, not
// the lead's.
func (a *App) leadSendback(ctx context.Context, method string, c wire.Call) (string, error) {
	a.mu.Lock()
	p := a.projAt(c.Root)
	mt, reply, err := a.sendbackDecide(p, method, c)
	if err == nil && reply == "" {
		p.held[c.Task] = method
	}
	a.mu.Unlock()
	if err != nil || reply != "" {
		return reply, err
	}
	defer a.release(p, c.Task)
	job := ""
	reply = fmt.Sprintf("%s %s (attempt %d, %s)", strings.ToUpper(method), c.Task, mt.attempt, mt.state)
	if mt.openJob() {
		job = JobID(mt.jobKey())
		reply = fmt.Sprintf("SENDBACK %s (job %s)", c.Task, job)
	}
	in := event.Intent{Key: event.Key{Task: c.Task, Attempt: mt.attempt, Step: verdictStep(method, c.CallID)},
		Class: event.ClassFact, Data: verdictData{Job: job, Caller: c.Agent, Note: c.Note}}
	raw, err := p.log.Do(ctx, in, a.opt.Now, func(context.Context) (any, error) {
		return verdictResult{Job: job, Reply: reply, Note: c.Note, Lead: true}, nil
	})
	if err != nil {
		return "", err
	}
	var v verdictResult
	_ = json.Unmarshal(raw, &v)
	a.mu.Lock()
	p.foldRecord(event.Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: event.KindResult, Data: raw,
		TS: a.opt.Now().UTC().Format(time.RFC3339)}) // the next attempt's begun
	a.mu.Unlock()
	a.logf("%s %s by %s", p.name, v.Reply, Dash(c.Agent))
	a.wake()
	return v.Reply, nil
}

// sendbackDecide is leadSendback's decision (under mu): the task as it is now, or the stored reply of the call id, or
// the refusal. A step of the task running, or a gate or publish pending, is refused until it settles.
func (a *App) sendbackDecide(p *proj, method string, c wire.Call) (mtask, string, error) {
	if p == nil {
		return mtask{}, "", fmt.Errorf("%s: no served project at %s", method, c.Root)
	}
	if reply, stored := p.folded.calls[verdictStep(method, c.CallID)]; stored {
		return mtask{}, reply, nil
	}
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	switch {
	case !p.merging:
		return mtask{}, "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		if method == wire.MethodAnswer {
			return mtask{}, "", fmt.Errorf("answer: the lead or the owner answers a worker, not %s (role %s)",
				Dash(c.Agent), c.Role)
		}
		return mtask{}, "", fmt.Errorf("sendback: the integrator, the lead or the owner sends a task back, not %s (role %s)",
			Dash(c.Agent), c.Role)
	case strings.TrimSpace(c.Note) == "" && method == wire.MethodAnswer:
		return mtask{}, "", errors.New("answer: say the answer")
	case strings.TrimSpace(c.Note) == "":
		return mtask{}, "", errors.New("sendback: say why")
	case mt.task == "":
		return mtask{}, "", fmt.Errorf("%s: %s is not open in obor serve", method, c.Task)
	case p.ended[c.Task]:
		return mtask{}, "", fmt.Errorf("%s: %s is %s", method, c.Task, stDropped)
	case p.held[c.Task] != "":
		return mtask{}, "", fmt.Errorf("%s %s: %s is running; retry when it settles", method, c.Task, p.held[c.Task])
	case p.running.Task == c.Task:
		return mtask{}, "", fmt.Errorf("%s: %s has %s running; retry when it settles", method, c.Task, p.running.Step)
	case p.log.Open(event.Key{Task: c.Task, Attempt: mt.attempt, Step: nth(event.StepGate, mt.gates+1)}):
		return mtask{}, "", fmt.Errorf("%s: %s has %s pending; retry when it settles", method, c.Task, nth(event.StepGate, mt.gates+1))
	case p.log.Open(event.Key{Task: c.Task, Attempt: mt.attempt, Step: nth(event.StepPublish, mt.pubs+1)}):
		return mtask{}, "", fmt.Errorf("%s: %s has %s pending; retry when it settles", method, c.Task, nth(event.StepPublish, mt.pubs+1))
	}
	why := mt.unsendable()
	if method == wire.MethodAnswer {
		why = p.unanswerable(mt)
	}
	if why != "" {
		return mtask{}, "", errors.New(method + ": " + why)
	}
	return mt, "", nil
}

// resolvedCheck is Scratch.Resolved holding p's scratch: never while a gate or a publish runs there.
func (a *App) resolvedCheck(ctx context.Context, p *proj, fx *Loop, task, input string) (string, error) {
	select {
	case p.scratch <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-p.scratch }()
	return fx.Scratch.Resolved(ctx, task, input)
}

// hold records the integrator's hold of job j (under a.verdicts): one fact, needs-you{held: why}, which also stores
// the reply under the call id. The job stays open: the integrator's verdict on it ends the hold, as obor resume does.
func (a *App) hold(p *proj, c wire.Call, job event.Key) (string, error) {
	a.mu.Lock()
	var lane string
	held, drive := false, false
	if mt := p.folded.loop[c.Task]; mt != nil {
		lane, held, drive = mt.lane, mt.needs.Seq != 0 && mt.needs.Fact().Cause == event.CauseHeld, mt.drive
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

func resumeReply(task, to, cause string) string {
	return fmt.Sprintf("RESUMED %s: %s (%s)", task, to, Dash(cause))
}

// resume answers the lead's or the owner's resume of task's merge-loop needs-you (row 40): a repeated call id gets
// the stored reply; else the project's merge loop runs, the caller is not an agent of another role, the task has a
// needs-you in force that is not a reject's, and c.To (or the cause's default) fits it: reprompt needs an integrator
// job of the attempt, retry a failed step. One fact, unblocked{to, note: c.Note, the decision}; the merge loop goes on
// from it, and the integrator's next job of the attempt carries the decision (decisionText, T-511). A task with no
// merge-loop needs-you but the watchdog's needs-you{stalled} in force is resumeStalled's (T-746).
func (a *App) resume(c wire.Call) (string, error) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	if c.CallID == "" || c.Task == "" {
		return "", errors.New("resume: a call id and a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	var mt mtask
	merging, stalled, worker := false, false, ""
	if p != nil {
		merging = p.merging
		reply, stored = p.folded.calls[verdictStep(wire.MethodResume, c.CallID)]
		if !stored { // a stalled worker's resume is stored as the answer it records (resumeStalled)
			reply, stored = p.folded.calls[verdictStep(wire.MethodAnswer, c.CallID)]
		}
		if m := p.folded.loop[c.Task]; m != nil {
			mt = *m
		}
		stalled = p.has(c.Task, event.StepNeedsYou, reasonStalled)
		worker = mt.agent
		if w := p.tasks[c.Task]; w != nil && worker == "" {
			worker = w.lane.Agent
		}
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("resume: no served project at %s", c.Root)
	case stored:
		return reply, nil
	case !merging:
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("resume: the lead or the owner resumes a task, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	if mt.needs.Seq == 0 && stalled {
		return a.resumeStalled(p, c, worker)
	}
	d := mt.needs.Fact()
	to, err := mt.resumeTo(c.Task, c.To)
	if err != nil {
		return "", err
	}
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepUnblocked, Kind: event.KindFact},
		event.FactData{Lane: mt.lane, Machine: Machine, Agent: c.Agent, Reason: to, Merge: true, Cause: d.Cause, Call: c.CallID,
			Note: strings.TrimSpace(c.Note)}) {
		return "", fmt.Errorf("resume: the event log of %s refused it", p.name)
	}
	a.wake()
	return resumeReply(c.Task, to, d.Cause), nil
}

// resumeStalled resumes task c.Task's watchdog needs-you{stalled} (T-746): its worker went quiet with its job open. The
// decision (c.Note, required; c.To none or reprompt: regate and retry are the merge loop's) goes to worker as an
// answer's line does: fact answered{to: worker, line "[obor answer] <decision>"}, whose fold ends the task's watchdog
// facts (the needs-you with them) and whose line deliver sends at the worker's next idle.
func (a *App) resumeStalled(p *proj, c wire.Call, worker string) (string, error) {
	note := strings.TrimSpace(c.Note)
	switch {
	case c.To != "" && c.To != wire.ResumeReprompt:
		return "", fmt.Errorf("resume: %s's worker stalled: reprompt it with the decision (obor resume %s \"<decision>\")",
			c.Task, c.Task)
	case note == "":
		return "", fmt.Errorf("resume: say the decision for %s's stalled worker (obor resume %s \"<decision>\")", c.Task,
			c.Task)
	case worker == "":
		return "", fmt.Errorf("resume: %s has no worker to send the decision to", c.Task)
	}
	m := event.Message{To: worker, From: cmp.Or(c.Agent, "owner"), Line: "[obor answer] " + note, Call: c.CallID,
		Task: c.Task, Reply: resumeReply(c.Task, wire.ResumeReprompt, reasonStalled)}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("resume: the event log of %s refused it", p.name)
	}
	a.logf("%s %s: obor serve sends %s the decision by %s", p.name, m.Reply, worker, Dash(c.Agent))
	a.wake()
	return m.Reply, nil
}

// resumeTo is the target obor resume takes for task's needs-you in mt with to ("": the cause's default), or the
// refusal of mt's state: a needs-you in force that is not a reject's, and a target that fits it (reprompt needs an
// integrator job of the attempt, retry a failed step). answerPath suggests the targets it takes.
func (mt mtask) resumeTo(task, to string) (string, error) {
	d := mt.needs.Fact()
	rule := mt.resumeRule(d.Cause) // the state the needs-you left: a dispatch's step runs again, an undelivered fix job is sent anew
	switch {
	case to == "" && rule.to != "":
		to = rule.to
	case to == "":
		to = resumeDefaultOf(d.Cause)
	}
	switch {
	case mt.needs.Seq == 0:
		return "", fmt.Errorf("resume: %s has no needs-you of the merge loop", task)
	case rule.closed:
		return "", fmt.Errorf("resume: %s is rejected (%s): free its lane", task, d.Reason)
	case to == "":
		return "", fmt.Errorf("resume: say regate or reprompt (%s: %s)", task, d.Reason)
	case to != wire.ResumeRegate && to != wire.ResumeReprompt && to != wire.ResumeRetry:
		return "", fmt.Errorf("resume: %s is neither regate, reprompt nor retry", to)
	case rule.only != "" && to != rule.only:
		return "", fmt.Errorf("resume: %s left its dispatch (%s): retry it", task, d.Reason)
	case to == wire.ResumeRetry && !strings.HasSuffix(d.Cause, failedSuffix) && !rule.retry:
		return "", fmt.Errorf("resume: %s has no failed step to retry (%s): regate or reprompt", task, d.Reason)
	case to == wire.ResumeReprompt && (rule.noReprompt || !mt.repromptable()):
		return "", fmt.Errorf("resume: %s has no integrator job to re-prompt (%s): regate", task, d.Reason)
	}
	return to, nil
}

// repromptable: the attempt has an integrator job to send anew: its last one, or the review job of a green candidate a
// needs-you held before it was sent.
func (mt mtask) repromptable() bool { return mt.job != "" || mt.kept() == stGreen }

func dropReply(task string) string { return "DROPPED " + task }

// drop answers the lead's or the owner's drop of task, or of lane's task (obor drop <LANE>: the lane freed too,
// freeLane; c.Note: why): a repeated call id gets the stored reply; else
// the project's merge loop runs, the caller is not an agent of another role, the task is open in the loop (or, by
// lane, its row holds it), and no step of it runs (p.held) nor has a gate or publish pending. One mu section decides
// and marks the task ended (p.ended), the same section a selected step's start check takes (begin), so no step
// starts after it. Then the lane is freed (by lane) and one fact, dropped{why, by}: the task leaves the loop and its
// lane row, its needs-you and timers end. A refused lane or a failed write clears the mark.
func (a *App) drop(c wire.Call) (string, error) {
	if c.CallID == "" || c.Task == "" && c.Lane == "" {
		return "", errors.New("drop: a call id and a task or a lane are required")
	}
	a.verdicts.Lock() // one verdict at a time; held only for the decision and the write, never across a wait
	a.mu.Lock()
	p := a.projAt(c.Root)
	mt, err := a.dropDecide(p, &c)
	a.mu.Unlock()
	a.verdicts.Unlock()
	if err != nil || mt.task == "" {
		return dropReply(c.Task), err // mt.task "": a stored reply
	}
	var lines []string
	if c.Lane != "" { // obor drop <LANE>: the lane is given back first; a refusal leaves the task as it was
		if lines, err = a.freeLane(p, c, mt); err != nil {
			a.mu.Lock()
			delete(p.ended, c.Task)
			a.mu.Unlock()
			return "", err
		}
	}
	ok := true
	if mt.state != stDropped { // a dropped task's lane freed again: its fact is in
		a.verdicts.Lock()
		ok = a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Lane: mt.lane, Machine: Machine, Agent: c.Agent, Reason: c.Note, Merge: true, Call: c.CallID})
		a.verdicts.Unlock()
	}
	a.mu.Lock()
	delete(p.ended, c.Task) // folded: the task is dropped; else the drop is refused
	a.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("drop: the event log of %s refused it", p.name)
	}
	a.wake()
	if c.Lane == "" {
		return dropReply(c.Task), nil
	}
	return strings.Join(append(lines, fmt.Sprintf("%s from %s (%s)", dropReply(c.Task), c.Lane,
		strings.ReplaceAll(c.Note, "\n", " "))), "\n"), nil
}

// freeLane gives the dropped task's lane back (obor drop <LANE>, T-439; the task marked ended): the lane adapter's
// Free (refused for uncommitted changes or unmerged commits unless c.Discard: nothing stopped then; else the lane
// detached at the target and the task branch deleted), then its worker stopped (at idle, a working one with c.Force;
// not with stop_idle_workers false unless c.Force; through serve's identity of it, stopAgent: no agent there, no line;
// a stop that fails, two panes claiming it or serve not connected to herdr is a line, not a refusal). Its lines say what it
// did, the stop first.
func (a *App) freeLane(p *proj, c wire.Call, mt mtask) ([]string, error) {
	fx, err := a.loopOf(p)
	if err != nil {
		return nil, err
	}
	if fx.Lane == nil || fx.Specs == nil || fx.Agents == nil {
		return nil, fmt.Errorf("drop: lane adapters off for %s", p.name)
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return nil, err
	}
	var dir string
	for _, l := range cfg.Lanes() {
		if l.Name == c.Lane {
			dir = l.Dir
		}
	}
	ctx := a.life
	target, _, err := fx.Specs.Target(ctx, c.Task)
	if err != nil {
		return nil, fmt.Errorf("drop: %v", err)
	}
	in := port.FreeInput{Lane: c.Lane, Dir: dir, Branch: strings.ReplaceAll(cfg.Data.Branches.Task, "{id}", c.Task),
		Target: target, Discard: c.Discard}
	var lines []string
	a.mu.Lock()
	agent := rowAgent(cfg, c.Lane, p.laneAgent(c.Lane))
	if row := rowAgent(cfg, c.Lane, p.folded.lr.rows[c.Lane].Agent); row != "" {
		agent = row
	}
	a.mu.Unlock()
	if agent == "" {
		agent = mt.agent
	}
	line, err := fx.Lane.Free(ctx, in)
	var refused port.Refused
	if errors.As(err, &refused) {
		return nil, fmt.Errorf("drop: %s", refused.Line)
	}
	if err != nil {
		return nil, fmt.Errorf("drop: %v", err)
	}
	if line != "" {
		lines = append(lines, line)
	}
	optOut := cfg.Data.StopIdleWorkers != nil && !*cfg.Data.StopIdleWorkers
	if agent != "" && (c.Force || !optOut) {
		if line, gone, err := a.stopAgent(ctx, p, fx, agent, c.Force, false); err != nil {
			lines = append(lines, fmt.Sprintf("worker of %s not stopped: %v", c.Lane, err))
		} else if !gone {
			lines = append([]string{line}, lines...)
		}
	}
	return lines, nil
}

// dropDecide is drop's decision (under mu): the task to drop, marked ended; mt.task "" with no error: the call id's
// reply is stored.
func (a *App) dropDecide(p *proj, c *wire.Call) (mtask, error) {
	if p == nil {
		return mtask{}, fmt.Errorf("drop: no served project at %s", c.Root)
	}
	if _, stored := p.folded.calls[verdictStep(wire.MethodDrop, c.CallID)]; stored {
		return mtask{}, nil
	}
	if c.Task == "" { // by lane: the task the loop has open there, else its row's, else its branch's
		var newest int64 // two open in the lane: the newest dispatch's task, the lane's current one (T-615)
		for _, t := range p.loopTasks() {
			if m := p.folded.loop[t]; m.lane == c.Lane && !states[m.state].ended && (c.Task == "" || m.dispSeq > newest) {
				c.Task, newest = t, m.dispSeq
			}
		}
		for _, l := range p.lanes {
			if c.Task == "" && l.Name == c.Lane {
				c.Task = cmp.Or(p.laneTask(c.Lane), l.Task)
			}
		}
		if c.Task == "" {
			c.Task = p.laneTask(c.Lane)
		}
		if c.Task == "" {
			return mtask{}, fmt.Errorf("drop: lane %s holds no task", c.Lane)
		}
	}
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	} else if c.Lane != "" { // a task the loop never held (dispatched before obor serve): only its lane is freed
		mt = mtask{task: c.Task, lane: c.Lane}
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
	case p.ended[c.Task]:
		return mtask{}, fmt.Errorf("drop: %s is %s already", c.Task, stDropped)
	case !mt.droppable(c.Lane != ""):
		return mtask{}, fmt.Errorf("drop: %s is %s already", c.Task, mt.state)
	case p.held[c.Task] != "":
		return mtask{}, fmt.Errorf("drop %s: %s is running; retry when it settles", c.Task, p.held[c.Task])
	case p.running.Task == c.Task:
		return mtask{}, fmt.Errorf("drop: %s has %s pending: retry once it settles", c.Task, p.running.Step)
	case p.log.Open(event.Key{Task: c.Task, Attempt: mt.attempt, Step: nth(event.StepGate, mt.gates+1)}):
		return mtask{}, fmt.Errorf("drop: %s has %s pending: retry once it settles", c.Task, nth(event.StepGate, mt.gates+1))
	case p.log.Open(event.Key{Task: c.Task, Attempt: mt.attempt, Step: nth(event.StepPublish, mt.pubs+1)}):
		return mtask{}, fmt.Errorf("drop: %s has %s pending: retry once it settles", c.Task, nth(event.StepPublish, mt.pubs+1))
	}
	p.ended[c.Task] = true
	return mt, nil
}

// droppable: obor drop takes mt's task in its state, by lane or by task: never a merged one; an ended one (rejected,
// dropped) only by lane, whose lane is the owner's to free (its fact: dropped) and a dropped one's lane again.
func (mt mtask) droppable(byLane bool) bool {
	return mt.state != stMerged && (!states[mt.state].ended || byLane)
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
	l.Agent, l.Pane, l.Sid = p.integrator, "", ""
	return agents.Launch(ctx, l)
}
