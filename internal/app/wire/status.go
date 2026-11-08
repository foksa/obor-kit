package wire

import (
	"obor/internal/app/event"
	"obor/internal/store"
)

// Status is the answer to the app socket's one request, status.
//
// A predicate serve ships is named so that a missing field (an older serve's answer, during an upgrade) reads as the
// safe answer: a task is open unless it says it ended, a lane is locked unless it says it is free (T-921).
type Status struct {
	Since     string         `json:"since,omitempty"`      // when this serve started (RFC 3339)
	Connected bool           `json:"connected"`            // the herdr subscription is live
	HerdrNote string         `json:"herdr_note,omitempty"` // "herdr 0.9.4, protocol 23" when herdr is newer than obor's protocol
	Project   *ProjectStatus `json:"project,omitempty"`    // the served project once loaded (nil before)
}

// ProjectStatus is the served project: its observed agents, whether the merge loop runs for it and its open tasks,
// the facts in force (the watchdogs' and the merge loop's needs-you) and the lead's open watches. LogError is its
// event log's last append error (cleared by a good append): every step that needs a record fails while it stays.
type ProjectStatus struct {
	Name     string         `json:"name"`
	Root     string         `json:"root"`
	Merging  bool           `json:"merging"`
	Agents   []AgentStatus  `json:"agents"`
	Tasks    []TaskStatus   `json:"tasks"`
	Facts    []event.Record `json:"facts"`
	LogError string         `json:"log_error,omitempty"`
	// Lanes is the line on serve's lane table when it is older than the tick: its age, and its last scan's error
	// (T-879; "": fresh).
	Lanes  string       `json:"lanes,omitempty"`
	Paused *event.Pause `json:"paused,omitempty"` // the pause in force (pause.go)
	// ModelPauses is the model pauses in force (obor pause --model, T-765), by model name (T-766: the Setup page).
	ModelPauses map[string]*event.Pause `json:"model_pauses,omitempty"`
	// Free is the configured lanes that hold no task now, by configset's own rule (laneHolds): obor web's setup page
	// warns before a lanes-count edit drops one not in it, and serve refuses that edit while a lane holds a task
	// (T-896). Absent (an older serve): no lane free, so every dropped lane warns (T-921).
	Free []string `json:"free"`
	// Watches is the lead's open watches (mergewatch.go), oldest first: obor status lists them (T-658).
	Watches []WatchStatus `json:"watches,omitempty"`
	// Idle is the project's idle state (T-803): the pipeline is empty and the lead was told; nil when it has work.
	Idle *IdleStatus `json:"idle,omitempty"`
	// Messages is the lines not delivered yet (messages.go): obor send's, obor brief's, a brief's finished line.
	Messages []MessageStatus `json:"messages,omitempty"`
	// Review is the open review request, else "review due: …" when more than 3 tasks merged since the last review
	// and branches.promote is unset (review.go; "": neither; T-974: a promotion requests its review itself); then the
	// open review's fix tasks, "RV-n fixes: T-a merged · T-b open" (T-924).
	Review string `json:"review,omitempty"`
	// Promote is the promotion of branches.main to branches.promote (T-974; nil: unset, or no pass read it yet).
	Promote *PromoteStatus `json:"promote,omitempty"`
	// ReviewRequest is the open review request as data (T-664: the view's queue; nil: none): Review is its line.
	ReviewRequest *ReviewRequestStatus `json:"review_request,omitempty"`
	// Briefs is the role agents' open briefs (messages.go): the brief each is on, since when and whether its line was
	// delivered (T-664: the view's roles and queue), by agent.
	Briefs []BriefStatus `json:"briefs,omitempty"`
	// Answers is, by a needs-you fact's seq, the commands that answer it in its task's state (answerPath; none: "").
	Answers map[int64]string `json:"answers,omitempty"`
	// Running is what serve does for the project without an agent now (T-657, T-662): its command steps whose effect
	// runs, and a pending self-restart, oldest first.
	Running []RunningStep `json:"running"`
	// Pending is the command steps whose intent has no result and whose effect does not run now (T-662): a step that
	// failed, or one the next pass or a replay has yet to run, oldest first.
	Pending []PendingStep `json:"pending"`
	// Scratch is the task that holds serve's worktree and why (T-669; nil: free), and ScratchQueue, in the merge loop's
	// order, each task whose next step needs it with what it waits for, if anything does (scratchQueue: the scheduler's
	// own rule; no task is named as next, T-673).
	Scratch      *ScratchStatus `json:"scratch,omitempty"`
	ScratchQueue []ScratchWait  `json:"scratch_queue"`
	// Stopped is the role agents whose only labelled pane is at its shell — serve stopped them once their work was done
	// (T-672: the integrator after its last verdict, the reviewer once its review closed) or the lead did (T-677:
	// obor restart <agent> --stop for any role) — sorted: their next use starts them fresh.
	Stopped []string `json:"stopped,omitempty"`
}

// RunningStep is one step that runs without an agent, as obor status' running line lists it (T-657): the merge loop's
// gate, publish (the merge) or after_merge, its nightly check, or a pending self-restart. Since is when the effect
// started (Unix seconds): the elapsed time the line shows (T-662: not the intent's time, which a replay's is older).
// Command is the command a gate runs now (T-669: commands.<name>, the class's, the guard or check; T-673: "" between
// two commands too, so a finished one never shows as running; "", also, before the first and after the last).
type RunningStep struct {
	Step    string `json:"step"`
	Task    string `json:"task,omitempty"`
	Command string `json:"command,omitempty"`
	Since   int64  `json:"since"`
}

// PendingStep is one command step whose intent has no result and whose effect does not run now, as obor status'
// pending line lists it (T-662): Since is when its intent was written (Unix seconds); Error and FailedAt are its last
// failure's reason and time (none: the step never failed, it is queued for its next pass or a replay).
type PendingStep struct {
	Step     string `json:"step"`
	Task     string `json:"task,omitempty"`
	Since    int64  `json:"since"`
	Error    string `json:"error,omitempty"`
	FailedAt int64  `json:"failed_at,omitempty"`
}

// ReviewRequestStatus is the open review request (review.go): the tasks it waits to merge (After) and those of them
// not merged yet (Waiting), the task whose fix it waits to close (Held), its note, who asked and since when (Unix
// seconds).
type ReviewRequestStatus struct {
	After   []string `json:"after,omitempty"`
	Waiting []string `json:"waiting,omitempty"`
	Held    string   `json:"held,omitempty"`
	Note    string   `json:"note,omitempty"`
	By      string   `json:"by,omitempty"`
	Since   int64    `json:"since"`
}

// BriefStatus is a role agent's open brief: to whom, since when (Unix seconds: its line's delivery, else its fact's
// time), whether its line was delivered, and whether the agent's pane worked on it since.
type BriefStatus struct {
	To        string `json:"to"`
	Since     int64  `json:"since"`
	Delivered bool   `json:"delivered"`
	Worked    bool   `json:"worked,omitempty"`
}

// WatchStatus is one of the lead's open watches: the task or comma list watched, who watches it, its note (T-666:
// what the lead meant to do when the set ends; "" when none), and since when (Unix seconds, its fact's time).
type WatchStatus struct {
	Task  string `json:"task"`
	By    string `json:"by,omitempty"`
	Note  string `json:"note,omitempty"`
	Since int64  `json:"since"`
}

// IdleStatus is the project's idle state (T-803): since when the pipeline is empty (Unix seconds, the time of the
// newest record that ended what it waited on: a merge, a drop, a finished brief, a verify's end, the last after-merge
// step, a review request's end; T-868), what ended it ("T-xx merged <sha7>", "T-xx dropped: <why>"), and the lead's
// acknowledgement: Acked and its Why.
type IdleStatus struct {
	Since int64  `json:"since"`
	Last  string `json:"last,omitempty"`
	Acked bool   `json:"acked,omitempty"`
	Why   string `json:"why,omitempty"`
}

// PromoteStatus is the promotion as the last pass read it (T-974): the promoted branch and its sha, the first-parent
// merges on the target since, and why the promotion waits ("": none waits: the open review or request, T-990, or a
// GREEN run waiting to be promoted). T-977, the monitor's: when the sha was promoted (At, fact promoted's time; 0: no
// fact promoted it), the tasks merged since (Tasks) and their weight against merge.promote_weight (Threshold), the
// promotion under way (Run) and the last RED one while its needs-you is open (Red).
type PromoteStatus struct {
	Branch    string      `json:"branch"`
	SHA       string      `json:"sha"`
	At        int64       `json:"at,omitempty"`
	Merges    int         `json:"merges"`
	Tasks     []string    `json:"tasks,omitempty"`
	Weight    int         `json:"weight"`
	Threshold int         `json:"threshold"`
	Wait      string      `json:"wait,omitempty"`
	Run       *PromoteRun `json:"run,omitempty"`
	Red       *PromoteRed `json:"red,omitempty"`
}

// What a promotion under way does (PromoteRun.Step, T-977).
const (
	PromoteSuite  = "suite"  // the full suite runs on the target's head
	PromoteReview = "review" // the suite was GREEN; the fast-forward waits for a closed review
)

// PromoteRun is the promotion under way: the target's head it checks, since when (the suite's start, or the GREEN
// result's time while it waits for review), its step (Promote*) and, waiting for review, the open review (RV-n; "":
// none is open yet, the request or a merge not reviewed).
type PromoteRun struct {
	SHA    string `json:"sha"`
	Since  int64  `json:"since"`
	Step   string `json:"step"`
	Review string `json:"review,omitempty"`
}

// PromoteRed is a RED promotion whose needs-you is open: the sha it ran on, the failing tests' line, when, and the
// tasks merged since the promoted sha it followed (Since).
type PromoteRed struct {
	SHA    string   `json:"sha"`
	Tests  string   `json:"tests"`
	At     int64    `json:"at"`
	Since  string   `json:"since,omitempty"`
	Merges []string `json:"merges,omitempty"`
}

// TaskDriving is a TaskStatus' State while the owner's drive agent works on the task (obor drive, T-751): serve sends
// it no jobs and no watchdog needs-you until obor land submits it.
const TaskDriving = "driving"

// A task's phase (TaskStatus.Phase, T-788): what serve does for it now, computed once by serve from its state's row
// (internal/app/states.go) and the live overlays (needs-you in force, a step running or pending); obor status, the
// view and obor web show it as it is.
const (
	PhaseWaiting        = "waiting"         // waits for its --after tasks or a free lane
	PhaseDispatching    = "dispatching"     // its dispatch's steps (lane, agent, the work job)
	PhaseWorking        = "working"         // its worker works on it (the work or fix job)
	PhaseDriving        = TaskDriving       // the owner's drive agent works in the lane (T-751)
	PhaseSubmitted      = "submitted"       // submitted: its gate is next
	PhaseGating         = "gating"          // its gate runs, or runs again next (resolved, moved, red)
	PhaseGatePending    = "gate pending"    // its gate's intent waits: a failed step, or a replay to run (T-662)
	PhaseTesting        = "testing"         // its candidate's lane test runs, or runs next (T-961)
	PhaseTestPending    = "test pending"    // its lane test's intent waits: a failed step (a dirty lane), or a replay to run
	PhaseReview         = "review"          // gate green: its review or resolve job is next
	PhaseReviewing      = "reviewing"       // the integrator's review or resolve job is out
	PhasePublish        = "publish"         // accepted: its publish is next
	PhaseMerging        = "merging"         // its publish runs
	PhasePublishPending = "publish pending" // its publish's intent waits (T-662)
	PhaseNeedsYou       = "needs-you"       // a needs-you is in force for it (or its lane): the owner decides
	PhaseHeld           = "held"            // the integrator's hold verdict: the owner decides (C7: held means this alone)
	PhaseMerged         = "merged"          // the target holds it: its after-merge steps
	PhaseRejected       = "rejected"        // ended: rejected
	PhaseDropped        = "dropped"         // ended: dropped
	PhaseDropping       = "dropping"        // obor drop decided: its worker's stop and its lane's free run, then dropped (T-852)
)

// The merge loop's stages (PhaseStage): the pipeline's columns, in the order a task goes through them.
const (
	StageSubmitted = "submitted" // submitted, waits for the scratch
	StageGating    = "gating"    // its gate or lane test runs, waits, or failed red
	StageReview    = "review"    // gate green: the integrator's review or resolve job
	StagePublish   = "publish"   // accepted: published onto the target
	StageHeld      = "held"      // needs-you or held: the owner decides
)

// PhaseStage is the merge-loop stage of phase ("": not in the merge loop: waiting, dispatching, working, driving or
// ended).
func PhaseStage(phase string) string {
	switch phase {
	case PhaseSubmitted:
		return StageSubmitted
	case PhaseGating, PhaseGatePending, PhaseTesting, PhaseTestPending:
		return StageGating
	case PhaseReview, PhaseReviewing:
		return StageReview
	case PhasePublish, PhaseMerging, PhasePublishPending:
		return StagePublish
	case PhaseNeedsYou, PhaseHeld:
		return StageHeld
	}
	return ""
}

// The steps a task's page shows it through (PhaseStep's indexes; obor web's stepper: dispatched, working, submitted,
// gate, review, merging, then merged: every step done).
const (
	TaskStepDispatched = iota
	TaskStepWorking
	TaskStepSubmitted
	TaskStepGate
	TaskStepReview
	TaskStepMerging
	TaskStepMerged
)

// PhaseStep is the step a task in phase is on, and whether phase places it (false: needs-you, held, merged or ended,
// whose records say; or no phase). Publish is merging, never merged: merged is the merged fact alone.
func PhaseStep(phase string) (int, bool) {
	switch phase {
	case PhaseWaiting, PhaseDispatching:
		return TaskStepDispatched, true
	case PhaseWorking, PhaseDriving:
		return TaskStepWorking, true
	case PhaseSubmitted:
		return TaskStepSubmitted, true
	case PhaseGating, PhaseGatePending, PhaseTesting, PhaseTestPending:
		return TaskStepGate, true
	case PhaseReview, PhaseReviewing:
		return TaskStepReview, true
	case PhasePublish, PhaseMerging, PhasePublishPending:
		return TaskStepMerging, true
	}
	return 0, false
}

// TaskStatus is one task of the merge loop that is not merged.
type TaskStatus struct {
	Task    string `json:"task"`
	Lane    string `json:"lane"`
	Attempt int    `json:"attempt"`
	State   string `json:"state"`
	// Ended is whether the state table ends the task (rejected or dropped; a merged one is no TaskStatus at all).
	// Readers show a task as open unless this is set, so an older serve's answer, without it, keeps the task open
	// (T-896, T-921).
	Ended   bool   `json:"ended,omitempty"`
	Phase   string `json:"phase"`             // what serve does for it now (Phase*, T-788)
	Stage   string `json:"stage,omitempty"`   // its merge-loop stage (Stage*; "": not in the merge loop; T-797)
	Running string `json:"running,omitempty"` // the step whose effect runs now in its own goroutine (gate, test, publish)

	After []string `json:"after,omitempty"` // waiting: the tasks it waits for
	Why   string   `json:"why,omitempty"`   // waiting, all it waits for merged: its dispatch's last refusal
	Level string   `json:"level,omitempty"` // waiting: the difficulty level its models came from
	Types []string `json:"types,omitempty"` // waiting: the models it takes (model refs, T-695, T-765)
	// Difficulty is a waiting task's spec Difficulty level (its dispatch's level, else its stored spec's line: a --model
	// override; "": neither) and Model the model ref it will run on: its first of Types not paused (a --model
	// override's or its level's; "": every one is paused) (T-977).
	Difficulty string `json:"difficulty,omitempty"`
	Model      string `json:"model,omitempty"`
	Ref        string `json:"ref,omitempty"`    // its spec's Ref: an external tracker's id (T-1022)
	Effort     string `json:"effort,omitempty"` // dispatched: the effort its lane's agent starts with ("": the kind's; T-695)
	// Job is its open job (a worker's work or fix job, the integrator's review or resolve job: <task>/<attempt>/<step>)
	// and Delivered whether its agent's job_next fetched it (T-664; "": no job open).
	Job       string `json:"job,omitempty"`
	Delivered bool   `json:"delivered,omitempty"`
	// Stopping is the worker the merge loop stops before the task's next step (a move, a drop; T-852): ""
	// once it is stopped or gone. Still set with Phase needs-you: the stop was refused (obor resume retries it).
	Stopping string `json:"stopping,omitempty"`
	// Started is when serve last tried to start the task's lane agent (Unix seconds; 0: no try recorded): its newest
	// agent.start record, a dispatch's or a resume's retry under the step's next key (foldStart; T-783, T-893).
	Started int64 `json:"started,omitempty"`
}

// AgentStatus is one observed agent: its address, lane and task (when its cwd is a lane) and status for Age seconds.
type AgentStatus struct {
	Machine   string `json:"machine"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Name      string `json:"name,omitempty"`
	Agent     string `json:"agent"`
	Lane      string `json:"lane,omitempty"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status"`
	Age       int64  `json:"age"`
	// The agent's current recorded session (sessions.go, T-577): its id, transcript file and start (Unix seconds);
	// none while the pane's agent is not identified or has none recorded.
	Session    string `json:"session,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Started    int64  `json:"started,omitempty"`
}

// SetSession fills a's recorded session fields (sessions.go, T-577) from s, the one session shape they mirror (T-889).
func (a *AgentStatus) SetSession(s store.Session) {
	a.Session, a.Transcript, a.Started = s.ID, s.Transcript, s.Started
}

// The scratch reasons are the merge loop's states where they are one (internal/app/loop.go takes them from here).

// Why a task holds the scratch, or what a task waiting for it waits on (ScratchStatus.Why, ScratchWait.Why).
const (
	ScratchGating     = "gating"     // its gate runs
	ScratchPublishing = "publishing" // its publish runs
	ScratchConflict   = "conflict"   // its merge conflicts there: the resolve job is not sent yet
	ScratchResolving  = "resolving"  // its resolve job is out
	ScratchResolved   = "resolved"   // the integrator committed the merge there: its gate is next
	ScratchHeld       = "held"       // a needs-you keeps one of the three above: the owner decides
)

// ScratchStatus is the task that holds the scratch and why (obor status; nil: the scratch is free).
type ScratchStatus struct {
	Task string `json:"task"`
	Why  string `json:"why"`
}

// ScratchWait is one task whose next step needs the scratch (a gate or a publish), in the pass's order: WaitsOn the
// task it waits for and Why (scratchWait: that task's ScratchStatus.Why, or gating / publishing for its step). Both
// empty: nothing serve knows holds the scratch; no task is predicted to take it (T-673: the scheduler's rules are
// more than status can copy).
type ScratchWait struct {
	Task    string `json:"task"`
	WaitsOn string `json:"waits_on,omitempty"`
	Why     string `json:"why,omitempty"`
}

// MessageStatus is one line not yet delivered (obor status): to whom, from whom, the line, its age in seconds, and why
// serve holds it ("": not held; T-746).
type MessageStatus struct {
	To   string `json:"to"`
	From string `json:"from,omitempty"`
	Line string `json:"line"`
	Age  int64  `json:"age"`
	Held string `json:"held,omitempty"`
}
