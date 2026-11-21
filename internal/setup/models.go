package setup

// The starter models: what obor init writes into a new project's agents.models and agents.difficulty. Only init
// reads these lists; a project's models are its config's (config.Config.Model).

import (
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
)

// StarterModels are the models obor init writes for each kind whose CLI is on PATH, in this order.
var StarterModels = []config.NamedModel{
	{Name: "opus", Kind: "claude", ID: "opus"},
	{Name: "sonnet", Kind: "claude", ID: "sonnet"},
	{Name: "haiku", Kind: "claude", ID: "haiku"},
	{Name: "fable", Kind: "claude", ID: "fable"},
	{Name: "deepseek", Kind: "opencode", ID: "opencode-go/deepseek-v4.1-flash"},
	{Name: "terra", Kind: "codex", ID: "gpt-6.1-terra"},
	{Name: "sol", Kind: "codex", ID: "gpt-6.1-sol"},
	{Name: "luna", Kind: "codex", ID: "gpt-6-luna"},
}

// StarterDifficulty is each level's model refs as obor init writes them, before initDifficulty keeps only the Claude
// kind's: hard wants opus at effort high, simple the cheap models.
var StarterDifficulty = map[string][]string{
	"hard":   {"opus:high"},
	"medium": {"sonnet", "deepseek"},
	"simple": {"deepseek", "sonnet", "haiku"},
}

// initModels writes agents.models from StarterModels: the entries of each kind whose CLI is on PATH. A config with
// agents.models already is kept.
func (s *Setup) initModels() error {
	if len(s.cfg.ModelNames()) > 0 {
		return nil
	}
	var add []config.NamedModel
	for _, e := range StarterModels {
		if _, err := project.LookPath(s.Env.Get("PATH"), e.Kind); err == nil {
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := s.editConfig(func(doc []byte) (config.Write, error) { return config.AddModels(doc, add) }); err != nil {
		return err
	}
	return s.load()
}

// upgradeModels is obor setup agents' config step: the agents.models entries the shared config's levels and roles
// name without one (config.Config.UpgradeEntries) are added and its former built-in entries with no kind get their
// former kind (config.Config.KindFills), both in .obor/config.json uncommitted; nothing else is written, and a config
// with neither is left as it is. The step decides from config.json alone (.obor/local.json left out): a per-machine
// reference adds no entry to the committed file, and a local entry does not suppress one it needs.
func (s *Setup) upgradeModels() error {
	shared, err := config.LoadMain(s.root)
	if err != nil {
		return project.Failf("%v", err)
	}
	fill, add := shared.KindFills(), shared.UpgradeEntries()
	if len(fill) == 0 && len(add) == 0 {
		return nil
	}
	if err := s.editConfig(func(doc []byte) (config.Write, error) { return config.MigrateModels(doc, fill, add) },
		": commit it"); err != nil {
		return err
	}
	return s.load()
}

// fallbackStarter is the starter entry (StarterModels) init adds when no agents.models entry is of the Claude kind: the
// model a role falls back to then.
const fallbackStarter = "opus"

// fillModelKinds gives the former built-in entries of .obor/config.json that have no kind their kind
// (config.Config.KindFills, as `obor setup agents` does) and returns the shared config as it is then: init's role
// step decides from it.
func (s *Setup) fillModelKinds() (*config.Config, error) {
	shared, err := config.LoadMain(s.root)
	if err != nil {
		return nil, project.Failf("%v", err)
	}
	fill := shared.KindFills()
	if len(fill) == 0 {
		return shared, nil
	}
	if err := s.editConfig(func(doc []byte) (config.Write, error) { return config.MigrateModels(doc, fill, nil) }); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if shared, err = config.LoadMain(s.root); err != nil {
		return nil, project.Failf("%v", err)
	}
	return shared, nil
}

// fallbackModel is the Claude model the roles fall back to, from shared (.obor/config.json): its first agents.models
// entry of the kind init requires (agents.Default), in config order. With none, the starter entry fallbackStarter,
// returned as add for the caller to write; a config that has an entry of that name of another kind fails naming
// roles and the entry to add under a name of its own.
func fallbackModel(shared *config.Config, roles []string) (name string, add *config.NamedModel, err error) {
	for _, n := range shared.ModelNames() {
		if m, ok := shared.Model(n); ok && m.Kind == agents.Default {
			return n, nil, nil
		}
	}
	e, ok := starterModel(fallbackStarter)
	if !ok {
		return "", nil, project.Failf("no starter entry for %s: a role falls back to it", fallbackStarter)
	}
	if _, taken := shared.Data.Agents.Models.M[e.Name]; taken {
		return "", nil, project.Failf("no %s model in agents.models for %s (init falls a role back to one) and %s names "+
			"another kind: add one to .obor/config.json, %s", agents.Default, strings.Join(roles, ", "), e.Name,
			config.NamedModel{Name: "<name>", Kind: e.Kind, ID: e.ID}.Member())
	}
	return e.Name, &e, nil
}

// starterModel is the StarterModels entry of name (ok false: none).
func starterModel(name string) (config.NamedModel, bool) {
	i := slices.IndexFunc(StarterModels, func(e config.NamedModel) bool { return e.Name == name })
	if i < 0 {
		return config.NamedModel{}, false
	}
	return StarterModels[i], true
}

// starterLevels is StarterDifficulty with only the Claude kind's models (agents.Default, which init requires on
// PATH), each level that keeps one.
func starterLevels(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	for level, listed := range StarterDifficulty {
		for _, ref := range listed {
			typ, _ := config.ParseModelRef(ref)
			if m, ok := cfg.Model(typ); ok && m.Kind == agents.Default {
				out[level] = append(out[level], ref)
			}
		}
	}
	return out
}
