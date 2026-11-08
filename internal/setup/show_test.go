package setup

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/store"
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

// T-1039: the roles line lists a custom role's verbs after its model ("": none named); a role without verbs shows none.
func TestSetupShowListsVerbs(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("roles", `{"lead": "opus", "tracker": {"model": "sonnet", "verbs": ["spec_set", "dispatch"]},
		"scout": {"verbs": ["watch"]}}`))
	out := x.ok(t, "show")
	for _, want := range []string{"lead opus (claude opus), ", "tracker sonnet (claude sonnet) [verbs spec_set dispatch]",
		"scout - (no model named) [verbs watch]"} {
		if !strings.Contains(out, want) {
			t.Errorf("obor setup show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lead opus (claude opus) [verbs") {
		t.Errorf("the lead shows verbs:\n%s", out)
	}
}

// T-979: obor setup show marks a paused model in each level's list with its end ("cleared" for none), and stops
// marking it once serve records the clear.
func TestSetupShowMarksPausedModels(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("setup", `{"confirmed": true}`))
	pauseFact(t, x.Project, "sonnet", "quota", 0)
	until := time.Now().Add(2 * time.Hour).Unix()
	pauseFact(t, x.Project, "deepseek", "down", until)
	out := x.ok(t, "show")
	for _, want := range []string{
		"sonnet (claude sonnet) (paused until cleared)",
		"deepseek (opencode opencode-go/deepseek-v4.1-flash) (paused until " + time.Unix(until, 0).Format("15:04") + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("obor setup show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "haiku (claude haiku) (paused") {
		t.Errorf("obor setup show marks a model no pause holds:\n%s", out)
	}

	unpauseFact(t, x.Project, "sonnet")
	out = x.ok(t, "show")
	if strings.Contains(out, "sonnet (claude sonnet) (paused") {
		t.Errorf("obor setup show still marks sonnet after its clear:\n%s", out)
	}
	if !strings.Contains(out, "deepseek (opencode opencode-go/deepseek-v4.1-flash) (paused until ") {
		t.Errorf("obor setup show lost deepseek's pause after sonnet's clear:\n%s", out)
	}
}

// pauseFact records a model's pause in p's log, as obor pause --model name [--for until] "why" does (until 0: until
// cleared).
func pauseFact(t *testing.T, p *testkit.Project, model, why string, until int64) {
	t.Helper()
	pauseStep(t, p, event.StepPaused, event.Pause{Model: model, Why: why, Until: until, Caller: "pj-lead"})
}

// unpauseFact records the clear of model's pause, as obor pause --model name --clear does.
func unpauseFact(t *testing.T, p *testkit.Project, model string) {
	t.Helper()
	pauseStep(t, p, event.StepUnpaused, event.Pause{Model: model, Caller: "pj-lead"})
}

// pauseStep appends d as a fact of step on the project's pause task.
func pauseStep(t *testing.T, p *testkit.Project, step string, d event.Pause) {
	t.Helper()
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(event.Record{Task: event.PauseTask, Step: step, Kind: event.KindFact, Data: data},
		time.Now()); err != nil {
		t.Fatal(err)
	}
}
