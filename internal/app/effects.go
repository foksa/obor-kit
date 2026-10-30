package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// The effects the core drives through adapters (docs/module-map.md: adapters reach each other only through these):
// Agents starts and stops agents (launch), Typer types job wakes and lines into their panes, PaneControl is what launch
// itself needs of the terminal multiplexer (the herdr socket client implements it).

// Agents starts and stops obor agents in their panes; the kill sequence is the implementation's (launch.Launcher).
type Agents interface {
	// Launch starts req.Agent (a lane's agent, a role, an agents entry) in its pane, its kind and args from its lane
	// type or role, after stopping what runs there. Returns the one-line result and the agent it started.
	Launch(ctx context.Context, req Launch) (Started, error)
	// Stop quits agent in pane ("": the pane labelled agent) and leaves the pane at its shell; force also stops a
	// working one. Returns the STOPPED line.
	Stop(ctx context.Context, agent, pane string, force bool) (string, error)
}

// Started is an agent start's outcome: its one-line result, the pane it ran the agent in and that pane's terminal id,
// the agent's kind and the session id herdr reported for it ("": none, or nothing was started: a scheduled restart of
// the caller's own pane).
type Started struct {
	Line, Pane, Terminal, Kind, Session string
}

// Launch is one agent (re)start: Agent in Pane ("": the pane labelled Agent), resuming Sid (else the recorded
// session) or Fresh; Force stops a working agent first.
type Launch struct {
	Agent, Pane, Sid string
	Fresh, Force     bool
	// Prompt is the CLI's first prompt for a fresh session: a job's wake.
	Prompt string
}

// Typer types text into an agent's pane as it is (a job's wake with its [obor job <id>] tag, or a line carrying its
// own [obor …]) and submits it. clear: the input line is cleared first (an earlier wake a dialog ate may sit there).
// Delivery is proven later by the agent (a job's job_next), not by this call. began: a byte of the text may have
// reached the pane (false only when the send was refused before any typing).
type Typer interface {
	Type(ctx context.Context, pane, text string, clear bool) (began bool, err error)
}

// JobID is the id of the job (task, attempt, step): <task>/<attempt>/<step> (docs/state-model.md "The log").
func JobID(k Key) string { return k.Task + "/" + strconv.Itoa(k.Attempt) + "/" + k.Step }

// PaneControl is the pane operations launch needs: find agents and panes, look at a pane, type into it, start an
// agent CLI in it. A "not found" error matches NotFound; ErrorCode reads the adapter's error code.
type PaneControl interface {
	Agent(ctx context.Context, target string) (AgentInfo, error)
	Panes(ctx context.Context) ([]PaneInfo, error)
	ProcessInfo(ctx context.Context, pane string) (ProcessInfo, error)
	// Screen is the pane's visible text, its last lines.
	Screen(ctx context.Context, pane string, lines int) (string, error)
	RenamePane(ctx context.Context, pane, label string) error   // label "" clears it
	RenameAgent(ctx context.Context, target, name string) error // name "" clears it
	// Type is text plus Enter, submitted at once.
	Type(ctx context.Context, pane, text string) error
	SendKeys(ctx context.Context, pane string, keys ...string) error
	// Start starts agent CLI kind in pane under name, returning once the agent runs there and is named: the agent as
	// the multiplexer answered, or as it shows it in the pane when the answer came later (its Pane "" when neither
	// named one).
	Start(ctx context.Context, name, kind, pane string, args ...string) (AgentInfo, error)
}

// NotFound: err says the pane or agent asked about does not exist (an error in the chain with NotFound() true).
func NotFound(err error) bool {
	var e interface{ NotFound() bool }
	return errors.As(err, &e) && e.NotFound()
}

// ErrorCode is the adapter's error code in err (herdr's agent_name_taken, agent_not_ready, …), "" when none.
func ErrorCode(err error) string {
	var e interface{ ErrorCode() string }
	if errors.As(err, &e) {
		return e.ErrorCode()
	}
	return ""
}

// AgentInfo is one agent the multiplexer recognises in a pane.
type AgentInfo struct {
	Name    string // "" when herdr has no name for it (it exited, or a restore dropped it)
	Pane    string
	Status  string // idle, working, blocked, done, unknown
	Cwd     string // no trailing /
	Kind    string // claude, codex, …
	Session string // the agent's session id, when herdr knows it
	Seq     int64  // state_change_seq: grows with each status change
	// Workspace is the herdr workspace id; Transcript is Session when herdr reports it as a path (agent_session kind
	// path: the agent's transcript file), else "".
	Workspace, Transcript string
}

// PaneInfo is one pane; the agent fields are empty when it runs none.
type PaneInfo struct {
	ID  string
	Tab string
	// Terminal is herdr's terminal_id: kept when the pane moves (pane.move gives it a new ID), "" when herdr reports none.
	Terminal string
	Label    string
	Cwd      string
	Kind     string
	Status   string
	Session  string
	// Workspace is the herdr workspace id; Transcript is Session when herdr reports it as a path (agent_session kind
	// path: the agent's transcript file), else "".
	Workspace, Transcript string
}

// Process is one foreground process of a pane.
type Process struct {
	PID     int      `json:"pid"`
	Name    string   `json:"name"`
	Argv0   string   `json:"argv0"`
	Argv    []string `json:"argv"`
	Cmdline string   `json:"cmdline"`
	Cwd     string   `json:"cwd"`
}

// ProcessInfo is a pane's shell and the processes of its foreground process group (an agent and what it runs in that
// group, e.g. an `obor run` started by Claude's Bash tool).
type ProcessInfo struct {
	Pane      string    `json:"pane_id"`
	ShellPID  int       `json:"shell_pid"`
	FgPGID    int       `json:"foreground_process_group_id"` // == ShellPID: the shell has the foreground
	Processes []Process `json:"foreground_processes"`
}

// AtShell: the pane's shell has the foreground (false when herdr did not say).
func (p ProcessInfo) AtShell() bool { return p.ShellPID != 0 && p.FgPGID == p.ShellPID }

// Foreground is the program in the pane's foreground: the group leader's argv0 without its path ("" when unknown).
func (p ProcessInfo) Foreground() string {
	for _, pr := range p.Processes {
		if pr.PID == p.FgPGID {
			return pr.Program()
		}
	}
	if len(p.Processes) > 0 {
		return p.Processes[0].Program()
	}
	return ""
}

// Program is the process's argv0 (else its name) without its path.
func (p Process) Program() string {
	s := p.Argv0
	if s == "" {
		s = p.Name
	}
	return s[strings.LastIndex(s, "/")+1:]
}

// Candidate is a checked merge waiting to be published, pinned at refs/obor/candidate/<task> until it is published or
// rejected: the commit the checks passed on and the target it was checked against.
type Candidate struct {
	Task, Lane string
	SHA        string // the checked merge commit (its first parent is Target)
	Target     string // the target sha the checks ran against
	Patch      string // sha256 of git diff Target SHA: an accept is bound to it (docs/state-model.md)
	Checks     string // the checks summary ("" when none ran)
	Stat       string // git diff --stat Target SHA
	Full       bool   // a full check ran (fact merged's full: obor serve's nightly reads it)
}

// Scratch is the merge loop's git side: serve's own worktree, the target branch, the pins under refs/obor/.
// An outcome is final for its step; an error is "not decided": the step runs again (App.do's failure rule).
type Scratch interface {
	// Submit commits the lane's uncommitted work (a leftover done file never) and pins its HEAD at
	// refs/obor/submitted/<task>/<id>: that sha, the gate's input. id is the submit step's identity (submitID): a pin
	// it already has is returned as it is. A final refusal (no lane dir, not on a branch, already in the target) is a
	// Refused error.
	Submit(ctx context.Context, task, id, lane, summary, worker string) (sha string, err error)
	// Gate resets serve's worktree, merges or takes in.Input there, checks it, commits and pins the candidate.
	Gate(ctx context.Context, in GateInput) (GateOutcome, error)
	// Resolved pins the merge the integrator resolved and committed in serve's worktree at refs/obor/resolved/<task>:
	// its sha (the verdict's pin). It must be the merge of input (the conflicting gate's) onto the target; anything
	// else (an open merge, HEAD in the target, another merge) is a Refused error.
	Resolved(ctx context.Context, task, input string) (sha string, err error)
	// Publish fast-forwards the target to c.SHA when it is still c.Target, then deletes the task's pins.
	Publish(ctx context.Context, c Candidate) (PublishOutcome, error)
	// AfterMerge runs commands.after_merge for the published c: ran | skipped | WARN …. Its intent (G) is the once:
	// a cut before its result runs it again (after_merge is idempotent).
	AfterMerge(ctx context.Context, c Candidate) (string, error)
	// Landed: the target holds merge evidence of task (merged outside the app); an error: git cannot tell.
	Landed(ctx context.Context, task string) (bool, error)
	// Unpin deletes a rejected task's pins (an absent pin is not an error).
	Unpin(ctx context.Context, task string) error
}

// GateInput is one gate: Input is the sha gated, per Kind: InputLane submit's sha (merged onto the target),
// InputResolved the verdict's pin (checked as committed), InputMoved submit's sha again (merged onto the moved target,
// never the old candidate: one merge commit per task, T-403). Summary is the merge's subject: "Merge <task>: <summary>",
// the same for a moved gate (T-360). Spec is the task's spec and SpecRev its rev, the store's current one (T-440): the
// gate puts both into the merge commit's message (T-521; a resolved gate amends the integrator's message), and the
// review job names that rev; "" (none stored, a task dispatched before T-440) adds nothing.
type GateInput struct {
	Task, Lane, Kind, Input string
	Summary                 string
	Spec                    string
	SpecRev                 int
}

// Gate input kinds.
const (
	InputLane     = "lane"
	InputResolved = "resolved"
	InputMoved    = "moved"
)

// GateOutcome is a gate's final outcome: Kind OutcomeGreen with the pinned Candidate; OutcomeRed or OutcomeFail with
// Line; OutcomeConflict with the conflicting Files, left open in Dir.
type GateOutcome struct {
	Kind      string
	Candidate Candidate
	Line      string
	Files     []string
	Dir       string
}

// Gate outcome kinds.
const (
	OutcomeGreen    = "green"
	OutcomeRed      = "red"      // a check failed
	OutcomeConflict = "conflict" // the merge conflicts; it stays open in Dir to be resolved
	OutcomeFail     = "fail"     // the checks cannot pass: lane_setup, a missing command
)

// PublishOutcome is a publish's final outcome: PublishMerged, PublishAlready (the target holds the candidate),
// PublishMoved (the target is at Head, not the candidate's), PublishHold (uncommitted Files in the main checkout are
// among the merged ones: nothing merged).
type PublishOutcome struct {
	Kind  string
	Head  string
	Files []string
}

// Publish outcome kinds.
const (
	PublishMerged  = "merged"
	PublishAlready = "already"
	PublishMoved   = "moved"
	PublishHold    = "hold"
)

// Refused is a final refusal returned as an error (Submit, Resolved): the step is not run again.
type Refused struct{ Line string }

func (r Refused) Error() string { return r.Line }

// LaneSetup prepares a lane for a dispatched task (internal/checks implements it).
type LaneSetup interface {
	// Prepare puts the lane at in.Dir on in.Branch at in.Target (in.Redo: the branch is kept as it is), runs
	// lane_ready, then lane_setup and lane_ready again when it was red. It returns the base sha and the lane's head
	// (merge-base(HEAD, target)): its lane row's (serve's projection, T-439). Repeating it converges on the same state.
	// A final refusal (no lane dir, lane_setup red, lane_ready still red) is a Refused error; anything else (git I/O)
	// is an error to retry.
	Prepare(ctx context.Context, in PrepareInput) (Prepared, error)
	// Free gives a dropped task's lane back (obor drop, T-439): refused (Refused) when it has uncommitted changes or
	// its branch has commits not in in.Target, unless in.Discard (then they are discarded); the lane is detached at
	// in.Target and its branch deleted. Repeating it converges (a missing branch is gone already). Its line says
	// what it discarded ("" nothing).
	Free(ctx context.Context, in FreeInput) (line string, err error)
}

// Prepared is lane.prepare's outcome: the base sha the task branch is on, and the lane's head (merge-base(HEAD,
// base)).
type Prepared struct{ Base, Head string }

// FreeInput is one lane freed by a drop: the lane's worktree, the task branch and the base it goes back to.
type FreeInput struct {
	Lane, Dir, Branch, Target string
	Discard                   bool
}

// PrepareInput is one lane.prepare step: Target is the target branch's sha read when the dispatch fact was
// recorded; Agent the lane's worker agent.
type PrepareInput struct {
	Task, Lane, Dir, Branch, Target, Agent string
	Redo                                   bool
	At                                     time.Time
}
