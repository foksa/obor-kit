package wire

import (
	"obor/internal/app/event"
)

// Status is the answer to the app socket's one request, status.
type Status struct {
	Since     string         `json:"since,omitempty"`   // when this serve started (RFC 3339)
	Connected bool           `json:"connected"`         // the herdr subscription is live
	Project   *ProjectStatus `json:"project,omitempty"` // the served project once loaded (nil before)
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
	Paused   *event.Pause   `json:"paused,omitempty"` // the pause in force (pause.go)
	// Watches is the lead's open watches (mergewatch.go), oldest first: obor monitor lists them (T-658).
	Watches []WatchStatus `json:"watches,omitempty"`
	// Messages is the lines not delivered yet (messages.go): obor send's, obor brief's, a brief's finished line.
	Messages []MessageStatus `json:"messages,omitempty"`
	// Review is the open review request, else "review due: …" when more than 3 tasks merged since the last review
	// (review.go; "": neither).
	Review string `json:"review,omitempty"`
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

// TaskStatus is one task of the merge loop that is not merged.
type TaskStatus struct {
	Task    string   `json:"task"`
	Lane    string   `json:"lane"`
	Attempt int      `json:"attempt"`
	State   string   `json:"state"`
	Running string   `json:"running,omitempty"` // the step whose effect runs now in its own goroutine (gate, publish)
	Hold    string   `json:"hold,omitempty"`    // accepted: "merge held: <role> working" while its publish waits (T-718)
	After   []string `json:"after,omitempty"`   // waiting: the tasks it waits for
	Why     string   `json:"why,omitempty"`     // waiting, all it waits for merged: its dispatch's last refusal
	Level   string   `json:"level,omitempty"`   // waiting: the difficulty level its lane types came from
	Types   []string `json:"types,omitempty"`   // waiting: the lane types it takes (type references, T-695)
	Effort  string   `json:"effort,omitempty"`  // dispatched: the effort its lane's agent starts with ("": the type's; T-695)
	// Job is its open job (a worker's work or fix job, the integrator's review or resolve job: <task>/<attempt>/<step>)
	// and Delivered whether its agent's job_next fetched it (T-664; "": no job open).
	Job       string `json:"job,omitempty"`
	Delivered bool   `json:"delivered,omitempty"`
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

// MessageStatus is one line not yet delivered (obor status): to whom, from whom, the line, and its age in seconds.
type MessageStatus struct {
	To   string `json:"to"`
	From string `json:"from,omitempty"`
	Line string `json:"line"`
	Age  int64  `json:"age"`
}
