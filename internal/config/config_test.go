package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	c := load(t, `{"prefix":"pj","merge":{"nightly_at":"03:00"},"done_file_sections":["A","B","C"],
		"kinds":{"claude":{"args":["--x"],"prompt_extra":"P"}},"lanes":4,
		"commands":{"check":{"cmd":"make check","timeout":90}},
		"merge":{"nightly_at":"03:00","classes":[{"name":"a","paths":["src/a/**"]}]}}`,
		`{"merge":{"nightly_at":"02:00","classes":[{"name":"b","paths":["src/b/**"]}]},"done_file_sections":["X"],
		"kinds":{"claude":{"args":["--y"]}},"lanes":2,"commands":{"check":{"heavy":true}},"lanes_dir":"/wt"}`)
	if m := c.Data.Merge; m.NightlyAt != "02:00" {
		t.Errorf("merge: objects merge key by key, got %+v", m)
	}
	if !reflect.DeepEqual(c.Data.DoneFileSections, []string{"X"}) {
		t.Errorf("a list is replaced whole: %q", c.Data.DoneFileSections)
	}
	if k := c.Data.Kinds.M["claude"]; k.PromptExtra != "P" || !reflect.DeepEqual(k.Args, []string{"--y"}) {
		t.Errorf("a kind merges key by key, its args replaced whole: %+v", k)
	}
	if c.Data.Lanes.Count() != 2 || len(c.Lanes()) != 2 {
		t.Errorf("local.json's lanes count replaces config.json's: %d", c.Data.Lanes.Count())
	}
	if cmd, ok := c.Command("check"); !ok || cmd.Cmd != "make check" || cmd.Timeout != 90 || !cmd.Heavy {
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

// TestUnknownKeyFails: the strict validate load (LoadForValidate) fails each unknown key with its path and file, where
// Load (every command's) ignores it (T-1081).
func TestUnknownKeyFails(t *testing.T) {
	for _, c := range []struct{ cfg, key string }{
		{`{"prefix":"pj","typo_key":1}`, "typo_key"},
		{`{"prefix":"pj","roles":{"mystery":{"model":"opus","writes":[]}}}`, "roles.mystery.writes"},
		{`{"prefix":"pj","models":{"m":{"kind":"claude","modle":"x"}}}`, "models.m.modle"},
		{`{"prefix":"pj","commands":{"c":{"cmd":"x","tmeout":5}}}`, "commands.c.tmeout"},
		{`{"prefix":"pj","kinds":{"claude":{"argz":[]}}}`, "kinds.claude.argz"},
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
	_, fails, err := LoadForValidate(writeProject(t, `{"prefix":"pj"}`, `{"slots":{"maxx":1}}`))
	if err != nil || len(fails) != 1 || !strings.HasPrefix(fails[0], "FAIL .slots.maxx: ") || !strings.HasSuffix(fails[0], "(.obor/local.json)") {
		t.Errorf("local.json unknown key: %q, %v", fails, err)
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
	c := load(t, `{"prefix":"pj","lanes":2,"roles":{"integrator":"opus:high"},"handoff_tokens":0}`, "")
	for _, tc := range []struct {
		path, want string
		ok         bool
	}{
		{".prefix", "pj", true},
		{".lanes", "2", true},
		{".roles.integrator", "opus:high", true},
		{".handoff_tokens", "0", true},
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
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LaneAgentAliases("L-01"); !reflect.DeepEqual(got, []string{"pj-l-01"}) { // derived only (T-1031)
		t.Errorf("LaneAgentAliases = %#v", got)
	}
	if _, ok := c.AgentLookup("pj-advisor", ""); ok {
		t.Error("an optional role that is not configured is known")
	}
	wt := filepath.Join(filepath.Dir(root), "proj-wt")
	for name, want := range map[string]AgentInfo{
		"pj-l-01":      {Role: "worker", Lane: "L-01", Cwd: wt + "/L-01"},
		"pj-l-02":      {Role: "worker", Lane: "L-02", Cwd: wt + "/L-02"},
		"pj-int":       {Role: "integrator", Type: "sonnet", Cwd: wt + "/integrator"}, // T-853: its role worktree
		"pj-lead":      {Role: "lead", Type: "opus", Effort: "high", Cwd: root},
		"pj-deep-dive": {Role: "deep_dive", Type: "opus", Cwd: wt + "/deep_dive"}, // T-853: its role worktree
		"pj-triage":    {Role: "triage", Cwd: root},
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
	// T-777: the removed forms resolve to nothing: the compact lane alias (pj-l1), an agents entry (adv) and the bare
	// lane name (l-01).
	for _, name := range []string{"pj-l1", "adv", "l-01"} {
		if got, ok := c.AgentLookup(name, ""); ok {
			t.Errorf("AgentLookup(%q) = %+v; want unknown", name, got)
		}
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
		if got := strings.Join(launchArgs(t, tc.m, tc.role, ""), " "); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if len(opus.Args) != 4 {
		t.Errorf("Launch changed the model's args: %q", opus.Args)
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
		{".merge.classes[0].with_full", "false", true},
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

// T-1041: a lane's env is the generic fact OBOR_LANE_CPUS (slots.lane_cpus) and the project's slots.lane_env, its
// {cpus} replaced by that budget, names in order; a project without lane_env gets the fact alone (no Go names), and a
// value with no placeholder is kept as it is.
func TestLaneEnv(t *testing.T) {
	for _, c := range []struct {
		name, config string
		want         []string
	}{
		{"no lane_env: the fact alone", `{}`, []string{"OBOR_LANE_CPUS=2"}},
		{"lane_cpus 3", `{"slots": {"lane_cpus": 3}}`, []string{"OBOR_LANE_CPUS=3"}},
		{"a template", `{"slots": {"lane_env": {"GOFLAGS": "-p={cpus} -trimpath", "GOMAXPROCS": "{cpus}"}}}`,
			[]string{"OBOR_LANE_CPUS=2", "GOFLAGS=-p=2 -trimpath", "GOMAXPROCS=2"}},
		{"a template on lane_cpus 3", `{"slots": {"lane_cpus": 3, "lane_env": {"X": "-p={cpus}"}}}`,
			[]string{"OBOR_LANE_CPUS=3", "X=-p=3"}},
		{"no placeholder: kept as it is", `{"slots": {"lane_env": {"CGO_ENABLED": "0"}}}`,
			[]string{"OBOR_LANE_CPUS=2", "CGO_ENABLED=0"}},
		{"the placeholder twice", `{"slots": {"lane_env": {"X": "{cpus}/{cpus}"}}}`,
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

// T-1041: a var of the start's kind env that slots.lane_env also names takes the config's value: obor merges nothing
// into it (the config line is the policy).
func TestWithLaneEnv(t *testing.T) {
	cfg := load(t, `{"slots": {"lane_env": {"GOFLAGS": "-p={cpus}"}}}`, "")
	got := WithLaneEnv(map[string]string{"GOFLAGS": "-mod=mod", "KEEP": "1"}, cfg)
	want := map[string]string{"GOFLAGS": "-p=2", "KEEP": "1", "OBOR_LANE_CPUS": "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithLaneEnv = %q, want %q", got, want)
	}
	if vars := map[string]string{"GOFLAGS": "-mod=mod"}; WithLaneEnv(vars, nil)["GOFLAGS"] != "-mod=mod" {
		t.Error("no config: the kind env's own var stays")
	}
}

// T-1041: slots.lane_env's names are env names and {cpus} its one placeholder: an unknown {…} or a name that is no env
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
			fails: []string{`FAIL .slots.lane_env.GOFLAGS: unknown placeholder {tests}: the one placeholder is {cpus} (slots.lane_cpus)`}},
		{name: "a name that is no env name", env: `{"GO FLAGS": "-p={cpus}"}`,
			fails: []string{`FAIL .slots.lane_env["GO FLAGS"]: is no env name: letters, digits and _ only, not starting with a digit, got "GO FLAGS"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := load(t, `{"prefix":"pj","slots":{"lane_env":`+c.env+`}}`, "")
			_, all := cfg.Validate(ValidateOptions{})
			var fails []string
			for _, f := range all {
				if strings.HasPrefix(f, "FAIL .slots.lane_env") {
					fails = append(fails, f)
				}
			}
			if !slices.Equal(fails, c.fails) {
				t.Errorf("validate = %q, want %q", fails, c.fails)
			}
		})
	}
}

// T-1031: lanes is a count (sugar for that many fresh lanes) or a list of {name, keep_agent}; obor config validate
// refuses a list with a repeated name or a name not of the L-NN shape, and a list member with an unknown key does not
// load. The form the file has is the one written back.
func TestLanesCountOrList(t *testing.T) {
	type lane struct{ name, mode string }
	for _, c := range []struct {
		name, lanes string
		want        []lane
		fails       []string
		loadErr     string
	}{
		{name: "a count: fresh lanes L-01 … L-NN", lanes: `2`,
			want: []lane{{"L-01", LaneFresh}, {"L-02", LaneFresh}}},
		{name: "no lanes", lanes: `0`},
		{name: "a list: its names and modes, in its order", lanes: `[{"name": "L-02", "keep_agent": true}, {"name": "L-05"}]`,
			want: []lane{{"L-02", LaneKeep}, {"L-05", LaneFresh}}},
		{name: "duplicate names", lanes: `[{"name": "L-01", "keep_agent": true}, {"name": "L-01"}]`,
			want:  []lane{{"L-01", LaneKeep}, {"L-01", LaneFresh}},
			fails: []string{"FAIL .lanes[1].name: lane L-01 is listed twice"}},
		{name: "bad names", lanes: `[{"name": "l-01"}, {"name": "L-1"}, {"name": "L-00"}, {"name": ""}]`,
			want: []lane{{"l-01", LaneFresh}, {"L-1", LaneFresh}, {"L-00", LaneFresh}, {"", LaneFresh}},
			fails: []string{`FAIL .lanes[0].name: must be a lane name L-NN (L-01, L-02 …), got "l-01"`,
				`FAIL .lanes[1].name: must be a lane name L-NN (L-01, L-02 …), got "L-1"`,
				`FAIL .lanes[2].name: must be a lane name L-NN (L-01, L-02 …), got "L-00"`,
				`FAIL .lanes[3].name: must be a lane name L-NN (L-01, L-02 …), got ""`}},
		{name: "a negative count", lanes: `-1`, fails: []string{"FAIL .lanes: must be a non-negative integer (how many lanes), got -1"}},
		{name: "an unknown member key", lanes: `[{"name": "L-01", "keep": true}]`, loadErr: `unknown field "keep"`},
		{name: "neither a count nor a list", lanes: `"two"`, loadErr: "lanes takes a count or a list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(writeProject(t, `{"prefix":"pj","lanes_dir":"../wt","lanes":`+c.lanes+`}`, ""))
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
			if got, want := Compact(cfg.Data.Lanes), strings.ReplaceAll(c.lanes, " ", ""); got != want {
				t.Errorf("written back as %s, want %s", got, want)
			}
		})
	}
	keep := load(t, `{"prefix":"pj","lanes":[{"name":"L-01","keep_agent":true},{"name":"L-02"}]}`, "")
	if !keep.KeepLane("L-01") || keep.KeepLane("L-02") || keep.KeepLane("L-03") || !keep.HasLane("L-02") || keep.HasLane("L-03") {
		t.Errorf("KeepLane/HasLane: L-01 keep, L-02 fresh, L-03 none; got %v %v %v / %v %v", keep.KeepLane("L-01"),
			keep.KeepLane("L-02"), keep.KeepLane("L-03"), keep.HasLane("L-02"), keep.HasLane("L-03"))
	}
}

// T-1050: a config's revision (Config.Rev) is its effective JSON's, so a rewrite that leaves what runs alone keeps it,
// while an edit in .obor/local.json — where a model's id or a kind's args may live — changes it: either file counts,
// not only the committed one.
func TestRevIsTheEffectiveConfig(t *testing.T) {
	one := load(t, `{"name":"pj","prefix":"pj"}`, "")
	same := load(t, "{\n  \"name\": \"pj\",\n  \"prefix\": \"pj\"\n}\n", "")
	if len(one.json) == 0 || one.Rev() != same.Rev() {
		t.Errorf("rev %s of a rewritten config.json, want %s: the same effective config", same.Rev(), one.Rev())
	}
	local := load(t, `{"name":"pj","prefix":"pj"}`, `{"models":{"sonnet":{"kind":"claude","model":"sonnet-4-6"}}}`)
	if local.Rev() == one.Rev() {
		t.Errorf("rev %s with local.json's model, want another than %s: a changed model id behind an alias counts",
			local.Rev(), one.Rev())
	}
}
