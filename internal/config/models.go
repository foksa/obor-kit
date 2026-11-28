package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/roles"
)

// The models: the config names a model with its effort, `<model>[:<effort>]` (a model ref), in difficulty
// levels, roles, agents and a spec's Model: line. A model is agents.models.<name> {kind, id}, the one place a name
// resolves; what starts it is its kind's base args (KindBaseArgs), its model id as the kind takes it
// (agents.Kinds.ModelArgs), then kinds.<kind>.args. The effort is the kind's own flag (agents.Start.Effort).

// NamedModel is one agents.models entry with its name: the kind and the model id the kind starts it with.
type NamedModel struct{ Name, Kind, ID string }

// Member is e as one member of agents.models, ready to paste: "opus": {"kind": "claude", "id": "opus"}.
func (e NamedModel) Member() string {
	return fmt.Sprintf("%s: {\"kind\": %s, \"id\": %s}", Compact(e.Name), Compact(e.Kind), Compact(e.ID))
}

// UpgradeModels are the entries `obor setup agents` adds for a name the config's levels or roles name without an
// agents.models entry (UpgradeEntries), the kind it gives an existing entry with no kind (KindFills), and the entry an
// unknown-model refusal quotes ready to paste (UnknownModel). No name resolves from it.
var UpgradeModels = []NamedModel{
	{"opus", "claude", "opus"},
	{"sonnet", "claude", "sonnet"},
	{"haiku", "claude", "haiku"},
	{"fable", "claude", "fable"},
	{"deepseek", "opencode", "opencode-go/deepseek-v4.1-flash"},
	{"terra", "codex", "gpt-6.1-terra"},
	{"sol", "codex", "gpt-6.1-sol"},
	{"luna", "codex", "gpt-6-luna"},
}

// UpgradeEntries are the UpgradeModels the config's levels or roles name (UsedModels) that agents.models lacks, in
// UpgradeModels' order: what `obor setup agents` adds.
func (c *Config) UpgradeEntries() []NamedModel {
	used := c.UsedModels()
	var out []NamedModel
	for _, e := range UpgradeModels {
		if _, has := c.Data.Agents.Models.M[e.Name]; !has && slices.Contains(used, e.Name) {
			out = append(out, e)
		}
	}
	return out
}

// KindFills are the UpgradeModels entries an existing agents.models entry has without a kind, in UpgradeModels' order:
// what `obor setup agents` gives its former kind, its id and every other member kept. c is the shared config
// (LoadMain): only the entries .obor/config.json has are filled, and an explicit kind is left as it is.
func (c *Config) KindFills() []NamedModel {
	var out []NamedModel
	for _, e := range UpgradeModels {
		if m, ok := c.Data.Agents.Models.M[e.Name]; ok && m.Kind == "" {
			out = append(out, e)
		}
	}
	return out
}

// KindBaseArgs are the args every start of a compiled kind begins with (kinds.<kind>.args are appended after the
// model). Codex's --no-daemon: its shared background server would give the MCP server and hooks the daemon's env and
// cwd, not the lane's. A plugin kind's start adds its own fixed flags (docs/agents/kind-protocol.md).
var KindBaseArgs = map[string][]string{
	"claude": {"--permission-mode", "auto"},
	"codex":  {"-s", "workspace-write", "-a", "never", "--no-daemon"},
}

// HCRequiredArgs are the flags a compiled kind's start must carry on an hc project (project.terminal "hc"), by kind:
// each turns off the kind's shared background server, whose commands would run outside the agent's hc terminal with
// another env and cwd (docs/research/hc-probes.md P1). KindBaseArgs carries it; obor config validate refuses a model
// without.
var HCRequiredArgs = map[string]string{"codex": "--no-daemon"}

// KindEntry is agents.kinds.<kind>: Args are extra args for every start of that CLI, JobNote the text added to every
// work job an agent of that kind gets. Plugin makes it a plugin kind: the executable answering obor's kind
// protocol (docs/agents/kind-protocol.md), a path (relative: to the project root; KindCommand).
type KindEntry struct {
	Args    []string `json:"args,omitzero"`
	JobNote string   `json:"job_note,omitempty"`
	Plugin  string   `json:"plugin,omitempty"`
}

// KindEntry is agents.kinds.<name> (the zero entry when the config does not name it): its args and its work jobs'
// note.
func (c *Config) KindEntry(name string) KindEntry { return c.Data.Agents.Kinds.M[name] }

// KitKinds are the plugin kinds the kit links into itself, by name: the name of the kit binary each is answered under
// in process (plugin.Local). A config naming no agents.kinds.<name>.plugin for one, or that name (or a path ending in
// it), has the kit's; any other plugin replaces it.
var KitKinds = map[string]string{"agy": "obor-kind-agy", "opencode": "obor-kind-opencode"}

// KindCommand is agents.kinds.<name>.plugin as the command obor runs ("": name is no plugin kind of this config): a
// kit kind with no plugin, or naming its kit binary, is that binary's name as it is (KitKinds: obor answers it in
// process), any other relative path relative to the project root.
func (c *Config) KindCommand(name string) string {
	switch cmd := c.Data.Agents.Kinds.M[name].Plugin; {
	case cmd == "" || cmd == KitKinds[name]:
		return KitKinds[name]
	case filepath.IsAbs(cmd):
		return cmd
	default:
		return filepath.Join(c.Root, cmd)
	}
}

// PluginKinds are this config's plugin kinds: each agents.kinds.<name> with a plugin, in config order, then each kit
// kind it names no plugin for (KitKinds, sorted).
func (c *Config) PluginKinds() []string {
	var out []string
	for _, name := range c.Data.Agents.Kinds.Keys {
		if c.Data.Agents.Kinds.M[name].Plugin != "" {
			out = append(out, name)
		}
	}
	var kit []string
	for name := range KitKinds {
		if !slices.Contains(out, name) {
			kit = append(kit, name)
		}
	}
	slices.Sort(kit)
	return append(out, kit...)
}

// StateDir is the project's state dir (project.StateDir: config cannot import project).
func (c *Config) StateDir() string { return filepath.Join(c.Root, ".obor", "state") }

// buildKinds are the compiled kinds and this config's plugin kinds (PluginKinds), each with the manifest obor setup
// agents cached: no process runs (every load, hooks included). A binary without the plugin package has none.
func (c *Config) buildKinds() agents.Kinds {
	if agents.Plugins == nil {
		return agents.Kinds{}
	}
	var ks []agents.Kind
	for _, name := range c.PluginKinds() {
		ks = append(ks, agents.Plugins.Kind(name, c.KindCommand(name), c.StateDir()))
	}
	return agents.WithPlugins(ks)
}

// Kinds are the kinds this config runs: the compiled ones and its plugin kinds, built once at its load. A Config not
// loaded (a literal, or nil: no config) holds the compiled kinds alone.
func (c *Config) Kinds() agents.Kinds {
	if c == nil {
		return agents.Kinds{}
	}
	return c.kinds
}

// Kind is the kind name of this config (Kinds): a compiled kind, or one of its plugin kinds; ok false: no such kind.
func (c *Config) Kind(name string) (agents.Kind, bool) { return c.Kinds().Lookup(name) }

// ModelEntry is agents.models.<name>: Kind is the kind that starts it (required), ID the model id the kind takes,
// verbatim ("" starts the kind without a model: its own choice).
type ModelEntry struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id"`
}

// Model is a resolved model: what starts an agent of it. Name is the model's name in the config, ID the model id the
// kind starts ("": the kind's own choice), Args the full launch args (base, model, agents.kinds.<kind>.args),
// PromptExtra the kind's job_note.
type Model struct {
	Name, Kind, ID string
	Args           []string
	PromptExtra    string
	kinds          agents.Kinds // the kinds of the config that resolved it (Config.Model; a literal: the compiled ones)
}

// ParseModelRef splits a model ref `<model>[:<effort>]`: the model's name and the effort ("": the kind's default).
func ParseModelRef(ref string) (model, effort string) {
	model, effort, _ = strings.Cut(ref, ":")
	return model, effort
}

// ModelRef is the ref of model with effort (ParseModelRef's inverse): the model alone without an effort or a model.
func ModelRef(model, effort string) string {
	if model == "" || effort == "" {
		return model
	}
	return model + ":" + effort
}

// Model resolves name from agents.models; ok false: the config has no entry of that name, or one with no kind
// (UnknownModel says which).
func (c *Config) Model(name string) (Model, bool) {
	e, ok := c.Data.Agents.Models.M[name]
	if !ok || e.Kind == "" {
		return Model{}, false
	}
	m := Model{Name: name, Kind: e.Kind, ID: e.ID, kinds: c.kinds}
	args := append([]string(nil), KindBaseArgs[m.Kind]...)
	if m.ID != "" {
		if with, err := c.kinds.ModelArgs(m.Kind, args, m.ID); err == nil {
			args = with
		} else {
			args = append(args, "--model", m.ID)
		}
	}
	k := c.Data.Agents.Kinds.M[m.Kind]
	m.Args = append(args, k.Args...)
	m.PromptExtra = k.JobNote
	return m, true
}

// ModelNames are the models this config knows: agents.models' names in config order.
func (c *Config) ModelNames() []string { return slices.Clone(c.Data.Agents.Models.Keys) }

// UnknownModel is why name resolves to no model (Config.Model): agents.models has no such entry — the message lists
// the names it has and, for a name of UpgradeModels, carries that entry ready to paste and names `obor setup agents`,
// which adds it — or its entry names no kind.
func (c *Config) UnknownModel(name string) error {
	if e, ok := c.Data.Agents.Models.M[name]; ok && e.Kind == "" {
		return fmt.Errorf("%s names no kind: set agents.models%s.kind (%s)", Compact(name), key(name),
			strings.Join(c.kinds.Names(), ", "))
	}
	names := strings.Join(c.ModelNames(), ", ")
	if names == "" {
		names = "none"
	}
	err := fmt.Errorf("%s is not a model (agents.models: %s)", Compact(name), names)
	if i := slices.IndexFunc(UpgradeModels, func(e NamedModel) bool { return e.Name == name }); i >= 0 {
		err = fmt.Errorf("%v: add it to agents.models in .obor/config.json: %s (obor setup agents adds every former built-in name the config uses)",
			err, UpgradeModels[i].Member())
	}
	return err
}

// ModelDetail is ref as a table shows it after its name: " (<kind> <model id>, effort <effort>)", the model id left
// out when the kind picks its own and the effort when the ref names none; " (no such model)" for an unknown one.
func (c *Config) ModelDetail(ref string) string {
	name, effort := ParseModelRef(ref)
	m, ok := c.Model(name)
	if !ok {
		return " (no such model)"
	}
	detail := m.Kind
	if m.ID != "" {
		detail += " " + m.ID
	}
	if effort != "" {
		detail += ", effort " + effort
	}
	return " (" + detail + ")"
}

// CheckModelRef is why ref names no model of this config, or an effort its kind does not take (agents.Kinds.CheckEffort:
// a plugin kind's cached manifest); nil when it is fine.
func (c *Config) CheckModelRef(ref string) error { return c.checkModelRef(ref, nil) }

// checkModelRef is CheckModelRef with the kinds of described in place of the config's (obor config validate: the
// plugin kinds as their describe just answered, not their cached manifests).
func (c *Config) checkModelRef(ref string, described map[string]agents.Kind) error {
	name, effort := ParseModelRef(ref)
	m, ok := c.Model(name)
	if !ok {
		return c.UnknownModel(name)
	}
	if strings.HasSuffix(ref, ":") {
		return fmt.Errorf("%s names no effort after the colon", Compact(ref))
	}
	check := func() error { return c.kinds.CheckEffortArgs(m.Kind, m.Args, effort) }
	if k, ok := described[m.Kind]; ok {
		check = func() error { return agents.CheckEffortOf(k, m.Args, effort) }
	}
	if err := check(); err != nil {
		return fmt.Errorf("%s: %v", ref, err)
	}
	return nil
}

// Overrides are the project's override files of kind (agents.Start.Overrides): each file its kind names
// (agents.OverrideFiles) that the project's root has in agents.OverrideDir(kind), name → document. nil: the kind
// takes none, or the project has none. An error: a file there that cannot be read, or one that does not parse
// (agents.ParseOverride); it names the file, and no agent of the kind starts on it.
func (c *Config) Overrides(kind string) (map[string]string, error) {
	k, ok := c.Kind(kind)
	if !ok {
		return nil, nil
	}
	return c.OverridesOf(k)
}

// OverridesOf is Overrides of the kind k as given: the files k names, not the config's kind of its name (a start's
// kind, agents.AsNow).
func (c *Config) OverridesOf(k agents.Kind) (map[string]string, error) {
	if c.Root == "" {
		return nil, nil
	}
	var out map[string]string
	for _, name := range agents.OverrideFiles(k) {
		rel := filepath.Join(agents.OverrideDir(k.Name()), name)
		b, err := os.ReadFile(filepath.Join(c.Root, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			_, err = agents.ParseOverride(name, string(b))
		}
		if err != nil {
			return nil, fmt.Errorf("%s: does not parse: %v", rel, err)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = string(b)
	}
	return out, nil
}

// Launch is a start of this model as its kind builds it (agents.StartOf: one call of a plugin kind): in, the
// launch boundary's complete start (kit, effort, dir, write dirs, resume or first prompt, session, env, readers and
// writer), with the model's args, the settings and a lane's strictness. The args with effort: the kind sets it
// the settings as the kind takes them (Claude's --settings <settings>); a lane's agent starts strict (obor's MCP
// server and the project's approved .mcp.json servers, none of the user's). Every agent but a lead starts with
// {"remoteControlAtStartup":false,"autoMemoryEnabled":false} (only the lead on the phone, and only the lead keeps
// auto memory) and trimmed (agents.Start.TrimUser: the kind's default part of the user's own settings off); a
// lead's start keeps the user's own settings untrimmed. overrides
// (Config.Overrides; none: the kind's defaults alone) are handed to every start, and each kind decides whether a lead's
// start applies them (agy applies them; claude, codex and opencode apply them only when TrimUser). role is the agent's
// (roles.Role.Name; "": none). A kind obor has no bridge for: the args alone, no env. What the env does not set the
// start unsets (agents.Kinds.StartVars): an earlier OpenCode start's model never carries into another kind's.
// An error: the start cannot carry the effort, or a kind's start failed.
func (m Model) Launch(role string, overrides map[string]string, in agents.Start) (agents.Launch, error) {
	k, ok := m.kinds.Lookup(m.Kind)
	if !ok {
		return agents.Launch{Args: append([]string(nil), m.Args...)}, nil
	}
	return m.LaunchOf(k, role, overrides, in)
}

// LaunchOf is Launch as the kind k builds it, k the model's kind as the start has it (agents.AsNow), not the
// config's.
func (m Model) LaunchOf(k agents.Kind, role string, overrides map[string]string, in agents.Start) (agents.Launch, error) {
	in.Args, in.Settings, in.StrictMCP, in.TrimUser, in.Overrides = m.Args, nil, roles.LaneBound(role), role != "lead", overrides
	if in.TrimUser {
		in.Settings = map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}
	}
	return agents.StartOf(k, in)
}
