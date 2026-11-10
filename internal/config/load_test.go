package config

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

// TestOverlayMatchesJq: each case's effective value is what the Bash loader's `jq -s '.[0] * .[1]'` gives (a null
// left by the merge is unset in the effective config: a derived default, or absent).
func TestOverlayMatchesJq(t *testing.T) {
	cfg := `{"version":2,"project":{"agent_prefix":"pj"},"checks":{"output":{"summary":"T"},"heavy_at_once":4,"slot_dir":"/s"},
		"agents":{"kinds":{"claude":{"args":["a","b"],"job_note":"P"}}},
		"merge":{"check":{"cmd":"x","heavy":true,"timeout":5}}}`
	local := `{"project":{"agent_prefix":"qq"},
		"agents":{"kinds":{"claude":{"args":["c"],"job_note":null}}},
		"merge":{"check":{"heavy":null}},"checks":{"heavy_at_once":null}}`
	c := load(t, cfg, local)
	for _, tc := range []struct{ name, path, want string }{
		{"a sibling of a null key is kept", ".checks.output.summary", `"T"`},
		{"null heavy clears it", ".merge.check", `{"cmd":"x","timeout":5}`},
		{"null slots.max clears it", ".checks.heavy_at_once", ``},
		{"its sibling is kept", ".checks.slot_dir", `"/s"`},
		{"null prompt_extra clears it", ".agents.kinds.claude.job_note", ``},
		{"a list is replaced", ".agents.kinds.claude.args", `["c"]`},
		{"flat overlay", ".project.agent_prefix", `"qq"`},
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
	if c.Data.Checks.HeavyAtOnce != nil || c.Data.Checks.SlotDir != "/s" {
		t.Errorf("typed checks after a null heavy_at_once: %+v", c.Data.Checks)
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

// LoadHook ignores unknown keys (both files) and returns them, writing nothing on Warn; every other error
// is Load's; Fallback is the defaults with the derived lanes.dir.
func TestLoadHook(t *testing.T) {
	var warn strings.Builder
	defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
	Warn = &warn
	for _, tc := range []struct {
		name, cfg, local string
		unknown          []string
		err              string
	}{
		{name: "clean", cfg: `{"version":2,"project":{"agent_prefix":"pj"}}`},
		{name: "unknown keys", cfg: `{"version":2,"project":{"agent_prefix":"pj"},"keep_warm":{"x":1}}`, local: `{"merge":{"future":1}}`,
			unknown: []string{"keep_warm", "merge.future"}},
		{name: "unknown key and a type error", cfg: `{"version":2,"project":{"agent_prefix":"pj"},"keep_warm":1,"nightly":{"at":5}}`, err: "nightly.at"},
		{name: "syntax error", cfg: `{"version":2,"project":{"agent_prefix":`, err: "not valid JSON"},
		{name: "syntax error in local.json", cfg: `{"version":2,"project":{"agent_prefix":"pj"},"keep_warm":1}`, local: `{`, err: "local.json is not valid JSON"},
		{name: "no config", err: "no "},
	} {
		c, unknown, err := LoadHook(writeProject(t, tc.cfg, tc.local))
		switch {
		case tc.err == "" && (err != nil || c == nil || c.Prefix() != "pj"):
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
	if c := Fallback("/x/proj"); c.LanesDir() != "/x/proj-wt" || c.Data.Merge.TargetBranch != "main" {
		t.Errorf("Fallback = %+v", c.Data)
	}
}

// The contract between a reader and a key this binary does not know (a newer config): the reader drops it, with its warning,
// and only LoadForValidate refuses it.
func TestNewerUnknownKeyStillDropped(t *testing.T) {
	root := writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"},"keep_warm":1}`, "")
	var out strings.Builder
	defer func(w io.Writer) { Warn = w }(Warn)
	Warn = &out
	c, err := Load(root)
	if err != nil || c == nil || c.Prefix() != "pj" {
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
	c, err = LoadWith(root, []byte(`{"version":2,"project":{"agent_prefix":"pj"},"keep_warm":2,"lanes":{"count":2}}`))
	if err != nil || c == nil || c.LaneCount() != 2 {
		t.Errorf("LoadWith: %+v, %v; want the newer key dropped", c, err)
	}
	if _, fails, err := LoadForValidate(root); err != nil || len(fails) != 1 || !strings.HasPrefix(fails[0], "FAIL .keep_warm: ") {
		t.Errorf("LoadForValidate: %q, %v, want the strict reader still refusing it", fails, err)
	}
}

// LoadReader's warning names both causes too: this binary is older than the config, or the key was
// retired (merge.max_load is one of the nine v0.28 keys, so restarting fixes nothing).
func TestLoadReaderWarnNamesBothCauses(t *testing.T) {
	root := writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"},"merge":{"max_load":10}}`, "")
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

// the retired keys (the promotion's, v0.29's two and merge.trailer)
// never stop Load, the load of every command: each is ignored with one warning naming it; only LoadForValidate fails,
// one line per key.
func TestRetiredKeysWarnOnLoadFailValidate(t *testing.T) {
	for _, tc := range []struct{ name, cfg string }{
		{"the promotion's keys", `{"version":2,"project":{"agent_prefix":"pj"},"merge":{"target_branch":"main",` +
			`"promote":"release","promote_weight":2,"weights":{"core":3}}}`},
		{"v0.29's removed keys", `{"version":2,"project":{"agent_prefix":"pj"},"merge":{"receipt_max_age":60},` +
			`"checks":{"load_limit":12.5}}`},
		{"T-1103's removed key", `{"version":2,"project":{"agent_prefix":"pj"},"merge":{"trailer":"Co-Authored-By: x"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeProject(t, tc.cfg, "")
			keys := map[string][]string{"the promotion's keys": {"merge.promote", "merge.promote_weight", "merge.weights"},
				"v0.29's removed keys": {"merge.receipt_max_age", "checks.load_limit"},
				"T-1103's removed key": {"merge.trailer"}}[tc.name]
			var out strings.Builder
			defer func(w io.Writer) { Warn = w }(Warn)
			Warn = &out
			c, err := Load(root)
			if err != nil || c == nil || c.Prefix() != "pj" || c.MainBranch() != "main" {
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
