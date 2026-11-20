package port

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Keys are obor's names for the keys SendKeys takes: these and a digit ("1"); each terminal adapter maps them to its
// terminal's own.
const (
	KeyEnter = "enter"
	KeyEsc   = "esc"
	KeyCtrlC = "ctrl+c"
	KeyCtrlU = "ctrl+u"
)

// PaneControl is the pane operations launch needs: read panes, look at one, type into it. A pane is named by its id,
// an agent by its pane's label; the terminal's own name for an agent is read only by Named, on the agent layer's side
// with Prompt and the terminal's AgentStarter. A "not found" error matches NotFound; every other kind of failure the
// callers ask about is a predicate below (PaneBusy, NotReady, TimedOut, …), never a code of the terminal's.
type PaneControl interface {
	Prompter
	// Pane is pane as the terminal reports it now: its label and cwd, and the facts of the agent that runs in it
	// (Agent "" when none does). Group is not set. NotFound: the terminal has no such pane.
	Pane(ctx context.Context, pane string) (Pane, error)
	Panes(ctx context.Context) ([]Pane, error)
	ProcessInfo(ctx context.Context, pane string) (ProcessInfo, error)
	// Screen is the pane's visible text, its last lines.
	Screen(ctx context.Context, pane string, lines int) (string, error)
	// Recent is the pane's recent output, its last lines, long lines unwrapped: what a wait for a line the shell
	// printed reads (the env line's mark). The visible screen can lag behind what the shell printed after an agent
	// exits, while recent output carries the mark; a terminal without that distinction serves Screen's text.
	Recent(ctx context.Context, pane string, lines int) (string, error)
	// WaitStatus waits (up to timeout) for the agent in pane to be in one of until (idle, done): the error matches
	// TimedOut when it is not; any other error says no agent is there to wait for (none ran, or it left the pane).
	WaitStatus(ctx context.Context, pane string, until []string, timeout time.Duration) error
	RenamePane(ctx context.Context, pane, label string) error // label "" clears it
	// Write types text into pane, whatever runs there, and with submit sends Enter after it: the multiplexer's way to
	// type (a command line at a shell, an agent's quit). It judges nothing: an agent at a dialog gets the text too.
	Write(ctx context.Context, pane, text string, submit bool) error
	// SendKeys sends keys (Keys' names) to pane, whatever runs there.
	SendKeys(ctx context.Context, pane string, keys ...string) error
	// Named is the pane whose live agent the terminal holds under name, the agent as the terminal reports it (its
	// label not read). NotFound: no live agent holds the name. An answer that names no pane is an error, never "no
	// holder".
	Named(ctx context.Context, name string) (Pane, error)
}

// AgentStarter is a terminal's own start of an agent CLI in a pane whose shell has the agent's env and cwd already
// (launch typed its env line there): launch asks the terminal's PaneControl for it. Start returns once the agent runs
// in the pane, the pane as the terminal then shows it (its ID may be another pane than s.Pane: the agent runs there).
// A failed start's error matches PaneBusy when the pane is no available shell (the caller may wait for its shell and
// start again), NotReady when the agent waits at a startup dialog, TimedOut when no agent came up within s.Bound,
// NameHeld when a live agent in another pane holds the agent's name (a start that lost a race: a refusal for good),
// and Accepted when the terminal had accepted the start (its command ran: the agent may run, at a dialog say). What
// the terminal keeps of an earlier start of the name that a live agent does not hold is the adapter's to clear, said
// on s.Say.
type AgentStarter interface {
	Start(ctx context.Context, s AgentStart) (Pane, error)
}

// AgentStart is one agent start a terminal makes (AgentStarter): the pane, the agent's name (obor's label of the
// pane), its kind and the kind's program, argv after the program (the first prompt in it already, Prompt too), the env and cwd launch's env
// line gave the pane's shell (an adapter that starts a program with its own env and cwd may pass them; one that types
// the command at the shell reads neither), how long the agent may take to come up and how often the adapter looks,
// and where the adapter's own lines go.
type AgentStart struct {
	Pane, Label, Kind string
	// Program is the kind's executable (agents.Programs of the kind in the launching pass's config): an adapter that types
	// the command line reads it, and looks no kind up.
	Program string
	Argv    []string
	// Env is NAME=VALUE for each variable the env line exports; the registry's other variables it unsets.
	Env    []string
	Cwd    string        // the directory the env line changed to ("": the shell's)
	Prompt string        // the first prompt, in Argv already ("": none)
	Bound  time.Duration // for the agent to come up (0: the adapter's default)
	Poll   time.Duration // how often the adapter looks while it waits (0: the adapter's default)
	// Say takes the adapter's lines (a name it kept and cleared, a start it counts though its own answer failed); nil:
	// silent.
	Say func(format string, args ...any)
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

// Accepted: err is a failed agent start the terminal had accepted (AgentStarter; an error in the chain with
// Accepted() true): it ran the agent's command, so the agent may run, at a startup dialog say.
func Accepted(err error) bool {
	var e interface{ Accepted() bool }
	return errors.As(err, &e) && e.Accepted()
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

// Adopter is an optional capability of a terminal's setup (hc has it, herdr does not): it places the project's live
// terminals that no window shows yet (made while the window was closed) by the placement rules, and returns how many it
// placed. A window still closed is a NotRunning error with nothing placed. Serve calls it after each connect.
type Adopter interface {
	Adopt(ctx context.Context, pr PlaceProject) (int, error)
}

// TerminalSetup is what obor setup and obor init ask of the terminal, in terminal-neutral terms: the pane
// operations launch needs, the panes that run an agent now, a new pane for an agent placed where app/layout
// decides, the terminal's own setting for a restart, and a project rename's share. The herdr adapter (herdr.Setup)
// implements it; setup decides whose pane and near what, never a tab, a workspace or a split (app/layout decides those).
type TerminalSetup interface {
	PaneControl
	// Agents is every pane that runs an agent now, with its label (obor's name of the agent; "": an agent obor did
	// not start, or a pane nobody labelled) and the agent's cwd.
	Agents(ctx context.Context) ([]Pane, error)
	// Place makes a new pane for an agent where p says and returns its id; app/layout decides where, the adapter
	// carries it out. Its refusals and failures are the lines setup prints after FAIL.
	Place(ctx context.Context, p Placement) (string, error)
	// ResumeHint is the optional line that saves a double start when the terminal resumes its own agents after a
	// restart, which obor's restore then replaces ("" when the user's terminal config says it does not); obor never
	// edits that config.
	ResumeHint() string
	// RenameProject renames the terminal's group of the project from old to name (herdr: the workspace labelled old)
	// and relabels each pane of it whose label relabel maps to another label.
	RenameProject(ctx context.Context, old, name string, relabel func(label string) string) (Renamed, error)
	// CloseProject closes the project's panes, then the tabs (and the group) they leave empty, one line each; with
	// c.Dry it closes nothing and says what it would. A pane or tab that is not the project's is never touched.
	CloseProject(ctx context.Context, c CloseProject) ([]string, error)
	// TakeLead makes the caller's pane (p.Pane) the lead's home when it qualifies: a pane of this terminal, at its
	// shell (no agent runs in it; the command offering the pane, p.Caller, may hold its foreground), alone in its
	// tab, and (herdr) in a group that holds no pane of another project. Taken, the pane is labelled p.Agent, its tab
	// named lead and its group named after the project; not taken, nothing is touched and Line says why.
	TakeLead(ctx context.Context, p LeadPane) (LeadTaken, error)
	// LeadSpace readies the caller's group for the lead before anything is placed (obor init): when the pane
	// qualifies as TakeLead would take it, the group placement uses is made the caller's (hc: the caller's space named
	// after the project), so the project's panes go there and TakeLead takes the pane after them. Only the group is
	// touched; Taken says the group is the caller's, Line what was done or why not.
	LeadSpace(ctx context.Context, p LeadPane) (LeadTaken, error)
}

// LeadPane is the caller's pane offered as the lead's home (obor lead): its id, the lead's agent name, the project
// and Caller, the process group of the command that offers it (0: none): a foreground of that group is that command
// itself, which exits before the lead starts there, never another program.
type LeadPane struct {
	Pane, Agent string
	Project     PlaceProject
	Caller      int
}

// LeadTaken is TakeLead's (and LeadSpace's) outcome: whether the pane is now the lead's (the group the caller's), and
// Line: what was done, or why the pane does not qualify.
type LeadTaken struct {
	Taken bool
	Line  string
}

// CloseProject is what obor uninstall asks the terminal to close: the project's name (its group is named after
// it) and, for a pane the terminal lists (its label and cwd), whether it is the project's (Ours).
type CloseProject struct {
	Name string
	Ours func(p Pane) bool
	Dry  bool
}

// Placement is a new agent pane setup asks the terminal for (obor setup pane / role, obor drive, obor init): whose
// pane it is, its kind, its cwd and env, and where. At most one of Group, In; none: by Kind, in a group of the
// project whose panes are all of that kind and which has room, else in a new one. A group holding the lead's pane is
// the lead's alone: nothing else goes there, not even by In. app/layout decides where (which group, a split or a new
// one, its label); the adapter asks it with its terminal's facts and carries the answer out.
type Placement struct {
	Kind    PaneKind // what the pane is to placement
	Cwd     string   // the new pane's cwd
	Env     []string // NAME=VALUE for the new pane's shell ("NAME=": set empty)
	Group   string   // in a new group labelled Group (obor setup --new-tab)
	In      string   // in the group the terminal knows by this id (obor setup --tab), unless it is the lead's
	Project PlaceProject
	Say     func(line string) // the placement's progress lines (a new group, a split)
	// Starts: the caller starts an agent in the pane at once, its start typing the env line (cwd and env) at the
	// pane's shell first (Start.Env), so the pane's shell needs no line typed by the placement too. Without it a
	// placed pane has Cwd and Env as Place returns.
	Starts bool
}

// PaneKind is what a pane is to placement: the lead's, a lane's (its worker or drive agent), or a role's (any other
// role of the project: integrator, reviewer, a custom one).
type PaneKind string

const (
	KindLead PaneKind = "lead"
	KindLane PaneKind = "lane"
	KindRole PaneKind = "role"
)

// PlaceProject is the project a placement is for: its name (a group of its own is named after it), root (that
// group's first pane works there), how many panes a group may hold, which cwds are the project's (Has: the main
// checkout or under its lanes dir), and the kind of a pane by its label (KindOf: "" for a label that names no agent
// of the project, or none).
type PlaceProject struct {
	Name, Root string
	MaxPanes   int
	Has        func(cwd string) bool
	KindOf     func(label string) PaneKind
}

// Renamed is RenameProject's outcome: the step's line, Already when nothing was left to do, and Warn when the terminal
// could not be asked (Line says what to do by hand).
type Renamed struct {
	Line          string
	Already, Warn bool
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
