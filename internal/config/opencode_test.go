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
	p.Config(testkit.Set("agents.models", map[string]any{"muse": map[string]any{"kind": "opencode", "id": ""}}))
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !agents.IsPlugin("opencode") {
		t.Fatal("precondition: opencode is a plugin kind of the loaded config")
	}
	ds, _ := c.Model("deepseek")
	l, err := ds.Launch("lead", agents.Start{Effort: "high"})
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
		l, err := ds.Launch(role, agents.Start{})
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
