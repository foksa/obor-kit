package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	cur, _ := project.Git(s.root, "config", "--local", "--get", "core.hooksPath")
	cur = strings.TrimSpace(cur)
	chain, _ := project.Git(s.root, "config", "--local", "--get", "obor.hooksChain")
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
			if _, err := project.Git(s.root, "config", "--local", "core.hooksPath", chain); err != nil {
				return false, project.Failf("cannot set core.hooksPath in %s", s.root)
			}
			if _, err := project.Git(s.root, "config", "--local", "--unset", "obor.hooksChain"); err != nil {
				return false, project.Failf("cannot unset obor.hooksChain in %s", s.root)
			}
			s.say("OK githooks removed (core.hooksPath = %s, the project's own)", chain)
			return true, nil
		}
		if _, err := project.Git(s.root, "config", "--local", "--unset", "core.hooksPath"); err != nil {
			return false, project.Failf("cannot unset core.hooksPath in %s", s.root)
		}
		s.say("OK githooks removed (core.hooksPath unset)")
		return true, nil
	}
	// The project's own hooks dir is chained, rather than make it choose.
	own := cur != "" && cur != dir && cur != chainDir && !kitHooks(cur)
	if own {
		if _, err := project.Git(s.root, "config", "--local", "obor.hooksChain", cur); err != nil {
			return false, project.Failf("cannot set obor.hooksChain in %s", s.root)
		}
		chain = cur
	}
	// The full set only when there is something to chain: git runs one dir, and the small set ships pre-commit and
	// pre-push alone.
	target := dir
	if chain != "" || dotgitHooks(s.root) {
		target = chainDir
	}
	if cur == target {
		s.say("OK githooks: core.hooksPath already %s", target)
		return false, nil
	}
	if _, err := project.Git(s.root, "config", "--local", "core.hooksPath", target); err != nil {
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
func dotgitHooks(root string) bool {
	repo, err := project.Find(root)
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
func hooksLine(root string) string {
	cur, _ := project.Git(root, "config", "--local", "--get", "core.hooksPath")
	chain, _ := project.Git(root, "config", "--local", "--get", "obor.hooksChain")
	line := "core.hooksPath = " + strings.TrimSpace(cur)
	if chain = strings.TrimSpace(chain); chain != "" {
		line += ", chaining " + chain
	}
	return line
}

// agentsCmd is `obor setup agents` (once per user): in a project, each plugin kind its config names
// (kinds.<name>.command: the kit's OpenCode) is described first and its manifest cached in the state dir
// (plugin.Cache), what hooks and the guard read; then every kind's install is run (its answer is obor setup mcp's) and
// a failure is a FAIL. A kind installs nothing user-global, so each is said once.
func (s *Setup) agentsCmd(args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	if _, err := s.cachePlugins(); err != nil {
		return err
	}
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		if _, err := agents.InstallOf(k, s.Kit, s.Env.Get); err != nil {
			return project.Failf("agents %s: %v", name, err)
		}
		s.say("agents %s: nothing to install", name)
	}
	return nil
}

// cachePlugins describes every plugin kind of the project around Dir (none outside a project, or with no config; a
// config that fails to load is its own error) and caches its manifest, then reloads the config so the
// registry's plugin kinds carry it; cached are their names (obor setup agents; obor init's kinds step). A kit
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
	if _, err := config.Load(s.root); err != nil {
		return nil, project.Failf("%v", err)
	}
	return cached, nil
}
