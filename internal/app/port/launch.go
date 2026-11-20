package port

import (
	"context"
	"errors"
	"slices"
	"time"
)

// The effects the core drives through adapters (docs/module-map.md: adapters reach each other only through these):
// Agents starts and stops agents (launch), Prompter types job wakes and lines into their agents, PaneControl is what
// launch itself needs of the terminal (the herdr socket client implements it).

// Agents starts and stops obor agents in their panes; the kill sequence is the implementation's (launch.Launcher).
type Agents interface {
	// Launch starts req.Agent (a lane's agent or a role) in its pane (req.Place: one it places when it has none), its
	// kind and args from its lane type or role, after stopping what runs there (req.NoStop: refused instead). Returns the one-line result and the agent it started; a fresh
	// start held by a trust dialog returns them too, with a *TrustDialog error (TrustHeld).
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
	// Handoff is the request asks for the handoff form of the role's first prompt (the lead's
	// `/obor:lead handoff`): the fresh session a lead starts for itself after writing its Handoff.
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
}

// Placer is Launch.Place: Place places a new pane for the agent, env NAME=VALUE for its shell ("NAME=": set empty,
// the start's env as agents.StartVars builds it), and returns the pane's id. A pointer in Launch, so a Launch stays
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
	// Whose is whose env and cwd that line sets (the AgentEnv the caller types it from): the terminal's AgentStart
	// carries them as its Env and Cwd. Zero: none given (a caller that set the env itself).
	Whose AgentEnv
}

// StartError is a failed start (the multiplexer's error, or the startup dialog seen in the pane). NotReady: the agent
// runs but waits at a startup dialog; Hint says what to do. NoShell: no start was made (Start.Await). Retry: Hint
// already names the command that starts the agent again.
type StartError struct {
	Err      error
	NotReady bool
	Hint     string
	NoShell  bool // the pane did not become its shell within Start.Shell (Await): nothing typed, nothing started
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

// TrustDialog is Agents.Launch's error for a fresh start held by a "Trust this folder?" dialog in its Started's pane:
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
