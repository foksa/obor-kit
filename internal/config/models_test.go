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

// models is an agents.models member naming one model of each kind and two more of codex: what the tests below start.
const models = `"models":{"opus":{"kind":"claude","id":"opus"},"deepseek":{"kind":"opencode","id":"opencode-go/deepseek-v4.1-flash"},` +
	`"terra":{"kind":"codex","id":"gpt-6.1-terra"},"sol":{"kind":"codex","id":"gpt-6.1-sol"},"luna":{"kind":"codex","id":"gpt-6-luna"}}`

// a model name resolves from agents.models only: with no entry no name is a model (a name obor init's starter list
// has too); each entry resolves to its kind, its id and its kind's base args, the id as the kind takes it (claude
// --model, codex -m, opencode --model), in config order.
func TestModelsResolveFromConfigOnly(t *testing.T) {
	none := load(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, "")
	for _, name := range []string{"opus", "sonnet", "deepseek", "terra"} {
		if m, ok := none.Model(name); ok {
			t.Errorf("Model(%s) with no agents.models = %+v, want no model", name, m)
		}
	}
	if got := none.ModelNames(); len(got) != 0 {
		t.Errorf("ModelNames with no agents.models = %q, want none", got)
	}
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{`+models+`}}`, "")
	for _, tc := range []struct {
		name, kind, id string
		args           []string
	}{
		{"opus", "claude", "opus", []string{"--permission-mode", "auto", "--model", "opus"}},
		{"deepseek", "opencode", "opencode-go/deepseek-v4.1-flash", []string{"--model", "opencode-go/deepseek-v4.1-flash"}},
		{"terra", "codex", "gpt-6.1-terra", []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6.1-terra"}},
	} {
		m, ok := c.Model(tc.name)
		if !ok || m.Name != tc.name || m.Kind != tc.kind || m.ID != tc.id || !slices.Equal(m.Args, tc.args) {
			t.Errorf("Model(%s) = %+v, %v; want kind %s, id %s, args %q", tc.name, m, ok, tc.kind, tc.id, tc.args)
		}
	}
	if _, ok := c.Model("sonnet"); ok {
		t.Error("Model(sonnet) resolves with no agents.models entry for it")
	}
	if got := c.ModelNames(); !slices.Equal(got, []string{"opus", "deepseek", "terra", "sol", "luna"}) {
		t.Errorf("ModelNames = %q, want agents.models' names in config order", got)
	}
}

// a model's id goes to its kind's args verbatim, so a version or a variant is one more entry: a versioned claude id,
// a bracketed variant, a codex id with its own suffix.
func TestModelVersionedIDVerbatim(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"models":{`+
		`"opus-4.6":{"kind":"claude","id":"claude-opus-4-6"},"opus-1m":{"kind":"claude","id":"opus[1m]"},`+
		`"atra":{"kind":"codex","id":"gpt-6.2-atra"}}}}`, "")
	for name, want := range map[string][]string{
		"opus-4.6": {"--permission-mode", "auto", "--model", "claude-opus-4-6"},
		"opus-1m":  {"--permission-mode", "auto", "--model", "opus[1m]"},
		"atra":     {"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6.2-atra"},
	} {
		m, ok := c.Model(name)
		if !ok {
			t.Fatalf("Model(%s) is not known", name)
		}
		if got := launchArgs(t, m, "worker", ""); !slices.Equal(got[:len(want)], want) {
			t.Errorf("%s: launch args %q, want them to start %q", name, got, want)
		}
	}
	if err := c.CheckModelRef("opus-4.6:high"); err != nil {
		t.Errorf("CheckModelRef(opus-4.6:high) = %v, want a model with its kind's effort", err)
	}
}

// the effort of a model ref is its kind's own flag at start (claude --effort, codex -c
// model_reasoning_effort=), each kind's own values (OpenCode's, a plugin kind: TestOpencodeModelEffort).
func TestModelEffortPerKind(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{`+models+`}}`, "")
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

// kinds.<kind>.args are appended after the model, to every model of the kind and no other; kinds.<kind>
// .prompt_extra is the work job's for each model of the kind.
func TestKindsArgsAppended(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},
		"agents":{`+models+`,"kinds":{"claude":{"args":["--strict-mcp-config"],"job_note":"Quote failures only."},
		"codex":{"args":["-c","sandbox_workspace_write.network_access=true"]}}}}`, "")
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

// Codex starts with --no-daemon, once, from the base args (its shared background server would give the MCP
// server and hooks the daemon's env and cwd). Workers and the reviewer get it the same way, and
// kinds.codex.args still follow it.
func TestCodexStartsWithNoDaemon(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},`+
		`"agents":{`+models+`,"kinds":{"codex":{"args":["-c","sandbox_workspace_write.network_access=true"]}}}}`, "")
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

// models.<name> names its kind and id: model "" starts the kind without a model; an entry with no kind is no model
// (UnknownModel names the missing kind).
func TestModelsEntries(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},
		"agents":{"models":{"opus":{"kind":"claude","id":"claude-opus-5-5"},"muse":{"kind":"opencode","id":""},
		"gem":{"kind":"codex","id":"gpt-x"},"bare":{"id":"x"}}}}`, "")
	for _, tc := range []struct {
		name, kind, id string
		args           []string
	}{
		{"opus", "claude", "claude-opus-5-5", []string{"--permission-mode", "auto", "--model", "claude-opus-5-5"}},
		{"muse", "opencode", "", []string{}},
		{"gem", "codex", "gpt-x", []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-x"}},
	} {
		m, ok := c.Model(tc.name)
		if !ok || m.Kind != tc.kind || m.ID != tc.id || !slices.Equal(m.Args, tc.args) {
			t.Errorf("Model(%s) = %+v, %v; want kind %s id %q args %q", tc.name, m, ok, tc.kind, tc.id, tc.args)
		}
	}
	if m, ok := c.Model("bare"); ok {
		t.Errorf("Model(bare) = %+v, want no model: its entry names no kind", m)
	}
	if err := c.CheckModelRef("bare"); err == nil || err.Error() != `"bare" names no kind: set agents.models.bare.kind (agy, claude, codex, opencode, pi)` {
		t.Errorf("CheckModelRef(bare) = %v, want the missing kind named", err)
	}
}

// a ref naming no model, or an effort its kind rejects, is refused with the config's names; a name obor setup agents
// would add (UpgradeModels) carries its entry ready to paste and names that command, and the paste makes it a model.
func TestUnknownModelRefused(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"models":{"muse":{"kind":"opencode","id":""},`+
		`"sol":{"kind":"codex","id":"gpt-6.1-sol"}}}}`, "")
	if _, ok := c.Model("gpt"); ok {
		t.Error("Model(gpt) is known")
	}
	for ref, refusal := range map[string]string{
		"gpt":       `"gpt" is not a model (agents.models: muse, sol)`,
		"gpt:high":  `"gpt" is not a model (agents.models: muse, sol)`,
		"opus:high": `"opus" is not a model (agents.models: muse, sol): add it to agents.models in .obor/config.json: "opus": {"kind": "claude", "id": "opus"} (obor setup agents adds every former built-in name the config uses)`,
		"deepseek":  `"deepseek" is not a model (agents.models: muse, sol): add it to agents.models in .obor/config.json: "deepseek": {"kind": "opencode", "id": "opencode-go/deepseek-v4.1-flash"} (obor setup agents adds every former built-in name the config uses)`,
		"sol:":      "names no effort after the colon",
		"sol:turbo": `effort "turbo" is not one of codex's (low, medium, high, xhigh, max, ultra)`,
	} {
		err := c.CheckModelRef(ref)
		if err == nil {
			t.Errorf("CheckModelRef(%s) passed, want %q", ref, refusal)
			continue
		}
		if strings.HasPrefix(refusal, `"`) && err.Error() != refusal || !strings.Contains(err.Error(), refusal) {
			t.Errorf("CheckModelRef(%s) = %v, want %q", ref, err, refusal)
		}
	}
	empty := load(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, "")
	if err := empty.CheckModelRef("gpt"); err == nil ||
		err.Error() != `"gpt" is not a model (agents.models: none)` {
		t.Errorf("CheckModelRef(gpt) with no agents.models = %v, want none listed", err)
	}
	if err := empty.CheckModelRef("opus"); err == nil ||
		err.Error() != `"opus" is not a model (agents.models: none): add it to agents.models in .obor/config.json: "opus": {"kind": "claude", "id": "opus"} (obor setup agents adds every former built-in name the config uses)` {
		t.Errorf("CheckModelRef(opus) with no agents.models = %v, want the entry to paste and obor setup agents named", err)
	}
	pasted := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"models":{`+
		`"opus": {"kind": "claude", "id": "opus"}}}}`, "")
	if err := pasted.CheckModelRef("opus:high"); err != nil {
		t.Errorf("the pasted entry: CheckModelRef(opus:high) = %v, want a model", err)
	}
}

// lanes.count is a count: L-01 … L-NN under lanes.dir, nothing else is a lane.
func TestLanesCount(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"count":3}}`, "")
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
	if none := load(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, ""); len(none.Lanes()) != 0 {
		t.Errorf("no lanes key: %d lanes", len(none.Lanes()))
	}
	if LaneName(12) != "L-12" || LaneName(100) != "L-100" {
		t.Errorf("LaneName: %s %s", LaneName(12), LaneName(100))
	}
}

// a kit kind with no agents.kinds.<name>.plugin, or naming its kit binary, is that binary's name as it is (obor answers
// it in process) and a plugin kind of the config; any other plugin is its path, relative to the root.
func TestKitKindCommand(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, "")
	if got := c.PluginKinds(); !slices.Contains(got, "opencode") || !slices.Contains(got, "agy") || !slices.Contains(got, "pi") {
		t.Errorf("PluginKinds = %q, want opencode, agy and pi (a kit kind with no plugin is one)", got)
	}
	if got := c.KindCommand("claude"); got != "" {
		t.Errorf("KindCommand(claude) = %q, want none (a compiled kind is no plugin kind)", got)
	}
	for _, kind := range []string{"opencode", "agy", "pi"} {
		bin := "obor-kind-" + kind
		for _, tc := range []struct{ plugin, want string }{
			{"", bin}, {bin, bin}, {"tools/oc", "tools/oc"}, {"/opt/" + bin, "/opt/" + bin},
		} {
			doc := `{"version":2,"project":{"agent_prefix":"pj"}}`
			if tc.plugin != "" {
				doc = `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"kinds":{"` + kind + `":{"plugin":"` + tc.plugin + `"}}}}`
			}
			c := load(t, doc, "")
			want := tc.want
			if tc.want == "tools/oc" {
				want = filepath.Join(c.Root, "tools", "oc")
			}
			if got := c.KindCommand(kind); got != want {
				t.Errorf("KindCommand(%s) with plugin %q = %q, want %q", kind, tc.plugin, got, want)
			}
		}
	}
}

// stateNamed is a start's state writer that writes nothing: the file's path is /state/<name>.
func stateNamed(name string, _ []byte) (string, error) { return "/state/" + name, nil }

// launchArgs is m.Launch's args of a start of agent a with no override file, kit, dir, write dirs or prompt (the type's
// launch args; a start file is named in /state, not written); an error fails the test (a compiled kind's start has
// none).
func launchArgs(t *testing.T, m Model, role, effort string) []string {
	t.Helper()
	l, err := m.Launch(role, nil, agents.Start{Agent: "a", Effort: effort, WriteState: stateNamed})
	if err != nil {
		t.Fatalf("Launch(%s, %s): %v", role, effort, err)
	}
	return l.Args
}
