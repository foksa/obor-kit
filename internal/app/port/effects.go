package port

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The effects the core drives through adapters (docs/module-map.md: adapters reach each other only through these):
// Agents starts and stops agents (launch), Prompter types job wakes and lines into their agents, PaneControl is what
// launch itself needs of the terminal (the herdr socket client implements it).

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
// Force stops a working agent first. A CLI restart sets only these (no --sid, --prompt, --type or --effort).
type Launch struct {
	Agent, Pane  string
	Fresh, Force bool
	// Prompt is the first prompt for a fresh session: a job's wake, or a brief's text (serve's).
	Prompt string
	// Effort is the effort to start with (a dispatch's model ref); "": a role's configured effort (roles.<role>),
	// else the kind's default.
	Effort string
	// Type is the model to start with (a dispatch's choice); "": a lane agent's lane row model (its last
	// dispatch's), else the configured one (a role's).
	Type string
}

// Prompter types text to the agent in a pane, as it is (a job's wake with its [obor job <id>] tag, or a line carrying
// its own [obor …]): the agent layer's way to type. A blocked agent is refused before any input (a dialog would take
// the text as its answer); a pane that runs no agent is refused too (NotFound). clear: the input line is cleared first
// (an earlier wake a dialog ate may sit there). Delivery is proven later by the agent (a job's job_next), not by this
// call. began: a byte of the text may have reached the pane (false only when the send was refused before any typing).
type Prompter interface {
	Prompt(ctx context.Context, pane, text string, clear bool) (began bool, err error)
}

// Keys are obor's names for the keys SendKeys takes: these and a digit ("1"); each terminal adapter maps them to its
// terminal's own.
const (
	KeyEnter = "enter"
	KeyEsc   = "esc"
	KeyCtrlC = "ctrl+c"
	KeyCtrlU = "ctrl+u"
)

// PaneControl is the pane operations launch needs: read panes, look at one, type into it, start an agent CLI in it.
// A pane is named by its id, an agent by its pane's label; the terminal's own name for an agent is read only by Named,
// on the agent layer's side with Start and Prompt. A "not found" error matches NotFound; every other kind of failure the callers ask about is a predicate below
// (PaneBusy, NotReady, TimedOut, …), never a code of the terminal's.
type PaneControl interface {
	Prompter
	// Pane is pane as the terminal reports it now: its label and cwd, and the facts of the agent that runs in it
	// (Agent "" when none does). Group is not set. NotFound: the terminal has no such pane.
	Pane(ctx context.Context, pane string) (Pane, error)
	Panes(ctx context.Context) ([]Pane, error)
	ProcessInfo(ctx context.Context, pane string) (ProcessInfo, error)
	// Screen is the pane's visible text, its last lines.
	Screen(ctx context.Context, pane string, lines int) (string, error)
	// WaitStatus waits (up to timeout) for the agent in pane to be in one of until (idle, done): the error matches
	// TimedOut when it is not; any other error says no agent is there to wait for (none ran, or it left the pane).
	WaitStatus(ctx context.Context, pane string, until []string, timeout time.Duration) error
	// WaitForOutput waits (up to timeout) for a line of the pane's recent text to match regex, and returns the text
	// as it then reads: the error matches TimedOut when nothing matched.
	WaitForOutput(ctx context.Context, pane, regex string, timeout time.Duration) (string, error)
	RenamePane(ctx context.Context, pane, label string) error // label "" clears it
	// Write types text into pane, whatever runs there, and with submit sends Enter after it: the multiplexer's way to
	// type (a command line at a shell, an agent's quit). It judges nothing: an agent at a dialog gets the text too.
	Write(ctx context.Context, pane, text string, submit bool) error
	// SendKeys sends keys (Keys' names) to pane, whatever runs there.
	SendKeys(ctx context.Context, pane string, keys ...string) error
	// Start starts agent CLI kind in pane for the agent name, returning once the terminal took the start: the pane as
	// it answered, or as it shows it when the answer came later (its ID "" when neither showed one). What the terminal
	// keeps of an earlier start of name that a live agent does not hold is cleared and the start retried once, say
	// taking the adapter's lines for it and for a failed clear (nil: silent). It decides no identity: a live agent in
	// another pane holding the name (a start that lost a race) is a refusal for good, the adapter's error matching
	// NameHeld with that pane. The holder lookup is Named's: only its NotFound says the name has no holder, any other
	// error stops the start with it, nothing cleared.
	Start(ctx context.Context, name, kind, pane string, say func(format string, args ...any), args ...string) (Pane, error)
	// Named is the pane whose live agent the terminal holds under name, the agent as the terminal reports it (its
	// label not read). NotFound: no live agent holds the name. An answer that names no pane is an error, never "no
	// holder".
	Named(ctx context.Context, name string) (Pane, error)
}

// NotFound: err says the pane or agent asked about does not exist (an error in the chain with NotFound() true).
func NotFound(err error) bool {
	var e interface{ NotFound() bool }
	return errors.As(err, &e) && e.NotFound()
}

// PaneBusy: err says the pane is not an available shell to start an agent in (an error in the chain with PaneBusy()
// true): its shell is not up yet, or a program or an agent holds it.
func PaneBusy(err error) bool {
	var e interface{ PaneBusy() bool }
	return errors.As(err, &e) && e.PaneBusy()
}

// NotReady: err says the agent started but waits at a startup dialog (an error in the chain with NotReady() true).
func NotReady(err error) bool {
	var e interface{ NotReady() bool }
	return errors.As(err, &e) && e.NotReady()
}

// NameHeld: err says a start was refused for good because a live agent in another pane holds the agent's name; it
// returns that pane's id (an error in the chain with NameHeld() string).
func NameHeld(err error) (pane string, held bool) {
	var e interface{ NameHeld() string }
	if !errors.As(err, &e) {
		return "", false
	}
	return e.NameHeld(), true
}

// TimedOut: err says the terminal's wait ended at its own timeout (an error in the chain with TimedOut() true).
func TimedOut(err error) bool {
	var e interface{ TimedOut() bool }
	return errors.As(err, &e) && e.TimedOut()
}

// Answered: err is the terminal's own answer, an error it sent (an error in the chain with Answered() true), not a
// connection that went.
func Answered(err error) bool {
	var e interface{ Answered() bool }
	return errors.As(err, &e) && e.Answered()
}

// NotRunning: err says the multiplexer is not running or not reachable (an error in the chain with Unavailable() true).
func NotRunning(err error) bool {
	var e interface{ Unavailable() bool }
	return errors.As(err, &e) && e.Unavailable()
}

// NeedsForce: err is a stop without force that refused an agent only a forced stop ends: still working after Escape,
// or its exit dialog names background tasks (launch's Stop; an error in the chain with NeedsForce() true).
func NeedsForce(err error) bool {
	var e interface{ NeedsForce() bool }
	return errors.As(err, &e) && e.NeedsForce()
}

// The stop's waits and the one bound they fit in: launch's Stop keeps to the bound whatever
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

// Restorer restarts a project's labelled panes that run no agent (launch.Restorer): serve calls it once its
// subscription is back from a lost terminal connection (a Subscribe or a stream's end with NotRunning), and at its
// first connect when the terminal restarted since serve last recorded it. It is
// idempotent: a pane whose agent runs is left alone, and runs for one project are one at a time. It returns when the run has ended (or ctx has).
type Restorer interface {
	RestoreProject(ctx context.Context, root string)
}

// TerminalSetup is what obor setup and obor init ask of the terminal, in terminal-neutral terms: the pane
// operations launch needs, the panes that run an agent now, a new pane for an agent placed by the adapter's own
// layout, the terminal's own setting for a restart, and a project rename's share. The herdr adapter (herdr.Setup)
// implements it; setup decides whose pane and near what, never a tab, a workspace or a split.
type TerminalSetup interface {
	PaneControl
	// Agents is every pane that runs an agent now, with its label (obor's name of the agent; "": an agent obor did
	// not start, or a pane nobody labelled) and the agent's cwd.
	Agents(ctx context.Context) ([]Pane, error)
	// Place makes a new pane for an agent where p says and returns its id; the layout is the adapter's. Its refusals
	// and failures are the lines setup prints after FAIL.
	Place(ctx context.Context, p Placement) (string, error)
	// ResumeHint is the one line to add when the terminal would resume its own agents after a restart and obor
	// restarts them itself ("" when the user's terminal config says it must not); obor never edits that config.
	ResumeHint() string
	// RenameProject renames the terminal's group of the project from old to name (herdr: the workspace labelled old)
	// and relabels each pane of it whose label relabel maps to another label.
	RenameProject(ctx context.Context, old, name string, relabel func(label string) string) (Renamed, error)
}

// Placement is a new agent pane setup asks the terminal for (obor setup pane / role, obor drive, obor init): whose
// pane it is, its cwd and env, and near what. At most one of Own, Group, In, Beside; none: beside the caller's pane
// when its group is the project's, else in the project's busiest group, else in a group of the role's own. The
// terminal adapter decides the layout (which group, a split or a new one, its label).
type Placement struct {
	Role, Lane string   // whose pane: its role ("worker" for a lane's) and lane ("" for none)
	Cwd        string   // the new pane's cwd
	Env        []string // NAME=VALUE for the new pane's shell ("NAME=": set empty)
	Own        bool     // in a new group of the role's own (obor init's first worker and its reviewer)
	Group      string   // in a new group labelled Group (obor setup --new-tab)
	In         string   // in the group the terminal knows by this id (obor setup --tab), whoever's it is
	// Beside: in the group of this pane when that group is the project's, else in a new group of the role's own (obor
	// init: L-02 and the integrator beside L-01).
	Beside  string
	Project PlaceProject
	Say     func(line string) // the placement's progress lines (a new group, a split)
}

// PlaceProject is the project a placement is for: its name (a group of its own is named after it), root (that
// group's first pane works there), the caller's pane ("": none), how many panes a group may hold, and which cwds are
// the project's (Has: the main checkout or under its lanes dir).
type PlaceProject struct {
	Name, Root string
	Self       string
	MaxPanes   int
	Has        func(cwd string) bool
}

// Renamed is RenameProject's outcome: the step's line, Already when nothing was left to do, and Warn when the terminal
// could not be asked (Line says what to do by hand).
type Renamed struct {
	Line          string
	Already, Warn bool
}

// AgentEnv is whose env and cwd a pane's shell is set to: the agent's name, its role (a role name; "" for none), its
// lane ("" for none) and the directory it works in ("" keeps the shell's); Vars the start's own variables
// (agents.Kind.StartEnv; "" unsets one).
type AgentEnv struct {
	Agent, Role, Lane, Cwd string
	Vars                   map[string]string
}

// Build is an agent's start to build (Starter.Build): its type (the model's kind and args), its role ("": none),
// effort ("": the type's), cwd, the dirs outside it the agent writes, and the session to resume or the first prompt.
type Build struct {
	Agent, Kind       string
	Args              []string // the model's args (config.Model.Args)
	Role, Effort, Dir string
	WriteDirs         []string
	Resume, Prompt    string
}

// Built is a start as its kind built it from the complete start, in one call of a plugin kind: the argv after the
// binary, the start's own env (Vars; "" unsets one) and the session id obor chose for it ("": none, or a resume).
type Built struct {
	Args    []string
	Vars    map[string]string
	Session string
}

// Start is one agent start in its pane (obor setup pane / role, obor restart): the agent's env and cwd are the
// pane shell's already, its argv the one its Build built.
type Start struct {
	Agent, Kind, Pane string
	Label             string   // the pane's label now: renamed to Agent when it differs
	Args              []string // the argv after the binary (Built.Args): started as it is
	Resume            string   // the session id to resume; "" for a new one
	// Session is the session id obor chose for a fresh start (Built.Session; Args name it): recorded for the agent
	// before it runs; "" for none.
	Session string
	Prompt  string // the CLI's first prompt (a job's wake), in Args already; "" for none
	// Again is the command that starts this agent again as it was started (a drive's `obor drive <T-xx>`, which keeps
	// its prompt); "": `obor restart <Agent>`. A hint that names the start again uses it.
	Again string
	Lane  string // the agent's lane: a session a fresh start reports is recorded with it
	// Shell bounds the wait for the pane's shell (Await) and the retries of a start the terminal refused as PaneBusy:
	// one bound for both, from the start's beginning; 0: neither, a pane the caller
	// knows is its shell. The callers' bounds differ (obor setup's new pane: 10 s, its shell comes up in moments; a
	// restart: 30 s, the old agent's exit is reaped first).
	Shell time.Duration
	// Await: the start waits for the pane's shell before its env line and start (a pane a new tab or split just made,
	// a pane whose agent was just stopped); past Shell, a StartError with NoShell and nothing typed. false: a pane not at
	// its shell is the env line's to refuse (a person's program in a pane that ran no agent).
	Await bool
	// Env types the agent's env line at the pane's shell once it is up, before the start (nil: the caller set it).
	Env func(ctx context.Context) error
}

// StartError is a failed start (the multiplexer's error, or the startup dialog seen in the pane). NotReady: the agent
// runs but waits at a startup dialog; Hint says what to do. NoShell: no start was made (Start.Await).
type StartError struct {
	Err      error
	NotReady bool
	Hint     string
	NoShell  bool // the pane did not become its shell within Start.Shell (Await): nothing typed, nothing started
}

func (e *StartError) Error() string {
	if e.NotReady {
		return e.Hint
	}
	return e.Err.Error()
}

// Located is where a start or stop of an agent goes (Starter.Locate): the pane ("": none, no pane is the agent's) and
// why.
type Located struct {
	Pane string
	// Runs: the agent runs in Pane now (a start there is a restart in place); Proof is what proves it (session, start,
	// label, name). Not Runs: Pane is the agent's home (the pane labelled with its name at its shell) or the target.
	Runs  bool
	Proof string
	Label string // Pane's label as the pane list read now shows it
}

// Starter starts an agent in a pane the caller placed (launch.Launcher implements it; `obor setup pane` and `role`
// are its callers): Locate decides first, before a pane is placed or labelled, where the agent runs or where its start
// goes (pane: the caller's target, "": none; adds: a start that adds an agent, false: one that only labels a pane);
// it returns holding the agent's restart lock, release after the caller's last step, and a refusal is a Refused error
// with nothing touched. Build builds the start once (its argv and env), SetEnv types the agent's env line at the pane's
// shell, Start runs the agent with the built argv.
type Starter interface {
	Locate(ctx context.Context, agent, pane string, adds bool) (Located, func(), error)
	Build(b Build) (Built, error)
	SetEnv(ctx context.Context, pane string, e AgentEnv) error
	Start(ctx context.Context, s Start) error
}

// NotShown is the reason a Panes adapter gives when its toast was not shown: herdr's busy, rate_limited, disabled or
// no_foreground_client. A best-effort toast: the caller logs it and goes on.
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
// published or rejected: the commit the slot made and the target it merged onto. Patch, Stat and Diff are of
// the task's own diff Base..S (S: the submitted sha, the merge's second parent), so a move keeps them (row 20).
type Candidate struct {
	Task, Lane string
	SHA        string // the merge commit (its first parent is Target, its second the submitted sha)
	Target     string // the target sha the merge went onto
	Base       string // merge-base(S, Target): the task's own diff is Base..S ("" on a candidate whose base is not recorded)
	Patch      string // sha256 of git diff Base S: an accept is bound to it (docs/state-model.md row 20)
	Checks     string // the slot's compile summary ("" when no compile ran)
	Stat       string // git diff --stat Base S
	Diff       string // git diff Base S when at most InlineDiff bytes, else "" (the review job carries it)
}

// InlineDiff is the largest candidate diff the review job carries whole: larger, the job says to read it.
const InlineDiff = 15 << 10

// Scratch is the merge loop's git side: serve's own worktree, the target branch, the pins under refs/obor/.
// An outcome is final for its step; an error is "not decided": the step runs again (App.do's failure rule).
type Scratch interface {
	// Submit commits the lane's uncommitted work (a leftover done file never) and pins its HEAD at
	// refs/obor/submitted/<task>/<id>: that sha, the gate's input. id is the submit step's identity (submitID): a pin
	// it already has is returned as it is. A final refusal (no lane dir, not on a branch, already in the target) is a
	// Refused error.
	Submit(ctx context.Context, task, id, lane, summary string) (sha string, err error)
	// Gate resets serve's worktree, merges or takes in.Input there, compiles it (commands.compile), commits and pins
	// the candidate (the gate runs no test there).
	Gate(ctx context.Context, in GateInput) (GateOutcome, error)
	// Test runs the class's checks for the candidate in.SHA in the task's lane worktree (row 18b): checked out
	// detached there, the lane's branch restored after. A lane with uncommitted changes is an error (tried again: the
	// lane is never touched); an outcome is green, red or fail.
	Test(ctx context.Context, in TestInput) (TestOutcome, error)
	// Resolved pins the merge the integrator resolved and committed in serve's worktree at refs/obor/resolved/<task>:
	// its sha (the verdict's pin). It must be the merge of input (the conflicting gate's) onto the target; anything
	// else (an open merge, HEAD in the target, another merge) is a Refused error.
	// A non-nil m (the last gate's Mismatch) is refused while the committed merge still lacks the checked resolution's
	// edits in m.Files and is unchanged from the replay.
	Resolved(ctx context.Context, task, input string, m *Mismatch) (sha string, err error)
	// Publish fast-forwards the target to c.SHA when it is still c.Target, then deletes the task's pins.
	Publish(ctx context.Context, c Candidate) (PublishOutcome, error)
	// AfterMerge runs merge.after for the published c: ran | skipped | WARN …. Its intent (G) is the once:
	// a cut before its result runs it again (after_merge is idempotent).
	AfterMerge(ctx context.Context, c Candidate) (string, error)
	// Landed: the target holds merge evidence of task (merged outside the app); an error: git cannot tell.
	Landed(ctx context.Context, task string) (bool, error)
	// Unpin deletes a rejected task's pins (an absent pin is not an error).
	Unpin(ctx context.Context, task string) error
}

// GateInput is one gate: Input is the sha gated, per Kind: InputLane submit's sha (merged onto the target),
// InputResolved the verdict's pin (checked as committed), InputMoved submit's sha again (merged onto the moved target,
// never the old candidate: one merge commit per task). The merge's subject is "Merge <task>: <Title>" (no title
// stored: the Summary's first line), the same for a moved and a resolved gate; the report's whole Summary
// section (Body) stays in the body. Spec is the task's spec and SpecRev its rev, the store's current one: the
// gate puts both into the merge commit's message (a resolved gate amends the integrator's message), and the
// review job names that rev; "" (none stored) adds nothing.
type GateInput struct {
	Task, Lane, Kind, Input string
	Summary, Title          string // the worker's summary line (the no-title subject); the spec's title ("": none stored)
	Body                    string // the worker report's whole Summary section, the merge's body ("": Summary)
	Spec                    string
	SpecRev                 int
	// Resolved, on an InputMoved gate, is the attempt's last checked resolution (the resolved gate's input; "":
	// none). A merge that needed a rerere replay is clean only when its patch equals that resolution's: else, or with
	// none, a conflict naming the files whose patch differs (rerere replays hunks, not the resolution's other edits).
	Resolved string
	// Command, when set, is told the name of each command the gate starts (commands.<name>: lane_setup, compile), for
	// obor status' running line.
	Command func(name string)
}

// TestInput is one lane test (row 18b): the task's candidate SHA, compiled onto Target, tested in Lane's
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
	// Mismatch: a conflict that is no unmerged path but a replayed resolution lost edits of the checked one.
	Mismatch *Mismatch
}

// Mismatch is a replayed resolution that differs from the checked one: the checked resolution's sha,
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
// among the merged ones: nothing merged), PublishPending (the merge was asked for and is not done yet: the task waits
// published until Landed says the target holds it; no implementation returns it yet).
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
	PublishPending = "pending"
)

// Refused is a final refusal returned as an error (Submit, Resolved): the step is not run again.
type Refused struct{ Line string }

func (r Refused) Error() string { return r.Line }

// LaneSetup prepares a lane for a dispatched task (internal/checks implements it).
type LaneSetup interface {
	// Prepare puts the lane at in.Dir on in.Branch at in.Target (in.Redo: the branch is kept as it is), runs
	// lane_ready, then lane_setup and lane_ready again when it was red. It returns the base sha: its lane row's
	// (serve's projection; its head is the submitted head). Repeating it converges on the same state.
	// A final refusal (no lane dir, lane_setup red, lane_ready still red) is a Refused error; anything else (git I/O)
	// is an error to retry.
	Prepare(ctx context.Context, in PrepareInput) (Prepared, error)
	// Free gives a dropped task's lane back (obor drop): refused (Refused) when it has uncommitted changes or
	// its branch has commits not in in.Target, unless in.Discard (then they are discarded); the lane is detached at
	// in.Target and its branch deleted. Repeating it converges (a missing branch is gone already). Its line says
	// what it discarded ("" nothing).
	Free(ctx context.Context, in FreeInput) (line string, err error)
}

// Prepared is lane.prepare's outcome: the base sha the task branch is on.
type Prepared struct{ Base string }

// Dispatch is what a dispatch chose for the lane: the lane and its worktree, its worker agent, the task branch and the
// target sha read when the dispatch fact was recorded (Redo: the branch is kept as it is). The app's dispatch fact
// embeds it first, so its tags are that record's; the lane intents (PrepareInput, FreeInput) embed it too.
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
