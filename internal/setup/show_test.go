package setup

import (
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/testkit"
)

// T-647: obor setup show prints the setup table from the config: per difficulty level its types in order with the
// first one's model and effort (or "no lane" when no lane has it, then the types that do), the lanes that run the
// level, the level's fallback; then the roles' types and the owner's answer. It is read-only.
func TestSetupShowPrintsTheTable(t *testing.T) {
	t.Parallel()
	x := newFx(t,
		testkit.Set("lane_types", `{
			"opus-high": {"kind": "claude", "args": ["--model", "opus", "--effort", "high"]},
			"opus": {"kind": "claude", "args": ["--model", "opus", "--effort", "medium"]},
			"sonnet": {"kind": "claude", "args": ["--model", "sonnet", "--effort", "medium"]},
			"deepseek": {"kind": "opencode", "args": []},
			"terra": {"kind": "codex", "args": ["-m", "gpt-6.1-terra", "-c", "model_reasoning_effort=high"]}}`),
		testkit.Set("lanes", `{"O-01": "opus", "S-01": "sonnet"}`),
		testkit.Set("roles", `{"lead": {"type": "opus"}, "integrator": {"type": "opus"},
			"reviewer": {"type": "terra"}}`),
		testkit.Set("setup", `{"confirmed": false}`))
	before := x.Read(".obor/config.json")
	out := x.ok(t, "show")
	want := strings.Join([]string{
		"models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch the lane):",
		"  hard   → opus-high (opus, effort high): no lane | opus (opus, effort medium): O-01",
		"  medium → sonnet (sonnet, effort medium): S-01 | deepseek (opencode): no lane",
		"  simple → deepseek (opencode): no lane | sonnet (sonnet, effort medium): S-01",
		"roles: lead opus (opus, effort medium), integrator opus (opus, effort medium), reviewer terra (gpt-6.1-terra, effort high)",
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

// T-647: a type with a lane is never marked; a type that exists but has no lane is "(no lane)" with the level's types
// that do run it; the table never names a lane_type the project lacks (the built-in default names some); a level with
// no lane anywhere names the T-631 fallback's lanes or says a task of it waits; a confirmed setup prints no starting
// point.
func TestSetupShowLevelLines(t *testing.T) {
	t.Parallel()
	types := `{
		"opus": {"kind": "claude", "args": ["--model", "opus", "--effort", "medium"]},
		"sonnet": {"kind": "claude", "args": ["--model", "sonnet"]},
		"deepseek": {"kind": "opencode", "args": []}}`
	x := newFx(t,
		testkit.Set("lane_types", types),
		testkit.Set("lanes", `{"O-01": "opus", "S-01": "sonnet"}`),
		testkit.Set("difficulty", `{"hard": ["opus"], "simple": ["deepseek", "sonnet", "haiku"]}`),
		testkit.Set("setup", `{"confirmed": true}`))
	out := x.ok(t, "show")
	for _, want := range []string{
		"  hard   → opus (opus, effort medium): O-01\n",
		"  medium → sonnet (sonnet): S-01 | deepseek (opencode): no lane\n",
		"  simple → deepseek (opencode): no lane | sonnet (sonnet): S-01\n",
		"confirmed: yes\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("obor setup show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "haiku") || strings.Contains(out, "opus-high") {
		t.Errorf("the table names a lane_type the project lacks:\n%s", out)
	}
	if strings.Contains(out, "starting point") {
		t.Errorf("a confirmed setup prints the starting point line:\n%s", out)
	}
	// A level whose own types have no lane at all names the T-631 fallback; with no lane anywhere, the task waits.
	y := newFx(t, testkit.Set("lane_types", types), testkit.Set("lanes", `{"O-01": "opus", "S-01": "sonnet"}`),
		testkit.Set("difficulty", `{"simple": ["deepseek"]}`))
	if out := y.ok(t, "show"); !strings.Contains(out, "  simple → deepseek (opencode): no lane | no simple lane: fallback sonnet\n") {
		t.Errorf("a level with no lane of its own:\n%s", out)
	}
	z := newFx(t, testkit.Set("lane_types", types), testkit.Set("lanes", `{}`),
		testkit.Set("difficulty", `{"simple": ["deepseek"]}`))
	if out := z.ok(t, "show"); !strings.Contains(out, "  simple → deepseek (opencode): no lane: a simple task waits for one\n") {
		t.Errorf("no lanes:\n%s", out)
	}
	// A level naming only types the project lacks: another level's lanes run it, and no such type is named.
	w := newFx(t, testkit.Set("lane_types", types), testkit.Set("lanes", `{"O-01": "opus", "S-01": "sonnet"}`),
		testkit.Set("difficulty", `{"simple": ["haiku"]}`))
	if out := w.ok(t, "show"); !strings.Contains(out, "  simple → no lane_type this level lists: S-01 | fallback sonnet\n") {
		t.Errorf("a level listing only missing types:\n%s", out)
	}
}

// T-653: a level with several types prints each type in order with its own model and effort and only its own lanes;
// a type whose args name no model shows its kind; a type no lane has says "no lane" — the level's other types keep
// their own. The order is the config's.
func TestSetupShowLanesStayUnderTheirType(t *testing.T) {
	t.Parallel()
	x := newFx(t,
		testkit.Set("lane_types", `{
			"sonnet": {"kind": "claude", "args": ["--model", "sonnet", "--effort", "medium"]},
			"deepseek": {"kind": "opencode", "args": ["--auto"]},
			"opus-high": {"kind": "claude", "args": ["--model", "opus", "--effort", "high"]},
			"opus": {"kind": "claude", "args": ["--model", "opus", "--effort", "medium"]}}`),
		testkit.Set("lanes", `{"S-01": "sonnet", "S-02": "sonnet", "D-01": "deepseek", "D-02": "deepseek",
			"O-01": "opus"}`),
		testkit.Set("difficulty", `{"hard": ["opus-high", "opus"], "medium": ["deepseek", "sonnet"],
			"simple": ["deepseek", "sonnet"]}`))
	out := x.ok(t, "show")
	for _, want := range []string{
		"  hard   → opus-high (opus, effort high): no lane | opus (opus, effort medium): O-01\n",
		"  medium → deepseek (opencode): D-01, D-02 | sonnet (sonnet, effort medium): S-01, S-02\n",
		"  simple → deepseek (opencode): D-01, D-02 | sonnet (sonnet, effort medium): S-01, S-02\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("obor setup show lacks %q:\n%s", want, out)
		}
	}
	// The config's order, not the built-in default's: sonnet first when the project lists it first.
	y := newFx(t,
		testkit.Set("lane_types", `{
			"sonnet": {"kind": "claude", "args": ["--model", "sonnet", "--effort", "medium"]},
			"deepseek": {"kind": "opencode", "args": ["--auto"]}}`),
		testkit.Set("lanes", `{"S-01": "sonnet", "D-01": "deepseek"}`),
		testkit.Set("difficulty", `{"medium": ["sonnet", "deepseek"]}`))
	if out := y.ok(t, "show"); !strings.Contains(out,
		"  medium → sonnet (sonnet, effort medium): S-01 | deepseek (opencode): D-01\n") {
		t.Errorf("a level's types out of the config's order:\n%s", out)
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

// T-647: obor init's output ends with the setup table, after the OK line.
func TestOborInitEndsWithTheSetupTable(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	out := x.init(t)
	tail := strings.Join([]string{
		"",
		"models and effort per difficulty (config difficulty; a spec's Difficulty: line picks the level, dispatch the lane):",
		"  hard   → opus-high (opus, effort high): no lane | opus (opus, effort medium): O-01",
		"  medium → sonnet (sonnet, effort medium): S-01 | deepseek (opencode): no lane",
		"  simple → deepseek (opencode): no lane | sonnet (sonnet, effort medium): S-01 | haiku (haiku): no lane",
		"roles: lead opus (opus, effort medium), integrator opus (opus, effort medium), reviewer sol (gpt-6.1-sol, effort high)",
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
