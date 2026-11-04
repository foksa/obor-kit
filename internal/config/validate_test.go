package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/spec"
	"obor/internal/testkit"

	// The bridges register their kinds in init (cmd/obor imports them in production): validate needs them too.
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	_ "obor/internal/agents/opencode"
)

func TestMain(m *testing.M) { testkit.Main(m) }

type edit = func(map[string]any)

var set = testkit.Set

// del removes the value at a dotted path.
func del(path string) edit {
	return func(c map[string]any) {
		keys := strings.Split(path, ".")
		for _, k := range keys[:len(keys)-1] {
			next, ok := c[k].(map[string]any)
			if !ok {
				return
			}
			c = next
		}
		delete(c, keys[len(keys)-1])
	}
}

// validate is `obor config validate` on the fixture config (lane L-01) changed by edits: the WARN lines, then the
// FAIL lines. The baseline carries the integrator serve needs (T-428), so the cases that predate it stay about their
// own rule; a case that wants it gone deletes it.
func validate(t *testing.T, p *testkit.Project, edits ...edit) []string {
	t.Helper()
	base := []edit{set("roles.integrator", "opus")}
	p.Config(append(base, edits...)...)
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	warns, fails := c.Validate(config.ValidateOptions{Overlap: spec.Overlap,
		StateAgent: func(lane string) string { return project.LaneAgent(p.Root, lane) }})
	return append(warns, fails...)
}

func newProject(t *testing.T) *testkit.Project {
	p := testkit.New(t)
	p.Lane("L-01", "")
	return p
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name  string
		edits []edit
		want  []string // the WARN and FAIL lines; none = OK
	}{
		// bash config.test.sh case_validate_ok_template_and_fixture
		{name: "fixture default", want: nil},
		// T-437: every agent fetches its jobs; the old switches still load and warn once (mcp.jobs: only off).
		{name: "mcp.jobs true is how obor runs", edits: []edit{set("mcp.jobs", true)}},
		{name: "mcp.jobs false is obsolete", edits: []edit{set("mcp.jobs", false)},
			want: []string{"WARN mcp.jobs false is obsolete: every agent fetches its jobs (T-437), remove it"}},
		// RV-52 #1: a model's kind must be one with a bridge; a leftover cursor or a typo is a FAIL naming the kinds.
		{name: "cursor is no supported kind", edits: []edit{set("models.cur", map[string]any{"kind": "cursor", "model": "c"})},
			want: []string{`FAIL .models.cur.kind: "cursor" is not a supported kind: use one of claude, codex, opencode`}},
		{name: "typo is no supported kind", edits: []edit{set("models.opus", map[string]any{"kind": "claod", "model": "opus"})},
			want: []string{`FAIL .models.opus.kind: "claod" is not a supported kind: use one of claude, codex, opencode`}},
		{name: "a kinds key is a supported kind", edits: []edit{set("kinds.cursor", map[string]any{"args": []any{"-x"}})},
			want: []string{`FAIL .kinds.cursor: "cursor" is not a supported kind: use one of claude, codex, opencode`}},
		{name: "every supported kind is accepted", edits: []edit{
			set("models.a", map[string]any{"kind": "claude", "model": "a"}), set("models.b", map[string]any{"kind": "codex", "model": "b"}),
			set("models.c", map[string]any{"kind": "opencode", "model": "go/ds"})}},
		// T-705: a used opencode model without a model id warns (not fails): opencode's own default may not work.
		{name: "a used opencode model without a model id warns", edits: []edit{
			set("models.oc", map[string]any{"kind": "opencode", "model": ""}), set("difficulty.simple", []any{"oc"})},
			want: []string{`WARN models.oc: opencode model without a model id (opencode's own default may not work, and an effort needs one): set its "model", e.g. "opencode-go/deepseek-v4.1-flash"`}},
		{name: "an unused opencode model without a model id is fine", edits: []edit{
			set("models.oc", map[string]any{"kind": "opencode", "model": ""})}},
		{name: "a new model needs its kind", edits: []edit{set("models.cur", map[string]any{"model": "x"})},
			want: []string{"FAIL .models.cur.kind: required (claude, codex, opencode)"}},
		{name: "a built-in's override may keep its kind", edits: []edit{set("models.opus", map[string]any{"model": "claude-opus-5-5"})}},
		{name: "stop_hook.worker_max_blocks is obsolete", edits: []edit{set("stop_hook.worker_max_blocks", 3)},
			want: []string{"WARN stop_hook: no longer used, remove it"}},
		// T-465: the heartbeat's keys, the Stop hook's nudge keys, obor notify's backends and merge.full_every went with
		// the heartbeat: WARN, never FAIL; the config still loads.
		{name: "heartbeat, stop_hook, notify, merge.full_every removed", edits: []edit{
			set("heartbeat", map[string]any{"interval": 900, "stale_done": 1200, "fallback": 7200}),
			set("stop_hook", map[string]any{"max_nudges": 2, "cooldown": 600, "ack_ttl": 3600}),
			set("notify", map[string]any{"backends": []any{"herdr"}}), set("merge.full_every", 86400)},
			want: []string{"WARN heartbeat: no longer used, remove it", "WARN stop_hook: no longer used, remove it",
				"WARN notify: no longer used, remove it", "WARN merge.full_every: no longer used, remove it"}},
		// T-466: obor tell, the inbox and the obor mod went: inbox (max_age, stale_s, only) WARNs, never FAILs.
		{name: "inbox removed", edits: []edit{set("inbox", map[string]any{"only": []any{"pj-lead"}, "max_age": 3600, "stale_s": -1})},
			want: []string{"WARN inbox: no longer used, remove it"}},
		// T-496: the per-file guard went: spec.files_heading and spec.enforce_files WARN, never FAIL.
		{name: "spec keys removed", edits: []edit{set("spec", map[string]any{"files_heading": "Files", "enforce_files": false})},
			want: []string{"WARN spec.files_heading: no longer used, remove it",
				"WARN spec.enforce_files: no longer used, remove it"}},
		// T-597: one kit per machine; a leftover version pin WARNs, never fails.
		{name: "the version pin is removed", edits: []edit{set("obor", map[string]any{"version": "0.7.0"})},
			want: []string{"WARN obor: no longer used, remove it"}},
		// bash config.test.sh case_validate_removed_keys_warn; the keys removed by v0.10.0 are unknown (see
		// TestRemovedByV010KeysAreUnknown), the rest warn (TestRemovedKeysWithinTwoVersions).
		{name: "removed keys warn", edits: []edit{set("paths.research", "docs/research"), set("paths.questions", "QUESTIONS.md"),
			set("paths.tasks", "docs/tasks"),
			set("cache.warn_tokens", 50000)},
			want: []string{"WARN paths.research: no longer used, remove it", "WARN paths.questions: no longer used, remove it",
				"WARN cache.warn_tokens: no longer used, remove it",
				"WARN paths.tasks: no longer written; keep a custom path while its unfinished tasks have specs there (docs/tasks needs no key), then remove it"}},
		// T-428: owner decision 2026-10-24 — an integrator for every task; serve dispatches and merges for no project
		// without one, so the config is invalid without the entry and unchanged with it.
		{name: "no roles.integrator", edits: []edit{del("roles.integrator")},
			want: []string{"FAIL .roles.integrator: required: serve dispatches and merges nothing without an integrator"}},
		{name: "roles.integrator configured", edits: []edit{set("roles.integrator", "sonnet:high")}},
		{name: "roles.integrator with no model", edits: []edit{set("roles.integrator", "")}},
		// bash config.test.sh case_validate_one_fail_per_broken_field
		{name: "version 2", edits: []edit{set("version", 2)}, want: []string{"FAIL .version: must be 1, got 2"}},
		{name: "no version", edits: []edit{del("version")}, want: []string{"FAIL .version: must be 1, got null"}},
		{name: "no prefix", edits: []edit{del("prefix")}, want: []string{"FAIL .prefix: required string, got null"}},
		{name: "prefix Bad", edits: []edit{set("prefix", "Bad")},
			want: []string{`FAIL .prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "Bad"`}},
		{name: "prefix too long", edits: []edit{set("prefix", "toolongprefix")},
			want: []string{`FAIL .prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "toolongprefix"`}},
		{name: "prefix 1x", edits: []edit{set("prefix", "1x")}, want: []string{`FAIL .prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "1x"`}},
		// T-765: lanes is a count.
		{name: "no lanes", edits: []edit{set("lanes", 0)}},
		{name: "negative lanes", edits: []edit{set("lanes", -1)},
			want: []string{"FAIL .lanes: must be a non-negative integer (how many lanes), got -1"}},
		// T-538: a project's difficulty entry names the levels and the lane types it has; the built-in default skips what it lacks.
		{name: "difficulty names known models", edits: []edit{set("difficulty", map[string]any{"hard": []any{"opus"}})}},
		{name: "difficulty with an unknown model", edits: []edit{set("difficulty", map[string]any{"medium": []any{"opus", "ghost"}})},
			want: []string{`FAIL .difficulty.medium[1]: "ghost" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna)`}},
		{name: "difficulty with an unknown level", edits: []edit{set("difficulty", map[string]any{"easy": []any{"opus"}})},
			want: []string{"FAIL .difficulty.easy: is not a level: use hard, medium or simple"}},
		// T-695: a level's list and a role's type take `<type>[:<effort>]`, the effort one its kind takes.
		{name: "difficulty mixes models and model:effort", edits: []edit{
			set("difficulty", map[string]any{"hard": []any{"opus:high", "luna:xhigh", "opus"}}),
			set("roles.lead", "opus:max"), set("roles.reviewer", "luna:minimal")}},
		{name: "difficulty with an effort its kind lacks", edits: []edit{set("difficulty", map[string]any{"hard": []any{"opus", "opus:minimal"}})},
			want: []string{`FAIL .difficulty.hard[1]: opus:minimal: effort "minimal" is not one of claude's (low, medium, high, xhigh, max)`}},
		// T-697: an OpenCode effort rides on the model's id (obor never guesses OpenCode's model).
		{name: "an opencode effort needs the model's id", edits: []edit{
			set("models.oc", map[string]any{"kind": "opencode", "model": ""}),
			set("difficulty", map[string]any{"hard": []any{"deepseek:high", "oc:high", "oc"}})},
			want: []string{`WARN models.oc: opencode model without a model id (opencode's own default may not work, and an effort needs one): set its "model", e.g. "opencode-go/deepseek-v4.1-flash"`,
				`FAIL .difficulty.hard[1]: oc:high: opencode: effort high needs a model id (models.<name>.model, provider/model)`}},
		{name: "difficulty with an unknown model and an effort", edits: []edit{set("difficulty", map[string]any{"hard": []any{"ghost:high"}})},
			want: []string{`FAIL .difficulty.hard[0]: "ghost" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna)`}},
		{name: "a role with an effort its kind lacks", edits: []edit{set("roles.lead", "opus:turbo")},
			want: []string{`FAIL .roles.lead: opus:turbo: effort "turbo" is not one of claude's (low, medium, high, xhigh, max)`}},
		{name: "a role with a colon and no effort", edits: []edit{set("roles.integrator", "opus:")},
			want: []string{`FAIL .roles.integrator: "opus:" names no effort after the colon`}},
		{name: "kinds args with a bare --", edits: []edit{set("kinds.claude.args", []any{"--strict-mcp-config", "--"})},
			want: []string{"FAIL .kinds.claude.args: `--` is not allowed: obor adds the first prompt itself"}},
		{name: "kinds args with -- inside a value", edits: []edit{set("kinds.claude.args", []any{"--x=--"})}},
		// T-777: the agents key is gone: advisor, researcher and triage are roles; validate refuses it with the hint.
		{name: "the agents key is refused", edits: []edit{set("agents", `{"hk-fable": {"type": "fable"}}`)},
			want: []string{"FAIL .agents: no longer a config key: advisor, researcher and triage are roles " +
				"(roles.advisor, roles.researcher, roles.triage); obor setup migrate moves a matching entry and drops the rest"}},
		{name: "roles.lead unknown", edits: []edit{set("roles.lead", "ghost")},
			want: []string{`FAIL .roles.lead: "ghost" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna)`}},
		{name: "several problems: one line each", edits: []edit{set("version", 3), set("prefix", "X"), set("models.extra", map[string]any{})},
			want: []string{"FAIL .version: must be 1, got 3", `FAIL .prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "X"`,
				"FAIL .models.extra.kind: required (claude, codex, opencode)"}},
		// T-349
		{name: "receipt_max_age and cpus fine", edits: []edit{set("merge.receipt_max_age", 0), set("slots.cpus", 4)}},
		{name: "receipt_max_age negative", edits: []edit{set("merge.receipt_max_age", -1)},
			want: []string{"FAIL .merge.receipt_max_age: must be a non-negative integer (seconds), got -1"}},
		{name: "nightly_at fine", edits: []edit{set("merge.nightly_at", "02:00")}},
		{name: "nightly_at no time", edits: []edit{set("merge.nightly_at", "2am")},
			want: []string{`FAIL .merge.nightly_at: must be a local time HH:MM (00:00 to 23:59), got "2am"`}},
		{name: "nightly_at out of range", edits: []edit{set("merge.nightly_at", "24:00")},
			want: []string{`FAIL .merge.nightly_at: must be a local time HH:MM (00:00 to 23:59), got "24:00"`}},
		{name: "slots.cpus zero", edits: []edit{set("slots.cpus", 0)},
			want: []string{"FAIL .slots.cpus: must be a positive integer, got 0"}},
		{name: "slots.cpus negative", edits: []edit{set("slots.cpus", -2)},
			want: []string{"FAIL .slots.cpus: must be a positive integer, got -2"}},
		// T-720
		{name: "checks.load_limit fine", edits: []edit{set("checks.load_limit", 12.5)}},
		{name: "checks.load_limit 0 (never wait) fine", edits: []edit{set("checks.load_limit", 0)}},
		{name: "checks.load_limit negative", edits: []edit{set("checks.load_limit", -1)},
			want: []string{"FAIL .checks.load_limit: must be a non-negative number (a load average; 0: never wait), got -1"}},
		// T-777: the compact alias (pj-l1) and the agents key are gone: a lane's agent is <prefix>-l-NN or its row's.
		// bash config.test.sh case_validate_lane_ready
		{name: "lane_ready string", edits: []edit{set("commands.lane_ready", "test -d node_modules")}},
		{name: "lane_ready object", edits: []edit{set("commands.lane_ready", `{"cmd": "test -d node_modules", "timeout": 30}`)}},
		// bash config.test.sh case_validate_after_merge
		{name: "after_merge string", edits: []edit{set("commands.after_merge", "make build")}},
		{name: "after_merge object with when", edits: []edit{set("commands.after_merge", `{"cmd": "make build", "timeout": 60, "when": ["cmd/**", "go.mod"]}`)}},
		{name: "after_merge null", edits: []edit{set("commands.after_merge", nil)}},
		// bash config.test.sh case_validate_duplicate_class_names
		{name: "duplicate class name", edits: []edit{set("merge.classes", `[{"name": "same", "paths": ["src/core/**"], "full": true},
			{"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]}]`)},
			want: []string{`FAIL .merge.classes[1].name: duplicate of .merge.classes[0].name ("same")`}},
		{name: "one line per later duplicate", edits: []edit{set("merge.classes", `[{"name": "a", "paths": ["src/**"], "full": true},
			{"name": "b", "paths": ["x"]}, {"name": "a", "paths": ["y"]}, {"name": "b", "paths": ["z"]}]`)},
			want: []string{`FAIL .merge.classes[2].name: duplicate of .merge.classes[0].name ("a")`,
				`FAIL .merge.classes[3].name: duplicate of .merge.classes[1].name ("b")`}},
		{name: "distinct class names, same paths", edits: []edit{set("merge.classes", `[{"name": "core", "paths": ["src/**"], "full": true},
			{"name": "ui", "paths": ["src/**"], "checks": ["ui"]}]`)}},
		// bash config.test.sh case_validate_roles_triage, case_validate_roles_deep_dive
		{name: "roles.triage", edits: []edit{set("roles.triage", "opus")}},
		{name: "roles.deep_dive", edits: []edit{set("roles.deep_dive", "opus:high")}},
		{name: "deep_dive model unknown", edits: []edit{set("roles.deep_dive", "ghost")},
			want: []string{`FAIL .roles.deep_dive: "ghost" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna)`}},
		// bash config.test.sh case_validate_stop_idle_workers
		{name: "stop_idle_workers false", edits: []edit{set("stop_idle_workers", false)}},
		{name: "stop_idle_workers true", edits: []edit{set("stop_idle_workers", true)}},
		// bash config.test.sh case_validate_handoff_tokens
		{name: "handoff_tokens 500000", edits: []edit{set("handoff_tokens", 500000)}},
		{name: "handoff_tokens 0", edits: []edit{set("handoff_tokens", 0)}},
		{name: "handoff_tokens negative", edits: []edit{set("handoff_tokens", -1)},
			want: []string{"FAIL .handoff_tokens: must be a non-negative integer, got -1"}},
		{name: "integrator handoff_tokens 0", edits: []edit{set("integrator_handoff_tokens", 0)}},
		{name: "integrator handoff_tokens negative", edits: []edit{set("integrator_handoff_tokens", -5)},
			want: []string{"FAIL .integrator_handoff_tokens: must be a non-negative integer, got -5"}},
		// bash config.test.sh case_validate_allow_push
		{name: "allow_push a role and a derived name", edits: []edit{set("guards", `{"allow_push": ["lead", "pj-lead"]}`)}},
		{name: "allow_push no role", edits: []edit{set("guards", `{"allow_push": ["lead-mp"]}`)},
			want: []string{`WARN .guards.allow_push: "lead-mp" is no role (nor a role's agent name): it allows no push`}},
		{name: "allow_push empty", edits: []edit{set("guards", `{"allow_push": []}`)}},
		{name: "allow_push an empty name", edits: []edit{set("guards", `{"allow_push": [""]}`)},
			want: []string{`FAIL .guards.allow_push: must be a list of roles (e.g. ["lead"])`}},
		// bash config.test.sh case_validate_neutral_overlap
		{name: "default neutral disjoint", edits: []edit{set("merge", neutralBase)}},
		{name: "*.md crosses /", edits: []edit{set("merge", neutralBase), set("merge.neutral", []any{"docs/**", "*.md"})},
			want: []string{"WARN merge.neutral glob *.md overlaps class skills glob skills/**",
				"WARN merge.neutral glob *.md overlaps class ui glob src/ui/**"}},
		{name: "an explicit file under a class glob", edits: []edit{set("merge", neutralBase), set("merge.neutral", []any{"STATUS.md"}),
			func(c map[string]any) {
				ui := c["merge"].(map[string]any)["classes"].([]any)[1].(map[string]any)
				ui["paths"] = append(ui["paths"].([]any), "*.md")
			}},
			want: []string{"WARN merge.neutral glob STATUS.md overlaps class ui glob *.md"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newProject(t)
			got := validate(t, p, c.edits...)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("validate = %q\nwant %q", got, c.want)
			}
		})
	}
}

// Shapes are the load's: a wrong type or an unknown key fails config.Load with the key named; validate never sees it.
func TestShapeErrorsFailTheLoad(t *testing.T) {
	for _, c := range []struct {
		edit edit
		want string
	}{
		{set("commands.lane_ready", 5), "lane_ready"},
		{set("commands.after_merge", map[string]any{"cmd": "make build", "when": "cmd/**"}), "when"},
		{set("lanes", map[string]any{}), "lanes"},
		{set("models", []any{}), "models"},
		{set("kinds.claude.args", "x"), "args"},
		{set("roles.mystery", "opus"), `unknown field "mystery"`},
		{set("roles.integrator", 5), "integrator"},
		{set("merge.classes", []any{map[string]any{"name": "a", "paths": "x"}}), "paths"},
		{set("stop_idle_workers", "no"), "stop_idle_workers"},
		{set("handoff_tokens", 1.5), "handoff_tokens"},
		{set("guards", `{"allow_push": "pj-lead"}`), "allow_push"},
		{set("merge.clases", []any{}), `unknown field "clases"`},
		{set("merge.receipt_max_age", "1d"), "receipt_max_age"},
		{set("merge.receipt_max_age", 1.5), "receipt_max_age"},
		{set("slots.cpus", "4"), "cpus"},
		{set("slots.cpus", 2.5), "cpus"},
		{set("checks.load_limit", "20"), "load_limit"},
		{set("checks.loadlimit", 20), `unknown field "loadlimit"`},
	} {
		p := newProject(t)
		p.Config(c.edit)
		if _, err := config.Load(p.Root); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("load: %v, want an error naming %s", err, c.want)
		}
	}
}

const neutralBase = `{"guard": "g", "classes": [{"name": "skills", "paths": ["skills/**"], "checks": ["c"]},
	{"name": "ui", "paths": ["src/ui/**"], "checks": ["c"]}]}`

// T-431: the trunk branch exists in the main checkout (present: the fixture's main is OK; missing: FAIL with the hint).
func TestValidateTrunkRef(t *testing.T) {
	p := newProject(t)
	if got := validate(t, p); len(got) != 0 {
		t.Fatalf("trunk main present: %q", got)
	}
	want := "FAIL .branches.main: refs/heads/trunk does not exist: set branches.main: your trunk is not trunk"
	if got := validate(t, p, set("branches.main", "trunk")); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("trunk missing: %q, want %q", got, want)
	}
	p.Git(p.Root, "branch", "trunk")
	if got := validate(t, p, set("branches.main", "trunk")); len(got) != 0 {
		t.Errorf("trunk created: %q", got)
	}
}

// T-467: one lead per project. axis-alies's shape (one roles.leads entry plus roles.lead) validates with the WARN,
// never a FAIL; roles.lead is the lead.
func TestValidateRemovedLeadsWarn(t *testing.T) {
	p := newProject(t)
	got := validate(t, p,
		set("roles.lead", "opus"),
		set("roles.leads", `[{"name": "mp", "agent": "lead-mp", "ids": ["T-383..T-499"],
			"paths": ["src/net/**"], "lanes": ["L-01"]}]`))
	if want := []string{"WARN roles.leads: no longer used, remove it"}; !reflect.DeepEqual(got, want) {
		t.Errorf("validate = %q, want %q", got, want)
	}
}

// T-470: done_file, paths.status, paths.queue and leads.cross_ttl still load, each with its WARN.
func TestValidateRemovedT470Keys(t *testing.T) {
	p := newProject(t)
	got := validate(t, p, set("done_file", `".task-done.md"`), set("paths.status", `"STATUS.md"`),
		set("paths.queue", `"docs/QUEUE.md"`), set("leads", `{"cross_ttl": 3600}`))
	want := []string{"WARN done_file: no longer used, remove it", "WARN paths.status: no longer used, remove it",
		"WARN paths.queue: no longer used, remove it", "WARN leads.cross_ttl: no longer used, remove it"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("validate = %q, want %q", got, want)
	}
}

// releaseMinor is the minor version this tree ships in (v0.13). A removed key WARNs in the two minor versions after the
// release that removed it, then is an unknown key. Once releaseMinor is past a row's keepUntil, the key goes: its
// field, its WARN in config.warnings and its row in TestRemovedKeysWithinTwoVersions.
const releaseMinor = 13

// TestRemovedKeysWithinTwoVersions: each versioned removed key still WARNs, and the test fails once the release is past
// the key's two versions. paths.research and paths.questions have no recorded removal version, so they have no row.
func TestRemovedKeysWithinTwoVersions(t *testing.T) {
	for _, r := range []struct {
		key       string // the WARN's path
		e         edit
		removed   int // v0.<removed>.0 removed it
		keepUntil int // the last minor that still WARNs: removed + 2
	}{
		{"mcp.jobs", set("mcp.jobs", false), 11, 13},
		{"cache.warn_tokens", set("cache.warn_tokens", 50000), 12, 14},
		{"heartbeat", set("heartbeat", `{"interval": 900}`), 12, 14},
		{"stop_hook", set("stop_hook", `{"max_nudges": 2}`), 12, 14},
		{"notify", set("notify", `{"backends": ["herdr"]}`), 12, 14},
		{"inbox", set("inbox", `{"only": ["pj-lead"]}`), 12, 14},
		{"merge.full_every", set("merge.full_every", 86400), 12, 14},
		{"roles.leads", set("roles.leads", `[{"name": "mp"}]`), 12, 14},
		{"done_file", set("done_file", `".task-done.md"`), 12, 14},
		{"leads.cross_ttl", set("leads", `{"cross_ttl": 3600}`), 12, 14},
		{"paths.status", set("paths.status", `"STATUS.md"`), 12, 14},
		{"paths.queue", set("paths.queue", `"docs/QUEUE.md"`), 12, 14},
		{"spec.files_heading", set("spec.files_heading", `"Files"`), 13, 15},
		{"spec.enforce_files", set("spec.enforce_files", false), 13, 15},
		{"paths.tasks", set("paths.tasks", `"docs/tasks"`), 14, 16},
		{"obor", set("obor", `{"version": "0.7.0"}`), 17, 19},
	} {
		if r.keepUntil != r.removed+2 {
			t.Errorf("%s: keepUntil v0.%d is not two minors after v0.%d", r.key, r.keepUntil, r.removed)
		}
		if releaseMinor > r.keepUntil {
			t.Errorf("%s: removed in v0.%d and WARNs only through v0.%d: it is past its two versions, drop the key, "+
				"its WARN and this row", r.key, r.removed, r.keepUntil)
			continue
		}
		p := newProject(t)
		if got := validate(t, p, r.e); !warnsKey(got, r.key) {
			t.Errorf("%s: no WARN for it in %q", r.key, got)
		}
	}
}

// TestRemovedByV010KeysAreUnknown: the keys removed by v0.10.0 are gone (no WARN, nothing read): an unknown key fails
// the load, as any unknown key does. The removal list is the v0.12 upgrade guide's.
func TestRemovedByV010KeysAreUnknown(t *testing.T) {
	for _, c := range []struct {
		name  string
		edits []edit
		field string // the key the load names as unknown
	}{
		{"milestone", []edit{set("milestone", `{"tag": "m{n}"}`)}, "milestone"},
		{"branches.milestone", []edit{set("branches.milestone", `"ms/{name}"`)}, "milestone"},
		{"cache.refuse_cold", []edit{set("cache.refuse_cold", false)}, "refuse_cold"},
		{"keep_warm", []edit{set("keep_warm", `{"agents": ["a"]}`)}, "keep_warm"},
		{"githooks.notify", []edit{set("githooks.notify", `["post-merge"]`)}, "githooks"},
		{"dispatch.serve", []edit{set("dispatch.serve", true)}, "dispatch"},
		{"merge.serve", []edit{set("merge.serve", true)}, "serve"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t)
			p.Config(c.edits...)
			_, err := config.Load(p.Root)
			if want := `unknown field "` + c.field + `"`; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Load: %v, want %s", err, want)
			}
		})
	}
}

// warnsKey: one of the WARN lines is the removed key key's.
func warnsKey(lines []string, key string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, "WARN "+key+" ") || strings.HasPrefix(l, "WARN "+key+":") {
			return true
		}
	}
	return false
}

// T-777: a lane row's agent is one of the lane's names; when it names a role's agent, that is a collision.
func TestValidateLaneStateAgent(t *testing.T) {
	p := newProject(t)
	p.State("L-01", `{"lane":"L-01","agent":"pj-rev"}`)
	got := validate(t, p, set("roles.reviewer", "opus"))
	if want := []string{`FAIL .lanes: lane L-01's agent "pj-rev" collides with a role agent`}; !reflect.DeepEqual(got, want) {
		t.Errorf("validate = %q, want %q", got, want)
	}
}

// bash config.test.sh case_validate_ok_template_and_fixture: the template plus the local example validate; this kit's
// own config too (bash classify.test.sh case_real_valid).
func TestValidateTemplatesAndKitConfig(t *testing.T) {
	kit, _ := filepath.Abs("../..")
	for _, files := range [][2]string{
		{"templates/config.example.json", "templates/local.example.json"},
		{".obor/config.json", ""},
	} {
		p := testkit.New(t)
		b, err := os.ReadFile(filepath.Join(kit, files[0]))
		if err != nil {
			t.Fatal(err)
		}
		p.Write(".obor/config.json", string(b))
		if files[1] != "" {
			b, _ := os.ReadFile(filepath.Join(kit, files[1]))
			p.Write(".obor/local.json", string(b))
		}
		c, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		warns, fails := c.Validate(config.ValidateOptions{Overlap: spec.Overlap})
		if len(warns)+len(fails) != 0 {
			t.Errorf("%s: %q %q", files[0], warns, fails)
		}
		if files[0] == "templates/config.example.json" { // T-769: every role it lists names a model, the lead too
			for _, role := range []string{"lead", "integrator", "reviewer"} {
				if rc, ok := c.Role(role); !ok || rc.Type == "" {
					t.Errorf("template role %s = %+v, %v; want a model", role, rc, ok)
				}
			}
			if rc, _ := c.Role("lead"); config.ModelRef(rc.Type, rc.Effort) != "opus:high" {
				t.Errorf("template lead = %+v, want opus:high", rc)
			}
		}
		if files[1] != "" && (c.Data.Lanes != 2 || c.DifficultyList("hard")[0] != "opus:xhigh") {
			t.Errorf("local example overrides the template: lanes %d, hard %q; want 2, opus:xhigh", c.Data.Lanes, c.DifficultyList("hard"))
		}
	}
}

// T-695, T-765: a model ref is `<model>[:<effort>]`; CheckModelRef refuses an unknown model, an empty effort and one
// its kind does not take; a role's ref splits into Type and Effort (AgentInfo carries the effort).
func TestModelRef(t *testing.T) {
	for _, c := range []struct{ ref, typ, effort string }{
		{"opus", "opus", ""}, {"opus:high", "opus", "high"}, {"luna:xhigh", "luna", "xhigh"}, {"opus:", "opus", ""},
	} {
		if typ, effort := config.ParseModelRef(c.ref); typ != c.typ || effort != c.effort {
			t.Errorf("ParseModelRef(%q) = %q, %q; want %q, %q", c.ref, typ, effort, c.typ, c.effort)
		}
	}
	p := newProject(t)
	p.Config(set("roles.lead", "opus:high"))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for ref, refusal := range map[string]string{"opus": "", "opus:high": "", "opus:bad": `effort "bad" is not one of claude's`,
		"luna:xhigh": "", "luna:max": `effort "max" is not one of codex's`, "haiku:low": "",
		"ghost": `"ghost" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna)`, "opus:": "names no effort"} {
		err := cfg.CheckModelRef(ref)
		if refusal == "" && err != nil || refusal != "" && (err == nil || !strings.Contains(err.Error(), refusal)) {
			t.Errorf("CheckModelRef(%q) = %v, want %q", ref, err, refusal)
		}
	}
	if rc, _ := cfg.Role("lead"); rc.Type != "opus" || rc.Effort != "high" {
		t.Errorf("Role(lead) = type %q effort %q, want opus, high", rc.Type, rc.Effort)
	}
	if info, ok := cfg.AgentLookup("pj-lead", "", nil); !ok || info.Type != "opus" || info.Effort != "high" {
		t.Errorf("AgentLookup(pj-lead) = %+v, %v; want type opus, effort high", info, ok)
	}
}

// T-538, T-765: a level's model refs are the project's difficulty entry, else the built-in default; a name that is no
// level has none.
func TestDifficultyList(t *testing.T) {
	p := newProject(t)
	p.Config(set("difficulty", map[string]any{"medium": []string{"haiku", "luna:high"}}))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for level, want := range map[string]string{"hard": "opus:high", "medium": "haiku,luna:high", "simple": "deepseek,sonnet,haiku"} {
		if got := strings.Join(cfg.DifficultyList(level), ","); got != want {
			t.Errorf("DifficultyList(%s) = %q, want %q", level, got, want)
		}
	}
	if got := cfg.DifficultyList("easy"); got != nil {
		t.Errorf("DifficultyList(easy) = %v, want none", got)
	}
}

// T-704: roles.researcher is a known role: a model validates, a name that is no model fails.
func TestValidateAcceptsTheResearcherRole(t *testing.T) {
	p := newProject(t)
	if got := validate(t, p, set("roles.researcher", "sonnet")); len(got) != 0 {
		t.Errorf("validate = %q, want none", got)
	}
	got := validate(t, p, set("roles.researcher", "nosuchmodel"))
	if want := `FAIL .roles.researcher: "nosuchmodel" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna)`; !slices.Contains(got, want) {
		t.Errorf("validate = %q, want it to contain %q", got, want)
	}
}
