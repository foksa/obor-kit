package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// editDoc is a config.json in shape 2 as obor setup writes it: indented by two, a final newline.
const editDoc = `{
  "version": 2,
  "project": {
    "name": "pj",
    "agent_prefix": "pj"
  },
  "lanes": {
    "count": 2
  },
  "agents": {
    "roles": {
      "lead": "opus:high",
      "integrator": "",
      "deep_dive": "sonnet"
    },
    "difficulty": {
      "hard": [
        "opus:high"
      ]
    },
    "kinds": {
      "codex": {
        "args": [
          "-c",
          "sandbox_workspace_write.network_access=true"
        ]
      },
      "opencode": {
        "plugin": "obor-kind-opencode"
      }
    },
    "models": {
      "kimi": {
        "kind": "opencode",
        "id": "opencode-go/kimi"
      }
    }
  }
}
`

// editProject is a project whose config.json is editDoc, loaded.
func editProject(t *testing.T) *Config {
	t.Helper()
	return load(t, editDoc, "")
}

// T-753, T-765: each editable key, set: the file's other members keep their bytes and layout, the result loads with
// the new value, and the change says the old and the new value.
func TestEditSetsEachField(t *testing.T) {
	for _, c := range []struct {
		key, val, old, new string
		check              func(*Config) bool
	}{
		{"agents.difficulty.hard", `["sonnet","opus:high"]`, `["opus:high"]`, `["sonnet","opus:high"]`,
			func(n *Config) bool { return slices.Equal(n.DifficultyList("hard"), []string{"sonnet", "opus:high"}) }},
		{"agents.difficulty.simple", `["luna:xhigh","kimi"]`, `["deepseek","sonnet","haiku"]`, `["luna:xhigh","kimi"]`,
			func(n *Config) bool { return slices.Equal(n.DifficultyList("simple"), []string{"luna:xhigh", "kimi"}) }},
		{"lanes.count", `4`, `2`, `4`, func(n *Config) bool { return len(n.Lanes()) == 4 && n.HasLane("L-04") }},
		{"lanes.count", `1`, `2`, `1`, func(n *Config) bool { return len(n.Lanes()) == 1 && !n.HasLane("L-02") }},
		{"agents.kinds.codex.args", `["--x"]`, `["-c","sandbox_workspace_write.network_access=true"]`, `["--x"]`, func(n *Config) bool {
			m, _ := n.Model("terra")
			return slices.Equal(m.Args, []string{"-s", "workspace-write", "-a", "never", "--no-daemon", "-m", "gpt-6.1-terra", "--x"})
		}},
		{"agents.kinds.claude.args", `["--strict-mcp-config"]`, "", `["--strict-mcp-config"]`, func(n *Config) bool {
			m, _ := n.Model("opus")
			return slices.Equal(m.Args, []string{"--permission-mode", "auto", "--model", "opus", "--strict-mcp-config"})
		}},
		{"agents.kinds.codex.args", `[]`, `["-c","sandbox_workspace_write.network_access=true"]`, "", func(n *Config) bool {
			m, _ := n.Model("terra")
			return len(m.Args) == len(KindBaseArgs["codex"])+2
		}},
		{"agents.roles.integrator", `"sonnet"`, "", `"sonnet"`, func(n *Config) bool { rc, _ := n.Role("integrator"); return rc.Type == "sonnet" }},
		{"agents.roles.lead", `"luna:low"`, `"opus:high"`, `"luna:low"`, func(n *Config) bool {
			rc, _ := n.Role("lead")
			return rc.Type == "luna" && rc.Effort == "low"
		}},
		{"nightly.at", `"03:00"`, "", `"03:00"`, func(n *Config) bool { return n.Data.Nightly.At == "03:00" }},
		{"nightly.at", `null`, "", "", func(n *Config) bool { return n.Data.Nightly.At == "" }},
	} {
		cfg := editProject(t)
		ch, err := cfg.Edit([]byte(editDoc), c.key, json.RawMessage(c.val))
		if err != nil {
			t.Errorf("%s = %s: %v", c.key, c.val, err)
			continue
		}
		if ch.Old != c.old || ch.New != c.new {
			t.Errorf("%s = %s: old %q new %q, want %q %q", c.key, c.val, ch.Old, ch.New, c.old, c.new)
		}
		n, err := LoadWith(cfg.Root, ch.Doc)
		if err != nil {
			t.Errorf("%s = %s: the result does not load: %v\n%s", c.key, c.val, err, ch.Doc)
			continue
		}
		if !c.check(n) {
			t.Errorf("%s = %s: not set in the result:\n%s", c.key, c.val, ch.Doc)
		}
		if _, fails := n.Validate(ValidateOptions{}); len(fails) > 0 {
			t.Errorf("%s = %s: the result fails validate: %q", c.key, c.val, fails)
		}
		// the layout is the file's: indented by two, a final newline; only the edited member's lines differ
		if !strings.HasSuffix(string(ch.Doc), "}\n") || !strings.Contains(string(ch.Doc), "\n    \"models\": {\n      \"kimi\": {\n") {
			t.Errorf("%s = %s: the layout is not the file's:\n%s", c.key, c.val, ch.Doc)
		}
		if lines := diffLines(editDoc, string(ch.Doc)); lines > 12 {
			t.Errorf("%s = %s: %d lines differ, want only the edited member's:\n%s", c.key, c.val, lines, ch.Doc)
		}
	}
	ch, err := editProject(t).Edit([]byte(editDoc), "lanes.count", json.RawMessage(`0`))
	if err != nil || !slices.Equal(ch.Dropped, []string{"L-01", "L-02"}) {
		t.Errorf("lanes.count = 0: dropped %q, %v; want L-01, L-02 (serve refuses while one has a task)", ch.Dropped, err)
	}
}

// T-781: nightly.at is the nightly's switch: a time sets it (on), null deletes it (off), the change naming the
// value before; off when off is no change of the file.
func TestEditNightlyAtOnOff(t *testing.T) {
	cfg := editProject(t)
	on, err := cfg.Edit([]byte(editDoc), "nightly.at", json.RawMessage(`"02:30"`))
	if err != nil || !strings.Contains(string(on.Doc), "\"at\": \"02:30\"") {
		t.Fatalf("on: %v\n%s", err, on.Doc)
	}
	onCfg, err := LoadWith(cfg.Root, on.Doc)
	if err != nil || onCfg.Data.Nightly.At != "02:30" {
		t.Fatalf("precondition: on loads with 02:30: %v", err)
	}
	off, err := onCfg.Edit(on.Doc, "nightly.at", json.RawMessage(`null`))
	if err != nil || off.Old != `"02:30"` || off.New != "" || strings.Contains(string(off.Doc), "nightly") {
		t.Fatalf("off: old %q new %q, %v; want \"02:30\" → unset, the key gone:\n%s", off.Old, off.New, err, off.Doc)
	}
	offCfg, err := LoadWith(cfg.Root, off.Doc)
	if err != nil || offCfg.Data.Nightly.At != "" {
		t.Fatalf("off does not load unset: %v\n%s", err, off.Doc)
	}
	again, err := offCfg.Edit(off.Doc, "nightly.at", json.RawMessage(`null`))
	if err != nil || string(again.Doc) != string(off.Doc) {
		t.Errorf("off when off: %v; want the file unchanged:\n%s", err, again.Doc)
	}
}

// diffLines is how many lines of a and b differ, by a line-set difference both ways (the other members' lines are
// shared).
func diffLines(a, b string) int {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[l]++
		}
		return m
	}
	ma, mb := count(a), count(b)
	n := 0
	for l, k := range ma {
		n += max(0, k-mb[l])
	}
	for l, k := range mb {
		n += max(0, k-ma[l])
	}
	return n
}

// T-753: a value the key does not take, a key that is not editable and a name the config does not have are refused,
// naming why. T-1126: the keys are shape 2's names; the `roles.<role>.type` alias is not editable.
func TestEditRefuses(t *testing.T) {
	cfg := editProject(t)
	for _, c := range []struct{ key, val, want string }{
		{"agents.difficulty.extreme", `["opus"]`, "is not a level"},
		{"agents.difficulty.hard", `["gpt"]`, `"gpt" is not a model (models: opus, sonnet, haiku, fable, deepseek, terra, sol, luna, kimi)`},
		{"agents.difficulty.hard", `["opus:turbo"]`, `effort "turbo" is not one of claude's`},
		{"agents.difficulty.hard", `"opus"`, "takes a list of model refs"},
		{"agents.difficulty.hard", `null`, "takes a list of model refs"},
		{"lanes.count", `-1`, "takes a whole number"},
		{"lanes.count", `"two"`, "takes a whole number"},
		{"lanes.L-01", `"opus"`, "is not an editable key"},
		{"agents.kinds.cursor.args", `["x"]`, `"cursor" is not a kind`},
		{"agents.kinds.claude.args", `"x"`, "takes a list of strings"},
		{"agents.kinds.claude.args", `["--"]`, "`--` is not allowed"},
		{"agents.kinds.claude.job_note", `"x"`, "is not an editable key"},
		{"agents.models.kimi.id", `"x"`, "is not an editable key"},
		{"lane_types.opus.max", `2`, "is not an editable key"},
		{"agents.roles.worker", `"opus"`, `"worker" is not a configured role`},
		{"agents.roles.triage", `"opus"`, `"triage" is not a configured role`},
		{"agents.roles.nobody", `"opus"`, `"nobody" is not a configured role`},
		{"agents.roles.lead", `""`, "takes a model ref"},
		{"agents.roles.lead", `null`, "takes a model ref"},
		{"agents.roles.integrator", `""`, "takes a model ref"},
		{"agents.roles.reviewer", `null`, "takes a model ref"},
		{"agents.roles.deep_dive", `""`, "takes a model ref"},
		{"agents.roles.lead.agent", `"x"`, "is not an editable key"},
		{"agents.roles.lead", `"gpt:high"`, `"gpt" is not a model`},
		{"merge.scratch_dir", `"x"`, "is not an editable key"},
		{"nightly.at", `"25:00"`, "nightly.at takes a local time HH:MM (00:00 to 23:59), or null (off)"},
		{"nightly.at", `"3:00"`, "nightly.at takes a local time HH:MM"},
		{"nightly.at", `"2am"`, "nightly.at takes a local time HH:MM"},
		{"nightly.at", `""`, "nightly.at takes a local time HH:MM"},
		{"nightly.at", `300`, "nightly.at takes a local time HH:MM"},
		{"nightly.at", ``, "nightly.at takes a local time HH:MM"},
		// the Setup page's old alias is no key now (T-1126)
		{"difficulty.hard", `["opus"]`, "is not an editable key"},
		{"roles.lead", `"opus"`, "is not an editable key"},
		{"roles.deep_dive.type", `"kimi:high"`, "is not an editable key"},
		{"lanes", `2`, "is not an editable key"},
		{"kinds.claude.args", `["--x"]`, "is not an editable key"},
		{"merge.nightly_at", `"03:00"`, "is not an editable key"},
	} {
		ch, err := cfg.Edit([]byte(editDoc), c.key, json.RawMessage(c.val))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %s: %q, %v; want an error with %q", c.key, c.val, ch.Doc, err, c.want)
		}
	}
}

// T-754: a key .obor/local.json sets is refused before anything is written: the committed value would not count. The
// refusal names local.json's member as the config has it.
func TestEditRefusesLocalOverride(t *testing.T) {
	for _, c := range []struct{ name, local, key, val, at string }{
		{"a kind's args", `{"agents":{"kinds":{"codex":{"args":["-c","x=1"]}}}}`, "agents.kinds.codex.args", `["y"]`, "agents.kinds.codex.args"},
		{"a kind local.json replaces", `{"agents":{"kinds":{"codex":null}}}`, "agents.kinds.codex.args", `["y"]`, "agents.kinds.codex"},
		{"a level", `{"agents":{"difficulty":{"hard":["sonnet"]}}}`, "agents.difficulty.hard", `["opus:high"]`, "agents.difficulty.hard"},
		{"a role", `{"agents":{"roles":{"lead":"sonnet"}}}`, "agents.roles.lead", `"opus:high"`, "agents.roles.lead"},
		{"the lanes", `{"lanes":{"count":3}}`, "lanes.count", `4`, "lanes.count"},
		{"a lane list", `{"lanes":{"list":[{"name":"L-01"}]}}`, "lanes.count", `4`, "lanes.list"},
	} {
		cfg, doc := localProject(t, c.local)
		ch, err := cfg.Edit(doc, c.key, json.RawMessage(c.val))
		if err == nil || !strings.Contains(err.Error(), "set in .obor/local.json at "+c.at+": edit it there") {
			t.Errorf("%s: %q, %v; want local.json's refusal at %s", c.name, ch.Doc, err, c.at)
		}
		if ch.Doc != nil || ch.Old != "" || ch.New != "" {
			t.Errorf("%s: %+v, want no change", c.name, ch)
		}
		if after, err := os.ReadFile(filepath.Join(cfg.Root, ".obor", "config.json")); err != nil || !bytes.Equal(after, doc) {
			t.Errorf("%s: the file changed (%v)", c.name, err)
		}
	}
}

// T-754: a member local.json leaves alone (another level, another kind) does not keep an edit from counting, and
// local.json's own values stay out of the committed file.
func TestEditIgnoresLocalElsewhere(t *testing.T) {
	for _, c := range []struct {
		name, local, key, val, leak string
		check                       func(doc []byte, n *Config) bool
	}{
		{"another level", `{"agents":{"difficulty":{"simple":["sonnet"]}}}`, "agents.difficulty.hard", `["opus"]`, "simple",
			func(_ []byte, n *Config) bool { return slices.Equal(n.DifficultyList("hard"), []string{"opus"}) }},
		{"another kind", `{"agents":{"kinds":{"claude":{"args":["--strict-mcp-config"]}}}}`, "agents.kinds.codex.args", `["--y"]`, "strict",
			func(_ []byte, n *Config) bool {
				return slices.Equal(n.Data.Agents.Kinds.M["codex"].Args, []string{"--y"})
			}},
	} {
		cfg, doc := localProject(t, c.local)
		ch, err := cfg.Edit(doc, c.key, json.RawMessage(c.val))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if strings.Contains(string(ch.Doc), c.leak) {
			t.Errorf("%s: local.json's %s landed in the committed file:\n%s", c.name, c.leak, ch.Doc)
		}
		n, err := LoadWith(cfg.Root, ch.Doc)
		if err != nil {
			t.Errorf("%s: the result does not load: %v", c.name, err)
			continue
		}
		if !c.check(ch.Doc, n) {
			t.Errorf("%s: the edit does not count:\n%s", c.name, ch.Doc)
		}
	}
}

// localProject is a project whose config.json is editDoc and whose local.json is local, and that file's bytes.
func localProject(t *testing.T, local string) (*Config, []byte) {
	t.Helper()
	cfg := load(t, editDoc, local)
	doc, err := os.ReadFile(filepath.Join(cfg.Root, ".obor", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, doc
}

// T-753: Relayout keeps the file's layout: a one-line file stays one line, a tab-indented one tabs, no final newline
// none.
func TestRelayout(t *testing.T) {
	for _, c := range []struct{ orig, want string }{
		{`{"a":1,"b":[1,2]}`, `{"a":1,"b":[1,3]}`},
		{"{\"a\": 1, \"b\": [1, 2]}\n", "{\"a\":1,\"b\":[1,3]}\n"},
		{"{\n\t\"a\": 1,\n\t\"b\": [\n\t\t1\n\t]\n}\n", "{\n\t\"a\": 1,\n\t\"b\": [\n\t\t1,\n\t\t3\n\t]\n}\n"},
		{"{\n    \"a\": 1,\n    \"b\": []\n}", "{\n    \"a\": 1,\n    \"b\": [\n        1,\n        3\n    ]\n}"},
	} {
		got, err := Relayout([]byte(c.orig), []byte(`{"a":1,"b":[1,3]}`))
		if err != nil || string(got) != c.want {
			t.Errorf("Relayout(%q): %q, %v; want %q", c.orig, got, err, c.want)
		}
	}
}

// T-753: NewFails is the fails a change adds: one the config already had is not the change's.
func TestNewFails(t *testing.T) {
	got := NewFails([]string{"FAIL .a: x"}, []string{"FAIL .a: x", "FAIL .b: y"})
	if !slices.Equal(got, []string{"FAIL .b: y"}) {
		t.Errorf("NewFails = %q", got)
	}
}
