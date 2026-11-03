package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// editDoc is a config.json as obor setup writes it: indented by two, a final newline.
const editDoc = `{
  "version": 1,
  "name": "pj",
  "prefix": "pj",
  "difficulty": {
    "hard": [
      "opus:high"
    ]
  },
  "lane_types": {
    "opus": {
      "kind": "claude",
      "args": [
        "--model",
        "opus",
        "--effort",
        "medium",
        "--permission-mode",
        "auto"
      ],
      "max": 4
    },
    "sonnet": {
      "kind": "claude",
      "args": [
        "--model",
        "sonnet"
      ]
    },
    "luna": {
      "kind": "codex",
      "args": [
        "-m",
        "gpt-6-luna",
        "-c",
        "model_reasoning_effort=high",
        "-s",
        "workspace-write"
      ]
    },
    "deepseek": {
      "kind": "opencode",
      "args": [
        "--auto",
        "--model",
        "opencode-go/deepseek"
      ]
    }
  },
  "lanes": {
    "L-01": "",
    "L-02": {
      "type": "opus",
      "agent": "pj-two"
    }
  },
  "roles": {
    "lead": {
      "type": "opus:high"
    },
    "integrator": {},
    "deep_dive": {
      "type": "sonnet"
    }
  }
}
`

// editProject is a project whose config.json is editDoc, loaded.
func editProject(t *testing.T) *Config {
	t.Helper()
	return load(t, editDoc, "")
}

// T-753: each editable key, set: the file's other members keep their bytes and layout, the result loads with the new
// value, and the change says the old and the new value.
func TestEditSetsEachField(t *testing.T) {
	for _, c := range []struct {
		key, val, old, new string
		check              func(*Config) bool
	}{
		{"difficulty.hard", `["sonnet","opus:high"]`, `["opus:high"]`, `["sonnet","opus:high"]`,
			func(n *Config) bool { return slices.Equal(n.DifficultyList("hard"), []string{"sonnet", "opus:high"}) }},
		{"difficulty.simple", `["luna:xhigh"]`, `["deepseek","sonnet","haiku"]`, `["luna:xhigh"]`,
			func(n *Config) bool { return slices.Equal(n.DifficultyList("simple"), []string{"luna:xhigh"}) }},
		{"lane_types.opus.model", `"fable"`, `"opus"`, `"fable"`, func(n *Config) bool {
			lt, _ := n.LaneType("opus")
			return lt.Model() == "fable" && slices.Equal(lt.Args, []string{"--model", "fable", "--effort", "medium", "--permission-mode", "auto"})
		}},
		{"lane_types.opus.effort", `"high"`, `"medium"`, `"high"`, func(n *Config) bool {
			lt, _ := n.LaneType("opus")
			return lt.Effort() == "high" && slices.Equal(lt.Args, []string{"--model", "opus", "--effort", "high", "--permission-mode", "auto"})
		}},
		{"lane_types.sonnet.effort", `"low"`, "", `"low"`, func(n *Config) bool {
			lt, _ := n.LaneType("sonnet")
			return lt.Effort() == "low" && slices.Equal(lt.Args, []string{"--model", "sonnet", "--effort", "low"})
		}},
		{"lane_types.luna.model", `"gpt-7"`, `"gpt-6-luna"`, `"gpt-7"`, func(n *Config) bool {
			lt, _ := n.LaneType("luna")
			return lt.Model() == "gpt-7" && slices.Equal(lt.Args, []string{"-m", "gpt-7", "-c", "model_reasoning_effort=high", "-s", "workspace-write"})
		}},
		{"lane_types.luna.effort", `"xhigh"`, `"high"`, `"xhigh"`, func(n *Config) bool {
			lt, _ := n.LaneType("luna")
			return lt.Effort() == "xhigh" && slices.Equal(lt.Args, []string{"-m", "gpt-6-luna", "-c", "model_reasoning_effort=xhigh", "-s", "workspace-write"})
		}},
		{"lane_types.deepseek.model", `"opencode-go/kimi"`, `"opencode-go/deepseek"`, `"opencode-go/kimi"`, func(n *Config) bool {
			lt, _ := n.LaneType("deepseek")
			return slices.Equal(lt.Args, []string{"--auto", "--model", "opencode-go/kimi"})
		}},
		{"lane_types.opus.max", `2`, `4`, `2`, func(n *Config) bool { lt, _ := n.LaneType("opus"); return lt.Max == 2 }},
		{"lane_types.opus.max", `0`, `4`, `0`, func(n *Config) bool { lt, _ := n.LaneType("opus"); return lt.Max == 0 }},
		{"lane_types.opus.max", `""`, `4`, `0`, func(n *Config) bool { lt, _ := n.LaneType("opus"); return lt.Max == 0 }},
		{"lanes.L-01", `"sonnet"`, `""`, `"sonnet"`, func(n *Config) bool { return n.Data.Lanes.M["L-01"].Type == "sonnet" }},
		{"lanes.L-02", `""`, `"opus"`, `""`, func(n *Config) bool {
			return n.Data.Lanes.M["L-02"] == LaneEntry{Type: "", Agent: "pj-two"} // an object lane keeps its agent
		}},
		{"roles.integrator.type", `"sonnet"`, "", `"sonnet"`, func(n *Config) bool { rc, _ := n.Role("integrator"); return rc.Type == "sonnet" }},
		{"roles.lead.type", `"luna:low"`, `"opus:high"`, `"luna:low"`, func(n *Config) bool {
			rc, _ := n.Role("lead")
			return rc.Type == "luna" && rc.Effort == "low"
		}},
		{"roles.lead.type", `""`, `"opus:high"`, "", func(n *Config) bool { rc, _ := n.Role("lead"); return rc.Type == "" && rc.Configured }},
		{"roles.deep_dive.type", `""`, `"sonnet"`, "", func(n *Config) bool { // an optional role stays configured
			rc, _ := n.Role("deep_dive")
			return rc.Type == "" && rc.Configured
		}},
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
		if !strings.HasSuffix(string(ch.Doc), "}\n") || !strings.Contains(string(ch.Doc), "\n  \"lane_types\": {\n    \"opus\": {\n") {
			t.Errorf("%s = %s: the layout is not the file's:\n%s", c.key, c.val, ch.Doc)
		}
		if lines := diffLines(editDoc, string(ch.Doc)); lines > 12 {
			t.Errorf("%s = %s: %d lines differ, want only the edited member's:\n%s", c.key, c.val, lines, ch.Doc)
		}
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
// naming why.
func TestEditRefuses(t *testing.T) {
	cfg := editProject(t)
	for _, c := range []struct{ key, val, want string }{
		{"difficulty.extreme", `["opus"]`, "is not a level"},
		{"difficulty.hard", `["gpt"]`, `"gpt" is not a lane type of this project`},
		{"difficulty.hard", `["opus:turbo"]`, `effort "turbo" is not one of claude's`},
		{"difficulty.hard", `"opus"`, "takes a list of type references"},
		{"difficulty.hard", `null`, "takes a list of type references"},
		{"lane_types.gpt.model", `"x"`, `"gpt" is not a lane type of this project`},
		{"lane_types.opus.model", `"--dangerous"`, "is no model"},
		{"lane_types.opus.model", `"a b"`, "is no model"},
		{"lane_types.opus.model", `3`, "takes a string"},
		{"lane_types.opus.effort", `"turbo"`, `effort "turbo" is not one of claude's`},
		{"lane_types.deepseek.effort", `"high"`, "opencode sets no effort in its args"},
		{"lane_types.opus.max", `-1`, "takes a whole number"},
		{"lane_types.opus.max", `"two"`, "takes a whole number"},
		{"lane_types.opus.args", `["x"]`, "is not an editable key"},
		{"lane_types.opus.kind", `"codex"`, "is not an editable key"},
		{"lanes.L-09", `"opus"`, `"L-09" is not a lane of this project`},
		{"lanes.L-01", `"gpt"`, `"gpt" is not a lane type of this project`},
		{"roles.worker.type", `"opus"`, `"worker" is not a configured role`},
		{"roles.triage.type", `"opus"`, `"triage" is not a configured role`},
		{"roles.nobody.type", `"opus"`, `"nobody" is not a configured role`},
		{"roles.lead.agent", `"x"`, "is not an editable key"},
		{"roles.lead.type", `"gpt:high"`, `"gpt" is not a lane type of this project`},
		{"merge.scratch_dir", `"x"`, "is not an editable key"},
		{"lanes", `{}`, "is not an editable key"},
	} {
		ch, err := cfg.Edit([]byte(editDoc), c.key, json.RawMessage(c.val))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %s: %q, %v; want an error with %q", c.key, c.val, ch.Doc, err, c.want)
		}
	}
}

// T-753: a lane or lane type local.json sets is not in config.json: the edit is refused, not written where it would
// not count.
func TestEditRefusesLocalOnly(t *testing.T) {
	cfg := load(t, `{"name":"pj","lane_types":{"opus":{"kind":"claude"}},"lanes":{"L-01":""}}`,
		`{"lane_types":{"haiku":{"kind":"claude","args":["--model","haiku"]}},"lanes":{"L-02":""}}`)
	doc, _ := os.ReadFile(filepath.Join(cfg.Root, ".obor", "config.json"))
	for _, key := range []string{"lane_types.haiku.model", "lanes.L-02"} {
		if _, err := cfg.Edit(doc, key, json.RawMessage(`"opus"`)); err == nil || !strings.Contains(err.Error(), "local.json sets it") {
			t.Errorf("%s: %v, want local.json's refusal", key, err)
		}
	}
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
