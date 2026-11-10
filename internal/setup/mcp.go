package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
)

// The obor MCP server (`obor mcp`) is registered per agent kind, as its bridge says
// (agents.Kind.Install): by the kit's manifest (Claude) or per start (Codex's launch args, OpenCode's generated config).
// A kind installs nothing user-global. This file reads those files.

// readConfig is the file as JSON (an absent or blank file is an empty config; not JSON is an error).
func readConfig(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(bytes.TrimSpace(b)) == 0 {
		return []byte(`{}`), nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, errors.New("not valid JSON")
	}
	return b, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Registration states of a Manifest entry (registration).
const (
	entryNone     = iota // no current obor entry at the key
	entryUnpinned        // the entry as written before the pin: registered, with a warning
	entryPinned          // the entry
)

// registration is how m's file (path) holds obor's entry at m.Key: entryNone, entryUnpinned or entryPinned; err: the file
// is not read (not JSON, or no object on the way to the key).
func registration(m agents.MCPRegistration) (state int, path string, err error) {
	path = m.Path
	doc, err := readConfig(path)
	if err != nil {
		return entryNone, path, err
	}
	raw, err := config.MemberAt(doc, m.Key)
	if err != nil || raw == nil {
		return entryNone, path, err
	}
	switch entry := anyJSON(string(raw)); {
	case reflect.DeepEqual(entry, anyJSON(mustJSON(m.Entry))):
		return entryPinned, path, nil
	case m.Unpinned != nil && reflect.DeepEqual(entry, anyJSON(mustJSON(m.Unpinned))):
		return entryUnpinned, path, nil
	}
	return entryNone, path, nil
}

// fixHint is how to register m: only the kit's manifest is left, and it ships with the kit.
const fixHint = "update the plugin"

// mcpCmd is `obor setup mcp [--model <name>]`, the doctor: per agent kind the project uses (usedKinds; the others are
// "not used", never a WARN) whether the obor MCP server is registered, then `obor mcp --check` (the handshake against
// the server itself); --model <name> instead asks for that model alone, from its effective start args.
// Read-only.
func (s *Setup) mcpCmd(_ context.Context, args []string) error {
	lane := ""
	switch {
	case len(args) == 2 && args[0] == "--model" && args[1] != "":
		lane = args[1]
	case len(args) != 0:
		return errUsage
	}
	if lane != "" {
		return s.mcpLane(lane)
	}
	used, err := s.usedKinds()
	if err != nil {
		return err
	}
	bad := false
	for _, name := range agents.Names() {
		if used != nil && !used[name] {
			s.say("mcp %s: not used (no model of a level, role or agent of this project runs it)", name)
			continue
		}
		k, _ := agents.Lookup(name)
		if c, ok := k.(agents.InstallChecker); ok { // the CLI's install: a warning, never a failure
			for _, w := range c.InstallWarnings(s.installHost()) {
				s.say("WARN %s: %s", name, w)
			}
		}
		in, err := agents.InstallOf(k, s.Kit, s.Env.Get)
		if err != nil { // a plugin kind's install failed
			s.say("WARN mcp %s: %v", name, err)
			bad = true
			continue
		}
		m := in.MCP
		if m.Where == agents.PerStart { // nothing to install
			how, _, _ := m.Lane(nil, os.ReadFile)
			s.say("mcp %s: registered per start (%s)", name, how)
			continue
		}
		// a manifest: every obor pane of the kind loads it with the plugin
		state, path, err := registration(m)
		switch {
		case err != nil:
			s.say("WARN mcp %s: not registered: %s: %v (%s)", name, path, err, fixHint)
		case state == entryNone:
			s.say("WARN mcp %s: not registered: no current %s in %s (%s)", name, strings.Join(m.Key, "."), path, fixHint)
		default:
			s.say("mcp %s: registered (%s)", name, path)
		}
		if state == entryUnpinned {
			s.say("WARN mcp %s: registered without the %s pin: it reaches the cwd checkout's serve in an obor checkout (%s)", name, agents.Pin, fixHint)
		}
		bad = bad || state == entryNone
	}
	line, err := s.McpCheck(s.Env)
	if err != nil {
		s.say("WARN mcp check: %v", err)
		bad = true
	} else {
		s.say("%s", line)
	}
	if bad {
		return project.Failf("mcp")
	}
	s.say("OK mcp")
	return nil
}

// installHost is this machine for agents.InstallChecker: the CLI looked up on the project env's PATH (an env without
// one finds none), the quarantine flags read with macOS's xattr (project.InstallHost, the one builder launch shares).
func (s *Setup) installHost() agents.InstallHost {
	if s.Install != nil {
		return s.Install()
	}
	return project.InstallHost(s.Env)
}

// usedKinds are the kinds the project runs: every used model's (a level's, a role's, an agent's), and agents.Default
// for a role with no model, as launch starts it; nil when there is no project config (check every kind), and a config
// that fails to load is its own error (the command fails with it).
func (s *Setup) usedKinds() (map[string]bool, error) {
	cfg, err := s.projectConfig()
	if err != nil || cfg == nil {
		return nil, err
	}
	s.cfg = cfg
	used := map[string]bool{}
	for _, name := range s.cfg.UsedModels() {
		if m, ok := s.cfg.Model(name); ok {
			used[m.Kind] = true
		}
	}
	for _, rc := range s.cfg.RoleAgents() {
		if lt, ok := s.cfg.Model(rc.Type); ok && lt.Kind != "" {
			used[lt.Kind] = true
		} else {
			used[agents.Default] = true
		}
	}
	return used, nil
}

// mcpLane is `obor setup mcp --model <name>`: whether a start of that model gives its agent obor's server, read from
// its effective start args as its kind says (agents.MCPRegistration.Lane) — claude: a lane's start carries
// --strict-mcp-config, so obor adds its --mcp-config (an explicit obor entry in kinds.claude.args wins); codex: launch
// args on every start; opencode: the start's generated config (OPENCODE_CONFIG). It starts no agent and runs no `claude -p`
// (a headless run in a lane worktree inherits the worker role and its Stop hook). No handshake: `obor setup mcp`
// runs it.
func (s *Setup) mcpLane(name string) error {
	if err := s.load(); err != nil {
		return err
	}
	lt, ok := s.cfg.Model(name)
	switch {
	case !ok:
		return project.Failf("mcp model %s: no model %s (models: %s)", name, name, strings.Join(s.cfg.ModelNames(), ", "))
	case lt.Kind == "":
		return project.Failf("mcp model %s: the model has no kind", name)
	}
	bad := false
	k, known := agents.Lookup(lt.Kind)
	if !known {
		s.say("WARN mcp model %s (%s): not registered: obor registers its server for %s", name, lt.Kind, kindList())
		return project.Failf("mcp model %s", name)
	}
	in, err := agents.InstallOf(k, s.Kit, s.Env.Get)
	if err != nil { // a plugin kind's install failed
		s.say("WARN mcp model %s (%s): %v", name, lt.Kind, err)
		return project.Failf("mcp model %s", name)
	}
	m := in.MCP
	// a lane's start: a lane never is the lead, the settings default applies, and a lane's agent starts strict
	// unless the project's committed CLI config opts out (the main checkout's copy stands for a lane's). A kind
	// whose start is one call (a plugin kind) gets the complete start, the kit too; a compiled kind's Lane reads
	// the type's launch args, before the start registers obor's server for the kit.
	start := agents.Start{Dir: s.cfg.Root, Read: os.ReadFile, Env: s.Env.Get, Dirs: project.SubDirs,
		WriteState: project.StateWriter(s.cfg.Root)}
	if _, whole := k.(agents.Whole); whole {
		start.Kit = s.Kit
	}
	built, err := lt.Launch("worker", start)
	if err != nil {
		s.say("WARN mcp model %s (%s): %v", name, lt.Kind, err)
		return project.Failf("mcp model %s", name)
	}
	how, registered, static := m.Lane(built.Args, os.ReadFile)

	switch {
	case !static && registered:
		s.say("mcp model %s (%s): registered per start (%s)", name, lt.Kind, how)
	case !static:
		s.say("WARN mcp model %s (%s): not registered: %s", name, lt.Kind, how)
		bad = true
	default: // the kit's plugin manifest serves the start
		switch state, path, err := registration(m); {
		case err != nil:
			s.say("WARN mcp model %s (%s): not registered: %s: %v (%s)", name, lt.Kind, path, err, fixHint)
			bad = true
		case state == entryNone:
			s.say("WARN mcp model %s (%s): not registered: no current %s in %s (%s)", name, lt.Kind, strings.Join(m.Key, "."), path, fixHint)
			bad = true
		default:
			s.say("mcp model %s (%s): registered by the plugin manifest (%s)", name, lt.Kind, path)
		}
	}
	if bad {
		return project.Failf("mcp model %s", name)
	}
	s.say("OK mcp model %s", name)
	return nil
}

// kindList is the kinds obor has a bridge for, "a, b and c".
func kindList() string {
	n := agents.Names()
	if len(n) < 2 {
		return strings.Join(n, "")
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}

func anyJSON(s string) any {
	var v any
	_ = json.Unmarshal([]byte(s), &v)
	return v
}
