package wire

import (
	"encoding/json"
	"strings"

	"obor/internal/store"
)

// The app socket's protocol, client side: what a caller can ask serve (the methods), the request it sends for each
// and the answers it gets. The server (Listen and the handlers) is in internal/app. Its own package, so a caller takes
// the protocol without the core. Each method has its own request type (request.go): an Envelope and that method's fields, its doc
// comment the method's documented request.

// Verdict methods of the app socket: the integrator's (VerdictReq), the lead's and the owner's (DropReq, ResumeReq).
const (
	MethodAccept   = "accept"
	MethodSendback = "sendback"
	MethodReject   = "reject"
	MethodResolved = "resolved"
	MethodHold     = "hold"
	MethodDrop     = "drop"
	MethodResume   = "resume"
)

// StopPause is the why of the pause obor stop sets: obor start ends a pause in force with this why only, and
// serve starts no lane worker again while it holds (a stop stops them).
const StopPause = "stopping (obor stop)"

// The other methods of the app socket, each documented on its request type; the server's handler for each is in
// internal/app.
const (
	MethodDispatch    = "dispatch"     // DispatchReq
	MethodWatch       = "watch"        // WatchReq
	MethodPause       = "pause"        // PauseReq
	MethodReview      = "review"       // ReviewReq
	MethodSession     = "session"      // SessionReq
	MethodStarted     = "started"      // StartedReq
	MethodSpecSet     = "spec_set"     // SpecSetReq
	MethodConfig      = "config_set"   // ConfigSetReq
	MethodBackup      = "backup"       // BackupReq
	MethodSend        = "send"         // SendReq
	MethodBrief       = "brief"        // BriefReq
	MethodStopped     = "stopped"      // StoppedReq
	MethodTurn        = "turn"         // TurnReq
	MethodRestartJob  = "restart_job"  // RestartJobReq
	MethodExit        = "exit"         // ExitReq
	MethodDrive       = "drive"        // DriveReq
	MethodLand        = "land"         // LandReq
	MethodTellLead    = "tell_lead"    // TellLeadReq
	MethodIdle        = "idle"         // IdleReq
	MethodReviewClose = "review_close" // ReviewCloseReq
	MethodEvents      = "events"       // EventsReq
	MethodWhere       = "where"        // WhereReq
)

// The MCP job tools' socket methods: `obor mcp` sends each tool call as one request (JobNextReq, TaskDoneReq,
// VerdictReq).
const (
	MethodJobNext  = "job_next"
	MethodTaskDone = "task_done"
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

// The states of a WhereReply: the agent is in one pane (Pane), two or more panes claim it with equal proof (Panes),
// no pane proves it now (Why), or serve is not connected to the terminal: nothing is known, not "no agent".
const (
	WhereIdentified   = "identified"
	WhereAmbiguous    = "ambiguous"
	WhereNone         = "none"
	WhereNotConnected = "not connected"
)

// WhereReply is where's answer (WhereReq), serve's identity table as it is: State; Identified: Pane the pane id, Source
// the proof that holds it (session, start, label) and Label the pane's label as last observed; Ambiguous: Panes, the
// panes that claim the agent (sorted); None: Why (at its shell, exited, no proof). Runs is, for each pane the request
// named, the agent the table gives it ("": none, or not a pane of the project); empty while not connected.
type WhereReply struct {
	State  string            `json:"state"`
	Pane   string            `json:"pane,omitempty"`
	Source string            `json:"source,omitempty"`
	Label  string            `json:"label,omitempty"`
	Panes  []string          `json:"panes,omitempty"`
	Why    string            `json:"why,omitempty"`
	Runs   map[string]string `json:"runs,omitempty"`
}

// SessionRequest is the session request reporting s, began in the project at root: its caller is s's agent.
func SessionRequest(root string, s store.Session) *SessionReq {
	return &SessionReq{Envelope: Envelope{Root: root, Agent: s.Agent}, Session: &s}
}

// StartedRequest is the started request reporting agent's start in the project at root: as a session request, it names
// the agent that began, not the process that started it.
func StartedRequest(root, agent string) *StartedReq {
	return &StartedReq{Envelope: Envelope{Root: root, Agent: agent}}
}

// What obor resume does with a merge-loop needs-you (docs/state-model.md row 40), the reason of its unblocked fact:
// gate the task again, send the integrator its job anew (a new job id: the old one is refused), or try the failed step
// again (its pending intent). Regate and sendback are also obor resume's two flags (ResumeReq.Flag): the choice where
// both fit.
const (
	ResumeRegate   = "regate"
	ResumeReprompt = "reprompt"
	ResumeRetry    = "retry"
	ResumeSendback = "sendback"
)

// ResumeArgs is obor resume's arguments, <T-xx> ["<text>"] [--regate|--sendback], as a resume request's Task, Text and
// Flag: the flag anywhere after the task, at most one, and at most one nonblank text (obor resume T-xx "yes"). An
// argument is a flag only if it is exactly --regate or --sendback: anything else is the text, one
// starting with - or -- too ("- use A", "--force is safe"), and after -- every argument is (obor resume T-xx --
// --regate). False: not that form.
func ResumeArgs(args []string) (ResumeReq, bool) {
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return ResumeReq{}, false
	}
	r := ResumeReq{Task: args[0]}
	flags := true
	for _, a := range args[1:] {
		switch {
		case flags && a == "--":
			flags = false
		case flags && (a == "--"+ResumeRegate || a == "--"+ResumeSendback):
			if r.Flag != "" {
				return ResumeReq{}, false
			}
			r.Flag = strings.TrimPrefix(a, "--")
		case r.Text != "" || strings.TrimSpace(a) == "":
			return ResumeReq{}, false
		default:
			r.Text = a
		}
	}
	return r, true
}

// Request and Answer are the app socket's lines: {"method":"status"} → {"status":…} or {"error":…}; a method's
// {"method":"accept","call":{…}} (or hold, resume, dispatch …) → {"reply":…} or {"error":…}. Call is the
// method's request type as JSON (Req): serve reads its Envelope first, then the method's type.
type Request struct {
	Method string          `json:"method"`
	Call   json.RawMessage `json:"call,omitempty"`
}

type Answer struct {
	Status *Status     `json:"status,omitempty"`
	Reply  string      `json:"reply,omitempty"`
	Error  string      `json:"error,omitempty"`
	Record *FeedRecord `json:"record,omitempty"` // one record of the events feed (MethodEvents)
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
// of a lane agent sends it too.
func WakeLine(job string) string { return JobLine(job, WakeText) }
