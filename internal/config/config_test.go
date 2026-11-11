package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
)

func writeProject(t *testing.T, cfg, local string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cfg != "" {
		os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644)
	}
	if local != "" {
		os.WriteFile(filepath.Join(root, ".obor", "local.json"), []byte(local), 0o644)
	}
	return root
}

func load(t *testing.T, cfg, local string) *Config {
	t.Helper()
	c, err := Load(writeProject(t, cfg, local))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// raw is `obor config get <path>`'s output and whether the key exists.
func raw(c *Config, path string) (string, bool) {
	if _, ok := c.Get(path); !ok {
		return "", false
	}
	s, _ := c.Raw(path)
	return s, true
}

func TestDefaultsAndDerivedKeys(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"models":{"cx":{"kind":"codex","id":"gpt-x"}}}}`, "")
	for path, want := range map[string]string{
		".merge.target_branch":     "main",
		".lanes.task_branch":       "task/{id}",
		".project.task_id_pattern": "T-[0-9]+[a-z]?",
		".lanes.dir":               "../proj-wt",
	} {
		if got, ok := raw(c, path); !ok || got != want {
			t.Errorf("%s = %q (exists %v), want %q", path, got, ok, want)
		}
	}
	if want := filepath.Join(filepath.Dir(c.Root), "proj-wt", "merge"); c.ScratchDir() != want {
		t.Errorf("ScratchDir = %s, want %s", c.ScratchDir(), want)
	}
	if _, ok := c.Get(".agents.roles.leads"); ok {
		t.Error(".agents.roles.leads: no longer derived (T-467)")
	}
	if _, ok := c.Get(".agents.models.cx.settings"); ok {
		t.Errorf("the Remote Control default is applied at launch, not in the config (T-280)")
	}
	if c.LanesDir() != filepath.Join(filepath.Dir(c.Root), "proj-wt") {
		t.Errorf("LanesDir = %s", c.LanesDir())
	}
}

func TestLocalOverlay(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"nightly":{"at":"03:00"},
		"lanes":{"links":["A","B","C"],"count":4},
		"agents":{"kinds":{"claude":{"args":["--x"],"job_note":"P"}}},
		"merge":{"check":{"cmd":"make check","timeout":90},
		"by_path":{"rules":[{"name":"a","paths":["src/a/**"]}]}}}`,
		`{"nightly":{"at":"02:00"},"lanes":{"links":["X"],"count":2,"dir":"/wt"},
		"agents":{"kinds":{"claude":{"args":["--y"]}}},
		"merge":{"check":{"heavy":true},"by_path":{"rules":[{"name":"b","paths":["src/b/**"]}]}}}`)
	if n := c.Data.Nightly; n.At != "02:00" {
		t.Errorf("nightly: objects merge key by key, got %+v", n)
	}
	if !reflect.DeepEqual(c.Data.Lanes.Links, []string{"X"}) {
		t.Errorf("a list is replaced whole: %q", c.Data.Lanes.Links)
	}
	if k := c.Data.Agents.Kinds.M["claude"]; k.JobNote != "P" || !reflect.DeepEqual(k.Args, []string{"--y"}) {
		t.Errorf("a kind merges key by key, its args replaced whole: %+v", k)
	}
	if c.LaneCount() != 2 || len(c.Lanes()) != 2 {
		t.Errorf("local.json's lanes count replaces config.json's: %d", c.LaneCount())
	}
	if cmd, ok := c.Command("check"); !ok || cmd.Cmd != "make check" || cmd.Timeout != 90 || !cmd.Heavy {
		t.Errorf("a command merges: %+v", cmd)
	}
	if got := c.Data.Merge.ByPath.Rules; !reflect.DeepEqual(got, List[PathRule]{{Name: "b", Paths: []string{"src/b/**"}}}) {
		t.Errorf("a list of objects is replaced whole, no field of the old entry kept: %+v", got)
	}
	if c.LanesDir() != "/wt" || c.ScratchDir() != "/wt/merge" {
		t.Errorf("derived after the overlay: %s %s", c.LanesDir(), c.ScratchDir())
	}
}

// TestUnknownKeyFails: the strict validate load (LoadForValidate) fails each unknown key with its path and file, where
// Load (every command's) ignores it.
func TestUnknownKeyFails(t *testing.T) {
	for _, c := range []struct{ cfg, key string }{
		{`{"version":2,"project":{"agent_prefix":"pj"},"typo_key":1}`, "typo_key"},
		{`{"version":2,"project":{"agent_prefix":"pj"},"agents":{"roles":{"mystery":{"model":"opus","writes":[]}}}}`, "agents.roles.mystery.writes"},
		{`{"version":2,"project":{"agent_prefix":"pj"},"agents":{"models":{"m":{"kind":"claude","modle":"x"}}}}`, "agents.models.m.modle"},
		{`{"version":2,"project":{"agent_prefix":"pj"},"commands":{"c":{"cmd":"x","tmeout":5}}}`, "commands.c.tmeout"},
		{`{"version":2,"project":{"agent_prefix":"pj"},"agents":{"kinds":{"claude":{"argz":[]}}}}`, "agents.kinds.claude.argz"},
	} {
		root := writeProject(t, c.cfg, "")
		want := []string{"FAIL ." + c.key + ": unknown to this obor (retired, or newer than this binary): delete it (.obor/config.json)"}
		if cfg, fails, err := LoadForValidate(root); err != nil || cfg == nil || !reflect.DeepEqual(fails, want) {
			t.Errorf("%s: LoadForValidate %q, %v; want %q", c.cfg, fails, err, want)
		}
		if _, err := Load(root); err != nil {
			t.Errorf("%s: Load %v, want the key ignored", c.cfg, err)
		}
	}
	// local.json has no version of its own: a key it holds that the config does not know is an unknown key like any
	// other.
	_, fails, err := LoadForValidate(writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, `{"slots":{"maxx":1}}`))
	if err != nil || len(fails) != 1 || !strings.HasPrefix(fails[0], "FAIL .slots: ") || !strings.HasSuffix(fails[0], "(.obor/local.json)") {
		t.Errorf("local.json unknown key: %q, %v", fails, err)
	}
	// A key the shape still types: a wrong type fails, as a removed key (an `any`) does not.
	if _, err := Load(writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"handoff_tokens":"no"}}`, "")); err == nil ||
		!strings.Contains(err.Error(), "handoff_tokens") {
		t.Errorf("wrong type: %v", err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(writeProject(t, "", "")); !errors.Is(err, ErrNoConfig) {
		t.Errorf("no config: %v", err)
	}
	if _, err := Load(writeProject(t, "{", "")); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("bad JSON: %v", err)
	}
	if _, err := Load(writeProject(t, "[]", "")); err == nil {
		t.Error("array config: no error")
	}
	if _, err := Load(writeProject(t, "{}", "5")); err == nil {
		t.Error("local not an object: no error")
	}
}

func TestOrderedMapsKeepOrder(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},
		"agents":{"models":{"z":{"kind":"codex","id":"z1"},"a":{"kind":"codex","id":"a1"}},
		"kinds":{"opencode":{"args":["--o"]},"claude":{"args":["--c"]}}},
		"commands":{"zz":"z","aa":null,"mm":{"cmd":"m","timeout":5}}}`,
		`{"agents":{"models":{"b":{"kind":"codex","id":"b1"},"z":{"kind":"codex","id":"z2"}}}}`)
	for _, k := range []struct {
		got, want []string
	}{{c.Data.Agents.Models.Keys, []string{"z", "a", "b"}}, {c.Data.Agents.Kinds.Keys, []string{"opencode", "claude"}},
		{c.Data.Commands.Keys, []string{"zz", "aa", "mm"}}} {
		if !reflect.DeepEqual(k.got, k.want) {
			t.Errorf("keys = %q, want %q (a known key keeps its place, a new one is appended)", k.got, k.want)
		}
	}
	if m, _ := c.Model("z"); m.ID != "z2" {
		t.Errorf("models.z = %+v, want local.json's model", m)
	}
	got, _ := raw(c, ".commands")
	if want := "{\n  \"zz\": \"z\",\n  \"aa\": null,\n  \"mm\": {\n    \"cmd\": \"m\",\n    \"timeout\": 5\n  }\n}"; got != want {
		t.Errorf("config get .commands = %s, want %s", got, want)
	}
	if _, ok := c.Command("aa"); ok {
		t.Error("a null command is set")
	}
}

func TestGet(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"count":2},`+
		`"agents":{"roles":{"integrator":"opus:high"},"handoff_tokens":0}}`, "")
	for _, tc := range []struct {
		path, want string
		ok         bool
	}{
		{".project.agent_prefix", "pj", true},
		{".lanes.count", "2", true},
		{".agents.roles.integrator", "opus:high", true},
		{".agents.handoff_tokens", "0", true},
		{".agents.roles.leads", "", false},
		{".nope", "", false},
		{".project.agent_prefix.x", "", false},
		{".nightly.at", "", false},
	} {
		if got, ok := raw(c, tc.path); got != tc.want || ok != tc.ok {
			t.Errorf("get %s = %q, %v; want %q, %v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
	if all, ok := c.Raw("."); !ok || !strings.HasPrefix(all, "{\n  \"version\"") {
		t.Errorf("get . = %.40q", all)
	}
}

// TestDefaults: each default the callers read as a typed field: unset → the default, set → the value.
func TestDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, set string
		get       func(*Config) any
		def, val  any
	}{
		{"nightly.also_run", `"nightly":{"also_run":["trim"]}`, func(c *Config) any { return len(c.Data.Nightly.AlsoRun) }, 0, 1},
		{"agents.docs_dir", `"agents":{"docs_dir":"doc/roles"}`, func(c *Config) any { return c.Data.Agents.DocsDir }, "docs/agents", "doc/roles"},
	} {
		if got := tc.get(load(t, `{"version":2}`, "")); got != tc.def {
			t.Errorf("%s unset = %v, want the default %v", tc.name, got, tc.def)
		}
		if got := tc.get(load(t, `{"version":2,`+tc.set+`}`, "")); got != tc.val {
			t.Errorf("%s set = %v, want %v", tc.name, got, tc.val)
		}
	}
}

func TestRolesAndAgentNames(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"count":1},`+
		`"agents":{"roles":{"reviewer":"opus","deep_dive":"sonnet:high","integrator":"opus"}}}`, "")
	for _, tc := range []struct{ role, lane, want string }{
		{"worker", "L-01", "pj-l-01"}, {"reviewer", "", "pj-rev"}, {"lead", "", "pj-lead"}, {"integrator", "", "pj-int"},
		{"nobody", "", ""},
	} {
		if got := c.AgentName(tc.role, tc.lane); got != tc.want {
			t.Errorf("AgentName(%s) = %q, want %q", tc.role, got, tc.want)
		}
	}
	if got, ok := c.Role("reviewer"); !ok || got.Agent != "pj-rev" || got.Type != "opus" || !got.Configured {
		t.Errorf("reviewer = %+v, %v", got, ok)
	}
	if got, _ := c.Role("deep_dive"); got.Type != "sonnet" || got.Effort != "high" {
		t.Errorf("deep_dive = %+v: a role ref names its model and effort", got)
	}
	if got := c.ReviewsDirSet(); got != "" {
		t.Errorf("ReviewsDirSet = %q, want none set", got)
	}
}

func TestAgentLookupPrecedence(t *testing.T) {
	root := writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"},
		"lanes":{"dir":"../proj-wt","count":2},
		"agents":{"roles":{"lead":"opus:high","integrator":"sonnet","triage":"","deep_dive":"opus"}}}`, "")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LaneAgentAliases("L-01"); !reflect.DeepEqual(got, []string{"pj-l-01"}) { // derived only
		t.Errorf("LaneAgentAliases = %#v", got)
	}
	if _, ok := c.AgentLookup("pj-advisor", ""); ok {
		t.Error("an optional role that is not configured is known")
	}
	wt := filepath.Join(filepath.Dir(root), "proj-wt")
	for name, want := range map[string]AgentInfo{
		"pj-l-01":       {Role: "worker", Lane: "L-01", Cwd: wt + "/L-01"},
		"pj-l-02":       {Role: "worker", Lane: "L-02", Cwd: wt + "/L-02"},
		"pj-l-02-drive": {Role: "driver", Lane: "L-02", Cwd: wt + "/L-02"},             // a lane's drive: its lane and worktree
		"pj-int":        {Role: "integrator", Type: "sonnet", Cwd: wt + "/integrator"}, // its role worktree
		"pj-lead":       {Role: "lead", Type: "opus", Effort: "high", Cwd: root},
		"pj-deep-dive":  {Role: "deep_dive", Type: "opus", Cwd: wt + "/deep_dive"}, // its role worktree
		"pj-triage":     {Role: "triage", Cwd: root},
	} {
		if got, ok := c.AgentLookup(name, ""); !ok || got != want {
			t.Errorf("AgentLookup(%q) = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	if got, ok := c.AgentLookup("unknown", wt+"/L-02"); !ok || got.Role != "worker" || got.Lane != "L-02" {
		t.Errorf("cwd lane lookup = %+v, %v", got, ok)
	}
	if _, ok := c.AgentLookup("unknown", ""); ok {
		t.Error("unknown agent found")
	}
	if _, ok := c.AgentLookup("pj-l-03-drive", ""); ok {
		t.Error("the drive name of a lane the config does not have is known")
	}
	want := []string{"pj-l-01", "pj-l-01-drive", "pj-l-02", "pj-l-02-drive"}
	if got := c.Agents(); len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Errorf("Agents = %v, want the lanes' workers and drives first: %v", got, want)
	}
	// the removed forms resolve to nothing: the compact lane alias (pj-l1), an agents entry (adv) and the bare
	// lane name (l-01).
	for _, name := range []string{"pj-l1", "adv", "l-01"} {
		if got, ok := c.AgentLookup(name, ""); ok {
			t.Errorf("AgentLookup(%q) = %+v; want unknown", name, got)
		}
	}
	// NamesOtherAgent: a label the config names other than the agent reserves the pane; the agent's own name, an
	// old alias and a person's label name nobody.
	for _, tc := range []struct {
		name, label string
		names       []string
		want        bool
	}{
		{"another lane's worker", "pj-l-02", []string{"pj-l-01"}, true},
		{"a role's agent", "pj-int", []string{"pj-l-01"}, true},
		{"a lane's drive agent", "pj-l-01-drive", []string{"pj-l-01"}, true},
		{"its own drive name", "pj-l-01-drive", []string{"pj-l-01-drive"}, false},
		{"its own name", "pj-l-01", []string{"pj-l-01"}, false},
		{"its own name, another agent's list", "pj-l-01", []string{"pj-l-02"}, true},
		{"an old alias", "pj-l1", []string{"pj-l-01"}, false},
		{"a person's label", "notes", []string{"pj-l-01"}, false},
		{"no label", "", []string{"pj-l-01"}, false},
	} {
		if got := c.NamesOtherAgent(tc.label, tc.names...); got != tc.want {
			t.Errorf("%s: NamesOtherAgent(%q, %v) = %v, want %v", tc.name, tc.label, tc.names, got, tc.want)
		}
	}
}

// A lane's agent (a worker, the owner's drive agent) starts with --strict-mcp-config, obor's MCP server alone. Every
// agent but a lead starts trimmed of the user's settings, each kind with its own switches (claude its
// settings file in place of the user's source; codex its three -c keys); a lead start carries none of them.
func TestLaunchArgs(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, "")
	opus, _ := c.Model("opus")
	luna, _ := c.Model("luna")
	for _, tc := range []struct {
		name string
		m    Model
		role string
		want string
	}{
		{"worker: strict, the settings file", opus, "worker",
			"--permission-mode auto --model opus --strict-mcp-config --setting-sources project,local --settings /state/settings/a.json"},
		{"drive agent: strict, the settings file too", opus, "driver",
			"--permission-mode auto --model opus --strict-mcp-config --setting-sources project,local --settings /state/settings/a.json"},
		{"lead: no settings file, no strict", opus, "lead", "--permission-mode auto --model opus"},
		{"integrator: no strict, the settings file", opus, "integrator",
			"--permission-mode auto --model opus --setting-sources project,local --settings /state/settings/a.json"},
		{"codex: no --settings, trimmed", luna, "integrator", "-s workspace-write -a never --no-daemon -m gpt-6-luna -c features.plugins=false -c features.apps=false -c skills.bundled.enabled=false -c check_for_update_on_startup=false"},
		{"codex worker: its kind's start, strict means nothing to it", luna, "worker",
			"-s workspace-write -a never --no-daemon -m gpt-6-luna -c features.plugins=false -c features.apps=false -c skills.bundled.enabled=false -c check_for_update_on_startup=false"},
		{"codex lead: no trim", luna, "lead", "-s workspace-write -a never --no-daemon -m gpt-6-luna -c check_for_update_on_startup=false"},
	} {
		if got := strings.Join(launchArgs(t, tc.m, tc.role, ""), " "); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if len(opus.Args) != 4 {
		t.Errorf("Launch changed the model's args: %q", opus.Args)
	}
}

// The project's override files of a kind are the files its kind names that the project's root has in .obor/<kind>/,
// each document as written; a file of another name there, another kind's and a kind with none are not in them, and a
// file that does not parse is an error naming it. Launch hands them to the kind on every start but a lead's: a
// worker's Claude settings file holds the override's env key beside obor's own, a lead's start writes none.
func TestOverrides(t *testing.T) {
	root := writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, "")
	write := func(rel, text string) {
		t.Helper()
		file := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const settings, config = `{"env": {"KIND": "k"}}`, "model_reasoning_effort = \"low\"\n"
	write(".obor/claude/settings_override.json", settings)
	write(".obor/claude/notes.md", "not an override file")
	write(".obor/codex/config_override.toml", config)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]map[string]string{
		"claude": {"settings_override.json": settings}, "codex": {"config_override.toml": config}, "opencode": nil, "nokind": nil,
	} {
		if got, err := c.Overrides(kind); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Overrides(%s) = %q, %v, want %q", kind, got, err, want)
		}
	}

	opus, _ := c.Model("opus")
	files := map[string]string{}
	in := agents.Start{Agent: "pj-rev", WriteState: func(name string, data []byte) (string, error) {
		files[name] = string(data)
		return "/state/" + name, nil
	}}
	overrides, _ := c.Overrides("claude")
	l, err := opus.Launch("reviewer", overrides, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "--permission-mode auto --model opus --setting-sources project,local --settings /state/settings/pj-rev.json"; strings.Join(l.Args, " ") != want {
		t.Errorf("a reviewer's launch args %q, want %q", l.Args, want)
	}
	var file any
	if err := json.Unmarshal([]byte(files["settings/pj-rev.json"]), &file); err != nil {
		t.Fatalf("settings file %q of %v: %v", files["settings/pj-rev.json"], files, err)
	}
	if want := `{"autoMemoryEnabled":false,"env":{"KIND":"k"},"remoteControlAtStartup":false}`; Compact(file) != want {
		t.Errorf("a reviewer's settings file %s, want %s", Compact(file), want)
	}
	clear(files)
	if l, err = opus.Launch("lead", overrides, in); err != nil || len(files) != 0 ||
		strings.Join(l.Args, " ") != "--permission-mode auto --model opus" {
		t.Errorf("a lead's launch: args %q, files %v, err %v; want the model's args alone and no file", l.Args, files, err)
	}

	write(".obor/codex/hooks_override.json", `{"hooks": `)
	if got, err := c.Overrides("codex"); err == nil || got != nil || !strings.Contains(err.Error(), filepath.Join(".obor", "codex", "hooks_override.json")+": does not parse") {
		t.Errorf("a file that does not parse: Overrides = %q, %v, want an error naming it", got, err)
	}
}

// Lookup (obor config get) knows the unset fields of existing collection members, not invented members or Go names.
func TestLookupCollectionDefaults(t *testing.T) {
	t.Parallel()
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"commands":{"lint":{"cmd":"true"}},
		"merge":{"check":"make check","by_path":{"rules":[{"name":"go","paths":["**/*.go"]}]}},
		"agents":{"models":{"m":{"kind":"claude","id":"x"}},"kinds":{"claude":{"args":["--a"]}}},"lanes":{"count":1}}`, "")
	for _, tc := range []struct {
		path, want string
		known      bool
	}{
		{".mcp.jobs", "", false}, // removed: no field, so no such key
		{".commands.lint.heavy", "false", true},
		{`.commands["lint"].heavy`, "false", true},
		{".commands.lint.cmd", `"true"`, true},
		{".merge.check.heavy", "false", true}, // a fixed command given as a string
		{".merge.check.cmd", `"make check"`, true},
		{".merge.compile.cmd", `""`, true},
		{".agents.kinds.claude.job_note", `""`, true},
		{`.agents.kinds["claude"].job_note`, `""`, true},
		{".agents.models.m.kind", `"claude"`, true},
		{".merge.by_path.rules[0].full", "false", true},
		{".merge.by_path.rules.0.full", "false", true},
		{".merge.by_path.rules[0].also_on_full", "false", true},
		{".commands.nope.heavy", "", false},
		{".commands.lint.nope", "", false},
		{".agents.kinds.codex.args", "", false},
		{".agents.models.nope.kind", "", false},
		{".merge.by_path.rules[1].full", "", false},
		{".merge.by_path.rules[-1].full", "", false},
		{".commands.Keys", "", false},
		{".agents.models.M", "", false},
		{".project.agent_prefix.x", "", false},
	} {
		v, ok := c.Lookup(tc.path)
		if ok != tc.known || (ok && string(v) != tc.want) {
			t.Errorf("Lookup(%s) = %s, %v; want %s, %v", tc.path, v, ok, tc.want, tc.known)
		}
	}
}

// TaskIDOK and TaskFromBranch (from package lanes) follow project.task_id_pattern and lanes.task_branch; TaskBranch is
// TaskFromBranch's inverse.
func TestTaskFromBranch(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj","task_id_pattern":"TK[0-9]+"},`+
		`"lanes":{"task_branch":"wip/{id}-lane"}}`, "")
	if c.TaskFromBranch("wip/TK12-lane") != "TK12" || c.TaskFromBranch("wip/T-12-lane") != "" ||
		c.TaskFromBranch("task/TK12") != "" {
		t.Error("TaskFromBranch")
	}
	if got := c.TaskBranch("TK12"); got != "wip/TK12-lane" || c.TaskFromBranch(got) != "TK12" {
		t.Errorf("TaskBranch(TK12) = %q, want the template's branch and back (T-896)", got)
	}
	if !c.TaskIDOK("TK1") || c.TaskIDOK("TK1x") || c.TaskIDOK("xTK1") || c.TaskIDOK("TK1\n") {
		t.Error("TaskIDOK matches a whole id only")
	}
	if bad := load(t, `{"version":2,"project":{"agent_prefix":"pj","task_id_pattern":"("}}`, ""); bad.TaskIDOK("(") {
		t.Error("a pattern that does not compile matches nothing")
	}
}

// a lane's env is the generic fact OBOR_LANE_CPUS (slots.lane_cpus) and the project's lanes.env, its
// {cpus} replaced by that budget, names in order; a project without lane_env gets the fact alone (no Go names), and a
// value with no placeholder is kept as it is.
func TestLaneEnv(t *testing.T) {
	for _, c := range []struct {
		name, config string
		want         []string
	}{
		{"no lane_env: the fact alone", `{"version":2}`, []string{"OBOR_LANE_CPUS=2"}},
		{"lane_cpus 3", `{"version":2,"lanes":{"cpus":3}}`, []string{"OBOR_LANE_CPUS=3"}},
		{"a template", `{"version":2,"lanes":{"env":{"GOFLAGS":"-p={cpus} -trimpath","GOMAXPROCS":"{cpus}"}}}`,
			[]string{"OBOR_LANE_CPUS=2", "GOFLAGS=-p=2 -trimpath", "GOMAXPROCS=2"}},
		{"a template on lane_cpus 3", `{"version":2,"lanes":{"cpus":3,"env":{"X":"-p={cpus}"}}}`,
			[]string{"OBOR_LANE_CPUS=3", "X=-p=3"}},
		{"no placeholder: kept as it is", `{"version":2,"lanes":{"env":{"CGO_ENABLED":"0"}}}`,
			[]string{"OBOR_LANE_CPUS=2", "CGO_ENABLED=0"}},
		{"the placeholder twice", `{"version":2,"lanes":{"env":{"X":"{cpus}/{cpus}"}}}`,
			[]string{"OBOR_LANE_CPUS=2", "X=2/2"}},
	} {
		if got := LaneEnv(load(t, c.config, "")); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: LaneEnv = %q, want %q", c.name, got, c.want)
		}
	}
	if got := LaneEnv(nil); !reflect.DeepEqual(got, []string{"OBOR_LANE_CPUS=2"}) {
		t.Errorf("nil config: LaneEnv = %q, want the fact alone", got)
	}
}

// a var of the start's kind env that lanes.env also names takes the config's value: obor merges nothing
// into it (the config line is the policy).
func TestWithLaneEnv(t *testing.T) {
	cfg := load(t, `{"version":2,"lanes":{"env":{"GOFLAGS":"-p={cpus}"}}}`, "")
	got := WithLaneEnv(map[string]string{"GOFLAGS": "-mod=mod", "KEEP": "1"}, cfg)
	want := map[string]string{"GOFLAGS": "-p=2", "KEEP": "1", "OBOR_LANE_CPUS": "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithLaneEnv = %q, want %q", got, want)
	}
	if vars := map[string]string{"GOFLAGS": "-mod=mod"}; WithLaneEnv(vars, nil)["GOFLAGS"] != "-mod=mod" {
		t.Error("no config: the kind env's own var stays")
	}
}

// lanes.env's names are env names and {cpus} its one placeholder: an unknown {…} or a name that is no env
// name fails validate, a value with no placeholder is fine.
func TestLaneEnvValidate(t *testing.T) {
	for _, c := range []struct {
		name  string
		env   string
		fails []string
	}{
		{name: "the template", env: `{"GOMAXPROCS": "{cpus}", "GOFLAGS": "-p={cpus} -trimpath"}`},
		{name: "no placeholder", env: `{"CGO_ENABLED": "0"}`},
		{name: "an unknown placeholder", env: `{"GOFLAGS": "-p={tests}"}`,
			fails: []string{`FAIL .lanes.env.GOFLAGS: unknown placeholder {tests}: the one placeholder is {cpus} (lanes.cpus)`}},
		{name: "a name that is no env name", env: `{"GO FLAGS": "-p={cpus}"}`,
			fails: []string{`FAIL .lanes.env["GO FLAGS"]: is no env name: letters, digits and _ only, not starting with a digit, got "GO FLAGS"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"env":`+c.env+`}}`, "")
			_, all := cfg.Validate(ValidateOptions{})
			var fails []string
			for _, f := range all {
				if strings.HasPrefix(f, "FAIL .lanes.env") {
					fails = append(fails, f)
				}
			}
			if !slices.Equal(fails, c.fails) {
				t.Errorf("validate = %q, want %q", fails, c.fails)
			}
		})
	}
}

// lanes is a count (sugar for that many fresh lanes) or a list of {name, keep_agent}; obor config validate
// refuses a list with a repeated name or a name not of the L-NN shape; a list member's unknown key is dropped as any
// unknown key is. The form the file has is the one written back.
func TestLanesCountOrList(t *testing.T) {
	type lane struct{ name, mode string }
	for _, c := range []struct {
		name, lanes, written string
		want                 []lane
		fails                []string
		loadErr              string
	}{
		{name: "a count: fresh lanes L-01 … L-NN", lanes: `"count":2`, written: "2",
			want: []lane{{"L-01", LaneFresh}, {"L-02", LaneFresh}}},
		{name: "no lanes", lanes: `"count":0`, written: "0"},
		{name: "a list: its names and modes, in its order", lanes: `"list":[{"name": "L-02", "keep_agent": true}, {"name": "L-05"}]`,
			written: `[{"name":"L-02","keep_agent":true},{"name":"L-05"}]`,
			want:    []lane{{"L-02", LaneKeep}, {"L-05", LaneFresh}}},
		{name: "duplicate names", lanes: `"list":[{"name": "L-01", "keep_agent": true}, {"name": "L-01"}]`,
			written: `[{"name":"L-01","keep_agent":true},{"name":"L-01"}]`,
			want:    []lane{{"L-01", LaneKeep}, {"L-01", LaneFresh}},
			fails:   []string{"FAIL .lanes.list[1].name: lane L-01 is listed twice"}},
		{name: "bad names", lanes: `"list":[{"name": "l-01"}, {"name": "L-1"}, {"name": "L-00"}, {"name": ""}]`,
			written: `[{"name":"l-01"},{"name":"L-1"},{"name":"L-00"},{"name":""}]`,
			want:    []lane{{"l-01", LaneFresh}, {"L-1", LaneFresh}, {"L-00", LaneFresh}, {"", LaneFresh}},
			fails: []string{`FAIL .lanes.list[0].name: must be a lane name L-NN (L-01, L-02 …), got "l-01"`,
				`FAIL .lanes.list[1].name: must be a lane name L-NN (L-01, L-02 …), got "L-1"`,
				`FAIL .lanes.list[2].name: must be a lane name L-NN (L-01, L-02 …), got "L-00"`,
				`FAIL .lanes.list[3].name: must be a lane name L-NN (L-01, L-02 …), got ""`}},
		{name: "a negative count", lanes: `"count":-1`, written: "-1", fails: []string{"FAIL .lanes.count: must be a non-negative integer (how many lanes), got -1"}},
		{name: "an unknown member key: dropped", lanes: `"list":[{"name": "L-01", "keep": true}]`, written: `[{"name":"L-01"}]`,
			want: []lane{{"L-01", LaneFresh}}},
		{name: "neither a count nor a list", lanes: `"count":"two"`, loadErr: "lanes.count"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"dir":"../wt",`+c.lanes+`}}`, ""))
			if c.loadErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.loadErr) {
					t.Fatalf("load: %v, want an error naming %q", err, c.loadErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []lane
			for _, l := range cfg.Lanes() {
				got = append(got, lane{l.Name, l.Mode})
				if l.Dir != cfg.LanesDir()+"/"+l.Name {
					t.Errorf("%s's dir %q", l.Name, l.Dir)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("lanes %v, want %v", got, c.want)
			}
			var fails []string
			_, all := cfg.Validate(ValidateOptions{})
			for _, f := range all {
				if strings.HasPrefix(f, "FAIL .lanes") {
					fails = append(fails, f)
				}
			}
			if !slices.Equal(fails, c.fails) {
				t.Errorf("validate %q, want %q", fails, c.fails)
			}
			// the form the file has is written back (Compact: obor config get, a config edit's old value)
			written := Compact(cfg.Data.Lanes.Count)
			if cfg.LanesListed() {
				written = Compact(cfg.Data.Lanes.List)
			}
			if written != c.written {
				t.Errorf("written back as %s, want %s", written, c.written)
			}
		})
	}
	keep := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"list":[{"name":"L-01","keep_agent":true},{"name":"L-02"}]}}`, "")
	if !keep.KeepLane("L-01") || keep.KeepLane("L-02") || keep.KeepLane("L-03") || !keep.HasLane("L-02") || keep.HasLane("L-03") {
		t.Errorf("KeepLane/HasLane: L-01 keep, L-02 fresh, L-03 none; got %v %v %v / %v %v", keep.KeepLane("L-01"),
			keep.KeepLane("L-02"), keep.KeepLane("L-03"), keep.HasLane("L-02"), keep.HasLane("L-03"))
	}
}

// a config's revision (Config.Rev) is its effective JSON's, so a rewrite that leaves what runs alone keeps it,
// while an edit in .obor/local.json — where a model's id or a kind's args may live — changes it: either file counts,
// not only the committed one.
func TestRevIsTheEffectiveConfig(t *testing.T) {
	one := load(t, `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`, "")
	same := load(t, "{\n  \"version\": 2,\n  \"project\": {\n    \"name\": \"pj\",\n    \"agent_prefix\": \"pj\"\n  }\n}\n", "")
	if len(one.json) == 0 || one.Rev() != same.Rev() {
		t.Errorf("rev %s of a rewritten config.json, want %s: the same effective config", same.Rev(), one.Rev())
	}
	local := load(t, `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`,
		`{"agents":{"models":{"sonnet":{"kind":"claude","id":"sonnet-4-6"}}}}`)
	if local.Rev() == one.Rev() {
		t.Errorf("rev %s with local.json's model, want another than %s: a changed model id behind an alias counts",
			local.Rev(), one.Rev())
	}
}
