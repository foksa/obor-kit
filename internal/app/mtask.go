// mtask.go: folding the merge loop's records into per-task state (mtask) and its accessors.
package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// foldLoop folds r into p's merge-loop tasks.
func (p *proj) foldLoop(r Record) {
	mt := p.folded.loop[r.Task]
	if r.Kind == KindFact && r.Step == StepDispatched {
		p.foldDispatch(r)
		return
	}
	if r.Kind == KindFact && r.Step == StepWaiting {
		p.foldWaiting(r)
		return
	}
	// A dropped fact is terminal for the task whatever attempt it names (RV-42-verify-2 #1: a submission decided
	// before the drop may record the next attempt first): live fold and replay end the task, and every later record of
	// it (submitted, a step result, a verdict) is ignored; only a new dispatch reopens it.
	if r.Kind == KindFact && r.Step == StepDropped {
		if mt != nil {
			mt.state, mt.needs, mt.dropped = stDropped, Record{}, r.Fact().Reason
		}
		return
	}
	if r.Kind == KindFact && r.Step == StepSubmitted {
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		next := &mtask{task: r.Task, lane: d.Lane, agent: d.Agent, attempt: r.Attempt, state: stSubmitted,
			report: d.Report}
		if mt != nil && mt.state == stDropped {
			return // ended: a submission decided before the drop
		}
		if mt != nil {
			// The worker's spec rev and the lead's decision on its open questions outlive its attempt (T-611).
			next.sendbacks, next.specRev = mt.sendbacks, mt.specRev
			next.openDecision, next.openAt = mt.openDecision, mt.openAt
		}
		p.folded.loop[r.Task] = next
		return
	}
	if mt == nil || r.Attempt != mt.attempt || mt.state == stDropped {
		return // another attempt's record, or the task is dropped
	}
	step, n := stepN(r.Step)
	switch r.Kind {
	case KindFact:
		switch r.Step {
		case StepKept:
			if mt.accepted != "" && mt.accepted == mt.cand.Patch { // a green gate of a new spec rev ended the accept
				mt.state = stAccepted
			}
		case StepSendback:
			var d noteData
			_ = json.Unmarshal(r.Data, &d)
			mt.sendback(d.Note)
		case StepMoved:
			mt.state = stMoved
		case StepMerged:
			var d mergedData
			_ = json.Unmarshal(r.Data, &d)
			mt.state, mt.needs = stMerged, Record{}
			mt.head, mt.mergedSeq = d.Head, r.Seq
			if d.Outside {
				mt.after = len(afterSteps)
			}
		case StepFailed:
			if mt.fails == nil {
				mt.fails = map[string]int{}
			}
			mt.fails[r.Fact().Step]++
		case StepNeedsYou:
			if r.Fact().Merge {
				mt.needs = r
				if r.Fact().Cause == causeOpen {
					mt.heldQuestions = true
				}
				if mt.state != stNeedsYou {
					mt.left = mt.state
				}
				if mt.state != stRejected && mt.state != stMerged {
					mt.state = stNeedsYou
				}
			}
		case StepDelivered:
			if rev := r.Fact().Rev; rev > 0 {
				mt.specRev = rev
			}
			switch job := r.Fact().Job; {
			case mt.state == stDispatched && mt.work != "" && job == JobID(Key{mt.task, mt.attempt, mt.work}):
				mt.state = stWorking
			case mt.job != "" && job == JobID(mt.jobKey()):
				mt.delivered = true
			}
		case StepLost:
			if mt.job != "" && r.Fact().Job == JobID(mt.jobKey()) && (mt.state == stAwaiting || mt.state == stResolving) {
				mt.state = mt.jobState(false)
				mt.lost++
			}
		case StepUnblocked:
			mt.unblock(r.Fact())
		}
	case KindIntent:
		switch step {
		case StepGate:
			var d gateIn
			_ = json.Unmarshal(r.Data, &d)
			mt.kind = d.Kind
		case StepReview, StepResolve:
			var d JobData
			_ = json.Unmarshal(r.Data, &d)
			mt.job, mt.patch, mt.delivered = r.Step, d.Patch, false
			if step == StepReview {
				mt.reviews, mt.state = n, stAwaiting
			} else {
				mt.resolves, mt.state = n, stResolving
			}
		case StepFix:
			mt.fixed, mt.fixes = true, n
		case StepWork:
			mt.work, mt.works = r.Step, n
		}
	case KindResult:
		if method, _ := verdictMethod(r.Step); method != "" {
			var v verdictResult
			_ = json.Unmarshal(r.Data, &v)
			if mt.state == stNeedsYou && mt.job != "" && v.Job == JobID(mt.jobKey()) {
				mt.state, mt.needs = mt.jobState(true), Record{} // a verdict on a held (or late) job ends the needs-you
			}
			mt.verdict(method, v)
			return
		}
		var g gateOut
		_ = json.Unmarshal(r.Data, &g)
		switch step {
		case StepSubmit:
			var d submitOut
			_ = json.Unmarshal(r.Data, &d)
			mt.submits, mt.input = n, d.SHA
			if d.Refused != "" {
				mt.state, mt.why, mt.cause = stFail, "submit: "+d.Refused, causeGate
			}
		case StepGate:
			mt.gates, mt.line = n, g.Line
			switch g.Outcome {
			case outGreen:
				if g.SpecRev != mt.gateRev {
					mt.accepted = "" // the accept was of another spec rev: review again (RV-55 #1)
				}
				mt.cand, mt.gateRev, mt.state = g.Candidate, g.SpecRev, stGreen
			case outRed:
				mt.state = stRed
			case outConflict:
				mt.state = stConflict
			default:
				mt.state, mt.why, mt.cause = stFail, "gate: "+g.Line, causeGate
			}
		case StepPrepare:
			var d prepareOut
			_ = json.Unmarshal(r.Data, &d)
			mt.prepares, mt.base = n, d.Base
			if d.Refused != "" {
				mt.refused, mt.cause = "lane: "+d.Refused, causeLane
			}
		case StepStartWork:
			var d startOut
			_ = json.Unmarshal(r.Data, &d)
			mt.starts, mt.started = n, d.Refused == ""
			if d.Refused != "" {
				mt.refused, mt.cause = "start: "+d.Refused, causeStart
			} else {
				mt.startAt = recordTime(r)
			}
		case StepPublish:
			mt.pubs, mt.line = n, g.Line
			switch g.Outcome {
			case outMerged:
				mt.state = stPublished
			case outMoved:
				mt.state = stMoved
			default:
				mt.state, mt.why, mt.cause = stFail, "publish: "+g.Line, causePublish
			}
		default:
			if i := slices.Index(afterSteps, r.Step); i >= 0 {
				mt.after = max(mt.after, i+1)
			}
		}
	}
}

// The outcomes in a gate's or a publish's result (gateOut).
const (
	outGreen    = "green"
	outRed      = "red"
	outConflict = "conflict"
	outFail     = "fail"
	outMerged   = "merged"
	outMoved    = "moved"
	outHold     = "hold"
)

// sendback starts the next attempt: the worker fixes what note says.
func (mt *mtask) sendback(note string) {
	mt.sendbacks++
	mt.next(note, false)
}

// next starts mt's next attempt on its worker: working, the fix job (note its text; answer: the answer to its
// question, obor answer) to be sent.
func (mt *mtask) next(note string, answer bool) {
	mt.attempt++
	mt.state, mt.line, mt.fixed, mt.fixes, mt.fails, mt.work = stWorking, note, false, 0, nil, ""
	mt.answer, mt.decision, mt.heldQuestions = answer, "", false
	// mt.openDecision is kept: the lead's decision on the task's open questions outlives the attempt (T-611).
	mt.gates, mt.reviews, mt.resolves, mt.pubs, mt.submits, mt.input = 0, 0, 0, 0, 0, ""
	mt.cand, mt.gateRev, mt.accepted, mt.kind, mt.job, mt.patch, mt.delivered, mt.lost = Candidate{}, 0, "", "", "", "", false, 0
}

// recordTime is r's time stamp (zero when unreadable).
func recordTime(r Record) time.Time {
	t, _ := time.Parse(time.RFC3339, r.TS)
	return t
}

// jobKey is the key of the attempt's last integrator job.
func (mt *mtask) jobKey() Key { return Key{mt.task, mt.attempt, mt.job} }

// openJob: the attempt's last integrator job is open, the one a verdict answers (docs/state-model.md "Jobs"): it is
// out (awaiting-accept, resolving), or a needs-you left it out. A verdict result, job.lost, unblocked{reprompt|regate}
// and a sendback close it, as each moves the state on; unblocked{retry} goes back to the state it left.
func (mt *mtask) openJob() bool {
	return mt.job != "" && len(states[mt.kept()].verdicts) > 0
}

// jobState is the state of the attempt's last job: out (open), or due to be sent.
func (mt *mtask) jobState(open bool) string {
	if base, _ := stepN(mt.job); base == StepResolve {
		if open {
			return stResolving
		}
		return stConflict
	}
	if open {
		return stAwaiting
	}
	return stGreen
}

// unblock folds obor resume's d: the needs-you ends, and the failure counts with it; reprompt sends the job anew (its
// next key), retry goes back to the state the needs-you left (its pending step runs again), regate gates again. A
// gate that failed runs again as it was (a lane gate on a new submit); a held or failed publish is published again (a
// moved target re-gates); a merged task goes on with its after-merge steps; anything else submits and gates the lane
// afresh.
func (mt *mtask) unblock(d FactData) {
	mt.needs, mt.fails, mt.lost = Record{}, nil, 0
	if d.Note != "" { // the integrator's next job carries it (decisionText)
		mt.decision = "Lead's decision (obor resume, " + Dash(d.Cause) + "): " + d.Note
		switch d.Cause {
		case causeHeld:
			mt.decision = "Lead's decision on your hold: " + d.Note
		case causeOpen:
			mt.decision = "Lead's decision on your open questions: " + d.Note
			// The worker's next fix job carries it too, whatever attempt it is sent in (T-611).
			mt.openDecision, mt.openAt = d.Note, mt.attempt
		}
	}
	switch {
	case d.Reason == ResumeReprompt:
		mt.state = mt.jobState(false)
	case d.Reason == ResumeRetry && mt.left != "":
		mt.state = mt.left
	case mt.state == stMerged:
	case d.Cause == causePublish:
		mt.state = stAccepted
	case d.Cause == causeGate:
		mt.state = gateState(mt.kind)
	default:
		mt.state = stSubmitted
	}
	if mt.state == stSubmitted && d.Reason != ResumeRetry {
		mt.input = "" // the lane may have moved on: submit it again
	}
	if mt.state == stDispatched { // a refused step runs again under its next key, an undelivered job is sent anew
		mt.refused = ""
		if d.Cause == causeUndelivered {
			mt.work = ""
		}
	}
	if mt.state == stWorking && mt.work == "" && d.Cause == causeUndelivered {
		mt.fixed = false // the fix job sent anew as fix.N
	}
}

// gateState is the state that runs a gate of kind.
func gateState(kind string) string {
	switch kind {
	case InputResolved:
		return stResolved
	case InputMoved:
		return stMoved
	}
	return stSubmitted
}

// unsendable is why the lead cannot send mt back now ("": it can): only a green candidate, an open job
// (awaiting-accept, resolving) or a needs-you after a red gate or a hold goes back to the worker. A state the loop
// leaves by itself settles first.
func (mt *mtask) unsendable() string {
	switch states[mt.state].send {
	case sendYes:
		return ""
	case sendByCause:
		if c := mt.needs.Fact().Cause; c == causeRed || c == causeHeld || c == causeOpen {
			return ""
		}
		return fmt.Sprintf("%s is %s (%s): obor resume or obor drop it", mt.task, stNeedsYou, mt.needs.Fact().Reason)
	case sendSettles:
		return fmt.Sprintf("%s is %s; retry when it settles", mt.task, mt.state)
	}
	return fmt.Sprintf("%s is %s", mt.task, mt.state)
}

// verdict folds the integrator's verdict on the task's open job (its state's verdicts), or the lead's sendback
// (v.Lead); one that no longer fits the state changes nothing.
func (mt *mtask) verdict(method string, v verdictResult) {
	if method == MethodAnswer {
		if mt.state == stWorking || mt.state == stDispatched {
			mt.next(v.Note, true)
		}
		return
	}
	if method == MethodSendback && v.Lead {
		if mt.unsendable() == "" {
			mt.needs = Record{}
			mt.sendback(v.Note)
		}
		return
	}
	for _, r := range states[mt.state].verdicts {
		if r.method == method {
			r.fold(mt, v)
			return
		}
	}
}

// loopTasks is p's merge-loop tasks by task name (under mu): a choice of the first, oldest or any one of them ranges
// over this, never over the map, so it never depends on Go's map order (T-615; the oldest job is by intent seq,
// T-606).
func (p *proj) loopTasks() []string {
	out := make([]string, 0, len(p.folded.loop))
	for task := range p.folded.loop {
		out = append(out, task)
	}
	slices.Sort(out)
	return out
}
