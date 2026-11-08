package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/roles"
)

// The models (T-765): the config names a model with its effort, `<model>[:<effort>]` (a model ref), in difficulty
// levels, roles, agents and a spec's Model: line. A model is a built-in one (BuiltinModels) or models.<name>, which
// adds one or overrides a built-in; what starts it is its kind's base args (KindBaseArgs), its model id as the kind
// takes it (agents.ModelArgs), then kinds.<kind>.args. The effort is the kind's own flag (agents.Start.Effort, T-695).

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
// the lane's (T-810).
var KindBaseArgs = map[string][]string{
	"claude":   {"--permission-mode", "auto"},
	"codex":    {"-s", "workspace-write", "-a", "never", "--no-daemon"},
	"opencode": {"--auto", "--standalone"},
}

// KindEntry is kinds.<kind>: args appended to every start of the kind, and the prompt_extra its work jobs carry.
// Command makes it a plugin kind (T-1036): the executable answering obor's kind protocol (docs/agents/kind-protocol.md),
// a path (relative: to the project root; KindCommand).
type KindEntry struct {
	Args        []string `json:"args,omitzero"`
	PromptExtra string   `json:"prompt_extra,omitempty"`
	Command     string   `json:"command,omitempty"`
}

// KindCommand is kinds.<name>.command as a path ("": name is no plugin kind of this config).
func (c *Config) KindCommand(name string) string {
	cmd := c.Data.Kinds.M[name].Command
	if cmd == "" || filepath.IsAbs(cmd) {
		return cmd
	}
	return filepath.Join(c.Root, cmd)
}

// StateDir is the project's state dir (project.StateDir: config cannot import project).
func (c *Config) StateDir() string { return filepath.Join(c.Root, ".obor", "state") }

// registerPlugins makes this config's plugin kinds (kinds.<name>.command) the registry's (agents.SetPlugins), each
// with the manifest obor setup agents cached: no process runs (every load, hooks included). A binary without the
// plugin package registers none.
func (c *Config) registerPlugins() {
	if agents.Plugins == nil {
		return
	}
	var ks []agents.Kind
	for _, name := range c.Data.Kinds.Keys {
		if cmd := c.KindCommand(name); cmd != "" {
			ks = append(ks, agents.Plugins.Kind(name, cmd, c.StateDir()))
		}
	}
	agents.SetPlugins(ks)
}

// ModelEntry is models.<name>: a model the built-in table lacks, or a built-in one overridden. Kind may be left out
// for a built-in name (its built-in kind); Model "" starts the kind without a model (its own choice).
type ModelEntry struct {
	Kind  string `json:"kind,omitempty"`
	Model string `json:"model"`
}

// Model is a resolved model: what starts an agent of it. Name is the model's name in the config, ID the model id the
// kind starts ("": the kind's own choice), Args the full launch args (base, model, kinds.<kind>.args).
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
	if e, ok := c.Data.Models.M[name]; ok {
		m.ID = e.Model
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
	k := c.Data.Kinds.M[m.Kind]
	m.Args = append(args, k.Args...)
	m.PromptExtra = k.PromptExtra
	return m, true
}

// ModelNames are the models this config knows: the built-in ones in table order, then models' own new names.
func (c *Config) ModelNames() []string {
	var out []string
	for _, b := range BuiltinModels {
		out = append(out, b.Name)
	}
	for _, k := range c.Data.Models.Keys {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// CheckModelRef is why ref names no model of this config, or an effort its kind does not take (agents.CheckEffort:
// a plugin kind's cached manifest, T-1036); nil when it is fine.
func (c *Config) CheckModelRef(ref string) error { return c.checkModelRef(ref, nil) }

// checkModelRef is CheckModelRef with the kinds of described in place of the registry's (obor config validate: the
// plugin kinds as their describe just answered, not their cached manifests, T-1042).
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
		check = func() error { return agents.CheckEffortOf(k, effort) }
	}
	if err := check(); err != nil {
		return fmt.Errorf("%s: %v", ref, err)
	}
	return nil
}

// Launch is a start of this model as its kind builds it (agents.StartOf, T-1042: one call of a plugin kind): in, the
// launch boundary's complete start (kit, effort, dir, write dirs, resume or first prompt, session, env, readers and
// writer), with the model's args, the settings and a lane's strictness. The args with effort: the kind sets it
// (T-695); the settings as the kind takes them (Claude's --settings <settings>); a lane's agent starts strict (its obor
// MCP server alone, T-770). Every agent but a lead starts with {"remoteControlAtStartup":false,"autoMemoryEnabled":false}
// (only the lead on the phone, T-280, and only the lead keeps auto memory, T-471). role is the agent's
// (roles.Role.Name; "": none). A kind obor has no bridge for: the args alone, no env. What the env does not set the
// start unsets (agents.StartVars, T-812): an earlier OpenCode start's model never carries into another kind's (RV-83
// #4, T-707). An error: the start cannot carry the effort, or a plugin kind's start failed (T-1036).
func (m Model) Launch(role string, in agents.Start) (agents.Launch, error) {
	k, ok := agents.Lookup(m.Kind)
	if !ok {
		return agents.Launch{Args: append([]string(nil), m.Args...)}, nil
	}
	in.Args, in.Settings, in.StrictMCP = m.Args, nil, roles.LaneBound(role)
	if role != "lead" {
		in.Settings = map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}
	}
	return agents.StartOf(k, in)
}
