package wire

import "obor/internal/store"

// The app socket's requests (T-1055): one type per method, each an Envelope and its method's fields only, its doc
// comment the method's documented request. The JSON keys are the ones every obor before the split sent (one union
// struct, a key per field: "note" carries a hold's why, resume's text, spec_set's what-changed …), so a request of
// either side reads the same; a key a method does not use is ignored, as any unknown key.

// Envelope is every request's caller: who calls (its derived agent name, project.Self, T-786, and its role; both empty:
// the owner), from which project, and the call's id (a repeated id gets serve's stored reply, so every call has its
// own: NewCallID). It is embedded first in each request type, its keys flat in the request's JSON.
type Envelope struct {
	Root  string `json:"root"`
	Agent string `json:"agent"`
	Role  string `json:"role"`
	// Version is the calling binary's version.Version (T-826), stamped by the client on every call (Verdict): an empty
	// one is a binary older than the field, and serve names either difference in its refusal.
	Version string `json:"version,omitempty"`
	CallID  string `json:"call_id"`
}

// Env is the request's envelope, for a caller filling any request's identity (obor mcp, the CLI).
func (e *Envelope) Env() *Envelope { return e }

// Req is a request of the app socket: its method and its envelope. The request types implement it as pointers
// (*AcceptReq …: the client stamps the envelope's version).
type Req interface {
	Method() string
	Env() *Envelope
}

// VerdictReq is the integrator's verdict on the job it answers (its MCP tools; obor mcp fills Task and Job from the job
// job_next fetched last). Job "" answers the task's current job. Verb is the method:
//   - accept: the candidate whose patch hash Patch names may be published, Note a note for the merge log;
//   - sendback: back to the worker with its notes (Note: why; the lead's is obor resume --sendback);
//   - reject: drop the patch (a resolve job: the resolve failed; Note: why);
//   - resolved: the conflict is merged in the scratch: gate it again;
//   - hold: the owner must decide: needs-you{held: Note}, the job stays open.
//
// Reply: <VERB> <task> (job <id>). Refused: no call id or task, not the project's integrator, the task dropped, no job
// open or a stale one, a job that does not take the verb, an accept's stale patch, a sendback, reject or hold without
// its why.
type VerdictReq struct {
	Envelope
	Verb  string `json:"-"`
	Task  string `json:"task"`
	Job   string `json:"job,omitempty"`
	Patch string `json:"patch,omitempty"`
	Note  string `json:"note,omitempty"`
}

func (r VerdictReq) Method() string { return r.Verb }

// DropReq is the lead's or the owner's drop (obor drop): the task Task ended for good, whatever it waits for, Why why.
// Force also stops a busy worker; Discard (T-439) discards the task's lane's uncommitted changes and unmerged commits.
// Reply: DROPPED <task>, or DROPPING <task> while its worker stops.
type DropReq struct {
	Envelope
	Task    string `json:"task"`
	Why     string `json:"note,omitempty"`
	Force   bool   `json:"force,omitempty"`
	Discard bool   `json:"discard,omitempty"`
}

func (DropReq) Method() string { return MethodDrop }

// ResumeReq is the lead's or the owner's word to the task Task (obor resume T-xx ["<text>"] [--regate|--sendback],
// ResumeArgs): serve picks the action from the task's state (a question's answer, a stalled or stopped worker's line, a
// drive's line, a sendback, a needs-you's decision), Text the word, Flag ResumeRegate or ResumeSendback where two fit
// ("": the action the task's state takes).
type ResumeReq struct {
	Envelope
	Task string `json:"task"`
	Text string `json:"note,omitempty"`
	Flag string `json:"to,omitempty"`
}

func (ResumeReq) Method() string { return MethodResume }

// DispatchReq is the lead's or the owner's dispatch (obor dispatch): Task onto Lane, else the first free lane of Types.
// Force also takes a dirty-free lane and stops the agent's background tasks; After the tasks it waits for (obor
// dispatch --after); Level is set with Types by a waiting task's re-dispatch: the spec's level they came from. Reply:
// DISPATCHED <task> → <lane> (queued), or WAITING <task> … (queued).
type DispatchReq struct {
	Envelope
	Task  string   `json:"task"`
	Lane  string   `json:"lane,omitempty"`
	Types []string `json:"types,omitempty"`
	Force bool     `json:"force,omitempty"`
	After []string `json:"after,omitempty"`
	Level string   `json:"level,omitempty"`
}

func (DispatchReq) Method() string { return MethodDispatch }

// WatchReq is the lead's or the owner's watch (obor watch): Task the task or comma list watched ("": the open watches
// listed), Note the lead's note on the watch, Clear ends its watch (--rm). Reply: WATCHING <set>, the set's outcome,
// UNWATCHED <set>, or the open watches.
type WatchReq struct {
	Envelope
	Task  string `json:"task"`
	Note  string `json:"note,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}

func (WatchReq) Method() string { return MethodWatch }

// PauseReq is a pause (obor pause; pause.go): Why its why, Until its end (epoch seconds, 0: until cleared), Model the
// model it holds (obor pause --model, T-765; "": the project's pause); Clear ends the pause in force.
type PauseReq struct {
	Envelope
	Why   string `json:"note,omitempty"`
	Until int64  `json:"until,omitempty"`
	Model string `json:"model,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}

func (PauseReq) Method() string { return MethodPause }

// ReviewReq is the lead's review request (obor review): After the tasks it waits for, Note the lead's note; Clear ends
// the open request.
type ReviewReq struct {
	Envelope
	Note  string   `json:"note,omitempty"`
	After []string `json:"after,omitempty"`
	Clear bool     `json:"clear,omitempty"`
}

func (ReviewReq) Method() string { return MethodReview }

// SessionReq is an agent's session that began (its SessionStart hook, a launch; sessions.go, T-887): the session as
// serve records it, the one session shape (SessionRequest builds it: the envelope's Agent is its agent). Its key is not
// the flat "session" id of the obor before it: a serve of either side reads the other's request and refuses it with
// both versions named (no session), never as an unreadable request.
type SessionReq struct {
	Envelope
	Session *store.Session `json:"session_fact,omitempty"`
}

func (SessionReq) Method() string { return MethodSession }

// SpecSetReq is the lead's or the owner's spec (obor spec set; specs.go): Spec stored as Task's spec, Changed what
// changed (T-819). Reply: SET <task> rev <n> (T-824), or UNCHANGED <task> rev <n>.
type SpecSetReq struct {
	Envelope
	Task    string `json:"task"`
	Changed string `json:"note,omitempty"`
	Spec    string `json:"spec,omitempty"`
}

func (SpecSetReq) Method() string { return MethodSpecSet }

// ConfigSetReq is the owner's setup edits (obor web, obor checks; configset.go, T-753, T-760): Changes in
// .obor/config.json, applied in order as one commit on main; Base the config.json revision (config.Rev) they were made
// on ("": not checked); Source where the owner made them ("web").
type ConfigSetReq struct {
	Envelope
	Changes []ConfigChange `json:"changes,omitempty"`
	Base    string         `json:"base,omitempty"`
	Source  string         `json:"source,omitempty"`
}

func (ConfigSetReq) Method() string { return MethodConfig }

// BackupReq asks serve to take a backup of the envelope's Root's database (obor nightly, T-435): serve is its one
// writer.
type BackupReq struct {
	Envelope
}

func (BackupReq) Method() string { return MethodBackup }

// SendReq is a line to another project's lead (obor send; messages.go): the envelope's Root the target project, From
// the sender project's name, Text the text.
type SendReq struct {
	Envelope
	Text string `json:"note,omitempty"`
	From string `json:"from,omitempty"`
}

func (SendReq) Method() string { return MethodSend }

// BriefReq is the lead's or the owner's brief (obor brief; messages.go): To the role agent, Text the brief ("": the
// restart's first prompt carried it). Prompt is a fresh start's first prompt (a review's start, the one brief without a
// line): recorded as the start's text (T-665, T-785). Fresh (obor brief --fresh, T-794): serve stops the running agent
// and starts it fresh on the brief, once the brief is recorded.
type BriefReq struct {
	Envelope
	Text   string `json:"note,omitempty"`
	To     string `json:"to,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Fresh  bool   `json:"fresh,omitempty"`
}

func (BriefReq) Method() string { return MethodBrief }

// StoppedReq is the StopFailure hook's (stopped.go, T-532): the envelope's agent (and role) stopped, Lane a worker's
// lane, Error the API error that ended its turn and Details its details.
type StoppedReq struct {
	Envelope
	Lane    string `json:"lane,omitempty"`
	Error   string `json:"error,omitempty"`
	Details string `json:"details,omitempty"`
}

func (StoppedReq) Method() string { return MethodStopped }

// TurnReq is an agent's next turn that began or ended (its UserPromptSubmit or Stop hook; stopped.go): Lane a worker's
// lane, Ended the hook is the turn's end (Stop); false: its start (UserPromptSubmit; T-571).
type TurnReq struct {
	Envelope
	Lane  string `json:"lane,omitempty"`
	Ended bool   `json:"ended,omitempty"`
}

func (TurnReq) Method() string { return MethodTurn }

// RestartJobReq is obor restart's (T-959): the job an agent's restart carries as its wake — the job job_next would
// hand the envelope's agent (with its role; Lane a worker's lane), with the same predicate and order. Reply: that job
// id, or "" when the agent has none.
type RestartJobReq struct {
	Envelope
	Lane string `json:"lane,omitempty"`
}

func (RestartJobReq) Method() string { return MethodRestartJob }

// ExitReq is the owner's or the lead's (obor stop, T-711): serve ends gracefully, as a signal ends it and as its
// self-restart does (T-652, T-656): the envelope's Root its project, Why why. The reply is EXITING; the caller
// confirms serve down by its serve.flock and socket.
type ExitReq struct {
	Envelope
	Why string `json:"note,omitempty"`
}

func (ExitReq) Method() string { return MethodExit }

// DriveReq is the owner's or the lead's (obor drive, T-751): Name the drive's name for a new drive (a new task on the
// first free untyped lane, Types its one type reference), else Task a drive's own task (its retry; any other task
// refused, T-912). The reply is DRIVE <task> → <lane> (<agent>, <type>): the CLI starts the drive agent.
type DriveReq struct {
	Envelope
	Task  string   `json:"task"`
	Name  string   `json:"note,omitempty"`
	Types []string `json:"types,omitempty"`
}

func (DriveReq) Method() string { return MethodDrive }

// LandReq is the owner's, the lead's or the drive agent's (obor land): Task the driving task, Report the summary its
// submission carries. The reply is LANDED <task>: submitted.
type LandReq struct {
	Envelope
	Task   string `json:"task"`
	Report string `json:"report,omitempty"`
}

func (LandReq) Method() string { return MethodLand }

// TellLeadReq is the drive agent's (obor tell-lead, T-761): Lane the drive's lane, Text the text. Serve posts
// "[obor <task> drive] <text> (reply: obor resume <task> "…")" to the lead as a lead line. The reply is TOLD ….
type TellLeadReq struct {
	Envelope
	Text string `json:"note,omitempty"`
	Lane string `json:"lane,omitempty"`
}

func (TellLeadReq) Method() string { return MethodTellLead }

// IdleReq is the lead's or the owner's (obor idle, T-803): Why the why nothing is in flight acknowledges the idle state
// ("" shows it). The reply is ACKED idle: <why>, or the state (NOT IDLE, IDLE …).
type IdleReq struct {
	Envelope
	Why string `json:"note,omitempty"`
}

func (IdleReq) Method() string { return MethodIdle }

// ReviewCloseReq is the lead's or the owner's (obor review close, T-926): Summary the summary, Accept each accepted
// finding's line for BACKLOG.md's Open section, Force closes with fixes pending. Serve commits the close on the
// integration branch and records fact review.closed; the reply is CLOSED <subject>.
type ReviewCloseReq struct {
	Envelope
	Summary string   `json:"note,omitempty"`
	Force   bool     `json:"force,omitempty"`
	Accept  []string `json:"accept,omitempty"`
}

func (ReviewCloseReq) Method() string { return MethodReviewClose }

// EventsReq is anyone's, as status is (obor events, T-1039): the event feed, the records after Seq (the last seq the
// caller has; 0: from the first record) in seq order, one Answer{Record} line each, then Answer{Reply: FeedEnd};
// Follow keeps the connection and streams each new record until the caller or serve closes it (feed.go).
type EventsReq struct {
	Envelope
	Seq    int64 `json:"seq,omitempty"`
	Follow bool  `json:"follow,omitempty"`
}

func (EventsReq) Method() string { return MethodEvents }

// JobNextReq is the MCP job_next tool's (obor mcp: the envelope from the agent's environment and cwd, a call id per
// tool call): Job the job id the wake names (that job when it is open; "" the caller's oldest), Lane the caller's lane
// (OBOR_LANE) and Pane its herdr pane, when set. The reply is the JSON of a JobReply: the caller's open job, "none", or
// superseded by a newer one.
type JobNextReq struct {
	Envelope
	Job  string `json:"job,omitempty"`
	Lane string `json:"lane,omitempty"`
	Pane string `json:"pane,omitempty"`
}

func (JobNextReq) Method() string { return MethodJobNext }

// TaskDoneReq is the MCP task_done tool's: the worker's Report submits its task (row 16), Task the task, else the one
// Lane (the caller's OBOR_LANE) holds (T-439). The reply is "submitted" or the refusal.
type TaskDoneReq struct {
	Envelope
	Task   string `json:"task"`
	Lane   string `json:"lane,omitempty"`
	Report string `json:"report,omitempty"`
}

func (TaskDoneReq) Method() string { return MethodTaskDone }
