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
	"obor/internal/project"
)

// herdKitHooks: path is the githooks dir of a herd or obor kit (a checkout whose plugin.json names herd or obor, or
// one that is gone: its path then still names the kit), which `obor setup githooks` may re-point.
func herdKitHooks(path string) bool {
	if filepath.Base(path) != "githooks" {
		return false
	}
	kit := filepath.Dir(path)
	b, err := os.ReadFile(filepath.Join(kit, ".claude-plugin", "plugin.json"))
	if err != nil {
		_, serr := os.Stat(kit)
		return os.IsNotExist(serr) && (strings.Contains(kit, "herd") || strings.Contains(kit, "obor"))
	}
	var m struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(b, &m) == nil && (m.Name == "herd" || m.Name == "obor")
}

func (s *Setup) githooks(args []string) error {
	remove := len(args) == 1 && args[0] == "--remove"
	if len(args) > 0 && !remove {
		return errUsage
	}
	if err := s.findRoot(); err != nil {
		return err
	}
	dir := filepath.Join(s.Kit, "githooks")
	cur, _ := project.Git(s.root, "config", "--local", "--get", "core.hooksPath")
	cur = strings.TrimSpace(cur)
	switch {
	case remove && cur == "":
		s.say("OK githooks: core.hooksPath not set")
	case remove && cur != dir:
		return project.Failf("core.hooksPath is %s (not obor's %s): left alone", cur, dir)
	case remove:
		if _, err := project.Git(s.root, "config", "--local", "--unset", "core.hooksPath"); err != nil {
			return project.Failf("cannot unset core.hooksPath in %s", s.root)
		}
		s.say("OK githooks removed (core.hooksPath unset)")
	case cur == dir:
		s.say("OK githooks: core.hooksPath already %s", dir)
	case cur != "" && herdKitHooks(cur):
		if _, err := project.Git(s.root, "config", "--local", "core.hooksPath", dir); err != nil {
			return project.Failf("cannot set core.hooksPath in %s", s.root)
		}
		s.say("OK githooks: core.hooksPath = %s (was %s, a herd or obor kit's)", dir, cur)
	case cur != "":
		return project.Failf("core.hooksPath is already set to %s (unset it first, or chain obor's hooks from there)", cur)
	default:
		if _, err := project.Git(s.root, "config", "--local", "core.hooksPath", dir); err != nil {
			return project.Failf("cannot set core.hooksPath in %s", s.root)
		}
		s.say("OK githooks: core.hooksPath = %s", dir)
	}
	return nil
}

func (s *Setup) herdrPlugin(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	if err := s.events(ctx, true); err != nil {
		return err
	}
	s.say("OK herdr-plugin")
	return nil
}

// events: obor.events in herdr, linked from <kit>/herdr-plugin directly (herdr keeps the path resolved at link time,
// so a symlink in between buys nothing): link sets it up (not linked → link; another kit's copy → re-link), else only
// says how. Then herdr's resume setting.
func (s *Setup) events(ctx context.Context, link bool) error {
	mine := filepath.Join(s.Kit, "herdr-plugin")
	if fi, err := os.Stat(filepath.Join(mine, "herdr-plugin.toml")); err != nil || !fi.Mode().IsRegular() {
		s.say("WARN herdr-plugin: no herdr plugin in %s", mine)
		return project.Failf("herdr-plugin")
	}
	defer s.resumeHint()
	plugins, err := s.H.Plugins(ctx)
	if err != nil {
		s.say("hint: could not read herdr plugin list; if obor.events is not linked (once per user): herdr plugin link %s", mine)
		return nil
	}
	root, linked := "", false
	for _, p := range plugins {
		if p.ID == "obor.events" {
			root, linked = p.Root, true
		}
	}
	switch {
	case linked && (root == "" || project.Physical(root) == project.Physical(mine)):
		s.say("herdr-plugin: obor.events linked (%s)", mine)
		return nil
	case !link && linked:
		s.say("herdr-plugin: obor.events runs %s, not this kit's %s (obor setup herdr-plugin re-links it)", root, mine)
		return nil
	case !link:
		s.say("hint: obor restarts its panes after a herdr session restore (once per user): obor setup herdr-plugin")
		return nil
	case linked:
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
	if value != "false" {
		s.say("hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false under [session] in " +
			path + " (user-global: affects every herdr pane, not only obor's; those come back as shells)")
	}
}

// agentsCmd is `obor setup agents [--remove]` (alias opencode-plugin; user-global, once per user): for every kind,
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
