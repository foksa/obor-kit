package herdr_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/herdr"
)

// ResumeHint: herdr's config.toml ($HERDR_CONFIG_PATH, else ~/.config/herdr/config.toml) counts
// only `[session] resume_agents_on_restore = false` as set (a comment after it too); a missing file, another value or
// the key in another section gives the one line to add, naming the file.
func TestResumeHint(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
		set        bool
	}{
		{"no config", "", false},
		{"false under [session]", "[session]\nresume_agents_on_restore = false\n", true},
		{"false with a comment", "[ui]\nx = 1\n[session]\nresume_agents_on_restore = false # obor\n", true},
		{"true", "[session]\nresume_agents_on_restore = true\n", false},
		{"false outside [session]", "[other]\nresume_agents_on_restore = false\n", false},
		{"a later section ends [session]", "[session]\n[other]\nresume_agents_on_restore = false\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if c.body != "" {
				if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
				if got, _ := os.ReadFile(path); string(got) != c.body {
					t.Fatalf("precondition: config holds %q", got)
				}
			}
			h := &herdr.Setup{Env: func(k string) (string, bool) {
				if k == "HERDR_CONFIG_PATH" {
					return path, true
				}
				return "", false
			}}
			hint := h.ResumeHint()
			if c.set {
				if hint != "" {
					t.Fatalf("set in %s: hint %q, want none", path, hint)
				}
				return
			}
			if !strings.Contains(hint, "set resume_agents_on_restore = false under [session] in "+path) {
				t.Errorf("hint %q, want the line naming %s", hint, path)
			}
			got, err := os.ReadFile(path)
			if c.body == "" && err == nil {
				t.Errorf("herdr's config created: %q", got)
			} else if c.body != "" && (err != nil || string(got) != c.body) {
				t.Errorf("herdr's config edited: %q, %v", got, err)
			}
		})
	}
}

// ResumeHint with no HERDR_CONFIG_PATH names ~/.config/herdr/config.toml under HOME.
func TestResumeHintHomePath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	h := &herdr.Setup{Env: func(k string) (string, bool) {
		if k == "HOME" {
			return home, true
		}
		return "", false
	}}
	want := filepath.Join(home, ".config", "herdr", "config.toml")
	if hint := h.ResumeHint(); !strings.Contains(hint, " in "+want+" ") {
		t.Errorf("hint %q, want %s", hint, want)
	}
}
