package port

import (
	"context"
	"errors"
	"slices"
)

// The effects the core drives through adapters (docs/module-map.md: adapters reach each other only through these):
// Launcher starts and stops agents (launch), Prompter types job wakes and lines into their agents, PaneControl is what
// launch itself needs of the terminal (the herdr socket client implements it).

// Launcher starts and stops obor agents in their panes, the one way an agent is started (serve's starts, obor restart,
// obor setup's --start, obor lead, obor drive); the kill sequence is the implementation's (launch.Launcher).
type Launcher interface {
	// Launch starts req.Agent (a lane's agent or a role) in its pane (req.Place: one it places when it has none), its
	// kind and args from its lane type or role, after stopping what runs there (req.NoStop: refused instead). Returns
	// the one-line result and the agent it started; a fresh start held by a trust dialog returns them too, with a
	// *TrustDialog error (TrustHeld). req.NoAgent: the pane only (see Launch.NoAgent).
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
	// Idle (a NoAgent launch only): the agent's pane was there already, labelled, at its shell with no agent in it:
	// nothing was done (a rerun). Line, Terminal, Kind and Session are empty for such a launch.
	Idle bool
}

// Launch is one agent (re)start: Agent in Pane ("": the pane labelled Agent), resuming the recorded session or Fresh;
// Force stops a working agent first. A CLI restart sets only these (no --sid, --prompt, --type or --effort).
type Launch struct {
	Agent, Pane  string
	Fresh, Force bool
	// Handoff is the request asks for the handoff form of the role's first prompt (the lead's
	// `/obor:lead handoff`, or the plain prompt naming the skill file with `handoff` where the kind takes no slash
	// command): the fresh session a lead starts for itself after writing its Handoff.
	// `obor restart <lead> --handoff` sets it (a plain --fresh does not), and a scheduled restart carries it.
	Handoff bool
	// Prompt is the first prompt for a fresh session: a job's wake, or a brief's text (serve's).
	Prompt string
	// Effort is the effort to start with (a dispatch's model ref); "": a role's configured effort (roles.<role>),
	// else the kind's default.
	Effort string
	// Type is the model to start with (a dispatch's choice); "": a lane agent's lane row model (its last
	// dispatch's), else the configured one (a role's).
	Type string
	// Place places a pane for an agent that has none (no pane it runs in, no home pane, no Pane named): called once,
	// after the start is built and before anything is typed, and the start goes into the pane it returns. nil: such an
	// agent is refused (no pane labelled Agent).
	Place *Placer
	// NoStop refuses a start rather than stop a running agent, nothing stopped: Agent itself running (a *Running), another
	// agent in Agent's lane worktree, or another program in the start's pane.
	NoStop bool
	// NoAgent labels the agent's pane and types its env (the role's and the lane's, nothing of a kind's own) and starts no
	// agent: the pane is the one the agent runs in, else its home (the pane labelled Agent at its shell), else Pane (the
	// target: its shell gets the env line), else a new one Place places (the terminal types the env it is given). It
	// refuses as NoStop does (the agent running → a *Running; another agent in its lane; another program in the pane),
	// and, as nothing is started, no model, session, quarantine or first prompt applies and serve down refuses no label.
	// A home pane with Pane unset is left as it is: Started.Idle. The pane gets the label of Agent. Fresh, Prompt,
	// Type, Effort, Handoff, Force and Again are not read.
	NoAgent bool
	// Again is the command that starts this agent again as it was started (a drive's `obor drive <T-xx>`, which keeps
	// its prompt), named by a failed start's hint; "": `obor restart <Agent>`.
	Again string
}

// Placer is Launch.Place: Place places a new pane for the agent, env NAME=VALUE for its shell ("NAME=": set empty,
// the start's env as agents.Kinds.StartVars builds it), and returns the pane's id. A pointer in Launch, so a Launch stays
// comparable.
type Placer struct {
	Place func(ctx context.Context, env []string) (pane string, err error)
}

// Running is a NoStop launch's refusal when its agent already runs, in Pane: nothing stopped, typed or placed.
type Running struct {
	Agent, Pane string
}

func (r *Running) Error() string { return r.Agent + " already running (pane " + r.Pane + ")" }

// Prompter types text to the agent in a pane, as it is (a job's wake with its [obor job <id>] tag, or a line carrying
// its own [obor …]): the agent layer's way to type. A blocked agent is refused before any input (a dialog would take
// the text as its answer); a pane that runs no agent is refused too (NotFound). clear: the input line is cleared first
// (an earlier wake a dialog ate may sit there). Delivery is proven later by the agent (a job's job_next), not by this
// call. began: a byte of the text may have reached the pane (false only when the send was refused before any typing).
type Prompter interface {
	Prompt(ctx context.Context, pane, text string, clear bool) (began bool, err error)
}

// Restorer restores the labelled panes of a project that lost their program (launch.Restorer): each at its shell, or
// running an agent the terminal started on its own, comes back with obor's start or is left at its shell. Serve calls
// it once a connect shows panes that lost their program (lost names them; internal/app/restore.go). It is
// idempotent: a pane running an agent obor started since is left alone, and runs for one project are one at a time. It returns when the
// run has ended (or ctx has).
type Restorer interface {
	RestoreProject(ctx context.Context, root string, lost Lost)
}

// Lost is which panes lost their program: every pane (All: the terminal cannot tell which, Snapshot.PaneRuns false),
// else the panes by id.
type Lost struct {
	All   bool
	Panes []string
}

// Has: pane is one of the lost.
func (l Lost) Has(pane string) bool { return l.All || slices.Contains(l.Panes, pane) }

// None: no pane is lost.
func (l Lost) None() bool { return !l.All && len(l.Panes) == 0 }

// Add is l and m together: every pane when either is, else the panes of both, each once.
func (l Lost) Add(m Lost) Lost {
	if l.All || m.All {
		return Lost{All: true}
	}
	out := Lost{Panes: slices.Clone(l.Panes)}
	for _, p := range m.Panes {
		if !slices.Contains(out.Panes, p) {
			out.Panes = append(out.Panes, p)
		}
	}
	return out
}

// AgentEnv is whose env and cwd a pane's shell is set to: the agent's name, its role (a role name; "" for none), its
// lane ("" for none) and the directory it works in ("" keeps the shell's); Vars the start's own variables
// (agents.Kind.StartEnv; "" unsets one).
type AgentEnv struct {
	Agent, Role, Lane, Cwd string
	Vars                   map[string]string
}

// StartError is a failed start (the multiplexer's error, or the startup dialog seen in the pane). NotReady: the agent
// runs but waits at a startup dialog; Hint says what to do. NoShell: no start was made (launch.Start.Await). Retry:
// Hint already names the command that starts the agent again.
type StartError struct {
	Err      error
	NotReady bool
	Hint     string
	NoShell  bool // the pane did not become its shell within launch.Start.Shell (Await): nothing typed, nothing started
	// Retry: Hint carries the command that starts the agent again (a kind's startup hint): a caller adding that
	// command to a whole failure adds none of its own.
	Retry bool
	// Trust (with NotReady): the terminal accepted a fresh start (its first prompt in its argv) and the pane shows a
	// "Trust this folder?" dialog: the agent runs and takes its first prompt once a person answers it (Launch's
	// TrustDialog). A resumed start at the dialog is no Trust: its wake is not delivered, so it is a failed start.
	Trust bool
}

func (e *StartError) Error() string {
	if e.NotReady {
		return e.Hint
	}
	return e.Err.Error()
}

// TrustDialog is Launcher.Launch's error for a fresh start held by a "Trust this folder?" dialog in its Started's pane:
// the agent runs there and takes the start's first prompt (an argument of its start) once a person answers the dialog,
// so the start happened and Launch's Started is filled as for a start that succeeded. Hint is the one guidance
// (agents.TrustHint: the agent, the pane, the answer of its kind). serve records such a start as started and raises the
// trust needs-you with Hint as its reason; any other caller fails on it as on a failed start.
type TrustDialog struct {
	Hint string
}

func (e *TrustDialog) Error() string { return e.Hint }

// TrustHeld is the TrustDialog in err's chain (nil: none).
func TrustHeld(err error) *TrustDialog {
	var td *TrustDialog
	if errors.As(err, &td) {
		return td
	}
	return nil
}

// Located is where a start or stop of an agent goes (launch.Launcher.Locate): the pane ("": none, no pane is the agent's) and
// why.
type Located struct {
	Pane string
	// Runs: the agent runs in Pane now (a start there is a restart in place); Proof is what proves it (session, start,
	// label, name). Not Runs: Pane is the agent's home (the pane labelled with its name at its shell) or the target.
	Runs  bool
	Proof string
	Label string // Pane's label as the pane list read now shows it
}
