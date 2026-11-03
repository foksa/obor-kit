package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
)

// The obor MCP server (`obor mcp`) is registered per agent kind, as its bridge says
// (agents.Kind.Install): by the kit's manifest (Claude), by launch args on every start (Codex), or by the obor entry of
// the CLI's user config (OpenCode), each entry at its MCPRegistration.Key. This file reads and edits those files.

// configFile is the file of m's entry: Path, or AltPath when that is the only one there (it is edited too when it is
// plain JSON, errComments when it is not).
func configFile(m agents.MCPRegistration) string {
	if _, err := os.Stat(m.Path); err != nil && m.AltPath != "" {
		if _, jerr := os.Stat(m.AltPath); jerr == nil {
			return m.AltPath
		}
	}
	return m.Path
}

// errComments: a .jsonc config that is not plain JSON; editing it would lose its comments.
var errComments = errors.New("has comments")

// readConfig is the file as it is, so every other key is written back as it was (an absent or blank file is an empty
// config; a .jsonc that is not valid JSON is errComments).
func readConfig(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(bytes.TrimSpace(b)) == 0 {
		return []byte(`{}`), nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		if strings.HasSuffix(path, ".jsonc") {
			return nil, errComments
		}
		return nil, errors.New("not valid JSON")
	}
	return b, nil
}

// configMCP writes the obor entry of m (a Config registration of kind) at m.Key into the user config (idempotent: an
// equal entry is left, a stale one, e.g. another kit's path, is replaced; every other key stays). remove deletes only
// that entry.
func (s *Setup) configMCP(kind string, m agents.MCPRegistration, remove bool) error {
	path, key := configFile(m), strings.Join(m.Key, ".")
	doc, err := readConfig(path)
	if errors.Is(err, errComments) {
		return s.pasteHint(kind, m, path)
	}
	if err != nil {
		return project.Failf("agents %s: %s: %v: left alone", kind, path, err)
	}
	cur, err := config.MemberAt(doc, m.Key)
	if err != nil {
		return project.Failf("agents %s: %s: %v: left alone", kind, path, err)
	}
	var v json.RawMessage
	switch {
	case remove && cur == nil:
		s.say("OK agents %s: no %s in %s", kind, key, path)
		return nil
	case !remove && cur != nil && reflect.DeepEqual(anyJSON(string(cur)), anyJSON(mustJSON(m.Entry))):
		s.say("OK agents %s: %s already set in %s", kind, key, path)
		return nil
	case !remove:
		v = json.RawMessage(mustJSON(m.Entry))
	}
	if doc, err = config.EditJSON(doc, m.Key, v); err != nil {
		return project.Failf("agents %s: %s: %v: left alone", kind, path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return project.Failf("agents %s: %v", kind, err)
	}
	var b bytes.Buffer // written in place, not atomically: a symlinked user config stays a link
	if err := json.Indent(&b, doc, "", "  "); err != nil {
		return project.Failf("agents %s: %v", kind, err)
	}
	if err := os.WriteFile(path, append(b.Bytes(), '\n'), 0o644); err != nil {
		return project.Failf("agents %s: %v", kind, err)
	}
	if remove {
		s.say("OK agents %s: %s removed from %s (restart %s)", kind, key, path, kind)
	} else {
		s.say("OK agents %s: %s set in %s (user-global; restart %s to load it)", kind, key, path, kind)
	}
	return nil
}

// pasteEntry prints the obor entry to paste by hand in a config obor does not edit (comments).
func (s *Setup) pasteEntry(m agents.MCPRegistration, path string) {
	entry, _ := json.MarshalIndent(m.Entry, "  ", "  ")
	s.say("paste into %s under %q:", path, strings.Join(m.Key[:len(m.Key)-1], "."))
	s.say("  %q: %s", m.Key[len(m.Key)-1], entry)
}

// pasteHint is the comments case of obor setup agents: obor does not edit a config with comments, so it prints the
// paste block and fails (the FAIL line stays last, exit 1).
func (s *Setup) pasteHint(kind string, m agents.MCPRegistration, path string) error {
	s.say("WARN agents %s: %s is not edited (comments would be lost)", kind, path)
	s.pasteEntry(m, path)
	return project.Failf("agents %s: %s is not edited (comments would be lost)", kind, path)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Registration states of a Manifest or Config entry (registration).
const (
	entryNone     = iota // no current obor entry at the key
	entryUnpinned        // the entry as written before the pin: registered, with a warning
	entryPinned          // the entry
)

// registration is how m's file (path) holds obor's entry at m.Key: entryNone, entryUnpinned or entryPinned; err: the file
// is not read (errComments, not JSON, or no object on the way to the key).
func registration(m agents.MCPRegistration) (state int, path string, err error) {
	path = configFile(m)
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

// fix is how to register m: the manifest ships with the kit (update the plugin); a config entry is obor setup agents'.
func fix(m agents.MCPRegistration) string {
	if m.Where == agents.Manifest {
		return "update the plugin"
	}
	return "obor setup agents"
}

// mcpCmd is `obor setup mcp [--model <name>]`, the doctor: per agent kind the project uses (usedKinds; the others are
// "not used", never a WARN) whether the obor MCP server is registered, then `obor mcp --check` (the handshake against
// the server itself); --model <name> instead asks for that model alone, from its effective start args (T-445).
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
	used := s.usedKinds()
	bad := false
	for _, name := range agents.Names() {
		if used != nil && !used[name] {
			s.say("mcp %s: not used (no model of a level, role or agent of this project runs it)", name)
			continue
		}
		k, _ := agents.Lookup(name)
		m := k.Install(s.Kit, s.Env.Get).MCP
		if m.Where == agents.PerStart { // nothing to install
			how, _, _ := m.Lane(nil, os.ReadFile)
			s.say("mcp %s: registered per start (%s)", name, how)
			continue
		}
		// a manifest: every obor pane of the kind loads it with the plugin; a config: the CLI's user config
		state, path, err := registration(m)
		switch {
		case errors.Is(err, errComments):
			s.say("WARN mcp %s: not registered: %s is not read (comments) (%s)", name, path, fix(m))
			s.pasteEntry(m, path)
		case err != nil:
			s.say("WARN mcp %s: not registered: %s: %v (%s)", name, path, err, fix(m))
		case state == entryNone:
			s.say("WARN mcp %s: not registered: no current %s in %s (%s)", name, strings.Join(m.Key, "."), path, fix(m))
		default:
			s.say("mcp %s: registered (%s)", name, path)
		}
		if state == entryUnpinned {
			s.say("WARN mcp %s: registered without the %s pin: it reaches the cwd checkout's serve in an obor checkout (%s)", name, agents.Pin, fix(m))
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

// usedKinds are the kinds the project runs: every used model's (a level's, a role's, an agent's), and agents.Default
// for a role with no model, as launch starts it; nil when the config does not load (check every kind).
func (s *Setup) usedKinds() map[string]bool {
	if s.load() != nil {
		return nil
	}
	used := map[string]bool{}
	for _, name := range s.cfg.UsedModels() {
		if m, ok := s.cfg.Model(name); ok {
			used[m.Kind] = true
		}
	}
	for _, role := range roles.All() {
		rc, _ := s.cfg.Role(role.Name)
		if role.Name == "worker" || role.Optional && !rc.Configured {
			continue
		}
		if lt, ok := s.cfg.Model(rc.Type); ok && lt.Kind != "" {
			used[lt.Kind] = true
		} else {
			used[agents.Default] = true
		}
	}
	return used
}

// mcpLane is `obor setup mcp --model <name>`: whether a start of that model gives its agent obor's server, read from
// its effective start args as its kind says (agents.MCPRegistration.Lane) — claude: a start with --strict-mcp-config
// gets obor's --mcp-config (an explicit obor entry in kinds.claude.args wins), else the plugin manifest's server; codex:
// launch args on every start; opencode: the user config's mcp.servers.obor entry. It starts no agent and runs no
// `claude -p` (T-445: a headless run in a lane worktree inherits the worker role and its Stop hook). No handshake:
// `obor setup mcp` runs it.
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
	m := k.Install(s.Kit, s.Env.Get).MCP
	// a lane never is the lead: the settings default applies
	how, registered, static := m.Lane(lt.LaunchArgs(false, ""), os.ReadFile)
	switch {
	case !static && registered:
		s.say("mcp model %s (%s): registered per start (%s)", name, lt.Kind, how)
	case !static:
		s.say("WARN mcp model %s (%s): not registered: %s", name, lt.Kind, how)
		bad = true
	default: // the Manifest or Config entry serves the start
		switch state, path, err := registration(m); {
		case errors.Is(err, errComments):
			s.say("WARN mcp model %s (%s): not registered: %s is not read (comments) (%s)", name, lt.Kind, path, fix(m))
			bad = true
		case err != nil:
			s.say("WARN mcp model %s (%s): not registered: %s: %v (%s)", name, lt.Kind, path, err, fix(m))
			bad = true
		case state == entryNone:
			s.say("WARN mcp model %s (%s): not registered: no current %s in %s (%s)", name, lt.Kind, strings.Join(m.Key, "."), path, fix(m))
			bad = true
		case m.Where == agents.Manifest:
			s.say("mcp model %s (%s): registered by the plugin manifest (%s)", name, lt.Kind, path)
		default:
			s.say("mcp model %s (%s): registered (%s)", name, lt.Kind, path)
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
