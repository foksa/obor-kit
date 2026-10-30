package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/config"
	"obor/internal/testkit"
)

// DD-v0.13-verify "Design 2 again" gap 2: the doctor checks the kinds the project's lane types and roles use; another
// registered kind is "not used", never a WARN or FAIL. A Claude-only project with no OpenCode entry is OK; a role of an
// OpenCode type makes OpenCode used (WARN, FAIL); a role with no type runs agents.Default.
func TestSetupMCPDoctorUsedKinds(t *testing.T) {
	t.Parallel()
	x := bare(t)
	x.Config(testkit.Set("lane_types", `{"opus": {"kind": "claude"}}`))
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	if _, ok := agents.Lookup("opencode"); !ok {
		t.Fatal("precondition: opencode is a registered kind")
	}
	if _, err := os.Stat(filepath.Join(conf, "opencode", "opencode.json")); err == nil {
		t.Fatal("precondition: no opencode config")
	}
	out, rc := x.run(t, "mcp")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("a Claude-only project: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp claude: registered", "claude is checked")
	has(t, out, "mcp opencode: not used (no lane type or role of this project runs it)", "opencode is listed, not checked")
	has(t, out, "mcp codex: not used", "codex is listed, not checked")
	hasNot(t, out, "WARN", "no warning for a kind the project does not run")

	x.Config(testkit.Set("lane_types", `{"muse": {"kind": "opencode"}}`), testkit.Set("roles.reviewer", `{"type": "muse"}`))
	out, rc = x.run(t, "mcp")
	if rc != 1 || last(out) != "FAIL mcp" {
		t.Errorf("an opencode reviewer, no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "WARN mcp opencode: not registered", "a role's kind is checked")
	has(t, out, "mcp claude: registered", "no claude lane type, but the lead has no type: agents.Default")
	has(t, out, "mcp codex: not used", "codex still unused")
}

// T-445: `obor setup mcp --lane <type>` says whether a start of that lane type gives its agent obor's server, from the
// type's effective start args alone (no agent started, no `claude -p`): a Claude type with --strict-mcp-config gets
// obor's --mcp-config (an explicit obor entry in its args wins), one without it the plugin manifest, Codex the launch
// args, OpenCode the user config's entry; a kind obor registers nothing for fails, as does an unknown type.
func TestSetupMCPLane(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lane_types", `{
		"opus": {"kind": "claude", "args": ["--model", "opus"]},
		"strict": {"kind": "claude", "args": ["--model", "opus", "--strict-mcp-config"]},
		"mine": {"kind": "claude", "args": ["--strict-mcp-config",
			"--mcp-config", "{\"mcpServers\":{\"obor\":{\"command\":\"/x/bin/obor\"}}}"]},
		"luna": {"kind": "codex", "args": ["-m", "gpt-6-luna"]},
		"muse": {"kind": "opencode", "args": ["-m", "x"]},
		"cur": {"kind": "cursor"}}`))
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}

	// Claude without the strict flag: the kit's plugin manifest, as every obor Claude pane loads it.
	has(t, x.ok(t, "mcp", "--lane", "opus"), "mcp lane opus (claude): registered by the plugin manifest", "no strict flag")
	// With it, the manifest's server is hidden: obor adds the kit's own on the start.
	has(t, x.ok(t, "mcp", "--lane", "strict"),
		"mcp lane strict (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "strict flag: obor adds --mcp-config")
	// An explicit obor entry in the type's args wins: no second one.
	has(t, x.ok(t, "mcp", "--lane", "mine"),
		"mcp lane mine (claude): registered per start (the type's args name an obor server in --mcp-config)", "explicit entry")
	// Codex gets it from the launch args.
	has(t, x.ok(t, "mcp", "--lane", "luna"), "mcp lane luna (codex): registered per start (launch args", "codex")

	// OpenCode: the user config's mcp.servers.obor entry, which obor setup agents writes.
	out, rc := x.run(t, "mcp", "--lane", "muse")
	if rc != 1 || last(out) != "FAIL mcp lane muse" {
		t.Errorf("no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "WARN mcp lane muse (opencode): not registered: no current mcp.servers.obor in "+
		filepath.Join(conf, "opencode", "opencode.json"), "the opencode entry is named")
	hasNot(t, out, "mcp lane muse (opencode): registered (", "not registered before obor setup agents")
	x.ok(t, "agents")
	out = x.ok(t, "mcp", "--lane", "muse")
	has(t, out, "mcp lane muse (opencode): registered ("+filepath.Join(conf, "opencode", "opencode.json")+")",
		"registered after obor setup agents")
	if last(out) != "OK mcp lane muse" {
		t.Errorf("result %q", last(out))
	}

	// A kind obor registers nothing for, an unknown lane type, and the flag without its type.
	has(t, x.failing(t, "FAIL mcp lane cur", "mcp", "--lane", "cur"),
		"WARN mcp lane cur (cursor): not registered", "a kind without registration")
	has(t, x.failing(t, "FAIL mcp lane nope: no lane type nope (lane_types)", "mcp", "--lane", "nope"),
		"no lane type", "an unknown lane type")
	x.failing(t, "FAIL "+Usage(), "mcp", "--lane")
	if !strings.Contains(Usage(), "mcp [--lane <type>]") {
		t.Errorf("usage names --lane: %s", Usage())
	}
}

// RV-47 #3: the lane doctor reads --mcp-config as the start does, every value up to the next option. A type whose obor
// entry is a later value (or a repeated flag) names obor itself: no per-start registration is added.
func TestRV47SetupMCPLaneVariadic(t *testing.T) {
	t.Parallel()
	const herdEntry = `{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`
	const otherEntry = `{"mcpServers":{"other":{"command":"/bin/other"}}}`
	x := newFx(t, testkit.Set("lane_types", `{
		"later": {"kind": "claude", "args": ["--strict-mcp-config", "--mcp-config", `+jsonStr(otherEntry)+`, `+jsonStr(herdEntry)+`]},
		"repeated": {"kind": "claude", "args": ["--strict-mcp-config", "--mcp-config", `+jsonStr(otherEntry)+`, "--mcp-config", `+jsonStr(herdEntry)+`]},
		"none": {"kind": "claude", "args": ["--strict-mcp-config", "--mcp-config", `+jsonStr(otherEntry)+`]}}`))
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	for _, name := range []string{"later", "repeated"} {
		has(t, x.ok(t, "mcp", "--lane", name),
			"mcp lane "+name+" (claude): registered per start (the type's args name an obor server in --mcp-config)", name)
	}
	// The control: no obor value, so obor adds its own --mcp-config.
	has(t, x.ok(t, "mcp", "--lane", "none"),
		"mcp lane none (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "no obor value: obor adds its own")
}

// jsonStr is s as a JSON string literal, as the config holds a lane type's args.
func jsonStr(s string) string { return config.Compact(s) }

// DD-v0.13 B1: a Config registration is written at its bridge's Key, whatever the CLI's shape (a Cursor-like
// mcpServers.obor here, not OpenCode's mcp.servers.obor): created with its parents, every other key kept, an equal
// entry left, --remove deletes only the entry and the objects it leaves empty; the doctor reads it at the same Key.
func TestConfigMCPAtTheKindsKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cfg", "mcp.json")
	m := agents.MCPRegistration{Where: agents.Config, Path: path, Key: []string{"mcpServers", "obor"},
		Entry: map[string]any{"command": "/kit/bin/obor", "args": []string{"mcp"}}}
	var out bytes.Buffer
	s := &Setup{Out: &out}
	if state, _, err := registration(m); state != entryNone || err != nil {
		t.Fatalf("precondition: no file, not registered: %d %v", state, err)
	}
	if err := s.configMCP("cur", m, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if got := anyJSON(string(b)); !reflect.DeepEqual(got, anyJSON(`{"mcpServers":{"obor":{"command":"/kit/bin/obor","args":["mcp"]}}}`)) {
		t.Errorf("written at the key: %s", b)
	}
	has(t, out.String(), "OK agents cur: mcpServers.obor set in "+path, "said, by the key")
	if state, _, err := registration(m); state != entryPinned || err != nil {
		t.Errorf("the doctor reads it at the key: %d %v", state, err)
	}

	os.WriteFile(path, []byte(`{"theme":"x","mcpServers":{"other":{"command":"o"},"obor":{"command":"/old"}}}`), 0o644)
	if err := s.configMCP("cur", m, false); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if got := anyJSON(string(b)); !reflect.DeepEqual(got, anyJSON(`{"theme":"x","mcpServers":{"other":{"command":"o"},"obor":{"command":"/kit/bin/obor","args":["mcp"]}}}`)) {
		t.Errorf("stale entry replaced, the rest kept: %s", b)
	}
	if err := s.configMCP("cur", m, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if got := anyJSON(string(b)); !reflect.DeepEqual(got, anyJSON(`{"theme":"x","mcpServers":{"other":{"command":"o"}}}`)) {
		t.Errorf("--remove deletes only the entry: %s", b)
	}
	os.WriteFile(path, []byte(`{"theme":"x","mcpServers":{"obor":{}}}`), 0o644)
	if err := s.configMCP("cur", m, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if got := anyJSON(string(b)); !reflect.DeepEqual(got, anyJSON(`{"theme":"x"}`)) {
		t.Errorf("an emptied mcpServers is deleted: %s", b)
	}
	os.WriteFile(path, []byte(`{"mcpServers":[]}`), 0o644)
	if err := s.configMCP("cur", m, false); err == nil || !strings.Contains(err.Error(), "mcpServers is not an object: left alone") {
		t.Errorf("a non-object on the way: %v", err)
	}
}

// T-518: the MCP edit keeps the user's file in its order: an unrelated member (zeta before alpha, a nested object's
// keys) stays where it was, the new entry goes last in its object, and --remove gives the file back as it was.
func TestConfigMCPKeepsMemberOrder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mcp.json")
	m := agents.MCPRegistration{Where: agents.Config, Path: path, Key: []string{"mcpServers", "obor"},
		Entry: map[string]any{"command": "/kit/bin/obor", "args": []string{"mcp"}}}
	before := `{
  "zeta": {
    "y": 1,
    "b": 2
  },
  "mcpServers": {
    "other": {
      "command": "o"
    }
  },
  "alpha": true
}
`
	after := `{
  "zeta": {
    "y": 1,
    "b": 2
  },
  "mcpServers": {
    "other": {
      "command": "o"
    },
    "obor": {
      "args": [
        "mcp"
      ],
      "command": "/kit/bin/obor"
    }
  },
  "alpha": true
}
`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Setup{Out: &bytes.Buffer{}}
	if err := s.configMCP("cur", m, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != after {
		t.Errorf("set: got\n%s\nwant\n%s", b, after)
	}
	if err := s.configMCP("cur", m, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != before {
		t.Errorf("remove: got\n%s\nwant\n%s", b, before)
	}
}

// DD-v0.13 B1: obor setup agents names each kind; one with no plugin and no config registration has nothing to install.
func TestSetupAgentsEveryKind(t *testing.T) {
	t.Parallel()
	x := bare(t)
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	out := x.ok(t, "agents")
	has(t, out, "agents claude: nothing to install", "claude: the manifest ships with the kit")
	has(t, out, "agents codex: nothing to install", "codex: launch args")
	has(t, out, "OK agents opencode: mcp.servers.obor set in ", "opencode: its config entry")
	if !strings.Contains(Usage(), "agents [--remove]") {
		t.Errorf("usage names agents: %s", Usage())
	}
}
