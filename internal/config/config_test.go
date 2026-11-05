package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	c := load(t, `{"version":1,"prefix":"pj","models":{"cx":{"kind":"codex","model":"gpt-x"}}}`, "")
	for path, want := range map[string]string{
		".branches.main":      "main",
		".branches.task":      "task/{id}",
		".task_id_pattern":    "T-[0-9]+[a-z]?",
		".done_file_sections": "[\n  \"Summary\",\n  \"Self-check\",\n  \"Skipped\",\n  \"Open questions\"\n]",
		".lanes_dir":          "../proj-wt",
		".merge.scratch_dir":  "../proj-wt/merge",
	} {
		if got, ok := raw(c, path); !ok || got != want {
			t.Errorf("%s = %q (exists %v), want %q", path, got, ok, want)
		}
	}
	if _, ok := c.Get(".roles.leads"); ok {
		t.Error(".roles.leads: no longer derived (T-467)")
	}
	if _, ok := c.Get(".models.cx.settings"); ok {
		t.Errorf("the Remote Control default is applied at launch, not in the config (T-280)")
	}
	if c.LanesDir() != filepath.Join(filepath.Dir(c.Root), "proj-wt") {
		t.Errorf("LanesDir = %s", c.LanesDir())
	}
}

func TestLocalOverlay(t *testing.T) {
	c := load(t, `{"prefix":"pj","merge":{"receipt_max_age":60},"done_file_sections":["A","B","C"],
		"kinds":{"claude":{"args":["--x"],"prompt_extra":"P"}},"lanes":4,
		"commands":{"check":{"cmd":"make check","timeout":90}},
		"merge":{"receipt_max_age":60,"classes":[{"name":"a","paths":["src/a/**"]}]}}`,
		`{"merge":{"nightly_at":"02:00","classes":[{"name":"b","paths":["src/b/**"]}]},"done_file_sections":["X"],
		"kinds":{"claude":{"args":["--y"]}},"lanes":2,"commands":{"check":{"dir":"sub"}},"lanes_dir":"/wt"}`)
	if m := c.Data.Merge; m.ReceiptMaxAge != 60 || m.NightlyAt != "02:00" {
		t.Errorf("merge: objects merge key by key, got %+v", m)
	}
	if !reflect.DeepEqual(c.Data.DoneFileSections, []string{"X"}) {
		t.Errorf("a list is replaced whole: %q", c.Data.DoneFileSections)
	}
	if k := c.Data.Kinds.M["claude"]; k.PromptExtra != "P" || !reflect.DeepEqual(k.Args, []string{"--y"}) {
		t.Errorf("a kind merges key by key, its args replaced whole: %+v", k)
	}
	if c.Data.Lanes != 2 || len(c.Lanes()) != 2 {
		t.Errorf("local.json's lanes count replaces config.json's: %d", c.Data.Lanes)
	}
	if cmd, ok := c.Command("check"); !ok || cmd.Cmd != "make check" || cmd.Timeout != 90 || cmd.Dir != "sub" {
		t.Errorf("a command merges: %+v", cmd)
	}
	if got := c.Data.Merge.Classes; !reflect.DeepEqual(got, List[MergeClass]{{Name: "b", Paths: []string{"src/b/**"}}}) {
		t.Errorf("a list of objects is replaced whole, no field of the old entry kept: %+v", got)
	}
	if c.LanesDir() != "/wt" || c.Data.Merge.ScratchDir != "/wt/merge" {
		t.Errorf("derived after the overlay: %s %s", c.LanesDir(), c.Data.Merge.ScratchDir)
	}
	// Defaults are a fresh copy per load: the overlay above did not write into them.
	if d := load(t, `{"prefix":"pj"}`, ""); len(d.Data.DoneFileSections) != 4 || d.Data.DoneFileSections[0] != "Summary" {
		t.Errorf("defaults changed by an earlier load: %q", d.Data.DoneFileSections)
	}
}

func TestUnknownKeyFails(t *testing.T) {
	for _, cfg := range []string{`{"prefix":"pj","typo_key":1}`, `{"prefix":"pj","roles":{"mystery":{"model":"opus","writes":[]}}}`,
		`{"prefix":"pj","models":{"m":{"kind":"claude","modle":"x"}}}`, `{"prefix":"pj","commands":{"c":{"cmd":"x","tmeout":5}}}`,
		`{"prefix":"pj","kinds":{"claude":{"argz":[]}}}`} {
		_, err := Load(writeProject(t, cfg, ""))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: %v, want an unknown field error", cfg, err)
		}
	}
	_, err := Load(writeProject(t, `{"prefix":"pj"}`, `{"slots":{"maxx":1}}`))
	if err == nil || !strings.Contains(err.Error(), `unknown field "maxx"`) || !strings.Contains(err.Error(), "local.json") {
		t.Errorf("local.json unknown key: %v", err)
	}
	// A key the shape still types: a wrong type fails, as a removed key (an `any`) no longer does (T-864).
	if _, err := Load(writeProject(t, `{"prefix":"pj","handoff_tokens":"no"}`, "")); err == nil ||
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
	c := load(t, `{"prefix":"pj","models":{"z":{"kind":"codex","model":"z1"},"a":{"kind":"codex","model":"a1"}},
		"kinds":{"opencode":{"args":["--o"]},"claude":{"args":["--c"]}},
		"commands":{"zz":"z","aa":null,"mm":{"cmd":"m","timeout":5}}}`,
		`{"models":{"b":{"kind":"codex","model":"b1"},"z":{"kind":"codex","model":"z2"}}}`)
	for _, k := range []struct {
		got, want []string
	}{{c.Data.Models.Keys, []string{"z", "a", "b"}}, {c.Data.Kinds.Keys, []string{"opencode", "claude"}},
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
	c := load(t, `{"prefix":"pj","lanes":2,"roles":{"integrator":"opus:high"},"integrator_handoff_tokens":0}`, "")
	for _, tc := range []struct {
		path, want string
		ok         bool
	}{
		{".prefix", "pj", true},
		{".lanes", "2", true},
		{".roles.integrator", "opus:high", true},
		{".integrator_handoff_tokens", "0", true},
		{".roles.leads", "", false},
		{".nope", "", false},
		{".prefix.x", "", false},
		{".merge.nightly_at", "", false},
	} {
		if got, ok := raw(c, tc.path); got != tc.want || ok != tc.ok {
			t.Errorf("get %s = %q, %v; want %q, %v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
	if all, ok := c.Raw("."); !ok || !strings.HasPrefix(all, "{\n  \"version\"") && !strings.HasPrefix(all, "{\n  \"prefix\"") {
		t.Errorf("get . = %.40q", all)
	}
}

// TestDefaults: each default the callers read as a typed field (T-286): unset → the default, set → the value.
func TestDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, set string
		get       func(*Config) any
		def, val  any
	}{
		{"merge.receipt_max_age", `"merge":{"receipt_max_age":60}`, func(c *Config) any { return c.Data.Merge.ReceiptMaxAge }, 86400, 60},
		{"merge.nightly_commands", `"merge":{"nightly_commands":["trim"]}`, func(c *Config) any { return len(c.Data.Merge.NightlyCommands) }, 0, 1},
		{"merge.affected_full", `"merge":{"affected_full":["Makefile"]}`, func(c *Config) any { return len(c.Data.Merge.AffectedFull) }, 0, 1},
		{"paths.agents", `"paths":{"agents":"doc/roles"}`, func(c *Config) any { return c.Data.Paths["agents"] }, "docs/agents", "doc/roles"},
	} {
		if got := tc.get(load(t, `{"prefix":"pj"}`, "")); got != tc.def {
			t.Errorf("%s unset = %v, want the default %v", tc.name, got, tc.def)
		}
		if got := tc.get(load(t, `{"prefix":"pj",`+tc.set+`}`, "")); got != tc.val {
			t.Errorf("%s set = %v, want %v", tc.name, got, tc.val)
		}
	}
}

func TestRolesAndAgentNames(t *testing.T) {
	c := load(t, `{"prefix":"pj","lanes":1,"roles":{"reviewer":"opus","deep_dive":"sonnet:high","integrator":"opus"}}`, "")
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
	if got := c.Paths(); got.Reviews != "" || got.Get("tasks") != "" || got.Get("queue") != "" {
		t.Errorf("Paths = %+v", got)
	}
}

func TestAgentLookupPrecedence(t *testing.T) {
	root := writeProject(t, `{"prefix":"pj","lanes_dir":"../proj-wt","lanes":2,
		"roles":{"lead":"opus:high","integrator":"sonnet","triage":"","deep_dive":"opus"}}`, "")
	// The agent L-01's state file names (project.LaneAgent; config reads no state files).
	state := func(lane string) string {
		if lane == "L-01" {
			return "bob"
		}
		return ""
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LaneAgentAliases("L-01", state("L-01")); !reflect.DeepEqual(got, []string{"bob", "pj-l-01"}) {
		t.Errorf("LaneAgentAliases = %#v", got)
	}
	if _, ok := c.AgentLookup("pj-advisor", "", state); ok {
		t.Error("an optional role that is not configured is known")
	}
	wt := filepath.Join(filepath.Dir(root), "proj-wt")
	for name, want := range map[string]AgentInfo{
		"bob":          {Role: "worker", Lane: "L-01", Cwd: wt + "/L-01"},
		"pj-l-01":      {Role: "worker", Lane: "L-01", Cwd: wt + "/L-01"},
		"pj-l-02":      {Role: "worker", Lane: "L-02", Cwd: wt + "/L-02"},
		"pj-int":       {Role: "integrator", Type: "sonnet", Cwd: wt + "/integrator"}, // T-853: its role worktree
		"pj-lead":      {Role: "lead", Type: "opus", Effort: "high", Cwd: root},
		"pj-deep-dive": {Role: "deep_dive", Type: "opus", Cwd: wt + "/deep_dive"}, // T-853: its role worktree
		"pj-triage":    {Role: "triage", Cwd: root},
	} {
		if got, ok := c.AgentLookup(name, "", state); !ok || got != want {
			t.Errorf("AgentLookup(%q) = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	if got, ok := c.AgentLookup("unknown", wt+"/L-02", state); !ok || got.Role != "worker" || got.Lane != "L-02" {
		t.Errorf("cwd lane lookup = %+v, %v", got, ok)
	}
	if _, ok := c.AgentLookup("unknown", "", state); ok {
		t.Error("unknown agent found")
	}
	// T-777: the removed forms resolve to nothing: the compact lane alias (pj-l1), an agents entry (adv) and the bare
	// lane name (l-01).
	for _, name := range []string{"pj-l1", "adv", "l-01"} {
		if got, ok := c.AgentLookup(name, "", state); ok {
			t.Errorf("AgentLookup(%q) = %+v; want unknown", name, got)
		}
	}
	if _, ok := c.AgentLookup("bob", "", nil); ok {
		t.Error("bob is known without the state agent")
	}
}

// T-280, T-770: Remote Control off and auto memory off for every claude agent but a lead; a lane's agent (a worker, the
// owner's drive agent) starts with --strict-mcp-config, obor's MCP server alone.
func TestLaunchArgs(t *testing.T) {
	c := load(t, `{"prefix":"pj"}`, "")
	opus, _ := c.Model("opus")
	luna, _ := c.Model("luna")
	for _, tc := range []struct {
		name string
		m    Model
		role string
		want string
	}{
		{"worker: strict, the settings", opus, "worker",
			`--permission-mode auto --model opus --strict-mcp-config --settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`},
		{"drive agent: strict too", opus, "driver",
			`--permission-mode auto --model opus --strict-mcp-config --settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`},
		{"lead: no --settings, no strict", opus, "lead", "--permission-mode auto --model opus"},
		{"integrator: no strict", opus, "integrator",
			`--permission-mode auto --model opus --settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`},
		{"codex: no --settings", luna, "integrator", "-s workspace-write -a never --no-daemon -m gpt-6-luna -c check_for_update_on_startup=false"},
		{"codex worker: its kind's start, strict means nothing to it", luna, "worker",
			"-s workspace-write -a never --no-daemon -m gpt-6-luna -c check_for_update_on_startup=false"},
	} {
		if got := strings.Join(tc.m.LaunchArgs(tc.role, "", nil), " "); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if len(opus.Args) != 4 {
		t.Errorf("LaunchArgs changed the model's args: %q", opus.Args)
	}
}

// Lookup (obor config get) knows the unset fields of existing collection members, not invented members or Go names.
func TestLookupCollectionDefaults(t *testing.T) {
	t.Parallel()
	c := load(t, `{"prefix":"pj","commands":{"check":{"cmd":"true"}},"models":{"m":{"kind":"claude","model":"x"}},
		"kinds":{"claude":{"args":["--a"]}},"lanes":1,
		"merge":{"classes":[{"name":"go","paths":["**/*.go"]}]}}`, "")
	for _, tc := range []struct {
		path, want string
		known      bool
	}{
		{".merge.trailer", `""`, true},
		{".mcp.jobs", "", false}, // removed (T-830): no field, so no such key
		{".commands.check.heavy", "false", true},
		{`.commands["check"].heavy`, "false", true},
		{".commands.check.cmd", `"true"`, true},
		{".kinds.claude.prompt_extra", `""`, true},
		{`.kinds["claude"].prompt_extra`, `""`, true},
		{".models.m.kind", `"claude"`, true},
		{".merge.classes[0].full", "false", true},
		{".merge.classes.0.full", "false", true},
		{".commands.nope.heavy", "", false},
		{".commands.check.nope", "", false},
		{".kinds.codex.args", "", false},
		{".models.nope.kind", "", false},
		{".merge.classes[1].full", "", false},
		{".merge.classes[-1].full", "", false},
		{".commands.Keys", "", false},
		{".models.M", "", false},
		{".prefix.x", "", false},
	} {
		v, ok := c.Lookup(tc.path)
		if ok != tc.known || (ok && string(v) != tc.want) {
			t.Errorf("Lookup(%s) = %s, %v; want %s, %v", tc.path, v, ok, tc.want, tc.known)
		}
	}
}

// TaskIDOK and TaskFromBranch (T-470: from package lanes) follow task_id_pattern and branches.task; TaskBranch is
// TaskFromBranch's inverse (T-896).
func TestTaskFromBranch(t *testing.T) {
	c := load(t, `{"prefix":"pj","task_id_pattern":"TK[0-9]+","branches":{"task":"wip/{id}-lane"}}`, "")
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
	if bad := load(t, `{"prefix":"pj","task_id_pattern":"("}`, ""); bad.TaskIDOK("(") {
		t.Error("a pattern that does not compile matches nothing")
	}
}
