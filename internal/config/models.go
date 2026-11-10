package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/roles"
)

// The models: the config names a model with its effort, `<model>[:<effort>]` (a model ref), in difficulty
// levels, roles, agents and a spec's Model: line. A model is a built-in one (BuiltinModels) or models.<name>, which
// adds one or overrides a built-in; what starts it is its kind's base args (KindBaseArgs), its model id as the kind
// takes it (agents.ModelArgs), then kinds.<kind>.args. The effort is the kind's own flag (agents.Start.Effort).

// BuiltinModel is one entry of the built-in model table: its name, its kind and the model id the kind starts it with.
type BuiltinModel struct{ Name, Kind, ID string }

// BuiltinModels is the built-in model table, in the order obor setup show and the errors list them.
var BuiltinModels = []BuiltinModel{
	{"opus", "claude", "opus"},
	{"sonnet", "claude", "sonnet"},
	{"haiku", "claude", "haiku"},
	{"fable", "claude", "fable"},
	{"deepseek", "opencode", "opencode-go/deepseek-v4.1-flash"},
	{"terra", "codex", "gpt-6.1-terra"},
	{"sol", "codex", "gpt-6.1-sol"},
	{"luna", "codex", "gpt-6-luna"},
}

// KindBaseArgs are the args every start of a kind begins with (kinds.<kind>.args are appended after the model).
// Codex's --no-daemon: its shared background server would give the MCP server and hooks the daemon's env and cwd, not
// the lane's.
var KindBaseArgs = map[string][]string{
	"claude":   {"--permission-mode", "auto"},
	"codex":    {"-s", "workspace-write", "-a", "never", "--no-daemon"},
	"opencode": {"--auto", "--standalone"},
}

// KindEntry is agents.kinds.<kind>: Args are extra args for every start of that CLI, JobNote the text added to every
// work job an agent of that kind gets. Plugin makes it a plugin kind: the executable answering obor's kind
// protocol (docs/agents/kind-protocol.md), a path (relative: to the project root; KindCommand).
type KindEntry struct {
	Args    []string `json:"args,omitzero"`
	JobNote string   `json:"job_note,omitempty"`
	Plugin  string   `json:"plugin,omitempty"`
}

// Kind is agents.kinds.<name> (the zero entry when the config does not name it): its args and its work jobs' note.
func (c *Config) Kind(name string) KindEntry { return c.Data.Agents.Kinds.M[name] }

// KitKinds are the plugin kinds the kit ships, by name: the binary each runs as, beside obor's own (KitKind).
// A config naming no agents.kinds.<name>.plugin for one runs the kit's binary; setting the line overrides it.
var KitKinds = map[string]string{"opencode": "obor-kind-opencode"}

// KindCommand is agents.kinds.<name>.plugin as a path ("": name is no plugin kind of this config): the name of a
// binary the kit ships (a KitKinds value) is that binary (KitKind), any other relative path relative to the project
// root. A kit kind with no plugin is the kit's (KitKinds).
func (c *Config) KindCommand(name string) string {
	cmd := c.Data.Agents.Kinds.M[name].Plugin
	if cmd == "" {
		cmd = KitKinds[name]
	}
	switch {
	case cmd == "" || filepath.IsAbs(cmd):
		return cmd
	case slices.Contains(slices.Collect(maps.Values(KitKinds)), cmd):
		return KitKind(cmd)
	}
	return filepath.Join(c.Root, cmd)
}

// KitKind is the path of the kind binary name the kit ships beside this obor's executable: dist/obor's
// dist/<name>, a release binary obor-<v>-<os>-<arch>'s <name>-<v>-<os>-<arch> in the same directory. An executable obor
// cannot find leaves name as it is (the kind's describe then fails naming it).
func KitKind(name string) string {
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	base := filepath.Base(exe)
	suffix := ""
	if strings.HasPrefix(base, "obor-") {
		suffix = strings.TrimPrefix(base, "obor")
	}
	return filepath.Join(filepath.Dir(exe), name+suffix)
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

// registerPlugins makes this config's plugin kinds (PluginKinds) the registry's (agents.SetPlugins), each
// with the manifest obor setup agents cached: no process runs (every load, hooks included). A binary without the
// plugin package registers none.
func (c *Config) registerPlugins() {
	if agents.Plugins == nil {
		return
	}
	var ks []agents.Kind
	for _, name := range c.PluginKinds() {
		ks = append(ks, agents.Plugins.Kind(name, c.KindCommand(name), c.StateDir()))
	}
	agents.SetPlugins(ks)
}

// ModelEntry is agents.models.<name>: a model the built-in table lacks, or a built-in one overridden. Kind may be left
// out for a built-in name (its built-in kind); ID is the model id the kind takes, "" starts the kind without a model
// (its own choice).
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

// BuiltinModelNamed is the built-in model of name; ok false: none.
func BuiltinModelNamed(name string) (BuiltinModel, bool) {
	i := slices.IndexFunc(BuiltinModels, func(b BuiltinModel) bool { return b.Name == name })
	if i < 0 {
		return BuiltinModel{}, false
	}
	return BuiltinModels[i], true
}

// Model resolves name: models.<name> (over the built-in of that name), else the built-in; ok false: neither.
func (c *Config) Model(name string) (Model, bool) {
	b, isBuiltin := BuiltinModelNamed(name)
	m := Model{Name: name, Kind: b.Kind, ID: b.ID}
	if e, ok := c.Data.Agents.Models.M[name]; ok {
		m.ID = e.ID
		if e.Kind != "" {
			m.Kind = e.Kind
		}
	} else if !isBuiltin {
		return Model{}, false
	}
	args := append([]string(nil), KindBaseArgs[m.Kind]...)
	if m.ID != "" {
		if with, err := agents.ModelArgs(m.Kind, args, m.ID); err == nil {
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

// ModelNames are the models this config knows: the built-in ones in table order, then models' own new names.
func (c *Config) ModelNames() []string {
	var out []string
	for _, b := range BuiltinModels {
		out = append(out, b.Name)
	}
	for _, k := range c.Data.Agents.Models.Keys {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// CheckModelRef is why ref names no model of this config, or an effort its kind does not take (agents.CheckEffort:
// a plugin kind's cached manifest); nil when it is fine.
func (c *Config) CheckModelRef(ref string) error { return c.checkModelRef(ref, nil) }

// checkModelRef is CheckModelRef with the kinds of described in place of the registry's (obor config validate: the
// plugin kinds as their describe just answered, not their cached manifests).
func (c *Config) checkModelRef(ref string, described map[string]agents.Kind) error {
	name, effort := ParseModelRef(ref)
	m, ok := c.Model(name)
	if !ok {
		return fmt.Errorf("%s is not a model (models: %s)", Compact(name), strings.Join(c.ModelNames(), ", "))
	}
	if strings.HasSuffix(ref, ":") {
		return fmt.Errorf("%s names no effort after the colon", Compact(ref))
	}
	check := func() error { return agents.CheckEffortArgs(m.Kind, m.Args, effort) }
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
	k, ok := agents.Lookup(kind)
	if !ok {
		return nil, nil
	}
	return c.OverridesOf(k)
}

// OverridesOf is Overrides of the kind k as given: the files k names, not the registry's kind of its name (a start's
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
// the settings as the kind takes them (Claude's --settings <settings>); a lane's agent starts strict (its obor
// MCP server alone). Every agent but a lead starts with {"remoteControlAtStartup":false,"autoMemoryEnabled":false}
// (only the lead on the phone, and only the lead keeps auto memory), trimmed (agents.Start.TrimUser: the kind's
// default part of the user's own settings off) and with overrides, the project's override files of the kind
// (Config.Overrides; none: the kind's defaults alone); a lead's start carries neither (the user's settings as they
// are). role is the agent's
// (roles.Role.Name; "": none). A kind obor has no bridge for: the args alone, no env. What the env does not set the
// start unsets (agents.StartVars): an earlier OpenCode start's model never carries into another kind's.
// An error: the start cannot carry the effort, or a kind's start failed.
func (m Model) Launch(role string, overrides map[string]string, in agents.Start) (agents.Launch, error) {
	k, ok := agents.Lookup(m.Kind)
	if !ok {
		return agents.Launch{Args: append([]string(nil), m.Args...)}, nil
	}
	return m.LaunchOf(k, role, overrides, in)
}

// LaunchOf is Launch as the kind k builds it, k the model's kind as the start has it (agents.AsNow), not the
// registry's.
func (m Model) LaunchOf(k agents.Kind, role string, overrides map[string]string, in agents.Start) (agents.Launch, error) {
	in.Args, in.Settings, in.StrictMCP, in.TrimUser, in.Overrides = m.Args, nil, roles.LaneBound(role), role != "lead", nil
	if in.TrimUser {
		in.Settings = map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}
		in.Overrides = overrides
	}
	return agents.StartOf(k, in)
}
