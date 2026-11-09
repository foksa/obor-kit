package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/config"
	"obor/internal/testkit"
)

// DD-v0.13-verify "Design 2 again" gap 2, T-765: the doctor checks the kinds the project's models use (a level's,
// a role's, an agent's); another registered kind is "not used", never a WARN or FAIL. A Claude-only project with no
// OpenCode entry is OK; a role of an OpenCode model makes OpenCode used (checked: registered per start, T-809); a role
// with no model runs agents.Default.
func TestSetupMCPDoctorUsedKinds(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeOnly := testkit.Set("agents.difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
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

	x.Config(claudeOnly, testkit.Set("agents.models", `{"muse": {"kind": "opencode", "id": "x"}}`), testkit.Set("agents.roles.reviewer", "muse"))
	out, rc = x.run(t, "mcp")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("an opencode reviewer, no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp opencode: registered per start (OPENCODE_CONFIG", "a role's kind is checked")
	has(t, out, "mcp claude: registered", "claude models of the levels, and the lead has no model: agents.Default")
	has(t, out, "mcp codex: not used", "codex still unused")
}

// T-445, T-765: `obor setup mcp --model <name>` says whether a start of that model gives its agent obor's server, from
// its effective start args alone (no agent started, no `claude -p`): a Claude start with --strict-mcp-config (in
// agents.kinds.claude.args) gets obor's --mcp-config (an explicit obor entry there wins), one without it the plugin
// manifest, Codex the launch args, OpenCode its generated config (T-809); a kind obor registers nothing for fails, as
// does an unknown model.
func TestSetupMCPLane(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("agents.models", `{"muse": {"kind": "opencode", "id": "x"}, "cur": {"kind": "cursor", "id": "c"}}`))
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}

	// A lane's Claude start is strict (T-770): obor adds the kit's own on the start, the plugin manifest's hidden.
	has(t, x.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "a lane's start: strict, obor adds --mcp-config")
	// With the flag in the args, the same: it is there once.
	strict := newFx(t, testkit.Set("agents.kinds.claude.args", `["--strict-mcp-config"]`))
	has(t, strict.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "strict flag: obor adds --mcp-config")
	// An explicit obor entry in the args wins: no second one.
	mine := newFx(t, testkit.Set("agents.kinds.claude.args", `["--strict-mcp-config", "--mcp-config", "{\"mcpServers\":{\"obor\":{\"command\":\"/x/bin/obor\"}}}"]`))
	has(t, mine.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (the type's args name an obor server in --mcp-config)", "explicit entry")
	// Codex gets it from the launch args.
	has(t, x.ok(t, "mcp", "--model", "luna"), "mcp model luna (codex): registered per start (launch args", "codex")

	// OpenCode: each start's generated config (T-809), no user config entry needed.
	if _, err := os.Stat(filepath.Join(conf, "opencode", "opencode.json")); err == nil {
		t.Fatal("precondition: no opencode user config")
	}
	out := x.ok(t, "mcp", "--model", "muse")
	has(t, out, "mcp model muse (opencode): registered per start (OPENCODE_CONFIG (the start's generated config: "+
		filepath.Join(kit, "bin", "obor")+" mcp, plugin "+filepath.Join(kit, "opencode")+"))", "the generated config")
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
		x := newFx(t, testkit.Set("agents.kinds.claude.args", args))
		x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
		has(t, x.ok(t, "mcp", "--model", "opus"),
			"mcp model opus (claude): registered per start (the type's args name an obor server in --mcp-config)", name)
	}
	// The control: no obor value, so obor adds its own --mcp-config.
	x := newFx(t, testkit.Set("agents.kinds.claude.args", `["--strict-mcp-config", "--mcp-config", `+jsonStr(otherEntry)+`]`))
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	has(t, x.ok(t, "mcp", "--model", "opus"),
		"mcp model opus (claude): registered per start (--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config "+
			claude.MCPConfig(kit)+")", "no obor value: obor adds its own")
}

// jsonStr is s as a JSON string literal, as the config holds kinds args.
func jsonStr(s string) string { return config.Compact(s) }

// T-1119: obor setup agents names each kind; a kind installs nothing user-global, so each has nothing to install.
// Not parallel: the config load registers its plugin kinds (OpenCode's) in the process registry.
func TestSetupAgentsEveryKind(t *testing.T) {
	x := newFx(t)
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	out := x.ok(t, "agents")
	has(t, out, "agents claude: nothing to install", "claude: the manifest ships with the kit")
	has(t, out, "agents codex: nothing to install", "codex: launch args")
	// T-835: the global retire step is gone; opencode installs nothing user-global (its start's generated config, T-809).
	has(t, out, "agents opencode: nothing to install", "opencode: nothing user-global")
	if !strings.Contains(Usage(), "agents |") {
		t.Errorf("usage names agents: %s", Usage())
	}
}

// TestSetupAgentsPluginKind (T-1036): obor setup agents describes a plugin kind the config names, caches its manifest in
// the state dir and installs what its install call returns; an install that fails is a FAIL naming the kind and the
// call. Not parallel: the load registers the plugin kind in the process's registry.
func TestSetupAgentsPluginKind(t *testing.T) {
	x := newFx(t, testkit.Set("agents.kinds.mine", map[string]any{"plugin": "obor-kind-mine"}))
	t.Cleanup(func() { agents.SetPlugins(nil) }) // the parallel tests after this one load no config
	cmd := filepath.Join(x.Root, "obor-kind-mine")
	manifest := `{"version":1,"name":"mine","status":"terminal"}`
	script := "#!/bin/sh\ncat >/dev/null\ncase $1 in describe) echo '" + manifest + "';;\n" +
		"install) if [ -e " + cmd + ".broken ]; then echo nope >&2; exit 2; fi; echo '{\"version\":1,\"mcp\":{\"where\":\"start\"}}';;\nesac\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(x.Root, ".obor", "state", "kinds", "mine.json")
	if _, err := os.Stat(cache); err == nil {
		t.Fatal("precondition: a manifest cached before setup agents")
	}
	out := x.ok(t, "agents")
	has(t, out, "OK agents mine: manifest of "+cmd+" cached (.obor/state/kinds/mine.json)", "the manifest cached")
	has(t, out, "agents mine: nothing to install", "its install: per start")
	if b, err := os.ReadFile(cache); err != nil || !strings.Contains(string(b), `"name": "mine"`) {
		t.Errorf("cache %s: %s, %v", cache, b, err)
	}
	if err := os.WriteFile(cmd+".broken", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	x.failing(t, "FAIL agents mine: kind mine: install: exit 2: nope", "agents")
}

// T-1046: obor setup agents on a config from before OpenCode's binary (no agents.kinds.opencode.plugin) writes the line
// into .obor/config.json (uncommitted) and caches the kit kind's manifest; a second run, the line there, writes nothing.
func TestSetupAgentsWritesTheKitKind(t *testing.T) {
	t.Parallel()
	x := newFx(t, func(c map[string]any) { delete(c["agents"].(map[string]any), "kinds") })
	path := filepath.Join(x.Root, ".obor", "config.json")
	cache := filepath.Join(x.Root, ".obor", "state", "kinds", "opencode.json")
	if b, _ := os.ReadFile(path); strings.Contains(string(b), `"kinds"`) || x.Exists(cache) {
		t.Fatalf("precondition: no kinds in the config, no cache: %s", b)
	}
	out := x.ok(t, "agents")
	has(t, out, "config agents.kinds.opencode.plugin = obor-kind-opencode (uncommitted: the kit's binary)", "the line written")
	has(t, out, "OK agents opencode: manifest of ", "the kit kind's manifest cached")
	var doc struct {
		Agents struct {
			Kinds map[string]struct{ Plugin string } `json:"kinds"`
		} `json:"agents"`
	}
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &doc) != nil || doc.Agents.Kinds["opencode"].Plugin != "obor-kind-opencode" {
		t.Errorf("config after setup agents: %s, %v", b, err)
	}
	if b, err := os.ReadFile(cache); err != nil || !strings.Contains(string(b), `"name": "opencode"`) {
		t.Errorf("cache %s: %s, %v", cache, b, err)
	}
	hasNot(t, x.ok(t, "agents"), "config agents.kinds.opencode.plugin", "a config that names it is left alone")
}

// T-1081 (DD-v0.32 #1): on a config that still names keys this obor does not know (the promotion's, T-1076), obor
// setup agents still writes agents.kinds.opencode.plugin and caches the manifest (it used to skip both without a
// word), and the file keeps those keys' bytes.
func TestSetupAgentsKeepsUnknownKeys(t *testing.T) {
	t.Parallel()
	x := newFx(t, func(c map[string]any) { delete(c["agents"].(map[string]any), "kinds") }, testkit.Set("branches.promote", "release"),
		testkit.Set("merge.promote_weight", 2), testkit.Set("merge.weights", map[string]any{"core": 3}))
	path := filepath.Join(x.Root, ".obor", "config.json")
	cache := filepath.Join(x.Root, ".obor", "state", "kinds", "opencode.json")
	members := []string{`"promote":"release"`, `"promote_weight":2`, `"weights":{"core":3}`}
	before, _ := os.ReadFile(path)
	compact := func(b []byte) string { return strings.Join(strings.Fields(string(b)), "") }
	for _, m := range members {
		if !strings.Contains(compact(before), m) {
			t.Fatalf("precondition: %s in the config: %s", m, before)
		}
	}
	if strings.Contains(string(before), `"kinds"`) || x.Exists(cache) {
		t.Fatalf("precondition: no kinds in the config, no cache: %s", before)
	}
	out := x.ok(t, "agents")
	has(t, out, "config agents.kinds.opencode.plugin = obor-kind-opencode (uncommitted: the kit's binary)", "the line written")
	has(t, out, "OK agents opencode: manifest of ", "the kit kind's manifest cached")
	after, err := os.ReadFile(path)
	var doc struct {
		Agents struct {
			Kinds map[string]struct{ Plugin string } `json:"kinds"`
		} `json:"agents"`
	}
	if err != nil || json.Unmarshal(after, &doc) != nil || doc.Agents.Kinds["opencode"].Plugin != "obor-kind-opencode" {
		t.Errorf("config after setup agents: %s, %v", after, err)
	}
	for _, m := range members {
		if !strings.Contains(compact(after), m) {
			t.Errorf("unknown key %s dropped by setup agents:\n%s", m, after)
		}
	}
	// Byte for byte: the members holding them (branches, merge) are their old bytes (the patch keeps every member).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(before, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"branches", "merge"} {
		if !strings.Contains(string(after), `"`+k+`": `+string(top[k])) {
			t.Errorf("%s not kept byte for byte:\nbefore %s\nafter %s", k, top[k], after)
		}
	}
}

// T-1045: the doctor prints a used kind's install warnings (codex: quarantined files beside its binary) as WARN lines
// and still ends OK (a warning, never a failure); a kind the project does not run is not checked.
func TestSetupMCPInstallWarning(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeOnly := testkit.Set("agents.difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
	const host = "/c/0.167.0/bin/codex-code-mode-host"
	asked := 0
	x.install = func() agents.InstallHost {
		return agents.InstallHost{GOOS: "darwin",
			LookPath:   func(string) (string, error) { asked++; return "/c/0.167.0/bin/codex", nil },
			Quarantine: func(p string) (uint32, bool) { return 0x0181, p == host }}
	}
	x.Config(claudeOnly)
	out, rc := x.run(t, "mcp")
	if rc != 0 || asked != 0 {
		t.Errorf("codex unused: exit %d, install checked %d times\n%s", rc, asked, out)
	}
	hasNot(t, out, "WARN codex", "an unused kind's install is not checked")

	x.Config(claudeOnly, testkit.Set("agents.roles.reviewer", "luna"))
	out, rc = x.run(t, "mcp")
	if asked == 0 {
		t.Fatalf("precondition: a codex reviewer makes codex used\n%s", out)
	}
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("a quarantined codex: exit %d, last %q (a warning, not a failure)\n%s", rc, last(out), out)
	}
	has(t, out, "WARN codex: "+host+" carry com.apple.quarantine", "the quarantined file is named")
	has(t, out, "fix: xattr -dr com.apple.quarantine /c/0.167.0", "the fix on the version dir")
}
