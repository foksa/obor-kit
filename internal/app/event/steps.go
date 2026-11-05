package event

import (
	"strconv"
	"strings"
)

// The steps a record names (Record.Step) and the task keys of the project-level facts (Record.Task). The comment of
// each says which kind of record it is and what its data is; the data types of the merge loop's own steps are
// package app's.

// Task keys of the project's own records: a needs-you or a fact of the project, not of a task.
const (
	// DiskTask is the task key of a project's low-disk needs-you: the project's, not a task's.
	DiskTask = "disk"
	// WatchTask is the task key of a project's watch facts.
	WatchTask = "watch"
	// NightlyTask is the task key of a project's nightly full check: its steps and its needs-you, the project's.
	NightlyTask = "nightly"
	// PauseTask is the task key of a project's pause facts.
	PauseTask = "pause"
	// LoopTask is the task key of a project's loop error (T-377): a needs-you of the project, not of a task.
	LoopTask = "loop"
	// ReviewTask is the task key of a project's review requests and of a failed start's needs-you.
	ReviewTask = "review"
	// MsgTask is the task key of a project's lines: their facts, deliveries and needs-you.
	MsgTask = "msg"
	// ServeTask is the task key of a failed self-restart's needs-you (T-652): the project's serve, not a task's.
	ServeTask = "serve"
	// RoleTask is the task key of serve's stops of role agents.
	RoleTask = "roles"
	// ConfigTask is the task key of the owner's setup edits (obor web, T-753).
	ConfigTask = "config"
	// IdleTask is the task key of the lead's acknowledgement of the project's idle state (T-803, T-866).
	IdleTask = "idle"
	// RuleTask is the task key of the needs-you of a rule whose action failed ruleFails passes in a row (T-866).
	RuleTask = "rule"
)

// StepAnswered is the fact of an answer (task MsgTask; data: Message, Task the task whose question it ends).
const StepAnswered = "answered"

// The dispatch's steps.
const (
	StepDispatched = "dispatch"     // fact: the request passed its refusals (data: dispatchData)
	StepWaiting    = "waiting"      // fact: the request waits for its After tasks to merge (data: dispatchData)
	StepPrepare    = "lane.prepare" // intent/result (G), lane.prepare.N after a refusal: PrepareInput → prepareOut
	StepStartWork  = "agent.start"  // intent/result (S), agent.start.N after a refusal: startData → startOut
	StepWork       = "work"         // prompt intent (P), work.N after obor resume: the worker's job
	// StepDrive is fact drive: the owner takes over a task in progress (obor drive T-xx, T-751): its worker is stopped
	// (StepWorkerStop), the drive agent works in the lane (data: dispatchData, Drive set). A new drive is a fact
	// dispatch with Drive set.
	StepDrive = "drive"
	// StepWorkerStop is the merge loop's stop of a task's worker before the step that needs its lane (T-852): a move's
	// (before the redo's lane.prepare), a take-over's (before the drive agent starts) or a drop's (before lane.free).
	// intent/result (S), worker.stop.N after a refusal obor resume retries: stopData → stopOut.
	StepWorkerStop = "worker.stop"
)

// A drop of a task that holds a lane (T-852): the decision, then its steps, then fact dropped.
const (
	StepDropping = "dropping"  // fact: obor drop decided on a task holding a lane (data: dropData): stop, free, dropped
	StepFree     = "lane.free" // intent/result (G), lane.free.N after a refusal: a dropping task's lane given back → freeOut
)

const (
	StepWatch   = "watch"   // fact: the lead watches a set of tasks' merges (data: WatchData)
	StepUnwatch = "unwatch" // fact: obor watch --rm ended it (data: WatchData)
)

// StepNightly is the nightly full check's intent/result (G), one key a day: nightly.<YYYY-MM-DD> → NightlyOut.
const StepNightly = "nightly"

// StepConfigSet is the fact of a setup edit serve wrote and committed (task ConfigTask; data: ConfigSet; T-753).
const StepConfigSet = "config.set"

const (
	StepPaused   = "paused"   // fact: the project is paused (data: Pause)
	StepUnpaused = "unpaused" // fact: obor pause --clear ended it (data: Pause, the call's)
)

// The merge loop's steps (the integrator's jobs: StepReview, StepResolve; its verdicts: <method>.<call id>).
const (
	StepSubmitted  = "submitted"    // fact: the worker's task_done submits the task (data: submitData, its report)
	StepSubmit     = "submit"       // intent/result (G), submit.N: the lane's commit pinned → submitOut
	StepGate       = "gate"         // intent/result (G), gate.N within an attempt: gateIn → gateOut
	StepKept       = "accept.kept"  // fact: the re-gated patch is the one accepted
	StepSendback   = "sendback"     // fact: the gate is red, back to the worker (data: noteData)
	StepFix        = "fix"          // prompt intent (P) of the new attempt: the worker's fix job (data: noteData)
	StepPublish    = "publish"      // intent/result (G), publish.N: the candidate → gateOut
	StepMoved      = "target.moved" // fact: the target moved under the candidate
	StepMerged     = "merged"       // fact (data: mergedData)
	StepAfterMerge = "after_merge"  // intent/result (G): commands.after_merge
	StepStop       = "agent.stop"   // intent/result (S): the lane's worker stopped (data: stopData)
	StepUnpin      = "unpin"        // intent/result (G): a rejected task's pins deleted
	StepDelivered  = "delivered"    // fact: the agent fetched its job (job_next)
	StepDropped    = "dropped"      // fact: the lead dropped the task (obor drop): terminal, no more steps (data: FactData{Reason, Agent})
	StepLost       = "job.lost"     // fact: the integrator exited with the job open: it is started, the job sent anew
	StepUnblocked  = "unblocked"    // fact: obor resume ended the needs-you (data: FactData, Reason regate|reprompt|retry)
	StepFailed     = "failed"       // fact: a step's effect failed, counted (data: FactData, Step the step's key)
	// StepSpecSeen is task_done's refusal of a job carried at an older spec rev: the worker was told the newer rev
	// (data: FactData, Rev: the rev now seen; T-819), so its next task_done on that rev submits.
	StepSpecSeen = "spec.seen"
)

// CauseHeld is the integrator's hold, the one cause of a merge-loop needs-you obor status lists as an event of its
// own (T-658: "held T-xx: why"; every cause is in obor status' needs-you line).
const CauseHeld = "held"

const (
	StepReviewRequest = "review.request" // fact: a review is requested (data: ReviewData)
	StepReviewRm      = "review.rm"      // fact: obor review --rm ended it (data: ReviewData)
	StepReviewFire    = "review.fire"    // fact: a pass fires the request (data: ReviewData, Request its seq)
	StepReviewStarted = "review.started" // fact: the reviewer started on the brief (data: ReviewData)
	StepReviewFix     = "review.fix"     // fact: fix tasks linked to the open review (data: ReviewData, ID, Tasks, Note; T-850)
	// StepReviewVerifyRequest is a verify requested (T-856: a fix round once its tasks merged; data: ReviewData, ID the
	// review, Tasks, Note, By, Round the fix round's seq): its seq keys the verify record, which a merge-loop pass admits once the reviewer is idle (reviewverify.go).
	StepReviewVerifyRequest = "review.verify.request"
)

const (
	StepMessage = "message" // fact: a line to type (data: Message)
	StepBriefed = "briefed" // fact: a role agent was briefed (data: Message; Line "" when its start carried it)
	StepWorked  = "worked"  // fact: a briefed agent's pane worked since its brief (data: workedData)
)

// The project's idle state (T-803, T-866): no task in flight, no needs-you and no brief open to the reviewer or the
// deep dive.
const (
	StepIdle    = "idle"     // compat until v0.28: an idle epoch fact (before T-866), folded to nothing
	StepIdleAck = "idle.ack" // fact: the lead or the owner acknowledged it with a why (data: idleAck, Key and Why)
)

// Job steps: the integrator's jobs, prompted by the merge loop as intents with these steps (a later job of the same
// attempt: review.2, resolve.2 …); their data is JobData.
const (
	StepReview  = "review"  // awaiting-accept: accept, sendback or reject
	StepResolve = "resolve" // resolving: resolved or reject
)

// StepServeRestart is the running line's item for a pending self-restart (T-657): serve ends itself on the new build
// once after_merge ran green (T-652).
const StepServeRestart = "restart"

const (
	StepStopped = "stopped" // fact: an agent's turn ended on an API error (data: stoppedData)
	StepTurned  = "turned"  // fact: a briefed role agent's stop ended with its next turn (data: turnedData)
)

// StepN splits step.N into step and N (N 1 when it has none). view reads a step's base through it: one split.
func StepN(step string) (string, int) {
	if i := strings.LastIndexByte(step, '.'); i > 0 {
		if n, err := strconv.Atoi(step[i+1:]); err == nil && n > 1 {
			return step[:i], n
		}
	}
	return step, 1
}
