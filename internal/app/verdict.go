package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"obor/internal/config"
)

// The integrator's verdicts over the app socket (docs/state-model.md rows 26–31, rule 3): accept, sendback, reject
// and resolved, and hold (the job stays open, the owner decides); and the owner's resume of a needs-you (row 40). They
// are validated and recorded here; the merge loop (loop.go) acts on them.

// Verdict methods of the app socket.
const (
	MethodAccept   = "accept"   // the candidate whose patch hash it names may be published
	MethodSendback = "sendback" // back to the worker with the integrator's notes
	MethodReject   = "reject"   // drop the patch (a resolve job: the resolve failed)
	MethodResolved = "resolved" // the conflict is merged in the scratch: gate it again
	MethodHold     = "hold"     // the owner must decide: needs-you{held: why}, the job stays open
	MethodDrop     = "drop"     // the lead's or the owner's: end the task for good (Call.Note: why), whatever it waits for
	MethodResume   = "resume"   // the lead's or the owner's: end the task's needs-you (Call.To: regate, reprompt, retry, "")
)

// Job steps: the integrator's jobs, prompted by the merge loop as intents with these steps (a later job of the same
// attempt: review.2, resolve.2 …); their data is JobData.
const (
	StepReview  = "review"  // awaiting-accept: accept, sendback or reject
	StepResolve = "resolve" // resolving: resolved or reject
)

// JobData is a review or resolve prompt intent's data: the candidate's patch hash an accept must name.
type JobData struct {
	Patch string `json:"patch,omitempty"`
}

// Call is one verdict request: who calls (OBOR_AGENT, OBOR_ROLE), from which project, its call id, and the task and
// job it answers. Job "" answers the task's current job. Patch is accept's; Note is accept's note and the why of a
// sendback, reject or hold; To is resume's regate, reprompt or retry ("": the default for the needs-you's cause).
// Lane, Types and Force are a dispatch's: the lane it names, else the first free lane of the types; force also takes a
// dirty-free lane and stops the agent's background tasks.
type Call struct {
	Root   string `json:"root"`
	Agent  string `json:"agent"`
	Role   string `json:"role"`
	CallID string `json:"call_id"`
	Task   string `json:"task"`
	Job    string `json:"job,omitempty"`
	Patch  string `json:"patch,omitempty"`
	Note   string `json:"note,omitempty"`
	To     string `json:"to,omitempty"`
	// A dispatch's.
	Lane  string   `json:"lane,omitempty"`
	Types []string `json:"types,omitempty"`
	Force bool     `json:"force,omitempty"`
	After []string `json:"after,omitempty"` // the tasks it waits for (obor dispatch --after)
	Level string   `json:"level,omitempty"` // set with Types by a waiting task's re-dispatch: the spec's level they came from
	// A drop's (by Lane, T-439): discard the lane's uncommitted changes and unmerged commits.
	Discard bool `json:"discard,omitempty"`
	// The MCP job tools': the caller's herdr pane; task_done's report.
	Pane   string `json:"pane,omitempty"`
	Report string `json:"report,omitempty"`
	// spec_set's (specs.go): the spec's text.
	Spec string `json:"spec,omitempty"`
	// pause's (pause.go): its end (epoch seconds, 0: until cleared), or clear the pause in force; Note is its why.
	Until int64 `json:"until,omitempty"`
	Clear bool  `json:"clear,omitempty"`
	// send's (messages.go): the sender project's name; To is a brief's role agent.
	From string `json:"from,omitempty"`
	// stopped's (stopped.go): the API error that ended the agent's turn and its details (T-532).
	Error   string `json:"error,omitempty"`
	Details string `json:"details,omitempty"`
	// turn's (stopped.go): the hook is the turn's end (Stop); false: its start (UserPromptSubmit; T-571).
	Ended bool `json:"ended,omitempty"`
	// session's (sessions.go): the session id, its transcript file, the agent's kind, when it started (Unix seconds)
	// and who reports it; Pane, Lane and Task are where it started.
	Session    string `json:"session,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Started    int64  `json:"started,omitempty"`
	Source     string `json:"source,omitempty"`
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
	case MethodAccept, MethodSendback, MethodReject, MethodResolved, MethodAnswer:
		return m, id
	}
	return "", ""
}

// foldJob folds r into p's answered calls: a verdict's result stores its reply under the call id; a hold (its
// needs-you) and a resume (its unblocked) store theirs. Which job is open is the merge loop's (mtask.openJob).
func (p *proj) foldJob(r Record) {
	switch {
	case r.Kind == KindFact && (r.Step == StepDispatched || r.Step == StepWaiting):
		var d dispatchData
		_ = json.Unmarshal(r.Data, &d)
		if d.Call != "" {
			p.calls[verdictStep(MethodDispatch, d.Call)] = d.Reply
		}
	case r.Kind == KindFact && r.Step == StepSubmitted:
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		if d.Call != "" {
			p.calls[verdictStep(MethodTaskDone, d.Call)] = taskDoneReply
		}
	case r.Kind == KindFact && r.Step == StepDelivered:
		p.delivered[r.Fact().Job] = true
	case r.Kind == KindFact && r.Step == StepDropped:
		if d := r.Fact(); d.Call != "" {
			p.calls[verdictStep(MethodDrop, d.Call)] = dropReply(r.Task)
		}
	case r.Kind == KindFact && (r.Step == StepNeedsYou || r.Step == StepUnblocked):
		if d := r.Fact(); d.Call != "" && r.Step == StepNeedsYou {
			p.calls[verdictStep(MethodHold, d.Call)] = holdReply(r.Task, d.Job)
		} else if d.Call != "" {
			p.calls[verdictStep(MethodResume, d.Call)] = resumeReply(r.Task, d.Reason, d.Cause)
		}
	case r.Kind == KindResult:
		method, id := verdictMethod(r.Step)
		if method == "" {
			return
		}
		var v verdictResult
		_ = json.Unmarshal(r.Data, &v)
		p.calls[method+"."+id] = v.Reply
		if method == MethodAnswer {
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
func (a *App) verdict(ctx context.Context, method string, c Call) (string, error) {
	a.verdicts.Lock() // one verdict at a time: two calls never both answer one job
	defer a.verdicts.Unlock()
	if c.CallID == "" || c.Task == "" {
		return "", fmt.Errorf("%s: a call id and a task are required", method)
	}
	if method == MethodAnswer {
		return a.answer(c)
	}
	if method == MethodSendback && c.Role != "integrator" {
		return a.leadSendback(ctx, method, c)
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	var key Key
	var patch string
	var open, dropped bool
	var jobSt string // the state of the task's job out: its verdicts
	var input string // a resolve job's: the conflicting gate's input, the submitted sha (T-403)
	sendbacks := 0
	merging := false
	if p != nil {
		merging = p.merging
		reply, stored = p.calls[verdictStep(method, c.CallID)]
		if mt := p.loop[c.Task]; mt != nil {
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
	jobStep, _ := stepN(key.Step)
	switch {
	case method != MethodHold && !takes(jobSt, method):
		return "", fmt.Errorf("%s: %s is a %s job (%s)", method, JobID(key), jobStep, accepts(jobSt))
	case method == MethodAccept && c.Patch != patch:
		return "", fmt.Errorf("stale: re-read the candidate (%s's patch is %s, not %s)", c.Task, patch, Dash(c.Patch))
	case (method == MethodSendback || method == MethodReject || method == MethodHold) && strings.TrimSpace(c.Note) == "":
		return "", fmt.Errorf("%s: say why", method)
	case method == MethodHold:
		return a.hold(p, c, key)
	}
	var sha string
	if method == MethodResolved {
		fx, err := a.loopOf(p)
		if err == nil {
			sha, err = a.resolvedCheck(ctx, p, fx, c.Task, input)
		}
		if err != nil {
			return "", fmt.Errorf("%s: %v", method, err)
		}
	}
	in := Intent{Key: Key{Task: c.Task, Attempt: key.Attempt, Step: verdictStep(method, c.CallID)}, Class: ClassFact,
		Data: verdictData{Job: JobID(key), Caller: c.Agent, Patch: c.Patch, Note: c.Note}}
	reply = fmt.Sprintf("%s %s (job %s)", strings.ToUpper(method), c.Task, JobID(key))
	if method == MethodSendback && sendbacks >= K {
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
	p.foldRecord(Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: KindResult, Data: raw,
		TS: a.opt.Now().UTC().Format(time.RFC3339)}) // a sendback's time: the next attempt's begun
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
func (a *App) leadSendback(ctx context.Context, method string, c Call) (string, error) {
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
	in := Intent{Key: Key{Task: c.Task, Attempt: mt.attempt, Step: verdictStep(method, c.CallID)},
		Class: ClassFact, Data: verdictData{Job: job, Caller: c.Agent, Note: c.Note}}
	raw, err := p.log.Do(ctx, in, a.opt.Now, func(context.Context) (any, error) {
		return verdictResult{Job: job, Reply: reply, Note: c.Note, Lead: true}, nil
	})
	if err != nil {
		return "", err
	}
	var v verdictResult
	_ = json.Unmarshal(raw, &v)
	a.mu.Lock()
	p.foldRecord(Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: KindResult, Data: raw,
		TS: a.opt.Now().UTC().Format(time.RFC3339)}) // the next attempt's begun
	a.mu.Unlock()
	a.logf("%s %s by %s", p.name, v.Reply, Dash(c.Agent))
	a.wake()
	return v.Reply, nil
}

// sendbackDecide is leadSendback's decision (under mu): the task as it is now, or the stored reply of the call id, or
// the refusal. A step of the task running, or a gate or publish pending, is refused until it settles.
func (a *App) sendbackDecide(p *proj, method string, c Call) (mtask, string, error) {
	if p == nil {
		return mtask{}, "", fmt.Errorf("%s: no served project at %s", method, c.Root)
	}
	if reply, stored := p.calls[verdictStep(method, c.CallID)]; stored {
		return mtask{}, reply, nil
	}
	var mt mtask
	if m := p.loop[c.Task]; m != nil {
		mt = *m
	}
	switch {
	case !p.merging:
		return mtask{}, "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		if method == MethodAnswer {
			return mtask{}, "", fmt.Errorf("answer: the lead or the owner answers a worker, not %s (role %s)",
				Dash(c.Agent), c.Role)
		}
		return mtask{}, "", fmt.Errorf("sendback: the integrator, the lead or the owner sends a task back, not %s (role %s)",
			Dash(c.Agent), c.Role)
	case strings.TrimSpace(c.Note) == "" && method == MethodAnswer:
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
	case p.log.Open(Key{c.Task, mt.attempt, nth(StepGate, mt.gates+1)}):
		return mtask{}, "", fmt.Errorf("%s: %s has %s pending; retry when it settles", method, c.Task, nth(StepGate, mt.gates+1))
	case p.log.Open(Key{c.Task, mt.attempt, nth(StepPublish, mt.pubs+1)}):
		return mtask{}, "", fmt.Errorf("%s: %s has %s pending; retry when it settles", method, c.Task, nth(StepPublish, mt.pubs+1))
	}
	why := mt.unsendable()
	if method == MethodAnswer {
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
func (a *App) hold(p *proj, c Call, job Key) (string, error) {
	a.mu.Lock()
	var lane string
	held := false
	if mt := p.loop[c.Task]; mt != nil {
		lane, held = mt.lane, mt.needs.Seq != 0 && mt.needs.Fact().Cause == causeHeld
	}
	a.mu.Unlock()
	if held {
		return "", fmt.Errorf("hold: %s is held already (the owner is told)", c.Task)
	}
	if !a.record(p, Record{Task: c.Task, Attempt: job.Attempt, Step: StepNeedsYou, Kind: KindFact},
		FactData{Lane: lane, Machine: Machine, Reason: "held: " + c.Note, Merge: true, Cause: causeHeld, Job: JobID(job),
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
// from it, and the integrator's next job of the attempt carries the decision (decisionText, T-511).
func (a *App) resume(c Call) (string, error) {
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
	merging := false
	if p != nil {
		merging = p.merging
		reply, stored = p.calls[verdictStep(MethodResume, c.CallID)]
		if m := p.loop[c.Task]; m != nil {
			mt = *m
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
	d := mt.needs.Fact()
	if d.Cause == causeOpen && strings.TrimSpace(c.Note) == "" { // the hold waits for the lead's answer (T-612)
		return "", fmt.Errorf("resume: %s waits on the lead's answer to its open questions: obor resume %s \"<decision>\"",
			c.Task, c.Task)
	}
	to, err := mt.resumeTo(c.Task, c.To)
	if err != nil {
		return "", err
	}
	if !a.record(p, Record{Task: c.Task, Attempt: mt.attempt, Step: StepUnblocked, Kind: KindFact},
		FactData{Lane: mt.lane, Machine: Machine, Agent: c.Agent, Reason: to, Merge: true, Cause: d.Cause, Call: c.CallID,
			Note: strings.TrimSpace(c.Note)}) {
		return "", fmt.Errorf("resume: the event log of %s refused it", p.name)
	}
	a.wake()
	return resumeReply(c.Task, to, d.Cause), nil
}

// ResumeArgs is obor resume's arguments, <T-xx> [regate|reprompt] ["<decision>"], as a call's Task, To and Note:
// regate and reprompt are the reserved modes, any other nonblank last argument is the decision (obor resume T-xx
// "yes", RV-54 #4). False: not that form.
func ResumeArgs(args []string) (Call, bool) {
	if len(args) == 0 || args[0] == "" {
		return Call{}, false
	}
	c, rest := Call{Task: args[0]}, args[1:]
	if len(rest) > 0 && (rest[0] == ResumeRegate || rest[0] == ResumeReprompt) {
		c.To, rest = rest[0], rest[1:]
	}
	if len(rest) == 1 && strings.TrimSpace(rest[0]) != "" {
		c.Note, rest = rest[0], nil
	}
	return c, len(rest) == 0
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
	case to != ResumeRegate && to != ResumeReprompt && to != ResumeRetry:
		return "", fmt.Errorf("resume: %s is neither regate, reprompt nor retry", to)
	case rule.only != "" && to != rule.only:
		return "", fmt.Errorf("resume: %s left its dispatch (%s): retry it", task, d.Reason)
	case to == ResumeRetry && !strings.HasSuffix(d.Cause, failedSuffix) && !rule.retry:
		return "", fmt.Errorf("resume: %s has no failed step to retry (%s): regate or reprompt", task, d.Reason)
	case to == ResumeReprompt && (rule.noReprompt || !mt.repromptable()):
		return "", fmt.Errorf("resume: %s has no integrator job to re-prompt (%s): regate", task, d.Reason)
	}
	return to, nil
}

// repromptable: the attempt has an integrator job to send anew: its last one, or the review job of a green candidate a
// needs-you held for the lead before it was sent (T-604).
func (mt mtask) repromptable() bool { return mt.job != "" || mt.kept() == stGreen }

func dropReply(task string) string { return "DROPPED " + task }

// drop answers the lead's or the owner's drop of task, or of lane's task (obor drop <LANE>: the lane freed too,
// freeLane; c.Note: why): a repeated call id gets the stored reply; else
// the project's merge loop runs, the caller is not an agent of another role, the task is open in the loop (or, by
// lane, its row holds it), and no step of it runs (p.held) nor has a gate or publish pending. One mu section decides
// and marks the task ended (p.ended), the same section a selected step's start check takes (begin), so no step
// starts after it. Then the lane is freed (by lane) and one fact, dropped{why, by}: the task leaves the loop and its
// lane row, its needs-you and timers end. A refused lane or a failed write clears the mark.
func (a *App) drop(c Call) (string, error) {
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
		ok = a.record(p, Record{Task: c.Task, Attempt: mt.attempt, Step: StepDropped, Kind: KindFact},
			FactData{Lane: mt.lane, Machine: Machine, Agent: c.Agent, Reason: c.Note, Merge: true, Call: c.CallID})
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
// not with stop_idle_workers false unless c.Force; a stop that fails is a line, not a refusal). Its lines say what it
// did, the stop first.
func (a *App) freeLane(p *proj, c Call, mt mtask) ([]string, error) {
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
	in := FreeInput{Lane: c.Lane, Dir: dir, Branch: strings.ReplaceAll(cfg.Data.Branches.Task, "{id}", c.Task),
		Target: target, Discard: c.Discard}
	var lines []string
	a.mu.Lock()
	agent := rowAgent(cfg, c.Lane, p.laneAgent(c.Lane))
	if row := rowAgent(cfg, c.Lane, p.lr.rows[c.Lane].Agent); row != "" {
		agent = row
	}
	a.mu.Unlock()
	if agent == "" {
		agent = mt.agent
	}
	line, err := fx.Lane.Free(ctx, in)
	var refused Refused
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
		if line, err := fx.Agents.Stop(ctx, agent, "", c.Force); err != nil && !NotFound(err) {
			lines = append(lines, fmt.Sprintf("worker of %s not stopped: %v", c.Lane, err))
		} else if err == nil && line != "" {
			lines = append([]string{line}, lines...)
		}
	}
	return lines, nil
}

// dropDecide is drop's decision (under mu): the task to drop, marked ended; mt.task "" with no error: the call id's
// reply is stored.
func (a *App) dropDecide(p *proj, c *Call) (mtask, error) {
	if p == nil {
		return mtask{}, fmt.Errorf("drop: no served project at %s", c.Root)
	}
	if _, stored := p.calls[verdictStep(MethodDrop, c.CallID)]; stored {
		return mtask{}, nil
	}
	if c.Task == "" { // by lane: the task the loop has open there, else its row's, else its branch's
		for t, m := range p.loop {
			if m.lane == c.Lane && !states[m.state].ended {
				c.Task = t
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
	if m := p.loop[c.Task]; m != nil {
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
	case p.log.Open(Key{c.Task, mt.attempt, nth(StepGate, mt.gates+1)}):
		return mtask{}, fmt.Errorf("drop: %s has %s pending: retry once it settles", c.Task, nth(StepGate, mt.gates+1))
	case p.log.Open(Key{c.Task, mt.attempt, nth(StepPublish, mt.pubs+1)}):
		return mtask{}, fmt.Errorf("drop: %s has %s pending: retry once it settles", c.Task, nth(StepPublish, mt.pubs+1))
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
func (a *App) startIntegrator(ctx context.Context, agents Agents, root string, l Launch) (Started, error) {
	a.mu.Lock()
	p := a.projAt(root)
	var w Where
	if p != nil {
		w = a.whereIs(p, p.integrator)
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return Started{}, fmt.Errorf("no served project at %s", root)
	case p.integrator == "":
		return Started{}, fmt.Errorf("%s has no integrator", p.name)
	case w.State == Identified && !l.Fresh:
		return Started{Line: "running"}, nil
	case w.State == Ambiguous:
		return Started{}, fmt.Errorf("integrator %s", notIdentified(p.integrator, w))
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return Started{}, err
	}
	if rc, _ := cfg.Role("integrator"); unbridged(cfg, rc.Type) != "" {
		return Started{}, fmt.Errorf("integrator %s: its kind %s has no bridge (obor config validate)", p.integrator,
			unbridged(cfg, rc.Type))
	}
	l.Agent, l.Pane, l.Sid = p.integrator, "", ""
	return agents.Launch(ctx, l)
}
