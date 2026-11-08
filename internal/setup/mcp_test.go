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
// OpenCode entry is OK; a role of an OpenCode model makes OpenCode used (checked: registered per start, T-809); a role
// with no model runs agents.Default.
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
	if rc != 0 || last(out) != "OK mcp" {
		t.Errorf("an opencode reviewer, no opencode entry: exit %d, last %q\n%s", rc, last(out), out)
	}
	has(t, out, "mcp opencode: registered per start (OPENCODE_CONFIG", "a role's kind is checked")
	has(t, out, "mcp claude: registered", "claude models of the levels, and the lead has no model: agents.Default")
	has(t, out, "mcp codex: not used", "codex still unused")
}

// T-445, T-765: `obor setup mcp --model <name>` says whether a start of that model gives its agent obor's server, from
// its effective start args alone (no agent started, no `claude -p`): a Claude start with --strict-mcp-config (in
// kinds.claude.args) gets obor's --mcp-config (an explicit obor entry there wins), one without it the plugin manifest,
// Codex the launch args, OpenCode its generated config (T-809); a kind obor registers nothing for fails, as does an unknown
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
// The other spelling of the file (AltPath) is edited when it is the only one there; a config with comments, or one that
// is no JSON, is left alone (T-835: the old global opencode entry was the last AltPath registration).
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

	// A registration with an AltPath (the other spelling of the file) edits it when it is the only one there.
	alt := m
	alt.Path, alt.AltPath = filepath.Join(filepath.Dir(path), "opencode.json"), filepath.Join(filepath.Dir(path), "opencode.jsonc")
	if _, err := os.Stat(alt.Path); err == nil {
		t.Fatalf("precondition: %s is not there", alt.Path)
	}
	if err := os.WriteFile(alt.AltPath, []byte(`{"theme":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := s.configMCP("cur", alt, false); err != nil {
		t.Fatal(err)
	}
	has(t, out.String(), "OK agents cur: mcpServers.obor set in "+alt.AltPath, "the alt file, said")
	if b, _ := os.ReadFile(alt.AltPath); anyJSON(string(b)) == nil || !strings.Contains(string(b), "/kit/bin/obor") {
		t.Errorf("the alt file got the entry: %s", b)
	}
	if _, err := os.Stat(alt.Path); err == nil {
		t.Errorf("the missing file %s is made: the alt one is edited instead", alt.Path)
	}

	// A config with comments is not edited: the paste block is printed and the step fails (comments would be lost).
	jsonc := filepath.Join(filepath.Dir(path), "commented.jsonc")
	commented := "// mine\n{\"mcpServers\":{}}\n"
	if err := os.WriteFile(jsonc, []byte(commented), 0o644); err != nil {
		t.Fatal(err)
	}
	alt.Path, alt.AltPath = jsonc, ""
	out.Reset()
	err := s.configMCP("cur", alt, false)
	if err == nil || err.Error() != "agents cur: "+jsonc+" is not edited (comments would be lost)" {
		t.Errorf("a config with comments: %v", err)
	}
	has(t, out.String(), "WARN agents cur: "+jsonc+" is not edited (comments would be lost)", "the comments case: warned")
	has(t, out.String(), "paste into "+jsonc+` under "mcpServers":`, "the comments case: the paste block")
	if b, _ := os.ReadFile(jsonc); string(b) != commented {
		t.Errorf("the commented config is not written: %q", b)
	}

	// A file that is no JSON at all is left alone, with the reason.
	broken := filepath.Join(filepath.Dir(path), "broken.json")
	alt.Path, alt.AltPath = broken, ""
	if err := os.WriteFile(broken, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.configMCP("cur", alt, false); err == nil || !strings.Contains(err.Error(), broken+": not valid JSON: left alone") {
		t.Errorf("a file that is no JSON: %v", err)
	}
	if b, _ := os.ReadFile(broken); string(b) != "{ not json" {
		t.Errorf("the broken file is not written: %q", b)
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
	// T-835: the global retire step is gone; opencode installs nothing user-global (its start's generated config, T-809).
	has(t, out, "agents opencode: nothing to install", "opencode: nothing user-global")
	if !strings.Contains(Usage(), "agents [--remove]") {
		t.Errorf("usage names agents: %s", Usage())
	}
}

// TestSetupAgentsPluginKind (T-1036): obor setup agents describes a plugin kind the config names, caches its manifest in
// the state dir and installs what its install call returns; an install that fails is a FAIL naming the kind and the
// call. Not parallel: the load registers the plugin kind in the process's registry.
func TestSetupAgentsPluginKind(t *testing.T) {
	x := newFx(t, testkit.Set("kinds.mine", map[string]any{"command": "obor-kind-mine"}))
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

// T-1045: the doctor prints a used kind's install warnings (codex: quarantined files beside its binary) as WARN lines
// and still ends OK (a warning, never a failure); a kind the project does not run is not checked.
func TestSetupMCPInstallWarning(t *testing.T) {
	t.Parallel()
	x := bare(t)
	claudeOnly := testkit.Set("difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["haiku"]}`)
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

	x.Config(claudeOnly, testkit.Set("roles.reviewer", "luna"))
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

// T-1049: xattr's com.apple.quarantine value carries its flags in hex before the first ';' (the time, agent and uuid
// follow): the user-approved bit's value reads as 0x01c1, and a value with no hex flags is no flag.
func TestQuarantineFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out    string
		flags  uint32
		wantOK bool
	}{
		{"0181;6af071dc;;d3b9-11ee\n", 0x0181, true},
		{"01c1;6af071dc;Safari;d3b9-11ee\n", 0x01c1, true},
		{"0083;\n", 0x0083, true},
		{"", 0, false},
		{"zz;6af071dc\n", 0, false},
	} {
		flags, ok := quarantineFlags(tc.out)
		if flags != tc.flags || ok != tc.wantOK {
			t.Errorf("quarantineFlags(%q) = %#x, %v; want %#x, %v", tc.out, flags, ok, tc.flags, tc.wantOK)
		}
	}
}
