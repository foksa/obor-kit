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
				_, err := Load(root)
				refused("Load", nil, err)
				c, err := LoadDaemon(root)
				refused("LoadDaemon", c, err)
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
// dropped by the lenient readers, with their warning.
func TestNewerUnknownKeyStillDropped(t *testing.T) {
	root := writeProject(t, `{"prefix":"pj","keep_warm":1}`, "")
	var out strings.Builder
	defer func(w io.Writer) { Warn = w }(Warn)
	Warn = &out
	c, err := LoadDaemon(root)
	if err != nil || c == nil || c.Data.Prefix != "pj" {
		t.Fatalf("LoadDaemon: %+v, %v; want the config", c, err)
	}
	if !strings.Contains(out.String(), `unknown key "keep_warm" ignored`) {
		t.Errorf("LoadDaemon warn = %q, want the newer-config warning", out.String())
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
	if err != nil || c == nil || c.Data.Lanes != 2 {
		t.Errorf("LoadWith: %+v, %v; want the newer key dropped", c, err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), `unknown field "keep_warm"`) {
		t.Errorf("Load: %v, want the strict reader still refusing it", err)
	}
}
