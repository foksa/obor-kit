package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/testkit"

	// The bridges register their kinds in init (cmd/obor imports them in production): validate needs them too
	// (OpenCode, a plugin kind, answers in process: testkit).
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
)

func TestMain(m *testing.M) { testkit.Main(m) }

type edit = func(map[string]any)

var set = testkit.Set

// trunks is Validate's trunk probe on a set of branches, so a row pays no git process (TestValidateTrunkRefIsGits
// keeps the real one).
func trunks(branches ...string) func(string) bool {
	return func(b string) bool { return slices.Contains(branches, b) }
}

// opts are Validate's options for a row: spec.Overlap, and a repository whose only branch is main (the fixture's).
func opts() config.ValidateOptions {
	return config.ValidateOptions{Overlap: spec.Overlap, Trunk: trunks("main")}
}

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
// FAIL lines (an unknown key's first, LoadForValidate's, as the command prints them). The base config carries the
// integrator serve needs, so the cases that predate it stay about their own rule; a case that wants it gone
// deletes it.
func validate(t *testing.T, p *testkit.Project, edits ...edit) []string {
	t.Helper()
	return validateWith(t, p, opts(), edits...)
}

// validateWith is validate with the options o.
func validateWith(t *testing.T, p *testkit.Project, o config.ValidateOptions, edits ...edit) []string {
	t.Helper()
	// a compile: the fixture's target is its default branch, and no compile is no validate line (TestValidateTrunkRef)
	base := []edit{set("agents.roles.integrator", "opus"), set("merge.compile", `{"cmd": "true"}`)}
	p.Config(append(base, edits...)...)
	c, unknown, err := config.LoadForValidate(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	warns, fails := c.Validate(o)
	return append(append(warns, unknown...), fails...)
}

// unknownFail is LoadForValidate's FAIL line for key (a dotted path) in config.json.
func unknownFail(key string) string {
	return "FAIL ." + key + ": unknown to this obor (retired, or newer than this binary): delete it (.obor/config.json)"
}

// newProject is a project laid out as files (testkit.NewDir) with lane L-01: these tests' subject is config.Load and
// Validate, which read files, and the trunk check has a stub (trunks); TestValidateTrunkRefIsGits runs the real one.
func newProject(t *testing.T) *testkit.Project {
	p := testkit.NewDir(t)
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
		// a model's kind must be one with a bridge; a leftover cursor or a typo is a FAIL naming the kinds.
		{name: "cursor is no supported kind", edits: []edit{set("agents.models.cur", map[string]any{"kind": "cursor", "id": "c"})},
			want: []string{`FAIL .agents.models.cur.kind: "cursor" is not a supported kind: use one of agy, claude, codex, opencode`}},
		{name: "typo is no supported kind", edits: []edit{set("agents.models.opus", map[string]any{"kind": "claod", "id": "opus"})},
			want: []string{`FAIL .agents.models.opus.kind: "claod" is not a supported kind: use one of agy, claude, codex, opencode`,
				`FAIL .agents.difficulty.hard[0]: opus:high: kind claod has no bridge, so no effort`}},
		{name: "a kinds key is a supported kind", edits: []edit{set("agents.kinds.cursor", map[string]any{"args": []any{"-x"}})},
			want: []string{`FAIL .agents.kinds.cursor: "cursor" is not a supported kind: use one of agy, claude, codex, opencode`}},
		{name: "every supported kind is accepted", edits: []edit{
			set("agents.models.a", map[string]any{"kind": "claude", "id": "a"}), set("agents.models.b", map[string]any{"kind": "codex", "id": "b"}),
			set("agents.models.c", map[string]any{"kind": "opencode", "id": "go/ds"})}},
		// a used opencode model without a model id warns (not fails): the kind's own default may not work.
		{name: "a used opencode model without a model id warns", edits: []edit{
			set("agents.models.oc", map[string]any{"kind": "opencode", "id": ""}), set("agents.difficulty.simple", []any{"oc"})},
			want: []string{`WARN agents.models.oc: opencode model without a model id (the kind's own default may not work, and an effort needs one): set its "id"`}},
		{name: "an unused opencode model without a model id is fine", edits: []edit{
			set("agents.models.oc", map[string]any{"kind": "opencode", "id": ""})}},
		// a kit kind's binary is the default (no FAIL for a missing plugin): a config that uses OpenCode (the
		// default levels' deepseek) with no agents.kinds.opencode.plugin validates clean.
		{name: "no agents.kinds.opencode.plugin, OpenCode used: answered in process, clean", edits: []edit{del("agents.kinds")}},
		{name: "a new model needs its kind", edits: []edit{set("agents.models.cur", map[string]any{"id": "x"})},
			want: []string{"FAIL .agents.models.cur.kind: required (agy, claude, codex, opencode)"}},
		// every entry names its kind, one obor init's starter list has too; a ref to it says the kind is missing.
		{name: "an entry with a starter name needs its kind too", edits: []edit{set("agents.models.opus", map[string]any{"id": "claude-opus-5-5"})},
			want: []string{"FAIL .agents.models.opus.kind: required (agy, claude, codex, opencode)",
				`FAIL .agents.roles.integrator: "opus" names no kind: set agents.models.opus.kind (agy, claude, codex, opencode)`,
				`FAIL .agents.difficulty.hard[0]: "opus" names no kind: set agents.models.opus.kind (agy, claude, codex, opencode)`}},
		// a model's name is the project's label: any string a ref and a Model: list can carry.
		{name: "a versioned model name", edits: []edit{func(c map[string]any) {
			c["agents"].(map[string]any)["models"].(map[string]any)["opus-4.6"] = map[string]any{"kind": "claude", "id": "claude-opus-4-6"}
		}, set("agents.roles.lead", "opus-4.6:high")}},
		{name: "a model name with a colon", edits: []edit{set("agents.models.op:1", map[string]any{"kind": "claude", "id": "opus"})},
			want: []string{`FAIL .agents.models["op:1"]: has ':': a model name has no ":" (the effort separator), "," (the Model: list separator) or whitespace`}},
		{name: "a model name with a comma", edits: []edit{set("agents.models.op,1", map[string]any{"kind": "claude", "id": "opus"})},
			want: []string{`FAIL .agents.models["op,1"]: has ',': a model name has no ":" (the effort separator), "," (the Model: list separator) or whitespace`}},
		{name: "a model name with a space", edits: []edit{set("agents.models.op 1", map[string]any{"kind": "claude", "id": "opus"})},
			want: []string{`FAIL .agents.models["op 1"]: has whitespace (' '): a model name has no ":" (the effort separator), "," (the Model: list separator) or whitespace`}},
		// owner decision 2026-10-24 — an integrator for every task; serve dispatches and merges for no project
		// without one, so the config is invalid without the entry and unchanged with it.
		{name: "no roles.integrator", edits: []edit{del("agents.roles.integrator")},
			want: []string{"FAIL .agents.roles.integrator: required: serve dispatches and merges nothing without an integrator"}},
		{name: "roles.integrator configured", edits: []edit{set("agents.roles.integrator", "sonnet:high")}},
		{name: "roles.integrator with no model", edits: []edit{set("agents.roles.integrator", "")}},
		// bash config.test.sh case_validate_one_fail_per_broken_field
		{name: "version 3", edits: []edit{set("version", 3)}, want: []string{"FAIL .version: must be 2, got 3"}},
		{name: "no prefix", edits: []edit{del("project.agent_prefix")}, want: []string{"FAIL .project.agent_prefix: required string, got null"}},
		{name: "prefix Bad", edits: []edit{set("project.agent_prefix", "Bad")},
			want: []string{`FAIL .project.agent_prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "Bad"`}},
		{name: "prefix too long", edits: []edit{set("project.agent_prefix", "toolongprefix")},
			want: []string{`FAIL .project.agent_prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "toolongprefix"`}},
		{name: "prefix 1x", edits: []edit{set("project.agent_prefix", "1x")}, want: []string{`FAIL .project.agent_prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "1x"`}},
		// lanes is a count.
		{name: "no lanes", edits: []edit{set("lanes.count", 0)}},
		{name: "negative lanes", edits: []edit{set("lanes.count", -1)},
			want: []string{"FAIL .lanes.count: must be a non-negative integer (how many lanes), got -1"}},
		// agents.difficulty names every level, each with models of the config.
		{name: "difficulty without a level", edits: []edit{del("agents.difficulty.medium")},
			want: []string{"FAIL .agents.difficulty.medium: required: the models a medium task goes to, a list of model refs"}},
		{name: "no difficulty", edits: []edit{del("agents.difficulty")},
			want: []string{"FAIL .agents.difficulty.hard: required: the models a hard task goes to, a list of model refs",
				"FAIL .agents.difficulty.medium: required: the models a medium task goes to, a list of model refs",
				"FAIL .agents.difficulty.simple: required: the models a simple task goes to, a list of model refs"}},
		{name: "difficulty names known models", edits: []edit{set("agents.difficulty.hard", []any{"opus"})}},
		{name: "difficulty with an unknown model", edits: []edit{set("agents.difficulty.medium", []any{"opus", "ghost"})},
			want: []string{`FAIL .agents.difficulty.medium[1]: "ghost" is not a model (agents.models: deepseek, fable, haiku, luna, opus, sol, sonnet, terra)`}},
		{name: "difficulty with an unknown level", edits: []edit{set("agents.difficulty.easy", []any{"opus"})},
			want: []string{"FAIL .agents.difficulty.easy: is not a level: use hard, medium, simple"}},
		// a level's list and a role's type take `<type>[:<effort>]`, the effort one its kind takes.
		{name: "difficulty mixes models and model:effort", edits: []edit{
			set("agents.difficulty.hard", []any{"opus:high", "luna:xhigh", "opus"}),
			set("agents.roles.lead", "opus:max"), set("agents.roles.reviewer", "luna:max")}},
		{name: "difficulty with an effort its kind lacks", edits: []edit{set("agents.difficulty.hard", []any{"opus", "opus:minimal"})},
			want: []string{`FAIL .agents.difficulty.hard[1]: opus:minimal: effort "minimal" is not one of claude's (low, medium, high, xhigh, max)`}},
		// an OpenCode effort rides on the model's id (obor never guesses OpenCode's model).
		{name: "an opencode effort needs the model's id", edits: []edit{
			set("agents.models.oc", map[string]any{"kind": "opencode", "id": ""}),
			set("agents.difficulty.hard", []any{"deepseek:high", "oc:high", "oc"})},
			want: []string{`WARN agents.models.oc: opencode model without a model id (the kind's own default may not work, and an effort needs one): set its "id"`,
				`FAIL .agents.difficulty.hard[1]: oc:high: kind opencode: start: effort high needs a model id (models.<name>.model, provider/model)`}},
		{name: "difficulty with an unknown model and an effort", edits: []edit{set("agents.difficulty.hard", []any{"ghost:high"})},
			want: []string{`FAIL .agents.difficulty.hard[0]: "ghost" is not a model (agents.models: deepseek, fable, haiku, luna, opus, sol, sonnet, terra)`}},
		{name: "a role with an effort its kind lacks", edits: []edit{set("agents.roles.lead", "opus:turbo")},
			want: []string{`FAIL .agents.roles.lead: opus:turbo: effort "turbo" is not one of claude's (low, medium, high, xhigh, max)`}},
		{name: "a role with a colon and no effort", edits: []edit{set("agents.roles.integrator", "opus:")},
			want: []string{`FAIL .agents.roles.integrator: "opus:" names no effort after the colon`}},
		{name: "kinds args with a bare --", edits: []edit{set("agents.kinds.claude.args", []any{"--strict-mcp-config", "--"})},
			want: []string{"FAIL .agents.kinds.claude.args: `--` is not allowed: obor adds the first prompt itself"}},
		{name: "kinds args with -- inside a value", edits: []edit{set("agents.kinds.claude.args", []any{"--x=--"})}},
		{name: "roles.lead unknown", edits: []edit{set("agents.roles.lead", "ghost")},
			want: []string{`FAIL .agents.roles.lead: "ghost" is not a model (agents.models: deepseek, fable, haiku, luna, opus, sol, sonnet, terra)`}},
		{name: "several problems: one line each", edits: []edit{set("version", 3), set("project.agent_prefix", "X"), set("agents.models.extra", map[string]any{})},
			want: []string{"FAIL .version: must be 2, got 3", `FAIL .project.agent_prefix: must match ^[a-z][a-z0-9-]{0,7}$, got "X"`,
				"FAIL .agents.models.extra.kind: required (agy, claude, codex, opencode)"}},
		{name: "slots.cpus fine", edits: []edit{set("checks.cpus_per_check", 4)}},
		// merge.receipt_max_age is retired: an unknown key, as are the promotion's three keys.
		{name: "merge.receipt_max_age is unknown", edits: []edit{set("merge.receipt_max_age", 60)},
			want: []string{unknownFail("merge.receipt_max_age")}},
		{name: "the promotion's keys are unknown", edits: []edit{set("branches.promote", "release"),
			set("merge.promote_weight", 2), set("merge.weights", map[string]any{"core": 3})},
			// branches is no group of shape 2: the promotion's member makes the whole key the unknown one (the map
			// writes its keys sorted)
			want: []string{unknownFail("branches"), unknownFail("merge.promote_weight"), unknownFail("merge.weights")}},
		{name: "nightly_at fine", edits: []edit{set("nightly.at", "02:00")}},
		{name: "nightly_at no time", edits: []edit{set("nightly.at", "2am")},
			want: []string{`FAIL .nightly.at: must be a local time HH:MM (00:00 to 23:59), got "2am"`}},
		{name: "nightly_at out of range", edits: []edit{set("nightly.at", "24:00")},
			want: []string{`FAIL .nightly.at: must be a local time HH:MM (00:00 to 23:59), got "24:00"`}},
		// nightly.also_run names defined, non-gating commands.
		{name: "nightly_commands fine", edits: []edit{set("commands.trim_cache", "true"),
			set("nightly.also_run", []any{"trim_cache"})}},
		// commands.e2e is a free command name like any other: set alone, it validates.
		{name: "commands.e2e is a free command name", edits: []edit{set("commands.e2e", "bun run e2e")}},
		{name: "nightly_commands empty", edits: []edit{set("nightly.also_run", []any{})}},
		{name: "nightly_commands an unknown name", edits: []edit{set("nightly.also_run", []any{"ghost"})},
			want: []string{"FAIL .nightly.also_run[0]: no commands.ghost: define it, or remove it"}},
		{name: "nightly_commands a gating command", edits: []edit{set("merge.check", "make check"),
			set("nightly.also_run", []any{"check"})},
			want: []string{`FAIL .nightly.also_run[0]: "check" is a gating command: nightly runs it as a gate, never non-gating`}},
		{name: "nightly_commands a merge class's check", edits: []edit{set("commands.ui", "true"),
			set("merge.by_path.rules", []any{map[string]any{"name": "ui", "paths": []any{"src/ui/**"}, "run": []any{"ui"}}}),
			set("nightly.also_run", []any{"ui"})},
			want: []string{`FAIL .nightly.also_run[0]: "ui" is a gating command: nightly runs it as a gate, never non-gating`}},
		{name: "slots.cpus zero", edits: []edit{set("checks.cpus_per_check", 0)},
			want: []string{"FAIL .checks.cpus_per_check: must be a positive integer, got 0"}},
		{name: "slots.cpus negative", edits: []edit{set("checks.cpus_per_check", -2)},
			want: []string{"FAIL .checks.cpus_per_check: must be a positive integer, got -2"}},
		// checks.load_limit went with the check slots' machine-load wait in v0.29; past its two minors it is an
		// unknown key of shape 2's checks group.
		{name: "checks.load_limit is unknown", edits: []edit{set("checks.load_limit", 12.5)},
			want: []string{unknownFail("checks.load_limit")}},
		{name: "workflow scratch-ff", edits: []edit{set("merge.workflow", "scratch-ff")}},
		{name: "workflow unknown kind", edits: []edit{set("merge.workflow", "x")},
			want: []string{`FAIL .merge.workflow: must be one of scratch-ff, got "x"`}},
		{name: "terminal herdr", edits: []edit{set("project.terminal", "herdr")}},
		// hc: every codex model of the fixture starts with its kind's flag (HCRequiredArgs), so none is refused.
		{name: "terminal hc", edits: []edit{set("project.terminal", "hc")}},
		{name: "terminal unknown kind", edits: []edit{set("project.terminal", "x")},
			want: []string{`FAIL .project.terminal: must be one of herdr, hc, got "x"`}},
		{name: "slots.lane_cpus fine", edits: []edit{set("lanes.cpus", 1)}},
		{name: "slots.lane_cpus zero", edits: []edit{set("lanes.cpus", 0)},
			want: []string{"FAIL .lanes.cpus: must be a positive integer, got 0"}},
		// there is no compact alias (pj-l1) and no agents key: a lane's agent is <prefix>-l-NN or its row's.
		// bash config.test.sh case_validate_lane_ready
		{name: "lane_ready string", edits: []edit{set("lanes.ready", "test -d node_modules")}},
		{name: "lane_ready object", edits: []edit{set("lanes.ready", `{"cmd": "test -d node_modules", "timeout": 30}`)}},
		// bash config.test.sh case_validate_after_merge
		{name: "after_merge string", edits: []edit{set("merge.after", "make build")}},
		{name: "after_merge object with when", edits: []edit{set("merge.after", `{"cmd": "make build", "timeout": 60, "when_changed": ["cmd/**", "go.mod"]}`)}},
		{name: "after_merge null", edits: []edit{set("merge.after", nil)}},
		// bash config.test.sh case_validate_duplicate_class_names
		{name: "duplicate class name", edits: []edit{set("merge.by_path.rules", `[{"name": "same", "paths": ["src/core/**"], "full": true},
			{"name": "same", "paths": ["src/ui/**"], "run": ["ui"]}]`)},
			want: []string{`FAIL .merge.by_path.rules[1].name: duplicate of .merge.by_path.rules[0].name ("same")`}},
		{name: "one line per later duplicate", edits: []edit{set("merge.by_path.rules", `[{"name": "a", "paths": ["src/**"], "full": true},
			{"name": "b", "paths": ["x"]}, {"name": "a", "paths": ["y"]}, {"name": "b", "paths": ["z"]}]`)},
			want: []string{`FAIL .merge.by_path.rules[2].name: duplicate of .merge.by_path.rules[0].name ("a")`,
				`FAIL .merge.by_path.rules[3].name: duplicate of .merge.by_path.rules[1].name ("b")`}},
		{name: "distinct class names, same paths", edits: []edit{set("merge.by_path.rules", `[{"name": "core", "paths": ["src/**"], "full": true},
			{"name": "ui", "paths": ["src/**"], "run": ["ui"]}]`)}},
		// with_full runs a class's checks after check on a full merge; on a full class it is meaningless.
		{name: "with_full on a checks class", edits: []edit{set("merge.by_path.rules", `[{"name": "ui", "paths": ["src/**"], "run": ["ui"]},
			{"name": "release", "paths": ["Makefile"], "run": ["release"], "also_on_full": true}]`)}},
		{name: "with_full on a full class", edits: []edit{set("merge.by_path.rules", `[{"name": "core", "paths": ["src/**"], "full": true},
			{"name": "core2", "paths": ["lib/**"], "full": true, "also_on_full": true}]`)},
			want: []string{"FAIL .merge.by_path.rules[1].also_on_full: meaningless with full: true: a full rule already runs merge.check"}},
		// bash config.test.sh case_validate_roles_triage, case_validate_roles_deep_dive
		{name: "roles.triage", edits: []edit{set("agents.roles.triage", "opus")}},
		{name: "roles.deep_dive", edits: []edit{set("agents.roles.deep_dive", "opus:high")}},
		{name: "deep_dive model unknown", edits: []edit{set("agents.roles.deep_dive", "ghost")},
			want: []string{`FAIL .agents.roles.deep_dive: "ghost" is not a model (agents.models: deepseek, fable, haiku, luna, opus, sol, sonnet, terra)`}},
		// bash config.test.sh case_validate_handoff_tokens
		{name: "handoff_tokens 500000", edits: []edit{set("agents.handoff_tokens", 500000)}},
		{name: "handoff_tokens 0", edits: []edit{set("agents.handoff_tokens", 0)}},
		{name: "handoff_tokens negative", edits: []edit{set("agents.handoff_tokens", -1)},
			want: []string{"FAIL .agents.handoff_tokens: must be a non-negative integer, got -1"}},
		// bash config.test.sh case_validate_allow_push
		{name: "allow_push a role", edits: []edit{set("rules", `{"may_push": ["lead", "integrator"]}`)}},
		{name: "allow_push no role", edits: []edit{set("rules", `{"may_push": ["lead-mp"]}`)},
			want: []string{`WARN .rules.may_push: "lead-mp" is no role: it allows no push`}},
		{name: "allow_push empty", edits: []edit{set("rules", `{"may_push": []}`)}},
		{name: "allow_push an empty name", edits: []edit{set("rules", `{"may_push": [""]}`)},
			want: []string{`FAIL .rules.may_push: must be a list of roles (e.g. ["lead"])`}},
		// rules.deny_commands entries have a match that compiles and the message the guard denies with.
		{name: "deny_commands a rule", edits: []edit{set("rules",
			`{"deny_commands": [{"match": "^bun test --coverage", "alone": true, "message": "a coverage run is a load run"}]}`)}},
		{name: "deny_commands a pairs rule", edits: []edit{set("rules",
			`{"deny_commands": [{"match": "^bun test", "message": "two bun test commands at once are a load run"}]}`)}},
		{name: "deny_commands a bad regex", edits: []edit{set("rules", `{"deny_commands": [{"match": "[", "message": "no"}]}`)},
			want: []string{"FAIL .rules.deny_commands[0].match: must be a regular expression: missing closing ]: `[`"}},
		{name: "deny_commands no match", edits: []edit{set("rules", `{"deny_commands": [{"message": "no"}]}`)},
			want: []string{"FAIL .rules.deny_commands[0].match: required: a regular expression on the resolved command line"}},
		{name: "deny_commands no message", edits: []edit{set("rules", `{"deny_commands": [{"match": "^x"}]}`)},
			want: []string{"FAIL .rules.deny_commands[0].message: required: the text the guard denies with"}},
		// bash config.test.sh case_validate_neutral_overlap
		// no merge.by_path.no_check: the docs/** class the config gets by default overlaps the default neutral glob.
		{name: "no neutral set, a class on the docs/** default", edits: []edit{set("merge.by_path", `{"guard": "g", "rules": [{"name": "docs", "paths": ["docs/**"], "run": ["c"]}]}`)},
			want: []string{"WARN merge.by_path.no_check glob docs/** overlaps rule docs glob docs/**"}},
		// an empty list is not the default: it keeps every file (Classify's reading of it) and warns of no overlap.
		{name: "an empty neutral list, a class on docs/**", edits: []edit{set("merge.by_path", `{"guard": "g", "no_check": [], "rules": [{"name": "docs", "paths": ["docs/**"], "run": ["c"]}]}`)}},
		{name: "*.md crosses /", edits: []edit{set("merge.by_path", neutralBase), set("merge.by_path.no_check", []any{"docs/**", "*.md"})},
			want: []string{"WARN merge.by_path.no_check glob *.md overlaps rule skills glob skills/**",
				"WARN merge.by_path.no_check glob *.md overlaps rule ui glob src/ui/**"}},
		{name: "an explicit file under a class glob", edits: []edit{set("merge.by_path", neutralBase), set("merge.by_path.no_check", []any{"STATUS.md"}),
			func(c map[string]any) {
				ui := c["merge"].(map[string]any)["by_path"].(map[string]any)["rules"].([]any)[1].(map[string]any)
				ui["paths"] = append(ui["paths"].([]any), "*.md")
			}},
			want: []string{"WARN merge.by_path.no_check glob STATUS.md overlaps rule ui glob *.md"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// no git: the table is about the config's rules; the trunk check has TestValidateTrunkRef
			p := newProject(t)
			got := validate(t, p, c.edits...)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("validate = %q\nwant %q", got, c.want)
			}
		})
	}
}

// Shapes are the load's: a wrong type fails config.Load, an unknown key LoadForValidate, each with the key
// named; validate never sees it.
func TestShapeErrorsFailTheLoad(t *testing.T) {
	p := newProject(t)
	for _, c := range []struct {
		edit edit
		want string
	}{
		{set("lanes.ready", 5), "lanes.ready: "},
		{set("merge.after", map[string]any{"cmd": "make build", "when_changed": "cmd/**"}), "when"},
		// there is no merge.after.restart_serve: binary following is serve's one automatic restart.
		{set("merge.after", map[string]any{"cmd": "make build", "restart_serve": true}), ".merge.after.restart_serve: unknown"},
		{set("lanes.count", "two"), "lanes.count"},
		{set("agents.models", []any{}), "models"},
		{set("agents.kinds.claude.args", "x"), "args"},
		// roles.mystery is a custom role (validate checks it); a key its row does not know fails the load.
		{set("agents.roles.mystery", map[string]any{"model": "opus", "writes": []string{}}), ".agents.roles.mystery.writes: unknown"},
		{set("agents.roles.integrator", 5), "integrator"},
		{set("agents.roles", 5), "roles: must be an object"},
		{set("merge.by_path.rules", []any{map[string]any{"name": "a", "paths": "x"}}), "paths"},
		{set("agents.handoff_tokens", 1.5), "handoff_tokens"},
		{set("rules", `{"may_push": "pj-lead"}`), "may_push"},
		{set("merge.clases", []any{}), ".merge.clases: unknown"},
		// the shape before v0.23 is no special case past its two-minor window: its keys are unknown keys (in a
		// file of shape 2 agents is a group).
		{set("agents", map[string]any{"triage": map[string]any{"type": "opus"}}), ".agents.triage: unknown"},
		{set("lane_types", map[string]any{"opus": map[string]any{"kind": "claude"}}), ".lane_types: unknown"},
		// a removed key past its two-minor window is an unknown key, as any key this binary does not know.
		// the two empty v0.28 sections are past theirs too.
		{set("mcp", map[string]any{}), ".mcp: unknown"},
		{set("cache", map[string]any{}), ".cache: unknown"},
		{set("heartbeat", map[string]any{"interval": 900}), "heartbeat"},
		{set("stop_hook", map[string]any{"max_nudges": 2}), "stop_hook"},
		{set("notify", map[string]any{"backends": []any{"herdr"}}), "notify"},
		{set("inbox", map[string]any{"only": []any{"pj-lead"}}), "inbox"},
		{set("merge.full_every", 86400), "full_every"},
		{set("done_file", ".task-done.md"), "done_file"},

		{set("leads", map[string]any{"cross_ttl": 3600}), "leads"},
		{set("spec", map[string]any{"files_heading": "Files"}), "spec"},
		// roles.leads is no special case: the leads row is a shape the load refuses, naming the key.
		{set("agents.roles", `{"leads": [{"name": "mp"}]}`), "leads"},
		{set("obor", map[string]any{"version": "0.7.0"}), "obor"},
		{set("checks.cpus_per_check", "4"), "cpus"},
		{set("checks.cpus_per_check", 2.5), "cpus"},
		{set("lanes.cpus", "3"), "lanes.cpus"},
		{set("checks.loadlimit", 20), ".checks.loadlimit: unknown"},
		// the nine v0.28 keys are past their two-minor window: each is an unknown key now.
		{set("merge.max_load", 10), ".merge.max_load: unknown"},
	} {
		p.Config(c.edit)
		_, unknown, err := config.LoadForValidate(p.Root)
		got := strings.Join(unknown, "\n")
		if err != nil {
			got = err.Error()
		}
		if got == "" || !strings.Contains(got, c.want) {
			t.Errorf("load: %q, want an error naming %s", got, c.want)
		}
	}
}

const neutralBase = `{"guard": "g", "rules": [{"name": "skills", "paths": ["skills/**"], "run": ["c"]},
	{"name": "ui", "paths": ["src/ui/**"], "run": ["c"]}]}`

// the trunk branch exists in the main checkout (present: the fixture's main is OK; missing: FAIL with the hint). No
// merge.compile is no validate line, not even with the trunk on the default branch: obor init gives that warning, where
// the choice is made (TestValidateTrunkRefIsGits: NoCompileWarning names it on the default branch, never on another).
func TestValidateTrunkRef(t *testing.T) {
	p := newProject(t)
	on := func(branches ...string) func(...edit) []string {
		return func(edits ...edit) []string {
			t.Helper()
			return validateWith(t, p, config.ValidateOptions{Overlap: spec.Overlap, Trunk: trunks(branches...)}, edits...)
		}
	}
	if got := on("main")(); len(got) != 0 {
		t.Fatalf("trunk main present: %q", got)
	}
	want := "FAIL .merge.target_branch: refs/heads/trunk does not exist: set merge.target_branch: your trunk is not trunk"
	if got := on("main")(set("merge.target_branch", "trunk")); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("trunk missing: %q, want %q", got, want)
	}
	if got := on("main", "trunk")(set("merge.target_branch", "trunk")); len(got) != 0 {
		t.Errorf("trunk created: %q", got)
	}
	noCompile := set("merge.compile", nil)
	if got := on("main")(set("merge.target_branch", "main"), noCompile); len(got) != 0 {
		t.Errorf("no compile, target the default branch: %q, want no validate line (obor init warns)", got)
	}
	if got := on("main", "trunk")(set("merge.target_branch", "trunk"), noCompile); len(got) != 0 {
		t.Errorf("no compile, target a branch other than the default: %q, want none", got)
	}
	// NoCompileWarning on a branch its caller read runs no git (a Root with no repository: a read of its own would
	// find none and give no warning).
	cfg := func(target, compile string) *config.Config {
		c := &config.Config{Root: t.TempDir()}
		c.Data.Merge.TargetBranch = target
		c.Data.Merge.Compile.Cmd = compile
		return c
	}
	warning := "merge.compile is empty and merge.target_branch trunk is the default branch: " + config.NoCompile
	for _, row := range []struct{ name, target, compile, def, want string }{
		{"passed branch is the target: warns", "trunk", "", "trunk", warning},
		{"passed branch is another: none", "main", "", "trunk", ""},
		{"a compile is set: none", "trunk", "make build", "trunk", ""},
		{"no target: none", "", "", "trunk", ""},
	} {
		if got := cfg(row.target, row.compile).NoCompileWarning(row.def); got != row.want {
			t.Errorf("%s: %q, want %q", row.name, got, row.want)
		}
	}
	if got := config.DefaultBranch(func(args ...string) (string, error) {
		if args[len(args)-1] == "refs/remotes/origin/HEAD" {
			return "origin/trunk\n", nil
		}
		return "main\n", nil
	}); got != "trunk" {
		t.Errorf("DefaultBranch with an origin HEAD: %q, want trunk (origin's, not the checked-out main)", got)
	}
}

// bash config.test.sh case_validate_ok_template_and_fixture: the template plus the local example validate. The kit's own
// .obor/config.json is not a product invariant: the kit's guard runs `bin/obor config validate` on it.
func TestValidateTemplates(t *testing.T) {
	kit, _ := filepath.Abs("../..")
	p := testkit.New(t)
	b, err := os.ReadFile(filepath.Join(kit, "templates/config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.Write(".obor/config.json", string(b))
	b, err = os.ReadFile(filepath.Join(kit, "templates/local.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.Write(".obor/local.json", string(b))
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	warns, fails := c.Validate(opts())
	if len(warns)+len(fails) != 0 {
		t.Errorf("templates: %q %q", warns, fails)
	}
	// every role it lists names a model, the lead too
	for _, role := range []string{"lead", "integrator", "reviewer"} {
		if rc, ok := c.Role(role); !ok || rc.Type == "" {
			t.Errorf("template role %s = %+v, %v; want a model", role, rc, ok)
		}
	}
	if rc, _ := c.Role("lead"); config.ModelRef(rc.Type, rc.Effort) != "opus:high" {
		t.Errorf("template lead = %+v, want opus:high", rc)
	}
	if c.LaneCount() != 2 || c.LanesDirSetting() != "../myproject-wt" {
		t.Errorf("local example overrides the template: lanes %d, dir %q; want 2, ../myproject-wt", c.LaneCount(),
			c.LanesDirSetting())
	}
	for _, level := range []string{"hard", "medium", "simple"} {
		if len(c.DifficultyList(level)) == 0 {
			t.Errorf("template level %s names no model", level)
		}
	}
}

// a model ref is `<model>[:<effort>]`; CheckModelRef refuses an unknown model, an empty effort and one
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
	p.Config(set("agents.roles.lead", "opus:high"))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for ref, refusal := range map[string]string{"opus": "", "opus:high": "", "opus:bad": `effort "bad" is not one of claude's`,
		"luna:xhigh": "", "luna:max": "", "terra:ultra": "", "luna:minimal": `effort "minimal" is not one of codex's (low, medium, high, xhigh, max, ultra)`, "haiku:low": "",
		"ghost": `"ghost" is not a model (agents.models: deepseek, fable, haiku, luna, opus, sol, sonnet, terra)`, "opus:": "names no effort"} {
		err := cfg.CheckModelRef(ref)
		if refusal == "" && err != nil || refusal != "" && (err == nil || !strings.Contains(err.Error(), refusal)) {
			t.Errorf("CheckModelRef(%q) = %v, want %q", ref, err, refusal)
		}
	}
	if rc, _ := cfg.Role("lead"); rc.Type != "opus" || rc.Effort != "high" {
		t.Errorf("Role(lead) = type %q effort %q, want opus, high", rc.Type, rc.Effort)
	}
	if info, ok := cfg.AgentLookup("pj-lead", ""); !ok || info.Type != "opus" || info.Effort != "high" {
		t.Errorf("AgentLookup(pj-lead) = %+v, %v; want type opus, effort high", info, ok)
	}
}

// a level's model refs are the project's difficulty entry; a level it does not name, and a name that is no level,
// have none.
func TestDifficultyList(t *testing.T) {
	p := newProject(t)
	p.Config(set("agents.difficulty", map[string]any{"medium": []string{"haiku", "luna:high"}}))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for level, want := range map[string]string{"hard": "", "medium": "haiku,luna:high", "simple": ""} {
		if got := strings.Join(cfg.DifficultyList(level), ","); got != want {
			t.Errorf("DifficultyList(%s) = %q, want %q", level, got, want)
		}
	}
	if got := cfg.DifficultyList("easy"); got != nil {
		t.Errorf("DifficultyList(easy) = %v, want none", got)
	}
}

// the project's committed CLI config, notes only (never a FAIL): a .claude/settings.json with disableAllHooks
// true, an opencode.json defining mcp.servers.obor; none for a clean project.
func TestValidateProjectFiles(t *testing.T) {
	p := newProject(t)
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(p.Root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/settings.json", `{"enabledMcpjsonServers":["mine"],"autoMemoryEnabled":true}`)
	write("opencode.json", `{"mcp":{"servers":{"mine":{}}}}`)
	write(".codex/config.toml", "sandbox_mode = \"read-only\"\n")
	if got := validate(t, p); len(got) != 0 {
		t.Fatalf("clean project: %q, want nothing", got)
	}
	write(".claude/settings.json", `{"disableAllHooks":true}`)
	write("opencode.json", `{"mcp":{"servers":{"obor":{"type":"local"}}}}`)
	got := validate(t, p)
	want := []string{
		"WARN .claude/settings.json: disableAllHooks is true: it turns off obor's guard hooks (the plugin's hooks)",
		"WARN opencode.json: mcp.servers.obor replaces obor's own server (the project's config wins): remove it",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("validate = %q, want %q", got, want)
	}
}

// TestPluginKind: agents.kinds.<name>.plugin makes a plugin kind of the loaded config (Config.Kind finds it, a
// model may name it); validate runs its describe: a command that answers passes, one that is not executable, answers for another
// kind, or takes a compiled kind's name fails.
func TestPluginKind(t *testing.T) {
	p := newProject(t)
	cmd := filepath.Join(p.Root, "kinds", "obor-kind-mine")
	if err := os.MkdirAll(filepath.Dir(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"version\":1,\"name\":\"mine\",\"status\":\"hooks\",\"stop\":{\"quit\":\"/q\"}}'\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmd+".txt", []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := set("agents.kinds.mine", map[string]any{"plugin": "kinds/obor-kind-mine"})
	got := validate(t, p, mine, set("agents.models.m1", map[string]any{"kind": "mine", "id": ""}))
	if len(got) != 0 {
		t.Errorf("a plugin kind that answers: %q, want OK", got)
	}
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Kinds().IsPlugin("mine") {
		t.Fatal("kinds.mine.plugin: no plugin kind mine in the loaded config")
	}
	if _, ok := c.Kind("mine"); !ok {
		t.Error("Kind(mine): none")
	}
	for _, c := range []struct {
		name string
		edit edit
		want string
	}{
		{"not executable", set("agents.kinds.mine", map[string]any{"plugin": "kinds/obor-kind-mine.txt"}),
			"FAIL .agents.kinds.mine.plugin: " + cmd + ".txt is not an executable file"},
		{"another kind's describe", set("agents.kinds.other", map[string]any{"plugin": cmd}),
			`FAIL .agents.kinds.other.plugin: kind other: describe: manifest names kind "mine", the config "other"`},
		{"a compiled kind's name", set("agents.kinds.claude", map[string]any{"plugin": cmd}),
			"FAIL .agents.kinds.claude.plugin: claude is a compiled kind: a plugin kind takes a name of its own"},
	} {
		got := validate(t, p, c.edit)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	validate(t, p) // a load without agents.kinds.<name>.plugin: no plugin kind mine
	without, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := without.Kind("mine"); ok {
		t.Error("a config without kinds.mine has kind mine")
	}
}

// A kit kind (agy, OpenCode) with no plugin, or one naming its kit binary, is answered in process: its kind has the
// manifest with nothing cached, and serve's cache step (plugin.Ensure) runs nothing (an executable of that name at
// the root is never run). A plugin naming another path runs that command.
func TestKitKindInProcess(t *testing.T) {
	for _, kind := range []string{"agy", "opencode"} {
		bin := "obor-kind-" + kind
		p := newProject(t)
		log := filepath.Join(p.Root, "runs")
		script := "#!/bin/sh\necho \"$1\" >>" + log + "\ncat >/dev/null\necho '{\"version\":1,\"name\":\"" + kind +
			"\",\"status\":\"terminal\"}'\n"
		for _, f := range []string{bin, "own-" + kind} {
			if err := os.WriteFile(filepath.Join(p.Root, f), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct {
			name, plugin string
			linked       bool
		}{{"no plugin", "", true}, {"the kit binary", bin, true}, {"another path", "own-" + kind, false}} {
			os.Remove(log)
			os.RemoveAll(filepath.Join(p.Root, ".obor", "state"))
			if c.plugin == "" {
				p.Config()
			} else {
				p.Config(set("agents.kinds."+kind, map[string]any{"plugin": c.plugin}))
			}
			cfg, err := config.Load(p.Root)
			if err != nil {
				t.Fatal(err)
			}
			k, ok := cfg.Kind(kind)
			if !ok || !cfg.Kinds().IsPlugin(kind) {
				t.Fatalf("%s, %s: precondition: a plugin kind %s (%v)", kind, c.name, kind, ok)
			}
			wrote, err := plugin.Ensure(kind, cfg.KindCommand(kind), cfg.StateDir())
			runs, _ := os.ReadFile(log)
			_, cached := os.Stat(plugin.CacheFile(cfg.StateDir(), kind))
			switch {
			case c.linked && (k.(agents.Uncached).Uncached() != nil || k.Caps().Status == ""):
				t.Errorf("%s, %s: the kind has no manifest: %v", kind, c.name, k.(agents.Uncached).Uncached())
			case c.linked && (wrote || err != nil || len(runs) != 0 || cached == nil):
				t.Errorf("%s, %s: Ensure wrote %v (%v), runs %q, cache %v; want in process: nothing run or cached",
					kind, c.name, wrote, err, runs, cached)
			case !c.linked && (!wrote || err != nil || string(runs) != "describe\n" || cached != nil):
				t.Errorf("%s, %s: Ensure wrote %v (%v), runs %q, cache %v; want the command's describe, cached",
					kind, c.name, wrote, err, runs, cached)
			}
		}
	}
}

// Each config holds its own kinds: Load(a), whose config names a plugin kind mykind, then Load(b), whose config
// names none, leaves a's config answering mykind, b's not, and each its own OpenCode (bound to its own state dir):
// a load changes nothing outside the config it returns.
func TestKindsPerConfig(t *testing.T) {
	a, b := newProject(t), newProject(t)
	a.Config(set("agents.kinds.mykind", map[string]any{"plugin": "kinds/obor-kind-mykind"}))
	b.Config()
	ca, err := config.Load(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ca.Kind("mykind"); !ok || !ca.Kinds().IsPlugin("mykind") {
		t.Fatal("precondition: a's config has the plugin kind mykind")
	}
	ocA, _ := ca.Kind("opencode")
	cb, err := config.Load(b.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ca.Kind("mykind"); !ok {
		t.Error("a's config lost mykind to the load of b")
	}
	if !slices.Contains(ca.Kinds().Names(), "mykind") {
		t.Errorf("a's kinds %q lack mykind after the load of b", ca.Kinds().Names())
	}
	if _, ok := cb.Kind("mykind"); ok || slices.Contains(cb.Kinds().Names(), "mykind") {
		t.Error("b's config, naming no mykind, has it")
	}
	ocA2, _ := ca.Kind("opencode")
	ocB, _ := cb.Kind("opencode")
	if ocA == nil || ocB == nil || ocA2 != ocA || ocB == ocA {
		t.Errorf("OpenCode: a's %p (after b's load %p), b's %p; want a's kept and b's its own", ocA, ocA2, ocB)
	}
}

// the model-id WARN names the model's own kind, for a plugin kind other than OpenCode too (its manifest's model_arg),
// and the "kind required" FAIL lists the config's kinds, the plugin kind among them.
func TestValidateModelArgWarnNamesItsKind(t *testing.T) {
	p := newProject(t)
	cmd := filepath.Join(p.Root, "kinds", "obor-kind-mine")
	if err := os.MkdirAll(filepath.Dir(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"version\":1,\"name\":\"mine\",\"status\":\"hooks\"," +
		"\"caps\":{\"model_arg\":true},\"stop\":{\"quit\":\"/q\"}}'\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := []edit{set("agents.kinds.mine", map[string]any{"plugin": "kinds/obor-kind-mine"}),
		set("agents.models.m1", map[string]any{"kind": "mine", "id": ""})}
	if _, err := plugin.Cache("mine", cmd, filepath.Join(p.Root, ".obor", "state")); err != nil { // obor setup agents
		t.Fatal(err)
	}
	if got := validate(t, p, mine...); len(got) != 0 {
		t.Fatalf("precondition: an unused model of a model_arg plugin kind validates %q, want OK", got)
	}
	got := validate(t, p, append(mine, set("agents.difficulty.simple", []any{"m1"}))...)
	want := []string{`WARN agents.models.m1: mine model without a model id (the kind's own default may not work, and an ` +
		`effort needs one): set its "id"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a used model of kind mine without an id: %q, want %q", got, want)
	}
	got = validate(t, p, append(mine, set("agents.models.m2", map[string]any{"id": "x"}))...)
	if want := "FAIL .agents.models.m2.kind: required (agy, claude, codex, mine, opencode)"; !slices.Contains(got, want) {
		t.Errorf("a model without a kind: %q, want the line %q (the config's kinds, mine among them)", got, want)
	}
}

// TestPluginKindEffortsFromDescribe: validate checks a role's and a difficulty level's effort on a
// plugin kind against the manifest its describe just answered: with no cached manifest (before obor setup agents), and
// with a stale one (the command since added the effort), the effort is taken; one the manifest lacks still fails.
// obor spec set, dispatch and drive keep reading the cache (CheckModelRef: the stale cache refuses the effort).
func TestPluginKindEffortsFromDescribe(t *testing.T) {
	p := newProject(t)
	cmd := filepath.Join(p.Root, "kinds", "obor-kind-mine")
	if err := os.MkdirAll(filepath.Dir(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	answer := func(efforts string) {
		t.Helper()
		script := "#!/bin/sh\ncat >/dev/null\necho '{\"version\":1,\"name\":\"mine\",\"status\":\"hooks\"," +
			"\"efforts\":" + efforts + ",\"stop\":{\"quit\":\"/q\"}}'\n"
		if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mine := []edit{set("agents.kinds.mine", map[string]any{"plugin": "kinds/obor-kind-mine"}),
		set("agents.models.m1", map[string]any{"kind": "mine", "id": ""}), set("agents.roles.reviewer", "m1:high"),
		set("agents.difficulty.hard", []any{"m1:high"})}
	answer(`["low","high"]`)
	state := filepath.Join(p.Root, ".obor", "state")
	if _, err := os.Stat(plugin.CacheFile(state, "mine")); err == nil {
		t.Fatal("precondition: no cached manifest")
	}
	if got := validate(t, p, mine...); len(got) != 0 {
		t.Errorf("no cached manifest: %q, want the effort taken (the describe declares high)", got)
	}
	if c, err := config.Load(p.Root); err != nil {
		t.Fatal(err)
	} else if err := c.Kinds().CheckEffort("mine", "high"); err == nil {
		t.Error("precondition: with no cache the config's kind declares no effort")
	}

	answer(`["low"]`) // the cache setup wrote before the command added high
	if _, err := plugin.Cache("mine", cmd, state); err != nil {
		t.Fatal(err)
	}
	answer(`["low","high"]`)
	if got := validate(t, p, mine...); len(got) != 0 {
		t.Errorf("a stale cached manifest: %q, want the effort taken", got)
	}
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckModelRef("m1:high"); err == nil || !strings.Contains(err.Error(), `effort "high" is not one of mine's (low)`) {
		t.Errorf("CheckModelRef(m1:high) on the stale cache = %v, want the cache's refusal (obor spec set's path)", err)
	}

	got := validate(t, p, append(mine, set("agents.roles.reviewer", "m1:max"))...)
	if want := `FAIL .agents.roles.reviewer: m1:max: effort "max" is not one of mine's (low, high)`; len(got) != 1 || got[0] != want {
		t.Errorf("an effort the describe lacks: %q, want %q", got, want)
	}
}

// checks.output.failed_test and failed_package — the failed-test line and the failed-package line — go
// together, and each compiles and has its named group (test, package); neither is fine (a project whose checks print
// no such lines).
func TestValidateFlakeRegex(t *testing.T) {
	for _, c := range []struct {
		name   string
		output string
		fails  []string // prefixes: a compile error carries regexp's own text
	}{
		{name: "the Go pair", output: `{"failed_test": "^--- FAIL: (?P<test>\\S+)", "failed_package": "^FAIL\\s+(?P<package>[^\\s./][^\\s]*[./][^\\s]*)"}`},
		{name: "no pair", output: `{"failure": "^--- FAIL"}`},
		{name: "one line only", output: `{"failed_test": "^--- FAIL: (?P<test>\\S+)"}`,
			fails: []string{"FAIL .checks.output.failed_package: required beside the other one: the failed-test line and the failed-package line go together"}},
		{name: "a bad regexp", output: `{"failed_test": "^--- FAIL: (?P<test>\\S+)", "failed_package": "["}`,
			fails: []string{"FAIL .checks.output.failed_package: is no regexp: error parsing regexp"}},
		{name: "no named group", output: `{"failed_test": "^--- FAIL: \\S+", "failed_package": "^FAIL\\s+(?P<package>\\S+)"}`,
			fails: []string{"FAIL .checks.output.failed_test: has no (?P<test>…) named group: the name to read from the line"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := testkit.New(t)
			p.Write(".obor/config.json", `{"version":2,"project":{"agent_prefix":"pj"},"checks":{"output":`+c.output+`}}`)
			cfg, err := config.Load(p.Root)
			if err != nil {
				t.Fatal(err)
			}
			_, all := cfg.Validate(config.ValidateOptions{Trunk: trunks("main")})
			var fails []string
			for _, f := range all {
				if strings.HasPrefix(f, "FAIL .checks.output.failed_") {
					fails = append(fails, f)
				}
			}
			if len(fails) != len(c.fails) {
				t.Fatalf("validate = %q, want %q", fails, c.fails)
			}
			for i, want := range c.fails {
				if !strings.HasPrefix(fails[i], want) {
					t.Errorf("validate[%d] = %q, want %q", i, fails[i], want)
				}
			}
		})
	}
}

// merge.retest.join is the separator merge.retest's {tests} joins the failing tests with, default "|".
func TestRetestJoin(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ cfg, want string }{
		{`{"version":2,"project":{"agent_prefix":"pj"}}`, "|"},
		{`{"version":2,"project":{"agent_prefix":"pj"},"merge":{"retest":{"join":","}}}`, ","},
		{`{"version":2,"project":{"agent_prefix":"pj"},"merge":{"retest":{"cmd":"make retest","join":","}}}`, ","},
		{`{"version":2,"project":{"agent_prefix":"pj"},"merge":{"retest":{"cmd":"x","join":" "}}}`, " "},
	} {
		p := testkit.New(t)
		p.Write(".obor/config.json", c.cfg)
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.RetestJoin(); got != c.want {
			t.Errorf("RetestJoin(%s) = %q, want %q", c.cfg, got, c.want)
		}
	}
	if got := (*config.Config)(nil).RetestJoin(); got != "|" {
		t.Errorf("nil config: RetestJoin() = %q, want |", got)
	}
}

// obor config validate reads the project's override files (.obor/<kind>/): a file that does not parse fails, naming
// it (a compiled kind's and the OpenCode plugin kind's); a file of another name there, a key of claude_override.json
// other than mcpServers and a hook Codex's hooks override adds to the user's are warnings; clean files say nothing.
func TestValidateOverrideFiles(t *testing.T) {
	p := newProject(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "hooks.json"), []byte(`{"hooks": {"Stop": [{"hooks": [{"command": "a.sh"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() []string {
		t.Helper()
		p.Config(set("agents.roles.integrator", "opus"))
		c, unknown, err := config.LoadForValidate(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		warns, fails := c.Validate(config.ValidateOptions{Overlap: spec.Overlap, Trunk: trunks("main"),
			Env: func(k string) string { return map[string]string{"HOME": home}[k] }})
		return append(append(warns, unknown...), fails...)
	}
	p.Write(".obor/claude/settings_override.json", `{"hooks": {"SessionStart": []}}`)
	p.Write(".obor/claude/claude_override.json", `{"mcpServers": {"blender": null}}`)
	p.Write(".obor/codex/config_override.toml", "model_reasoning_effort = \"low\"\n")
	p.Write(".obor/codex/hooks_override.json", `{"hooks": {"Stop": []}}`)
	p.Write(".obor/opencode/opencode_override.json", `{"plugins": ["-herdr.opencode"]}`)
	if got := run(); len(got) != 0 {
		t.Fatalf("precondition: clean override files validate clean: %q", got)
	}

	p.Write(".obor/claude/claude_override.json", `{"mcpServers": {"blender": null}, "numStartups": 1}`)
	p.Write(".obor/claude/settings.json", `{}`)
	p.Write(".obor/codex/config_override.toml", "model_reasoning_effort = \n")
	p.Write(".obor/codex/hooks_override.json", `{"hooks": {"Stop": [{"hooks": [{"command": "a.sh"}, {"command": "new.sh"}]}]}}`)
	p.Write(".obor/opencode/opencode_override.json", `{"plugins": `)
	got := run()
	want := []string{
		"WARN .obor/claude/settings.json: no override file of claude (it takes settings_override.json, claude_override.json): nothing reads it",
		"WARN .obor/claude/claude_override.json: numStartups: only mcpServers applies",
		"WARN .obor/codex/hooks_override.json: adds a Stop hook: Codex runs none from an override; add it to the project's own Codex hooks instead",
		"FAIL .obor/codex/config_override.toml: does not parse: ",
		"FAIL .obor/opencode/opencode_override.json: does not parse: ",
	}
	if len(got) != len(want) {
		t.Fatalf("validate =\n %q\nwant\n %q", got, want)
	}
	for i, w := range want {
		if got[i] != w && !(strings.HasSuffix(w, "does not parse: ") && strings.HasPrefix(got[i], w) && len(got[i]) > len(w)) {
			t.Errorf("line %d: %q, want %q", i, got[i], w)
		}
	}
}

// The OK line names the lead model's kind beside the lead's role: serve resolves the kind with the lead's role, so a
// change to it needs a restart. The literal text, because a phrase a reader is told to look for is the promise.
func TestReloadLineNamesLeadModelKind(t *testing.T) {
	line := config.ReloadLine()
	if !strings.Contains(line, "agents.roles.lead") {
		t.Fatalf("the OK line does not name agents.roles.lead: %q", line)
	}
	if !strings.Contains(line, "agents.models.<the lead's model>.kind") {
		t.Errorf("the OK line does not name the lead model's kind among the keys a restart is for: %q", line)
	}
}

// On an hc project a codex model whose start lacks the kind's flag (Codex --no-daemon) is refused, one line per model;
// the same config on herdr passes. Not parallel: it takes the flags out of config.KindBaseArgs for its run.
func TestValidateHCRequiresNoSharedServer(t *testing.T) {
	saved := map[string][]string{}
	for kind, flag := range config.HCRequiredArgs {
		saved[kind] = config.KindBaseArgs[kind]
		var without []string
		for _, a := range saved[kind] {
			if a != flag {
				without = append(without, a)
			}
		}
		config.KindBaseArgs[kind] = without
	}
	t.Cleanup(func() {
		for kind, args := range saved {
			config.KindBaseArgs[kind] = args
		}
	})
	p := newProject(t)
	if got := validate(t, p, set("project.terminal", "herdr")); got != nil {
		t.Fatalf("precondition: the config without the flags on herdr validates %q, want OK", got)
	}
	got := validate(t, p, set("project.terminal", "hc"))
	c, _, err := config.LoadForValidate(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for _, name := range c.ModelNames() {
		m, _ := c.Model(name)
		if flag := config.HCRequiredArgs[m.Kind]; flag != "" {
			want = append(want, "FAIL .agents.models."+name+": a "+m.Kind+" start on an hc project (project.terminal) needs "+
				flag+": its shared background server runs the agent's commands outside hc's terminal, with another env and cwd")
		}
	}
	if len(want) < 2 {
		t.Fatalf("precondition: the fixture's models hold %d models with an hc-required flag, want two at least", len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("validate on hc = %q\nwant %q", got, want)
	}
}

// A lanes.links entry the main checkout tracks is noted (the file comes from git, the link is no longer needed); one
// it does not track, and a run that cannot ask git, say nothing.
func TestValidateTrackedLinks(t *testing.T) {
	p := newProject(t)
	validate(t, p, set("lanes.links", []any{".claude/settings.json", ".env"}))
	c, _, err := config.LoadForValidate(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LaneLinks(); !reflect.DeepEqual(got, []string{".claude/settings.json", ".env"}) {
		t.Fatalf("lanes.links = %q: the fixture does not hold the entries", got)
	}
	warnsOf := func(tracked func(string) bool) []string {
		var got []string
		warns, _ := c.Validate(config.ValidateOptions{Overlap: spec.Overlap, Trunk: trunks("main"), Tracked: tracked})
		for _, w := range warns {
			if strings.Contains(w, "lanes.links") {
				got = append(got, w)
			}
		}
		return got
	}
	want := []string{"WARN lanes.links .claude/settings.json is tracked: the link is no longer needed"}
	if got := warnsOf(func(path string) bool { return path == ".claude/settings.json" }); !reflect.DeepEqual(got, want) {
		t.Errorf("tracked .claude/settings.json: warns = %q, want %q", got, want)
	}
	if got := warnsOf(func(string) bool { return false }); len(got) != 0 {
		t.Errorf("no entry tracked: warns = %q, want none", got)
	}
	if got := warnsOf(nil); len(got) != 0 {
		t.Errorf("no git query: warns = %q, want none", got)
	}
}
