package wire

import (
	"encoding/json"
	"strings"

	"obor/internal/store"
)

// The app socket's protocol, client side: what a caller can ask serve (the methods), the Call it sends and the
// answers it gets. The server (Listen and the handlers) is in internal/app. Moved here from internal/app (T-692, app
// split 3/5).

// Verdict methods of the app socket.
const (
	MethodAccept   = "accept"   // the candidate whose patch hash it names may be published
	MethodSendback = "sendback" // the integrator's: back to the worker with its notes (the lead's is obor resume --sendback)
	MethodReject   = "reject"   // drop the patch (a resolve job: the resolve failed)
	MethodResolved = "resolved" // the conflict is merged in the scratch: gate it again
	MethodHold     = "hold"     // the owner must decide: needs-you{held: why}, the job stays open
	MethodDrop     = "drop"     // the lead's or the owner's: end the task for good (Call.Note: why), whatever it waits for
	MethodResume   = "resume"   // the lead's or the owner's word to a task (Call.Note; Call.To: "", regate, sendback): serve picks the action
)

// StopPause is the why of the pause obor stop sets (T-709): obor start ends a pause in force with this why only, and
// serve starts no lane worker again while it holds (T-874: the stop stops them).
const StopPause = "stopping (obor stop)"

// The other methods of the app socket; the server's handler for each is in internal/app.
const (
	MethodDispatch = "dispatch"   // Call.Task onto Call.Lane, or the first free lane of Call.Types
	MethodWatch    = "watch"      // Call.Task the task or comma list watched (""; the open watches listed), Call.Clear ends its watch
	MethodPause    = "pause"      // Call.Note the why, Call.Until its end (0: until cleared); Call.Clear ends the pause in force
	MethodReview   = "review"     // Call.After the tasks it waits for, Call.Note the lead's note, Call.Clear ends the open request
	MethodSession  = "session"    // Call.Session began (SessionCall: Call.Agent is its agent)
	MethodSpecSet  = "spec_set"   // Call.Spec stored as Call.Task's spec, Call.Note what changed (T-819); the reply "SET <task> rev <n>" (T-824), or "UNCHANGED <task> rev <n>"
	MethodConfig   = "config_set" // the owner's setup edits (obor web, T-753, T-760): Call.Changes in .obor/config.json, one commit on main
	MethodBackup   = "backup"     // serve takes a backup of Call.Root's database (obor nightly, T-435): serve is its one writer
	MethodSend     = "send"       // Call.Root the target project, Call.From the sender project, Call.Note the text
	MethodBrief    = "brief"      // Call.To the role agent, Call.Note the brief ("": the restart's first prompt carried it)
	MethodStopped  = "stopped"    // Call.Agent the agent, Call.Role its role, Call.Lane a worker's lane, Call.Error, Call.Details
	MethodTurn     = "turn"       // an agent's next turn began or ended (its UserPromptSubmit or Stop hook): Call.Agent, Call.Lane
	// MethodExit (the owner's or the lead's; obor stop, T-711): serve ends gracefully, as a signal ends it and as its
	// self-restart does (T-652, T-656): Call.Root its project, Call.Note why. The reply is EXITING; the caller confirms
	// serve down by its serve.flock and socket.
	MethodExit = "exit"
	// MethodDrive (the owner's or the lead's; obor drive, T-751): Call.Note the drive's name for a new drive (a new task
	// on the first free untyped lane, Call.Types its one type reference), else Call.Task a drive's own task (its retry;
	// any other task refused, T-912). The reply is DRIVE <task> → <lane> (<agent>, <type>): the CLI starts the drive
	// agent.
	MethodDrive = "drive"
	// MethodLand (the owner's, the lead's or the drive agent's; obor land): Call.Task the driving task, Call.Report the
	// summary its submission carries. The reply is LANDED <task>: submitted.
	MethodLand = "land"
	// MethodTellLead (the drive agent's; obor tell-lead, T-761): Call.Lane the drive's lane, Call.Note the text. Serve posts
	// "[obor <task> drive] <text> (reply: obor resume <task> "…")" to the lead as a lead line. The reply is TOLD ….
	MethodTellLead = "tell_lead"
	// MethodIdle (the lead's or the owner's; obor idle, T-803): Call.Note the why nothing is in flight acknowledges the
	// idle state ("" shows it). The reply is ACKED idle: <why>, or the state (NOT IDLE, IDLE …).
	MethodIdle = "idle"
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

// Call is one verdict request: who calls (its derived agent name, project.Self, T-786, and its role), from which project, its call id, and the task and
// job it answers. Job "" answers the task's current job. Patch is accept's; Note is accept's note and the why of a
// sendback, reject or hold, resume's text, and spec_set's what-changed (T-819); To is resume's flag: regate or
// sendback ("": the action the task's state takes).
// Lane, Types and Force are a dispatch's: the lane it names, else the first free lane of the types; force also takes a
// dirty-free lane and stops the agent's background tasks.
type Call struct {
	Root  string `json:"root"`
	Agent string `json:"agent"`
	Role  string `json:"role"`
	// Version is the calling binary's version.Version (T-826), stamped by the client on every call (Verdict): an empty
	// one is a binary older than the field, and serve names either difference in its refusal.
	Version string `json:"version,omitempty"`
	CallID  string `json:"call_id"`
	Task    string `json:"task"`
	Job     string `json:"job,omitempty"`
	Patch   string `json:"patch,omitempty"`
	Note    string `json:"note,omitempty"`
	To      string `json:"to,omitempty"`
	// A dispatch's.
	Lane  string   `json:"lane,omitempty"`
	Types []string `json:"types,omitempty"`
	Force bool     `json:"force,omitempty"`
	After []string `json:"after,omitempty"` // the tasks it waits for (obor dispatch --after)
	Level string   `json:"level,omitempty"` // set with Types by a waiting task's re-dispatch: the spec's level they came from
	// A drop's (T-439): discard the task's lane's uncommitted changes and unmerged commits.
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
	Source  string         `json:"source,omitempty"`
	// pause's (pause.go): its end (epoch seconds, 0: until cleared), or clear the pause in force; Note is its why.
	Until int64 `json:"until,omitempty"`
	// Model is the model a pause holds (obor pause --model, T-765): "": the project's pause.
	Model string `json:"model,omitempty"`
	Clear bool   `json:"clear,omitempty"`
	// send's (messages.go): the sender project's name; To is a brief's role agent. Prompt is a fresh start's first
	// prompt (a review's start, the one brief without a line): recorded as the start's text (T-665, T-785).
	From   string `json:"from,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// brief's (obor brief --fresh, T-794): serve stops the running agent and starts it fresh on the brief, once the
	// brief is recorded.
	Fresh bool `json:"fresh,omitempty"`
	// stopped's (stopped.go): the API error that ended the agent's turn and its details (T-532).
	Error   string `json:"error,omitempty"`
	Details string `json:"details,omitempty"`
	// turn's (stopped.go): the hook is the turn's end (Stop); false: its start (UserPromptSubmit; T-571).
	Ended bool `json:"ended,omitempty"`
	// session's (sessions.go, T-887): the session as serve records it, the one session shape (SessionCall builds the
	// call). Its key is not the flat "session" id of the obor before it: a serve of either side reads the other's call
	// and refuses it with both versions named (no session), never as an unreadable request.
	Session *store.Session `json:"session_fact,omitempty"`
}

// SessionCall is the session request reporting s, began in the project at root: its caller is s's agent.
func SessionCall(root string, s store.Session) Call {
	return Call{Root: root, Agent: s.Agent, Session: &s}
}

// What obor resume does with a merge-loop needs-you (docs/state-model.md row 40), the reason of its unblocked fact:
// gate the task again, send the integrator its job anew (a new job id: the old one is refused), or try the failed step
// again (its pending intent). Regate and sendback are also obor resume's two flags (Call.To): the choice where both fit.
const (
	ResumeRegate   = "regate"
	ResumeReprompt = "reprompt"
	ResumeRetry    = "retry"
	ResumeSendback = "sendback"
)

// ResumeArgs is obor resume's arguments, <T-xx> ["<text>"] [--regate|--sendback], as a call's Task, Note and To: the
// flag anywhere after the task, at most one, and at most one nonblank text (obor resume T-xx "yes", RV-54 #4). An
// argument is a flag only if it is exactly --regate or --sendback (RV-99 #5): anything else is the text, one starting
// with - or -- too ("- use A", "--force is safe"), and after -- every argument is (obor resume T-xx -- --regate). False:
// not that form.
func ResumeArgs(args []string) (Call, bool) {
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return Call{}, false
	}
	c := Call{Task: args[0]}
	flags := true
	for _, a := range args[1:] {
		switch {
		case flags && a == "--":
			flags = false
		case flags && (a == "--"+ResumeRegate || a == "--"+ResumeSendback):
			if c.To != "" {
				return Call{}, false
			}
			c.To = strings.TrimPrefix(a, "--")
		case c.Note != "" || strings.TrimSpace(a) == "":
			return Call{}, false
		default:
			c.Note = a
		}
	}
	return c, true
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

// WakeText is a job's wake for an agent that fetches it: the line after its [obor job <id>] tag (JobLine).
const WakeText = "call the obor job_next tool"

// JobLine is text as job's prompt: the job tag before it.
func JobLine(job, text string) string { return "[obor job " + job + "] " + text }

// WakeLine is job's wake as one line with its tag, as serve types it and as a fresh start's first prompt: obor restart
// of a lane agent sends it too (T-773).
func WakeLine(job string) string { return JobLine(job, WakeText) }
