// Package plugin is a plugin kind (T-1036): an agents.Kind whose CLI-specific part is an executable, any language,
// answering obor's kind protocol (docs/agents/kind-protocol.md): one JSON request on stdin, one JSON reply on stdout,
// per run. A project names it as kinds.<name>.command; obor serve (at its start and when the command changes, Ensure),
// obor init and obor setup agents run its describe and cache the reply (the manifest) in the state dir. What the
// manifest answers (caps, stop, headless forms, the transcript glob, the inbox record) is read from that cache, never by
// running the command: hooks and the guard load the config, and hook time stays the binary's own; without a cache the
// guard denies the kind's program outright (T-1054). The calls that run it (start, not-ready, install, turn, deliver) first describe it again when
// the command changed (its mtime, or another path) since the cache was written.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"obor/internal/agents"
	"obor/internal/project"
)

func init() { agents.Plugins = loader{} }

// Version is the protocol's version: every request carries it, every reply must name it.
const Version = 1

// Manifest is describe's reply: what obor reads without running the command.
type Manifest struct {
	Version  int      `json:"version"`
	Name     string   `json:"name"`   // the kind's name: the config's kinds.<name>
	Status   string   `json:"status"` // terminal | hooks | transcript (agents.Caps.Status)
	Caps     Caps     `json:"caps"`
	Efforts  []string `json:"efforts,omitempty"`
	Vars     []string `json:"vars,omitempty"` // every variable start's env may set (agents.Kind.Vars)
	Env      []string `json:"env,omitempty"`  // the variables obor snapshots into every request (HOME, the CLI's own)
	Stop     Stop     `json:"stop"`
	Headless Headless `json:"headless"`
	// Transcript is the glob of a session's transcript: {sid} the session id, ${VAR} a variable Env names; "": none.
	Transcript string `json:"transcript,omitempty"`
	// InboxRecord are the variables whose values (a JSON object, name → value) are a session's inbox record, the record
	// deliver gets back; none, or one of them empty: no record.
	InboxRecord []string  `json:"inbox_record,omitempty"`
	NotReady    *NotReady `json:"not_ready,omitempty"` // nil: a start is ready once the agent runs
	Typed       bool      `json:"typed,omitempty"`     // every line is typed into the pane: no deliver call
	MCPLane     MCPLane   `json:"mcp_lane"`
	// ProjectWarnings: the kind answers project-warnings (obor config validate's notes on the project's CLI files).
	ProjectWarnings bool `json:"project_warnings,omitempty"`
}

// Caps are agents.Caps as the manifest spells them; Status and Transcript come from the manifest's status and
// transcript.
type Caps struct {
	Hooks         string `json:"hooks,omitempty"` // none | native | plugin
	Inbox         bool   `json:"inbox,omitempty"`
	Resume        bool   `json:"resume,omitempty"`
	Skills        bool   `json:"skills,omitempty"`
	Sessions      bool   `json:"sessions,omitempty"`
	NamedSession  bool   `json:"named_session,omitempty"`
	ProjectPlugin bool   `json:"project_plugin,omitempty"`
	ModelArg      bool   `json:"model_arg,omitempty"`
	VersionVar    string `json:"version_var,omitempty"`
}

// Stop is agents.Stop; Dialog.Key a regular expression.
type Stop struct {
	Quit        string   `json:"quit,omitempty"`
	SecondEnter bool     `json:"second_enter,omitempty"`
	ClearInput  []string `json:"clear_input,omitempty"`
	AfterEscape []string `json:"after_escape,omitempty"`
	Program     string   `json:"program,omitempty"`
	Dialog      *struct {
		Option string `json:"option"`
		Key    string `json:"key,omitempty"`
	} `json:"dialog,omitempty"`
}

// Headless is agents.Headless.
type Headless struct {
	Programs    []string `json:"programs,omitempty"`
	Subcommands []string `json:"subcommands,omitempty"`
	Anywhere    []string `json:"anywhere,omitempty"`
	ValueOpts   []string `json:"value_opts,omitempty"`
	PrintFlag   bool     `json:"print_flag,omitempty"`
}

// NotReady is how a start's screen says the agent cannot take a prompt yet: Pattern (a regular expression; Why the
// reason when it matches) or Call (run not-ready per poll).
type NotReady struct {
	Pattern string `json:"pattern,omitempty"`
	Why     string `json:"why,omitempty"`
	Call    bool   `json:"call,omitempty"`
}

// MCPLane is closure 3 as data (agents.MCPRegistration.Lane): how a lane's start gets obor's MCP server ({kit} in how:
// the kit install names).
type MCPLane struct {
	How        string `json:"how"`
	Registered bool   `json:"registered,omitempty"`
	Static     bool   `json:"static,omitempty"`
}

// manifest is a checked Manifest with its regular expressions compiled.
type manifest struct {
	Manifest
	notReady, dialogKey *regexp.Regexp
}

var hookTransports = map[string]agents.HookTransport{"": agents.HooksNone, "none": agents.HooksNone,
	"native": agents.HooksNative, "plugin": agents.HooksPlugin}

// parse is describe's reply b as kind name's manifest; an error says what is wrong with it.
func parse(name string, b []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(b, &m.Manifest); err != nil {
		return nil, fmt.Errorf("malformed manifest: %v", err)
	}
	switch {
	case m.Version != Version:
		return nil, fmt.Errorf("manifest version %d, obor speaks %d", m.Version, Version)
	case m.Name != name:
		return nil, fmt.Errorf("manifest names kind %q, the config %q", m.Name, name)
	case !slices.Contains(agents.Statuses, agents.Status(m.Status)):
		return nil, fmt.Errorf("manifest status %q is none of terminal, hooks, transcript", m.Status)
	}
	if _, ok := hookTransports[m.Caps.Hooks]; !ok {
		return nil, fmt.Errorf("manifest caps.hooks %q is none of none, native, plugin", m.Caps.Hooks)
	}
	var err error
	if nr := m.NotReady; nr != nil {
		if nr.Call == (nr.Pattern != "") {
			return nil, errors.New("manifest not_ready names a pattern or call: true, one of them")
		}
		if nr.Pattern != "" {
			if m.notReady, err = regexp.Compile(nr.Pattern); err != nil {
				return nil, fmt.Errorf("manifest not_ready.pattern: %v", err)
			}
		}
	}
	if d := m.Stop.Dialog; d != nil && d.Key != "" {
		if m.dialogKey, err = regexp.Compile(d.Key); err != nil {
			return nil, fmt.Errorf("manifest stop.dialog.key: %v", err)
		}
	}
	return &m, nil
}

// cache is the manifest file obor setup agents writes: the manifest and the command it describes, as it was then.
type cache struct {
	Command  string          `json:"command"`
	Mtime    time.Time       `json:"mtime"`
	Manifest json.RawMessage `json:"manifest"`
}

// CacheFile is kind name's cached manifest in stateDir.
func CacheFile(stateDir, name string) string { return filepath.Join(stateDir, "kinds", name+".json") }

// Kind is a plugin kind: agents.Kind (and agents.Whole, agents.Fallible, agents.ArgsChecker, agents.ProjectChecker) by
// running its command.
type Kind struct {
	name, command, stateDir string
	local                   Handler // non-nil: the kind answers in process (Local), no command runs

	mu      sync.Mutex
	m       *manifest // nil: no usable cache (missing says why)
	missing error
	cmdAt   string    // the command the manifest describes
	mtimeAt time.Time // and its mtime then
}

// New is kind name as command, its manifest read from stateDir's cache (no process runs). A missing or unreadable
// cache leaves it without a manifest: every call that runs the command fails, naming obor setup agents.
func New(name, command, stateDir string) *Kind {
	k := &Kind{name: name, command: command, stateDir: stateDir}
	file := CacheFile(stateDir, name)
	b, err := os.ReadFile(file)
	var c cache
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err == nil {
		k.m, err = parse(name, c.Manifest)
	}
	if err != nil {
		k.missing = fmt.Errorf("kind %s: no manifest cached (%s: %v): run obor setup agents", name, file, err)
		return k
	}
	k.cmdAt, k.mtimeAt = c.Command, c.Mtime
	return k
}

// Cache runs kind name's describe, writes its manifest to stateDir's cache and returns the kind (obor setup agents,
// obor init).
func Cache(name, command, stateDir string) (*Kind, error) {
	k := &Kind{name: name, command: command, stateDir: stateDir, local: Local[name]}
	if err := k.describe(); err != nil {
		return nil, err
	}
	return k, nil
}

// Ensure is Cache when kind name's cache in stateDir does not describe command as it is now (none, unreadable, another
// command, another mtime: obor serve, T-1054); wrote says it described. A kind answered in process (Local) caches
// nothing: it needs no cache.
func Ensure(name, command, stateDir string) (wrote bool, err error) {
	if Local[name] != nil {
		return false, nil
	}
	if b, err := os.ReadFile(CacheFile(stateDir, name)); err == nil {
		var c cache
		fi, serr := os.Stat(command)
		if json.Unmarshal(b, &c) == nil && serr == nil && c.Command == command && c.Mtime.Equal(fi.ModTime()) {
			if _, err := parse(name, c.Manifest); err == nil {
				return false, nil
			}
		}
	}
	if _, err := Cache(name, command, stateDir); err != nil {
		return false, err
	}
	return true, nil
}

// describe runs describe and makes its manifest k's and the cache's. Called with k.mu held, or before k is shared.
func (k *Kind) describe() error {
	var mtime time.Time // a Local kind's: none
	if k.local == nil {
		fi, err := executable(k.command)
		if err != nil {
			return fmt.Errorf("kind %s: describe: %v", k.name, err)
		}
		mtime = fi.ModTime()
	}
	var raw json.RawMessage
	if err := k.run("describe", nil, &raw); err != nil {
		return err
	}
	m, err := parse(k.name, raw)
	if err != nil {
		return fmt.Errorf("kind %s: describe: %v", k.name, err)
	}
	b, _ := json.MarshalIndent(cache{Command: k.command, Mtime: mtime, Manifest: raw}, "", "  ")
	file := CacheFile(k.stateDir, k.name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("kind %s: describe: %v", k.name, err)
	}
	if err := project.WriteAtomic(file, append(b, '\n')); err != nil {
		return fmt.Errorf("kind %s: describe: %v", k.name, err)
	}
	k.m, k.missing, k.cmdAt, k.mtimeAt = m, nil, k.command, mtime
	return nil
}

// executable is command's file info; an error when it is no executable file.
func executable(command string) (os.FileInfo, error) {
	fi, err := os.Stat(command)
	switch {
	case err != nil:
		return nil, err
	case !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0:
		return nil, fmt.Errorf("%s is not an executable file", command)
	}
	return fi, nil
}

// manifest is k's manifest for a call that runs the command: described again first when the command changed since the
// cache (another path, another mtime).
func (k *Kind) manifest() (*manifest, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil || k.local != nil {
		return k.m, k.missing
	}
	if fi, err := os.Stat(k.command); err != nil || k.cmdAt != k.command || !fi.ModTime().Equal(k.mtimeAt) {
		if err := k.describe(); err != nil {
			return nil, err
		}
	}
	return k.m, nil
}

// cached is k's manifest as loaded, for the methods that run nothing (nil: none).
func (k *Kind) cached() *manifest {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m
}

// Handler is a kind answering in process: call's reply to the request req, as its binary would print it (an error: the
// binary's non-zero exit).
type Handler func(call string, req []byte) ([]byte, error)

// Local are the kinds this binary answers in process, by name, in place of their command: a test binary has no built
// kind binary, so testkit puts OpenCode's own handler here (kinds/opencode/kind.Handle). Empty in obor.
var Local = map[string]Handler{}

// local is kind name answered by h: its manifest from h's describe, nothing cached; a describe that fails leaves it
// without a manifest (missing says why).
func local(name, stateDir string, h Handler) *Kind {
	k := &Kind{name: name, stateDir: stateDir, local: h}
	var raw json.RawMessage
	err := k.run("describe", nil, &raw)
	if err == nil {
		k.m, err = parse(name, raw)
	}
	k.missing = err
	return k
}

// loader is the binary's agents.PluginLoader.
type loader struct{}

func (loader) Kind(name, command, stateDir string) agents.Kind {
	if h := Local[name]; h != nil {
		return local(name, stateDir, h)
	}
	return New(name, command, stateDir)
}

// Check: command is executable and its describe answers with a manifest of kind name; the kind with that manifest
// (nothing is cached: the cache stays setup's).
func (loader) Check(name, command string) (agents.Kind, error) {
	if h := Local[name]; h != nil {
		k := local(name, "", h)
		return k, k.missing
	}
	fi, err := executable(command)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	k := &Kind{name: name, command: command}
	if err := k.run("describe", nil, &raw); err != nil {
		return nil, err
	}
	m, err := parse(name, raw)
	if err != nil {
		return nil, fmt.Errorf("kind %s: describe: %v", name, err)
	}
	k.m, k.cmdAt, k.mtimeAt = m, command, fi.ModTime()
	return k, nil
}

func (k *Kind) Name() string { return k.name }

// Uncached (agents.Uncached) is why k has no manifest (nil: it has one).
func (k *Kind) Uncached() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m != nil {
		return nil
	}
	return k.missing
}

// Caps are the manifest's: Status its status, Transcript whether it names a transcript glob.
func (k *Kind) Caps() agents.Caps {
	m := k.cached()
	if m == nil {
		return agents.Caps{}
	}
	c := m.Caps
	return agents.Caps{Hooks: hookTransports[c.Hooks], Inbox: c.Inbox, Resume: c.Resume, Skills: c.Skills,
		Transcript: m.Transcript != "", Status: agents.Status(m.Status), Sessions: c.Sessions,
		NamedSession: c.NamedSession, ProjectPlugin: c.ProjectPlugin, ModelArg: c.ModelArg, VersionVar: c.VersionVar}
}

func (k *Kind) Efforts() []string {
	if m := k.cached(); m != nil {
		return m.Efforts
	}
	return nil
}

func (k *Kind) Vars() []string {
	if m := k.cached(); m != nil {
		return m.Vars
	}
	return nil
}

func (k *Kind) Stop() agents.Stop {
	m := k.cached()
	if m == nil {
		return agents.Stop{}
	}
	s := m.Stop
	out := agents.Stop{Quit: s.Quit, SecondEnter: s.SecondEnter, ClearInput: s.ClearInput, AfterEscape: s.AfterEscape,
		Program: s.Program}
	if s.Dialog != nil {
		out.Dialog = &agents.Dialog{Option: s.Dialog.Option, Key: m.dialogKey}
	}
	return out
}

func (k *Kind) Headless() agents.Headless {
	m := k.cached()
	if m == nil {
		return agents.Headless{}
	}
	h := m.Headless
	return agents.Headless{Programs: h.Programs, Subcommands: h.Subcommands, Anywhere: h.Anywhere,
		ValueOpts: h.ValueOpts, PrintFlag: h.PrintFlag}
}

// Transcript is the manifest's glob with sid and the env's values in; "" when it names none or a variable it names is
// empty (or not among the manifest's env).
func (k *Kind) Transcript(env func(string) string, sid string) string {
	m := k.cached()
	if m == nil || m.Transcript == "" {
		return ""
	}
	empty := false
	out := os.Expand(strings.ReplaceAll(m.Transcript, "{sid}", sid), func(v string) string {
		val := ""
		if slices.Contains(m.Env, v) && env != nil {
			val = env(v)
		}
		empty = empty || val == ""
		return val
	})
	if empty {
		return ""
	}
	return out
}

// InboxRecord is the manifest's inbox_record variables and their values in env, as a JSON object; nil: it names none,
// or one is empty.
func (k *Kind) InboxRecord(env func(string) string) []byte {
	m := k.cached()
	if m == nil || len(m.InboxRecord) == 0 || env == nil {
		return nil
	}
	rec := map[string]string{}
	for _, v := range m.InboxRecord {
		if rec[v] = env(v); rec[v] == "" {
			return nil
		}
	}
	b, _ := json.Marshal(rec)
	return b
}
