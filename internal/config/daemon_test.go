package config

import (
	"bytes"
	"strings"
	"testing"
)

// TestLoadDaemonUnknownPaths: LoadDaemon drops each unknown member found by its schema path, warns once per path, keeps
// the known members that share a name, and still fails a type error; Load refuses every case (RV-41 #2).
func TestLoadDaemonUnknownPaths(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, local string
		warn             []string
		check            func(t *testing.T, c *Config)
	}{
		{
			name: "one unknown field (control)",
			cfg:  `{"merge":{"future":1}}`,
			warn: []string{"merge.future"},
		},
		{
			// RV-41 probe: the same new name in two objects
			name: "the same name in two nested objects",
			cfg:  `{"merge":{"future":1},"slots":{"future":2}}`,
			warn: []string{"merge.future", "slots.future"},
		},
		{
			name: "the same name in two class-list entries",
			cfg:  `{"merge":{"classes":[{"name":"a","paths":["x"],"future":1},{"name":"b","paths":["y"],"future":2}]}}`,
			warn: []string{"merge.classes[0].future", "merge.classes[1].future"},
			check: func(t *testing.T, c *Config) {
				if cl := c.Data.Merge.Classes; len(cl) != 2 || cl[0].Name != "a" || cl[1].Paths[0] != "y" {
					t.Errorf("classes = %+v, want both kept", cl)
				}
			},
		},
		{
			name: "the same name in two ordered commands",
			cfg:  `{"commands":{"a":{"cmd":"x","future":1},"b":{"cmd":"y","future":2}}}`,
			warn: []string{"commands.a.future", "commands.b.future"},
			check: func(t *testing.T, c *Config) {
				if _, ok := c.Command("b"); !ok || len(c.Data.Commands.Keys) != 2 {
					t.Errorf("commands = %v, want a and b", c.Data.Commands.Keys)
				}
			},
		},
		{
			name: "the same name in two models",
			cfg:  `{"models":{"x":{"kind":"claude","bar":1},"y":{"kind":"codex","bar":2}}}`,
			warn: []string{"models.x.bar", "models.y.bar"},
		},
		{
			name: "an unknown member sharing a name with a known one elsewhere",
			cfg:  `{"merge":{"guard":"g","future":{"guard":1}},"models":{"x":{"kind":"claude","model":"m3","merge":{"guard":2}}}}`,
			warn: []string{"merge.future", "models.x.merge"},
			check: func(t *testing.T, c *Config) {
				if c.Data.Merge.Guard != "g" || c.Data.Models.M["x"].Model != "m3" {
					t.Errorf("known members lost: merge.guard=%q model=%q", c.Data.Merge.Guard, c.Data.Models.M["x"].Model)
				}
			},
		},
		{
			name:  "the same name in config.json and local.json",
			cfg:   `{"merge":{"future":1}}`,
			local: `{"slots":{"future":2},"merge":{"guard":"g"}}`,
			warn:  []string{"merge.future", "slots.future"},
			check: func(t *testing.T, c *Config) {
				if c.Data.Merge.Guard != "g" {
					t.Errorf("merge.guard = %q, want the overlay kept", c.Data.Merge.Guard)
				}
			},
		},
		{
			name: "inside a map stays untouched",
			cfg:  `{"paths":{"future":"x"},"merge":{"future":1}}`,
			warn: []string{"merge.future"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeProject(t, tc.cfg, tc.local)
			if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("Load: %v, want an unknown field error (strict stays strict)", err)
			}
			var out bytes.Buffer
			defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
			Warn = &out
			c, err := LoadDaemon(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadDaemon(root); err != nil {
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

// TestLoadDaemonTypeErrors: a wrong type is still an error next to an unknown member.
func TestLoadDaemonTypeErrors(t *testing.T) {
	root := writeProject(t, `{"merge":{"future":1,"nightly_at":5},"slots":{"future":2}}`, "")
	var out bytes.Buffer
	defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
	Warn = &out
	if _, err := LoadDaemon(root); err == nil || !strings.Contains(err.Error(), "nightly_at") {
		t.Errorf("LoadDaemon: %v, want the nightly_at type error", err)
	}
}
