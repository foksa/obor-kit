package wire

import (
	"encoding/json"
	"strings"
)

// The app socket's protocol, client side: what a caller can ask serve (the methods), the Call it sends and the
// answers it gets. The server (Listen and the handlers) is in internal/app. Moved here from internal/app (T-692, app
// split 3/5).

// Verdict methods of the app socket.
const (
	MethodAccept   = "accept"   // the candidate whose patch hash it names may be published
	MethodSendback = "sendback" // back to the worker with the integrator's notes
	MethodReject   = "reject"   // drop the patch (a resolve job: the resolve failed)
	MethodResolved = "resolved" // the conflict is merged in the scratch: gate it again
	MethodHold     = "hold"     // the owner must decide: needs-you{held: why}, the job stays open
	MethodDrop     = "drop"     // the lead's or the owner's: end the task for good (Call.Note: why), whatever it waits for
	MethodResume   = "resume"   // the lead's or the owner's: end the task's needs-you (Call.To: regate, reprompt, retry, "")
)

// The other methods of the app socket; the server's handler for each is in internal/app.
const (
	MethodAnswer   = "answer"        // the lead's answer to a worker's question in force (answer.go): Call.Task, Call.Note
	MethodDispatch = "dispatch"      // Call.Task onto Call.Lane, or the first free lane of Call.Types
	MethodWatch    = "watch"         // Call.Task the task or comma list watched (""; the open watches listed), Call.Clear ends its watch
	MethodPause    = "pause"         // Call.Note the why, Call.Until its end (0: until cleared); Call.Clear ends the pause in force
	MethodReview   = "review"        // Call.After the tasks it waits for, Call.Note the lead's note, Call.Clear ends the open request
	MethodVerify   = "review_verify" // obor review verify: Call.After the fix tasks, Call.Findings the findings to re-run, Call.Note the lead's note
	MethodSession  = "session"       // Call.Agent's session Call.Session began (Call.Kind, Call.Transcript, Call.Started, Call.Source; where: Call.Pane, Call.Lane, Call.Task)
	MethodSpecSet  = "spec_set"      // Call.Spec stored as Call.Task's spec, the reply "SET <task> rev <n>" (or "UNCHANGED <task> rev <n>")
	MethodConfig   = "config_set"    // the owner's setup edits (obor web, T-753, T-760): Call.Changes in .obor/config.json, one commit on main
	MethodBackup   = "backup"        // serve takes a backup of Call.Root's database (obor nightly, T-435): serve is its one writer
	MethodSend     = "send"          // Call.Root the target project, Call.From the sender project, Call.Note the text
	MethodBrief    = "brief"         // Call.To the role agent, Call.Note the brief ("": the restart's first prompt carried it)
	MethodStopped  = "stopped"       // Call.Agent the agent, Call.Role its role, Call.Lane a worker's lane, Call.Error, Call.Details
	MethodTurn     = "turn"          // an agent's next turn began or ended (its UserPromptSubmit or Stop hook): Call.Agent, Call.Lane
	// MethodExit (the owner's or the lead's; obor stop, T-711): serve ends gracefully, as a signal ends it and as its
	// self-restart does (T-652, T-656): Call.Root its project, Call.Note why. The reply is EXITING; the caller confirms
	// serve down by its serve.flock and socket.
	MethodExit = "exit"
	// MethodDrive (the owner's or the lead's; obor drive, T-751): Call.Note the drive's name for a new drive (a new task
	// on the first free untyped lane, Call.Types its one type reference), else Call.Task the task in progress taken
	// over. The reply is DRIVE <task> → <lane> (<agent>, <type>): the CLI starts the drive agent.
	MethodDrive = "drive"
	// MethodLand (the owner's, the lead's or the drive agent's; obor land): Call.Task the driving task, Call.Report the
	// summary its submission carries. The reply is LANDED <task>: submitted.
	MethodLand = "land"
)

// The MCP job tools' socket methods: `obor mcp` sends each tool call as one request with a Call (Agent, Role, Root from
// the agent's environment and cwd, Lane and Pane when set, a CallID per tool call; Task for task_done). The app answers
// job_next with the JSON of a JobReply as the reply line, task_done with "submitted" or the refusal.
const (
	MethodJobNext  = "job_next"  // the caller's open job, "none", or superseded by a newer one
	MethodTaskDone = "task_done" // the worker's report submits its task (row 16)
)

// JobReply is job_next's answer. State: "job" (the fields are set), "none", or "superseded" (Current names the
// caller's newer job id).
type JobReply struct {
	State   string `json:"state"`
	Job     string `json:"job,omitempty"`
	Task    string `json:"task,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Step    string `json:"step,omitempty"`
	Text    string `json:"text,omitempty"`
	Current string `json:"current,omitempty"`
}

// Call is one verdict request: who calls (OBOR_AGENT, OBOR_ROLE), from which project, its call id, and the task and
// job it answers. Job "" answers the task's current job. Patch is accept's; Note is accept's note and the why of a
// sendback, reject or hold; To is resume's regate, reprompt or retry ("": the default for the needs-you's cause).
// Lane, Types and Force are a dispatch's: the lane it names, else the first free lane of the types; force also takes a
// dirty-free lane and stops the agent's background tasks.
type Call struct {
	Root   string `json:"root"`
	Agent  string `json:"agent"`
	Role   string `json:"role"`
	CallID string `json:"call_id"`
	Task   string `json:"task"`
	Job    string `json:"job,omitempty"`
	Patch  string `json:"patch,omitempty"`
	Note   string `json:"note,omitempty"`
	To     string `json:"to,omitempty"`
	// A dispatch's.
	Lane  string   `json:"lane,omitempty"`
	Types []string `json:"types,omitempty"`
	Force bool     `json:"force,omitempty"`
	After []string `json:"after,omitempty"` // the tasks it waits for (obor dispatch --after)
	Level string   `json:"level,omitempty"` // set with Types by a waiting task's re-dispatch: the spec's level they came from
	// review_verify's: the finding numbers to re-run (none: every finding); After is its fix tasks.
	Findings []string `json:"findings,omitempty"`
	// A drop's (by Lane, T-439): discard the lane's uncommitted changes and unmerged commits.
	Discard bool `json:"discard,omitempty"`
	// The MCP job tools': the caller's herdr pane; task_done's report.
	Pane   string `json:"pane,omitempty"`
	Report string `json:"report,omitempty"`
	// spec_set's (specs.go): the spec's text.
	Spec string `json:"spec,omitempty"`
	// config_set's (configset.go, T-753, T-760): the changes, applied in order as one commit, and Base, the config.json
	// revision (config.Rev) the changes were made on ("": not checked); Source is where the owner made them ("web").
	Changes []ConfigChange `json:"changes,omitempty"`
	Base    string         `json:"base,omitempty"`
	// pause's (pause.go): its end (epoch seconds, 0: until cleared), or clear the pause in force; Note is its why.
	Until int64 `json:"until,omitempty"`
	Clear bool  `json:"clear,omitempty"`
	// send's (messages.go): the sender project's name; To is a brief's role agent. Prompt is a fresh start's first
	// prompt (obor restart --fresh --prompt): a brief without a line, recorded as the start's text (T-665).
	From   string `json:"from,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// stopped's (stopped.go): the API error that ended the agent's turn and its details (T-532).
	Error   string `json:"error,omitempty"`
	Details string `json:"details,omitempty"`
	// turn's (stopped.go): the hook is the turn's end (Stop); false: its start (UserPromptSubmit; T-571).
	Ended bool `json:"ended,omitempty"`
	// session's (sessions.go): the session id, its transcript file, the agent's kind, when it started (Unix seconds)
	// and who reports it; Pane, Lane and Task are where it started.
	Session    string `json:"session,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Started    int64  `json:"started,omitempty"`
	Source     string `json:"source,omitempty"`
}

// What obor resume does with a needs-you (docs/state-model.md row 40): gate the task again, send the integrator its
// job anew (a new job id: the old one is refused), or try the failed step again (its pending intent).
const (
	ResumeRegate   = "regate"
	ResumeReprompt = "reprompt"
	ResumeRetry    = "retry"
)

// ResumeArgs is obor resume's arguments, <T-xx> [regate|reprompt] ["<decision>"], as a call's Task, To and Note:
// regate and reprompt are the reserved modes, any other nonblank last argument is the decision (obor resume T-xx
// "yes", RV-54 #4). False: not that form.
func ResumeArgs(args []string) (Call, bool) {
	if len(args) == 0 || args[0] == "" {
		return Call{}, false
	}
	c, rest := Call{Task: args[0]}, args[1:]
	if len(rest) > 0 && (rest[0] == ResumeRegate || rest[0] == ResumeReprompt) {
		c.To, rest = rest[0], rest[1:]
	}
	if len(rest) == 1 && strings.TrimSpace(rest[0]) != "" {
		c.Note, rest = rest[0], nil
	}
	return c, len(rest) == 0
}

// Request and Answer are the app socket's lines: {"method":"status"} → {"status":…} or {"error":…}; a verdict
// {"method":"accept","call":{…}} (or hold, resume, dispatch …) → {"reply":…} or {"error":…}.
type Request struct {
	Method string `json:"method"`
	Call   *Call  `json:"call,omitempty"`
}

type Answer struct {
	Status *Status `json:"status,omitempty"`
	Reply  string  `json:"reply,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// ConfigChange is one change of a config_set call: an editable key (config.EditKeys) and its new value as JSON.
type ConfigChange struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}
