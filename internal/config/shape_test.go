package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// T-1157: a config.json of shape 1 — no version, or version 1 — fails every load with one error naming the file and
// the way out: v0.33 read and rewrote it, and this obor no longer reads it (docs/design.md "Config").
func TestShape1Refused(t *testing.T) {
	for _, doc := range []string{
		`{"project":{"agent_prefix":"pj"}}`,
		`{"version":1,"project":{"agent_prefix":"pj"}}`,
	} {
		root := writeProject(t, doc, "")
		want := "obor: " + filepath.Join(root, ".obor", "config.json") + ": config shape 1, which this obor no longer " +
			"reads: run obor config migrate with obor v0.33, then upgrade (docs/UPGRADE-v0.33.md lists old and new keys)"
		c, err := Load(root)
		if c != nil || err == nil || err.Error() != want {
			t.Errorf("Load of %s: %+v, %v; want no config and the error %q", doc, c, err, want)
		}
		c, _, err = LoadForValidate(root)
		if c != nil || err == nil || err.Error() != want {
			t.Errorf("LoadForValidate of %s: %+v, %v; want no config and the error %q", doc, c, err, want)
		}
	}
}

// T-1157: local.json has no shape of its own: a shape-1 key in it is an unknown key like any other, not a shape to
// migrate — Load warns with it, and LoadForValidate names it as a FAIL line.
func TestShape1KeyInLocalIsUnknown(t *testing.T) {
	root := writeProject(t, `{"version":2,"project":{"agent_prefix":"pj"}}`, `{"handoff_tokens":400000}`)
	var out strings.Builder
	defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
	Warn = &out
	c, err := Load(root)
	if err != nil || c == nil || c.Data.Agents.HandoffTokens != nil {
		t.Fatalf("a shape-2 config.json beside a shape-1 local.json key: %+v, %v; want the config, the key dropped", c, err)
	}
	if want := `unknown key "handoff_tokens" ignored`; !strings.Contains(out.String(), want) {
		t.Errorf("Load warn = %q, want it to name %q", out.String(), want)
	}
	if _, fails, err := LoadForValidate(root); err != nil || len(fails) != 1 ||
		!strings.HasPrefix(fails[0], "FAIL .handoff_tokens: ") || !strings.HasSuffix(fails[0], "(.obor/local.json)") {
		t.Errorf("LoadForValidate: %q, %v; want one FAIL line naming local.json's handoff_tokens", fails, err)
	}
}
