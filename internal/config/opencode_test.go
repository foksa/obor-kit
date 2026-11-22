package config_test

import (
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/testkit"
)

// OpenCode (a plugin kind, answering in process here) takes the effort as the model's #variant in
// its plugin's env; a value not among its efforts, and an effort on a model without an id, are refused (the second by
// a start of the args and effort: agents.ArgsChecker).
func TestOpencodeModelEffort(t *testing.T) {
	p := testkit.NewDir(t)
	p.Config(testkit.Set("agents.models.muse", map[string]any{"kind": "opencode", "id": ""}),
		testkit.Set("agents.models.gem", map[string]any{"kind": "agy", "id": "gemini-4-neon-high"}))
	p.Write(".obor/agy/settings_override.json", `{"toolPermission":"always-proceed","permissions":{"allow":["command(go test)"]}}`)
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Kinds().IsPlugin("opencode") {
		t.Fatal("precondition: opencode is a plugin kind of the loaded config")
	}
	// Launch hands the project's override files to a lead's start too: agy merges the override's rules into its lead's
	// settings file without its mode keys.
	if gem, ok := c.Model("gem"); !ok {
		t.Fatal("precondition: config has agy model gem")
	} else if over, err := c.Overrides("agy"); err != nil || len(over) != 1 {
		t.Fatalf("Overrides(agy) = %q, %v", over, err)
	} else {
		var settings string
		if _, err := gem.Launch("lead", over, agents.Start{
			WriteState: func(name string, b []byte) (string, error) { settings = string(b); return "/state/" + name, nil },
		}); err != nil || !strings.Contains(settings, `"command(go test)"`) || strings.Contains(settings, `"always-proceed"`) {
			t.Errorf("an agy lead's launch with an override: settings %q, err %v; want allow rule without toolPermission", settings, err)
		}
	}
	ds, _ := c.Model("deepseek")
	l, err := ds.Launch("lead", nil, agents.Start{Effort: "high"})
	if env := l.Env; err != nil || env["OBOR_OPENCODE_MODEL"] != "opencode-go/deepseek-v4.1-flash#high" {
		t.Errorf("deepseek at high: env %v, %v; want the model's #high variant", l.Env, err)
	}
	if err := c.CheckModelRef("deepseek:high"); err != nil {
		t.Errorf("deepseek:high refused: %v", err)
	}
	// Every agent but a lead starts trimmed: OpenCode's switch is in the start's env, and a lead's start unsets it
	// (the kind protocol's trim_user, through the kind's own start).
	const skills = "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"
	for role, want := range map[string]string{"worker": "1", "reviewer": "1", "lead": ""} {
		l, err := ds.Launch(role, nil, agents.Start{})
		if v, set := l.Env[skills]; err != nil || !set || v != want {
			t.Errorf("a %s start: %s = %q (in the env: %v), %v; want %q", role, skills, v, set, err, want)
		}
	}
	for ref, refusal := range map[string]string{
		"deepseek:big": `effort "big" is not one of opencode's`,
		"muse:high":    "kind opencode: start: effort high needs a model id",
	} {
		if err := c.CheckModelRef(ref); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Errorf("CheckModelRef(%s) = %v, want %q", ref, err, refusal)
		}
	}
}
