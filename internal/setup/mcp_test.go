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

// DD-v0.13-verify "Design 2 again" gap 2, T-765: the doctor checks the kinds the project's models use (a level's,
// a role's, an agent's); another registered kind is "not used", never a WARN or FAIL. A Claude-only project with no
// OpenCode entry is OK; a role of an OpenCode model makes OpenCode used (WARN, FAIL); a role with no model runs
// agents.Default.
func TestSetupMCPDoctorUsedKinds(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeOnly := testkit.Set("difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
	x.Config(claudeOnly)
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
	has(t, out, "mcp opencode: not used (no model of a level, role or agent of this project runs it)", "opencode is listed, not checked")
	has(t, out, "mcp codex: not used", "codex is listed, not checked")
	hasNot(t, out, "WARN", "no warning for a kind the project does not run")

	x.Config(claudeOnly, testkit.Set("models", `{"muse": {"kind": "opencode", "model": "x"}}`), testkit.Set("roles.reviewer", "muse"))
	out, rc = x.run(t, "mcp")
	if rc != 1 || last(out) != "FAIL mcp" {
		t.Errorf("an opencode reviewer, no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "WARN mcp opencode: not registered", "a role's kind is checked")
	has(t, out, "mcp claude: registered", "claude models of the levels, and the lead has no model: agents.Default")
	has(t, out, "mcp codex: not used", "codex still unused")
}

// T-445, T-765: `obor setup mcp --model <name>` says whether a start of that model gives its agent obor's server, from
// its effective start args alone (no agent started, no `claude -p`): a Claude start with --strict-mcp-config (in
// kinds.claude.args) gets obor's --mcp-config (an explicit obor entry there wins), one without it the plugin manifest,
// Codex the launch args, OpenCode the user config's entry; a kind obor registers nothing for fails, as does an unknown
// model.
func TestSetupMCPLane(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("models", `{"muse": {"kind": "opencode", "model": "x"}, "cur": {"kind": "cursor", "model": "c"}}`))
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}

	// A lane's Claude start is strict (T-770): obor adds the kit's own on the start, the plugin manifest's hidden.
	has(t, x.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "a lane's start: strict, obor adds --mcp-config")
	// With the flag in the args, the same: it is there once.
	strict := newFx(t, testkit.Set("kinds.claude.args", `["--strict-mcp-config"]`))
	has(t, strict.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "strict flag: obor adds --mcp-config")
	// An explicit obor entry in the args wins: no second one.
	mine := newFx(t, testkit.Set("kinds.claude.args", `["--strict-mcp-config", "--mcp-config", "{\"mcpServers\":{\"obor\":{\"command\":\"/x/bin/obor\"}}}"]`))
	has(t, mine.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (the type's args name an obor server in --mcp-config)", "explicit entry")
	// Codex gets it from the launch args.
	has(t, x.ok(t, "mcp", "--model", "luna"), "mcp model luna (codex): registered per start (launch args", "codex")

	// OpenCode: the user config's mcp.servers.obor entry, which obor setup agents writes.
	out, rc := x.run(t, "mcp", "--model", "muse")
	if rc != 1 || last(out) != "FAIL mcp model muse" {
		t.Errorf("no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "WARN mcp model muse (opencode): not registered: no current mcp.servers.obor in "+
		filepath.Join(conf, "opencode", "opencode.json"), "the opencode entry is named")
	hasNot(t, out, "mcp model muse (opencode): registered (", "not registered before obor setup agents")
	x.ok(t, "agents")
	out = x.ok(t, "mcp", "--model", "muse")
	has(t, out, "mcp model muse (opencode): registered ("+filepath.Join(conf, "opencode", "opencode.json")+")",
		"registered after obor setup agents")
	if last(out) != "OK mcp model muse" {
		t.Errorf("result %q", last(out))
	}

	// A kind obor registers nothing for, an unknown model, and the flag without its model.
	has(t, x.failing(t, "FAIL mcp model cur", "mcp", "--model", "cur"),
		"WARN mcp model cur (cursor): not registered", "a kind without registration")
	has(t, x.failing(t, "FAIL mcp model nope: no model nope (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna, cur, muse)",
		"mcp", "--model", "nope"), "no model nope", "an unknown model")
	x.failing(t, "FAIL "+Usage(), "mcp", "--model")
	x.failing(t, "FAIL "+Usage(), "mcp", "--lane", "opus")
	if !strings.Contains(Usage(), "mcp [--model <name>]") {
		t.Errorf("usage names --model: %s", Usage())
	}
}

// RV-47 #3: the doctor reads --mcp-config as the start does, every value up to the next option. Args whose obor entry
// is a later value (or a repeated flag) name obor itself: no per-start registration is added.
func TestRV47SetupMCPLaneVariadic(t *testing.T) {
	t.Parallel()
	const oborEntry = `{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`
	const otherEntry = `{"mcpServers":{"other":{"command":"/bin/other"}}}`
	for name, args := range map[string]string{
		"later":    `["--strict-mcp-config", "--mcp-config", ` + jsonStr(otherEntry) + `, ` + jsonStr(oborEntry) + `]`,
		"repeated": `["--strict-mcp-config", "--mcp-config", ` + jsonStr(otherEntry) + `, "--mcp-config", ` + jsonStr(oborEntry) + `]`,
	} {
		x := newFx(t, testkit.Set("kinds.claude.args", args))
		x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
		has(t, x.ok(t, "mcp", "--model", "opus"),
			"mcp model opus (claude): registered per start (the type's args name an obor server in --mcp-config)", name)
	}
	// The control: no obor value, so obor adds its own --mcp-config.
	x := newFx(t, testkit.Set("kinds.claude.args", `["--strict-mcp-config", "--mcp-config", `+jsonStr(otherEntry)+`]`))
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	has(t, x.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "no obor value: obor adds its own")
}

// jsonStr is s as a JSON string literal, as the config holds kinds args.
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
