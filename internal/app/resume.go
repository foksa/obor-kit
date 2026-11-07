package app

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"time"
)

// obor resume <T-xx> ["<text>"] [--regate|--sendback] (T-775) is the one verb that sends the lead's or the owner's word
// to a task; serve picks the action from the task's state (resumeAct):
//   - a worker's question in force (its Blocked: report, needs-you{asks}, row 16b): the answer, fact answered{to: the
//     worker, line "[obor answer] <text>"}, delivered in the same attempt as its kind takes a line; --regate instead
//     gates the lane as it is (a Blocked "re-gate, not my diff");
//   - a stalled or stopped worker (the watchdog's needs-you{stalled}, a stop on an API error; T-746): the text as the
//     same line;
//   - a driving task (T-761): the text as the line to the drive agent (answered{driver}), each its own line;
//   - a green candidate, a review or resolve job out: the lead's sendback (the text its why);
//   - a merge-loop needs-you (row 40): what its cause takes: held → the decision to the integrator (reprompt), a
//     failed step → retry, a gate or publish failure → regate, an undelivered or stalled job → reprompt; a red gate or
//     sendbacks past K, or a resolve rejected → the owner's choice, --regate or --sendback.
//
// A flag only where two actions fit; one that does not fit is refused naming what would happen. The text is required
// for an answer, a line, a sendback and a hold's decision; elsewhere it is the decision the integrator's next job
// carries. One fact per action; a repeated call id gets the stored reply. An answer result of an older log
// (answer.<call id>, its fix job the next attempt) still folds as it did (stepAnswer).

// The actions of obor resume besides the unblocked fact's (wire.ResumeRegate, ResumeReprompt, ResumeRetry).
const (
	actAnswer   = "answer"   // the worker's question: the answer line
	actLine     = "line"     // a stalled or stopped worker: the line
	actDrive    = "drive"    // a driving task: the line to the drive agent
	actSendback = "sendback" // the lead's sendback: the next attempt, its fix job the text
)

// stepAnswer is an older log's answer verdict step (answer.<call id>, before T-511's answered fact): it folds as it
// did, a fix job of the next attempt.
const stepAnswer = "answer"

// resumeDoes is what obor resume does by action act, for its refusals and the needs-you lines.
func resumeDoes(act string) string {
	return map[string]string{
		actAnswer:           "sends the worker the answer",
		actLine:             "sends the worker the line",
		actDrive:            "sends the drive agent the line",
		actSendback:         "sends it back to its worker",
		wire.ResumeRegate:   "gates it again",
		wire.ResumeReprompt: "sends the integrator its job anew",
		wire.ResumeRetry:    "retries the failed step",
	}[act]
}

func resumeReply(task, to, cause string) string {
	return fmt.Sprintf("RESUMED %s: %s (%s)", task, to, Dash(cause))
}

func answerReply(task, agent string) string {
	return fmt.Sprintf("ANSWERED %s: obor serve sends %s the answer", task, agent)
}

// resume answers obor resume (under a.verdicts, then a.submitMu: task_done's and land's lock, in their order):
// resumeDecide's refusal or stored reply, else its action's one fact. Every action records a fact, so the decision is
// taken under both locks (RV-99 #4): a task that moved meanwhile (a task_done decided before it: submitted) is refused
// by its state as it is now, nothing recorded, and a task_done waiting on the lock decides on the state the resume
// left (a --regate's submitted takes no submission), so a late submission never brings a gated task back.
func (a *App) resume(c wire.Call) (string, error) {
	a.verdicts.Lock()
	defer a.verdicts.Unlock()
	a.hook(hookResumeWaits)
	a.submitMu.Lock()
	defer a.submitMu.Unlock()
	a.hook(hookResumeLocked)
	if c.CallID == "" || c.Task == "" {
		return "", errors.New("resume: a call id and a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	act, mt, reply, err := a.resumeDecide(p, c)
	if err == nil && reply == "" && act == actSendback {
		p.held[c.Task] = actSendback // no step of the old attempt starts until the sendback has folded
	}
	a.mu.Unlock()
	if err != nil || reply != "" {
		return reply, err
	}
	c.Note = strings.TrimSpace(c.Note)
	switch act {
	case actSendback:
		return a.leadSendback(p, c, mt)
	case actAnswer, actLine, actDrive:
		return a.resumeLine(p, c, mt, act)
	}
	d := mt.needs.fact.Fact()
	if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepUnblocked, Kind: event.KindFact},
		event.FactData{Lane: mt.lane, Machine: Machine, Agent: c.Agent, Reason: act, Merge: true, Cause: d.Cause, Call: c.CallID,
			Note: c.Note}) {
		return "", fmt.Errorf("resume: the event log of %s refused it", p.name)
	}
	a.wake()
	return resumeReply(c.Task, act, d.Cause), nil
}

// resumeDecide is resume's decision (under mu): the action and the task as it is now, or the stored reply of the call
// id, or the refusal: the merge loop off, a caller of another role, the task not open, busy, no action that fits
// (resumeAct), no text where its action needs one.
func (a *App) resumeDecide(p *proj, c wire.Call) (string, mtask, string, error) {
	if p == nil {
		return "", mtask{}, "", fmt.Errorf("resume: no served project at %s", c.Root)
	}
	for _, m := range []string{wire.MethodResume, wire.MethodSendback} { // a sendback's reply is stored under its own step
		if reply, stored := p.folded.calls[verdictStep(m, c.CallID)]; stored {
			return "", mtask{}, reply, nil
		}
	}
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	switch {
	case !p.merging:
		return "", mtask{}, "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", mtask{}, "", fmt.Errorf("resume: the lead or the owner resumes a task, not %s (role %s)", Dash(c.Agent), c.Role)
	case mt.task == "":
		return "", mtask{}, "", fmt.Errorf("resume: %s is not open in obor serve", c.Task)
	}
	if why := p.busy(c.Task); why != "" {
		return "", mtask{}, "", busyErr("resume", c.Task, why)
	}
	act, err := p.resumeAct(mt, c.To)
	if err != nil {
		return "", mtask{}, "", err
	}
	if strings.TrimSpace(c.Note) == "" && p.resumeNeedsText(mt, act) {
		return "", mtask{}, "", fmt.Errorf("resume %s: say the text: obor resume %s \"<text>\" %s", c.Task, c.Task,
			resumeDoes(act))
	}
	return act, mt, "", nil
}

// resumeNeedsText: obor resume's act on mt needs a text: an answer, a line, a sendback (its why), a hold's decision.
func (p *proj) resumeNeedsText(mt mtask, act string) bool {
	switch act {
	case actAnswer, actLine, actDrive, actSendback:
		return true
	}
	return act == wire.ResumeReprompt && mt.needs.fact.Fact().Cause == event.CauseHeld
}

// resumeAct is the action obor resume takes on mt with flag ("", regate or sendback) now (under mu), or the refusal: a
// state that takes no resume, nothing to resume, a flag that does not fit (naming what would happen), no flag where the
// owner chooses (naming both). resumeFits says which actions fit.
func (p *proj) resumeAct(mt mtask, flag string) (string, error) {
	t := mt.task
	if flag != "" && flag != wire.ResumeRegate && flag != wire.ResumeSendback {
		return "", fmt.Errorf("resume: --%s is no flag of obor resume (--regate or --sendback)", flag)
	}
	def, choice, err := p.resumeFits(mt)
	switch {
	case err != nil:
		return "", err
	case flag == "" && def != "":
		return def, nil
	case flag != "" && (flag == def || slices.Contains(choice, flag)):
		return flag, nil
	case flag == "":
		return "", fmt.Errorf("resume %s: say %s (%s)", t, flags(choice), mt.needs.fact.Fact().Reason)
	case def == "":
		return "", fmt.Errorf("resume %s: --%s does not fit (%s): say %s", t, flag, mt.needs.fact.Fact().Reason, flags(choice))
	}
	return "", fmt.Errorf("resume %s: --%s does not fit: obor resume %s %s", t, flag, t, resumeDoes(def))
}

// flags is the choice as obor resume's flags: "--regate or --sendback".
func flags(choice []string) string {
	out := make([]string, len(choice))
	for i, c := range choice {
		out[i] = "--" + c
	}
	return strings.Join(out, " or ")
}

// resumeFits is what obor resume may do with mt now (under mu): its default action, the flags that choose another
// (or, with no default, the owner's choice between them), or why nothing fits.
func (p *proj) resumeFits(mt mtask) (def string, choice []string, err error) {
	t, st := mt.task, mt.shown()
	if !mt.may(vResume) {
		if mt.needs.due() || states[mt.state].pick != nil && !states[mt.state].ended {
			return "", nil, fmt.Errorf("resume: %s is %s; retry when it settles", t, st)
		}
		return "", nil, fmt.Errorf("resume: %s is %s", t, st)
	}
	working := mt.is(stWorking) || mt.is(stDispatched)
	switch {
	case mt.drive && mt.is(stDriving):
		return actDrive, nil, nil
	case working && p.asks(t):
		return actAnswer, []string{wire.ResumeRegate}, nil // a Blocked "re-gate, not my diff": --regate
	case working && (p.has(t, event.StepNeedsYou, reasonStalled) || p.stops(t)):
		return actLine, nil, nil
	case mt.is(stGreen) || mt.is(stAwaiting) || mt.is(stResolving): // a drive's goes back to driving (foldSendback)
		return actSendback, nil, nil
	case !mt.needs.held() && mt.drive:
		return "", nil, errors.New("resume: " + driveAnswerable(mt))
	case !mt.needs.held():
		return "", nil, fmt.Errorf("resume: %s is %s with nothing to resume (no question, no needs-you)", t, st)
	}
	d := mt.needs.fact.Fact()
	rule := mt.resumeRule(d.Cause)
	to := cmp.Or(rule.to, resumeDefaultOf(d.Cause))
	switch {
	case rule.closed:
		return "", nil, fmt.Errorf("resume: %s is rejected (%s): obor drop %s frees its lane", t, d.Reason, t)
	case to != "":
		if _, err := mt.resumeTo(t, to); err != nil {
			return "", nil, err
		}
		return to, nil, nil
	}
	if _, err := mt.resumeTo(t, wire.ResumeRegate); err == nil {
		choice = append(choice, wire.ResumeRegate)
	}
	if mt.lane != "" && mt.agent != "" { // a sendback needs a worker to send the fix job to
		choice = append(choice, wire.ResumeSendback)
	}
	if len(choice) == 0 {
		return "", nil, fmt.Errorf("resume: %s has nothing obor resume can do (%s): obor drop it", t, d.Reason)
	}
	return "", choice, nil
}

// resumeTo is the target obor resume takes for task's needs-you in mt with to, or the refusal of mt's state: a
// needs-you in force that is not a reject's, and a target that fits it (reprompt needs an integrator job of the
// attempt, retry a failed step). A question's --regate (no needs-you) gates the lane as it is.
func (mt mtask) resumeTo(task, to string) (string, error) {
	d := mt.needs.fact.Fact()
	rule := mt.resumeRule(d.Cause) // by its phase: a dispatch's step runs again, an undelivered fix job is sent anew
	switch {
	case !mt.needs.held():
		return "", fmt.Errorf("resume: %s has no needs-you of the merge loop", task)
	case rule.closed:
		return "", fmt.Errorf("resume: %s is rejected (%s): obor drop %s frees its lane", task, d.Reason, task)
	case rule.only != "" && to != rule.only:
		return "", fmt.Errorf("resume: %s left its dispatch (%s): obor resume %s retries it", task, d.Reason, task)
	case to == wire.ResumeRetry && !strings.HasSuffix(d.Cause, failedSuffix) && !rule.retry:
		return "", fmt.Errorf("resume: %s has no failed step to retry (%s)", task, d.Reason)
	case to == wire.ResumeReprompt && (rule.noReprompt || !mt.repromptable()):
		return "", fmt.Errorf("resume: %s has no integrator job to re-prompt (%s): --regate", task, d.Reason)
	}
	return to, nil
}

// repromptable: the attempt has an integrator job to send anew: its last one, or the review job of a green candidate a
// needs-you held before it was sent.
func (mt mtask) repromptable() bool { return mt.job != "" || mt.state == stGreen }

// resumeLine records obor resume's line (an answer, a stalled or stopped worker's line, a drive's line): fact
// answered{to: the worker or the drive agent, line "[obor answer] <text>"}, whose fold ends the task's question and
// watchdog facts (answered) and whose line deliver sends at the agent's next idle; a drive's lines are each their own
// (a newer one ends no older), and a land or a drop closes an undelivered one unsent.
func (a *App) resumeLine(p *proj, c wire.Call, mt mtask, act string) (string, error) {
	to := mt.agent
	a.mu.Lock()
	if w := p.tasks[c.Task]; w != nil && to == "" {
		to = w.lane.Agent
	}
	a.mu.Unlock()
	if to == "" {
		return "", fmt.Errorf("resume: %s has no worker to send the line to", c.Task)
	}
	m := event.Message{To: to, From: cmp.Or(c.Agent, "owner"), Line: "[obor answer] " + c.Note, Call: c.CallID,
		Task: c.Task, Driver: act == actDrive, Reply: answerReply(c.Task, to)}
	if act == actLine {
		m.Reply = resumeReply(c.Task, wire.ResumeReprompt, reasonStalled)
		if p.stops(c.Task) {
			m.Reply = resumeReply(c.Task, wire.ResumeReprompt, causeStopped)
		}
	}
	if note := a.unnamed(p, to, ""); note != "" && act != actLine {
		m.Reply += " (" + note + ")"
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("resume: the event log of %s refused it", p.name)
	}
	a.logf("%s %s by %s", p.name, m.Reply, Dash(c.Agent))
	a.wake()
	return m.Reply, nil
}

// leadSendback records the lead's or the owner's sendback of a task (obor resume's actSendback; c.Note: why; RV-45 #1),
// under a.verdicts, the task held (p.held) since the decision: the integrator's sendback transition, from a green
// candidate, an open job (awaiting-accept, resolving) or a needs-you of the owner's choice. The fold ends the job, the
// candidate and the needs-you and starts the next attempt; the merge loop sends the worker its fix job. K is the
// integrator's bound, not the lead's.
func (a *App) leadSendback(p *proj, c wire.Call, mt mtask) (string, error) {
	defer a.release(p, c.Task, actSendback)
	method := wire.MethodSendback
	job := ""
	reply := fmt.Sprintf("SENDBACK %s (attempt %d, %s)", c.Task, mt.attempt, mt.shown())
	if mt.openJob() {
		job = JobID(mt.jobKey())
		reply = fmt.Sprintf("SENDBACK %s (job %s)", c.Task, job)
	}
	in := event.Intent{Key: event.Key{Task: c.Task, Attempt: mt.attempt, Step: verdictStep(method, c.CallID)},
		Class: event.ClassFact, Data: verdictData{Job: job, Caller: c.Agent, Note: c.Note}}
	a.mu.Lock()
	r, err := a.verdictRecord(p, in, verdictResult{Job: job, Reply: reply, Note: c.Note, Lead: true})
	if err != nil {
		a.mu.Unlock()
		return "", err
	}
	var v verdictResult
	_ = json.Unmarshal(r.Data, &v)
	p.foldRecord(r) // the next attempt's begun
	a.mu.Unlock()
	a.logf("%s %s by %s", p.name, v.Reply, Dash(c.Agent))
	a.wake()
	return v.Reply, nil
}

// driveAnswerable is why a drive cannot take the lead's line now (under mu; "": it can): only while it is driving; a
// drive that is dispatched (not yet driving) or landed (submitted, a gate, a review, a needs-you) has no line path
// until the merge loop sends it back to driving. obor resume and obor tell-lead share it (T-764): the reply tell-lead
// advertises must be one the drive can get.
func driveAnswerable(mt mtask) string {
	if mt.is(stDriving) {
		return ""
	}
	return fmt.Sprintf("%s is %s (landed: gate, review and merge follow; the lead cannot answer a drive until it is "+
		"driving again)", mt.task, mt.shown())
}

// answered ends task's question (an answer's fold): all its watchdog facts end, the question with them, and the stall
// timer starts afresh (a reset event: the time the worker waited on its question is no stall).
func (p *proj) answered(task string) {
	p.clear(task)
	if w := p.tasks[task]; w != nil {
		w.quietSince, w.seenWrite = time.Time{}, time.Time{}
	}
}
