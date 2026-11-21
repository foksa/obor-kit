package setup

// The starter models: what obor init writes into a new project's agents.models and agents.difficulty. Only init
// reads these lists; a project's models are its config's (config.Config.Model).

import (
	"slices"

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

// initFallbackModel adds the fallback model's starter entry (StarterModels) when agents.models has no entry of that
// name: initRoleTypes falls a role back to it, so the role it writes resolves. An entry the config has is never
// replaced.
func (s *Setup) initFallbackModel() error {
	if _, ok := s.cfg.Data.Agents.Models.M[fallbackRoleModel]; ok {
		return nil
	}
	e, ok := starterModel(fallbackRoleModel)
	if !ok {
		return project.Failf("no starter entry for %s: the role falls back to it", fallbackRoleModel)
	}
	if err := s.editConfig(func(doc []byte) (config.Write, error) {
		return config.AddModels(doc, []config.NamedModel{e})
	}); err != nil {
		return err
	}
	return s.load()
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
