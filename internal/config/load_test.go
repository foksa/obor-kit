package config

import (
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestOverlayMatchesJq: each case's effective value is what the Bash loader's `jq -s '.[0] * .[1]'` gives (a null
// left by the merge is unset in the effective config: a derived default, or absent).
func TestOverlayMatchesJq(t *testing.T) {
	cfg := `{"prefix":"pj","merge":{"scratch_dir":"../custom-scratch","summary_regex":"T"},
		"kinds":{"claude":{"args":["a","b"],"prompt_extra":"P"}},
		"commands":{"check":{"cmd":"x","heavy":true,"timeout":5}},"slots":{"max":4,"dir":"/s"}}`
	local := `{"prefix":"qq","merge":{"scratch_dir":null},
		"kinds":{"claude":{"args":["c"],"prompt_extra":null}},
		"commands":{"check":{"heavy":null}},"slots":{"max":null}}`
	c := load(t, cfg, local)
	for _, tc := range []struct{ name, path, want string }{
		{"null scratch_dir resets to the derived default", ".merge.scratch_dir", `"../proj-wt/merge"`},
		{"a sibling of a null key is kept", ".merge.summary_regex", `"T"`},
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
		{name: "unknown key and a type error", cfg: `{"keep_warm":1,"merge":{"nightly_at":5}}`, err: "nightly_at"},
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

// T-847: a key of the config shape before v0.23 (`agents`, `lane_types`) is refused by every reader, not just the strict
// one: the ordinary unknown-field error naming its file, so serve, the hooks and the readers never run such a config on
// the defaults.
func TestRetiredShapeKeysRefusedByEveryReader(t *testing.T) {
	for _, key := range []string{"agents", "lane_types"} {
		for _, where := range []string{"config.json", "local.json"} {
			t.Run(key+"/"+where, func(t *testing.T) {
				cfg, doc, local := `{"prefix":"pj"}`, `{"prefix":"pj"}`, ""
				if where == "local.json" {
					local = `{"` + key + `":{"x":1}}`
				} else {
					cfg = `{"prefix":"pj","` + key + `":{"x":1}}`
					doc = cfg
				}
				root := writeProject(t, cfg, local)
				want := filepath.Join(root, ".obor", where) + `: unknown field "` + key + `"`
				refused := func(reader string, c *Config, err error) {
					t.Helper()
					if c != nil || err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("%s: %+v, %v; want no config and an error naming %q", reader, c, err, want)
					}
				}
				c, err := Load(root)
				refused("Load", c, err)
				c, fails, err := LoadForValidate(root)
				refused("LoadForValidate", c, err)
				if fails != nil {
					t.Errorf("LoadForValidate: FAIL lines %q, want the error alone", fails)
				}
				c, unknown, err := LoadHook(root)
				refused("LoadHook (fails closed)", c, err)
				if unknown != nil {
					t.Errorf("LoadHook: unknown = %q, want none", unknown)
				}
				c, _, err = LoadReader(root)
				refused("LoadReader", c, err)
				c, err = LoadWith(root, []byte(doc))
				refused("LoadWith", c, err)
			})
		}
	}
}

// T-847 keeps T-377's contract: a key this binary does not know that is no retired shape (a newer config) is still
// dropped by every reader, with its warning, and only LoadForValidate refuses it (T-1081).
func TestNewerUnknownKeyStillDropped(t *testing.T) {
	root := writeProject(t, `{"prefix":"pj","keep_warm":1}`, "")
	var out strings.Builder
	defer func(w io.Writer) { Warn = w }(Warn)
	Warn = &out
	c, err := Load(root)
	if err != nil || c == nil || c.Data.Prefix != "pj" {
		t.Fatalf("Load: %+v, %v; want the config", c, err)
	}
	if !strings.Contains(out.String(), `unknown key "keep_warm" ignored`) {
		t.Errorf("Load warn = %q, want the newer-config warning", out.String())
	}
	out.Reset()
	_, unknown, err := LoadHook(root)
	if err != nil || !reflect.DeepEqual(unknown, []string{"keep_warm"}) || out.Len() != 0 {
		t.Errorf("LoadHook: unknown = %q, %v, warn %q; want the key returned and nothing on Warn", unknown, err, out.String())
	}
	_, warn, err := LoadReader(root)
	if err != nil || !strings.Contains(warn, `unknown field "keep_warm" ignored`) {
		t.Errorf("LoadReader: warn = %q, %v", warn, err)
	}
	c, err = LoadWith(root, []byte(`{"prefix":"pj","keep_warm":2,"lanes":2}`))
	if err != nil || c == nil || c.Data.Lanes.Count() != 2 {
		t.Errorf("LoadWith: %+v, %v; want the newer key dropped", c, err)
	}
	if _, fails, err := LoadForValidate(root); err != nil || len(fails) != 1 || !strings.HasPrefix(fails[0], "FAIL .keep_warm: ") {
		t.Errorf("LoadForValidate: %q, %v, want the strict reader still refusing it", fails, err)
	}
}

// T-1063 (RV-153 #1): LoadReader's warning names both causes too: this binary is older than the config, or the key was
// retired (merge.max_load is one of the nine v0.28 keys, so restarting fixes nothing).
func TestLoadReaderWarnNamesBothCauses(t *testing.T) {
	root := writeProject(t, `{"merge":{"max_load":10}}`, "")
	_, warn, err := LoadReader(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(warn, "WARN ") || strings.Contains(warn, "\n") {
		t.Errorf("warn = %q, want one WARN line", warn)
	}
	for _, want := range []string{`unknown field "max_load"`, "older than the config (restart it)",
		"key was retired (obor config validate names it; delete it)"} {
		if !strings.Contains(warn, want) {
			t.Errorf("warn %q, want %q", warn, want)
		}
	}
}

// T-1081 (DD-v0.32 #1): the keys T-1076 deleted (the promotion's), v0.29's two removed keys and T-1103's merge.trailer
// never stop Load, the load of every command: each is ignored with one warning naming it; only LoadForValidate fails,
// one line per key.
func TestRetiredKeysWarnOnLoadFailValidate(t *testing.T) {
	for _, tc := range []struct{ name, cfg string }{
		{"the promotion's keys", `{"prefix":"pj","branches":{"main":"main","promote":"release"},` +
			`"merge":{"promote_weight":2,"weights":{"core":3}}}`},
		{"v0.29's removed keys", `{"prefix":"pj","merge":{"receipt_max_age":60},"checks":{"load_limit":12.5}}`},
		{"T-1103's removed key", `{"prefix":"pj","merge":{"trailer":"Co-Authored-By: x"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeProject(t, tc.cfg, "")
			keys := map[string][]string{"the promotion's keys": {"branches.promote", "merge.promote_weight", "merge.weights"},
				"v0.29's removed keys": {"merge.receipt_max_age", "checks"},
				"T-1103's removed key": {"merge.trailer"}}[tc.name]
			var out strings.Builder
			defer func(w io.Writer) { Warn = w }(Warn)
			Warn = &out
			c, err := Load(root)
			if err != nil || c == nil || c.Data.Prefix != "pj" || c.Data.Branches.Main != "main" {
				t.Fatalf("Load: %+v, %v; want the config", c, err)
			}
			if n := strings.Count(out.String(), "\n"); n != len(keys) {
				t.Errorf("Load warned %d lines, want one per key (%d):\n%s", n, len(keys), out.String())
			}
			out.Reset()
			c, fails, err := LoadForValidate(root)
			if err != nil || c == nil || len(fails) != len(keys) {
				t.Fatalf("LoadForValidate: %q, %v; want one FAIL per key %q", fails, err, keys)
			}
			for i, key := range keys {
				want := "FAIL ." + key + ": unknown to this obor (retired, or newer than this binary): delete it (.obor/config.json)"
				if fails[i] != want {
					t.Errorf("FAIL line %q, want %q", fails[i], want)
				}
			}
			if out.Len() != 0 {
				t.Errorf("LoadForValidate warned: %q, want nothing on Warn", out.String())
			}
		})
	}
}
