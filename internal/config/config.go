// Package config loads a project's effective config: .obor/local.json merged into .obor/config.json as JSON (objects
// merge key by key; null, lists and scalars are replaced: jq's *), decoded over the defaults, then the derived keys
// (lanes_dir, merge.scratch_dir, claude settings).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"obor/internal/agents"
	"obor/internal/roles"
)

// AgentNameRE is what a herdr agent name obor starts must match.
var AgentNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ErrNoConfig: the project has no .obor/config.json (exit 3; hooks no-op).
var ErrNoConfig = errors.New("no config")

// Data is the config file's shape, and its field comments are the reference for each key. A key it does not name
// fails the load.
type Data struct {
	Version          *int                `json:"version,omitempty"`
	Obor             any                 `json:"obor,omitempty"` // removed in v0.17.0 (T-597): validate warns
	Name             string              `json:"name,omitempty"`
	Prefix           string              `json:"prefix,omitempty"`
	LanesDir         string              `json:"lanes_dir"`
	Branches         Branches            `json:"branches"`
	TaskIDPattern    string              `json:"task_id_pattern"`
	Paths            map[string]string   `json:"paths"`
	Spec             Spec                `json:"spec,omitzero"`
	DoneFile         any                 `json:"done_file,omitempty"` // removed in v0.12.0 (T-470: task_done): validate warns
	DoneFileSections []string            `json:"done_file_sections"`
	Commands         Ordered[Command]    `json:"commands"`
	LaneLinks        []string            `json:"lane_links"`
	Guards           *Guards             `json:"guards,omitempty"`
	ProtectedFiles   []string            `json:"protected_files"`
	LaneTypes        Ordered[LaneType]   `json:"lane_types"`
	Lanes            Ordered[LaneEntry]  `json:"lanes"`
	StopIdleWorkers  *bool               `json:"stop_idle_workers,omitempty"`
	Agents           Ordered[AgentEntry] `json:"agents,omitzero"`
	Roles            Roles               `json:"roles"`
	Setup            Setup               `json:"setup,omitzero"`
	Leads            any                 `json:"leads,omitempty"` // removed in v0.12.0 (T-470: leads.cross_ttl had no reader): validate warns
	Merge            Merge               `json:"merge"`
	MCP              MCP                 `json:"mcp"`
	Slots            *Slots              `json:"slots,omitempty"`
	StopHook         any                 `json:"stop_hook,omitempty"` // removed in v0.12.0 (T-465): validate warns
	Limits           *Limits             `json:"limits,omitempty"`
	Heartbeat        any                 `json:"heartbeat,omitempty"` // removed in v0.12.0 (T-465): validate warns
	Cache            Cache               `json:"cache"`
	Inbox            any                 `json:"inbox,omitempty"`  // removed in v0.12.0 (T-466): validate warns
	Notify           any                 `json:"notify,omitempty"` // removed in v0.12.0 (T-465): validate warns
	// Difficulty maps a level (hard, medium, simple) to its lane types in dispatch order; a level it omits has the built-in
	// default (DifficultyDefault).
	Difficulty map[string][]string `json:"difficulty,omitempty"`
}

// Spec holds only removed keys (T-496: no "Files you may touch" enforcement; placement is checked at accept).
type Spec struct {
	FilesHeading any `json:"files_heading,omitempty"` // removed in v0.13.0 (T-496): validate warns
	EnforceFiles any `json:"enforce_files,omitempty"` // removed in v0.13.0 (T-496): validate warns
}
type Guards struct {
	AllowPush []string `json:"allow_push,omitzero"`
}
type Limits struct {
	MaxPanesPerTab *int `json:"max_panes_per_tab,omitempty"`
}

type Branches struct {
	Main string `json:"main"`
	Task string `json:"task"`
}

// Command is commands.<name>: a string is {cmd}, null or absent is unset. Name is the key.
type Command struct {
	Name    string   `json:"-"`
	Cmd     string   `json:"cmd"`
	Timeout int      `json:"timeout,omitempty"`
	Heavy   bool     `json:"heavy,omitempty"` // takes a machine-wide check slot (obor run)
	Dir     string   `json:"dir,omitempty"`
	Paths   []string `json:"paths,omitzero"`
	When    []string `json:"when,omitzero"`
	At      string   `json:"at,omitempty"`
	// RestartServe (after_merge's, T-652): once the command ran green, serve restarts itself on the new build — it
	// exits gracefully and launchd's KeepAlive starts it again (T-656). Default false: a project on the installed
	// binary has nothing to pick up.
	RestartServe bool `json:"restart_serve,omitempty"`
}

type command Command // Command without its JSON methods

func (c *Command) UnmarshalJSON(b []byte) error {
	switch b[0] {
	case 'n':
		*c = Command{}
		return nil
	case '"':
		*c = Command{}
		return json.Unmarshal(b, &c.Cmd)
	}
	return strict(b, (*command)(c))
}

// MarshalJSON writes a command that is only a cmd as its string (null when unset).
func (c Command) MarshalJSON() ([]byte, error) {
	if Compact(command(c)) == Compact(command{Cmd: c.Cmd}) {
		if c.Cmd == "" {
			return []byte("null"), nil
		}
		return []byte(Compact(c.Cmd)), nil
	}
	return []byte(Compact(command(c))), nil
}

// LaneType is lane_types.<name>: how to start an agent of this type. Name is the key.
type LaneType struct {
	Name        string         `json:"-"`
	Kind        string         `json:"kind"`
	Args        []string       `json:"args,omitzero"`
	Max         int            `json:"max,omitempty"`
	PromptExtra string         `json:"prompt_extra,omitempty"`
	Jobs        any            `json:"jobs,omitempty"`     // removed in v0.11.0 (T-437): every kind fetches its jobs; validate warns
	Settings    map[string]any `json:"settings,omitempty"` // the kind's settings (Claude: --settings)
}

// Model is the model this type's args name: Claude's --model M, Codex's -m M (also as --model=M or -m=M); "" when the
// args name none (obor setup show must not guess one).
func (lt LaneType) Model() string {
	for i := 0; i < len(lt.Args); i++ {
		switch a := lt.Args[i]; {
		case a == "--model" || a == "-m":
			if i+1 < len(lt.Args) {
				return lt.Args[i+1]
			}
		case strings.HasPrefix(a, "--model="):
			return strings.TrimPrefix(a, "--model=")
		case strings.HasPrefix(a, "-m="):
			return strings.TrimPrefix(a, "-m=")
		}
	}
	return ""
}

// Effort is the effort this type's args set: Claude's --effort E, Codex's -c model_reasoning_effort=E (also as
// --effort=E); "" when the args set none.
func (lt LaneType) Effort() string {
	for i := 0; i < len(lt.Args); i++ {
		a := lt.Args[i]
		switch {
		case a == "--effort" && i+1 < len(lt.Args):
			return lt.Args[i+1]
		case strings.HasPrefix(a, "--effort="):
			return strings.TrimPrefix(a, "--effort=")
		case a == "-c" && i+1 < len(lt.Args):
			if e, ok := strings.CutPrefix(lt.Args[i+1], "model_reasoning_effort="); ok {
				return e
			}
		case strings.HasPrefix(a, "-c") && strings.Contains(a, "model_reasoning_effort="):
			_, e, _ := strings.Cut(a, "model_reasoning_effort=")
			return e
		}
	}
	return ""
}

// LaneEntry is lanes.<name>: a lane type, or {type, agent}.
type LaneEntry struct {
	Type  string `json:"type"`
	Agent string `json:"agent,omitempty"`
}

type laneEntry LaneEntry

func (l *LaneEntry) UnmarshalJSON(b []byte) error {
	if b[0] == '"' {
		*l = LaneEntry{}
		return json.Unmarshal(b, &l.Type)
	}
	return strict(b, (*laneEntry)(l))
}

func (l LaneEntry) MarshalJSON() ([]byte, error) {
	if l.Agent == "" {
		return []byte(Compact(l.Type)), nil
	}
	return []byte(Compact(laneEntry(l))), nil
}

// AgentEntry is agents.<name>: an agent that is no lane or role (T-145).
type AgentEntry struct {
	Type string `json:"type"`
	Cwd  string `json:"cwd,omitempty"`
	Role string `json:"role,omitempty"`
}

// Roles is roles: one entry per table role (nil: not configured), and the leads.
type Roles struct {
	Lead       *RoleEntry `json:"lead,omitempty"`
	Integrator *RoleEntry `json:"integrator,omitempty"`
	Worker     *RoleEntry `json:"worker,omitempty"`
	Reviewer   *RoleEntry `json:"reviewer,omitempty"`
	DeepDive   *RoleEntry `json:"deep_dive,omitempty"`
	Triage     *RoleEntry `json:"triage,omitempty"`
	Advisor    *RoleEntry `json:"advisor,omitempty"`
	Leads      any        `json:"leads,omitempty"` // removed in v0.12.0 (T-467): validate warns
}

type RoleEntry struct {
	Type          string  `json:"type,omitempty"`
	Agent         string  `json:"agent,omitempty"`
	Worktree      *string `json:"worktree,omitempty"`
	HandoffTokens *int64  `json:"handoff_tokens,omitempty"`
}

// Setup is `setup`: what obor init records about the project's models, effort and lanes (T-647). `obor setup show`
// prints that table; the lead goes through it with the owner at its first session.
type Setup struct {
	// Confirmed is the owner's answer to the setup table: false or absent means the lead asks at its first session.
	Confirmed *bool `json:"confirmed,omitempty"`
}

// SetupConfirmed is the owner's answer to the setup table (setup.confirmed): absent is not confirmed (T-647).
func (c *Config) SetupConfirmed() bool {
	return c.Data.Setup.Confirmed != nil && *c.Data.Setup.Confirmed
}

// entry is roles.<name>; nil when not configured.
func (r *Roles) entry(name string) *RoleEntry {
	return map[string]*RoleEntry{"lead": r.Lead, "integrator": r.Integrator, "worker": r.Worker, "reviewer": r.Reviewer,
		"deep_dive": r.DeepDive, "triage": r.Triage, "advisor": r.Advisor}[name]
}

// MCP is the mcp section: jobs was the switch of obor serve's jobs by fetch (app-mcp), always on since T-437.
type MCP struct {
	Jobs any `json:"jobs,omitempty"` // removed in v0.11.0 (T-437): validate warns unless true
}

type Merge struct {
	ScratchDir   string           `json:"scratch_dir"`
	Trailer      string           `json:"trailer,omitempty"`
	SummaryRegex string           `json:"summary_regex,omitempty"`
	FailGrep     string           `json:"fail_grep,omitempty"`
	TimeoutGrep  string           `json:"timeout_grep,omitempty"`
	MaxLoad      *float64         `json:"max_load,omitempty"`
	LoadWait     *int             `json:"load_wait,omitempty"`
	WindowWait   *int             `json:"window_wait,omitempty"`
	Classes      List[MergeClass] `json:"classes,omitzero"`
	Neutral      []string         `json:"neutral,omitzero"`
	// AffectedFull are globs of files whose change makes `obor affected go` answer "full" (build and test support).
	AffectedFull []string `json:"affected_full,omitzero"`
	Guard        string   `json:"guard,omitempty"`
	FullEvery    any      `json:"full_every,omitempty"` // removed in v0.12.0 (T-465): validate warns
	// NightlyAt is when obor serve runs the nightly full check ("HH:MM" local time; "": never, T-464).
	NightlyAt string `json:"nightly_at,omitempty"`
	// ReceiptMaxAge is how old (s) a worker's green-run receipt may be for the gate to reuse it (default 86400; 0: never).
	ReceiptMaxAge int `json:"receipt_max_age"`
}

// MergeClass is one merge.classes entry.
type MergeClass struct {
	Name   string   `json:"name"`
	Paths  []string `json:"paths"`
	Checks []string `json:"checks,omitzero"`
	Full   bool     `json:"full,omitempty"`
}

type Slots struct {
	Dir           string `json:"dir,omitempty"`
	Max           *int   `json:"max,omitempty"`
	MergeBlockMax *int   `json:"merge_block_max,omitempty"`
	CPUs          *int   `json:"cpus,omitempty"` // CHECK_CPUS: the CPUs one check may use (default cores / max, at least 1)
}

// Cache holds only a removed key (prompt-cache awareness went with obor status --herdr, T-463).
type Cache struct {
	WarnTokens any `json:"warn_tokens,omitempty"` // removed in v0.12.0 (T-463): validate warns
}

// Defaults is a fresh copy each call: decoding reuses a slice's backing array. A null in the files leaves a default.
func Defaults() Data {
	return Data{
		Branches:         Branches{Main: "main", Task: "task/{id}"},
		TaskIDPattern:    "T-[0-9]+[a-z]?",
		Paths:            map[string]string{"agents": "docs/agents"},
		DoneFileSections: []string{"Summary", "Self-check", "Skipped", "Open questions"},
		LaneLinks:        []string{},
		ProtectedFiles:   []string{},
		Merge:            Merge{ReceiptMaxAge: 86400},
	}
}

// Config is the effective config of the project at Root.
type Config struct {
	Root       string
	Data       Data
	json       []byte // Data as JSON, for Get
	unknownErr error  // Load's error when read dropped an unknown key
}

// Load builds the effective config of the project whose main checkout is root (absolute, physical): config.json and
// local.json merged as JSON (merge), decoded once over the defaults, then the derived keys. An unknown key is an error.
func Load(root string) (*Config, error) {
	c, unknown, err := read(root)
	if err == nil && len(unknown) > 0 {
		return nil, c.unknownErr
	}
	return c, err
}

// LoadDaemon is Load for the long-running process (obor serve): a key this binary does not know
// (the config is newer than the binary) is ignored with one warning per key and process on Warn, not an error that
// stops the loop (T-377). Every other error is Load's.
func LoadDaemon(root string) (*Config, error) {
	c, unknown, err := read(root)
	for _, key := range unknown {
		warnUnknown(root, key)
	}
	return c, err
}

// LoadHook is Load for the hooks (DD-v0.13 A1): a key this binary does not know is ignored, as LoadDaemon does, but
// returned in unknown instead of warned of on Warn (the SessionStart hook names them; the others say nothing). Every
// other error is Load's: the hooks then fail closed (Fallback).
func LoadHook(root string) (*Config, []string, error) { return read(root) }

// Warn receives LoadDaemon's warnings.
var Warn io.Writer = os.Stderr

var warned sync.Map // "<file>: <key path>" already warned of in this process

// unknownKey is Load's error for an unknown key (a dotted path), named as encoding/json names an unknown field.
type unknownKey struct{ file, key string }

func (u unknownKey) Error() string {
	last := u.key[strings.LastIndex(u.key, ".")+1:]
	if last == u.key {
		return fmt.Sprintf("obor: %s: unknown field %q", u.file, u.key)
	}
	return fmt.Sprintf("obor: %s: unknown field %q (%s)", u.file, last, u.key)
}

// read reads and merges the files once and decodes them over the defaults, the keys this binary does not know dropped
// and returned in unknown (dotted paths, file order). err is every other failure (with no config and no unknown).
func read(root string) (*Config, []string, error) {
	var files [2][]byte
	for i, name := range []string{"config.json", "local.json"} {
		path := filepath.Join(root, ".obor", name)
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist) && i == 0:
			return nil, nil, fmt.Errorf("obor: no %s: %w", path, ErrNoConfig)
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("obor: cannot read %s", path)
		case !json.Valid(b):
			return nil, nil, fmt.Errorf("obor: %s is not valid JSON", path)
		}
		if _, ok := object(b); !ok {
			return nil, nil, fmt.Errorf("obor: %s is not a JSON object", path)
		}
		files[i] = b
	}
	merged := files[0]
	if files[1] != nil {
		merged = merge(files[0], files[1])
	}
	c := &Config{Root: root, Data: Defaults()}
	merged, unknown := dropUnknown(merged, reflect.TypeOf(c.Data), "")
	if err := strict(merged, &c.Data); err != nil {
		// Name the file at fault: config.json when it fails alone, else local.json.
		name := "local.json"
		if b, _ := dropUnknown(files[0], reflect.TypeOf(c.Data), ""); strict(b, &Data{}) != nil {
			name = "config.json"
		}
		return nil, nil, fmt.Errorf("obor: %s: %s", filepath.Join(root, ".obor", name), strings.TrimPrefix(err.Error(), "json: "))
	}
	c.Data.derive(filepath.Base(root))
	b, err := json.Marshal(c.Data)
	if err != nil {
		return nil, nil, fmt.Errorf("obor: cannot build the effective config: %w", err)
	}
	c.json = b
	if len(unknown) > 0 { // name config.json when it has one, else local.json
		c.unknownErr = unknownKey{filepath.Join(root, ".obor", "local.json"), unknown[0]}
		if _, in := dropUnknown(files[0], reflect.TypeOf(c.Data), ""); len(in) > 0 {
			c.unknownErr = unknownKey{filepath.Join(root, ".obor", "config.json"), in[0]}
		}
	}
	return c, unknown, nil
}

// warnUnknown tells once per process that key (a dotted path) is ignored.
func warnUnknown(root, key string) {
	if _, dup := warned.LoadOrStore(root+": "+key, true); dup {
		return
	}
	fmt.Fprintf(Warn, "obor: %s: unknown key %q ignored: this binary is older than the config: restart serve\n",
		filepath.Join(root, ".obor"), key)
}

// derive fills what the files leave unset: lanes_dir, merge.scratch_dir.
func (d *Data) derive(base string) {
	if d.LanesDir == "" {
		d.LanesDir = "../" + base + "-wt"
	}
	if d.Merge.ScratchDir == "" {
		d.Merge.ScratchDir = d.LanesDir + "/merge"
	}
}

// Get is the JSON value at path in the effective config: "." is all of it, ".a.b" a key, ".a.0" or ".a[0]" a list
// item, `.a["k"]` a key with any characters but dots. ok false when there is no such key.
func (c *Config) Get(path string) (json.RawMessage, bool) {
	cur := json.RawMessage(c.json)
	path = strings.NewReplacer(`["`, ".", `"]`, "", "[", ".", "]", "").Replace(path)
	for _, k := range strings.Split(path, ".") {
		if k == "" {
			continue
		}
		var obj map[string]json.RawMessage
		var list []json.RawMessage
		i, numErr := strconv.Atoi(k)
		switch {
		case json.Unmarshal(cur, &obj) == nil && obj != nil:
			v, ok := obj[k]
			if !ok {
				return nil, false
			}
			cur = v
		case numErr == nil && json.Unmarshal(cur, &list) == nil && i >= 0 && i < len(list):
			cur = list[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Lookup is Get, then for a key the effective config omits (an unset field with no default) the field's zero value:
// ok false only for a path that is neither set nor a known field. It walks the schema as dropUnknown does: a struct
// field is known whether set or not, but a member of a map, lane type, command etc. and a list index must exist.
func (c *Config) Lookup(path string) (json.RawMessage, bool) {
	if v, ok := c.Get(path); ok {
		return v, true
	}
	t := reflect.TypeOf(c.Data)
	cur := json.RawMessage(c.json)
	path = strings.NewReplacer(`["`, ".", `"]`, "", "[", ".", "]", "").Replace(path)
	for _, k := range strings.Split(path, ".") {
		if k == "" {
			continue
		}
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		member := func() (json.RawMessage, bool) {
			var obj map[string]json.RawMessage
			if json.Unmarshal(cur, &obj) != nil {
				return nil, false
			}
			v, ok := obj[k]
			return v, ok
		}
		if vt, ok := reflect.Zero(t).Interface().(valueTyper); ok {
			v, found := member()
			if !found {
				return nil, false
			}
			t, cur = vt.valueType(), v
			continue
		}
		switch t.Kind() {
		case reflect.Struct:
			f, ok := fieldOf(t, k)
			if !ok {
				return nil, false
			}
			if v, ok := shorthandField(cur, t, f); ok {
				t, cur = f.Type, v
				continue
			}
			t = f.Type
			cur, _ = member()
		case reflect.Map:
			v, found := member()
			if !found {
				return nil, false
			}
			t, cur = t.Elem(), v
		case reflect.Slice:
			var list []json.RawMessage
			i, err := strconv.Atoi(k)
			if err != nil || json.Unmarshal(cur, &list) != nil || i < 0 || i >= len(list) {
				return nil, false
			}
			t, cur = t.Elem(), list[i]
		default:
			return nil, false
		}
	}
	if cur != nil {
		return cur, true
	}
	b, err := json.Marshal(reflect.Zero(t).Interface())
	return b, err == nil
}

// shorthandField is the value of t's field key in cur when cur is a shorthand (a command or lane given as a string,
// which the effective JSON keeps): cur decoded into t, then that field. ok false when cur is no shorthand.
func shorthandField(cur json.RawMessage, t reflect.Type, f reflect.StructField) (json.RawMessage, bool) {
	if len(cur) == 0 || cur[0] == '{' || string(cur) == "null" {
		return nil, false
	}
	v := reflect.New(t)
	if strict(cur, v.Interface()) != nil {
		return nil, false
	}
	b, err := json.Marshal(v.Elem().FieldByIndex(f.Index).Interface())
	return b, err == nil
}

// Raw is `obor config get <path>`'s output: a string as is, anything else as indented JSON; ok false when absent or null.
func (c *Config) Raw(path string) (string, bool) {
	v, ok := c.Lookup(path)
	if !ok || string(v) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, true
	}
	return strings.TrimSuffix(Pretty(v), "\n"), true
}

// LanesDir is the absolute, lexically normalised lanes dir.
func (c *Config) LanesDir() string { return AbsUnder(c.Root, c.Data.LanesDir) }

// AbsUnder is p if absolute, else root/p, lexically normalised (obor_path_norm).
func AbsUnder(root, p string) string {
	if !strings.HasPrefix(p, "/") {
		p = root + "/" + p
	}
	return filepath.Clean(p)
}

// Lane is one configured lane.
type Lane struct {
	Name, Type, Dir string
}

// Paths are the named project files (paths.<name>); Reviews is paths.reviews.
type Paths struct {
	Reviews string
	Values  map[string]string
}

func (p Paths) Get(name string) string { return p.Values[name] }

// RoleConfig combines the shared role defaults with effective config overrides.
type RoleConfig struct {
	Role        roles.Role
	Agent, Type string
	Effort      string // the effort roles.<role>.type names after the type (`opus:high`, T-695); "": the type's args
	Worktree    string
	Configured  bool
}

// AgentInfo is the role, lane, launch type, and cwd resolved for a configured agent, and the effort its role's type
// reference names ("": the type's args; T-695).
type AgentInfo struct {
	Role, Lane, Type, Cwd string
	Effort                string
}

// Lanes is the configured lanes in config order.
func (c *Config) Lanes() []Lane {
	dir := c.LanesDir()
	out := make([]Lane, 0, len(c.Data.Lanes.Keys))
	for _, k := range c.Data.Lanes.Keys {
		out = append(out, Lane{Name: k, Type: c.Data.Lanes.M[k].Type, Dir: dir + "/" + k})
	}
	return out
}

// HasLane reports whether name is a configured lane.
func (c *Config) HasLane(name string) bool {
	_, ok := c.Data.Lanes.M[name]
	return ok
}

// LaneType is lane_types.<name>.
func (c *Config) LaneType(name string) (LaneType, bool) {
	lt, ok := c.Data.LaneTypes.M[name]
	lt.Name = name
	return lt, ok
}

// LaunchArgs are the type's launch args: args (with effort: the kind sets it in place of the args' own, T-695), then
// the settings as its kind takes them (agents.Kind.Start: Claude's --settings <settings>; a kind obor has no bridge
// for: args alone). Every agent but a lead starts with
// {"remoteControlAtStartup":false,"autoMemoryEnabled":false} (only the lead on the phone, T-280, and only the lead
// keeps auto memory, T-471); the type's settings overlay those keys.
func (lt LaneType) LaunchArgs(lead bool, effort string) []string {
	settings := lt.Settings
	if !lead {
		settings = map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}
		for k, v := range lt.Settings {
			settings[k] = v
		}
	}
	k, ok := agents.Lookup(lt.Kind)
	if !ok {
		return append([]string(nil), lt.Args...)
	}
	return k.Start(agents.Start{Args: lt.Args, Settings: settings, Effort: effort})
}

// StartEnv is the environment a start of this type with effort needs beyond the pane's (agents.Kind.StartEnv:
// OpenCode's model and effort); read and env read its config. nil for a kind obor has no bridge for.
func (lt LaneType) StartEnv(effort string, read agents.ReadFile, env func(string) string) (map[string]string, error) {
	k, ok := agents.Lookup(lt.Kind)
	if !ok {
		return nil, nil
	}
	return k.StartEnv(agents.Start{Args: lt.Args, Effort: effort, Read: read, Env: env})
}

// ParseTypeRef splits a type reference `<type>[:<effort>]` (T-695: a spec's Type: line, dispatch --types, a difficulty
// level's list, roles.<role>.type): the lane type and the effort ("": the type's own args).
func ParseTypeRef(ref string) (typ, effort string) {
	typ, effort, _ = strings.Cut(ref, ":")
	return typ, effort
}

// CheckTypeRef is why ref names no lane type of this config or an effort its kind does not take (agents.CheckEffort);
// nil when it is fine.
func (c *Config) CheckTypeRef(ref string) error {
	typ, effort := ParseTypeRef(ref)
	lt, ok := c.LaneType(typ)
	if !ok {
		return fmt.Errorf("%s is not a lane type of this project (lane types: %s)", Compact(typ), strings.Join(c.LaneTypeNames(), ", "))
	}
	if _, has := strings.CutPrefix(ref, typ+":"); has && effort == "" {
		return fmt.Errorf("%s names no effort after the colon", Compact(ref))
	}
	if err := agents.CheckEffort(lt.Kind, effort); err != nil {
		return fmt.Errorf("%s: %v", ref, err)
	}
	return nil
}

// Command is commands.<name>; ok false when unset (absent, null or no cmd).
func (c *Config) Command(name string) (Command, bool) {
	cmd := c.Data.Commands.M[name]
	cmd.Name = name
	return cmd, cmd.Cmd != ""
}

// Paths returns the configured paths.
func (c *Config) Paths() Paths { return Paths{Reviews: c.Data.Paths["reviews"], Values: c.Data.Paths} }

// Role resolves a table role and its configured agent, type, and worktree overrides.
func (c *Config) Role(name string) (RoleConfig, bool) {
	role, ok := roles.Lookup(name)
	if !ok {
		return RoleConfig{}, false
	}
	rc := RoleConfig{Role: role, Worktree: role.Worktree}
	if role.Name != "worker" {
		rc.Agent = role.AgentName(c.Data.Prefix, "", "")
	}
	if e := c.Data.Roles.entry(name); e != nil {
		rc.Configured = true
		rc.Type, rc.Effort = ParseTypeRef(e.Type)
		if e.Agent != "" {
			rc.Agent = e.Agent
		}
		if e.Worktree != nil {
			rc.Worktree = *e.Worktree // "" too: no worktree
		}
	}
	return rc, true
}

// AgentName is the herdr agent name of role (of lane, for a worker): the configured agent, else the table default.
func (c *Config) AgentName(role, lane string) string {
	rc, ok := c.Role(role)
	if !ok {
		return ""
	}
	return rc.Role.AgentName(c.Data.Prefix, lane, rc.Agent)
}

// MonitorLabel is the label of the project's monitor pane, <prefix>-monitor: obor setup monitor creates it and the
// restore hook starts `obor monitor` in it again (T-658). It is no agent name: nothing dispatches to it.
func (c *Config) MonitorLabel() string { return c.Data.Prefix + "-monitor" }

// roleAgents is every table role but the worker, an optional one only when configured.
func (c *Config) roleAgents() []RoleConfig {
	var out []RoleConfig
	for _, role := range roles.All() {
		rc, _ := c.Role(role.Name)
		if role.Name != "worker" && (!role.Optional || rc.Configured) {
			out = append(out, rc)
		}
	}
	return out
}

// RoleWorktrees is the worktree name of every configured role: roles.<role>.worktree, else a table role's default
// (reviewer → review, deep_dive → deep-dive). A linked worktree of such a name under lanes_dir is no lane.
func (c *Config) RoleWorktrees() []string {
	var out []string
	for _, role := range roles.All() {
		if rc, _ := c.Role(role.Name); rc.Configured && rc.Worktree != "" {
			out = append(out, rc.Worktree)
		}
	}
	return out
}

// LaneAgentAliases returns the names that identify this lane's worker; stateAgent is the agent its lane row names
// ("" none: config reads no store; callers pass project.LaneAgent).
func (c *Config) LaneAgentAliases(lane, stateAgent string) []string {
	return roles.LaneAgentAliases(lane, c.Data.Prefix, stateAgent, c.Data.Lanes.M[lane].Agent)
}

// AgentLookup follows lane → lead → role → agents precedence, then resolves an unmatched cwd at to a lane. stateAgent
// is the agent a lane's row names (nil: none; project.AgentLookup passes project.LaneAgent).
func (c *Config) AgentLookup(agent, at string, stateAgent func(lane string) string) (AgentInfo, bool) {
	for _, lane := range c.Lanes() {
		state := ""
		if stateAgent != nil {
			state = stateAgent(lane.Name)
		}
		for _, name := range c.LaneAgentAliases(lane.Name, state) {
			if agent == name {
				return AgentInfo{Role: "worker", Lane: lane.Name, Type: lane.Type, Cwd: lane.Dir}, true
			}
		}
	}
	for _, rc := range c.roleAgents() {
		if rc.Agent != "" && rc.Agent == agent {
			cwd := c.Root
			if rc.Worktree != "" {
				cwd = filepath.Join(c.LanesDir(), rc.Worktree)
			}
			return AgentInfo{Role: rc.Role.Name, Type: rc.Type, Effort: rc.Effort, Cwd: cwd}, true
		}
	}
	if e, ok := c.Data.Agents.M[agent]; ok {
		cwd := e.Cwd
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(c.Root, cwd)
		}
		return AgentInfo{Role: e.Role, Type: e.Type, Cwd: filepath.Clean(cwd)}, true
	}
	for _, lane := range c.Lanes() {
		if at != "" && filepath.Clean(at) == filepath.Clean(lane.Dir) {
			return AgentInfo{Role: "worker", Lane: lane.Name, Type: lane.Type, Cwd: lane.Dir}, true
		}
	}
	return AgentInfo{}, false
}

// Unique is values without repeats and empty strings, in order.
func Unique(values ...string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// IntegratorHandoffTokens is roles.integrator.handoff_tokens (T-215): the integrator's automatic handoff threshold,
// default 150000; 0 = off.
func (c *Config) IntegratorHandoffTokens() int64 {
	if in := c.Data.Roles.Integrator; in != nil && in.HandoffTokens != nil {
		return *in.HandoffTokens
	}
	return 150000
}

// TaskIDOK: id matches task_id_pattern (Go RE2 syntax) as a whole; a pattern that does not compile matches nothing.
func (c *Config) TaskIDOK(id string) bool {
	re, err := regexp.Compile("^(?:" + c.Data.TaskIDPattern + ")$")
	return err == nil && !strings.Contains(id, "\n") && re.MatchString(id)
}

// TaskFromBranch is the task id in a branch named by the branches.task template ("" when none).
func (c *Config) TaskFromBranch(branch string) string {
	pre, suf, ok := strings.Cut(c.Data.Branches.Task, "{id}")
	if !ok || len(branch) < len(pre)+len(suf) || !strings.HasPrefix(branch, pre) || !strings.HasSuffix(branch, suf) {
		return ""
	}
	if id := branch[len(pre) : len(branch)-len(suf)]; c.TaskIDOK(id) {
		return id
	}
	return ""
}

// ParseClock reads a local time of day "HH:MM" (merge.nightly_at): its hour and minute; false when s is none.
func ParseClock(s string) (hour, minute int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// DifficultyDefault is the lane types of each difficulty level when .obor/config.json's `difficulty` does not name
// the level (a spec's `Difficulty:` line picks the level, dispatch the lane): hard wants opus at effort high
// (opus:high, a type reference: any opus lane, started at effort high; T-695), simple the cheap types. A project that lacks a type of a level skips it (DifficultyLevelTypes).
var DifficultyDefault = map[string][]string{
	"hard":   {"opus:high"},
	"medium": {"sonnet", "deepseek"},
	"simple": {"deepseek", "sonnet", "haiku"},
}

// DifficultyTypes is the lane types of level in dispatch order: the project's difficulty entry, else the built-in
// default, keeping only the types a configured lane has (a type without a lane would wait forever, T-629). A level
// left with none falls back to the project's other lane types, medium's built-in list before hard's (simple → sonnet
// before opus; DifficultyLevelTypes is the list before that fallback). ok is false for a level that is none of hard,
// medium, simple.
func (c *Config) DifficultyTypes(level string) (types []string, ok bool) {
	if _, ok := DifficultyDefault[level]; !ok {
		return nil, false
	}
	if types := c.DifficultyLevelTypes(level); len(types) > 0 {
		return types, true
	}
	for _, other := range []string{"medium", "hard"} {
		if other == level {
			continue
		}
		if types := c.laneTypes(DifficultyDefault[other]); len(types) > 0 {
			return types, true
		}
	}
	return nil, true
}

// DifficultyLevelTypes is level's listed types that a configured lane has, in their order: DifficultyTypes before its
// fallback (dispatch says when it used the fallback). nil for a name that is no level.
func (c *Config) DifficultyLevelTypes(level string) []string {
	return c.laneTypes(c.DifficultyList(level))
}

// DifficultyList is level's listed types before the lanes filter, in order: the project's difficulty entry, else the
// built-in default. nil for a name that is no level.
func (c *Config) DifficultyList(level string) []string {
	listed, ok := DifficultyDefault[level]
	if !ok {
		return nil
	}
	if own, set := c.Data.Difficulty[level]; set {
		listed = own
	}
	return listed
}

// laneTypes keeps the type references of listed whose type at least one configured lane has.
func (c *Config) laneTypes(listed []string) []string {
	var out []string
	for _, t := range listed {
		if typ, _ := ParseTypeRef(t); c.HasLaneOfType(typ) {
			out = append(out, t)
		}
	}
	return out
}

// LaneTypeNames is the configured lane_types names, in config order.
func (c *Config) LaneTypeNames() []string { return append([]string(nil), c.Data.LaneTypes.Keys...) }

// HasLaneOfType reports whether a configured lane is of type typ.
func (c *Config) HasLaneOfType(typ string) bool {
	for _, k := range c.Data.Lanes.Keys {
		if c.Data.Lanes.M[k].Type == typ {
			return true
		}
	}
	return false
}
