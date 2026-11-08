package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
)

// kitHooks: path is the githooks dir of an obor kit (a checkout whose plugin.json names obor, or one that is gone: its
// path then still names the kit), or that dir's chain/ set (T-623), which `obor setup githooks` may re-point.
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

// githooks points core.hooksPath at <kit>/githooks, or at its chain/ set when there is something to chain (T-623: a
// recorded obor.hooksChain, or a hook in .git/hooks that the small set lacks). A project's own hooks dir (the value it
// already had) is not refused but chained (T-620): recorded as obor.hooksChain in the repo config and run by the hook
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
	// The project's own hooks dir is chained, rather than make it choose (T-620).
	own := cur != "" && cur != dir && cur != chainDir && !kitHooks(cur)
	if own {
		if _, err := project.Git(s.root, "config", "--local", "obor.hooksChain", cur); err != nil {
			return false, project.Failf("cannot set obor.hooksChain in %s", s.root)
		}
		chain = cur
	}
	// The full set only when there is something to chain: git runs one dir, and the small set ships pre-commit and
	// pre-push alone (T-623).
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
// pre-commit and pre-push (T-623). git never runs .git/hooks once core.hooksPath is set, so only githooks/chain/ can
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

// herdrPlugin is `obor setup herdr-plugin`: the terminal's integration (port.TerminalSetup.Integrate; herdr: the
// obor.events plugin linked from the kit), its lines, then its hint.
func (s *Setup) herdrPlugin(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	if _, err := s.integrate(ctx); err != nil {
		return err
	}
	s.say("OK herdr-plugin")
	return nil
}

// integrate installs the terminal's integration (user-global, once per user) and says its lines; its hint goes to
// obor init's summary when init runs, else it is said after them, also when the install failed.
func (s *Setup) integrate(ctx context.Context) (port.Integration, error) {
	in, err := s.T.Integrate(ctx, s.Kit, func(line string) { s.say("%s", line) })
	switch {
	case in.Hint == "":
	case s.hints != nil:
		*s.hints = append(*s.hints, in.Hint)
	default:
		s.say("%s", in.Hint)
	}
	return in, err
}

// agentsCmd is `obor setup agents [--remove]` (user-global, once per user): for every kind, what its bridge says to
// install (agents.Install): its plugin link (Plugin), then the obor entry of its user config (a Config registration,
// configMCP). A link to another kit's copy is re-linked; a file there that is no link, or links elsewhere, is left
// alone. --remove deletes only that link and that entry. Nothing else is touched; a kind with neither is said once.
// In a project, each plugin kind its config names (kinds.<name>.command, T-1036) is described first and its manifest
// cached in the state dir (plugin.Cache), what hooks and the guard read; a plugin kind's install failing is a FAIL.
func (s *Setup) agentsCmd(args []string) error {
	remove := len(args) == 1 && args[0] == "--remove"
	if len(args) > 0 && !remove {
		return errUsage
	}
	if err := s.cachePlugins(); err != nil {
		return err
	}
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		in, err := agents.InstallOf(k, s.Kit, s.Env.Get)
		if err != nil {
			return project.Failf("agents %s: %v", name, err)
		}
		if in.Plugin == nil && in.MCP.Where != agents.Config {
			s.say("agents %s: nothing to install", name)
			continue
		}
		if in.Plugin != nil {
			if err := s.pluginLink(name, *in.Plugin, remove); err != nil {
				return err
			}
		}
		if in.MCP.Where == agents.Config {
			if err := s.configMCP(name, in.MCP, remove); err != nil {
				return err
			}
		}
	}
	return nil
}

// cachePlugins describes every plugin kind of the project around Dir (none outside a project, or with no config) and
// caches its manifest, then reloads the config so the registry's plugin kinds carry it.
func (s *Setup) cachePlugins() error {
	if s.findRoot() != nil {
		return nil
	}
	cfg, err := config.Load(s.root)
	if err != nil {
		return nil
	}
	cached := false
	for _, name := range cfg.Data.Kinds.Keys {
		cmd := cfg.KindCommand(name)
		if cmd == "" {
			continue
		}
		if _, err := plugin.Cache(name, cmd, cfg.StateDir()); err != nil {
			return project.Failf("agents %s: %v", name, err)
		}
		s.say("OK agents %s: manifest of %s cached (%s)", name, cmd, s.rel(plugin.CacheFile(cfg.StateDir(), name)))
		cached = true
	}
	if cached {
		if _, err := config.Load(s.root); err != nil {
			return project.Failf("%v", err)
		}
	}
	return nil
}

// kitCopy: target is another obor kit's copy of mine (a file of the same name in a dir of the same name).
func kitCopy(target, mine string) bool {
	return filepath.Base(target) == filepath.Base(mine) && filepath.Base(filepath.Dir(target)) == filepath.Base(filepath.Dir(mine))
}

// pluginLink links kind's l.To to l.From (remove: deletes the link); a link to a file of the same name in a dir of the
// same name (another kit's copy) is ours.
func (s *Setup) pluginLink(kind string, l agents.Link, remove bool) error {
	mine, link := l.From, l.To
	if fi, err := os.Stat(mine); !remove && (err != nil || !fi.Mode().IsRegular()) {
		return project.Failf("agents %s: no %s", kind, mine)
	}
	target, err := os.Readlink(link)
	exists := err == nil
	if _, lerr := os.Lstat(link); lerr == nil && !exists {
		return project.Failf("agents %s: %s is not a link: left alone", kind, link)
	}
	ours := exists && kitCopy(target, mine)
	switch {
	case exists && !ours:
		return project.Failf("agents %s: %s links to %s (not an obor kit's %s): left alone", kind, link, target,
			filepath.Join(filepath.Base(filepath.Dir(mine)), filepath.Base(mine)))
	case remove && !exists:
		s.say("OK agents %s: %s not linked", kind, link)
		return nil
	case remove:
		if err := os.Remove(link); err != nil {
			return project.Failf("agents %s: %v", kind, err)
		}
		s.say("OK agents %s: plugin removed (%s; restart %s)", kind, link, kind)
		return nil
	case target == mine:
		s.say("OK agents %s: %s already links to %s", kind, link, mine)
		return nil
	case exists:
		if err := os.Remove(link); err != nil {
			return project.Failf("agents %s: %v", kind, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return project.Failf("agents %s: %v", kind, err)
	}
	if err := os.Symlink(mine, link); err != nil {
		return project.Failf("agents %s: %v", kind, err)
	}
	if exists {
		s.say("agents %s: re-linked %s to %s (was %s)", kind, link, mine, target)
	}
	s.say("OK agents %s: %s → %s (user-global; restart %s to load it)", kind, link, mine, kind)
	return nil
}
