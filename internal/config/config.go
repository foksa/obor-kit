// Package config loads a project's effective config: .obor/local.json merged into .obor/config.json as JSON (objects
// merge key by key; null, lists and scalars are replaced: jq's *), decoded over the defaults, then the derived keys
// (lanes.dir, the merge scratch, claude settings). A file of config shape 1 is reshaped first (Reshape).
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

// Data is the config file's shape, version 2 (docs/research/config-shape.md §2): ten top-level keys, nine groups by
// what they are about and the version. Its field comments are the reference for each key. A key it does not name
// fails the load. A file of shape 1 is reshaped to it on load (Reshape) for one minor release.
type Data struct {
	// Version is the shape of this file: 2.
	Version *int    `json:"version,omitempty"`
	Project Project `json:"project"`
	Lanes   Lanes   `json:"lanes"`
	Agents  Agents  `json:"agents"`
	Rules   Rules   `json:"rules"`
	Merge   Merge   `json:"merge"`
	Nightly Nightly `json:"nightly,omitzero"`
	// Commands are the project's own commands, commands.<name>: named in merge.by_path.rules[].run,
	// merge.by_path.guard or nightly.also_run, or run by `obor run <name>`. The seven commands obor runs by a fixed
	// name live in their groups (Config.Command finds them).
	Commands Ordered[Command] `json:"commands"`
	Checks   Checks           `json:"checks,omitzero"`
	Reviews  Reviews          `json:"reviews,omitzero"`
}

// ShapeVersion is the config shape this obor reads: Data's. A file of an older version is reshaped on load.
const ShapeVersion = 2

// Project is `project`: what the project is called and what its ids look like.
type Project struct {
	// Name is the name obor send, obor projects and the web page use. Default: the directory's name.
	Name string `json:"name,omitempty"`
	// AgentPrefix: agents are named <prefix>-lead, <prefix>-int, <prefix>-l-01.
	AgentPrefix string `json:"agent_prefix,omitempty"`
	// TaskIDPattern is what a task id looks like (Go RE2). Default T-[0-9]+[a-z]?.
	TaskIDPattern string `json:"task_id_pattern"`
	// Terminal is the terminal obor drives panes through (T-1044): one of TerminalKinds; absent: herdr.
	Terminal string `json:"terminal,omitempty"`
}

// Lanes is `lanes`: how many lanes, where they are and what every lane gets.
type Lanes struct {
	// Count is how many lanes: L-01 … L-NN, each fresh (T-765).
	Count int `json:"count,omitempty"`
	// List is in place of count: named lanes, each {name, keep_agent} (T-1031: a keep lane's agent stays between its
	// tasks).
	List []LaneEntry `json:"list,omitzero"`
	// Dir is the directory of the lane worktrees. Default ../<repo>-wt.
	Dir string `json:"dir"`
	// TaskBranch is the branch a lane works on, {id} the task's. Default task/{id}.
	TaskBranch string `json:"task_branch"`
	// Links are paths symlinked from the main checkout into every lane (untracked files a lane needs).
	Links []string `json:"links"`
	// Setup is the command that prepares a new lane (also run in the merge scratch and the nightly's worktree).
	Setup Command `json:"setup,omitzero"`
	// Ready is the command that says a lane is still set up.
	Ready Command `json:"ready,omitzero"`
	// CPUs are the CPUs every process in a lane may use (OBOR_LANE_CPUS; Env's {cpus}). Default 2 (T-1041).
	CPUs *int `json:"cpus,omitempty"`
	// Env is the env every process in a lane gets beside the generic facts, NAME: value; {cpus} stands for CPUs
	// (T-1041). Unset: only the facts.
	Env map[string]string `json:"env,omitempty"`
}

// Agents is `agents`: which model does what, and how each agent CLI starts.
type Agents struct {
	// Roles is the model of each role, `<model>[:<effort>]`; an object {model, write, verbs} defines a custom role.
	Roles Roles `json:"roles"`
	// Difficulty is the models a hard, medium or simple task goes to, first free one first; a level it omits has the
	// built-in default (DifficultyDefault).
	Difficulty map[string][]string `json:"difficulty,omitempty"`
	// Kinds is kinds.<kind>: extra args for every start of that CLI, its work jobs' note, a plugin kind's executable.
	Kinds Ordered[KindEntry] `json:"kinds,omitzero"`
	// Models is models.<name> {kind, id}: a model the built-in table (BuiltinModels) lacks, or one overridden.
	Models Ordered[ModelEntry] `json:"models,omitzero"`
	// HandoffTokens is the context size above which the lead is reminded to hand off (T-175) and a keep lane's idle
	// agent is restarted (T-1031). Default 500000.
	HandoffTokens *int64 `json:"handoff_tokens,omitempty"`
	// Confirmed: the owner went through the models above with the lead (T-647); false or absent: the lead asks.
	Confirmed *bool `json:"confirmed,omitempty"`
	// DocsDir is where the project's role docs are. Default docs/agents.
	DocsDir string `json:"docs_dir,omitempty"`
}

// Rules is `rules`: what an agent may not do.
type Rules struct {
	// MayPush are the roles that may git push (T-105). Default: none.
	MayPush []string `json:"may_push,omitzero"`
	// ProtectedFiles are the files only the lead edits, unless a task's spec unlocks one.
	ProtectedFiles []string `json:"protected_files"`
	// DenyCommands are shell commands denied for every agent (T-905).
	DenyCommands []DenyCommand `json:"deny_commands,omitzero"`
}

// DenyCommand is rules.deny_commands[i] (T-905): a command the agent guard denies for every role. Match is a regex on
// the resolved command line (the program by its basename, its words one space apart, quotes removed); Alone denies a
// matching command run alone (and so also two at once), without it only two matching commands run at once — a
// sibling background unit pair, xargs -P, parallel — are denied. Message is the deny line.
type DenyCommand struct {
	Match   string `json:"match"`
	Alone   bool   `json:"alone,omitempty"`
	Message string `json:"message"`
}

// Merge is `merge`: what a merge must pass and where it lands.
type Merge struct {
	// TargetBranch is the branch tasks merge into. Default main.
	TargetBranch string `json:"target_branch"`
	// Workflow is how a task lands (T-1029): one of WorkflowKinds; absent: scratch-ff.
	Workflow string `json:"workflow,omitempty"`
	// Compile is what the merge slot runs on every merge.
	Compile Command `json:"compile,omitzero"`
	// Check is what every merge must pass (run in the task's lane); the full check when by_path picks no smaller one.
	Check Command `json:"check,omitzero"`
	// ByPath picks a smaller check by the changed paths (design "Change-based merge checks").
	ByPath ByPath `json:"by_path,omitzero"`
	// Retest runs a red check's failing tests once more; {tests} is their names joined by its join (default |,
	// T-1047).
	Retest Command `json:"retest,omitzero"`
	// After runs in the main checkout after a merge, only when one of its when_changed globs changed.
	After Command `json:"after,omitzero"`
}

// ByPath is merge.by_path: the checks chosen by changed path (Classify takes it whole).
type ByPath struct {
	// Guard is a command (by name) that must pass before a smaller check is trusted. Unset: every merge runs
	// merge.check.
	Guard string `json:"guard,omitempty"`
	// NoCheck are files that need no check of their own. Default docs/**.
	NoCheck []string `json:"no_check,omitzero"`
	// Rules: a change inside a rule's paths runs the commands in its run instead of merge.check.
	Rules List[PathRule] `json:"rules,omitzero"`
}

// PathRule is one merge.by_path.rules entry.
type PathRule struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	// Run are the commands, by name.
	Run  []string `json:"run,omitzero"`
	Full bool     `json:"full,omitempty"`
	// AlsoOnFull runs them after merge.check too when the merge is a full one (T-1007): a lane with a file only this
	// rule covers, beside an unmatched one, still runs it. Meaningless with Full: obor config validate refuses the pair.
	AlsoOnFull bool `json:"also_on_full,omitempty"`
}

// Nightly is `nightly`: the nightly run on the target branch.
type Nightly struct {
	// At is the local time of the nightly ("HH:MM", T-464); absent: no nightly.
	At string `json:"at,omitempty"`
	// SlowCheck is the slow tier: the nightly runs lanes.setup, merge.check, then this, and is RED when one fails.
	SlowCheck Command `json:"slow_check,omitzero"`
	// AlsoRun are commands, by name, run after those, in order (T-817); a failure is a WARN line, never RED. A name
	// must be a defined command and no gating one.
	AlsoRun []string `json:"also_run,omitzero"`
}

// Checks is `checks`: how checks run on this machine and how their output is read.
type Checks struct {
	// HeavyAtOnce is how many commands marked "heavy": true run at once on this machine. Default 2.
	HeavyAtOnce *int `json:"heavy_at_once,omitempty"`
	// CPUsPerCheck is exported to a check as CHECK_CPUS. Default: cores / heavy_at_once, at least 1.
	CPUsPerCheck *int `json:"cpus_per_check,omitempty"`
	// SlotDir is where the slot files are. Default /tmp/obor-check-slots.
	SlotDir string `json:"slot_dir,omitempty"`
	Output  Output `json:"output,omitzero"`
}

// Output is checks.output: how a check's output is read.
type Output struct {
	// Summary is the part of a check's output that goes into its result line.
	Summary string `json:"summary,omitempty"`
	// Failure picks the lines of a red check's output that go into the RED line.
	Failure string `json:"failure,omitempty"`
	// FailedTest and FailedPackage read the failing tests and packages from those lines (T-1047: the name in the
	// group test, package), for the retest and obor flakes. Unset: no flake parsing.
	FailedTest    string `json:"failed_test,omitempty"`
	FailedPackage string `json:"failed_package,omitempty"`
}

// Reviews is `reviews`.
type Reviews struct {
	// Dir is where the reviewer writes an open review. Default docs/reviews.
	Dir string `json:"dir,omitempty"`
}

// DefaultMainBranch is merge.target_branch when the file sets none.
const DefaultMainBranch = "main"

// MainBranch is merge.target_branch, the branch tasks merge into (DefaultMainBranch when the file sets none).
func (c *Config) MainBranch() string {
	if c.Data.Merge.TargetBranch != "" {
		return c.Data.Merge.TargetBranch
	}
	return DefaultMainBranch
}

// The workflow kinds serve has an implementation of (internal/checks' ScratchFF: the merge slot, the lane test, the
// fast-forward publish).
const DefaultWorkflow = "scratch-ff"

var WorkflowKinds = []string{DefaultWorkflow}

// WorkflowKind is merge.workflow, else DefaultWorkflow.
func (c *Config) WorkflowKind() string {
	if c != nil && c.Data.Merge.Workflow != "" {
		return c.Data.Merge.Workflow
	}
	return DefaultWorkflow
}

// The terminal kinds obor has an adapter for (internal/herdr).
const DefaultTerminal = "herdr"

var TerminalKinds = []string{DefaultTerminal}

// TerminalKind is project.terminal, else DefaultTerminal.
func (c *Config) TerminalKind() string {
	if c != nil && c.Data.Project.Terminal != "" {
		return c.Data.Project.Terminal
	}
	return DefaultTerminal
}

// Command is a command: commands.<name>, or one of the seven obor runs by a fixed name in its group (FixedCommands).
// A string is {cmd}, null or absent is unset. Name is the name it is run by. When (merge.after's when_changed) and
// Join (merge.retest's join) mean something only there.
type Command struct {
	Name    string   `json:"-"`
	Cmd     string   `json:"cmd"`
	Timeout int      `json:"timeout,omitempty"`
	Heavy   bool     `json:"heavy,omitempty"` // takes a machine-wide check slot (obor run)
	When    []string `json:"when_changed,omitzero"`
	Join    string   `json:"join,omitempty"`
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

// Roles is agents.roles: agents.roles.<name> in config order (T-801), a table role's model ref or a custom role's row
// (RoleEntry; a null member is no member).
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
		return errors.New("agents.roles: must be an object")
	}
	var kept []member
	for _, m := range ms {
		if string(m.Val) != "null" {
			kept = append(kept, m)
		}
	}
	return r.Ordered.UnmarshalJSON(joinMembers(kept))
}

// entry is agents.roles.<name>; ok false when not configured.
func (r *Roles) entry(name string) (RoleEntry, bool) {
	e, ok := r.M[name]
	return e, ok
}

// RoleEntry is agents.roles.<name> (T-801): a model ref string ("": configured, no model named), or a custom role's
// object {"model": "<ref>", "write": ["<dir>/", "<file>"], "verbs": ["spec_set", …]} (Object). Write is nil when the
// entry names no write list (a string, or an object without write: a kit default row keeps its own). Verbs are the
// wire methods serve admits the role to (T-1039, roles.Verbs; a built-in role has none: validate fails them).
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

// SetupConfirmed is the owner's answer to the setup table (agents.confirmed): absent is not confirmed (T-647).
func (c *Config) SetupConfirmed() bool {
	return c.Data.Agents.Confirmed != nil && *c.Data.Agents.Confirmed
}

// SetupConfirmedSet: the config has agents.confirmed at all (false is an answer; absent is not).
func (c *Config) SetupConfirmedSet() bool { return c.Data.Agents.Confirmed != nil }

// MergeSummaryRegex is checks.output.summary: the part of a check's output that goes into its result line.
func (c *Config) MergeSummaryRegex() string { return c.Data.Checks.Output.Summary }

// MergeFailGrep is checks.output.failure: the lines of a red check's output that go into the RED line.
func (c *Config) MergeFailGrep() string { return c.Data.Checks.Output.Failure }

// MergeFlakeRegex is checks.output's failed_test and failed_package, as the pair the flake parse reads (nil when both
// are unset).
func (c *Config) MergeFlakeRegex() []string {
	o := c.Data.Checks.Output
	if o.FailedTest == "" && o.FailedPackage == "" {
		return nil
	}
	return []string{o.FailedTest, o.FailedPackage}
}

// MergeGuard is merge.by_path.guard: the command that must pass before a smaller check is trusted.
func (c *Config) MergeGuard() string { return c.Data.Merge.ByPath.Guard }

// MergeNightlyCommands is nightly.also_run: the commands `obor nightly` runs after its gating checks, in order.
func (c *Config) MergeNightlyCommands() []string { return c.Data.Nightly.AlsoRun }

// ByPath is merge.by_path as a value: what classifies a merge (Classify's guard, no-check files and rules) takes it
// whole.
func (c *Config) ByPath() ByPath { return c.Data.Merge.ByPath }

// DefaultRetestJoin is merge.retest.join's default: a regexp's alternatives separator, the form a retest's -run takes.
const DefaultRetestJoin = "|"

// RetestJoin is merge.retest.join, else DefaultRetestJoin.
func (c *Config) RetestJoin() string {
	if c != nil && c.Data.Merge.Retest.Join != "" {
		return c.Data.Merge.Retest.Join
	}
	return DefaultRetestJoin
}

// NightlyAt is nightly.at ("HH:MM" local time): when obor serve runs the nightly full check; "" is never.
func (c *Config) NightlyAt() string { return c.Data.Nightly.At }

// ReportSections are the sections a worker's task_done report must have, each a `Name:` label (T-1107): built in, the
// done_file_sections key they came from is retired.
var ReportSections = []string{"Summary", "Self-check", "Skipped", "Open questions"}

// Defaults is a fresh copy each call: decoding reuses a slice's backing array. A null in the files leaves a default.
func Defaults() Data {
	return Data{
		Project: Project{TaskIDPattern: "T-[0-9]+[a-z]?"},
		Lanes:   Lanes{TaskBranch: "task/{id}", Links: []string{}},
		Agents:  Agents{DocsDir: "docs/agents"},
		Rules:   Rules{ProtectedFiles: []string{}},
		Merge:   Merge{TargetBranch: DefaultMainBranch},
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
	shape1     bool     // config.json is of shape 1, reshaped in memory (Reshape)
}

// Shape1: config.json is of shape 1 and runs reshaped in memory (Reshape), until serve or obor config migrate
// rewrites it; the release after refuses it.
func (c *Config) Shape1() bool { return c != nil && c.shape1 }

// Shape1Warning is the one line a load of a shape-1 config.json warns with.
func Shape1Warning(root string) string {
	return fmt.Sprintf("obor: %s: config shape 1: read as shape 2 for this release, refused by the next; serve "+
		"rewrites it at its start (obor config migrate by hand)", filepath.Join(root, ".obor", "config.json"))
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
	if c.Shape1() {
		warnOnce(root+": shape 1", Shape1Warning(root))
	}
	return c, err
}

// LoadForValidate is Load for obor config validate (and the merge guard that runs it), the one strict place (T-710,
// T-1081): unknown are its FAIL lines, one per key this binary does not know, naming it and its file; cfg is the
// config without them, for the other checks. Nothing goes to Warn (a shape-1 file is Validate's WARN). Every other
// error is Load's.
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
// returned in unknown instead of warned of on Warn (the SessionStart hook names them; the others say nothing; a
// shape-1 file is read reshaped without a word). Every other error is Load's: the hooks then fail closed (Fallback).
func LoadHook(root string) (*Config, []string, error) { return read(root) }

// LoadReader is Load for the readers (obor status, obor stop/start, obor web; T-709): a key this binary does not know (a
// config newer than a long-running obor web, or a key this obor retired) is ignored, and warn names it, instead of
// going to Warn. Every other error is Load's; obor config validate (LoadForValidate) refuses such a config. Every
// loader is lenient, validate strict: that is the contract (T-710, T-1081).
func LoadReader(root string) (cfg *Config, warn string, err error) {
	c, unknown, err := read(root)
	if err != nil {
		return c, "", err
	}
	var warns []string
	if len(unknown) > 0 {
		warns = append(warns, "WARN "+strings.TrimPrefix(c.unknownErr.Error(), "obor: ")+
			" ignored: this obor does not know it — either this binary is older than the config (restart it) or the"+
			" key was retired (obor config validate names it; delete it)")
	}
	if c.Shape1() {
		warns = append(warns, "WARN "+strings.TrimPrefix(Shape1Warning(root), "obor: "))
	}
	return c, strings.Join(warns, "; "), nil
}

// Warn receives Load's warnings.
var Warn io.Writer = os.Stderr

var warned sync.Map // "<file>: <key path>" already warned of in this process

// warnOnce writes line to Warn once per process and id.
func warnOnce(id, line string) {
	if _, dup := warned.LoadOrStore(id, true); !dup {
		fmt.Fprintln(Warn, line)
	}
}

// retiredKeys are top-level keys the config shape before v0.23 had (a lane_types table, an agents table). They are
// retired, not an unknown key of a newer binary: every reader refuses one in a file of shape 1, as a pre-v0.23 config
// runs on no defaults (T-847). In shape 2, agents is a group.
var retiredKeys = []string{"agents", "lane_types"}

// retiredKey is the first top-level member of b retiredKeys names ("" when none). An agents object whose members are
// all shape 2's agents group's (a file with no version written in shape 2) is that group, not the old table.
func retiredKey(b json.RawMessage) string {
	ms, _ := object(b)
	for _, m := range ms {
		if slices.Contains(retiredKeys, m.Key) && !(m.Key == "agents" && agentsGroup(m.Val)) {
			return m.Key
		}
	}
	return ""
}

// agentsGroup: v is an object of shape 2's agents members only.
func agentsGroup(v json.RawMessage) bool {
	ms, ok := object(v)
	for _, m := range ms {
		if _, known := fieldOf(reflect.TypeFor[Agents](), m.Key); !known {
			return false
		}
	}
	return ok
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

// readWith is read with doc as config.json's bytes (nil: the file's). A config.json of shape 1 (ShapeOf) is reshaped
// before the merge, and local.json with it (it has no version: it follows config.json's shape); one struct, one decode.
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
	}
	shape1 := ShapeOf(files[0]) < ShapeVersion
	for i, name := range names {
		if !shape1 || files[i] == nil {
			continue
		}
		path := filepath.Join(root, ".obor", name)
		// A retired shape key is refused before the merge can hide it and before a lenient reader drops it (T-847).
		if key := retiredKey(files[i]); key != "" {
			return nil, nil, unknownKey{path, key}
		}
		b, err := Reshape(files[i])
		if err != nil {
			return nil, nil, fmt.Errorf("obor: %s: cannot read it as config shape 2: %v", path, err)
		}
		files[i] = b
	}
	merged := files[0]
	if files[1] != nil {
		merged = merge(files[0], files[1])
	}
	c := &Config{Root: root, Data: Defaults(), local: files[1], shape1: shape1}
	merged, unknown := dropUnknown(merged, reflect.TypeOf(c.Data), "")
	if err := strict(merged, &c.Data); err != nil {
		// Name the file at fault: config.json when it fails alone, else local.json.
		name := "local.json"
		if b, _ := dropUnknown(files[0], reflect.TypeOf(c.Data), ""); strict(b, &Data{}) != nil {
			name = "config.json"
		}
		msg := strings.TrimPrefix(err.Error(), "json: ")
		if at := badCommand(merged); at != "" {
			msg = at + ": " + msg
		}
		return nil, nil, fmt.Errorf("obor: %s: %s", filepath.Join(root, ".obor", name), msg)
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

// badCommand is the dotted path of the first of FixedCommands whose value in doc does not decode ("" when none): the
// decoder names no field for an error a Command's own decode returns (commands.<name> are named by Ordered).
func badCommand(doc []byte) string {
	for _, name := range []string{"lane_setup", "lane_ready", "compile", "check", "retest", "after_merge", "check_milestone"} {
		at := FixedCommands[name]
		if v, _ := MemberAt(doc, at); v != nil && strict(v, &Command{}) != nil {
			return strings.Join(at, ".")
		}
	}
	return ""
}

// warnUnknown tells once per process that key (a dotted path) is ignored.
func warnUnknown(root, key string) {
	warnOnce(root+": "+key, fmt.Sprintf("obor: %s: unknown key %q ignored: this obor does not know it — either this"+
		" binary is older than the config (restart serve) or the key was retired (obor config validate names it; delete"+
		" it)", filepath.Join(root, ".obor"), key))
}

// derive fills what the files leave unset: lanes.dir.
func (d *Data) derive(base string) {
	if d.Lanes.Dir == "" {
		d.Lanes.Dir = "../" + base + "-wt"
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
func (c *Config) LanesDir() string { return AbsUnder(c.Root, c.Data.Lanes.Dir) }

// LanesDirSetting is lanes.dir as the config file stores it (relative to the root, or absolute): the template
// placeholder's value; LanesDir is the absolute form.
func (c *Config) LanesDirSetting() string { return c.Data.Lanes.Dir }

// LaneCount is how many lanes the config has: a count, or a list's length (T-1031).
func (c *Config) LaneCount() int { return c.Data.Lanes.total() }

// LanesListed: the lanes are a list the owner wrote (lanes.list), not a count.
func (c *Config) LanesListed() bool { return c.Data.Lanes.List != nil }

// LaneLinks is lanes.links: the paths symlinked from the main checkout into every lane.
func (c *Config) LaneLinks() []string { return c.Data.Lanes.Links }

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

// LaneEntry is one member of lanes.list: the lane's name (L-NN) and whether its agent is kept between tasks.
type LaneEntry struct {
	Name      string `json:"name"`
	KeepAgent bool   `json:"keep_agent,omitempty"`
}

// total is how many lanes l configures: the list's length when there is one (it wins over a count), else the count
// (a negative count: none; validate refuses it).
func (l Lanes) total() int {
	if l.List != nil {
		return len(l.List)
	}
	return max(l.Count, 0)
}

// entries is l as a list: a count's lanes L-01 … L-NN, each fresh.
func (l Lanes) entries() []LaneEntry {
	if l.List != nil {
		return l.List
	}
	out := make([]LaneEntry, 0, l.total())
	for i := 1; i <= l.total(); i++ {
		out = append(out, LaneEntry{Name: LaneName(i)})
	}
	return out
}

// RoleConfig combines the shared role defaults with effective config overrides.
type RoleConfig struct {
	Role  roles.Role
	Agent string
	// Type is the model agents.roles.<role> names ("": none), Effort the effort after it (`opus:high`, T-695; "": the kind's
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
	entries := c.Data.Lanes.entries()
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

// HandoffTokens is the handoff threshold (agents.handoff_tokens, else DefaultHandoffTokens; 0: off): the lead's reminder
// (T-175) and a keep lane's restart of its idle agent between tasks (T-1031).
func (c *Config) HandoffTokens() int64 {
	if n := c.Data.Agents.HandoffTokens; n != nil {
		return *n
	}
	return DefaultHandoffTokens
}

// ProjectName is the name the project answers to: its config's name, else its root's base name ("" for no config).
func (c *Config) ProjectName() string {
	switch {
	case c == nil:
		return ""
	case c.Data.Project.Name != "":
		return c.Data.Project.Name
	}
	return filepath.Base(c.Root)
}

// Name is the name the config stores ("" when it sets none); ProjectName is the one to show.
func (c *Config) Name() string { return c.Data.Project.Name }

// Prefix is the agent prefix the config stores ("" when it sets none).
func (c *Config) Prefix() string { return c.Data.Project.AgentPrefix }

// FixedCommands are the commands obor runs by a fixed name, each with its home in shape 2 (config-shape.md §3): the
// names typed at a prompt and written in logs and steps stay (obor run check, <task>-check.log).
var FixedCommands = map[string][]string{
	"lane_setup":      {"lanes", "setup"},
	"lane_ready":      {"lanes", "ready"},
	"compile":         {"merge", "compile"},
	"check":           {"merge", "check"},
	"retest":          {"merge", "retest"},
	"after_merge":     {"merge", "after"},
	"check_milestone": {"nightly", "slow_check"},
}

// fixed is the command obor runs by the fixed name name (ok false: name is a project command's).
func (d *Data) fixed(name string) (Command, bool) {
	switch name {
	case "lane_setup":
		return d.Lanes.Setup, true
	case "lane_ready":
		return d.Lanes.Ready, true
	case "compile":
		return d.Merge.Compile, true
	case "check":
		return d.Merge.Check, true
	case "retest":
		return d.Merge.Retest, true
	case "after_merge":
		return d.Merge.After, true
	case "check_milestone":
		return d.Nightly.SlowCheck, true
	}
	return Command{}, false
}

// Command is the command run by name: one of the FixedCommands in its group, else commands.<name>; ok false when
// unset (absent, null or no cmd).
func (c *Config) Command(name string) (Command, bool) {
	cmd, ok := c.Data.fixed(name)
	if !ok {
		cmd = c.Data.Commands.M[name]
	}
	cmd.Name = name
	return cmd, cmd.Cmd != ""
}

// ReviewsDirSet is reviews.dir as the file sets it ("" when it sets none; ReviewsDir has the default).
func (c *Config) ReviewsDirSet() string { return c.Data.Reviews.Dir }

// Role is the one role lookup (T-801): a table role, a kit default row (roles.DefaultRow) or a custom role a
// agents.roles.<name> row adds, with its configured model ref; the agent's name is derived (roles.AgentName). A row whose
// name cannot name a custom role (roles.CheckCustomName) is no role (validate fails it); the driver is never
// configured (obor drive starts it).
func (c *Config) Role(name string) (RoleConfig, bool) {
	e, set := c.Data.Agents.Roles.entry(name)
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
		rc.Agent = role.AgentName(c.Data.Project.AgentPrefix, "")
	}
	if set {
		rc.Configured = true
		rc.Type, rc.Effort = ParseModelRef(e.Model)
	}
	return rc, true
}

// AgentsDir is agents.docs_dir (default docs/agents): the project's role docs, relative to the root.
func (c *Config) AgentsDir() string {
	if d := strings.Trim(c.Data.Agents.DocsDir, "/"); d != "" {
		return d
	}
	return "docs/agents"
}

// RoleDoc is the project's doc of role name, relative to the root: <agents.docs_dir>/<doc file> (roles.Role.DocFile; a
// custom role's <name>.md).
func (c *Config) RoleDoc(name string) string {
	file := strings.ReplaceAll(name, "_", "-") + ".md"
	if rc, ok := c.Role(name); ok {
		file = rc.Role.DocFile()
	}
	return path.Join(c.AgentsDir(), file)
}

// Roles is every role c knows, configured or not: the table's, the kit's default rows, then each other agents.roles.<name>
// that names a custom role, in config order.
func (c *Config) Roles() []RoleConfig {
	var names []string
	for _, r := range roles.All() {
		names = append(names, r.Name)
	}
	for _, r := range roles.DefaultRows() {
		names = append(names, r.Name)
	}
	names = Unique(append(names, c.Data.Agents.Roles.Keys...)...)
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
	return rc.Role.AgentName(c.Data.Project.AgentPrefix, lane)
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

// MayPush is the one rule for "this role may push" (the agent guard's checkPush and, for a role, the git pre-push hook
// through `obor whoami --can-push`, T-793): a role that is not lane-bound (never a worker's or a driver's, T-751) and
// is listed in rules.may_push. A custom role never pushes (T-801; validate fails a may_push that lists one). A
// caller with no role is not an agent: the pre-push hook lets it push without asking (T-1116).
func (c *Config) MayPush(role string) bool {
	rc, _ := c.Role(role)
	return !roles.LaneBound(role) && !rc.Role.Custom && c.PushAllowed(role)
}

// PushAllowed: rules.may_push lists role (T-105).
func (c *Config) PushAllowed(role string) bool {
	return role != "" && slices.Contains(c.Data.Rules.MayPush, role)
}

// DenyCommands is rules.deny_commands: the commands the agent guard denies for every role (T-905); nil when unset.
func (c *Config) DenyCommands() []DenyCommand { return c.Data.Rules.DenyCommands }

// ProtectedFiles is rules.protected_files: the files only the lead edits, unless a task's spec unlocks one (T-1123).
func (c *Config) ProtectedFiles() []string { return c.Data.Rules.ProtectedFiles }

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

// RoleDir is where a role agent of role r runs (T-853), by its cwd rule: its role worktree <lanes.dir>/<role> (the
// reviewer, the deep dive, the integrator), else the main checkout. A lane-bound role runs in its lane (Lanes), not
// here: the main checkout.
func (c *Config) RoleDir(r roles.Role) string {
	if r.Cwd == roles.CwdRoleWorktree {
		return c.RoleWorktree(r.Name)
	}
	return c.Root
}

// DefaultLaneCPUs is lanes.cpus when unset.
const DefaultLaneCPUs = 2

// LaneCPUs is lanes.cpus, else DefaultLaneCPUs.
func (c *Config) LaneCPUs() int {
	if c != nil && c.Data.Lanes.CPUs != nil {
		return *c.Data.Lanes.CPUs
	}
	return DefaultLaneCPUs
}

// HeavyAtOnce is checks.heavy_at_once: how many commands marked heavy run at once on this machine (false: the file
// sets none).
func (c *Config) HeavyAtOnce() (int, bool) {
	if c.Data.Checks.HeavyAtOnce == nil {
		return 0, false
	}
	return *c.Data.Checks.HeavyAtOnce, true
}

// CPUsPerCheck is checks.cpus_per_check: the CPUs one check may use (CHECK_CPUS; false: the file sets none).
func (c *Config) CPUsPerCheck() (int, bool) {
	if c.Data.Checks.CPUsPerCheck == nil {
		return 0, false
	}
	return *c.Data.Checks.CPUsPerCheck, true
}

// CheckSlotDir is checks.slot_dir: where the check slot files are ("" when the file sets none).
func (c *Config) CheckSlotDir() string { return c.Data.Checks.SlotDir }

// LaneEnv is a lane's env (T-1041), as NAME=value entries: the generic facts obor exports — OBOR_LANE_CPUS, the lane's
// CPU budget (lanes.cpus) — then lanes.env, its values with {cpus} replaced by that budget, names in order.
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

// LaneCPUsPlaceholder is the one placeholder lanes.env's values may carry: lanes.cpus.
const LaneCPUsPlaceholder = "{cpus}"

// LaneEnvMap is lanes.env (nil: none).
func (c *Config) LaneEnvMap() map[string]string {
	if c == nil {
		return nil
	}
	return c.Data.Lanes.Env
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

// RoleWorktree is the worktree serve owns for role (T-853): <lanes.dir>/<role>, never a lane.
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

// ReviewsDir is reviews.dir (default docs/reviews): where the reviewer writes an open review (T-731).
func (c *Config) ReviewsDir() string {
	if r := strings.Trim(c.Data.Reviews.Dir, "/"); r != "" {
		return r
	}
	return "docs/reviews"
}

// ReviewWriteDirs are the dirs a WriteReviewsOnly role (the reviewer, the deep dive) writes in the main checkout,
// relative to the root: reviews.dir (default docs/reviews). The guard's write profile and the start's writable dirs
// (RoleWriteDirs) are both these.
func (c *Config) ReviewWriteDirs() []string {
	return []string{c.ReviewsDir()}
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

// ScratchDir is the merge scratch, <lanes.dir>/merge, absolute.
func (c *Config) ScratchDir() string { return filepath.Join(c.LanesDir(), "merge") }

// LaneAgentAliases returns the names that identify this lane's worker: its derived one (T-1031: no lane row names
// one).
func (c *Config) LaneAgentAliases(lane string) []string {
	return roles.LaneAgentAliases(lane, c.Data.Project.AgentPrefix)
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

// TaskIDOK: id matches project.task_id_pattern (Go RE2 syntax) as a whole; a pattern that does not compile matches nothing.
func (c *Config) TaskIDOK(id string) bool {
	re, err := regexp.Compile("^(?:" + c.Data.Project.TaskIDPattern + ")$")
	return err == nil && !strings.Contains(id, "\n") && re.MatchString(id)
}

// TaskIDPattern is project.task_id_pattern: what a task id looks like (Go RE2 syntax).
func (c *Config) TaskIDPattern() string { return c.Data.Project.TaskIDPattern }

// TaskBranch is the branch the lanes.task_branch template names for task id: its {id} replaced, the inverse of
// TaskFromBranch.
func (c *Config) TaskBranch(id string) string {
	return strings.ReplaceAll(c.Data.Lanes.TaskBranch, "{id}", id)
}

// TaskFromBranch is the task id in a branch named by the lanes.task_branch template ("" when none).
func (c *Config) TaskFromBranch(branch string) string {
	pre, suf, ok := strings.Cut(c.Data.Lanes.TaskBranch, "{id}")
	if !ok || len(branch) < len(pre)+len(suf) || !strings.HasPrefix(branch, pre) || !strings.HasSuffix(branch, suf) {
		return ""
	}
	if id := branch[len(pre) : len(branch)-len(suf)]; c.TaskIDOK(id) {
		return id
	}
	return ""
}

// ParseClock reads a local time of day "HH:MM" (nightly.at): its hour and minute; false when s is none.
func ParseClock(s string) (hour, minute int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// DifficultyDefault is the model refs of each difficulty level when .obor/config.json's `agents.difficulty` does not name
// the level (a spec's `Difficulty:` line picks the level): hard wants opus at effort high, simple the cheap models.
var DifficultyDefault = map[string][]string{
	"hard":   {"opus:high"},
	"medium": {"sonnet", "deepseek"},
	"simple": {"deepseek", "sonnet", "haiku"},
}

// DifficultyList is level's model refs in dispatch order: the project's agents.difficulty entry, else the built-in default.
// nil for a name that is no level.
func (c *Config) DifficultyList(level string) []string {
	listed, ok := DifficultyDefault[level]
	if !ok {
		return nil
	}
	if own, set := c.Data.Agents.Difficulty[level]; set {
		listed = own
	}
	return listed
}

// HasDifficulty: the config sets agents.difficulty at all (else every level's built-in default applies).
func (c *Config) HasDifficulty() bool { return c.Data.Agents.Difficulty != nil }

// KindCommandSet: agents.kinds.<name>.plugin is set in the config (KindCommand falls back to the kit's binary for a
// kit kind; obor setup fills the fallback in).
func (c *Config) KindCommandSet(name string) bool { return c.Data.Agents.Kinds.M[name].Plugin != "" }

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
