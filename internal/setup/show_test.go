package setup

import (
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/testkit"
)

// T-647, T-765: obor setup show prints the setup table from the config: per difficulty level its model refs in order,
// each with its kind, model id and the ref's effort; the lanes; then the roles' refs and the owner's answer. It is
// read-only.
func TestSetupShowPrintsTheTable(t *testing.T) {
	t.Parallel()
	x := newFx(t,
		testkit.Set("lanes", 2),
		testkit.Set("roles", `{"lead": "opus:high", "integrator": "opus",
			"reviewer": "terra:xhigh", "deep_dive": "opus:max"}`),
		testkit.Set("setup", `{"confirmed": false}`))
	before := x.Read(".obor/config.json")
	out := x.ok(t, "show")
	want := strings.Join([]string{
		"models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch its first free model):",
		"  hard   → opus:high (claude opus, effort high)",
		"  medium → sonnet (claude sonnet) | deepseek (opencode opencode-go/deepseek-v4.1-flash)",
		"  simple → deepseek (opencode opencode-go/deepseek-v4.1-flash) | sonnet (claude sonnet) | haiku (claude haiku)",
		"lanes: 2 (L-01, L-02; each runs the model its dispatch picks)",
		"roles: lead opus:high (claude opus, effort high), integrator opus (claude opus), reviewer terra:xhigh (codex gpt-6.1-terra, effort xhigh), deep_dive opus:max (claude opus, effort max)",
		"confirmed: no",
		"starting point: the lead goes through it with you at its first session",
		"",
	}, "\n")
	if out != want {
		t.Errorf("obor setup show:\n%s\nwant:\n%s", out, want)
	}
	if x.Read(".obor/config.json") != before {
		t.Error("obor setup show changed the config (it is read-only)")
	}
}

// T-765: a level's own list in its order; a model from models (its own id, or none: its kind alone); a ref naming no
// model marked; no lanes is "0"; a role not set says so; a confirmed setup prints no starting point.
func TestSetupShowLevelLines(t *testing.T) {
	t.Parallel()
	x := newFx(t,
		testkit.Set("models", `{"kimi": {"kind": "opencode", "model": "opencode-go/kimi"}, "muse": {"kind": "opencode", "model": ""}}`),
		testkit.Set("difficulty", `{"hard": ["kimi:high", "opus"], "medium": ["muse"], "simple": ["ghost"]}`),
		testkit.Set("setup", `{"confirmed": true}`))
	out := x.ok(t, "show")
	for _, want := range []string{
		"  hard   → kimi:high (opencode opencode-go/kimi, effort high) | opus (claude opus)\n",
		"  medium → muse (opencode)\n",
		"  simple → ghost (no such model)\n",
		"lanes: 0\n",
		"roles: lead - (roles.lead not set),",
		"confirmed: yes\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("obor setup show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "starting point") {
		t.Errorf("a confirmed setup prints the starting point line:\n%s", out)
	}
}

// T-647: obor init writes setup.confirmed: false when the config has no such key, and a rerun keeps the owner's true.
func TestOborInitRecordsTheSetupAnswer(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Setup.Confirmed == nil || *cfg.Data.Setup.Confirmed {
		t.Fatalf("a new project's setup.confirmed = %v, want false", cfg.Data.Setup.Confirmed)
	}
	if stateOf(out, "setup") != "done" || !strings.Contains(summary(out, "setup"), "the lead asks the owner") {
		t.Errorf("setup step %q", summary(out, "setup"))
	}
	// The owner answers at the first session: a rerun keeps that answer.
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"setup", "confirmed"}, true)
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	out = x.init(t)
	if cfg, err = config.Load(x.Root); err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Setup.Confirmed == nil || !*cfg.Data.Setup.Confirmed {
		t.Errorf("a rerun changed setup.confirmed to %v, want the owner's true", cfg.Data.Setup.Confirmed)
	}
	if stateOf(out, "setup") != "already" || !strings.HasSuffix(summary(out, "setup"), "confirmed by the owner") {
		t.Errorf("setup step after the answer: %q", summary(out, "setup"))
	}
}

// T-647: an existing config without the setup key (written before the table) gets confirmed: false on init: nothing
// there says the owner saw the table.
func TestOborInitAddsTheMissingSetupAnswer(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.init(t)
	doc, err := config.EditJSON([]byte(x.Read(".obor/config.json")), []string{"setup"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	x.Write(".obor/config.json", string(doc))
	out := x.init(t)
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Setup.Confirmed == nil || *cfg.Data.Setup.Confirmed {
		t.Errorf("setup.confirmed = %v, want false (init restores it when the key is absent)", cfg.Data.Setup.Confirmed)
	}
	if stateOf(out, "setup") != "done" {
		t.Errorf("setup step %q", summary(out, "setup"))
	}
}

// T-647, T-724, RV-88 #1: obor init's output ends with the setup table, after the OK line; no deepseek (OpenCode is not opted in).
func TestOborInitEndsWithTheSetupTable(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	out := x.init(t)
	tail := strings.Join([]string{
		"",
		"models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch its first free model):",
		"  hard   → opus:high (claude opus, effort high)",
		"  medium → sonnet (claude sonnet)",
		"  simple → sonnet (claude sonnet) | haiku (claude haiku)",
		"lanes: 2 (L-01, L-02; each runs the model its dispatch picks)",
		"roles: lead opus:high (claude opus, effort high), integrator opus (claude opus), reviewer sol:high (codex gpt-6.1-sol, effort high)",
		"confirmed: no",
		"starting point: the lead goes through it with you at its first session",
		"",
	}, "\n")
	if !strings.HasSuffix(out, tail) {
		t.Errorf("obor init's output does not end with the setup table:\n%s", out)
	}
	if i, j := strings.Index(out, "OK init "), strings.Index(out, "models and effort"); i < 0 || j < i {
		t.Errorf("the OK line does not come before the table:\n%s", out)
	}
}

// T-704: a configured researcher is listed with the other roles as a model ref; an unconfigured one is not.
func TestSetupShowListsTheResearcher(t *testing.T) {
	t.Parallel()
	without := newFx(t, testkit.Set("roles", `{"lead": "opus", "integrator": "opus", "reviewer": "opus"}`))
	if out := without.ok(t, "show"); strings.Contains(out, "researcher") {
		t.Fatalf("precondition: an unconfigured researcher is listed:\n%s", out)
	}
	x := newFx(t, testkit.Set("roles", `{"lead": "opus", "integrator": "opus", "reviewer": "opus",
			"researcher": "sonnet:low"}`))
	if out := x.ok(t, "show"); !strings.Contains(out, "researcher sonnet:low (claude sonnet, effort low)") {
		t.Errorf("obor setup show lacks the researcher:\n%s", out)
	}
}
