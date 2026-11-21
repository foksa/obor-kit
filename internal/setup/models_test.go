package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/testkit"
)

// sharedModels is agents.models of the project's .obor/config.json as the file has it (not the merged config).
func sharedModels(t *testing.T, x *fx) map[string]config.ModelEntry {
	t.Helper()
	var doc struct {
		Agents struct {
			Models map[string]config.ModelEntry `json:"models"`
		} `json:"agents"`
	}
	if b := x.Read(".obor/config.json"); json.Unmarshal([]byte(b), &doc) != nil {
		t.Fatalf(".obor/config.json does not parse:\n%s", b)
	}
	return doc.Agents.Models
}

// upgradeModel is UpgradeModels' entry of name (the kind and id the step writes).
func upgradeModel(t *testing.T, name string) config.NamedModel {
	t.Helper()
	i := slices.IndexFunc(config.UpgradeModels, func(e config.NamedModel) bool { return e.Name == name })
	if i < 0 {
		t.Fatalf("%s is no UpgradeModels entry", name)
	}
	return config.UpgradeModels[i]
}

// obor setup agents gives a former built-in entry with no kind its former kind, keeping its id; an entry that names a
// kind is left as it is; a second run writes nothing.
func TestSetupAgentsFillsTheKindOfAFormerBuiltin(t *testing.T) {
	x := newFx(t,
		testkit.Set("agents.models", `{"opus": {"id": "claude-opus-4-6"}, "fable": {"kind": "claude", "id": "my-fable"}}`),
		testkit.Set("agents.roles", `{"lead": "opus", "integrator": "opus", "reviewer": "fable"}`),
		testkit.Set("agents.difficulty", `{"hard": ["opus"], "medium": ["sonnet"], "simple": ["opus"]}`))
	out := x.ok(t, "agents")
	has(t, out, "config agents.models = kind opus; + sonnet (uncommitted: commit it)", "the kind filled, sonnet added")
	got := sharedModels(t, x)
	for name, want := range map[string]config.ModelEntry{
		"opus":   {Kind: "claude", ID: "claude-opus-4-6"},
		"fable":  {Kind: "claude", ID: "my-fable"},
		"sonnet": {Kind: "claude", ID: "sonnet"},
	} {
		if g := got[name]; g != want {
			t.Errorf("agents.models.%s = %+v, want %+v", name, g, want)
		}
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opus", "fable", "sonnet"} {
		if _, ok := cfg.Model(name); !ok {
			t.Errorf("%s does not resolve after the step", name)
		}
	}

	before := x.Read(".obor/config.json")
	if out := x.ok(t, "agents"); strings.Contains(out, "config agents.models") || x.Read(".obor/config.json") != before {
		t.Errorf("a second run wrote the config:\n%s", out)
	}
}

// obor setup agents decides its migration from .obor/config.json alone: a shared role naming opus gets opus even when
// .obor/local.json names terra there, a model only the local overlay names is not written anywhere, and a local entry
// does not keep a shared addition from being made.
func TestSetupAgentsMigratesTheSharedConfigOnly(t *testing.T) {
	const models = `{"kimi": {"kind": "opencode", "id": "opencode-go/kimi"}}`
	for _, c := range []struct {
		name             string
		roles, levels    string
		local            string
		added, unwritten []string
		line             string
	}{
		{
			name:   "shared roles name opus, the local overlay terra",
			roles:  `{"lead": "opus", "integrator": "opus", "reviewer": "opus"}`,
			levels: `{"hard": ["opus"], "medium": ["opus"], "simple": ["opus"]}`,
			local: `{"agents": {"roles": {"lead": "terra", "advisor": "mystery"},
				"difficulty": {"hard": ["terra"]},
				"models": {"terra": {"kind": "codex", "id": "local-terra"}}}}`,
			added:     []string{"opus"},
			unwritten: []string{"terra", "mystery"},
			line:      "config agents.models = + opus (uncommitted: commit it)",
		},
		{
			name:   "a local entry does not suppress a shared addition",
			roles:  `{"lead": "terra", "integrator": "terra", "reviewer": "terra"}`,
			levels: `{"hard": ["terra"], "medium": ["terra"], "simple": ["terra"]}`,
			local:  `{"agents": {"models": {"terra": {"kind": "codex", "id": "local-terra"}}}}`,
			added:  []string{"terra"},
			line:   "config agents.models = + terra (uncommitted: commit it)",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newFx(t, testkit.Set("agents.models", models), testkit.Set("agents.roles", c.roles),
				testkit.Set("agents.difficulty", c.levels))
			x.Write(".obor/local.json", c.local)
			out := x.ok(t, "agents")
			has(t, out, c.line, "the shared migration's line")
			got := sharedModels(t, x)
			if got["kimi"] != (config.ModelEntry{Kind: "opencode", ID: "opencode-go/kimi"}) {
				t.Errorf("agents.models.kimi = %+v, want the entry the file had", got["kimi"])
			}
			for _, name := range c.added {
				if e := upgradeModel(t, name); got[name].Kind != e.Kind || got[name].ID != e.ID {
					t.Errorf("agents.models.%s = %+v, want the upgrade entry %+v", name, got[name], e)
				}
			}
			for _, name := range c.unwritten {
				if _, ok := got[name]; ok {
					t.Errorf("config.json has agents.models.%s, written from the local overlay", name)
				}
			}
			// the whole config: with the local overlay gone (another checkout), what the step wrote still resolves
			// and what only local.json named does not.
			if err := os.Remove(filepath.Join(x.Root, ".obor", "local.json")); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(x.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range c.added {
				if _, ok := cfg.Model(name); !ok {
					t.Errorf("%s does not resolve without the local overlay", name)
				}
			}
			for _, name := range c.unwritten {
				if _, ok := cfg.Model(name); ok {
					t.Errorf("%s resolves without the local overlay", name)
				}
			}

			before := x.Read(".obor/config.json")
			if out := x.ok(t, "agents"); strings.Contains(out, "config agents.models") || x.Read(".obor/config.json") != before {
				t.Errorf("a second run wrote the config:\n%s", out)
			}
		})
	}
}
