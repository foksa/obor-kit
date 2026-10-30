package app

import (
	"fmt"
	"strings"
)

// The merge loop's states as one table (docs/state-model.md "States" and "Transitions": the code mirrors it, row by
// row). A state's row says what each function decides for it: pick its next step, stepName that step's name, verdict
// the integrator's verdicts it takes, unsendable whether the lead may send it back, resume what obor resume does with a
// needs-you that left it, scratchBusy whether it holds the scratch, drive whether a merge outside the app may have
// landed it, drop whether it ended already. A new state is one row here (and one test, TestState<Name>Row<N>).
type stateRow struct {
	pick     func(a *App, p *proj, mt mtask) step // its next step (under mu); nil, or no pick: it waits
	name     func(mt mtask) string                // the step pick selects, for a refused drop's reply (nil: "a step (<state>)")
	verdicts []verdictRule                        // the integrator's verdicts on the job it has out, in the order a refusal names them
	send     sendRule                             // the lead's sendback (unsendable)
	resume   func(mt mtask, cause string) resumeRule
	scratch  bool // its conflict is open in the scratch: no other task is gated (scratchBusy)
	landed   bool // a merge outside the app (by hand in git) may have landed it: drive checks before its step (row 34)
	ended    bool // terminal: no drop
}

// verdictRule is one verdict a state takes and how it folds.
type verdictRule struct {
	method string
	fold   func(mt *mtask, v verdictResult)
}

// sendRule is whether the lead may send a task in a state back (mtask.unsendable).
type sendRule int

const (
	sendNo      sendRule = iota // never: "<T> is <state>"
	sendYes                     // a green candidate or an open job
	sendSettles                 // a state the loop leaves by itself: "retry when it settles"
	sendByCause                 // a needs-you: after a red gate or a hold only
)

// resumeRule is what obor resume may do with a needs-you that left a state (App.resume).
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
		stSubmitted: {pick: (*App).pickGate, name: gateName, send: sendSettles, landed: true},
		// rows 18–20: an accept bound to the patch is kept, else a review job
		stGreen: {pick: (*App).pickGreen, name: func(mt mtask) string { return nth(StepReview, mt.reviews+1) },
			send: sendYes, landed: true},
		// rows 19, 25–29, 31: the review job is out
		stAwaiting: {pick: (*App).pickJob, name: jobName, send: sendYes, landed: true, verdicts: []verdictRule{
			{MethodAccept, func(mt *mtask, v verdictResult) { mt.accepted, mt.state = v.Patch, stAccepted }},
			{MethodSendback, foldSendback},
			{MethodReject, func(mt *mtask, v verdictResult) { mt.state, mt.line = stRejected, v.Note }},
		}},
		// rows 21–22: a sendback, or the owner after K
		stRed: {pick: (*App).pickRed, send: sendSettles, landed: true},
		// rows 18, 23: a resolve job
		stConflict: {pick: (*App).pickConflict, name: func(mt mtask) string { return nth(StepResolve, mt.resolves+1) },
			send: sendSettles, scratch: true, landed: true},
		// rows 23, 25, 30–31: the resolve job is out
		stResolving: {pick: (*App).pickJob, name: jobName, send: sendYes, scratch: true, landed: true, verdicts: []verdictRule{
			{MethodResolved, func(mt *mtask, v verdictResult) { mt.state, mt.resolved = stResolved, v.SHA }},
			{MethodReject, func(mt *mtask, v verdictResult) {
				mt.state, mt.why, mt.cause = stFail, "resolve rejected: "+v.Note, causeResolveRejected
			}},
		}},
		// rows 30, 18: gate the merge the integrator committed in the scratch (it holds the scratch itself)
		stResolved: {pick: (*App).pickGate, name: gateName, send: sendSettles, scratch: true},
		// row 22: the owner decides (unless a merge outside the app took the lane meanwhile)
		stFail: {pick: (*App).pickFail, send: sendSettles, landed: true},
		// rows 20, 26, 33, 35: publish it
		stAccepted: {pick: (*App).pickAccepted, name: func(mt mtask) string { return nth(StepPublish, mt.pubs+1) },
			send: sendSettles},
		// rows 32, 18: gate the candidate again on the new target
		stMoved: {pick: (*App).pickGate, name: gateName, send: sendSettles},
		// rows 33–34: the target holds the candidate
		stPublished: {pick: (*App).pickPublished, send: sendSettles},
		// rows 34, 37–38: the after-merge steps; a needs-you of one has no job to re-prompt
		stMerged: {pick: (*App).pickMerged, ended: true,
			resume: func(mtask, string) resumeRule { return resumeRule{noReprompt: true} }},
		// rows 29, 40: unpin, then the owner frees the lane (no resume of the reject's needs-you)
		stRejected: {pick: (*App).pickRejected, ended: true,
			resume: func(_ mtask, cause string) resumeRule { return resumeRule{closed: cause == causeRejected} }},
		// row 44: terminal
		stDropped: {ended: true},
		// row 2a: waiting for the tasks its dispatch names to merge, then dispatched (row 2); a needs-you (one of them
		// dropped or rejected) is retried: it waits again
		stWaiting: {pick: (*App).pickWaiting, name: func(mtask) string { return "the dispatch" },
			resume: func(mtask, string) resumeRule { return resumeRule{to: ResumeRetry, only: ResumeRetry, retry: true} }},
		// rows 2–9: the dispatch's steps; a needs-you of one is retried (row 40)
		stDispatched: {pick: (*App).nextDispatch, name: func(mtask) string { return "the dispatch" },
			resume: func(mtask, string) resumeRule { return resumeRule{to: ResumeRetry, only: ResumeRetry, retry: true} }},
		// rows 6, 10–16, 21, 24: the fix job (a dispatched attempt: its task_done submits it); an undelivered fix job
		// is sent anew by retry (row 40)
		stWorking: {pick: (*App).pickWorking, name: func(mtask) string { return "the fix" }, landed: true,
			resume: func(mt mtask, cause string) resumeRule {
				if mt.work == "" && cause == causeUndelivered {
					return resumeRule{to: ResumeRetry, retry: true}
				}
				return resumeRule{}
			}},
		// row 40: the owner decides; the state it left is kept (mtask.kept)
		stNeedsYou: {send: sendByCause, landed: true},
	}
}

// gateName names a gate state's step: submit first, then the attempt's next gate.
func gateName(mt mtask) string {
	if mt.state == stSubmitted && mt.input == "" {
		return StepSubmit
	}
	return nth(StepGate, mt.gates+1)
}

// jobName names an open job's step: the job itself.
func jobName(mt mtask) string { return mt.job }

// foldSendback folds the integrator's sendback: the next attempt, or the owner past K (row 22).
func foldSendback(mt *mtask, v verdictResult) {
	if mt.sendbacks < K {
		mt.sendback(v.Note)
		return
	}
	mt.state, mt.why, mt.cause = stFail, fmt.Sprintf("sent back %d times, the integrator would again: %s", K, v.Note),
		causeSendbacks
}

// kept is the state mt's needs-you left (needs-you), else its state.
func (mt *mtask) kept() string {
	if mt.state == stNeedsYou {
		return mt.left
	}
	return mt.state
}

// resumeRule is obor resume's rule for mt's needs-you with cause.
func (mt mtask) resumeRule(cause string) resumeRule {
	if f := states[mt.kept()].resume; f != nil {
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
