package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
)

// The obor MCP server (`obor mcp`) is registered per agent kind, as its bridge says
// (agents.Kind.Install): by the kit's manifest (Claude), per start (Codex's launch args, OpenCode's generated config)
// or by committed files of the project (agy's .agents/mcp_config.json and .agents/hooks.json, which obor setup agents
// writes). A kind installs nothing user-global. This file reads those files.

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

// fixHint is how to register a manifest entry: the kit's manifest ships with the kit.
const fixHint = "update the plugin"

// fixFor is how to register m: a project file's entry is obor setup agents' to write and the lead's to commit, a
// manifest's is the kit's (fixHint).
func fixFor(m agents.MCPRegistration) string {
	if m.Where == agents.Project {
		return "run obor setup agents in the project, then commit the file"
	}
	return fixHint
}

// pathKitTimeout bounds the obor help call pathWarning runs to ask the shim which kit it runs.
const pathKitTimeout = 5 * time.Second

// pathWarning: kind name's project file names obor by PATH, so its agents run the obor the project env's PATH finds for
// their MCP server and hooks: a WARN when that is none, cannot be asked, or runs another kit. The shim owns the choice
// (self-hosting included), so pathWarning runs it with help in the project root.
func (s *Setup) pathWarning(ctx context.Context, name string) {
	found, err := project.LookPath(s.Env.Get("PATH"), "obor")
	if err != nil {
		s.say("WARN mcp %s: no obor on PATH: its agents' MCP server and hooks do not start", name)
		return
	}
	dir := s.root
	if dir == "" {
		dir = s.Dir
	}
	root, err := shimKit(ctx, found, dir)
	if err != nil {
		s.say("WARN mcp %s: could not tell which kit the obor on PATH (%s) runs (%v): %s's hooks and MCP server run it",
			name, found, err, name)
		return
	}
	root = project.Physical(root)
	if own := project.Physical(s.Kit); root != own {
		s.say("WARN mcp %s: the obor on PATH (%s, kit %s) is not this kit (%s): %s's hooks and MCP server run it",
			name, found, root, own, name)
	}
}

// shimKit runs path with help in dir under pathKitTimeout as its own process group, with OBOR_SELF_HOSTED and
// OBOR_NO_SELF_HOST unset (as an agent's pane has them) and OBOR_NO_FETCH=1 (no network), and returns its
// "plugin root:" line.
func shimKit(ctx context.Context, path, dir string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, pathKitTimeout)
	defer cancel()
	execPath := path
	if !filepath.IsAbs(execPath) {
		if a, err := filepath.Abs(execPath); err == nil {
			execPath = a
		}
	}
	cmd := exec.CommandContext(tctx, execPath, "help")
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "OBOR_SELF_HOSTED=") ||
			strings.HasPrefix(kv, agents.Pin+"=") ||
			strings.HasPrefix(kv, "OBOR_NO_FETCH=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "OBOR_NO_FETCH=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cleanup := project.Group(cmd)
	defer cleanup()
	if err := cmd.Run(); err != nil {
		if errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("timed out after %s", pathKitTimeout)
		}
		return "", errors.New(exitText(err))
	}
	root := pluginRootLine(stdout.String())
	if root == "" {
		return "", errors.New("no plugin root line")
	}
	return root, nil
}

// pluginRootLine is the path on the "plugin root: <path>" line of obor help's output, or "" when absent.
func pluginRootLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "plugin root:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// mcpCmd is `obor setup mcp [--model <name>]`, the doctor: per agent kind the project configures (usedKinds; the
// others are "not used", never a WARN) whether the obor MCP server (and any project hooks file) is registered, then
// `obor mcp --check` (the handshake against the server itself); --model <name> instead asks for that model alone,
// from its effective start args. Read-only.
func (s *Setup) mcpCmd(ctx context.Context, args []string) error {
	lane := ""
	switch {
	case len(args) == 2 && args[0] == "--model" && args[1] != "":
		lane = args[1]
	case len(args) != 0:
		return errUsage
	}
	if lane != "" {
		return s.mcpLane(ctx, lane)
	}
	used, err := s.usedKinds()
	if err != nil {
		return err
	}
	bad := false
	for _, name := range s.cfg.Kinds().Names() {
		if used != nil && !used[name] {
			s.say("mcp %s: not used (no agents.models entry of kind %s)", name, name)
			continue
		}
		k, _ := s.cfg.Kind(name)
		if c, ok := k.(agents.InstallChecker); ok { // the CLI's install: a warning, never a failure
			for _, w := range c.InstallWarnings(s.installHost()) {
				s.say("WARN %s: %s", name, w)
			}
		}
		in, err := agents.InstallOf(k, s.Kit, s.root, s.Env.Get)
		if err != nil { // a plugin kind's install failed
			s.say("WARN mcp %s: %v", name, err)
			bad = true
			continue
		}
		if in.MCP.Where == agents.Project && filepath.IsAbs(in.MCP.Path) ||
			in.Hooks.Where == agents.Project && filepath.IsAbs(in.Hooks.Path) {
			s.pathWarning(ctx, name)
		}
		bad = s.checkRegistration("mcp", name, in.MCP) || bad
		if in.Hooks.Where == agents.Project {
			bad = s.checkRegistration("hooks", name, in.Hooks) || bad
		}
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

// checkRegistration reports whether m (label: "mcp" or "hooks") is registered for kind name; bad is true when the
// expected file entry is absent or unreadable.
func (s *Setup) checkRegistration(label, name string, m agents.MCPRegistration) (bad bool) {
	switch {
	case m.Where == agents.PerStart: // nothing to install
		how, _, _ := m.Lane(nil, os.ReadFile)
		s.say("%s %s: registered per start (%s)", label, name, how)
		return false
	case m.Where == agents.Project && !filepath.IsAbs(m.Path): // no project here
		s.say("%s %s: registered by each project's %s (obor setup agents in the project writes it)", label, name, m.Path)
		return false
	}
	// a manifest (every obor pane of the kind loads it with the plugin) or the project's file
	fix := fixFor(m)
	state, path, err := registration(m)
	switch {
	case err != nil:
		s.say("WARN %s %s: not registered: %s: %v (%s)", label, name, path, err, fix)
	case state == entryNone:
		s.say("WARN %s %s: not registered: no current %s in %s (%s)", label, name, strings.Join(m.Key, "."), path, fix)
	default:
		s.say("%s %s: registered (%s)", label, name, path)
	}
	if state == entryUnpinned {
		s.say("WARN %s %s: registered without the %s pin: it reaches the cwd checkout's serve in an obor checkout (%s)", label, name, agents.Pin, fixHint)
	}
	return state == entryNone
}

// installHost is this machine for agents.InstallChecker: the CLI looked up on the project env's PATH (an env without
// one finds none), the quarantine flags read with macOS's xattr (project.InstallHost, the one builder launch shares).
func (s *Setup) installHost() agents.InstallHost {
	if s.Install != nil {
		return s.Install()
	}
	return project.InstallHost(s.Env)
}

// usedKinds are the kinds the project configures: every agents.models entry's kind, and agents.Default for a role with
// no model, as launch starts it; nil when there is no project config (check every kind), and a config that fails to
// load is its own error (the command fails with it).
func (s *Setup) usedKinds() (map[string]bool, error) {
	cfg, err := s.projectConfig()
	if err != nil || cfg == nil {
		return nil, err
	}
	s.cfg = cfg
	used := map[string]bool{}
	for _, name := range s.cfg.ModelNames() {
		if m, ok := s.cfg.Model(name); ok {
			used[m.Kind] = true
		}
	}
	for _, rc := range s.cfg.RoleAgents() {
		if lt, ok := s.cfg.Model(rc.Type); !ok || lt.Kind == "" {
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
func (s *Setup) mcpLane(ctx context.Context, name string) error {
	if err := s.load(); err != nil {
		return err
	}
	lt, ok := s.cfg.Model(name)
	if !ok {
		return project.Failf("mcp model %s: %v", name, s.cfg.UnknownModel(name))
	}
	bad := false
	k, known := s.cfg.Kind(lt.Kind)
	if !known {
		s.say("WARN mcp model %s (%s): not registered: obor registers its server for %s", name, lt.Kind, kindList(s.cfg.Kinds()))
		return project.Failf("mcp model %s", name)
	}
	in, err := agents.InstallOf(k, s.Kit, s.cfg.Root, s.Env.Get)
	if err != nil { // a plugin kind's install failed
		s.say("WARN mcp model %s (%s): %v", name, lt.Kind, err)
		return project.Failf("mcp model %s", name)
	}
	m := in.MCP
	// a lane's start: a lane never is the lead, the settings default applies, and a lane's agent starts strict
	// unless the project's committed CLI config opts out (the main checkout's copy stands for a lane's). A kind
	// whose start is one call (a plugin kind) gets the complete start, the kit too; a compiled kind's Lane reads
	// the type's launch args, before the start registers obor's server for the kit, and its start file (Claude's
	// settings file, its MCP file) is named, not written: no agent starts.
	start := agents.Start{Agent: "worker", Dir: s.cfg.Root, Root: s.cfg.Root, Read: os.ReadFile, Env: s.Env.Get, Dirs: project.SubDirs,
		WriteState: project.StateWriter(s.cfg.Root)}
	if _, whole := k.(agents.Whole); whole {
		start.Kit = s.Kit
	} else {
		start.WriteState = func(name string, _ []byte) (string, error) {
			return filepath.Join(project.StateDir(s.cfg.Root), name), nil
		}
	}
	overrides, err := s.cfg.Overrides(lt.Kind)
	var built agents.Launch
	if err == nil {
		built, err = lt.Launch("worker", overrides, start)
	}
	if err != nil {
		s.say("WARN mcp model %s (%s): %v", name, lt.Kind, err)
		return project.Failf("mcp model %s", name)
	}
	how, registered, static := m.Lane(built.Args, os.ReadFile)

	warnedPath := false
	switch {
	case !static && registered:
		s.say("mcp model %s (%s): registered per start (%s)", name, lt.Kind, how)
	case !static:
		s.say("WARN mcp model %s (%s): not registered: %s", name, lt.Kind, how)
		bad = true
	default: // the kit's plugin manifest or the project's file serves the start
		by := "the plugin manifest"
		if m.Where == agents.Project {
			by = "the project's file"
			s.pathWarning(ctx, lt.Kind)
			warnedPath = true
		}
		bad = s.checkLaneFile("mcp", name, lt.Kind, by, m) || bad
	}
	if in.Hooks.Where == agents.Project {
		if !warnedPath {
			s.pathWarning(ctx, lt.Kind)
		}
		bad = s.checkLaneFile("hooks", name, lt.Kind, "the project's file", in.Hooks) || bad
	}
	if bad {
		return project.Failf("mcp model %s", name)
	}
	s.say("OK mcp model %s", name)
	return nil
}

// checkLaneFile reports whether m (label: "mcp" or "hooks") is registered for model of kind; bad is true when the
// expected file entry is absent or unreadable.
func (s *Setup) checkLaneFile(label, model, kind, by string, m agents.MCPRegistration) (bad bool) {
	switch state, path, err := registration(m); {
	case err != nil:
		s.say("WARN %s model %s (%s): not registered: %s: %v (%s)", label, model, kind, path, err, fixFor(m))
		return true
	case state == entryNone:
		s.say("WARN %s model %s (%s): not registered: no current %s in %s (%s)", label, model, kind, strings.Join(m.Key, "."), path, fixFor(m))
		return true
	default:
		s.say("%s model %s (%s): registered by %s (%s)", label, model, kind, by, path)
		return false
	}
}

// kindList is the kinds of kinds (obor has a bridge for each), "a, b and c".
func kindList(kinds agents.Kinds) string {
	n := kinds.Names()
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
