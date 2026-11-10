// Package agents is the bridge layer (docs/module-map.md): what differs per agent CLI obor drives, as data and pure
// functions over strings and bytes. No herdr, no sockets, no exec, no file I/O: the adapters (launch, setup, guard,
// herdr, inbox) do that and ask the kind what differs. A compiled kind is one package (agents/claude, agents/codex)
// that registers itself from its init; cmd/obor and testkit import all of them. A plugin kind is an executable answering
// obor's kind protocol (agents/plugin, docs/agents/kind-protocol.md; OpenCode, kinds/opencode), registered by
// the config that names it.
package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind is one agent CLI as obor drives it.
type Kind interface {
	Name() string // herdr's --kind and models.<name>.kind

	// A kind's start is Split's two methods (a compiled kind) or Whole's one call (a plugin kind): StartOf.

	// NotReady is why a just-started agent cannot take a prompt yet, from its screen ("": nothing of this kind's).
	NotReady(screen string) string
	// Efforts are the effort values Start.Effort takes, from the CLI's own help; nil: this kind takes none.
	Efforts() []string
	// Vars are every variable a start's env may set: the kind's part of the registry (agents.Vars).
	Vars() []string

	// Stop is the quit recipe; launch runs it (the group proofs and the terminate ladder stay in launch).
	Stop() Stop

	// Headless are the argv forms of this CLI the guard denies (an accident guard: forms agents type).
	Headless() Headless

	// Install is where the MCP server reaches this kind (env: the user's HOME, XDG_CONFIG_HOME); setup checks it. A kind
	// installs nothing user-global.
	Install(kit string, env func(string) string) Install

	// Transcript is the path (a glob) of session sid's transcript (env: HOME and the kind's own config dir variable).
	// It takes no cwd: how a kind names its transcript's directory is the kind's own (Claude resolves symlinks and
	// hashes a long path), and the one true path is the one the session itself reports (the SessionStart hook).
	// "" when the kind keeps none obor can find.
	Transcript(env func(string) string, sid string) string
	// LastTurn is the last turn a part of a session's transcript records, whole lines only (a partial last line is
	// not read); ok false: none in it. The adapter passes the transcript's end, then earlier parts while none is found.
	// A kind whose hooks report a stop reads none (Caps.Status not StatusTranscript).
	LastTurn(tail []byte) (t Turn, ok bool)
	// Session is the session a transcript's first line names (launch finds a new session's transcript by it);
	// ok false: this kind's transcripts name none there, or the line is not such a record.
	Session(first []byte) (s Session, ok bool)
	// Reset reads an API error a turn ended on (errText as the CLI names it, details its message, seen at at):
	// limited, the error is a usage or rate limit, so the turn can go on once it resets; reset, when the
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
	Settings map[string]any // the settings to start with (config.Model.Launch); nil: none
	// SettingsFile is the file that holds the start's settings in place of Settings and of the user's own (a
	// Preparer wrote it: claude's settings file); "": none.
	SettingsFile string
	Resume       string   // the session id to resume ("": fresh)
	Prompt       string   // the first prompt of a fresh start, one line ("": none)
	Read         ReadFile // reads a file an arg names (a config file); nil: none is read
	// Effort is the effort to start with (a type reference's `:<effort>`): the kind sets it, replacing the one
	// Args set; "": Args as given.
	Effort string
	// StrictMCP is a lane's agent starting (a worker, the owner's drive agent): a kind that hides the MCP servers
	// its command line does not name starts that way, obor's own registered on the start (claude: --strict-mcp-config
	// with --mcp-config MCPConfig; a kind that hides nothing ignores it).
	StrictMCP bool
	// Override is what obor overrides of the user's own settings on this start (every agent but a lead,
	// config.Model.Launch: the config's agents.overrides.<kind> with the role's laid over, empty when the config
	// names none); every user setting it does not name applies, and so do the project's own config and obor's
	// wiring. nil: the user's settings as they are (a lead). Each kind applies it its own way (claude: Prepare's
	// merged settings file; codex: -c features.plugins/features.apps/skills.bundled.enabled false and skills.config
	// with enabled false per dir of the user's skills dir; opencode: OPENCODE_DISABLE_CLAUDE_CODE_SKILLS hides
	// ~/.claude/skills and the project's .claude/skills, while .agents/skills stays, and settings, off.plugins and
	// off.mcp go into its generated config).
	Override *Override
	// Agent is the agent's name, for a kind that writes a state file per agent (claude's settings file); "": none
	// is named.
	Agent string
	// Env is the start's environment (HOME, XDG_CONFIG_HOME, …), for a kind that reads its own config; nil: none.
	Env func(string) string
	// Dirs lists a dir's subdirectories by name (a user's skills dir, for a start with an override): the adapter's I/O; nil:
	// none is listed.
	Dirs ListDirs
	// Session is the session id this start runs as, for a kind that takes one on its command line (Claude's
	// --session-id: obor chooses it and records it before the start); "" for one that takes none. A resume
	// names Resume instead.
	Session string
	// WriteState writes a file the start reads (OpenCode's generated config) as name in the project's state
	// dir, replacing the last start's, and returns its path: the adapter's I/O; nil: the start writes none.
	WriteState func(name string, data []byte) (path string, err error)
	// WriteDirs are dirs outside its cwd the agent writes (config.RoleWriteDirs: a reviewer's or deep dive's
	// files go to the main checkout's reviews dir from its role worktree): a kind that sandboxes its writes to the cwd
	// lets it write there too (codex: --add-dir); a kind with no such sandbox ignores them.
	WriteDirs []string
	// Dir is the start dir, the agent's cwd (its lane worktree, or the main checkout or role worktree of a role): a
	// kind reads the project's committed CLI config there with Read, for the keys its policy flags give way to (
	// a project customizes its agents natively); "" or Read nil: no project file is read, the flags as before.
	Dir string
}

// Override is one kind's override of the user's own settings (config agents.overrides.<kind>, a role's laid over
// it). Settings are keys in the kind's native shape, set over the user's. Off switches off entries of the user's own
// config, each named by a substring of the entry.
type Override struct {
	Settings map[string]any `json:"settings,omitempty"`
	Off      Off            `json:"off,omitzero"`
}

// Off are the user's entries an override switches off, each a substring of the entry it names: a hook's command, a
// plugin's id, a skill's dir name, an MCP server's name.
type Off struct {
	Hooks   []string `json:"hooks,omitempty"`
	Plugins []string `json:"plugins,omitempty"`
	Skills  []string `json:"skills,omitempty"`
	MCP     []string `json:"mcp,omitempty"`
}

// Over is o with over laid over it: over's settings win key by key (Merge), its off lists are added.
func (o Override) Over(over Override) Override {
	return Override{Settings: Merge(o.Settings, over.Settings), Off: Off{
		Hooks:   slices.Concat(o.Off.Hooks, over.Off.Hooks),
		Plugins: slices.Concat(o.Off.Plugins, over.Off.Plugins),
		Skills:  slices.Concat(o.Off.Skills, over.Off.Skills),
		MCP:     slices.Concat(o.Off.MCP, over.Off.MCP),
	}}
}

// Merge is a with b laid over it, as a new map that shares no object with either: where both hold an object under a
// key the two merge the same way, anything else in b replaces a's value.
func Merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		if m, ok := v.(map[string]any); ok {
			v = Merge(m, nil)
		}
		out[k] = v
	}
	for k, v := range b {
		if bm, ok := v.(map[string]any); ok {
			am, _ := out[k].(map[string]any)
			v = Merge(am, bm)
		}
		out[k] = v
	}
	return out
}

// Matches: entry holds one of subs (an Off list's match of a user's entry).
func Matches(subs []string, entry string) bool {
	for _, s := range subs {
		if s != "" && strings.Contains(entry, s) {
			return true
		}
	}
	return false
}

// ReadFile reads a file for a kind (os.ReadFile): the adapter's I/O, handed to a pure function.
type ReadFile func(path string) ([]byte, error)

// ListDirs is the names of dir's subdirectories (project.SubDirs): the adapter's I/O, handed to a pure function.
type ListDirs func(dir string) ([]string, error)

// UserDir is the user's config dir of a kind's CLI: $<envVar> when the start's environment sets it, else
// $HOME/<dflt>; "": the start has no environment, or it names neither (nothing of the user's is read then).
func UserDir(in Start, envVar, dflt string) string {
	if in.Env == nil {
		return ""
	}
	if d := in.Env(envVar); d != "" {
		return d
	}
	if home := in.Env("HOME"); home != "" {
		return filepath.Join(home, dflt)
	}
	return ""
}

// UserSkills are the skills of a user's config dir (UserDir): the names of the subdirectories of its skills dir,
// sorted, without the dot dirs (a CLI's own, such as Codex's .system). nil: no config dir (""), no lister, or a
// skills dir that does not list.
func UserSkills(in Start, userDir string) []string {
	if userDir == "" || in.Dirs == nil {
		return nil
	}
	dir := filepath.Join(userDir, "skills")
	names, err := in.Dirs(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range names {
		if !strings.HasPrefix(n, ".") {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// ProjectFile is the file rel of the start's dir (in.Dir), read with in.Read. found false: no dir or reader,
// or no such file; err: a file there that could not be read (the kind then starts as if it set nothing).
func ProjectFile(in Start, rel string) (b []byte, found bool, err error) {
	if in.Dir == "" || in.Read == nil {
		return nil, false, nil
	}
	b, err = in.Read(filepath.Join(in.Dir, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return b, true, err
}

// ProjectChecker is a kind that reads a project's committed CLI config: Warnings are `obor config validate`'s
// notes on the files in dir, "<file>: <why>" each (a file that does not parse, a key that disables part of obor).
type ProjectChecker interface {
	ProjectWarnings(dir string, read ReadFile) []string
}

// ProjectWarnings are every registered kind's ProjectChecker warnings on dir's files, kinds in name order; a kind in
// described (a plugin kind obor config validate just described) answers in place of the registry's, whose
// manifest may not be cached.
func ProjectWarnings(dir string, read ReadFile, described map[string]Kind) []string {
	var out []string
	for _, n := range Names() {
		k := lookup(n)
		if d := described[n]; d != nil {
			k = d
		}
		if c, ok := k.(ProjectChecker); ok {
			out = append(out, c.ProjectWarnings(dir, read)...)
		}
	}
	return out
}

// InstallHost is the machine a kind's CLI is installed on, the adapter's I/O handed to a pure check.
type InstallHost struct {
	GOOS     string                            // runtime.GOOS
	LookPath func(name string) (string, error) // the CLI on the user's PATH
	Resolve  func(path string) (string, error) // symlinks followed (filepath.EvalSymlinks)
	// Quarantine is the file's com.apple.quarantine flags (xattr -p, "0181" → 0x0181); ok false: the flag is not set.
	Quarantine func(path string) (flags uint32, ok bool)
}

// InstallChecker is a kind that checks its CLI's install: Warnings are `obor setup mcp`'s lines, one per problem,
// each naming the file and the fix (obor applies none).
type InstallChecker interface {
	InstallWarnings(h InstallHost) []string
}

// StartChecker is a kind that refuses a start its CLI's install cannot make work: StartRefusal is why ("" the
// start may go ahead), naming the files and the owner's fix. h is the machine's install; the kind runs no process.
type StartChecker interface {
	StartRefusal(h InstallHost) string
}

// StartRefusal is the kind name's refusal of a start on machine h ("" when the start may go ahead, or no such kind).
func StartRefusal(kind string, h InstallHost) string {
	if c, ok := lookup(kind).(StartChecker); ok {
		return c.StartRefusal(h)
	}
	return ""
}

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

// Install is how obor's MCP server reaches a kind.
type Install struct {
	MCP MCPRegistration
}

// Where obor's MCP server reaches a CLI.
const (
	Manifest = "manifest" // the kit's plugin manifest (Path)
	PerStart = "start"    // launch args on every start: nothing to install
)

// MCPRegistration is how obor's MCP server reaches a CLI.
type MCPRegistration struct {
	Where    string         // Manifest or PerStart
	Path     string         // the manifest ("" for PerStart)
	Key      []string       // Manifest: the obor entry's place in the file's JSON, outermost first
	Entry    map[string]any // Manifest: the obor entry
	Unpinned map[string]any // Manifest: the entry as written before the pin: registered, with a warning
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
// false: its start, the turn still running) and, when it ended on an error (a usage limit), the error as the
// CLI names it and its message (Error "": it did not, or has not ended yet), and when the limit it hit resets as the
// transcript records it (zero: unknown).
type Turn struct {
	ID, Error, Details string
	Ended              bool
	Reset              time.Time
}

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
	HooksPlugin                      // obor's plugin for the CLI: it leaves a marker obor status checks
)

// Caps are a kind's capabilities.
type Caps struct {
	Hooks      HookTransport
	Inbox      bool // Deliver posts over the session's inbox socket
	Resume     bool // Start honours Resume
	Skills     bool // a first prompt may be a plugin's slash command (/obor:lead)
	Transcript bool // Transcript finds the session file
	// Status is where the agent's status and turns come from: StatusTranscript, LastTurn reads the session's
	// turns from its transcript (Codex); StatusTerminal, herdr's agent detection; StatusHooks, its hooks.
	Status   Status
	Sessions bool // Session reads the session a transcript begins with: launch reports a fresh start's
	// NamedSession: a fresh start names its session id (Start.Session: obor's choice, recorded before the start) and
	// its transcript is the path the session itself reports: obor is the only source of its session ids, so herdr's
	// agent_session is never adopted for a resume.
	NamedSession  bool
	ProjectPlugin bool // loads obor's plugin from the project's .claude/settings.json (a worktree needs the file too)
	ModelArg      bool // its own default model may not work: a model should name its id
	// VersionVar is the session's environment variable holding the CLI's version, for the plugin marker ("": none)
	VersionVar string
}

// Status is a kind's source of its agents' status (idle, working, done) and turns (Caps.Status).
type Status string

const (
	StatusTerminal   Status = "terminal"   // the terminal's agent detection (herdr's --kind)
	StatusHooks      Status = "hooks"      // the kind's hooks report a turn's start and stop
	StatusTranscript Status = "transcript" // LastTurn reads the turns from the session's transcript
)

// Statuses are the Status values, in the manifest's order (a plugin kind's `status`).
var Statuses = []Status{StatusTerminal, StatusHooks, StatusTranscript}

// TurnsRead: k's turns are read from its transcript (Caps.Status StatusTranscript).
func TurnsRead(k Kind) bool { return k.Caps().Status == StatusTranscript }

// Default is the kind of an agent with no type that herdr reports no kind for.
const Default = "claude"

// Pin is the variable every kit-owned `obor mcp` start sets, as hooks do: bin/obor then runs this kit, not the main
// checkout of the obor checkout the agent happens to sit in.
const Pin = "OBOR_NO_SELF_HOST"

// EnvVars are the variables `obor mcp` reads from the agent's session (internal/mcp); a CLI that starts MCP servers
// with a stripped environment forwards them.
var EnvVars = []string{RoleVar, LaneVar, PaneVar, "OBOR_STATE_HOME"}

// RoleVar, LaneVar and PaneVar are an agent's identity in its env (the role's env value, its lane, its terminal pane:
// project.Pane resolves it); RetiredVars are variables an older obor exported that no start sets any more
// (OBOR_AGENT, a name is derived, project.Self): every start unsets them.
const (
	RoleVar = "OBOR_ROLE"
	LaneVar = "OBOR_LANE"
	PaneVar = "OBOR_PANE"
)

var RetiredVars = []string{"OBOR_AGENT"}

// Vars is the registry of every variable a start of any agent sets: the identity vars, the retired ones and
// each kind's own (Kind.Vars), sorted. A start unsets every one it does not set, so no value of an earlier start in the
// pane's shell survives into it, whatever the kinds.
func Vars() []string {
	out := append([]string{RoleVar, LaneVar, PaneVar}, RetiredVars...)
	for _, n := range Names() {
		if k := lookup(n); k != nil {
			out = append(out, k.Vars()...)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// EnvVar is one variable a start sets.
type EnvVar struct{ Name, Value string }

// StartVars is the one builder of a start's env: set, the role's env value ("": none), the lane ("": none), the
// pane the agent starts in ("": not known yet, a pane being made) and the kind's StartEnv vars (a "" value:
// none), in that order (a kind's sorted); unset, every registry var (Vars) the
// start does not set and keep does not name, sorted. keep is a var the caller leaves as the pane has it (an unknown
// agent's OBOR_ROLE: obor cannot tell it).
func StartVars(role, lane, pane string, kind map[string]string, keep ...string) (set []EnvVar, unset []string) {
	if role != "" {
		set = append(set, EnvVar{RoleVar, role})
	}
	if lane != "" {
		set = append(set, EnvVar{LaneVar, lane})
	}
	if pane != "" {
		set = append(set, EnvVar{PaneVar, pane})
	}
	keys := make([]string, 0, len(kind))
	for k := range kind {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := kind[k]; v != "" && k != RoleVar && k != LaneVar && k != PaneVar {
			set = append(set, EnvVar{k, v})
		}
	}
	for _, v := range Vars() {
		if !slices.Contains(keep, v) && !slices.ContainsFunc(set, func(e EnvVar) bool { return e.Name == v }) {
			unset = append(unset, v)
		}
	}
	return set, unset
}

// The registry: the compiled kinds (Register, from their init) and the plugin kinds the loaded config names
// (SetPlugins). A config load replaces the plugin kinds while serve's goroutines look kinds up: mu guards both.
var (
	mu      sync.RWMutex
	kinds   = map[string]Kind{}
	plugins = map[string]bool{} // the names in kinds that are plugin kinds
)

// Register adds k (a kind package's init); a second kind of one name panics.
func Register(k Kind) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := kinds[k.Name()]; dup {
		panic("agents: kind " + k.Name() + " registered twice")
	}
	kinds[k.Name()] = k
}

// Lookup is the kind of name; ok false: no such kind.
func Lookup(name string) (Kind, bool) {
	mu.RLock()
	defer mu.RUnlock()
	k, ok := kinds[name]
	return k, ok
}

// lookup is Lookup's kind alone (nil: none).
func lookup(name string) Kind {
	k, _ := Lookup(name)
	return k
}

// PluginLoader builds the kinds a project's config names as executables (kinds.<name>.command); the plugin
// package (internal/agents/plugin) sets Plugins from its init, so the config (which cannot import it) registers them.
type PluginLoader interface {
	// Kind is the kind name answering obor's kind protocol as command, its manifest the one obor setup agents cached in
	// stateDir: no process runs (hooks and the guard load the config).
	Kind(name, command, stateDir string) Kind
	// Check runs command's describe: why it cannot be the kind name (not executable, no answer, a bad manifest), else
	// the kind with the manifest it answered, which it caches nowhere (obor config validate checks efforts against it).
	Check(name, command string) (Kind, error)
}

// Plugins is the binary's PluginLoader; nil: this binary runs no plugin kinds.
var Plugins PluginLoader

// SetPlugins makes ks the plugin kinds (a config load: the kinds its kinds.<name>.command name), replacing the last
// load's. A plugin kind never replaces a compiled one (obor config validate refuses the name): it is left out.
func SetPlugins(ks []Kind) {
	mu.Lock()
	defer mu.Unlock()
	for n := range plugins {
		delete(kinds, n)
	}
	clear(plugins)
	for _, k := range ks {
		if _, compiled := kinds[k.Name()]; !compiled {
			kinds[k.Name()] = k
			plugins[k.Name()] = true
		}
	}
}

// IsPlugin: name is a plugin kind of the loaded config (no compiled kind).
func IsPlugin(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return plugins[name]
}

// Uncached is a plugin kind that may have no manifest at hand: Uncached is why (nil: it has one). Without one
// its Headless is empty, so the guard denies its program outright rather than let a headless run through.
type Uncached interface {
	Uncached() error
}

// Fallible is a kind whose install can fail (a plugin kind runs a process): Install with its error. Its
// Install gives the failure's zero form (no registration, Lane saying why).
type Fallible interface {
	InstallErr(kit string, env func(string) string) (Install, error)
}

// Launch is one start as its kind built it: the argv after the binary and the start's own env (variable → value; ""
// unsets it), from one complete Start (StartOf).
type Launch struct {
	Args []string
	Env  map[string]string
}

// Whole is a kind whose start is one call (a plugin kind runs its command once per start): the argv, the env and
// the files it writes come from one reply to the complete Start, applied together.
type Whole interface {
	StartWhole(in Start) (Launch, error)
}

// Preparer is a Split kind whose start reads or writes files before its argv is built (claude's merged settings
// file): Prepare is in with that done and what it wrote named (Start.SettingsFile). An error refuses the start.
type Preparer interface {
	Prepare(in Start) (Start, error)
}

// Split is a compiled kind's start: its argv and its env by two methods (a plugin kind is Whole and has
// neither).
type Split interface {
	Kind
	// Start is the argv after the binary: the type's args, the kind's settings and MCP registration (none without a
	// kit), then the resume form or the first prompt (never both).
	Start(in Start) []string
	// StartEnv is the environment a start needs beyond the pane's (variable → value; "" unsets it). An error: the
	// start cannot carry in.Effort (it says why).
	StartEnv(in Start) (map[string]string, error)
}

// StartOf is k's start of in, a complete Start built once at the launch boundary: a Whole kind's one call;
// a Split kind's env (StartEnv) and its argv in two passes of Start: the type's launch args (Args with
// Settings or SettingsFile, Effort, StrictMCP, Override with Env and Dirs, WriteDirs), then the start over them (Kit, Resume,
// Session, Prompt), Dir and Read in both; a Preparer's Prepare comes first. An error: the start cannot carry
// in.Effort, a plugin kind's start or a Prepare failed, or k has no start.
func StartOf(k Kind, in Start) (Launch, error) {
	if w, ok := k.(Whole); ok {
		return w.StartWhole(in)
	}
	s, ok := k.(Split)
	if !ok {
		return Launch{}, fmt.Errorf("kind %s has no start", k.Name())
	}
	env, err := s.StartEnv(in)
	if err != nil {
		return Launch{}, err
	}
	if p, ok := k.(Preparer); ok {
		if in, err = p.Prepare(in); err != nil {
			return Launch{}, err
		}
	}
	args := s.Start(Start{Args: in.Args, Settings: in.Settings, SettingsFile: in.SettingsFile, Effort: in.Effort, StrictMCP: in.StrictMCP,
		Override: in.Override, Env: in.Env, Dirs: in.Dirs, WriteDirs: in.WriteDirs, Dir: in.Dir, Read: in.Read})
	args = s.Start(Start{Kit: in.Kit, Args: args, Resume: in.Resume, Session: in.Session, Prompt: in.Prompt,
		Dir: in.Dir, Read: in.Read})
	return Launch{Args: args, Env: env}, nil
}

// InstallOf is k.Install(kit, env), or a Fallible kind's InstallErr.
func InstallOf(k Kind, kit string, env func(string) string) (Install, error) {
	if f, ok := k.(Fallible); ok {
		return f.InstallErr(kit, env)
	}
	return k.Install(kit, env), nil
}

// ByProgram is the kind whose Headless.Programs (default: its Name()) holds program, an executable's base name; ok
// false: no kind runs as program.
func ByProgram(program string) (Kind, bool) {
	for _, n := range Names() {
		if k := lookup(n); k != nil && slices.Contains(Programs(k), program) {
			return k, true
		}
	}
	return nil, false
}

// Skills: the kind named name takes a plugin's slash command as its first prompt (its Caps.Skills); an unknown kind
// does not.
func Skills(name string) bool {
	k, ok := Lookup(name)
	return ok && k.Caps().Skills
}

// Resumes: the kind named name resumes a session (its Caps.Resume), as OpenCode's does not; an unknown kind
// does not either.
func Resumes(name string) bool {
	k, ok := Lookup(name)
	return ok && k.Caps().Resume
}

// NamedSession: the kind named name starts a session obor names itself (its Caps.NamedSession); an unknown
// kind's sessions are not obor's.
func NamedSession(name string) bool {
	k, ok := Lookup(name)
	return ok && k.Caps().NamedSession
}

// CheckEffort is why the kind named kind cannot start with effort ("" takes any kind): an unknown kind, a kind that
// takes no effort, or a value not among its Efforts (obor spec set, dispatch and config validate refuse it).
func CheckEffort(kind, effort string) error {
	if effort == "" {
		return nil
	}
	k, ok := Lookup(kind)
	if !ok {
		return fmt.Errorf("kind %s has no bridge, so no effort", kind)
	}
	return checkEffort(k, effort)
}

// ArgsChecker is a kind whose effort also depends on the type's args (OpenCode: --model; a plugin kind asks its
// start).
type ArgsChecker interface {
	CheckEffort(args []string, effort string) error
}

// CheckEffortArgs is CheckEffort and, for a kind that checks its args too (ArgsChecker), why args cannot carry effort.
func CheckEffortArgs(kind string, args []string, effort string) error {
	if err := CheckEffort(kind, effort); err != nil || effort == "" {
		return err
	}
	return CheckEffortOf(lookup(kind), args, effort)
}

// CheckEffortOf is CheckEffortArgs for a kind at hand (obor config validate's plugin kind as its describe just
// answered), not the registry's.
func CheckEffortOf(k Kind, args []string, effort string) error {
	if effort == "" {
		return nil
	}
	if err := checkEffort(k, effort); err != nil {
		return err
	}
	if c, ok := k.(ArgsChecker); ok {
		return c.CheckEffort(args, effort)
	}
	return nil
}

// ModelEditor is a kind that names its model in the args (config.Model): WithModel is args with model in place
// of the model they name (appended when they name none; "" drops it), every other arg as it is.
type ModelEditor interface {
	WithModel(args []string, model string) []string
}

// ModelArgs is args of kind with model as their model (ModelEditor); an error for a kind whose model is no arg.
func ModelArgs(kind string, args []string, model string) ([]string, error) {
	e, ok := lookup(kind).(ModelEditor)
	if !ok {
		return nil, fmt.Errorf("kind %s names no model in its args", kind)
	}
	return e.WithModel(args, model), nil
}

// SetFlag is args with the value of the flag any of names spells (`<name> <value>` or `<name>=<value>`) set to value:
// the first one keeps its place and spelling, a later one is dropped; none: names[0] value appended. Value "" drops
// every one. Every other arg stays as it is (a bridge's WithModel).
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
	mu.RLock()
	defer mu.RUnlock()
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

// ResetIn is the reset time a limit message names: an epoch after a '|', else a clock time's next occurrence
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
