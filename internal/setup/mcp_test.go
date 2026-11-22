package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/config"
	"obor/internal/testkit"
)

// the doctor checks the kinds the project's models use (a level's,
// a role's, an agent's); another registered kind is "not used", never a WARN or FAIL. A Claude-only project with no
// OpenCode entry is OK; a role of an OpenCode model makes OpenCode used (checked: registered per start); a role
// with no model runs agents.Default.
func TestSetupMCPDoctorUsedKinds(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeOnly := testkit.Set("agents.difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
	x.Config(claudeOnly)
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}
	if cfg, err := config.Load(x.Root); err != nil {
		t.Fatal(err)
	} else if _, ok := cfg.Kind("opencode"); !ok {
		t.Fatal("precondition: opencode is a kind of the project's config")
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

	x.Config(claudeOnly, testkit.AddModels(`{"muse": {"kind": "opencode", "id": "x"}}`), testkit.Set("agents.roles.reviewer", "muse"))
	out, rc = x.run(t, "mcp")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("an opencode reviewer, no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp opencode: registered per start (OPENCODE_CONFIG", "a role's kind is checked")
	has(t, out, "mcp claude: registered", "claude models of the levels, and the lead has no model: agents.Default")
	has(t, out, "mcp codex: not used", "codex still unused")
}

// `obor setup mcp --model <name>` says whether a start of that model gives its agent obor's server, from
// its effective start args alone (no agent started, no `claude -p`): a Claude start with --strict-mcp-config (in
// agents.kinds.claude.args) gets obor's --mcp-config (an explicit obor entry there wins), one without it the plugin
// manifest, Codex the launch args, OpenCode its generated config; a kind obor registers nothing for fails, as
// does an unknown model.
func TestSetupMCPLane(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.AddModels(`{"muse": {"kind": "opencode", "id": "x"}, "cur": {"kind": "cursor", "id": "c"}}`))
	conf := filepath.Join(x.Base, "xdg")
	x.env = []string{"XDG_CONFIG_HOME=" + conf}

	// A lane's Claude start is strict: obor adds the kit's own on the start, the plugin manifest's hidden.
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

	// OpenCode: each start's generated config, no user config entry needed.
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
	has(t, x.failing(t, `FAIL mcp model nope: "nope" is not a model (agents.models: cur, deepseek, fable, haiku, luna, muse, opus, sol, sonnet, terra)`,
		"mcp", "--model", "nope"), `"nope" is not a model`, "an unknown model")
	x.failing(t, "FAIL "+Usage(), "mcp", "--model")
	x.failing(t, "FAIL "+Usage(), "mcp", "--lane", "opus")
	if !strings.Contains(Usage(), "mcp [--model <name>]") {
		t.Errorf("usage names --model: %s", Usage())
	}
}

// the doctor reads --mcp-config as the start does, every value up to the next option. Args whose obor entry
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

// obor setup agents names each kind; a kind installs nothing user-global, so each has nothing to install.
// Not parallel: the config load registers its plugin kinds (OpenCode's) in the process registry.
func TestSetupAgentsEveryKind(t *testing.T) {
	x := newFx(t)
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	out := x.ok(t, "agents")
	has(t, out, "agents claude: nothing to install", "claude: the manifest ships with the kit")
	has(t, out, "agents codex: nothing to install", "codex: launch args")
	// there is no global retire step; opencode installs nothing user-global (its start's generated config).
	has(t, out, "agents opencode: nothing to install", "opencode: nothing user-global")
	// agy registers through a project file, written only for a config with an agy model.
	has(t, out, "agents agy: not used (no agents.models entry of kind agy): .agents/mcp_config.json not written", "agy unused")
	if x.Exists(".agents/mcp_config.json") {
		t.Error("setup agents wrote agy's project file for a config with no agy model")
	}
	if !strings.Contains(Usage(), "agents |") {
		t.Errorf("usage names agents: %s", Usage())
	}
}

// obor setup agents adds an agents.models entry (config.UpgradeModels' kind and id) for each such name the levels or
// roles name without one, in that list's order after the entries the file has, and writes nothing else; a name no
// entry of that list has, and one nothing names, are not added; a second run writes nothing.
// Not parallel: the config load registers its plugin kinds (OpenCode's) in the process registry.
func TestSetupAgentsAddsTheModelsTheConfigNames(t *testing.T) {
	x := newFx(t)
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	doc := `{"version": 2, "project": {"name": "proj", "agent_prefix": "pj"},
  "agents": {"roles": {"lead": "opus:high", "integrator": "opus", "reviewer": "terra:xhigh"},
    "difficulty": {"hard": ["opus:high"], "medium": ["kimi", "sonnet"], "simple": ["mystery", "sonnet:low"]},
    "models": {"kimi": {"kind": "opencode", "id": "opencode-go/kimi"}}}}
`
	x.Write(".obor/config.json", doc)
	out := x.ok(t, "agents")
	has(t, out, "config agents.models = + opus, sonnet, terra (uncommitted: commit it)", "the named models added")
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelNames(); !slices.Equal(got, []string{"kimi", "opus", "sonnet", "terra"}) {
		t.Errorf("agents.models = %q, want kimi kept, then opus, sonnet, terra (not haiku: nothing names it; not mystery)", got)
	}
	for _, name := range []string{"opus", "sonnet", "terra"} {
		i := slices.IndexFunc(config.UpgradeModels, func(e config.NamedModel) bool { return e.Name == name })
		if m, ok := cfg.Model(name); i < 0 || !ok || m.Kind != config.UpgradeModels[i].Kind || m.ID != config.UpgradeModels[i].ID {
			t.Errorf("model %s = %+v, %v; want the upgrade list's kind and id", name, m, ok)
		}
	}
	if err := cfg.CheckModelRef("mystery"); err == nil {
		t.Error("mystery resolves: the step added a name no upgrade entry has")
	}
	var before, after map[string]any
	if json.Unmarshal([]byte(doc), &before) != nil || json.Unmarshal([]byte(x.Read(".obor/config.json")), &after) != nil {
		t.Fatalf("the config does not parse:\n%s", x.Read(".obor/config.json"))
	}
	delete(before["agents"].(map[string]any), "models")
	delete(after["agents"].(map[string]any), "models")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("setup agents wrote more than agents.models:\n%s", x.Read(".obor/config.json"))
	}

	written := x.Read(".obor/config.json")
	if out := x.ok(t, "agents"); strings.Contains(out, "config agents.models") || x.Read(".obor/config.json") != written {
		t.Errorf("a second run wrote the config:\n%s", out)
	}
}

// TestSetupAgentsPluginKind: obor setup agents describes a plugin kind the config names, caches its manifest in
// the state dir and installs what its install call returns; an install that fails is a FAIL naming the kind and the
// call.
func TestSetupAgentsPluginKind(t *testing.T) {
	x := newFx(t, testkit.Set("agents.kinds.mine", map[string]any{"plugin": "obor-kind-mine"}))
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

// obor setup agents caches the kit kind's manifest as the command the config resolves to
// (config.KindCommand: the kit's binary) and writes nothing to .obor/config.json — a shape-2 file
// gets no unneeded uncommitted edit. A file holding a key this obor does not know keeps its bytes too.
func TestSetupAgentsCachesTheKitKindWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"shape 2", `{"version": 2, "project": {"name": "proj", "agent_prefix": "pj"},` +
			` "branches": {"promote": "release"}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newFx(t)
			path := filepath.Join(x.Root, ".obor", "config.json")
			x.Write(".obor/config.json", tc.doc)
			cache := filepath.Join(x.Root, ".obor", "state", "kinds", "opencode.json")
			if x.Exists(cache) {
				t.Fatal("precondition: a manifest cached before setup agents")
			}
			out := x.ok(t, "agents")
			has(t, out, "OK agents opencode: manifest of ", "the kit kind's manifest cached")
			var c struct {
				Command string `json:"command"`
			}
			want := config.KitKind(config.KitKinds["opencode"])
			if b, err := os.ReadFile(cache); err != nil || json.Unmarshal(b, &c) != nil || c.Command != want {
				t.Errorf("cache %s: %s, %v; want the kit's binary %s", cache, b, err, want)
			}
			if b, err := os.ReadFile(path); err != nil || string(b) != tc.doc {
				t.Errorf("config after setup agents:\n%s, %v; want it unchanged", b, err)
			}
		})
	}
}

// the doctor prints a used kind's install warnings (codex: quarantined files beside its binary) as WARN lines
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

// obor setup agents writes agy's registration, mcpServers.obor naming obor on PATH, into the project's
// .agents/mcp_config.json when the config has an agy model, and its hooks, the named hook obor, into
// .agents/hooks.json: each file's other entries kept, each file's path added to rules.protected_files (all
// uncommitted); a second run finds them and writes nothing.
// Not parallel: the config load registers its plugin kinds in the process registry.
func TestSetupAgentsWritesAgysProjectFile(t *testing.T) {
	x := newFx(t, testkit.AddModels(`{"neon": {"kind": "agy", "id": "gemini-4-neon-high"}}`),
		testkit.Set("rules.protected_files", []string{"docs/design.md"}))
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	x.Write(".agents/mcp_config.json", `{"mcpServers": {"mine": {"command": "my-server"}}}`)
	x.Write(".agents/hooks.json", `{"mine": {"Stop": [{"command": "./mine.sh"}]}}`)
	out := x.ok(t, "agents")
	has(t, out, "agents agy: mcpServers.obor written in .agents/mcp_config.json (uncommitted: commit it)", "the entry written")
	has(t, out, "config rules.protected_files = + .agents/mcp_config.json (uncommitted: commit it)", "the file protected")
	has(t, out, "agents agy: obor written in .agents/hooks.json (uncommitted: commit it)", "the hooks entry written")
	has(t, out, "config rules.protected_files = + .agents/hooks.json (uncommitted: commit it)", "the hooks file protected")
	var hooks map[string]map[string]any
	if err := json.Unmarshal([]byte(x.Read(".agents/hooks.json")), &hooks); err != nil {
		t.Fatalf("the hooks file: %v\n%s", err, x.Read(".agents/hooks.json"))
	}
	if hooks["mine"] == nil || hooks["obor"]["PreToolUse"] == nil || hooks["obor"]["SessionStart"] == nil {
		t.Errorf("hooks.json = %v, want the project's own hook kept and obor's added", hooks)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(x.Read(".agents/mcp_config.json")), &doc); err != nil {
		t.Fatalf("the file: %v\n%s", err, x.Read(".agents/mcp_config.json"))
	}
	if want := map[string]map[string]any{"mine": {"command": "my-server"}, "obor": {"command": "obor", "args": []any{"mcp"}}}; !reflect.DeepEqual(doc.MCPServers, want) {
		t.Errorf("mcpServers = %v, want %v", doc.MCPServers, want)
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ProtectedFiles(); !slices.Equal(got, []string{"docs/design.md", ".agents/mcp_config.json", ".agents/hooks.json"}) {
		t.Errorf("rules.protected_files = %q, want the files added after the project's own", got)
	}
	file, hfile, conf := x.Read(".agents/mcp_config.json"), x.Read(".agents/hooks.json"), x.Read(".obor/config.json")
	out = x.ok(t, "agents")
	has(t, out, "OK agents agy: mcpServers.obor in .agents/mcp_config.json", "the second run finds the entry")
	has(t, out, "OK agents agy: obor in .agents/hooks.json", "the second run finds the hooks entry")
	if x.Read(".agents/mcp_config.json") != file || x.Read(".agents/hooks.json") != hfile || x.Read(".obor/config.json") != conf ||
		strings.Contains(out, "config rules") {
		t.Errorf("a second run wrote again:\n%s", out)
	}
}

// obor setup mcp checks a used agy's project file: missing, a WARN naming setup agents as the fix (the doctor
// fails); written, registered. The file names obor by PATH, so a PATH with no obor, or with another kit's, is a WARN;
// the kit's own bin/obor (through a symlink) is none.
// Not parallel: the config load registers its plugin kinds in the process registry.
func TestSetupMCPChecksAgysProjectFile(t *testing.T) {
	x := newFx(t, testkit.AddModels(`{"neon": {"kind": "agy", "id": "gemini-4-neon-high"}}`), testkit.Set("agents.roles.reviewer", "neon"))
	bin := filepath.Join(x.Base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg"), "PATH=" + bin}
	file := filepath.Join(x.Root, ".agents", "mcp_config.json")
	out, rc := x.run(t, "mcp")
	if rc == 0 {
		t.Errorf("no project file: exit 0\n%s", out)
	}
	has(t, out, "WARN mcp agy: not registered: no current mcpServers.obor in "+file+" (run obor setup agents in the project, then commit the file)", "missing")
	has(t, out, "WARN mcp agy: no obor on PATH", "no obor on PATH")
	x.ok(t, "agents")
	if err := os.Symlink(filepath.Join(kit, "bin", "obor"), filepath.Join(bin, "obor")); err != nil {
		t.Fatal(err)
	}
	out, rc = x.run(t, "mcp")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("written, the kit's obor on PATH: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp agy: registered ("+file+")", "registered")
	hasNot(t, out, "WARN mcp agy", "the kit's own obor: no warning")
	has(t, x.ok(t, "mcp", "--model", "neon"), "mcp model neon (agy): registered by the project's file ("+file+")", "a lane's start")
	os.Remove(filepath.Join(bin, "obor"))
	if err := os.WriteFile(filepath.Join(bin, "obor"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _ = x.run(t, "mcp")
	has(t, out, "WARN mcp agy: the obor on PATH ("+filepath.Join(bin, "obor")+") is not this kit's ("+filepath.Join(kit, "bin", "obor")+")", "another obor")
}
