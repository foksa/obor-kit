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
	c := load(t, `{"version":1,"prefix":"pj","lane_types":{"opus":{"kind":"claude"},"cx":{"kind":"codex"}}}`, "")
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
	for _, p := range []string{".paths.status", ".paths.queue", ".done_file", ".leads", ".spec"} { // removed (T-470, T-496)
		if got, ok := c.Get(p); ok {
			t.Errorf("%s = %s: removed, no default any more", p, got)
		}
	}
	for _, p := range []string{".lane_types.opus.settings", ".lane_types.cx.settings"} {
		if _, ok := c.Get(p); ok {
			t.Errorf("%s: the Remote Control default is applied at launch, not in the config (T-280)", p)
		}
	}
	if c.LanesDir() != filepath.Join(filepath.Dir(c.Root), "proj-wt") {
		t.Errorf("LanesDir = %s", c.LanesDir())
	}
}

func TestLocalOverlay(t *testing.T) {
	c := load(t, `{"prefix":"pj","merge":{"receipt_max_age":60},"done_file_sections":["A","B","C"],
		"lane_types":{"opus":{"kind":"claude","args":["--model","opus"],"max":4}},
		"commands":{"check":{"cmd":"make check","timeout":90}},
		"merge":{"receipt_max_age":60,"classes":[{"name":"a","paths":["src/a/**"]}]}}`,
		`{"merge":{"nightly_at":"02:00","classes":[{"name":"b","paths":["src/b/**"]}]},"done_file_sections":["X"],"lane_types":{"opus":{"max":2}},
		"commands":{"check":{"dir":"sub"}},"lanes_dir":"/wt"}`)
	if m := c.Data.Merge; m.ReceiptMaxAge != 60 || m.NightlyAt != "02:00" {
		t.Errorf("merge: objects merge key by key, got %+v", m)
	}
	if !reflect.DeepEqual(c.Data.DoneFileSections, []string{"X"}) {
		t.Errorf("a list is replaced whole: %q", c.Data.DoneFileSections)
	}
	if lt, _ := c.LaneType("opus"); lt.Max != 2 || !reflect.DeepEqual(lt.Args, []string{"--model", "opus"}) {
		t.Errorf("a lane type merges: %+v", lt)
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
	for _, cfg := range []string{`{"prefix":"pj","typo_key":1}`, `{"prefix":"pj","roles":{"mystery":{}}}`,
		`{"prefix":"pj","lane_types":{"opus":{"kind":"claude","modle":"x"}}}`, `{"prefix":"pj","commands":{"c":{"cmd":"x","tmeout":5}}}`} {
		_, err := Load(writeProject(t, cfg, ""))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: %v, want an unknown field error", cfg, err)
		}
	}
	_, err := Load(writeProject(t, `{"prefix":"pj"}`, `{"slots":{"maxx":1}}`))
	if err == nil || !strings.Contains(err.Error(), `unknown field "maxx"`) || !strings.Contains(err.Error(), "local.json") {
		t.Errorf("local.json unknown key: %v", err)
	}
	if _, err := Load(writeProject(t, `{"prefix":"pj","stop_idle_workers":"no"}`, "")); err == nil ||
		!strings.Contains(err.Error(), "stop_idle_workers") {
		t.Errorf("wrong type: %v", err)
	}
}

// TestSpecRemoved (T-496): spec.enforce_files and spec.files_heading still load, whatever their type (removed keys:
// validate warns).
func TestSpecRemoved(t *testing.T) {
	c := load(t, `{"prefix":"pj","spec":{"enforce_files":"no","files_heading":"Files"}}`, "")
	if c.Data.Spec.EnforceFiles != "no" || c.Data.Spec.FilesHeading != "Files" {
		t.Errorf("spec = %+v, want both kept as read", c.Data.Spec)
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
	c := load(t, `{"prefix":"pj","lane_types":{"z":{"kind":"codex"},"a":{"kind":"codex"}},
		"lanes":{"Z-01":"z","A-01":{"type":"a","agent":"x"},"M-01":"z"},
		"commands":{"zz":"z","aa":null,"mm":{"cmd":"m","timeout":5}},"agents":{"zed":{"type":"z"},"amy":{"type":"a"}}}`,
		`{"lanes":{"B-01":"a","Z-01":"a"}}`)
	var lanes []string
	for _, l := range c.Lanes() {
		lanes = append(lanes, l.Name+"="+l.Type)
	}
	if want := []string{"Z-01=a", "A-01=a", "M-01=z", "B-01=a"}; !reflect.DeepEqual(lanes, want) {
		t.Errorf("lanes = %q, want %q (a known key keeps its place, a new one is appended)", lanes, want)
	}
	for _, k := range []struct {
		got, want []string
	}{{c.Data.LaneTypes.Keys, []string{"z", "a"}}, {c.Data.Commands.Keys, []string{"zz", "aa", "mm"}},
		{c.Data.Agents.Keys, []string{"zed", "amy"}}} {
		if !reflect.DeepEqual(k.got, k.want) {
			t.Errorf("keys = %q, want %q", k.got, k.want)
		}
	}
	got, _ := raw(c, ".lanes")
	if want := "{\n  \"Z-01\": \"a\",\n  \"A-01\": {\n    \"type\": \"a\",\n    \"agent\": \"x\"\n  },\n  \"M-01\": \"z\",\n  \"B-01\": \"a\"\n}"; got != want {
		t.Errorf("config get .lanes = %s, want %s", got, want)
	}
	got, _ = raw(c, ".commands")
	if want := "{\n  \"zz\": \"z\",\n  \"aa\": null,\n  \"mm\": {\n    \"cmd\": \"m\",\n    \"timeout\": 5\n  }\n}"; got != want {
		t.Errorf("config get .commands = %s, want %s", got, want)
	}
	if _, ok := c.Command("aa"); ok {
		t.Error("a null command is set")
	}
}

func TestGet(t *testing.T) {
	c := load(t, `{"prefix":"pj","lanes":{"O-01":"opus"},"roles":{"integrator":{"handoff_tokens":0}}}`, "")
	for _, tc := range []struct {
		path, want string
		ok         bool
	}{
		{".prefix", "pj", true},
		{`.lanes["O-01"]`, "opus", true},
		{".lanes.O-01", "opus", true},
		{".roles.integrator.handoff_tokens", "0", true},
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

// TestLeadRemoved (T-467): roles.leads still loads (a removed key: validate warns) but no longer names or types a
// lead; roles.lead is the one lead.
func TestLeadRemoved(t *testing.T) {
	c := load(t, `{"prefix":"pj","roles":{"lead":{"type":"opus","agent":"boss"},
		"leads":[{"name":"mp","agent":"lead-mp"},{"name":"ui","agent":"lead-ui"}]}}`, "")
	if c.Data.Roles.Lead.Agent != "boss" || c.AgentName("lead", "") != "boss" {
		t.Errorf("roles.lead is the only lead: %q %q", c.Data.Roles.Lead.Agent, c.AgentName("lead", ""))
	}
	if a, ok := c.AgentLookup("boss", "", nil); !ok || a.Role != "lead" || a.Type != "opus" {
		t.Errorf("the lead's agent resolves to the lead: %+v %v", a, ok)
	}
	if _, ok := c.AgentLookup("lead-ui", "", nil); ok {
		t.Error("a roles.leads agent is no lead any more")
	}
	if one := load(t, `{"prefix":"pj","roles":{"lead":{"agent":"boss"}}}`, ""); one.AgentName("lead", "") != "boss" {
		t.Errorf("roles.lead.agent = %q", one.AgentName("lead", ""))
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

func TestIntegratorHandoffTokens(t *testing.T) {
	for cfg, want := range map[string]int64{
		`{"prefix":"pj"}`: 150000,
		`{"prefix":"pj","roles":{"integrator":{"type":"opus"}}}`:           150000,
		`{"prefix":"pj","roles":{"integrator":{"handoff_tokens":200000}}}`: 200000,
		`{"prefix":"pj","roles":{"integrator":{"handoff_tokens":0}}}`:      0,
	} {
		if got := load(t, cfg, "").IntegratorHandoffTokens(); got != want {
			t.Errorf("%s: IntegratorHandoffTokens = %d, want %d", cfg, got, want)
		}
	}
}

func TestRolesAndAgentNames(t *testing.T) {
	c := load(t, `{"prefix":"pj","lane_types":{"opus":{"kind":"claude"}},"lanes":{"O-01":"opus"},
		"roles":{"worker":{"agent":"custom-worker"},"reviewer":{"type":"opus"},"deep_dive":{"worktree":"dd"}}}`, "")
	for _, tc := range []struct{ role, lane, want string }{
		{"worker", "O-01", "custom-worker"}, {"reviewer", "", "pj-rev"}, {"lead", "", "pj-lead"}, {"integrator", "", "pj-int"},
		{"nobody", "", ""},
	} {
		if got := c.AgentName(tc.role, tc.lane); got != tc.want {
			t.Errorf("AgentName(%s) = %q, want %q", tc.role, got, tc.want)
		}
	}
	if got, ok := c.Role("reviewer"); !ok || got.Agent != "pj-rev" || got.Type != "opus" || got.Worktree != "review" || !got.Configured {
		t.Errorf("reviewer = %+v, %v", got, ok)
	}
	if got := c.RoleWorktrees(); !reflect.DeepEqual(got, []string{"review", "dd"}) {
		t.Errorf("RoleWorktrees = %q", got)
	}
	if got := c.Paths(); got.Reviews != "" || got.Get("tasks") != "" || got.Get("queue") != "" {
		t.Errorf("Paths = %+v", got)
	}
}

func TestAgentLookupPrecedence(t *testing.T) {
	root := writeProject(t, `{"prefix":"pj","lanes_dir":"../proj-wt","lane_types":{"opus":{"kind":"claude"}},
		"lanes":{"O-01":{"type":"opus","agent":"custom-lane"}},
		"roles":{"lead":{"type":"opus"},"integrator":{"agent":"my-int"},"triage":{},"deep_dive":{"type":"opus","worktree":"dd"}},
		"agents":{"adv":{"type":"opus"},"des":{"type":"opus","cwd":"sub/","role":"designer"},
			"far":{"type":"opus","cwd":"/tmp/x/../y"},"my-int":{"type":"opus"},"pj-o-01":{"type":"opus"}}}`, "")
	// The agent O-01's state file names (project.LaneAgent; config reads no state files).
	state := func(lane string) string {
		if lane == "O-01" {
			return "bob"
		}
		return ""
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LaneAgentAliases("O-01", state("O-01")); !reflect.DeepEqual(got, []string{"bob", "custom-lane", "pj-o-01", "pj-o1"}) {
		t.Errorf("LaneAgentAliases = %#v", got)
	}
	if _, ok := c.AgentLookup("pj-advisor", "", state); ok {
		t.Error("an optional role that is not configured is known")
	}
	wt := filepath.Join(filepath.Dir(root), "proj-wt")
	for name, want := range map[string]AgentInfo{
		"bob":          {Role: "worker", Lane: "O-01", Type: "opus", Cwd: wt + "/O-01"},
		"pj-o-01":      {Role: "worker", Lane: "O-01", Type: "opus", Cwd: wt + "/O-01"},
		"my-int":       {Role: "integrator", Cwd: root},
		"pj-lead":      {Role: "lead", Type: "opus", Cwd: root},
		"pj-deep-dive": {Role: "deep_dive", Type: "opus", Cwd: root}, // T-718: its old worktree dd is unused
		"adv":          {Type: "opus", Cwd: root},
		"des":          {Role: "designer", Type: "opus", Cwd: filepath.Join(root, "sub")},
		"far":          {Type: "opus", Cwd: "/tmp/y"},
	} {
		if got, ok := c.AgentLookup(name, "", state); !ok || got != want {
			t.Errorf("AgentLookup(%q) = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	if got, ok := c.AgentLookup("unknown", wt+"/O-01", state); !ok || got.Role != "worker" || got.Lane != "O-01" {
		t.Errorf("cwd lane lookup = %+v, %v", got, ok)
	}
	if _, ok := c.AgentLookup("unknown", "", state); ok {
		t.Error("unknown agent found")
	}
	if got, ok := c.AgentLookup("o-01", "", state); ok {
		t.Errorf("bare lane name o-01 found: %+v (T-302)", got)
	}
	if _, ok := c.AgentLookup("bob", "", nil); ok {
		t.Error("bob is known without the state agent")
	}
}

// T-280: Remote Control off and auto memory off for every claude agent but a lead; explicit settings overlay them (a lead has only its own).
func TestLaunchArgs(t *testing.T) {
	def := LaneType{Kind: "claude", Args: []string{"--model", "opus"}}
	own := LaneType{Kind: "claude", Args: []string{"--model", "opus"}, Settings: map[string]any{"x": 1}}
	cx := LaneType{Kind: "codex", Args: []string{"-m", "luna"}}
	for _, c := range []struct {
		name string
		lt   LaneType
		lead bool
		want string
	}{
		{"worker, default", def, false, `--model opus --settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`},
		{"lead, default: no --settings", def, true, "--model opus"},
		{"lead, explicit settings", own, true, `--model opus --settings {"x":1}`},
		{"worker, explicit settings overlay the defaults", own, false, `--model opus --settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false,"x":1}`},
		{"codex: no --settings", cx, false, "-m luna -c check_for_update_on_startup=false"},
	} {
		if got := strings.Join(c.lt.LaunchArgs(c.lead, ""), " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if len(def.Args) != 2 {
		t.Errorf("LaunchArgs changed the type's args: %q", def.Args)
	}
}

// T-647: a lane type's model and effort come from its args as each kind names them; a type that names none says "" and
// obor setup show must not guess one.
func TestLaneTypeModelAndEffort(t *testing.T) {
	for _, c := range []struct {
		name       string
		args       []string
		model, eff string
	}{
		{"claude", []string{"--model", "opus", "--effort", "high", "--permission-mode", "auto"}, "opus", "high"},
		{"claude, one word", []string{"--model=sonnet", "--effort=medium"}, "sonnet", "medium"},
		{"codex", []string{"-m", "gpt-6.1-sol", "-c", "model_reasoning_effort=xhigh", "-s", "workspace-write"}, "gpt-6.1-sol", "xhigh"},
		{"codex, joined -c", []string{"-m=gpt-6-luna", "-cmodel_reasoning_effort=low"}, "gpt-6-luna", "low"},
		{"no flag under it", []string{"--model"}, "", ""},
		{"haiku: no effort", []string{"--model", "haiku", "--permission-mode", "auto"}, "haiku", ""},
		{"opencode: neither", []string{"--auto", "--standalone"}, "", ""},
		{"codex: another -c", []string{"-c", "check_for_update_on_startup=false", "-m", "luna"}, "luna", ""},
	} {
		lt := LaneType{Kind: "claude", Args: c.args}
		if got := lt.Model(); got != c.model {
			t.Errorf("%s: Model() = %q, want %q", c.name, got, c.model)
		}
		if got := lt.Effort(); got != c.eff {
			t.Errorf("%s: Effort() = %q, want %q", c.name, got, c.eff)
		}
	}
}

// Lookup (obor config get) knows the unset fields of existing collection members, not invented members or Go names.
func TestLookupCollectionDefaults(t *testing.T) {
	t.Parallel()
	c := load(t, `{"prefix":"pj","commands":{"check":{"cmd":"true"}},"lane_types":{"opus":{"kind":"claude"}},
		"lanes":{"O-01":"opus"},"agents":{"amy":{"type":"opus"}},
		"merge":{"classes":[{"name":"go","paths":["**/*.go"]}]}}`, "")
	for _, tc := range []struct {
		path, want string
		known      bool
	}{
		{".mcp.jobs", "null", true},
		{".commands.check.heavy", "false", true},
		{`.commands["check"].heavy`, "false", true},
		{".commands.check.cmd", `"true"`, true},
		{".lane_types.opus.jobs", "null", true},
		{`.lane_types["opus"].jobs`, "null", true},
		{".lanes.O-01.agent", `""`, true},
		{`.lanes["O-01"].agent`, `""`, true},
		{".agents.amy.cwd", `""`, true},
		{".merge.classes[0].full", "false", true},
		{".merge.classes.0.full", "false", true},
		{".commands.nope.heavy", "", false},
		{".commands.check.nope", "", false},
		{".lane_types.nope.jobs", "", false},
		{".lanes.X-99.agent", "", false},
		{".agents.bob.cwd", "", false},
		{".merge.classes[1].full", "", false},
		{".merge.classes[-1].full", "", false},
		{".commands.Keys", "", false},
		{".lane_types.M", "", false},
		{".prefix.x", "", false},
	} {
		v, ok := c.Lookup(tc.path)
		if ok != tc.known || (ok && string(v) != tc.want) {
			t.Errorf("Lookup(%s) = %s, %v; want %s, %v", tc.path, v, ok, tc.want, tc.known)
		}
	}
}

// TaskIDOK and TaskFromBranch (T-470: from package lanes) follow task_id_pattern and branches.task.
func TestTaskFromBranch(t *testing.T) {
	c := load(t, `{"prefix":"pj","task_id_pattern":"TK[0-9]+","branches":{"task":"wip/{id}-lane"}}`, "")
	if c.TaskFromBranch("wip/TK12-lane") != "TK12" || c.TaskFromBranch("wip/T-12-lane") != "" ||
		c.TaskFromBranch("task/TK12") != "" {
		t.Error("TaskFromBranch")
	}
	if !c.TaskIDOK("TK1") || c.TaskIDOK("TK1x") || c.TaskIDOK("xTK1") || c.TaskIDOK("TK1\n") {
		t.Error("TaskIDOK matches a whole id only")
	}
	if bad := load(t, `{"prefix":"pj","task_id_pattern":"("}`, ""); bad.TaskIDOK("(") {
		t.Error("a pattern that does not compile matches nothing")
	}
}

// T-723: LaneTakes is a lane type a configured lane can run (a lane of it, or an untyped one: the pool); a name that is
// no lane type never. RoleAgentsOfType is the agents of the roles configured with a type (the lead by its own type),
// sorted: what a type's max counts besides its workers.
func TestLaneTakesAndRoleAgentsOfType(t *testing.T) {
	const types = `"lane_types":{"opus":{"kind":"claude"},"sonnet":{"kind":"claude"}}`
	typed := load(t, `{"prefix":"pj",`+types+`,"lanes":{"O-01":"opus"},
		"roles":{"lead":{"type":"opus","agent":"boss"},"reviewer":{"type":"opus","agent":"pj-rv"},
			"integrator":{"type":"sonnet","agent":"my-int"},"triage":{}}}`, "")
	pooled := load(t, `{"prefix":"pj",`+types+`,"lanes":{"O-01":"opus","L-01":""}}`, "")
	for _, c := range []struct {
		name string
		cfg  *Config
		typ  string
		want bool
	}{
		{"a lane of the type", typed, "opus", true},
		{"a type with no lane of it and no untyped lane", typed, "sonnet", false},
		{"an untyped lane runs any lane type", pooled, "sonnet", true},
		{"an untyped lane runs no name that is no lane type", pooled, "gemini", false},
	} {
		if got := c.cfg.LaneTakes(c.typ); got != c.want {
			t.Errorf("%s: LaneTakes(%s) = %v, want %v", c.name, c.typ, got, c.want)
		}
	}
	for typ, want := range map[string][]string{"opus": {"boss", "pj-rv"}, "sonnet": {"my-int"}, "haiku": nil} {
		if got := typed.RoleAgentsOfType(typ); !reflect.DeepEqual(got, want) {
			t.Errorf("RoleAgentsOfType(%s) = %#v, want %#v", typ, got, want)
		}
	}
	if got := pooled.RoleAgentsOfType("opus"); got != nil {
		t.Errorf("RoleAgentsOfType(opus) with no roles = %#v, want none", got)
	}
}
