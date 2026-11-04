// mtask.go: folding the merge loop's records into per-task state (mtask) and its accessors.
package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
)

// foldLoop folds r into p's merge-loop tasks.
func (p *proj) foldLoop(r event.Record) {
	mt := p.folded.loop[r.Task]
	if r.Kind == event.KindFact && r.Step == event.StepDispatched {
		p.foldDispatch(r)
		return
	}
	if r.Kind == event.KindFact && r.Step == event.StepWaiting {
		p.foldWaiting(r)
		return
	}
	if r.Kind == event.KindFact && r.Step == event.StepDrive {
		p.foldDrive(r)
		return
	}
	// A dropped fact is terminal for the task whatever attempt it names (RV-42-verify-2 #1: a submission decided
	// before the drop may record the next attempt first): live fold and replay end the task, and every later record of
	// it (submitted, a step result, a verdict) is ignored; only a new dispatch reopens it.
	if r.Kind == event.KindFact && r.Step == event.StepDropped {
		if mt != nil {
			mt.state, mt.needs, mt.dropped, mt.droppedSeq = stDropped, event.Record{}, r.Fact().Reason, r.Seq
		}
		return
	}
	if r.Kind == event.KindFact && r.Step == event.StepSubmitted {
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		next := &mtask{task: r.Task, lane: d.Lane, agent: d.Agent, attempt: r.Attempt, state: stSubmitted,
			report: d.Report}
		if mt != nil && mt.state == stDropped {
			return // ended: a submission decided before the drop
		}
		if mt != nil {
			// The worker's spec rev outlives its attempt; a drive stays one (T-751).
			next.sendbacks, next.specRev, next.drive = mt.sendbacks, mt.specRev, mt.drive
			next.dispSeq = mt.dispSeq // the lane's current task is its newest dispatch's (T-615)
			// The dispatch's identity outlives the submission (T-778): a move after a red gate or a sendback redoes the
			// task on its branch and target with the type it runs.
			next.disp, next.at = mt.disp, mt.at
		}
		p.folded.loop[r.Task] = next
		return
	}
	if mt == nil || r.Attempt != mt.attempt || mt.state == stDropped {
		return // another attempt's record, or the task is dropped
	}
	step, n := event.StepN(r.Step)
	switch r.Kind {
	case event.KindFact:
		switch r.Step {
		case event.StepKept:
			if mt.accepted != "" && mt.accepted == mt.cand.Patch { // a green gate of a new spec rev ended the accept
				mt.state = stAccepted
			}
		case event.StepSendback:
			var d noteData
			_ = json.Unmarshal(r.Data, &d)
			mt.sendback(d.Note)
		case event.StepMoved:
			mt.state = stMoved
		case event.StepMerged:
			var d event.MergedData
			_ = json.Unmarshal(r.Data, &d)
			mt.state, mt.needs = stMerged, event.Record{}
			mt.head, mt.mergedSeq = d.Head, r.Seq
			if d.Outside {
				mt.after = len(afterSteps)
			}
		case event.StepFailed:
			if mt.fails == nil {
				mt.fails = map[string]failure{}
			}
			f := mt.fails[r.Fact().Step]
			f.n, f.at, f.why = f.n+1, event.RecordTime(r), r.Fact().Reason
			mt.fails[r.Fact().Step] = f
		case event.StepNeedsYou:
			// An older log's hold of the open questions before review (T-604) ends here: the review job goes out (T-649).
			if r.Fact().Merge && r.Fact().Cause != causeOpen {
				mt.needs = r
				if mt.state != stNeedsYou {
					mt.left = mt.state
				}
				if mt.state != stRejected && mt.state != stMerged {
					mt.state = stNeedsYou
				}
			}
		case event.StepDelivered:
			if rev := r.Fact().Rev; rev > 0 {
				mt.specRev = rev
			}
			switch job := r.Fact().Job; {
			case mt.state == stDispatched && mt.work != "" && job == JobID(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}):
				mt.state = stWorking
			case mt.job != "" && job == JobID(mt.jobKey()):
				mt.delivered = true
			}
		case event.StepLost:
			if mt.job != "" && r.Fact().Job == JobID(mt.jobKey()) && (mt.state == stAwaiting || mt.state == stResolving) {
				mt.state = mt.jobState(false)
				mt.lost++
			}
		case event.StepUnblocked:
			mt.unblock(r.Fact())
		}
	case event.KindIntent:
		switch step {
		case event.StepGate:
			var d gateIn
			_ = json.Unmarshal(r.Data, &d)
			mt.kind = d.Kind
		case event.StepReview, event.StepResolve:
			var d JobData
			_ = json.Unmarshal(r.Data, &d)
			mt.job, mt.patch, mt.delivered = r.Step, d.Patch, false
			if step == event.StepReview {
				mt.reviews, mt.state = n, stAwaiting
			} else {
				mt.resolves, mt.state = n, stResolving
			}
		case event.StepFix:
			mt.fixed, mt.fixes = true, n
		case event.StepWork:
			mt.work, mt.works = r.Step, n
		}
	case event.KindResult:
		if method, _ := verdictMethod(r.Step); method != "" {
			var v verdictResult
			_ = json.Unmarshal(r.Data, &v)
			if mt.state == stNeedsYou && mt.job != "" && v.Job == JobID(mt.jobKey()) {
				mt.state, mt.needs = mt.jobState(true), event.Record{} // a verdict on a held (or late) job ends the needs-you
			}
			mt.verdict(method, v)
			return
		}
		var g gateOut
		_ = json.Unmarshal(r.Data, &g)
		switch step {
		case event.StepSubmit:
			var d submitOut
			_ = json.Unmarshal(r.Data, &d)
			mt.submits, mt.input = n, d.SHA
			if d.Refused != "" {
				mt.state, mt.why, mt.cause = stFail, "submit: "+d.Refused, causeGate
			}
		case event.StepGate:
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
		case event.StepPrepare:
			var d prepareOut
			_ = json.Unmarshal(r.Data, &d)
			mt.prepares, mt.base = n, d.Base
			if d.Refused != "" {
				mt.refused, mt.cause = "lane: "+d.Refused, causeLane
			} else if mt.drive && mt.state == stDispatched && d.Base != "" {
				mt.state = stDriving // a new drive: the lane is the owner's now (T-751)
			}
		case event.StepStartWork:
			var d startOut
			_ = json.Unmarshal(r.Data, &d)
			mt.starts, mt.started = n, d.Refused == ""
			if d.Refused != "" {
				mt.refused, mt.cause = "start: "+d.Refused, causeStart
			} else {
				mt.startAt = event.RecordTime(r)
			}
		case event.StepPublish:
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
// question, obor resume) to be sent.
func (mt *mtask) next(note string, answer bool) {
	mt.attempt++
	mt.state = stWorking
	if mt.drive { // a drive goes back to its owner, never to a worker (T-751)
		mt.state = stDriving
	}
	mt.attemptVars = attemptVars{line: note, answer: answer}
}

// jobKey is the key of the attempt's last integrator job.
func (mt *mtask) jobKey() event.Key {
	return event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.job}
}

// openJob: the attempt's last integrator job is open, the one a verdict answers (docs/state-model.md "Jobs"): it is
// out (awaiting-accept, resolving), or a needs-you left it out. A verdict result, job.lost, unblocked{reprompt|regate}
// and a sendback close it, as each moves the state on; unblocked{retry} goes back to the state it left.
func (mt *mtask) openJob() bool {
	return mt.job != "" && len(states[mt.kept()].verdicts) > 0
}

// jobState is the state of the attempt's last job: out (open), or due to be sent.
func (mt *mtask) jobState(open bool) string {
	if base, _ := event.StepN(mt.job); base == event.StepResolve {
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
func (mt *mtask) unblock(d event.FactData) {
	mt.needs, mt.fails, mt.lost = event.Record{}, nil, 0
	if d.Note != "" { // the integrator's next job carries it (decisionText)
		mt.decision = "Lead's decision (obor resume, " + Dash(d.Cause) + "): " + d.Note
		switch d.Cause {
		case event.CauseHeld:
			mt.decision = "Lead's decision on your hold: " + d.Note
		}
	}
	switch {
	case d.Reason == wire.ResumeReprompt:
		mt.state = mt.jobState(false)
	case d.Reason == wire.ResumeRetry && mt.left != "":
		mt.state = mt.left
	case mt.state == stMerged:
	case d.Cause == causePublish:
		mt.state = stAccepted
	case d.Cause == causeGate:
		mt.state = gateState(mt.kind)
	default:
		mt.state = stSubmitted
	}
	if mt.state == stSubmitted && d.Reason != wire.ResumeRetry {
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
	case port.InputResolved:
		return stResolved
	case port.InputMoved:
		return stMoved
	}
	return stSubmitted
}

// verdict folds the integrator's verdict on the task's open job (its state's verdicts), or the lead's sendback
// (v.Lead); one that no longer fits the state changes nothing.
func (mt *mtask) verdict(method string, v verdictResult) {
	if method == stepAnswer { // an older log's answer: its fix job the next attempt
		if mt.state == stWorking || mt.state == stDispatched {
			mt.next(v.Note, true)
		}
		return
	}
	if method == wire.MethodSendback && v.Lead {
		if st := mt.state; st == stGreen || st == stAwaiting || st == stResolving || st == stNeedsYou {
			// The lead's sendback starts the next attempt but does not count toward K: K counts the gate's and the
			// integrator's sendbacks (T-619).
			mt.needs = event.Record{}
			mt.next(v.Note, false)
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
