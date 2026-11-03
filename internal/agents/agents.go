// Package agents is the bridge layer (docs/module-map.md): what differs per agent CLI obor drives, as data and pure
// functions over strings and bytes. No herdr, no sockets, no exec, no file I/O: the adapters (launch, setup, guard,
// herdr, inbox) do that and ask the kind what differs. A kind is one package (agents/claude, agents/codex,
// agents/opencode) that registers itself from its init; cmd/obor and testkit import all of them. Adding a kind = one
// package implementing Kind + a config entry.
package agents

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is one agent CLI as obor drives it.
type Kind interface {
	Name() string // herdr's --kind and models.<name>.kind

	// Start is the argv after the binary: the type's args, the kind's settings and MCP registration (none without a
	// kit), then the resume form or the first prompt (never both).
	Start(in Start) []string
	// NotReady is why a just-started agent cannot take a prompt yet, from its screen ("": nothing of this kind's).
	NotReady(screen string) string
	// Efforts are the effort values Start.Effort takes, from the CLI's own help (T-695); nil: this kind takes none.
	Efforts() []string
	// StartEnv is the environment a start needs beyond the pane's (variable → value; "" unsets it): OpenCode's model
	// and effort reach obor's plugin this way (T-703). An error: the start cannot carry in.Effort (it says why).
	StartEnv(in Start) (map[string]string, error)

	// Stop is the quit recipe; launch runs it (the group proofs and the terminate ladder stay in launch).
	Stop() Stop

	// Headless are the argv forms of this CLI the guard denies (an accident guard: forms agents type).
	Headless() Headless

	// Install is what setup registers for this kind and where (env: the user's HOME, XDG_CONFIG_HOME); setup does the
	// file operations.
	Install(kit string, env func(string) string) Install

	// Transcript is the path (a glob) of session sid's transcript (env: HOME and the kind's own config dir variable);
	// with cwd, only the file of a session started in cwd, "" when this kind's transcripts do not record their cwd.
	// "" when the kind keeps none obor can find.
	Transcript(env func(string) string, cwd, sid string) string
	// LastTurn is the last turn a part of a session's transcript records, whole lines only (a partial last line is
	// not read); ok false: none in it. The adapter passes the transcript's end, then earlier parts while none is found.
	// A kind whose hooks report a stop reads none (Caps.Turns false).
	LastTurn(tail []byte) (t Turn, ok bool)
	// Session is the session a transcript's first line names (T-577: launch finds a new session's transcript by it);
	// ok false: this kind's transcripts name none there, or the line is not such a record.
	Session(first []byte) (s Session, ok bool)
	// Reset reads an API error a turn ended on (errText as the CLI names it, details its message, seen at at):
	// limited, the error is a usage or rate limit, so the turn can go on once it resets (T-568); reset, when the
	// message says it does (zero: it names no time).
	Reset(errText, details string, at time.Time) (reset time.Time, limited bool)

	// Deliver is how text reaches a running session of this kind: Typed (herdr agent.prompt), or an inbox post built
	// from the session's SessionStart record (ok false: the record names no inbox; keep the line queued).
	Deliver(text string, record []byte) (Delivery, bool)
	// InboxRecord is the inbox record of the session the SessionStart hook runs in, from that session's environment
	// (env), in the format Deliver reads; nil: the session has no inbox of this kind.
	InboxRecord(env func(string) string) []byte

	Caps() Caps
}

// Start is one start of an agent.
type Start struct {
	Kit      string         // this kit's root ("": no kit to name; no MCP registration on the command line)
	Args     []string       // the type's args as given
	Settings map[string]any // the settings to start with (config.Model.LaunchArgs); nil: none
	Resume   string         // the session id to resume ("": fresh)
	Prompt   string         // the first prompt of a fresh start, one line ("": none)
	Read     ReadFile       // reads a file an arg names (a config file); nil: none is read
	// Effort is the effort to start with (a type reference's `:<effort>`, T-695): the kind sets it, replacing the one
	// Args set; "": Args as given.
	Effort string
	// Env is the start's environment (HOME, XDG_CONFIG_HOME, …), for a kind that reads its own config; nil: none.
	Env func(string) string
}

// ReadFile reads a file for a kind (os.ReadFile): the adapter's I/O, handed to a pure function.
type ReadFile func(path string) ([]byte, error)

// Stop is a kind's quit recipe.
type Stop struct {
	Quit        string   // typed with Enter ("/exit", "/quit")
	SecondEnter bool     // the slash-command popup takes the first Enter; a second one, a poll later, runs the quit
	ClearInput  []string // the keys that clear the input (what a person typed; the quit and its popup): before the quit, and when it did not exit
	AfterEscape []string // the keys after a forced Escape, before the quit (the interrupted prompt is put back)
	Program     string   // the leader's program when it is not Name() ("": Name())
	Dialog      *Dialog  // the dialog the quit opens while the agent has background tasks; nil: none
}

// Dialog is a quit's dialog: without force it is closed (Escape) and the stop refused; force picks Option.
type Dialog struct {
	Option string         // the option force picks; on the screen while the dialog is open
	Key    *regexp.Regexp // Option's key on the screen: its first submatch (no match: the dialog shows no key for it)
}

// Headless are the argv forms of a CLI that run it headless.
type Headless struct {
	Programs    []string // the executables the forms are denied for (nil: Name()): the command word's base name
	Subcommands []string // denied as the first positional word
	Anywhere    []string // also denied as any later positional word before `--`
	ValueOpts   []string // global options that take the next word as their value (so it is no subcommand)
	PrintFlag   bool     // -p / --print, also inside a short-flag cluster, before `--`
}

// Install is what setup registers for a kind.
type Install struct {
	MCP    MCPRegistration
	Plugin *Link // a user-global plugin file to link; nil: none
}

// Where obor's MCP server reaches a CLI.
const (
	Manifest = "manifest" // the kit's plugin manifest (Path)
	PerStart = "start"    // launch args on every start: nothing to install
	Config   = "config"   // the obor entry of the CLI's user config (Path), which obor setup agents writes
)

// MCPRegistration is how obor's MCP server reaches a CLI.
type MCPRegistration struct {
	Where    string         // Manifest, PerStart or Config
	Path     string         // the manifest, or the user config file ("" for PerStart)
	AltPath  string         // Config: the other spelling of the file, read and edited when it is the only one there
	Key      []string       // Manifest, Config: the obor entry's place in the file's JSON, outermost first
	Entry    map[string]any // Manifest, Config: the obor entry
	Unpinned map[string]any // Manifest, Config: the entry as written before the pin (T-420): registered, with a warning
	// Lane is how a start with the type's args gets the server (obor setup mcp --lane): static true when the Where
	// registration serves it (setup checks that file), else registered per start or not, how saying which.
	Lane func(args []string, read ReadFile) (how string, registered, static bool)
}

// Session is a session as its transcript's first line names it: its id, the cwd it started in and when.
type Session struct {
	ID, Cwd string
	Started time.Time
}

// Turn is one turn of a session as its transcript records it: its id, whether the event read is its end (Ended;
// false: its start, the turn still running, T-571) and, when it ended on an error (a usage limit), the error as the
// CLI names it and its message (Error "": it did not, or has not ended yet), and when the limit it hit resets as the
// transcript records it (zero: unknown; T-568).
type Turn struct {
	ID, Error, Details string
	Ended              bool
	Reset              time.Time
}

// Link is a plugin link: From in the kit, To the user-global path.
type Link struct{ From, To string }

// Delivery is how one text reaches a running session.
type Delivery struct {
	Typed  bool     // herdr agent.prompt with the text as it is
	Socket string   // inbox: the unix socket to dial
	Lines  []string // inbox: the JSON lines to write, in order
}

// HookTransport is how a kind runs obor's hooks.
type HookTransport int

const (
	HooksNone   HookTransport = iota // no hooks
	HooksNative                      // the CLI's own hooks (the plugin's hooks/hooks.json)
	HooksPlugin                      // obor's plugin for the CLI: it leaves a marker obor status checks (T-307)
)

// Caps are a kind's capabilities.
type Caps struct {
	Hooks         HookTransport
	Inbox         bool // Deliver posts over the session's inbox socket
	Resume        bool // Start honours Resume
	Skills        bool // a first prompt may be a plugin's slash command (/obor:lead, T-594)
	Transcript    bool // Transcript finds the session file
	Turns         bool // LastTurn reads the session's turns from its transcript (T-563)
	Sessions      bool // Session reads the session a transcript begins with: launch reports a fresh start's (T-577)
	ProjectPlugin bool // loads obor's plugin from the project's .claude/settings.json (a worktree needs the file too, T-705)
	ModelArg      bool // its own default model may not work: a model should name its id (T-705)
	// VersionVar is the session's environment variable holding the CLI's version, for the plugin marker ("": none)
	VersionVar string
}

// Default is the kind of an agent with no type that herdr reports no kind for.
const Default = "claude"

// Pin is the variable every kit-owned `obor mcp` start sets, as hooks do: bin/obor then runs this kit, not the main
// checkout of the obor checkout the agent happens to sit in (T-420).
const Pin = "OBOR_NO_SELF_HOST"

// EnvVars are the variables `obor mcp` reads from the agent's session (internal/mcp); a CLI that starts MCP servers
// with a stripped environment forwards them.
var EnvVars = []string{"OBOR_ROLE", "OBOR_AGENT", "OBOR_LANE", "HERDR_PANE_ID", "OBOR_STATE_HOME"}

var kinds = map[string]Kind{}

// Register adds k (a kind package's init); a second kind of one name panics.
func Register(k Kind) {
	if _, dup := kinds[k.Name()]; dup {
		panic("agents: kind " + k.Name() + " registered twice")
	}
	kinds[k.Name()] = k
}

// Lookup is the kind of name; ok false: no such kind.
func Lookup(name string) (Kind, bool) {
	k, ok := kinds[name]
	return k, ok
}

// ByProgram is the kind whose Headless.Programs (default: its Name()) holds program, an executable's base name; ok
// false: no kind runs as program.
func ByProgram(program string) (Kind, bool) {
	for _, n := range Names() {
		if slices.Contains(Programs(kinds[n]), program) {
			return kinds[n], true
		}
	}
	return nil, false
}

// Skills: the kind named name takes a plugin's slash command as its first prompt (its Caps.Skills); an unknown kind
// does not.
func Skills(name string) bool {
	k, ok := kinds[name]
	return ok && k.Caps().Skills
}

// Resumes: the kind named name resumes a session (its Caps.Resume), as OpenCode's does not (T-581); an unknown kind
// does not either.
func Resumes(name string) bool {
	k, ok := kinds[name]
	return ok && k.Caps().Resume
}

// CheckEffort is why the kind named kind cannot start with effort ("" takes any kind): an unknown kind, a kind that
// takes no effort, or a value not among its Efforts (T-695: obor spec set, dispatch and config validate refuse it).
func CheckEffort(kind, effort string) error {
	if effort == "" {
		return nil
	}
	k, ok := kinds[kind]
	if !ok {
		return fmt.Errorf("kind %s has no bridge, so no effort", kind)
	}
	return checkEffort(k, effort)
}

// ArgsChecker is a kind whose effort also depends on the type's args (OpenCode: --model, T-697).
type ArgsChecker interface {
	CheckEffort(args []string, effort string) error
}

// CheckEffortArgs is CheckEffort and, for a kind that checks its args too (ArgsChecker), why args cannot carry effort.
func CheckEffortArgs(kind string, args []string, effort string) error {
	if err := CheckEffort(kind, effort); err != nil || effort == "" {
		return err
	}
	if c, ok := kinds[kind].(ArgsChecker); ok {
		return c.CheckEffort(args, effort)
	}
	return nil
}

// ModelEditor is a kind that names its model in the args (config.Model, T-765): WithModel is args with model in place
// of the model they name (appended when they name none; "" drops it), every other arg as it is.
type ModelEditor interface {
	WithModel(args []string, model string) []string
}

// ModelArgs is args of kind with model as their model (ModelEditor); an error for a kind whose model is no arg.
func ModelArgs(kind string, args []string, model string) ([]string, error) {
	e, ok := kinds[kind].(ModelEditor)
	if !ok {
		return nil, fmt.Errorf("kind %s names no model in its args", kind)
	}
	return e.WithModel(args, model), nil
}

// SetFlag is args with the value of the flag any of names spells (`<name> <value>` or `<name>=<value>`) set to value:
// the first one keeps its place and spelling, a later one is dropped; none: names[0] value appended. Value "" drops
// every one. Every other arg stays as it is (a bridge's WithModel; T-753).
func SetFlag(args, names []string, value string) []string {
	out := make([]string, 0, len(args)+2)
	set := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		spaced, joined := false, ""
		for _, n := range names {
			if a == n && i+1 < len(args) {
				spaced = true
			} else if strings.HasPrefix(a, n+"=") {
				joined = n
			}
		}
		switch {
		case spaced:
			if !set && value != "" {
				out = append(out, a, value)
				set = true
			}
			i++
		case joined != "":
			if !set && value != "" {
				out = append(out, joined+"="+value)
				set = true
			}
		default:
			out = append(out, a)
		}
	}
	if !set && value != "" {
		out = append(out, names[0], value)
	}
	return out
}

// checkEffort is CheckEffort for a kind at hand.
func checkEffort(k Kind, effort string) error {
	kind := k.Name()
	switch {
	case len(k.Efforts()) == 0:
		return fmt.Errorf("kind %s takes no effort (%q)", kind, effort)
	case !slices.Contains(k.Efforts(), effort):
		return fmt.Errorf("effort %q is not one of %s's (%s)", effort, kind, strings.Join(k.Efforts(), ", "))
	}
	return nil
}

// Programs are k's executables: Headless.Programs, else its Name().
func Programs(k Kind) []string {
	if p := k.Headless().Programs; len(p) > 0 {
		return p
	}
	return []string{k.Name()}
}

// Names are the registered kinds, sorted.
func Names() []string {
	out := make([]string, 0, len(kinds))
	for n := range kinds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// resetEpoch is a limit message's reset as Unix seconds after a '|' (Claude Code's "… usage limit reached|<epoch>").
var resetEpoch = regexp.MustCompile(`\|(\d{10})\b`)

// resetClock is a limit message's reset as a clock time: "resets 6pm", "resets at 6:30 PM", "try again at 6:30 PM".
var resetClock = regexp.MustCompile(`(?i)\b(?:resets|try again)(?: at)? (\d{1,2})(?::(\d{2}))? ?([ap])\.?m\b`)

// ResetIn is the reset time a limit message names (T-568): an epoch after a '|', else a clock time's next occurrence
// after at, in at's location (the clock of the machine the agent runs on). Zero: it names none.
func ResetIn(text string, at time.Time) time.Time {
	if m := resetEpoch.FindStringSubmatch(text); m != nil {
		sec, _ := strconv.ParseInt(m[1], 10, 64) // ten digits: cannot fail
		return time.Unix(sec, 0)
	}
	m := resetClock.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}
	}
	h, _ := strconv.Atoi(m[1]) // digits: cannot fail
	mins, _ := strconv.Atoi(m[2])
	if h < 1 || h > 12 || mins > 59 {
		return time.Time{}
	}
	h %= 12
	if strings.EqualFold(m[3], "p") {
		h += 12
	}
	t := time.Date(at.Year(), at.Month(), at.Day(), h, mins, 0, 0, at.Location())
	if t.Before(at) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}
