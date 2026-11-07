package port

import (
	"context"
	"errors"
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
	// working one (up to StopIdle to go idle after Escape). Returns the STOPPED line.
	Stop(ctx context.Context, agent, pane string, force bool) (string, error)
}

// Started is an agent start's outcome: its one-line result, the pane it ran the agent in and that pane's terminal id,
// the agent's kind and the session id herdr reported for it ("": none, or nothing was started: a scheduled restart of
// the caller's own pane).
type Started struct {
	Line, Pane, Terminal, Kind, Session string
}

// Launch is one agent (re)start: Agent in Pane ("": the pane labelled Agent), resuming the recorded session or Fresh;
// Force stops a working agent first. A CLI restart sets only these (T-785: no --sid, --prompt, --type or --effort).
type Launch struct {
	Agent, Pane  string
	Fresh, Force bool
	// Prompt is the first prompt for a fresh session: a job's wake, or a brief's text (serve's).
	Prompt string
	// Effort is the effort to start with (a dispatch's model ref, T-695); "": a role's configured effort (roles.<role>),
	// else the kind's default.
	Effort string
	// Type is the model to start with (a dispatch's choice, T-722); "": a lane agent's lane row model (its last
	// dispatch's), else the configured one (a role's).
	Type string
}

// Typer types text into an agent's pane as it is (a job's wake with its [obor job <id>] tag, or a line carrying its
// own [obor …]) and submits it. clear: the input line is cleared first (an earlier wake a dialog ate may sit there).
// Delivery is proven later by the agent (a job's job_next), not by this call. began: a byte of the text may have
// reached the pane (false only when the send was refused before any typing).
type Typer interface {
	Type(ctx context.Context, pane, text string, clear bool) (began bool, err error)
}

// PaneControl is the pane operations launch needs: find agents and panes, look at a pane, type into it, start an
// agent CLI in it. A "not found" error matches NotFound; ErrorCode reads the adapter's error code.
type PaneControl interface {
	Agent(ctx context.Context, target string) (Pane, error)
	Panes(ctx context.Context) ([]Pane, error)
	ProcessInfo(ctx context.Context, pane string) (ProcessInfo, error)
	// Screen is the pane's visible text, its last lines.
	Screen(ctx context.Context, pane string, lines int) (string, error)
	// Wait waits (up to timeout) for the agent at target to be in one of until (idle, done): the error's code is
	// "timeout" when it is not, agent_not_running when it left the pane.
	Wait(ctx context.Context, target string, until []string, timeout time.Duration) error
	// WaitForOutput waits (up to timeout) for a line of the pane's recent text to match regex, and returns the text
	// as it then reads: the error's code is "timeout" when nothing matched.
	WaitForOutput(ctx context.Context, pane, regex string, timeout time.Duration) (string, error)
	RenamePane(ctx context.Context, pane, label string) error   // label "" clears it
	RenameAgent(ctx context.Context, target, name string) error // name "" clears it
	// Type is text plus Enter, submitted at once.
	Type(ctx context.Context, pane, text string) error
	// Wake types a job's wake into the agent in pane (agent.prompt): a blocked agent is refused before any input
	// (a resume or update dialog would take it as an answer), unlike Type.
	Wake(ctx context.Context, pane, text string) error
	SendKeys(ctx context.Context, pane string, keys ...string) error
	// Start starts agent CLI kind in pane under name, returning once the agent runs there and is named: the agent as
	// the multiplexer answered, or as it shows it in the pane when the answer came later (its ID "" when neither
	// named one).
	Start(ctx context.Context, name, kind, pane string, args ...string) (Pane, error)
}

// NotFound: err says the pane or agent asked about does not exist (an error in the chain with NotFound() true).
func NotFound(err error) bool {
	var e interface{ NotFound() bool }
	return errors.As(err, &e) && e.NotFound()
}

// CodeNeedsForce is the ErrorCode of a stop without force that refused an agent only a forced stop ends: still working
// after Escape, or its exit dialog names background tasks (launch's Stop, T-773).
const CodeNeedsForce = "needs_force"

// The stop's waits and the one bound they fit in (T-829): launch's Stop keeps to the bound whatever
// OBOR_RESTART_WAIT says.

// StopIdle is how long a stop waits for a working agent to go idle after Escape before it refuses without force, or
// exits anyway with it: launch's Idle default.
const StopIdle = 30 * time.Second

// StopQuit is how long a stop waits for an agent to exit after its quit: launch's Wait default for a stop (an
// OBOR_RESTART_WAIT larger than it is capped so the total holds).
const StopQuit = 20 * time.Second

// StopTerminate is the kill sequence a stop may still run after those waits: the agent's ladder (SIGTERM ≤ 3 s, then
// SIGKILL ≤ 2 s), and its leftovers' (the same).
const StopTerminate = 10 * time.Second

// StopBound is the most a stop may take: its idle wait, its quit wait and its terminate together.
const StopBound = StopIdle + StopQuit + StopTerminate

// NeedsForce: err is such a refusal (CodeNeedsForce in the chain).
func NeedsForce(err error) bool { return ErrorCode(err) == CodeNeedsForce }

// NotRunning: err says the multiplexer is not running or not reachable (an error in the chain with Unavailable() true).
func NotRunning(err error) bool {
	var e interface{ Unavailable() bool }
	return errors.As(err, &e) && e.Unavailable()
}

// Rect is a pane's size on screen in cells (pane.layout).
type Rect struct {
	Pane string
	W, H int
}

// Plugin is one linked multiplexer plugin (plugin.list): its id and the directory it runs from.
type Plugin struct{ ID, Root string }

// AgentEnv is whose env and cwd a pane's shell is set to: the agent's name, its role (a role name; "" for none), its
// lane ("" for none) and the directory it works in ("" keeps the shell's); Vars the start's own variables
// (agents.Kind.StartEnv, T-695; "" unsets one).
type AgentEnv struct {
	Agent, Role, Lane, Cwd string
	Vars                   map[string]string
}

// Start is one agent start in its pane (obor setup pane / role, obor restart): the agent's env and cwd are the
// pane shell's already.
type Start struct {
	Agent, Kind, Pane string
	Label             string   // the pane's label now: renamed to Agent when it differs
	Args              []string // the type's launch args (config.Model.LaunchArgs)
	Resume            string   // the session id to resume; "" for a new one
	Prompt            string   // the CLI's first prompt (a job's wake); "" for none
	Lane              string   // the agent's lane: a session a fresh start reports is recorded with it
}

// StartError is a failed start (the multiplexer's error, or the startup dialog seen in the pane). NotReady: the agent
// runs but waits at a startup dialog; Hint says what to do.
type StartError struct {
	Err      error
	NotReady bool
	Hint     string
}

func (e *StartError) Error() string {
	if e.NotReady {
		return e.Hint
	}
	return e.Err.Error()
}

// Starter starts an agent in a pane the caller placed (launch.Launcher implements it; `obor setup pane` and `role`
// are its callers): SetEnv types the agent's env line at the pane's shell, Start runs the agent.
type Starter interface {
	SetEnv(ctx context.Context, pane string, e AgentEnv) error
	Start(ctx context.Context, s Start) error
}

// ErrorCode is the adapter's error code in err (herdr's agent_name_taken, agent_not_ready, …), "" when none.
func ErrorCode(err error) string {
	var e interface{ ErrorCode() string }
	if errors.As(err, &e) {
		return e.ErrorCode()
	}
	return ""
}

// NotShown is the reason a Panes adapter gives when its toast was not shown: herdr's busy, rate_limited, disabled or
// no_foreground_client. A best-effort toast (T-949): the caller logs it and goes on.
type NotShown struct {
	Reason string
}

func (n *NotShown) Error() string { return "notification not shown: " + n.Reason }

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

// Candidate is a compiled merge waiting to be tested and published, pinned at refs/obor/candidate/<task> until it is
// published or rejected: the commit the slot made and the target it merged onto (T-961). Patch, Stat and Diff are of
// the task's own diff Base..S (S: the submitted sha, the merge's second parent), so a move keeps them (row 20).
type Candidate struct {
	Task, Lane string
	SHA        string // the merge commit (its first parent is Target, its second the submitted sha)
	Target     string // the target sha the merge went onto
	Base       string // merge-base(S, Target): the task's own diff is Base..S ("": a gate before T-961)
	Patch      string // sha256 of git diff Base S: an accept is bound to it (docs/state-model.md row 20)
	Checks     string // the slot's compile summary ("" when no compile ran)
	Stat       string // git diff --stat Base S
	Diff       string // git diff Base S when at most InlineDiff bytes, else "" (the review job carries it, T-602)
}

// InlineDiff is the largest candidate diff the review job carries whole (T-602): larger, the job says to read it.
const InlineDiff = 15 << 10

// Scratch is the merge loop's git side: serve's own worktree, the target branch, the pins under refs/obor/.
// An outcome is final for its step; an error is "not decided": the step runs again (App.do's failure rule).
type Scratch interface {
	// Submit commits the lane's uncommitted work (a leftover done file never) and pins its HEAD at
	// refs/obor/submitted/<task>/<id>: that sha, the gate's input. id is the submit step's identity (submitID): a pin
	// it already has is returned as it is. A final refusal (no lane dir, not on a branch, already in the target) is a
	// Refused error.
	Submit(ctx context.Context, task, id, lane, summary, worker string) (sha string, err error)
	// Gate resets serve's worktree, merges or takes in.Input there, compiles it (commands.compile), commits and pins
	// the candidate (T-961: no test runs there).
	Gate(ctx context.Context, in GateInput) (GateOutcome, error)
	// Test runs the class's checks for the candidate in.SHA in the task's lane worktree (T-961, row 18b): checked out
	// detached there, the lane's branch restored after. A lane with uncommitted changes is an error (tried again: the
	// lane is never touched); an outcome is green, red or fail.
	Test(ctx context.Context, in TestInput) (TestOutcome, error)
	// Resolved pins the merge the integrator resolved and committed in serve's worktree at refs/obor/resolved/<task>:
	// its sha (the verdict's pin). It must be the merge of input (the conflicting gate's) onto the target; anything
	// else (an open merge, HEAD in the target, another merge) is a Refused error.
	// A non-nil m (the last gate's Mismatch) is refused while the committed merge still lacks the checked resolution's
	// edits in m.Files and is unchanged from the replay (T-917).
	Resolved(ctx context.Context, task, input string, m *Mismatch) (sha string, err error)
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
// never the old candidate: one merge commit per task, T-403). The merge's subject is "Merge <task>: <Title>" (no title
// stored: the Summary's first line), the same for a moved and a resolved gate (T-360, T-802); the report's whole Summary
// section (Body, T-807) stays in the body. Spec is the task's spec and SpecRev its rev, the store's current one (T-440): the
// gate puts both into the merge commit's message (T-521; a resolved gate amends the integrator's message), and the
// review job names that rev; "" (none stored, a task dispatched before T-440) adds nothing.
type GateInput struct {
	Task, Lane, Kind, Input string
	Summary, Title          string // the worker's summary line (the no-title subject); the spec's title ("": none stored)
	Body                    string // the worker report's whole Summary section, the merge's body ("": Summary, T-807)
	Spec                    string
	SpecRev                 int
	// Resolved, on an InputMoved gate, is the attempt's last checked resolution (the resolved gate's input, T-915; "":
	// none). A merge that needed a rerere replay is clean only when its patch equals that resolution's: else, or with
	// none, a conflict naming the files whose patch differs (rerere replays hunks, not the resolution's other edits).
	Resolved string
	// Command, when set, is told the name of each command the gate starts (commands.<name>: lane_setup, compile), for
	// obor status' running line (T-669).
	Command func(name string)
}

// TestInput is one lane test (T-961, row 18b): the task's candidate SHA, compiled onto Target, tested in Lane's
// worktree; Branch is the lane's branch as the dispatch recorded it, restored when the lane is found detached already
// (a test cut before its restore). Command is told each command it starts, as GateInput's.
type TestInput struct {
	Task, Lane, Branch string
	SHA, Target        string
	Command            func(name string)
}

// TestOutcome is a lane test's final outcome: Kind OutcomeGreen, OutcomeRed or OutcomeFail with Line; Checks the
// checks' summary.
type TestOutcome struct {
	Kind, Line string
	Checks     string
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
	// Mismatch: a conflict that is no unmerged path but a replayed resolution lost edits of the checked one (T-917).
	Mismatch *Mismatch
}

// Mismatch is a replayed resolution that differs from the checked one (T-915, T-917): the checked resolution's sha,
// the files whose patch differs, the tree of the replayed merge in Dir (what "unchanged from the replay" is compared
// with).
type Mismatch struct {
	Checked string   `json:"checked"`
	Tree    string   `json:"tree"`
	Files   []string `json:"files"`
	Dir     string   `json:"dir"`
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

// Dispatch is what a dispatch chose for the lane: the lane and its worktree, its worker agent, the task branch and the
// target sha read when the dispatch fact was recorded (Redo: the branch is kept as it is). The app's dispatch fact
// embeds it first, so its tags are that record's; the lane intents (PrepareInput, FreeInput) embed it too (T-891: an
// intent from before stored these as Lane, Dir, …, which decode into it, encoding/json matching keys case-insensitively).
type Dispatch struct {
	Lane   string `json:"lane"`
	Dir    string `json:"dir"`
	Agent  string `json:"agent,omitempty"`
	Branch string `json:"branch"`
	Target string `json:"target"`
	Redo   bool   `json:"redo,omitempty"`
}

// FreeInput is one lane freed by a drop: the lane's worktree, the task branch and the base it goes back to (Target:
// read when the step runs; Agent and Redo unused).
type FreeInput struct {
	Dispatch
	Discard bool
}

// PrepareInput is one lane.prepare step: the task's dispatch, and when its fact was recorded.
type PrepareInput struct {
	Task string
	Dispatch
	At time.Time
}
