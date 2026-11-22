package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/project"
)

// kitHooks: path is the githooks dir of an obor kit (a checkout whose plugin.json names obor, or one that is gone: its
// path then still names the kit), or that dir's chain/ set, which `obor setup githooks` may re-point.
func kitHooks(path string) bool {
	if filepath.Base(path) == "chain" {
		path = filepath.Dir(path)
	}
	if filepath.Base(path) != "githooks" {
		return false
	}
	kit := filepath.Dir(path)
	b, err := os.ReadFile(filepath.Join(kit, ".claude-plugin", "plugin.json"))
	if err != nil {
		_, serr := os.Stat(kit)
		return os.IsNotExist(serr) && strings.Contains(kit, "obor")
	}
	var m struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(b, &m) == nil && m.Name == "obor"
}

// githooks points core.hooksPath at <kit>/githooks, or at its chain/ set when there is something to chain (a
// recorded obor.hooksChain, or a hook in .git/hooks that the small set lacks). A project's own hooks dir (the value it
// already had) is not refused but chained: recorded as obor.hooksChain in the repo config and run by the hook
// before obor's rule, for every hook. --remove puts it back and unsets obor.hooksChain.
func (s *Setup) githooks(args []string) error {
	_, err := s.githooksStep(args)
	return err
}

// planHooks is what `obor setup githooks` decides from the values git holds: cur is core.hooksPath, chain is
// obor.hooksChain ("" when unset), dir and chainDir are the kit's two sets, and dotgit says whether .git/hooks holds a
// hook the small set lacks (asked only when nothing is recorded). own: cur is the project's own hooks dir, which is
// chained rather than made to choose (the returned chain is then cur). target is the set core.hooksPath gets: the full
// set only when there is something to chain, since git runs one dir and the small set ships pre-commit and pre-push
// alone.
func planHooks(dir, chainDir, cur, chain string, dotgit func() bool) (own bool, newChain, target string) {
	own = cur != "" && cur != dir && cur != chainDir && !kitHooks(cur)
	if own {
		chain = cur
	}
	if chain != "" || dotgit() {
		return own, chain, chainDir
	}
	return own, chain, dir
}

// githooksStep is githooks; changed says core.hooksPath moved (obor init's step line).
func (s *Setup) githooksStep(args []string) (changed bool, err error) {
	remove := len(args) == 1 && args[0] == "--remove"
	if len(args) > 0 && !remove {
		return false, errUsage
	}
	if err := s.findRoot(); err != nil {
		return false, err
	}
	dir := filepath.Join(s.Kit, "githooks")
	chainDir := filepath.Join(dir, "chain")
	cur, _ := s.git().Git(s.root, "config", "--local", "--get", "core.hooksPath")
	cur = strings.TrimSpace(cur)
	chain, _ := s.git().Git(s.root, "config", "--local", "--get", "obor.hooksChain")
	chain = strings.TrimSpace(chain)
	if remove {
		switch {
		case cur != "" && cur != dir && cur != chainDir:
			return false, project.Failf("core.hooksPath is %s (not obor's %s): left alone", cur, dir)
		case cur == "" && chain == "":
			s.say("OK githooks: core.hooksPath not set")
			return false, nil
		}
		if chain != "" {
			if _, err := s.git().Git(s.root, "config", "--local", "core.hooksPath", chain); err != nil {
				return false, project.Failf("cannot set core.hooksPath in %s", s.root)
			}
			if _, err := s.git().Git(s.root, "config", "--local", "--unset", "obor.hooksChain"); err != nil {
				return false, project.Failf("cannot unset obor.hooksChain in %s", s.root)
			}
			s.say("OK githooks removed (core.hooksPath = %s, the project's own)", chain)
			return true, nil
		}
		if _, err := s.git().Git(s.root, "config", "--local", "--unset", "core.hooksPath"); err != nil {
			return false, project.Failf("cannot unset core.hooksPath in %s", s.root)
		}
		s.say("OK githooks removed (core.hooksPath unset)")
		return true, nil
	}
	own, chain, target := planHooks(dir, chainDir, cur, chain, func() bool { return s.dotgitHooks(s.root) })
	if own {
		if _, err := s.git().Git(s.root, "config", "--local", "obor.hooksChain", cur); err != nil {
			return false, project.Failf("cannot set obor.hooksChain in %s", s.root)
		}
	}
	if cur == target {
		s.say("OK githooks: core.hooksPath already %s", target)
		return false, nil
	}
	if _, err := s.git().Git(s.root, "config", "--local", "core.hooksPath", target); err != nil {
		return false, project.Failf("cannot set core.hooksPath in %s", s.root)
	}
	switch {
	case own:
		s.say("OK githooks: core.hooksPath = %s, chaining %s", target, cur)
	case cur != "" && kitHooks(cur):
		s.say("OK githooks: core.hooksPath = %s (was %s, an obor kit's)", target, cur)
	case chain != "":
		s.say("OK githooks: core.hooksPath = %s, chaining %s", target, chain)
	default:
		s.say("OK githooks: core.hooksPath = %s", target)
	}
	return true, nil
}

// dotgitHooks: .git/hooks holds an executable hook (not a *.sample) that the small githooks/ set lacks, other than
// pre-commit and pre-push. git never runs .git/hooks once core.hooksPath is set, so only githooks/chain/ can
// run it.
func (s *Setup) dotgitHooks(root string) bool {
	repo, err := s.git().Find(root)
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(repo.Common, "hooks"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".sample") || name == "pre-commit" || name == "pre-push" {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// hooksLine is obor init's githooks step: core.hooksPath, and the chain when one is recorded.
func (s *Setup) hooksLine(root string) string {
	cur, _ := s.git().Git(root, "config", "--local", "--get", "core.hooksPath")
	chain, _ := s.git().Git(root, "config", "--local", "--get", "obor.hooksChain")
	line := "core.hooksPath = " + strings.TrimSpace(cur)
	if chain = strings.TrimSpace(chain); chain != "" {
		line += ", chaining " + chain
	}
	return line
}

// agentsCmd is `obor setup agents` (once per user, and after an upgrade): in a project, the agents.models entries its
// levels and roles name without one are added and its former built-in entries with no kind get theirs
// (upgradeModels: decided from config.json alone), then each plugin kind its config names
// (kinds.<name>.command: the kit's OpenCode and agy) is described and its manifest cached in the state dir
// (plugin.Cache), what hooks and the guard read; then every kind's install is run (its answer is obor setup mcp's) and
// a failure is a FAIL. A kind installs nothing user-global; one that registers through a project file (agy's
// .agents/mcp_config.json, and its hooks' .agents/hooks.json) has it written there when the config has a model of that
// kind (projectInstall).
func (s *Setup) agentsCmd(args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	if cfg, err := s.projectConfig(); err != nil {
		return err
	} else if cfg != nil {
		s.cfg = cfg
		if err := s.upgradeModels(); err != nil {
			return err
		}
	}
	if _, err := s.cachePlugins(); err != nil {
		return err
	}
	for _, name := range s.cfg.Kinds().Names() {
		k, _ := s.cfg.Kind(name)
		in, err := agents.InstallOf(k, s.Kit, s.root, s.Env.Get)
		if err != nil {
			return project.Failf("agents %s: %v", name, err)
		}
		var files []agents.MCPRegistration
		for _, m := range []agents.MCPRegistration{in.MCP, in.Hooks} {
			if m.Where == agents.Project {
				files = append(files, m)
			}
		}
		if len(files) == 0 {
			s.say("agents %s: nothing to install", name)
		}
		for _, m := range files {
			if err := s.projectInstall(name, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// projectInstall writes kind name's registration m (where project: its MCP server's or its hooks') into the project:
// when the config has an agents.models entry of the kind, m.Entry at m.Key of m.Path (the file's other entries kept; an
// absent file is made), and the file's path, relative to the root, into rules.protected_files (no worker edits its own
// wiring); both uncommitted, for the lead to commit. Outside a project, or with no model of the kind, nothing is written.
func (s *Setup) projectInstall(name string, m agents.MCPRegistration) error {
	if s.root == "" || !filepath.IsAbs(m.Path) {
		s.say("agents %s: registered by each project's %s (obor setup agents in the project writes it)", name, m.Path)
		return nil
	}
	rel := s.rel(m.Path)
	if !s.hasModelOf(name) {
		s.say("agents %s: not used (no agents.models entry of kind %s): %s not written", name, name, rel)
		return nil
	}
	doc, err := readConfig(m.Path)
	if err != nil {
		return project.Failf("agents %s: %s: %v", name, rel, err)
	}
	switch state, _, err := registration(m); {
	case err != nil:
		return project.Failf("agents %s: %s: %v", name, rel, err)
	case state == entryPinned:
		s.say("OK agents %s: %s in %s", name, strings.Join(m.Key, "."), rel)
	default:
		out, err := config.EditJSON(doc, m.Key, m.Entry)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(m.Path), 0o755)
		}
		if err == nil {
			err = writeJSON(m.Path, out)
		}
		if err != nil {
			return project.Failf("agents %s: cannot write %s: %v", name, rel, err)
		}
		s.say("agents %s: %s written in %s (uncommitted: commit it)", name, strings.Join(m.Key, "."), rel)
	}
	shared, err := config.LoadMain(s.root)
	if err != nil {
		return project.Failf("%v", err)
	}
	if slices.Contains(shared.ProtectedFiles(), rel) {
		return nil
	}
	if err := s.editConfig(func(doc []byte) (config.Write, error) { return config.AddProtectedFile(doc, rel) },
		": commit it"); err != nil {
		return err
	}
	return s.load()
}

// hasModelOf: the config has an agents.models entry of kind name.
func (s *Setup) hasModelOf(name string) bool {
	for _, n := range s.cfg.ModelNames() {
		if m, ok := s.cfg.Model(n); ok && m.Kind == name {
			return true
		}
	}
	return false
}

// cachePlugins describes every plugin kind of the project around Dir (none outside a project, or with no config; a
// config that fails to load is its own error) and caches its manifest, then reloads the config (Setup.cfg) so its
// plugin kinds carry it; cached are their names (obor setup agents; obor init's kinds step). A kit
// kind the config names no plugin for (config.KitKinds: the kit's binary is the default) is cached as that
// default and the config is left as it is (a value obor derives needs no edit).
func (s *Setup) cachePlugins() (cached []string, err error) {
	cfg, err := s.projectConfig()
	if err != nil || cfg == nil {
		return nil, err
	}
	for _, name := range cfg.PluginKinds() {
		cmd := cfg.KindCommand(name)
		if _, err := plugin.Cache(name, cmd, cfg.StateDir()); err != nil {
			return nil, project.Failf("agents %s: %v", name, err)
		}
		s.say("OK agents %s: manifest of %s cached (%s)", name, cmd, s.rel(plugin.CacheFile(cfg.StateDir(), name)))
		cached = append(cached, name)
	}
	if s.cfg, err = config.Load(s.root); err != nil {
		return nil, project.Failf("%v", err)
	}
	return cached, nil
}
