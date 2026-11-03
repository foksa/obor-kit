package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"obor/internal/agents"
	"obor/internal/app/port"
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

func (s *Setup) herdrPlugin(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	mine, err := s.pluginDir()
	if err != nil {
		return err
	}
	plugins, err := s.H.Plugins(ctx)
	if err != nil {
		return project.Failf("cannot read herdr's plugin list: %v", err)
	}
	if err := s.events(ctx, mine, plugins); err != nil {
		return err
	}
	s.say("OK herdr-plugin")
	return nil
}

// pluginDir is this kit's herdr-plugin: FAIL, after a WARN, when the kit has none.
func (s *Setup) pluginDir() (string, error) {
	mine := filepath.Join(s.Kit, "herdr-plugin")
	if fi, err := os.Stat(filepath.Join(mine, "herdr-plugin.toml")); err != nil || !fi.Mode().IsRegular() {
		s.say("WARN herdr-plugin: no herdr plugin in %s", mine)
		return "", project.Failf("herdr-plugin")
	}
	return mine, nil
}

// events: obor.events in herdr, linked from mine (<kit>/herdr-plugin) directly (herdr keeps the path resolved at link
// time, so a symlink in between buys nothing): not linked → link; another kit's copy → re-link. Then herdr's resume
// setting. plugins is herdr's plugin list, read once by the caller (T-613 #4: a list error is the step's error, never
// "linked"); the caller checked the kit has the plugin.
func (s *Setup) events(ctx context.Context, mine string, plugins []port.Plugin) error {
	defer s.resumeHint()
	if eventsLinked(mine, plugins) {
		s.say("herdr-plugin: obor.events linked (%s)", mine)
		return nil
	}
	root, linked := "", false
	for _, p := range plugins {
		if p.ID == "obor.events" {
			root, linked = p.Root, true
		}
	}
	if linked {
		if err := s.H.UnlinkPlugin(ctx, "obor.events"); err != nil {
			s.say("WARN herdr-plugin: %v", err)
			return project.Failf("herdr-plugin")
		}
	}
	if err := s.H.LinkPlugin(ctx, mine); err != nil {
		s.say("WARN herdr-plugin: %v", err)
		return project.Failf("herdr-plugin")
	}
	if linked {
		s.say("herdr-plugin: obor.events re-linked to %s (was %s; user-global: every project's restores run it)", mine, root)
	} else {
		s.say("herdr-plugin: obor.events linked to %s (user-global: obor restarts its panes after a herdr session restore)", mine)
	}
	return nil
}

// eventsLinked: herdr runs this kit's obor.events (mine is its herdr-plugin dir; a plugin with no root counts: herdr
// did not say where it is).
func eventsLinked(mine string, plugins []port.Plugin) bool {
	physical := project.Physical(mine)
	for _, p := range plugins {
		if p.ID == "obor.events" && (p.Root == "" || project.Physical(p.Root) == physical) {
			return true
		}
	}
	return false
}

var resumeRE = regexp.MustCompile(`^\s*resume_agents_on_restore\s*=\s*([^#\s]*)`)

// resumeHint: obor's startup hook restarts obor panes itself, so herdr must not resume agents on a restore (R-28 §1,
// T-159). Unless herdr's config.toml ($HERDR_CONFIG_PATH, else ~/.config/herdr/config.toml) sets `[session]
// resume_agents_on_restore = false`: the one line to add, saying it is user-global (RV-18 #3). Never edited.
func (s *Setup) resumeHint() {
	path := s.Env.Get("HERDR_CONFIG_PATH")
	if path == "" {
		path = filepath.Join(s.Env.Get("HOME"), ".config", "herdr", "config.toml")
	}
	value := ""
	if f, err := os.Open(path); err == nil {
		section := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") {
				head, _, _ := strings.Cut(line, "]")
				section = strings.TrimSpace(strings.TrimPrefix(head, "["))
			} else if m := resumeRE.FindStringSubmatch(line); m != nil && section == "session" {
				value = m[1]
			}
		}
		f.Close()
	}
	if value == "false" {
		return
	}
	line := "hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false under [session] in " +
		path + " (user-global: affects every herdr pane, not only obor's; those come back as shells)"
	if s.hints != nil {
		*s.hints = append(*s.hints, line)
		return
	}
	s.say("%s", line)
}

// agentsCmd is `obor setup agents [--remove]` (user-global, once per user): for every kind,
// what its bridge says to install (agents.Install): its plugin link (Plugin: e.g. OpenCode's
// <XDG_CONFIG_HOME|~/.config>/opencode/plugins/obor.js → <kit>/opencode/obor.js, T-306), then the obor entry of its
// user config (a Config registration, configMCP). A link to another kit's copy is re-linked; a file there that is no
// link, or links elsewhere, is left alone. --remove deletes only that link and that entry. Nothing else is touched; a
// kind with neither is said once.
func (s *Setup) agentsCmd(args []string) error {
	remove := len(args) == 1 && args[0] == "--remove"
	if len(args) > 0 && !remove {
		return errUsage
	}
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		in := k.Install(s.Kit, s.Env.Get)
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
	ours := exists && filepath.Base(target) == filepath.Base(mine) &&
		filepath.Base(filepath.Dir(target)) == filepath.Base(filepath.Dir(mine))
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
