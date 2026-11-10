package config

import (
	"bytes"
	"strings"
	"testing"
)

// TestLoadUnknownPaths: Load drops each unknown member found by its schema path, warns once per path, keeps the known
// members that share a name, and still fails a type error; LoadForValidate fails every case, one line per path.
func TestLoadUnknownPaths(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, local string
		warn             []string
		check            func(t *testing.T, c *Config)
	}{
		{
			name: "one unknown field (control)",
			cfg:  `{"version":2,"merge":{"future":1}}`,
			warn: []string{"merge.future"},
		},
		{
			// the same new name in two objects
			name: "the same name in two nested objects",
			cfg:  `{"version":2,"merge":{"future":1},"checks":{"future":2}}`,
			warn: []string{"merge.future", "checks.future"},
		},
		{
			name: "the same name in two rule-list entries",
			cfg: `{"version":2,"merge":{"by_path":{"rules":[{"name":"a","paths":["x"],"future":1},` +
				`{"name":"b","paths":["y"],"future":2}]}}}`,
			warn: []string{"merge.by_path.rules[0].future", "merge.by_path.rules[1].future"},
			check: func(t *testing.T, c *Config) {
				if r := c.Data.Merge.ByPath.Rules; len(r) != 2 || r[0].Name != "a" || r[1].Paths[0] != "y" {
					t.Errorf("rules = %+v, want both kept", r)
				}
			},
		},
		{
			name: "the same name in two ordered commands",
			cfg:  `{"version":2,"commands":{"a":{"cmd":"x","future":1},"b":{"cmd":"y","future":2}}}`,
			warn: []string{"commands.a.future", "commands.b.future"},
			check: func(t *testing.T, c *Config) {
				if _, ok := c.Command("b"); !ok || len(c.Data.Commands.Keys) != 2 {
					t.Errorf("commands = %v, want a and b", c.Data.Commands.Keys)
				}
			},
		},
		{
			// dir, paths and at went with baseline and gate; a command keeps its cmd, the field is an unknown key.
			name: "a removed command field under a free command name",
			cfg:  `{"version":2,"commands":{"gate":{"cmd":"x","paths":["src/**"]}}}`,
			warn: []string{"commands.gate.paths"},
			check: func(t *testing.T, c *Config) {
				if cmd, ok := c.Command("gate"); !ok || cmd.Cmd != "x" {
					t.Errorf("commands.gate = %+v, %v; want its cmd kept", cmd, ok)
				}
			},
		},
		{
			name: "the same name in two models",
			cfg:  `{"version":2,"agents":{"models":{"x":{"kind":"claude","bar":1},"y":{"kind":"codex","bar":2}}}}`,
			warn: []string{"agents.models.x.bar", "agents.models.y.bar"},
		},
		{
			name: "an unknown member sharing a name with a known one elsewhere",
			cfg: `{"version":2,"merge":{"by_path":{"guard":"g"},"future":{"guard":1}},` +
				`"agents":{"models":{"x":{"kind":"claude","id":"m3","by_path":{"guard":2}}}}}`,
			warn: []string{"merge.future", "agents.models.x.by_path"},
			check: func(t *testing.T, c *Config) {
				if c.MergeGuard() != "g" || c.Data.Agents.Models.M["x"].ID != "m3" {
					t.Errorf("known members lost: merge.by_path.guard=%q id=%q", c.MergeGuard(), c.Data.Agents.Models.M["x"].ID)
				}
			},
		},
		{
			name:  "the same name in config.json and local.json",
			cfg:   `{"version":2,"merge":{"future":1}}`,
			local: `{"checks":{"future":2},"merge":{"by_path":{"guard":"g"}}}`,
			warn:  []string{"merge.future", "checks.future"},
			check: func(t *testing.T, c *Config) {
				if c.MergeGuard() != "g" {
					t.Errorf("merge.by_path.guard = %q, want the overlay kept", c.MergeGuard())
				}
			},
		},
		{
			name: "inside a map stays untouched",
			cfg:  `{"version":2,"lanes":{"env":{"future":"x"}},"merge":{"future":1}}`,
			warn: []string{"merge.future"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeProject(t, tc.cfg, tc.local)
			var out bytes.Buffer
			defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
			Warn = &out
			if _, fails, err := LoadForValidate(root); err != nil || len(fails) != len(tc.warn) {
				t.Fatalf("LoadForValidate: %q, %v; want a FAIL per path (strict stays strict)", fails, err)
			} else {
				for i, p := range tc.warn {
					if !strings.HasPrefix(fails[i], "FAIL ."+p+": unknown to this obor") {
						t.Errorf("FAIL line %q, want one naming %s", fails[i], p)
					}
				}
			}
			if out.Len() != 0 {
				t.Errorf("LoadForValidate warned on Warn: %q", out.String())
			}
			c, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(out.String(), "\n"); n != len(tc.warn) {
				t.Errorf("warnings:\n%s want %d lines (once per path)", out.String(), len(tc.warn))
			}
			for _, p := range tc.warn {
				if !strings.Contains(out.String(), `unknown key "`+p+`" ignored`) {
					t.Errorf("no warning for %s in:\n%s", p, out.String())
				}
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

// the no-op unknown-key warning names both causes: this binary is older than the config, or the
// key was retired (merge.max_load is one of the nine v0.28 keys, so restarting serve fixes nothing).
func TestWarnUnknownNamesBothCauses(t *testing.T) {
	root := writeProject(t, `{"version":2,"merge":{"max_load":10}}`, "")
	var out bytes.Buffer
	defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
	Warn = &out
	if _, err := Load(root); err != nil {
		t.Fatal(err)
	}
	warn := out.String()
	if n := strings.Count(warn, "\n"); n != 1 {
		t.Errorf("warnings: %d lines, want 1: %q", n, warn)
	}
	for _, want := range []string{`unknown key "merge.max_load"`, "older than the config (restart serve)",
		"key was retired (obor config validate names it; delete it)"} {
		if !strings.Contains(warn, want) {
			t.Errorf("warn %q, want %q", warn, want)
		}
	}
}

// TestLoadTypeErrors: a wrong type is still an error next to an unknown member.
func TestLoadTypeErrors(t *testing.T) {
	root := writeProject(t, `{"version":2,"merge":{"future":1},"nightly":{"at":5},"checks":{"future":2}}`, "")
	var out bytes.Buffer
	defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
	Warn = &out
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "nightly.at") {
		t.Errorf("Load: %v, want the nightly.at type error", err)
	}
}
