// block.go: the merge loop's needs-you as a block on a task's phase (T-827, DD-consolidation S2).
package app

import (
	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// block is the merge loop's needs-you on a task (mtask.needs): none, due (its phase's own work failed: the cause and
// why of the needs-you its next pass records, pickDue) or held (the needs-you fact in force: the owner decides). The
// task's state stays its phase throughout. mtask.block sets one, mtask.unblock clears it: obor resume (resumed), a
// verdict on the held job, a sendback of the lead's, a take-over, a merge, a drop.
type block struct {
	cause, why string       // due: the needs-you to record; held after it: kept, the block is closed
	fact       event.Record // held: the needs-you fact in force (Seq 0: not recorded)
}

// due: the needs-you is decided, not recorded yet (a serve restart decides it again from the same records).
func (b block) due() bool { return b.why != "" && b.fact.Seq == 0 }

// held: the needs-you fact is in force.
func (b block) held() bool { return b.fact.Seq != 0 }

// closed: the block began due, so its phase's work ended (a gate or publish failure, a refused submit, sendbacks past
// K, a resolve rejected) rather than paused (a held job, a failed step, a stalled or undelivered job): no job of the
// phase is open, it holds no scratch, and obor resume's retry has no step of it to run again.
func (b block) closed() bool { return b.why != "" }

// block sets mt's block b: due, or held (a recorded due block stays closed; a held one stays as it began).
func (mt *mtask) block(b block) {
	if b.held() {
		b.cause, b.why = mt.needs.cause, mt.needs.why
	}
	mt.needs = b
}

// unblock clears mt's block: its phase goes on.
func (mt *mtask) unblock() { mt.needs = block{} }

// paused: a block holds mt's phase: due, or held on a phase that has not ended (a merged or rejected task's needs-you
// changes nothing of its phase: it only waits for obor resume or a drop).
func (mt *mtask) paused() bool {
	return mt.needs.due() || mt.needs.held() && !states[mt.state].ended
}

// onHold: a held block holds mt's phase (the owner decides; a due one is recorded first).
func (mt *mtask) onHold() bool { return mt.needs.held() && mt.paused() }

// landed: a merge outside the app (by hand in git) may have landed mt: its row says so, or a block holds it (the owner
// may have merged it by hand meanwhile); drive checks before its step.
func (mt *mtask) landed() bool { return states[mt.state].landed || mt.paused() }

// is: mt is in phase st and no block holds it.
func (mt *mtask) is(st string) bool { return mt.state == st && !mt.paused() }

// shown is the state obor status and the replies name for mt: its phase, or for a block the names obor has always
// shown (fail: due; needs-you: held).
func (mt *mtask) shown() string {
	switch {
	case mt.needs.due():
		return "fail"
	case mt.paused():
		return wire.PhaseNeedsYou
	}
	return mt.state
}

// resumeTarget is the phase obor resume's act takes mt to, by its phase and its needs-you's cause (docs/state-model.md
// row 40): reprompt sends the job anew; retry runs the paused phase's pending step again (a merged task: its
// after-merge steps); a gate or publish failure gates (as it was) or publishes again; anything else submits and gates
// the lane afresh.
func (mt *mtask) resumeTarget(act, cause string) string {
	switch {
	case act == wire.ResumeReprompt:
		return mt.jobState(false)
	case act == wire.ResumeRetry && !mt.needs.closed(), mt.state == stMerged:
		return mt.state
	case cause == causePublish:
		return stAccepted
	case cause == causeGate:
		return gateState(mt.kind)
	}
	return stSubmitted
}
