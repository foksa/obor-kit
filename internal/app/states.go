package app

import (
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
)

// The merge loop's states as one table (docs/state-model.md "States" and "Transitions": the code mirrors it, row by
// row). A state's row says what each function decides for it: pick its next step, stepName that step's name, verdict
// the integrator's verdicts it takes, verbs which of the lead's task verbs it takes (mtask.may), resume what obor
// resume does with a needs-you on it, scratch whether it holds the scratch, uses whether its step needs it,
// landed whether a merge outside the app may have landed it, ended whether it ended already, failed whether it ended
// without a merge, phase what obor status and web show for it (phaseOf adds the live overlays). A needs-you is no
// state: a block on one (mtask.needs, block.go), which pick, may and phase see first. A new state is one row here (and
// one test, TestState<Name>Row<N>).
type stateRow struct {
	pick     func(a *App, p *proj, mt mtask) step // its next step (under mu); nil, or no pick: it waits
	name     func(mt mtask) string                // the step pick selects, for a refused drop's reply (nil: "a step (<state>)")
	verdicts []verdictRule                        // the integrator's verdicts on the job it has out, in the order a refusal names them
	verbs    verb                                 // the task verbs it takes (mtask.may; each also needs proj.busy "")
	resume   func(mt mtask, cause string) resumeRule
	scratch  bool   // its merge is open in the scratch: no other task is gated (scratchHolder)
	uses     string // its step needs the scratch: ScratchGating (a gate) or ScratchPublishing ("": none; scratchWait)
	landed   bool   // a merge outside the app (by hand in git) may have landed it: drive checks before its step (row 34)
	ended    bool   // terminal: no drop
	failed   bool   // ended without a merge: it never merges (a task waiting for it waits in vain)
	phase    string // what obor status, the view and web show for it with no live overlay (wire.Phase*; phaseOf)
}

// verdictRule is one verdict a state takes and how it folds.
type verdictRule struct {
	method string
	fold   func(mt *mtask, v verdictResult)
}

// verb is one of the task verbs of the lead, the owner or the worker (T-775): a state's row says which it takes, and
// each verb is mt.may(v) && p.busy(task) == "", with its own refusal text.
type verb uint8

const (
	vResume verb = 1 << iota // obor resume: the lead's word to the task (resumeAct says which action)
	vDrop                    // obor drop
	vDrive                   // obor drive <T-xx>: a take-over (a drive already is driveAgain's)
	vLand                    // obor land
	vMove                    // obor dispatch <T-xx> --model (move.go)
	vSubmit                  // task_done
)

// workVerbs are the verbs of a task its worker works on (dispatched, working); drive, move and submit also need a
// dispatch's lane prepared (mt.base).
const workVerbs = vResume | vDrop | vDrive | vMove | vSubmit

// may: mt's state takes v. A block decides first (one rule for every verb): a due one takes only a drop (its
// needs-you is recorded on the next pass); a held one takes obor resume, a drop and a task_done (the next attempt), and
// a take-over when its phase takes one (a worker's needs-you: obor drive fixes it by hand). A merged or rejected
// task's needs-you leaves its row's verbs as they are.
func (mt mtask) may(v verb) bool {
	vs := states[mt.state].verbs
	switch {
	case mt.needs.due():
		vs = vDrop
	case mt.paused() && mt.state != stDropping: // a dropping task takes no task_done, blocked or not (T-852)
		vs = vResume | vDrop | vSubmit | vs&vDrive
	}
	return vs&v != 0
}

// resumeRule is what obor resume may do with a needs-you on a state (App.resume).
type resumeRule struct {
	to         string // the default target ("": the cause's, resumeDefaultOf)
	only       string // the one target that fits ("": any the cause allows)
	retry      bool   // retry fits whatever the cause (else only a failed step's)
	noReprompt bool   // no integrator job to re-prompt, whatever the attempt had
	closed     bool   // no resume at all: the owner frees the lane
}

// states is the table, one row per state of internal/app/loop.go (set in init: its picks read it).
var states map[string]stateRow

func init() {
	states = map[string]stateRow{
		// rows 16, 18, 34: submit the lane's commit, then gate it (a conflict open in the scratch: wait)
		stSubmitted: {pick: (*App).pickGate, name: gateName, verbs: vDrop, landed: true, uses: wire.ScratchGating,
			phase: wire.PhaseSubmitted},
		// rows 18–20: an accept bound to the patch is kept, else a review job
		stGreen: {pick: (*App).pickGreen, name: func(mt mtask) string { return nth(event.StepReview, mt.reviews+1) },
			verbs: vResume | vDrop, landed: true, phase: wire.PhaseReview},
		// rows 19, 25–29, 31: the review job is out
		stAwaiting: {pick: (*App).pickJob, name: jobName, verbs: vResume | vDrop, landed: true, phase: wire.PhaseReviewing,
			verdicts: []verdictRule{
				{wire.MethodAccept, func(mt *mtask, v verdictResult) { mt.accepted, mt.state = v.Patch, stAccepted }},
				{wire.MethodSendback, foldSendback},
				{wire.MethodReject, func(mt *mtask, v verdictResult) { mt.state, mt.line = stRejected, v.Note }},
			}},
		// rows 21–22: a sendback, or the owner after K
		stRed: {pick: (*App).pickRed, verbs: vDrop, landed: true, phase: wire.PhaseGating},
		// rows 18, 23: a resolve job
		stConflict: {pick: (*App).pickConflict, name: func(mt mtask) string { return nth(event.StepResolve, mt.resolves+1) },
			verbs: vDrop, scratch: true, landed: true, phase: wire.PhaseReview},
		// rows 23, 25, 30–31: the resolve job is out
		stResolving: {pick: (*App).pickJob, name: jobName, verbs: vResume | vDrop, scratch: true, landed: true,
			phase: wire.PhaseReviewing, verdicts: []verdictRule{
				{wire.MethodResolved, func(mt *mtask, v verdictResult) { mt.state, mt.resolved = stResolved, v.SHA }},
				{wire.MethodReject, func(mt *mtask, v verdictResult) {
					mt.block(block{cause: causeResolveRejected, why: "resolve rejected: " + v.Note})
				}},
			}},
		// rows 30, 18: gate the merge the integrator committed in the scratch (it holds the scratch itself)
		stResolved: {pick: (*App).pickGate, name: gateName, verbs: vDrop, scratch: true, uses: wire.ScratchGating,
			phase: wire.PhaseGating},
		// rows 20, 26, 33, 35: publish it
		stAccepted: {pick: (*App).pickAccepted, name: func(mt mtask) string { return nth(event.StepPublish, mt.pubs+1) },
			verbs: vDrop, uses: wire.ScratchPublishing, phase: wire.PhasePublish},
		// rows 32, 18: gate the candidate again on the new target
		stMoved: {pick: (*App).pickGate, name: gateName, verbs: vDrop, uses: wire.ScratchGating, phase: wire.PhaseGating},
		// rows 33–34: the target holds the candidate
		stPublished: {pick: (*App).pickPublished, verbs: vDrop, phase: wire.PhasePublish},
		// rows 34, 37–38: the after-merge steps; a needs-you of one has no job to re-prompt
		stMerged: {pick: (*App).pickMerged, ended: true, verbs: vResume, phase: wire.PhaseMerged,
			resume: func(mtask, string) resumeRule { return resumeRule{noReprompt: true} }},
		// rows 29, 40: unpin, then the owner frees the lane (no resume of the reject's needs-you)
		stRejected: {pick: (*App).pickRejected, ended: true, failed: true, verbs: vResume | vDrop,
			phase:  wire.PhaseRejected,
			resume: func(_ mtask, cause string) resumeRule { return resumeRule{closed: cause == causeRejected} }},
		// row 44: terminal
		stDropped: {ended: true, failed: true, phase: wire.PhaseDropped},
		// row 2a: waiting for the tasks its dispatch names to merge, then dispatched (row 2); a needs-you (one of them
		// dropped or rejected) is retried: it waits again
		stWaiting: {pick: (*App).pickWaiting, name: func(mtask) string { return "the dispatch" }, verbs: vResume | vDrop,
			phase: wire.PhaseWaiting,
			resume: func(mtask, string) resumeRule {
				return resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}
			}},
		// rows 2–9: the dispatch's steps; a needs-you of one is retried (row 40)
		stDispatched: {pick: (*App).nextDispatch, name: func(mtask) string { return "the dispatch" }, verbs: workVerbs,
			phase: wire.PhaseDispatching,
			resume: func(mtask, string) resumeRule {
				return resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}
			}},
		// rows 6, 10–16, 21, 24: the fix job (a dispatched attempt: its task_done submits it); an undelivered fix job
		// is sent anew by retry (row 40)
		stWorking: {pick: (*App).pickWorking, name: func(mtask) string { return "the fix" }, landed: true, verbs: workVerbs,
			phase: wire.PhaseWorking,
			resume: func(mt mtask, cause string) resumeRule {
				if mt.work == "" && cause == causeUndelivered {
					return resumeRule{to: wire.ResumeRetry, retry: true}
				}
				return resumeRule{}
			}},
		// T-751: the owner's drive agent works in the lane; obor land submits it. Its one step is the lead's line when a
		// red gate, a sendback or a hold sent it back; no jobs, no watchdog; obor resume is a line to the drive agent
		stDriving: {pick: (*App).pickDriving, name: func(mtask) string { return "the drive" }, verbs: vResume | vDrop | vLand,
			phase:  wire.PhaseDriving,
			resume: func(_ mtask, cause string) resumeRule { return resumeRule{retry: cause == causeStop} }},
		// T-852: obor drop decided on a task holding a lane: its worker's stop, lane.free, then dropped; a needs-you of
		// one (a refused stop, a refused free) is retried, or the drop given again (--force, --discard)
		stDropping: {pick: (*App).pickDropping, name: func(mtask) string { return "the drop" }, verbs: vResume | vDrop,
			failed: true, phase: wire.PhaseDropping,
			resume: func(mtask, string) resumeRule {
				return resumeRule{to: wire.ResumeRetry, only: wire.ResumeRetry, retry: true}
			}},
	}
}

// phaseOf is mt's phase now (under mu; T-788), the one place obor status, the view and web get it from: phase of its
// live overlays — a watchdog's needs-you on its task or its lane (blocked), its gate or publish running or pending
// (steps: stepPhases), the role holding its merge.
func (a *App) phaseOf(p *proj, mt *mtask, steps map[string]string) string {
	return phase(mt, overlays{blocked: p.blocked(mt), step: steps[mt.task], hold: a.holdRole(p, mt)})
}

// stageOf is mt's merge-loop stage ("": not in the merge loop), from its state, its steps and the merge hold, and the
// merge loop's needs-you (a block on a merge-loop state: held, red past K); a watchdog's needs-you on a working or dispatching task is not the merge loop's (T-797).
func (a *App) stageOf(p *proj, mt *mtask, steps map[string]string) string {
	if mt.paused() && (mt.state == stWaiting || mt.state == stDispatched || mt.state == stWorking) {
		return "" // a needs-you its dispatch or worker left (redoable): the worker's, never the merge loop's
	}
	return wire.PhaseStage(phase(mt, overlays{step: steps[mt.task], hold: a.holdRole(p, mt)}))
}

// overlays are the live facts on a task its state does not say: a watchdog's needs-you in force (blocked), the phase
// its gate or publish running or pending gives it (step: gating, merging, gate pending, publish pending; "": none),
// the role holding its merge (hold, T-718; "": none).
type overlays struct {
	blocked    bool
	step, hold string
}

// phase is mt's phase with the overlays o: a needs-you first — the merge loop's block (held: the integrator's hold
// verdict alone, C7) or a watchdog's — then its step running or pending, then the merge hold, else its state's row.
func phase(mt *mtask, o overlays) string {
	switch {
	case mt.onHold() && mt.needs.fact.Fact().Cause == event.CauseHeld:
		return wire.PhaseHeld
	case mt.paused() || o.blocked:
		return wire.PhaseNeedsYou
	case o.step != "":
		return o.step
	case o.hold != "":
		return wire.MergeHold(o.hold)
	}
	return states[mt.state].phase
}

// blocked: a watchdog's needs-you is in force for mt's task or its lane (a stalled worker, a stop, a question).
func (p *proj) blocked(mt *mtask) bool {
	for task, fs := range p.folded.active {
		for _, f := range fs {
			if f.Step == event.StepNeedsYou && (task == mt.task || mt.lane != "" && f.Fact().Lane == mt.lane) {
				return true
			}
		}
	}
	return false
}

// stepPhases is, by task, the phase its open gate or publish gives it (open: the log's open intents): gating or
// merging while its effect runs, else gate pending or publish pending (T-662: a failed step, a replay to run).
func (p *proj) stepPhases(open []event.Record) map[string]string {
	out := map[string]string{}
	for _, r := range open {
		step, task, ok := commandStep(r)
		if !ok || task == "" || step != event.StepGate && step != event.StepPublish {
			continue
		}
		_, running := p.exec[r.Key()]
		switch {
		case running && step == event.StepGate:
			out[task] = wire.PhaseGating
		case running:
			out[task] = wire.PhaseMerging
		case out[task] == "" && step == event.StepGate:
			out[task] = wire.PhaseGatePending
		case out[task] == "":
			out[task] = wire.PhasePublishPending
		}
	}
	return out
}

// busy is why no task verb may act on task now ("": one may): the one reader of the live claims on it. A drop of it is
// being recorded (p.ended), a step or a decision holds it (p.held: claim), its scratch step runs (p.running), or its
// attempt's next gate or publish intent is open (a step a restart replays; not a needs-you's, which runs no step). Each verb refuses with "<verb> <T>: <busy>;
// retry when it settles" (busyErr).
func (p *proj) busy(task string) string {
	if why := p.claim(task); why != "" {
		return why
	}
	if p.running.Task == task {
		return fmt.Sprintf("%s has %s running", task, p.running.Step)
	}
	var mt mtask
	if m := p.folded.loop[task]; m != nil {
		mt = *m
	}
	if mt.onHold() { // it runs no step: an open intent is the failed step's, which obor resume retries
		return ""
	}
	for _, step := range []string{nth(event.StepGate, mt.gates+1), nth(event.StepPublish, mt.pubs+1)} {
		if p.log.Open(event.Key{Task: task, Attempt: mt.attempt, Step: step}) {
			return fmt.Sprintf("%s has %s pending", task, step)
		}
	}
	return ""
}

// claim is busy's half a selected step's start (begin) reads too: a drop being recorded, or a step or a decision
// holding the task ("": none). A step's own pending intent is no claim on it: the step replays it.
func (p *proj) claim(task string) string {
	switch {
	case p.ended[task]:
		return task + " has a drop being recorded"
	case p.held[task] != "":
		return fmt.Sprintf("%s has %s running", task, p.held[task])
	}
	return ""
}

// busyErr is verb's refusal of task for busy's why.
func busyErr(verb, task, why string) error {
	return fmt.Errorf("%s %s: %s; retry when it settles", verb, task, why)
}

// gateName names a gate state's step: submit first, then the attempt's next gate.
func gateName(mt mtask) string {
	if mt.state == stSubmitted && mt.input == "" {
		return event.StepSubmit
	}
	return nth(event.StepGate, mt.gates+1)
}

// jobName names an open job's step: the job itself.
func jobName(mt mtask) string { return mt.job }

// foldSendback folds the integrator's sendback: the next attempt, or the owner past K (row 22). A drive goes back to
// driving whatever its count (T-751): its owner fixes it.
func foldSendback(mt *mtask, v verdictResult) {
	if mt.sendbacks < K || mt.drive {
		mt.sendback(v.Note)
		return
	}
	mt.block(block{cause: causeSendbacks, why: fmt.Sprintf("sent back %d times, the integrator would again: %s", K, v.Note)})
}

// resumeRule is obor resume's rule for mt's needs-you with cause, by its phase.
func (mt mtask) resumeRule(cause string) resumeRule {
	if f := states[mt.state].resume; f != nil {
		return f(mt, cause)
	}
	return resumeRule{}
}

// accepts is the verdicts a job's state takes, for a refusal: "accept, sendback or reject".
func accepts(st string) string {
	var ms []string
	for _, r := range states[st].verdicts {
		ms = append(ms, r.method)
	}
	if len(ms) < 2 {
		return strings.Join(ms, "")
	}
	return strings.Join(ms[:len(ms)-1], ", ") + " or " + ms[len(ms)-1]
}

// takes: st takes the verdict method.
func takes(st, method string) bool {
	for _, r := range states[st].verdicts {
		if r.method == method {
			return true
		}
	}
	return false
}
