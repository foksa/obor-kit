// Package config loads a project's effective config: .obor/local.json merged into .obor/config.json as JSON (objects
// merge key by key; null, lists and scalars are replaced: jq's *), decoded over the defaults, then the derived keys
// (lanes_dir, the merge scratch, claude settings).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"obor/internal/roles"
)

// AgentNameRE is what a herdr agent name obor starts must match.
var AgentNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ErrNoConfig: the project has no .obor/config.json (exit 3; hooks no-op).
var ErrNoConfig = errors.New("no config")

// Data is the config file's shape, and its field comments are the reference for each key. A key it does not name
// fails the load.
type Data struct {
	Version        *int              `json:"version,omitempty"`
	Name           string            `json:"name,omitempty"`
	Prefix         string            `json:"prefix,omitempty"`
	LanesDir       string            `json:"lanes_dir"`
	Branches       Branches          `json:"branches"`
	TaskIDPattern  string            `json:"task_id_pattern"`
	Paths          map[string]string `json:"paths"`
	Commands       Ordered[Command]  `json:"commands"`
	LaneLinks      []string          `json:"lane_links"`
	Guards         *Guards           `json:"guards,omitempty"`
	ProtectedFiles []string          `json:"protected_files"`
	// Lanes is the project's lanes under lanes_dir, every one pooled (T-765): a count N (L-01 … L-NN, each fresh), or
	// a list [{"name": "L-01", "keep_agent": true}, …] (T-1031: a keep lane's agent stays between its tasks).
	Lanes LaneList `json:"lanes"`
	// Kinds is kinds.<kind>: args appended to every start of the kind, and its work jobs' prompt_extra (T-765).
	Kinds Ordered[KindEntry] `json:"kinds,omitzero"`
	// Models is models.<name>: a model the built-in table (BuiltinModels) lacks, or a built-in one overridden.
	Models Ordered[ModelEntry] `json:"models,omitzero"`
	// HandoffTokens is the lead's automatic handoff threshold (T-175); absent: the guard's default.
	HandoffTokens *int64 `json:"handoff_tokens,omitempty"`
	Roles         Roles  `json:"roles"`
	Setup         Setup  `json:"setup,omitzero"`
	Merge         Merge  `json:"merge"`
	// Workflow is workflow: the git workflow serve runs (T-1029); absent: scratch-ff.
	Workflow Workflow `json:"workflow,omitzero"`
	// Terminal is terminal: the terminal adapter obor drives its panes through (T-1044); absent: herdr.
	Terminal Terminal `json:"terminal,omitzero"`
	Slots    *Slots   `json:"slots,omitempty"`
	// Difficulty maps a level (hard, medium, simple) to its model refs in dispatch order; a level it omits has the
	// built-in default (DifficultyDefault).
	Difficulty map[string][]string `json:"difficulty,omitempty"`
}

type Guards struct {
	AllowPush    []string      `json:"allow_push,omitzero"`
	DenyCommands []DenyCommand `json:"deny_commands,omitzero"`
}

// DenyCommand is guards.deny_commands[i] (T-905): a command the agent guard denies for every role. Match is a regex
// on the resolved command line (the program by its basename, its words one space apart, quotes removed); Alone denies
// a matching command run alone (and so also two at once), without it only two matching commands run at once — a
// sibling background unit pair, xargs -P, parallel — are denied. Message is the deny line.
type DenyCommand struct {
	Match   string `json:"match"`
	Alone   bool   `json:"alone,omitempty"`
	Message string `json:"message"`
}

type Branches struct {
	Main string `json:"main"`
	Task string `json:"task"`
}

// Workflow is workflow (T-1029): Kind selects the git workflow's implementation at serve start (one of WorkflowKinds;
// "": DefaultWorkflow). Its target stays branches.main.
type Workflow struct {
	Kind string `json:"kind,omitempty"`
}

// The workflow kinds serve has an implementation of (internal/checks' ScratchFF: the merge slot, the lane test, the
// fast-forward publish).
const DefaultWorkflow = "scratch-ff"

var WorkflowKinds = []string{DefaultWorkflow}

// WorkflowKind is workflow.kind, else DefaultWorkflow.
func (c *Config) WorkflowKind() string {
	if c != nil && c.Data.Workflow.Kind != "" {
		return c.Data.Workflow.Kind
	}
	return DefaultWorkflow
}

// Terminal is terminal (T-1044): Kind selects the compiled-in terminal adapter (one of TerminalKinds; "":
// DefaultTerminal).
type Terminal struct {
	Kind string `json:"kind,omitempty"`
}

// The terminal kinds obor has an adapter for (internal/herdr).
const DefaultTerminal = "herdr"

var TerminalKinds = []string{DefaultTerminal}

// TerminalKind is terminal.kind, else DefaultTerminal.
func (c *Config) TerminalKind() string {
	if c != nil && c.Data.Terminal.Kind != "" {
		return c.Data.Terminal.Kind
	}
	return DefaultTerminal
}

// Command is commands.<name>: a string is {cmd}, null or absent is unset. Name is the key.
type Command struct {
	Name    string   `json:"-"`
	Cmd     string   `json:"cmd"`
	Timeout int      `json:"timeout,omitempty"`
	Heavy   bool     `json:"heavy,omitempty"` // takes a machine-wide check slot (obor run)
	When    []string `json:"when,omitzero"`
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

// Roles is roles: roles.<name> in config order (T-801), a table role's model ref or a custom role's row (RoleEntry; a
// null member is no member).
type Roles struct {
	Ordered[RoleEntry]
}

func (r *Roles) UnmarshalJSON(b []byte) error {
	*r = Roles{}
	if string(b) == "null" {
		return nil
	}
	ms, ok := object(b)
	if !ok {
		return errors.New("roles: must be an object")
	}
	var kept []member
	for _, m := range ms {
		if string(m.Val) != "null" {
			kept = append(kept, m)
		}
	}
	return r.Ordered.UnmarshalJSON(joinMembers(kept))
}

// entry is roles.<name>; ok false when not configured.
func (r *Roles) entry(name string) (RoleEntry, bool) {
	e, ok := r.M[name]
	return e, ok
}

// RoleEntry is roles.<name> (T-801): a model ref string ("": configured, no model named), or a custom role's object
// {"model": "<ref>", "write": ["<dir>/", "<file>"], "verbs": ["spec_set", …]} (Object). Write is nil when the entry
// names no write list (a string, or an object without write: a kit default row keeps its own). Verbs are the wire
// methods serve admits the role to (T-1039, roles.Verbs; a built-in role has none: validate fails them).
type RoleEntry struct {
	Model  string   `json:"model"`
	Write  []string `json:"write,omitzero"`
	Verbs  []string `json:"verbs,omitzero"`
	Object bool     `json:"-"`
}

func (e *RoleEntry) UnmarshalJSON(b []byte) error {
	*e = RoleEntry{}
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &e.Model)
	}
	var o struct {
		Model string    `json:"model"`
		Write *[]string `json:"write"`
		Verbs []string  `json:"verbs"`
	}
	if err := strict(b, &o); err != nil {
		return err
	}
	e.Model, e.Object, e.Verbs = o.Model, true, o.Verbs
	if o.Write != nil {
		e.Write = append([]string{}, *o.Write...)
	}
	return nil
}

func (e RoleEntry) MarshalJSON() ([]byte, error) {
	if !e.Object {
		return []byte(Compact(e.Model)), nil
	}
	type plain RoleEntry
	return []byte(Compact(plain(e))), nil
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

type Merge struct {
	SummaryRegex string `json:"summary_regex,omitempty"`
	FailGrep     string `json:"fail_grep,omitempty"`
	// FlakeRegex is the pair of regexps that read a red run's failing tests and packages from its fail_grep lines
	// (T-1047): the failed-test line (the name in its group test) and the failed-package line (the name in its group
	// package). Unset: no flake parsing, no error (a project whose checks print no such lines).
	FlakeRegex []string `json:"flake_regex,omitzero"`
	// RetestJoin is what commands.retest's {tests} joins the failing tests with (default "|", a regexp's
	// alternatives; T-1047).
	RetestJoin string           `json:"retest_join,omitempty"`
	Classes    List[MergeClass] `json:"classes,omitzero"`
	Neutral    []string         `json:"neutral,omitzero"`
	// AffectedFull are globs of files whose change makes a project's affected-packages list answer "full" (build and
	// test support; obor-kit's list is tools/affected, T-1040).
	AffectedFull []string `json:"affected_full,omitzero"`
	Guard        string   `json:"guard,omitempty"`
	// NightlyAt is when obor serve runs the nightly full check ("HH:MM" local time; "": never, T-464).
	NightlyAt string `json:"nightly_at,omitempty"`
	// NightlyCommands are commands.<name> keys `obor nightly` runs after its gating checks and the backup, in order,
	// each non-gating (T-817): one report line, never RED; a nonzero exit a WARN line. A name must be a defined
	// command and no gating one. Unset: none.
	NightlyCommands []string `json:"nightly_commands,omitzero"`
}

// DefaultRetestJoin is merge.retest_join's default: a regexp's alternatives separator, the form a retest's -run
// takes.
const DefaultRetestJoin = "|"

// RetestJoin is merge.retest_join, else DefaultRetestJoin.
func (c *Config) RetestJoin() string {
	if c != nil && c.Data.Merge.RetestJoin != "" {
		return c.Data.Merge.RetestJoin
	}
	return DefaultRetestJoin
}

// MergeClass is one merge.classes entry.
type MergeClass struct {
	Name   string   `json:"name"`
	Paths  []string `json:"paths"`
	Checks []string `json:"checks,omitzero"`
	Full   bool     `json:"full,omitempty"`
	// WithFull runs this class's checks after check on a full merge too (T-1007): a lane with a file only this class
	// covers, beside an unmatched one, still runs it. Meaningless with Full: obor config validate refuses the pair.
	WithFull bool `json:"with_full,omitempty"`
}

type Slots struct {
	Dir  string `json:"dir,omitempty"`
	Max  *int   `json:"max,omitempty"`
	CPUs *int   `json:"cpus,omitempty"` // CHECK_CPUS: the CPUs one check may use (default cores / max, at least 1)
	// LaneCPUs is a lane's CPU budget (default 2): every process in a lane gets it as OBOR_LANE_CPUS, and it is what
	// LaneEnv's {cpus} placeholder stands for (T-1041).
	LaneCPUs *int `json:"lane_cpus,omitempty"`
	// LaneEnv is the env every process in a lane gets beside the generic facts, NAME: value (T-1041): the project's
	// own policy, its values templated with {cpus} (LaneEnv). Unset: only the facts.
	LaneEnv map[string]string `json:"lane_env,omitempty"`
}

// ReportSections are the sections a worker's task_done report must have, each a `Name:` label (T-1107): built in, the
// done_file_sections key they came from is retired.
var ReportSections = []string{"Summary", "Self-check", "Skipped", "Open questions"}

// Defaults is a fresh copy each call: decoding reuses a slice's backing array. A null in the files leaves a default.
func Defaults() Data {
	return Data{
		Branches:       Branches{Main: "main", Task: "task/{id}"},
		TaskIDPattern:  "T-[0-9]+[a-z]?",
		Paths:          map[string]string{"agents": "docs/agents"},
		LaneLinks:      []string{},
		ProtectedFiles: []string{},
	}
}

// Config is the effective config of the project at Root.
type Config struct {
	Root       string
	Data       Data
	json       []byte   // Data as JSON, for Get
	local      []byte   // .obor/local.json's bytes (nil: none), for Edit's override check (T-754)
	unknownErr error    // LoadReader's warning when read dropped an unknown key
	unknownIn  []string // the unknown keys config.json itself has (the others are local.json's), for LoadForValidate
}

// Rev is the revision of the config as it runs (Rev of its effective JSON: config.json with .obor/local.json merged
// and the defaults filled, T-1050). Unlike the file's own revision it changes when an edit in either file changes what
// runs — a model's id in local.json too — so a start can record it and a keep lane reuse its agent only while it is
// the same (startFits). An edit that leaves the effective config alone (a rewrite, a key set to its own value) does
// not change it.
func (c *Config) Rev() string { return Rev(c.json) }

// Load builds the effective config of the project whose main checkout is root (absolute, physical): config.json and
// local.json merged as JSON (merge), decoded once over the defaults, then the derived keys. A key this binary does not
// know (the config is newer than the binary, or the key was retired) is ignored with one warning per key and process
// on Warn (T-377), for every command, hook, reader and serve alike (T-1081): only obor config validate
// (LoadForValidate) refuses one. A retired pre-v0.23 shape key (retiredKeys) and every other error stay errors.
func Load(root string) (*Config, error) {
	c, unknown, err := read(root)
	for _, key := range unknown {
		warnUnknown(root, key)
	}
	return c, err
}

// LoadForValidate is Load for obor config validate (and the merge guard that runs it), the one strict place (T-710,
// T-1081): unknown are its FAIL lines, one per key this binary does not know, naming it and its file; cfg is the
// config without them, for the other checks. Nothing goes to Warn. Every other error is Load's.
func LoadForValidate(root string) (cfg *Config, unknown []string, err error) {
	c, keys, err := read(root)
	if err != nil {
		return nil, nil, err
	}
	for _, key := range keys {
		file := "local.json"
		if slices.Contains(c.unknownIn, key) {
			file = "config.json"
		}
		unknown = append(unknown, "FAIL ."+key+": unknown to this obor (retired, or newer than this binary): delete it"+
			" (.obor/"+file+")")
	}
	return c, unknown, nil
}

// LoadHook is Load for the hooks (DD-v0.13 A1): a key this binary does not know is ignored, as Load does, but
// returned in unknown instead of warned of on Warn (the SessionStart hook names them; the others say nothing). Every
// other error is Load's: the hooks then fail closed (Fallback).
func LoadHook(root string) (*Config, []string, error) { return read(root) }

// LoadReader is Load for the readers (obor status, obor stop/start, obor web; T-709): a key this binary does not know (a
// config newer than a long-running obor web, or a key this obor retired) is ignored, and warn names it, instead of
// going to Warn. Every other error is Load's; obor config validate (LoadForValidate) refuses such a config. Every
// loader is lenient, validate strict: that is the contract (T-710, T-1081).
func LoadReader(root string) (cfg *Config, warn string, err error) {
	c, unknown, err := read(root)
	if err != nil || len(unknown) == 0 {
		return c, "", err
	}
	return c, "WARN " + strings.TrimPrefix(c.unknownErr.Error(), "obor: ") +
		" ignored: this obor does not know it — either this binary is older than the config (restart it) or the" +
		" key was retired (obor config validate names it; delete it)", nil
}

// Warn receives Load's warnings.
var Warn io.Writer = os.Stderr

var warned sync.Map // "<file>: <key path>" already warned of in this process

// retiredKeys are top-level keys the config shape before v0.23 had (a lane_types table, an agents table). They are
// retired, not an unknown key of a newer binary: every reader refuses one, as a pre-v0.23 config runs on no defaults
// (T-847).
var retiredKeys = []string{"agents", "lane_types"}

// retiredKey is the first top-level member of b retiredKeys names ("" when none).
func retiredKey(b json.RawMessage) string {
	ms, _ := object(b)
	for _, m := range ms {
		if slices.Contains(retiredKeys, m.Key) {
			return m.Key
		}
	}
	return ""
}

// unknownKey names an unknown key (a dotted path), named as encoding/json names an unknown field.
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
func read(root string) (*Config, []string, error) { return readWith(root, nil) }

// LoadWith is Load of the project at root with doc in place of its config.json's bytes (local.json read as it
// is): what a config change would load as, before it is written (obor web's setup edit, T-753).
func LoadWith(root string, doc []byte) (*Config, error) {
	c, _, err := readWith(root, doc)
	return c, err
}

// readWith is read with doc as config.json's bytes (nil: the file's).
func readWith(root string, doc []byte) (*Config, []string, error) {
	names := []string{"config.json", "local.json"}
	var files [2][]byte
	for i, name := range names {
		path := filepath.Join(root, ".obor", name)
		b, err := os.ReadFile(path)
		if i == 0 && doc != nil {
			b, err = doc, nil
		}
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
		// A retired shape key is refused before the merge can hide it and before a lenient reader drops it (T-847).
		if key := retiredKey(b); key != "" {
			return nil, nil, unknownKey{path, key}
		}
	}
	merged := files[0]
	if files[1] != nil {
		merged = merge(files[0], files[1])
	}
	c := &Config{Root: root, Data: Defaults(), local: files[1]}
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
	c.registerPlugins()
	b, err := json.Marshal(c.Data)
	if err != nil {
		return nil, nil, fmt.Errorf("obor: cannot build the effective config: %w", err)
	}
	c.json = b
	if len(unknown) > 0 { // name config.json when it has one, else local.json
		c.unknownErr = unknownKey{filepath.Join(root, ".obor", "local.json"), unknown[0]}
		if _, c.unknownIn = dropUnknown(files[0], reflect.TypeOf(c.Data), ""); len(c.unknownIn) > 0 {
			c.unknownErr = unknownKey{filepath.Join(root, ".obor", "config.json"), c.unknownIn[0]}
		}
	}
	return c, unknown, nil
}

// warnUnknown tells once per process that key (a dotted path) is ignored.
func warnUnknown(root, key string) {
	if _, dup := warned.LoadOrStore(root+": "+key, true); dup {
		return
	}
	fmt.Fprintf(Warn, "obor: %s: unknown key %q ignored: this obor does not know it — either this binary is older"+
		" than the config (restart serve) or the key was retired (obor config validate names it; delete it)\n",
		filepath.Join(root, ".obor"), key)
}

// derive fills what the files leave unset: lanes_dir.
func (d *Data) derive(base string) {
	if d.LanesDir == "" {
		d.LanesDir = "../" + base + "-wt"
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

// Lane is one configured lane, pooled (it runs the model its dispatch picks, T-765), and its Mode (T-1031):
// LaneFresh or LaneKeep.
type Lane struct {
	Name, Dir string
	Mode      string
}

// The lane modes (Lane.Mode, T-1031): a fresh lane's agent is stopped once its task merged and every dispatch starts
// a new one; a keep lane's agent stays at its shell between tasks, the next dispatch's job goes to the same session
// (a start only when the lane has no live agent), and a dispatch naming it while it is busy queues behind its task.
const (
	LaneFresh = "fresh"
	LaneKeep  = "keep"
)

// LaneEntry is one member of a lanes list: the lane's name (L-NN) and whether its agent is kept between tasks.
type LaneEntry struct {
	Name      string `json:"name"`
	KeepAgent bool   `json:"keep_agent,omitempty"`
}

// LaneList is the lanes key: a count N (List nil: N fresh lanes L-01 … L-NN, sugar for that list) or a list
// (List non-nil, N unused). It reads and writes back the form the file has.
type LaneList struct {
	N    int
	List []LaneEntry
}

// Count is how many lanes l configures (a negative count: none; validate refuses it).
func (l LaneList) Count() int {
	if l.List != nil {
		return len(l.List)
	}
	return max(l.N, 0)
}

// Listed: the lanes key is a list, not a count.
func (l LaneList) Listed() bool { return l.List != nil }

// Entries is l as a list: a count's lanes L-01 … L-NN, each fresh.
func (l LaneList) Entries() []LaneEntry {
	if l.List != nil {
		return l.List
	}
	out := make([]LaneEntry, 0, l.Count())
	for i := 1; i <= l.Count(); i++ {
		out = append(out, LaneEntry{Name: LaneName(i)})
	}
	return out
}

func (l *LaneList) UnmarshalJSON(b []byte) error {
	var list []LaneEntry
	if strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
		if err := strict(b, &list); err != nil {
			return fmt.Errorf("lanes: %w", err)
		}
		*l = LaneList{List: append([]LaneEntry{}, list...)}
		return nil
	}
	var n *int
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New(`lanes takes a count or a list of {"name": "L-NN", "keep_agent": true}`)
	}
	*l = LaneList{}
	if n != nil {
		l.N = *n
	}
	return nil
}

func (l LaneList) MarshalJSON() ([]byte, error) {
	if l.List != nil {
		return json.Marshal(l.List)
	}
	return json.Marshal(l.N)
}

// Paths are the named project files (paths.<name>); Reviews is paths.reviews.
type Paths struct {
	Reviews string
	Values  map[string]string
}

func (p Paths) Get(name string) string { return p.Values[name] }

// RoleConfig combines the shared role defaults with effective config overrides.
type RoleConfig struct {
	Role  roles.Role
	Agent string
	// Type is the model roles.<role> names ("": none), Effort the effort after it (`opus:high`, T-695; "": the kind's
	// default).
	Type, Effort string
	Configured   bool
}

// AgentInfo is the role, lane, model (Type) and cwd resolved for a configured agent, and the effort its model ref
// names ("": the kind's default; T-695). A worker's Type is "": its dispatch picks the model.
type AgentInfo struct {
	Role, Lane, Type, Cwd string
	Effort                string
}

// LaneName is the name of the i-th lane (from 1): L-01 … L-99, L-100 ….
func LaneName(i int) string { return fmt.Sprintf("L-%02d", i) }

// Lanes is the configured lanes in config order (a count's: L-01 … L-NN), each with its mode.
func (c *Config) Lanes() []Lane {
	dir := c.LanesDir()
	entries := c.Data.Lanes.Entries()
	out := make([]Lane, 0, len(entries))
	for _, e := range entries {
		mode := LaneFresh
		if e.KeepAgent {
			mode = LaneKeep
		}
		out = append(out, Lane{Name: e.Name, Dir: dir + "/" + e.Name, Mode: mode})
	}
	return out
}

// Lane is the configured lane name (ok false: none).
func (c *Config) Lane(name string) (Lane, bool) {
	for _, l := range c.Lanes() {
		if l.Name == name {
			return l, true
		}
	}
	return Lane{}, false
}

// HasLane reports whether name is a configured lane.
func (c *Config) HasLane(name string) bool {
	_, ok := c.Lane(name)
	return ok
}

// KeepLane: name is a configured lane whose agent is kept between tasks (T-1031).
func (c *Config) KeepLane(name string) bool {
	l, ok := c.Lane(name)
	return ok && l.Mode == LaneKeep
}

// DefaultHandoffTokens is handoff_tokens when the config sets none.
const DefaultHandoffTokens = 500000

// HandoffTokens is the handoff threshold (handoff_tokens, else DefaultHandoffTokens; 0: off): the lead's reminder
// (T-175) and a keep lane's restart of its idle agent between tasks (T-1031).
func (c *Config) HandoffTokens() int64 {
	if n := c.Data.HandoffTokens; n != nil {
		return *n
	}
	return DefaultHandoffTokens
}

// ProjectName is the name the project answers to: its config's name, else its root's base name ("" for no config).
func (c *Config) ProjectName() string {
	switch {
	case c == nil:
		return ""
	case c.Data.Name != "":
		return c.Data.Name
	}
	return filepath.Base(c.Root)
}

// Command is commands.<name>; ok false when unset (absent, null or no cmd).
func (c *Config) Command(name string) (Command, bool) {
	cmd := c.Data.Commands.M[name]
	cmd.Name = name
	return cmd, cmd.Cmd != ""
}

// Paths returns the configured paths.
func (c *Config) Paths() Paths { return Paths{Reviews: c.Data.Paths["reviews"], Values: c.Data.Paths} }

// Role is the one role lookup (T-801): a table role, a kit default row (roles.DefaultRow) or a custom role a
// roles.<name> row adds, with its configured model ref; the agent's name is derived (roles.AgentName). A row whose
// name cannot name a custom role (roles.CheckCustomName) is no role (validate fails it); the driver is never
// configured (obor drive starts it).
func (c *Config) Role(name string) (RoleConfig, bool) {
	e, set := c.Data.Roles.entry(name)
	role, ok := roles.Lookup(name)
	switch {
	case ok:
		set = set && name != roles.Driver
	case roles.CheckCustomName(name) != nil:
		return RoleConfig{}, false
	default:
		role, ok = roles.DefaultRow(name)
		switch {
		case !ok && !set:
			return RoleConfig{}, false
		case !ok:
			role = roles.Custom(name, e.Write)
		case e.Write != nil: // an object replaces a default row's write list
			role.WritePaths = e.Write
		}
		role.Verbs = e.Verbs
	}
	rc := RoleConfig{Role: role}
	if role.Name != "worker" {
		rc.Agent = role.AgentName(c.Data.Prefix, "")
	}
	if set {
		rc.Configured = true
		rc.Type, rc.Effort = ParseModelRef(e.Model)
	}
	return rc, true
}

// AgentsDir is paths.agents (default docs/agents): the project's role docs, relative to the root.
func (c *Config) AgentsDir() string {
	if d := strings.Trim(c.Data.Paths["agents"], "/"); d != "" {
		return d
	}
	return "docs/agents"
}

// RoleDoc is the project's doc of role name, relative to the root: <paths.agents>/<doc file> (roles.Role.DocFile; a
// custom role's <name>.md).
func (c *Config) RoleDoc(name string) string {
	file := strings.ReplaceAll(name, "_", "-") + ".md"
	if rc, ok := c.Role(name); ok {
		file = rc.Role.DocFile()
	}
	return path.Join(c.AgentsDir(), file)
}

// Roles is every role c knows, configured or not: the table's, the kit's default rows, then each other roles.<name>
// that names a custom role, in config order.
func (c *Config) Roles() []RoleConfig {
	var names []string
	for _, r := range roles.All() {
		names = append(names, r.Name)
	}
	for _, r := range roles.DefaultRows() {
		names = append(names, r.Name)
	}
	names = Unique(append(names, c.Data.Roles.Keys...)...)
	var out []RoleConfig
	for _, name := range names {
		if rc, ok := c.Role(name); ok {
			out = append(out, rc)
		}
	}
	return out
}

// AgentName is the herdr agent name of role (of lane, for a worker): the derived name (T-765).
func (c *Config) AgentName(role, lane string) string {
	rc, ok := c.Role(role)
	if !ok {
		return ""
	}
	return rc.Role.AgentName(c.Data.Prefix, lane)
}

// Self is the herdr agent name of the slot role (of lane, for a lane-bound role) holds: a worker's derived name, the
// drive agent's (roles.DriveAgent of its lane's worker), another table role's derived name; "" for no role, a role
// outside the table or a lane-bound role without a lane. The one derivation of "who am I" (T-786): a name obor can
// derive is never stored (no OBOR_AGENT).
func (c *Config) Self(role, lane string) string {
	switch {
	case role == roles.Driver:
		if w := c.AgentName("worker", lane); w != "" {
			return roles.DriveAgent(w)
		}
		return ""
	case roles.LaneBound(role):
		return c.AgentName("worker", lane)
	}
	return c.AgentName(role, "")
}

// MayPush is the one rule for "this caller may push" (the agent guard's checkPush and the git pre-push hook through
// `obor whoami --can-push`, T-793): a role that is not lane-bound (never a worker's or a driver's, T-751) and is
// listed in guards.allow_push. A custom role never pushes (T-801; validate fails an allow_push that lists one).
func (c *Config) MayPush(role string) bool {
	rc, _ := c.Role(role)
	return !roles.LaneBound(role) && !rc.Role.Custom && c.PushAllowed(role)
}

// PushAllowed: guards.allow_push lists role (T-105).
func (c *Config) PushAllowed(role string) bool {
	if role == "" || c.Data.Guards == nil {
		return false
	}
	return slices.Contains(c.Data.Guards.AllowPush, role)
}

// DenyCommands is guards.deny_commands: the commands the agent guard denies for every role (T-905); nil when unset.
func (c *Config) DenyCommands() []DenyCommand {
	if c.Data.Guards == nil {
		return nil
	}
	return c.Data.Guards.DenyCommands
}

// RoleAgents is every role that counts as a role agent, in Roles order: every role but the worker, an optional one
// (deep dive, a custom role, the driver) only when configured. Its agents are the ones obor brief reaches, obor restart
// --stop stops, obor status and obor web list.
func (c *Config) RoleAgents() []RoleConfig {
	var out []RoleConfig
	for _, rc := range c.Roles() {
		if rc.Role.Name != "worker" && (!rc.Role.Optional || rc.Configured) {
			out = append(out, rc)
		}
	}
	return out
}

// RoleDir is where a role agent of role r runs (T-853), by its cwd rule: its role worktree <lanes_dir>/<role> (the
// reviewer, the deep dive, the integrator), else the main checkout. A lane-bound role runs in its lane (Lanes), not
// here: the main checkout.
func (c *Config) RoleDir(r roles.Role) string {
	if r.Cwd == roles.CwdRoleWorktree {
		return c.RoleWorktree(r.Name)
	}
	return c.Root
}

// DefaultLaneCPUs is slots.lane_cpus when unset.
const DefaultLaneCPUs = 2

// LaneCPUs is slots.lane_cpus, else DefaultLaneCPUs.
func (c *Config) LaneCPUs() int {
	if c != nil {
		if s := c.Data.Slots; s != nil && s.LaneCPUs != nil {
			return *s.LaneCPUs
		}
	}
	return DefaultLaneCPUs
}

// LaneEnv is a lane's env (T-1041), as NAME=value entries: the generic facts obor exports — OBOR_LANE_CPUS, the lane's
// CPU budget (slots.lane_cpus) — then slots.lane_env, its values with {cpus} replaced by that budget, names in order.
// What the env means (a Go build's -p, a test tier) is the project's config line, never obor's code.
func LaneEnv(c *Config) []string {
	n := strconv.Itoa(c.LaneCPUs())
	out := []string{"OBOR_LANE_CPUS=" + n}
	m := c.LaneEnvMap()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+strings.ReplaceAll(m[k], LaneCPUsPlaceholder, n))
	}
	return out
}

// LaneCPUsPlaceholder is the one placeholder slots.lane_env's values may carry: slots.lane_cpus.
const LaneCPUsPlaceholder = "{cpus}"

// LaneEnvMap is slots.lane_env (nil: none).
func (c *Config) LaneEnvMap() map[string]string {
	if c == nil || c.Data.Slots == nil {
		return nil
	}
	return c.Data.Slots.LaneEnv
}

// WithLaneEnv is vars (a start's kind env, not changed) with LaneEnv added (T-1041): a lane worker's start of any kind,
// launch's env line and obor setup's pane. A var the config names replaces the one vars had: the config line is the
// policy, obor merges nothing into it.
func WithLaneEnv(vars map[string]string, c *Config) map[string]string {
	out := make(map[string]string, len(vars)+2)
	for k, v := range vars {
		out[k] = v
	}
	for _, kv := range LaneEnv(c) {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// RoleWorktree is the worktree serve owns for role (T-853): <lanes_dir>/<role>, never a lane.
func (c *Config) RoleWorktree(role string) string { return filepath.Join(c.LanesDir(), role) }

// RoleWorktrees is the worktree of every table role whose cwd rule is a role worktree, configured or not: a linked
// worktree there is no lane (project.LaneOf).
func (c *Config) RoleWorktrees() []string {
	var out []string
	for _, r := range roles.All() {
		if r.Cwd == roles.CwdRoleWorktree {
			out = append(out, c.RoleWorktree(r.Name))
		}
	}
	return out
}

// ReviewWriteDirs are the dirs a WriteReviewsOnly role (the reviewer, the deep dive) writes in the main checkout,
// relative to the root: paths.reviews (default docs/reviews). The guard's write profile and the start's writable dirs
// (RoleWriteDirs) are both these.
func (c *Config) ReviewWriteDirs() []string {
	reviews := strings.Trim(c.Paths().Reviews, "/")
	if reviews == "" {
		reviews = "docs/reviews"
	}
	return []string{reviews}
}

// RoleWriteDirs are the dirs outside its cwd an agent of role r writes, absolute (T-853): the reviewer and the deep
// dive (WriteReviewsOnly, in their role worktree) write their files into the main checkout's ReviewWriteDirs; every
// other role none. A sandboxed kind's start lets it write there (agents.Start.WriteDirs: Codex's --add-dir).
func (c *Config) RoleWriteDirs(r roles.Role) []string {
	if r.Cwd != roles.CwdRoleWorktree || r.Write != roles.WriteReviewsOnly {
		return nil
	}

	var out []string
	for _, d := range c.ReviewWriteDirs() {
		out = append(out, filepath.Join(c.Root, d))
	}
	return out
}

// ScratchDir is the merge scratch, <lanes_dir>/merge, absolute.
func (c *Config) ScratchDir() string { return filepath.Join(c.LanesDir(), "merge") }

// LaneAgentAliases returns the names that identify this lane's worker: its derived one (T-1031: no lane row names
// one).
func (c *Config) LaneAgentAliases(lane string) []string {
	return roles.LaneAgentAliases(lane, c.Data.Prefix)
}

// AgentLookup follows lane → role precedence, then resolves an unmatched cwd at to a lane.
func (c *Config) AgentLookup(agent, at string) (AgentInfo, bool) {
	for _, lane := range c.Lanes() {
		for _, name := range c.LaneAgentAliases(lane.Name) {
			if agent == name {
				return AgentInfo{Role: "worker", Lane: lane.Name, Cwd: lane.Dir}, true
			}
		}
	}
	for _, rc := range c.RoleAgents() {
		if rc.Agent != "" && rc.Agent == agent {
			return AgentInfo{Role: rc.Role.Name, Type: rc.Type, Effort: rc.Effort, Cwd: c.RoleDir(rc.Role)}, true
		}
	}
	for _, lane := range c.Lanes() {
		if at != "" && filepath.Clean(at) == filepath.Clean(lane.Dir) {
			return AgentInfo{Role: "worker", Lane: lane.Name, Cwd: lane.Dir}, true
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

// TaskIDOK: id matches task_id_pattern (Go RE2 syntax) as a whole; a pattern that does not compile matches nothing.
func (c *Config) TaskIDOK(id string) bool {
	re, err := regexp.Compile("^(?:" + c.Data.TaskIDPattern + ")$")
	return err == nil && !strings.Contains(id, "\n") && re.MatchString(id)
}

// TaskBranch is the branch the branches.task template names for task id: its {id} replaced, the inverse of
// TaskFromBranch.
func (c *Config) TaskBranch(id string) string {
	return strings.ReplaceAll(c.Data.Branches.Task, "{id}", id)
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

// DifficultyDefault is the model refs of each difficulty level when .obor/config.json's `difficulty` does not name
// the level (a spec's `Difficulty:` line picks the level): hard wants opus at effort high, simple the cheap models.
var DifficultyDefault = map[string][]string{
	"hard":   {"opus:high"},
	"medium": {"sonnet", "deepseek"},
	"simple": {"deepseek", "sonnet", "haiku"},
}

// DifficultyList is level's model refs in dispatch order: the project's difficulty entry, else the built-in default.
// nil for a name that is no level.
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

// PluginEnabledIn: dir's .claude/settings.json enables an obor plugin (enabledPlugins "obor@<marketplace>": true). A
// lane is a worktree, so an untracked settings file of the main checkout is not there unless it is linked (T-705).
func PluginEnabledIn(dir string) bool {
	var settings struct {
		Enabled map[string]any `json:"enabledPlugins"`
	}
	b, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil || json.Unmarshal(b, &settings) != nil {
		return false
	}
	for k, v := range settings.Enabled {
		if strings.HasPrefix(k, "obor@") && v == true {
			return true
		}
	}
	return false
}
