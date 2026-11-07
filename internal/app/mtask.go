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
	// A dropped fact is terminal for the task whatever attempt it names (RV-42-verify-2 #1: a submission decided
	// before the drop may record the next attempt first): live fold and replay end the task, and every later record of
	// it (submitted, a step result, a verdict) is ignored; only a new dispatch reopens it.
	if r.Kind == event.KindFact && r.Step == event.StepDropped {
		if mt != nil {
			mt.unblock()
			mt.state, mt.dropped, mt.droppedSeq, mt.endedAt = stDropped, r.Fact().Reason, r.Seq, event.RecordTime(r)
		}
		return
	}
	// Fact dropping (T-852) is the drop's decision on a task holding a lane, whatever attempt it names (as dropped):
	// its steps (the worker's stop, lane.free) run under the task's attempt, then fact dropped ends it.
	if r.Kind == event.KindFact && r.Step == event.StepDropping {
		if mt != nil && mt.state != stDropped {
			mt.foldDropping(r)
		}
		return
	}
	if r.Kind == event.KindFact && r.Step == event.StepSubmitted {
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		next := &mtask{task: r.Task, lane: d.Lane, agent: d.Agent, attempt: r.Attempt, state: stSubmitted,
			report: d.Report}
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
			mt.unblock()
			mt.state = stMerged
			mt.head, mt.mergedSeq, mt.endedAt = d.Head, r.Seq, event.RecordTime(r)
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
				mt.block(block{fact: r})
			}
		case event.StepSpecSeen:
			if rev := r.Fact().Rev; rev > mt.specRev { // the newer rev task_done's refusal showed the worker (T-819)
				mt.specRev = rev
			}
		case event.StepDelivered:
			if rev := r.Fact().Rev; rev > 0 {
				mt.specRev = rev
			}
			switch job := r.Fact().Job; {
			case mt.is(stDispatched) && mt.work != "" && job == JobID(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}):
				mt.state = stWorking
			case mt.job != "" && job == JobID(mt.jobKey()):
				mt.delivered = true
			}
		case event.StepLost:
			if mt.job != "" && r.Fact().Job == JobID(mt.jobKey()) && (mt.is(stAwaiting) || mt.is(stResolving)) {
				mt.state = mt.jobState(false)
				mt.lost++
			}
		case event.StepUnblocked:
			mt.resumed(r.Fact())
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
			if mt.onHold() && mt.job != "" && v.Job == JobID(mt.jobKey()) {
				mt.unblock() // a verdict on a held (or late) job ends the needs-you
				mt.state = mt.jobState(true)
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
				mt.block(block{cause: causeGate, why: "submit: " + d.Refused})
			}
		case event.StepGate:
			mt.gates, mt.line, mt.mismatch = n, g.Line, g.Mismatch
			switch g.Outcome {
			case outGreen:
				if g.SpecRev != mt.gateRev {
					mt.accepted = "" // the accept was of another spec rev: review again (RV-55 #1)
				}
				mt.cand, mt.gateRev, mt.state = g.Candidate, g.SpecRev, stGreen
				// T-961: a lane or resolved merge the slot compiled is tested in the lane next; a moved one is not (row
				// 32). A gate before T-961 (no Base) ran the checks itself: green as it folded then.
				if g.Candidate.Base != "" && mt.kind != port.InputMoved {
					mt.state = stCandidate
				}
			case outRed:
				mt.state = stRed
			case outConflict:
				mt.state = stConflict
			default:
				mt.block(block{cause: causeGate, why: "gate: " + g.Line})
			}
		case event.StepTest:
			var d testOut
			_ = json.Unmarshal(r.Data, &d)
			mt.tests, mt.line = n, d.Line
			switch d.Outcome {
			case outGreen:
				mt.state, mt.tested = stGreen, d.Checks
			case outRed:
				mt.state = stRed
			default:
				mt.block(block{cause: causeGate, why: "test: " + d.Line})
			}
		case event.StepPrepare:
			var d prepareOut
			_ = json.Unmarshal(r.Data, &d)
			mt.prepares, mt.base = n, d.Base
			if d.Refused != "" {
				mt.refused = "lane: " + d.Refused
			} else if mt.drive && mt.is(stDispatched) && d.Base != "" {
				mt.state = stDriving // a new drive: the lane is the owner's now (T-751)
			}
		case event.StepWorkerStop:
			mt.foldStop(n, r.Data)
		case event.StepFree:
			mt.foldFree(n, r.Data)
		case event.StepStartWork:
			var d startOut
			_ = json.Unmarshal(r.Data, &d)
			mt.starts, mt.started = n, d.Refused == ""
			if d.Refused != "" {
				mt.refused = "start: " + d.Refused
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
				mt.block(block{cause: causePublish, why: "publish: " + g.Line})
			}
		default:
			if i := slices.Index(afterSteps, r.Step); i >= 0 {
				mt.after = max(mt.after, i+1)
				if r.Seq > mt.afterSeq { // the last after-merge step (T-868: the idle key once it is the newest end)
					mt.afterSeq, mt.afterAt = r.Seq, event.RecordTime(r)
				}
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
	mt.next(note)
}

// next starts mt's next attempt on its worker: working, the fix job (note its text) to be sent.
func (mt *mtask) next(note string) {
	mt.attempt++
	mt.state = stWorking
	if mt.drive { // a drive goes back to its owner, never to a worker (T-751)
		mt.state = stDriving
	}
	mt.attemptVars = attemptVars{line: note}
}

// jobKey is the key of the attempt's last integrator job.
func (mt *mtask) jobKey() event.Key {
	return event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.job}
}

// openJob: the attempt's last integrator job is open, the one a verdict answers (docs/state-model.md "Jobs"): it is
// out (awaiting-accept, resolving), also while a block pauses it (a closed block ended it). A verdict result, job.lost,
// unblocked{reprompt|regate} and a sendback close it, as each moves the phase on; unblocked{retry} keeps the phase.
func (mt *mtask) openJob() bool {
	return mt.job != "" && len(states[mt.state].verdicts) > 0 && !mt.needs.closed()
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

// resumed folds obor resume's d: the block ends, and the failure counts with it; the task goes to resumeTarget's phase
// (reprompt: the job anew under its next key; retry: the paused phase's pending step again; regate: the gate or the
// publish again). A dispatch's refused step runs again under its next key, as do a refused worker.stop and a dropping
// task's refused lane.free; an undelivered job is sent anew.
func (mt *mtask) resumed(d event.FactData) {
	mt.unblock()
	mt.fails, mt.lost = nil, 0
	if d.Note != "" { // the integrator's next job carries it (decisionText)
		mt.decision = "Lead's decision (obor resume, " + Dash(d.Cause) + "): " + d.Note
		switch d.Cause {
		case event.CauseHeld:
			mt.decision = "Lead's decision on your hold: " + d.Note
		}
	}
	mt.state = mt.resumeTarget(d.Reason, d.Cause)
	if mt.state == stSubmitted && d.Reason != wire.ResumeRetry {
		mt.input = "" // the lane may have moved on: submit it again
	}
	if d.Cause == causeStop { // a refused worker.stop runs again under its next key (T-852)
		mt.halt.refused = ""
	}
	if mt.state == stDropping { // a refused lane.free too
		mt.refused = ""
	}
	if mt.state == stDispatched {
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
	if method == wire.MethodSendback && v.Lead {
		if mt.is(stGreen) || mt.is(stAwaiting) || mt.is(stResolving) || mt.onHold() {
			// The lead's sendback starts the next attempt but does not count toward K: K counts the gate's and the
			// integrator's sendbacks (T-619).
			mt.unblock()
			mt.next(v.Note)
		}
		return
	}
	if mt.paused() {
		return // the owner decides first: a verdict on a held job ended the block already (foldLoop)
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
