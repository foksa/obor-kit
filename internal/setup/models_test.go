package setup

import (
	"encoding/json"
	"maps"
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

// obor init decides the roles' fallback from .obor/config.json alone (the file it writes), after the kind fill: a role
// whose shared model resolves and whose kind's CLI is on PATH is kept, else it goes to the first shared claude model in
// config order, else to the starter opus entry (added); an opus of another kind fails init naming the roles and the
// entry to add. The shared file validates for the roles alone, and a second run writes nothing.
func TestOborInitRoleFallbackFromTheSharedConfig(t *testing.T) {
	t.Parallel()
	const (
		sol    = `"sol": {"kind": "codex", "id": "gpt-6.1-sol"}`
		custom = `"custom": {"kind": "claude", "id": "claude-opus-5"}`
		opus   = `"opus": {"kind": "claude", "id": "opus"}`
	)
	for _, c := range []struct {
		name   string
		models string // the shared agents.models members ("": none)
		role   string // the shared role model of lead, integrator and reviewer ("": unset)
		local  string // .obor/local.json ("": none)
		want   string // the role model after init ("": init fails)
		has    map[string]config.ModelEntry
	}{
		{name: "a claude model of its own", models: sol + "," + custom, role: "sol", want: "custom",
			has: map[string]config.ModelEntry{"sol": {Kind: "codex", ID: "gpt-6.1-sol"}, "custom": {Kind: "claude", ID: "claude-opus-5"}}},
		{name: "a claude role is kept", models: opus, role: "opus", want: "opus",
			has: map[string]config.ModelEntry{"opus": {Kind: "claude", ID: "opus"}}},
		{name: "no claude model gets the starter opus", models: sol, role: "sol", want: "opus",
			has: map[string]config.ModelEntry{"sol": {Kind: "codex", ID: "gpt-6.1-sol"}, "opus": {Kind: "claude", ID: "opus"}}},
		{name: "an opus with no kind gets claude and keeps its id", models: sol + `, "opus": {"id": "my-opus"}`, role: "sol",
			want: "opus", has: map[string]config.ModelEntry{"opus": {Kind: "claude", ID: "my-opus"}}},
		{name: "an opus only the local overlay has is added to the shared file", models: sol, role: "sol", want: "opus",
			local: `{"agents": {"models": {"opus": {"kind": "claude", "id": "local-opus"}}}}`,
			has:   map[string]config.ModelEntry{"opus": {Kind: "claude", ID: "opus"}}},
		{name: "an opus of another kind fails init", models: sol + `, "opus": {"kind": "codex", "id": "gpt-6.1-terra"}`,
			role: "sol", has: map[string]config.ModelEntry{"sol": {Kind: "codex", ID: "gpt-6.1-sol"},
				"opus": {Kind: "codex", ID: "gpt-6.1-terra"}}},
		{name: "no model and no role gets the starter opus", want: "opus",
			has: map[string]config.ModelEntry{"opus": {Kind: "claude", ID: "opus"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFakeGitInitFx(t, false)
			x.Config(testkit.Set("agents.difficulty", `{"hard": ["sol"], "medium": ["sol"], "simple": ["sol"]}`))
			if c.models == "" {
				x.editConfig(t, []string{"agents", "models"}, nil)
			} else {
				x.editConfig(t, []string{"agents", "models"}, json.RawMessage("{"+c.models+"}"))
			}
			if c.role == "" {
				x.editConfig(t, []string{"agents", "roles"}, nil)
			} else {
				x.editConfig(t, []string{"agents", "roles"},
					map[string]string{"lead": c.role, "integrator": c.role, "reviewer": c.role})
			}
			if c.local != "" {
				x.Write(".obor/local.json", c.local)
			}
			roles := []string{"lead", "integrator", "reviewer"}
			sharedRoles := func() map[string]string {
				shared, err := config.LoadMain(x.Root)
				if err != nil {
					t.Fatal(err)
				}
				out := map[string]string{}
				for _, r := range roles {
					rc, _ := shared.Role(r)
					out[r] = rc.Type
				}
				return out
			}
			if c.role != "" { // precondition: the roles name what the row says
				for r, got := range sharedRoles() {
					if got != c.role {
						t.Fatalf("precondition: shared %s = %q, want %q", r, got, c.role)
					}
				}
			}

			if c.want == "" {
				out, rc := x.run(t, "obor-init")
				l := last(out)
				if rc != 1 || !strings.HasPrefix(l, "FAIL ") || !strings.Contains(l, "lead, integrator, reviewer") ||
					!strings.Contains(l, `"<name>": {"kind": "claude", "id": "opus"}`) {
					t.Fatalf("exit %d, last line %q: want a FAIL naming the roles and the entry to add", rc, l)
				}
				for r, got := range sharedRoles() {
					if got != c.role {
						t.Errorf("shared %s = %q after the refusal, want it untouched (%q)", r, got, c.role)
					}
				}
				if got := sharedModels(t, x.fx); !maps.Equal(got, c.has) {
					t.Errorf("agents.models = %+v after the refusal, want it as it was %+v", got, c.has)
				}
				return
			}

			x.init(t)
			shared, err := config.LoadMain(x.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range roles {
				rc, _ := shared.Role(r)
				if rc.Type != c.want {
					t.Errorf("shared %s = %q, want %q", r, rc.Type, c.want)
				}
				if err := shared.CheckModelRef(rc.Type); err != nil {
					t.Errorf("the shared file alone does not validate the %s's model: %v", r, err)
				}
			}
			got := sharedModels(t, x.fx)
			for name, want := range c.has {
				if got[name] != want {
					t.Errorf("agents.models.%s = %+v, want %+v", name, got[name], want)
				}
			}
			before := x.Read(".obor/config.json")
			if x.init(t); x.Read(".obor/config.json") != before {
				t.Errorf("config changed on a rerun")
			}
		})
	}
}
