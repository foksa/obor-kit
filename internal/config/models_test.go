package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
)

// T-765: each built-in model resolves to its kind, its model id and its kind's base args, the model as the kind takes
// it (claude --model, codex -m, opencode --model).
func TestBuiltinModelsResolve(t *testing.T) {
	c := load(t, `{"prefix":"pj"}`, "")
	want := map[string]struct {
		kind, id string
		args     []string
	}{
		"opus":     {"claude", "opus", []string{"--permission-mode", "auto", "--model", "opus"}},
		"sonnet":   {"claude", "sonnet", []string{"--permission-mode", "auto", "--model", "sonnet"}},
		"haiku":    {"claude", "haiku", []string{"--permission-mode", "auto", "--model", "haiku"}},
		"fable":    {"claude", "fable", []string{"--permission-mode", "auto", "--model", "fable"}},
		"deepseek": {"opencode", "opencode-go/deepseek-v4.1-flash", []string{"--auto", "--standalone", "--model", "opencode-go/deepseek-v4.1-flash"}},
		"terra":    {"codex", "gpt-6.1-terra", []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6.1-terra"}},
		"sol":      {"codex", "gpt-6.1-sol", []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6.1-sol"}},
		"luna":     {"codex", "gpt-6-luna", []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6-luna"}},
	}
	if len(BuiltinModels) != len(want) {
		t.Fatalf("BuiltinModels has %d models, the test names %d", len(BuiltinModels), len(want))
	}
	for _, b := range BuiltinModels {
		w := want[b.Name]
		m, ok := c.Model(b.Name)
		if !ok || m.Name != b.Name || m.Kind != w.kind || m.ID != w.id || !slices.Equal(m.Args, w.args) {
			t.Errorf("Model(%s) = %+v, %v; want kind %s, id %s, args %q", b.Name, m, ok, w.kind, w.id, w.args)
		}
	}
	if !slices.Equal(c.ModelNames(), []string{"opus", "sonnet", "haiku", "fable", "deepseek", "terra", "sol", "luna"}) {
		t.Errorf("ModelNames = %q", c.ModelNames())
	}
}

// T-765: the effort of a model ref is its kind's own flag at start (claude --effort, codex -c
// model_reasoning_effort=), each kind's own values (OpenCode's, a plugin kind: TestOpencodeModelEffort).
func TestModelEffortPerKind(t *testing.T) {
	c := load(t, `{"prefix":"pj"}`, "")
	for _, tc := range []struct {
		model, effort, inArgs string
	}{
		{"opus", "high", "--effort high"},
		{"terra", "xhigh", "-c model_reasoning_effort=xhigh"},
		{"terra", "max", "-c model_reasoning_effort=max"},
		{"terra", "ultra", "-c model_reasoning_effort=ultra"},
	} {
		m, _ := c.Model(tc.model)
		if err := c.CheckModelRef(tc.model + ":" + tc.effort); err != nil {
			t.Errorf("%s:%s refused: %v", tc.model, tc.effort, err)
		}
		if got := strings.Join(launchArgs(t, m, "lead", tc.effort), " "); !strings.Contains(got, tc.inArgs) {
			t.Errorf("%s at %s: args %q, want %q in them", tc.model, tc.effort, got, tc.inArgs)
		}
		if got := strings.Join(launchArgs(t, m, "lead", ""), " "); strings.Contains(got, "effort") {
			t.Errorf("%s with no effort: args %q set one (the kind's default stands)", tc.model, got)
		}
	}
	for ref, refusal := range map[string]string{
		"opus:minimal": `effort "minimal" is not one of claude's`,
		"luna:minimal": `effort "minimal" is not one of codex's (low, medium, high, xhigh, max, ultra)`,
	} {
		if err := c.CheckModelRef(ref); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Errorf("CheckModelRef(%s) = %v, want %q", ref, err, refusal)
		}
	}
}

// T-765: kinds.<kind>.args are appended after the model, to every model of the kind and no other; kinds.<kind>
// .prompt_extra is the work job's for each model of the kind.
func TestKindsArgsAppended(t *testing.T) {
	c := load(t, `{"prefix":"pj","kinds":{"claude":{"args":["--strict-mcp-config"],"prompt_extra":"Quote failures only."},
		"codex":{"args":["-c","sandbox_workspace_write.network_access=true"]}}}`, "")
	opus, _ := c.Model("opus")
	if want := []string{"--permission-mode", "auto", "--model", "opus", "--strict-mcp-config"}; !slices.Equal(opus.Args, want) {
		t.Errorf("opus args = %q, want %q", opus.Args, want)
	}
	if opus.PromptExtra != "Quote failures only." {
		t.Errorf("opus prompt_extra = %q", opus.PromptExtra)
	}
	sol, _ := c.Model("sol")
	if want := []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6.1-sol", "-c", "sandbox_workspace_write.network_access=true"}; !slices.Equal(sol.Args, want) {
		t.Errorf("sol args = %q, want %q", sol.Args, want)
	}
	if sol.PromptExtra != "" {
		t.Errorf("sol prompt_extra = %q, want none (claude's is claude's)", sol.PromptExtra)
	}
	if ds, _ := c.Model("deepseek"); slices.Contains(ds.Args, "--strict-mcp-config") {
		t.Errorf("deepseek args = %q: another kind's args", ds.Args)
	}
}

// T-810: Codex starts with --no-daemon, once, from the base args (its shared background server would give the MCP
// server and hooks the daemon's env and cwd). Workers and the reviewer get it the same way, and
// kinds.codex.args still follow it.
func TestCodexStartsWithNoDaemon(t *testing.T) {
	c := load(t, `{"prefix":"pj","kinds":{"codex":{"args":["-c","sandbox_workspace_write.network_access=true"]}}}`, "")
	for _, name := range []string{"terra", "sol", "luna"} {
		m, ok := c.Model(name)
		if !ok {
			t.Fatalf("Model(%s) is not known", name)
		}
		for _, role := range []string{"worker", "reviewer"} {
			args := launchArgs(t, m, role, "")
			if n := strings.Count(" "+strings.Join(args, " ")+" ", " --no-daemon "); n != 1 {
				t.Errorf("%s for %s: args %q carry --no-daemon %d times, want once", name, role, args, n)
				continue
			}
			noDaemon, kindsArgs := slices.Index(args, "--no-daemon"), slices.Index(args, "sandbox_workspace_write.network_access=true")
			if kindsArgs < 0 || noDaemon > kindsArgs {
				t.Errorf("%s for %s: args %q, want kinds.codex.args after --no-daemon", name, role, args)
			}
		}
	}
	for _, name := range []string{"opus", "deepseek"} {
		m, _ := c.Model(name)
		if slices.Contains(launchArgs(t, m, "reviewer", ""), "--no-daemon") {
			t.Errorf("%s: args %q carry Codex's --no-daemon", name, m.Args)
		}
	}
}

// T-765: models.<name> adds a model or overrides a built-in one (its kind kept when the entry names none; model ""
// starts the kind without a model).
func TestModelsOverride(t *testing.T) {
	c := load(t, `{"prefix":"pj","models":{"opus":{"model":"claude-opus-5-5"},"muse":{"kind":"opencode","model":""},
		"deepseek":{"kind":"opencode","model":"opencode-go/kimi"},"gem":{"kind":"codex","model":"gpt-x"}}}`, "")
	for _, tc := range []struct {
		name, kind, id string
		args           []string
	}{
		{"opus", "claude", "claude-opus-5-5", []string{"--permission-mode", "auto", "--model", "claude-opus-5-5"}},
		{"muse", "opencode", "", []string{"--auto", "--standalone"}},
		{"deepseek", "opencode", "opencode-go/kimi", []string{"--auto", "--standalone", "--model", "opencode-go/kimi"}},
		{"gem", "codex", "gpt-x", []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-x"}},
	} {
		m, ok := c.Model(tc.name)
		if !ok || m.Kind != tc.kind || m.ID != tc.id || !slices.Equal(m.Args, tc.args) {
			t.Errorf("Model(%s) = %+v, %v; want kind %s id %q args %q", tc.name, m, ok, tc.kind, tc.id, tc.args)
		}
	}
	if got := c.ModelNames(); !slices.Equal(got, []string{"opus", "sonnet", "haiku", "fable", "deepseek", "terra", "sol", "luna", "muse", "gem"}) {
		t.Errorf("ModelNames = %q, want the built-in ones, then models' new ones", got)
	}
}

// T-765: a ref naming no model, or an effort its kind rejects, is refused with the known names.
func TestUnknownModelRefused(t *testing.T) {
	c := load(t, `{"prefix":"pj","models":{"muse":{"kind":"opencode","model":""}}}`, "")
	if _, ok := c.Model("gpt"); ok {
		t.Error("Model(gpt) is known")
	}
	for ref, refusal := range map[string]string{
		"gpt":       `"gpt" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna, muse)`,
		"gpt:high":  `"gpt" is not a model`,
		"opus:":     "names no effort after the colon",
		"sol:turbo": `effort "turbo" is not one of codex's (low, medium, high, xhigh, max, ultra)`,
	} {
		if err := c.CheckModelRef(ref); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Errorf("CheckModelRef(%s) = %v, want %q", ref, err, refusal)
		}
	}
}

// T-765: lanes is a count: L-01 … L-NN under lanes_dir, nothing else is a lane.
func TestLanesCount(t *testing.T) {
	c := load(t, `{"prefix":"pj","lanes":3}`, "")
	var names []string
	for _, l := range c.Lanes() {
		names = append(names, l.Name)
		if l.Dir != c.LanesDir()+"/"+l.Name {
			t.Errorf("%s dir = %s", l.Name, l.Dir)
		}
	}
	if !slices.Equal(names, []string{"L-01", "L-02", "L-03"}) {
		t.Errorf("Lanes = %q", names)
	}
	if !c.HasLane("L-03") || c.HasLane("L-04") || c.HasLane("O-01") {
		t.Error("HasLane: L-01 … L-03 only")
	}
	if got := c.AgentName("worker", "L-02"); got != "pj-l-02" {
		t.Errorf("L-02's agent = %q, want pj-l-02", got)
	}
	if none := load(t, `{"prefix":"pj"}`, ""); len(none.Lanes()) != 0 {
		t.Errorf("no lanes key: %d lanes", len(none.Lanes()))
	}
	if LaneName(12) != "L-12" || LaneName(100) != "L-100" {
		t.Errorf("LaneName: %s %s", LaneName(12), LaneName(100))
	}
}

// T-1155: a kit kind (OpenCode) with no agents.kinds.<name>.plugin runs the kit's binary, as before T-1153, and is a
// plugin kind of the config; a set plugin still overrides it.
func TestKitKindDefaultCommand(t *testing.T) {
	c := load(t, `{"prefix":"pj"}`, "")
	if want := KitKind("obor-kind-opencode"); c.KindCommand("opencode") != want {
		t.Errorf("KindCommand(opencode) with no plugin = %q, want the kit's binary %q", c.KindCommand("opencode"), want)
	}
	if got := c.PluginKinds(); !slices.Contains(got, "opencode") {
		t.Errorf("PluginKinds = %q, want opencode (a kit kind with no plugin is one)", got)
	}
	if got := c.KindCommand("claude"); got != "" {
		t.Errorf("KindCommand(claude) = %q, want none (a compiled kind is no plugin kind)", got)
	}
	own := load(t, `{"prefix":"pj","kinds":{"opencode":{"plugin":"tools/oc"}}}`, "")
	if want := filepath.Join(own.Root, "tools", "oc"); own.KindCommand("opencode") != want {
		t.Errorf("KindCommand(opencode) = %q, want the set plugin %q", own.KindCommand("opencode"), want)
	}
}

// launchArgs is m.Launch's args of a start with no kit, dir, write dirs or prompt (the type's launch args); an error
// fails the test (a compiled kind's start has none).
func launchArgs(t *testing.T, m Model, role, effort string) []string {
	t.Helper()
	l, err := m.Launch(role, agents.Start{Effort: effort})
	if err != nil {
		t.Fatalf("Launch(%s, %s): %v", role, effort, err)
	}
	return l.Args
}
