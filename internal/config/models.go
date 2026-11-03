package config

import (
	"fmt"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/agents/opencode"
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
var KindBaseArgs = map[string][]string{
	"claude":   {"--permission-mode", "auto"},
	"codex":    {"-s", "workspace-write", "-a", "never"},
	"opencode": {"--auto", "--standalone"},
}

// KindEntry is kinds.<kind>: args appended to every start of the kind, and the prompt_extra its work jobs carry.
type KindEntry struct {
	Args        []string `json:"args,omitzero"`
	PromptExtra string   `json:"prompt_extra,omitempty"`
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

// CheckModelRef is why ref names no model of this config, or an effort its kind does not take (agents.CheckEffort);
// nil when it is fine.
func (c *Config) CheckModelRef(ref string) error {
	name, effort := ParseModelRef(ref)
	m, ok := c.Model(name)
	if !ok {
		return fmt.Errorf("%s is not a model (models: %s)", Compact(name), strings.Join(c.ModelNames(), ", "))
	}
	if strings.HasSuffix(ref, ":") {
		return fmt.Errorf("%s names no effort after the colon", Compact(ref))
	}
	if err := agents.CheckEffortArgs(m.Kind, m.Args, effort); err != nil {
		return fmt.Errorf("%s: %v", ref, err)
	}
	return nil
}

// LaunchArgs are the model's launch args: args (with effort: the kind sets it, T-695), then the settings as its kind
// takes them (agents.Kind.Start: Claude's --settings <settings>; a kind obor has no bridge for: args alone). Every
// agent but a lead starts with {"remoteControlAtStartup":false,"autoMemoryEnabled":false} (only the lead on the phone,
// T-280, and only the lead keeps auto memory, T-471).
func (m Model) LaunchArgs(lead bool, effort string) []string {
	var settings map[string]any
	if !lead {
		settings = map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}
	}
	k, ok := agents.Lookup(m.Kind)
	if !ok {
		return append([]string(nil), m.Args...)
	}
	return k.Start(agents.Start{Args: m.Args, Settings: settings, Effort: effort})
}

// StartEnv is the environment a start of this model needs beyond the pane's (agents.Kind.StartEnv: OpenCode's model
// and effort, T-703), in a start's Effort, Read and Env (its Args are the model's). A start of any other kind (a kind
// obor has no bridge for too) unsets OpenCode's model variable: obor's own, so an earlier OpenCode start in the pane
// or the caller's env must not carry into it (RV-83 #4, T-707).
func (m Model) StartEnv(in agents.Start) (map[string]string, error) {
	k, ok := agents.Lookup(m.Kind)
	if !ok {
		return map[string]string{opencode.ModelVar: ""}, nil
	}
	in.Args = m.Args
	vars, err := k.StartEnv(in)
	if err != nil || m.Kind == opencode.Name {
		return vars, err
	}
	out := map[string]string{opencode.ModelVar: ""}
	for key, v := range vars {
		out[key] = v
	}
	return out, nil
}
