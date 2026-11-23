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
	"obor/internal/project"
	"obor/internal/testkit"
)

// the doctor checks every kind any agents.models entry names (and agents.Default for a role with no model); another
// registered kind is "not used", never a WARN or FAIL. A Claude-only project with no OpenCode entry is OK; adding an
// OpenCode model to agents.models makes OpenCode checked (registered per start) even when no level or role names it.
func TestSetupMCPDoctorUsedKinds(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeModels := testkit.Set("agents.models", `{"opus": {"kind": "claude", "id": "opus"}, "sonnet": {"kind": "claude", "id": "sonnet"}, "haiku": {"kind": "claude", "id": "haiku"}}`)
	claudeOnly := testkit.Set("agents.difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
	x.Config(claudeModels, claudeOnly)
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
	has(t, out, "mcp opencode: not used (no agents.models entry of kind opencode)", "opencode is listed, not checked")
	has(t, out, "mcp codex: not used", "codex is listed, not checked")
	has(t, out, "mcp agy: not used", "agy is listed, not checked")
	hasNot(t, out, "WARN", "no warning for a kind the project does not run")

	x.Config(claudeModels, claudeOnly, testkit.AddModels(`{"muse": {"kind": "opencode", "id": "x"}}`))
	if slices.Contains(x.Cfg().UsedModels(), "muse") {
		t.Fatal("precondition: no level or role names muse")
	}
	out, rc = x.run(t, "mcp")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("an opencode model in agents.models, no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp opencode: registered per start (OPENCODE_CONFIG", "any agents.models entry's kind is checked")
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

// obor setup agents caches nothing for a kit kind (obor answers it in process) and writes nothing to
// .obor/config.json — a shape-2 file gets no unneeded uncommitted edit. A file holding a key this obor does not know
// keeps its bytes too.
func TestSetupAgentsCachesNoKitKind(t *testing.T) {
	x := newFx(t)
	doc := `{"version": 2, "project": {"name": "proj", "agent_prefix": "pj"}, "branches": {"promote": "release"}}` + "\n"
	path := filepath.Join(x.Root, ".obor", "config.json")
	x.Write(".obor/config.json", doc)
	cache := filepath.Join(x.Root, ".obor", "state", "kinds")
	if x.Exists(cache) {
		t.Fatal("precondition: a manifest cached before setup agents")
	}
	out := x.ok(t, "agents")
	if strings.Contains(out, "manifest of") || x.Exists(cache) {
		t.Errorf("setup agents cached a kit kind:\n%s", out)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != doc {
		t.Errorf("config after setup agents:\n%s, %v; want it unchanged", b, err)
	}
}

// the doctor prints a used kind's install warnings (codex: quarantined files beside its binary) as WARN lines
// and still ends OK (a warning, never a failure); a kind the project does not run is not checked.
func TestSetupMCPInstallWarning(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeModels := testkit.Set("agents.models", `{"opus": {"kind": "claude", "id": "opus"}, "sonnet": {"kind": "claude", "id": "sonnet"}, "haiku": {"kind": "claude", "id": "haiku"}}`)
	claudeOnly := testkit.Set("agents.difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
	const host = "/c/0.167.0/bin/codex-code-mode-host"
	asked := 0
	x.install = func() agents.InstallHost {
		return agents.InstallHost{GOOS: "darwin",
			LookPath:   func(string) (string, error) { asked++; return "/c/0.167.0/bin/codex", nil },
			Quarantine: func(p string) (uint32, bool) { return 0x0181, p == host }}
	}
	x.Config(claudeModels, claudeOnly)
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

// writeFakeObor writes an executable fake obor script at path with body (after #!/bin/sh).
func writeFakeObor(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// obor setup mcp checks an agy model's project files (.agents/mcp_config.json and .agents/hooks.json) even when no
// level or role uses that model: missing either, a WARN naming setup agents as the fix (the doctor fails); written,
// both registered. The files name obor by PATH, so a PATH with no obor, or with one running another kit, is a WARN;
// one running this kit is none.
// Not parallel: the config load registers its plugin kinds in the process registry.
func TestSetupMCPChecksAgysProjectFile(t *testing.T) {
	x := newFx(t, testkit.AddModels(`{"neon": {"kind": "agy", "id": "gemini-4-neon-high"}}`))
	if slices.Contains(x.Cfg().UsedModels(), "neon") {
		t.Fatal("precondition: no level or role uses neon")
	}
	bin := filepath.Join(x.Base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg"), "PATH=" + bin}
	file := filepath.Join(x.Root, ".agents", "mcp_config.json")
	hfile := filepath.Join(x.Root, ".agents", "hooks.json")
	out, rc := x.run(t, "mcp")
	if rc == 0 {
		t.Errorf("no project files: exit 0\n%s", out)
	}
	has(t, out, "WARN mcp agy: not registered: no current mcpServers.obor in "+file+" (run obor setup agents in the project, then commit the file)", "missing mcp")
	has(t, out, "WARN hooks agy: not registered: no current obor in "+hfile+" (run obor setup agents in the project, then commit the file)", "missing hooks")
	has(t, out, "WARN mcp agy: no obor on PATH", "no obor on PATH")
	x.ok(t, "agents")
	writeFakeObor(t, filepath.Join(bin, "obor"), "printf 'plugin root: %s\\n' '"+kit+"'\n")
	out, rc = x.run(t, "mcp")
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("written, the kit's obor on PATH: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp agy: registered ("+file+")", "registered mcp")
	has(t, out, "hooks agy: registered ("+hfile+")", "registered hooks")
	hasNot(t, out, "WARN", "the kit's own obor: no warning")
	laneOut := x.ok(t, "mcp", "--model", "neon")
	has(t, laneOut, "mcp model neon (agy): registered by the project's file ("+file+")", "a lane's start mcp")
	has(t, laneOut, "hooks model neon (agy): registered by the project's file ("+hfile+")", "a lane's start hooks")

	// Missing hooks file alone fails both the doctor and --model.
	if err := os.Remove(hfile); err != nil {
		t.Fatal(err)
	}
	out, rc = x.run(t, "mcp")
	if rc == 0 {
		t.Errorf("missing hooks file: exit 0\n%s", out)
	}
	has(t, out, "mcp agy: registered ("+file+")", "mcp still registered")
	has(t, out, "WARN hooks agy: not registered: no current obor in "+hfile+" (run obor setup agents in the project, then commit the file)", "missing hooks reported")
	out, rc = x.run(t, "mcp", "--model", "neon")
	if rc == 0 {
		t.Errorf("missing hooks file (--model): exit 0\n%s", out)
	}
	// The doctor's check start answers without the kind reading its hooks file: both files are its own checks.
	has(t, out, "mcp model neon (agy): registered by the project's file ("+file+")", "mcp still reported by --model")
	has(t, out, "WARN hooks model neon (agy): not registered: no current obor in "+hfile+" (run obor setup agents in the project, then commit the file)", "missing hooks reported by --model")
	hasNot(t, out, "kind agy: start:", "the start does not refuse it")

	x.ok(t, "agents")
	other := filepath.Join(x.Base, "other-kit")
	writeFakeObor(t, filepath.Join(bin, "obor"), "printf 'plugin root: %s\\n' '"+other+"'\n")
	out, _ = x.run(t, "mcp")
	has(t, out, "WARN mcp agy: the obor on PATH ("+filepath.Join(bin, "obor")+", kit "+other+") is not this kit (", "another obor")
}

// obor setup mcp --model <name> builds a lane's start as a check: its kind's start runs no CLI and obor writes none of
// the files it names, so the project's state dir stays as it was and the doctor reports an agy start's two project
// files, its MCP config and its hooks. A stale hooks file is that file's finding with its fix, never the start's
// refusal, and the doctor's exit is as for any other finding.
// Not parallel: the config load registers its plugin kinds in the process registry.
func TestSetupMCPLaneChecksTheStart(t *testing.T) {
	x := newFx(t, testkit.AddModels(`{"gemini": {"kind": "agy", "id": "gemini-4-neon-high"}}`))
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	x.ok(t, "agents") // writes agy's two project files
	file := filepath.Join(x.Root, ".agents", "mcp_config.json")
	hfile := filepath.Join(x.Root, ".agents", "hooks.json")
	state := filepath.Join(x.Root, ".obor", "state")
	names := func(t *testing.T) []string {
		t.Helper()
		entries, err := os.ReadDir(state)
		if os.IsNotExist(err) { // nothing cached: the kit's kinds are answered in process
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(entries))
		for i, e := range entries {
			out[i] = e.Name()
		}
		return out
	}
	before := names(t)
	out := x.ok(t, "mcp", "--model", "gemini")
	has(t, out, "mcp model gemini (agy): registered by the project's file ("+file+")", "the MCP config is reported")
	has(t, out, "hooks model gemini (agy): registered by the project's file ("+hfile+")", "the hooks file is reported")
	if last(out) != "OK mcp model gemini" {
		t.Errorf("result %q", last(out))
	}
	if after := names(t); !slices.Equal(before, after) {
		t.Errorf("the check wrote the state dir: %v, want %v", after, before)
	}

	// A stale hooks entry: the hooks file's finding, its fix named, the same exit as any finding; the MCP config
	// stays reported and the start does not refuse it.
	x.Write(".agents/hooks.json", `{"obor": {"PreToolUse": [{"command": "obor kind agy hook"}]}}`)
	out, rc := x.run(t, "mcp", "--model", "gemini")
	if rc != 1 || last(out) != "FAIL mcp model gemini" {
		t.Errorf("stale hooks: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp model gemini (agy): registered by the project's file ("+file+")", "the MCP config is still reported")
	has(t, out, "WARN hooks model gemini (agy): not registered: no current obor in "+hfile+" (run obor setup agents in the project, then commit the file)", "the stale hooks file is the finding")
	hasNot(t, out, "kind agy: start:", "the start does not refuse it")
}

// agy's project files name obor by PATH: `obor setup mcp --model <agy model>` and `obor setup agents`, which writes
// them, WARN once when PATH's obor is none, fails or prints no plugin root, or runs another kit, and say nothing when
// its help output names this kit (including a shim under another kit that hops to the checkout).
// Not parallel: the config load registers its plugin kinds in the process registry.
func TestSetupAgysProjectFilesWarnOnTheOborOnPATH(t *testing.T) {
	own := project.Physical(kit)
	// checkArgs verifies pathWarning's invocation contract: `obor help` with self-host vars unset and OBOR_NO_FETCH=1.
	const checkArgs = `[ "$1" = help ] || exit 9
[ -z "${OBOR_SELF_HOSTED+x}" ] || exit 10
[ -z "${OBOR_NO_SELF_HOST+x}" ] || exit 11
[ "${OBOR_NO_FETCH:-}" = 1 ] || exit 12
`
	for _, c := range []struct {
		name string
		// setup builds a test-owned PATH dir for a project at projectRoot and returns the exact expected WARN ("" none).
		setup func(t *testing.T, base, projectRoot string) (dir, want string)
	}{
		{"none on PATH", func(t *testing.T, _, _ string) (string, string) {
			return t.TempDir(), "WARN mcp agy: no obor on PATH: its agents' MCP server and hooks do not start"
		}},
		{"same kit", func(t *testing.T, _, _ string) (string, string) {
			dir := t.TempDir()
			writeFakeObor(t, filepath.Join(dir, "obor"), checkArgs+"printf 'plugin root: %s\\nusage: obor\\n' '"+kit+"'\n")
			return dir, ""
		}},
		{"other kit", func(t *testing.T, base, _ string) (string, string) {
			dir := t.TempDir()
			other := filepath.Join(base, "other-kit")
			obor := filepath.Join(dir, "obor")
			writeFakeObor(t, obor, checkArgs+"printf 'plugin root: %s\\n' '"+other+"'\n")
			return dir, "WARN mcp agy: the obor on PATH (" + obor + ", kit " + other + ") is not this kit (" + own + "): agy's hooks and MCP server run it"
		}},
		{"hops to checkout", func(t *testing.T, base, projectRoot string) (string, string) {
			// A symlinked shim under another kit hops to this kit when run in the project root with self-host vars unset.
			t.Setenv("OBOR_SELF_HOSTED", "1")
			t.Setenv(agents.Pin, "1")
			other := filepath.Join(base, "other-kit", "bin", "obor")
			writeFakeObor(t, other, checkArgs+
				"[ \"$(pwd -P)\" = '"+project.Physical(projectRoot)+"' ] || exit 13\n"+
				"printf 'plugin root: %s\\n' '"+kit+"'\n")
			dir := t.TempDir()
			if err := os.Symlink(other, filepath.Join(dir, "obor")); err != nil {
				t.Fatal(err)
			}
			return dir, ""
		}},
		{"failing script", func(t *testing.T, _, _ string) (string, string) {
			dir := t.TempDir()
			obor := filepath.Join(dir, "obor")
			writeFakeObor(t, obor, "echo boom >&2\nexit 1\n")
			return dir, "WARN mcp agy: could not tell which kit the obor on PATH (" + obor + ") runs (exit 1): agy's hooks and MCP server run it"
		}},
		{"no plugin root line", func(t *testing.T, _, _ string) (string, string) {
			dir := t.TempDir()
			obor := filepath.Join(dir, "obor")
			writeFakeObor(t, obor, "echo 'usage: obor'\n")
			return dir, "WARN mcp agy: could not tell which kit the obor on PATH (" + obor + ") runs (no plugin root line): agy's hooks and MCP server run it"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			const models = `{"neon": {"kind": "agy", "id": "gemini-4-neon-high"}}`
			// agy's files, written with this kit's obor on PATH: no warning
			x := newFx(t, testkit.AddModels(models), testkit.Set("agents.roles.reviewer", "neon"))
			sameDir := t.TempDir()
			writeFakeObor(t, filepath.Join(sameDir, "obor"), "printf 'plugin root: %s\\n' '"+kit+"'\n")
			x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg"), "PATH=" + sameDir}
			if out := x.ok(t, "agents"); strings.Contains(out, "WARN") {
				t.Fatalf("precondition: setup agents with this kit's obor warns nothing:\n%s", out)
			}
			dir, want := c.setup(t, x.Base, x.Root)
			x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg"), "PATH=" + dir}
			out := x.ok(t, "mcp", "--model", "neon")
			has(t, out, "mcp model neon (agy): registered by the project's file", "the registration")
			if want == "" {
				hasNot(t, out, "WARN", "no warning")
			} else if count(out, want) != 1 {
				t.Errorf("want one %q in\n%s", want, out)
			}
			// obor setup agents, which writes the files: one WARN, whichever file it writes
			y := newFx(t, testkit.AddModels(models), testkit.Set("agents.roles.reviewer", "neon"))
			dir, want = c.setup(t, y.Base, y.Root)
			y.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(y.Base, "xdg"), "PATH=" + dir}
			out = y.ok(t, "agents")
			if want == "" {
				hasNot(t, out, "WARN", "no warning")
			} else if count(out, want) != 1 {
				t.Errorf("setup agents: want one %q in\n%s", want, out)
			}
			if !y.Exists(".agents/mcp_config.json") || !y.Exists(".agents/hooks.json") {
				t.Errorf("the files are written either way:\n%s", out)
			}
		})
	}
}
