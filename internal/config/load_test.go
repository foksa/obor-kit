package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestOverlayMatchesJq: each case's effective value is what the Bash loader's `jq -s '.[0] * .[1]'` gives (a null
// left by the merge is unset in the effective config: a derived default, or absent).
func TestOverlayMatchesJq(t *testing.T) {
	cfg := `{"prefix":"pj","merge":{"scratch_dir":"../custom-scratch","trailer":"T"},
		"kinds":{"claude":{"args":["a","b"],"prompt_extra":"P"}},
		"commands":{"check":{"cmd":"x","heavy":true,"timeout":5}},"slots":{"max":4,"dir":"/s"}}`
	local := `{"prefix":"qq","merge":{"scratch_dir":null},
		"kinds":{"claude":{"args":["c"],"prompt_extra":null}},
		"commands":{"check":{"heavy":null}},"slots":{"max":null}}`
	c := load(t, cfg, local)
	for _, tc := range []struct{ name, path, want string }{
		{"null scratch_dir resets to the derived default", ".merge.scratch_dir", `"../proj-wt/merge"`},
		{"a sibling of a null key is kept", ".merge.trailer", `"T"`},
		{"null heavy clears it", ".commands.check", `{"cmd":"x","timeout":5}`},
		{"null slots.max clears it", ".slots", `{"dir":"/s"}`},
		{"null prompt_extra clears it", ".kinds.claude.prompt_extra", ``},
		{"a list is replaced", ".kinds.claude.args", `["c"]`},
		{"flat overlay", ".prefix", `"qq"`},
	} {
		got, ok := c.Get(tc.path)
		if tc.want == "" {
			if ok {
				t.Errorf("%s: %s = %s, want absent", tc.name, tc.path, got)
			}
			continue
		}
		var g, w any
		if !ok || json.Unmarshal(got, &g) != nil || json.Unmarshal([]byte(tc.want), &w) != nil || !reflect.DeepEqual(g, w) {
			t.Errorf("%s: %s = %s (exists %v), want %s", tc.name, tc.path, got, ok, tc.want)
		}
	}
	if cmd, ok := c.Command("check"); !ok || cmd.Heavy || cmd.Timeout != 5 {
		t.Errorf("typed command after a null heavy: %+v", cmd)
	}
	if m, _ := c.Model("opus"); m.PromptExtra != "" || !reflect.DeepEqual(m.Args, []string{"--permission-mode", "auto", "--model", "opus", "c"}) {
		t.Errorf("typed model after a null prompt_extra: %+v", m)
	}
	if c.Data.Slots == nil || c.Data.Slots.Max != nil || c.Data.Slots.Dir != "/s" {
		t.Errorf("typed slots after a null max: %+v", c.Data.Slots)
	}
}

func TestMerge(t *testing.T) {
	for _, tc := range []struct{ a, b, want string }{
		{`{"a":1,"b":{"c":1,"d":2}}`, `{"b":{"d":3,"e":4},"f":5}`, `{"a":1,"b":{"c":1,"d":3,"e":4},"f":5}`},
		{`{"a":{"b":1}}`, `{"a":null}`, `{"a":null}`},
		{`{"a":[1,2]}`, `{"a":[3]}`, `{"a":[3]}`},
		{`{"a":"s"}`, `{"a":{"b":1}}`, `{"a":{"b":1}}`},
		{`{"a":1,"a":2}`, `{}`, `{"a":2}`},
	} {
		if got := string(merge([]byte(tc.a), []byte(tc.b))); got != tc.want {
			t.Errorf("%s * %s = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}

// DD-v0.13 A1: LoadHook ignores unknown keys (both files) and returns them, writing nothing on Warn; every other error
// is Load's; Fallback is the defaults with the derived lanes_dir.
func TestLoadHook(t *testing.T) {
	var warn strings.Builder
	defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
	Warn = &warn
	for _, tc := range []struct {
		name, cfg, local string
		unknown          []string
		err              string
	}{
		{name: "clean", cfg: `{"prefix":"pj"}`},
		{name: "unknown keys", cfg: `{"prefix":"pj","keep_warm":{"x":1}}`, local: `{"merge":{"future":1}}`,
			unknown: []string{"keep_warm", "merge.future"}},
		{name: "unknown key and a type error", cfg: `{"keep_warm":1,"merge":{"receipt_max_age":"soon"}}`, err: "receipt_max_age"},
		{name: "syntax error", cfg: `{"prefix":`, err: "not valid JSON"},
		{name: "syntax error in local.json", cfg: `{"keep_warm":1}`, local: `{`, err: "local.json is not valid JSON"},
		{name: "no config", err: "no "},
	} {
		c, unknown, err := LoadHook(writeProject(t, tc.cfg, tc.local))
		switch {
		case tc.err == "" && (err != nil || c == nil || c.Data.Prefix != "pj"):
			t.Errorf("%s: %+v, %v; want the config", tc.name, c, err)
		case tc.err != "" && (err == nil || c != nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: %v, %v; want error %q and no config", tc.name, c, err, tc.err)
		case !reflect.DeepEqual(unknown, tc.unknown):
			t.Errorf("%s: unknown = %q, want %q", tc.name, unknown, tc.unknown)
		}
	}
	if warn.Len() != 0 {
		t.Errorf("Warn got %q, want nothing (the hooks report unknown keys)", warn.String())
	}
	if c := Fallback("/x/proj"); c.LanesDir() != "/x/proj-wt" || c.Data.Branches.Main != "main" {
		t.Errorf("Fallback = %+v", c.Data)
	}
}
