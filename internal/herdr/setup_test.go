package herdr_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
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

// CloseProject closes the labelled panes the project owns and no other: another project's pane in the same tab, a pane
// with no label and the caller's own (Ours says no) stay; with Dry nothing closes.
func TestCloseProjectClosesOnlyWhatIsOurs(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Shell("w1:a", "pj-o-01", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t1" })
	f.Shell("w1:b", "pj-o-02", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t1" })
	f.Shell("w1:c", "zz-o-01", "/zz", func(p *herdrtest.Pane) { p.Tab = "w1:t1" }) // another project's, same tab
	f.Shell("w1:d", "", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t1" })        // no label
	f.Shell("w1:e", "pj-lead", "/pj", func(p *herdrtest.Pane) { p.Tab = "w1:t2" }) // the caller's own
	srv := herdrtest.ServeMem(t, f)
	srv.ClosePanes = true
	h := &herdr.Setup{Socket: srv.Client()}
	ours := func(p port.Pane) bool { return strings.HasPrefix(p.Label, "pj-") && p.ID != "w1:e" }
	ids := func() string {
		var out []string
		for _, p := range f.Panes {
			out = append(out, p.ID)
		}
		return strings.Join(out, " ")
	}

	lines, err := h.CloseProject(context.Background(), port.CloseProject{Name: "pj", Ours: ours, Dry: true})
	if err != nil || len(lines) != 2 || ids() != "w1:a w1:b w1:c w1:d w1:e" {
		t.Fatalf("dry run: %v, %v, panes %s", lines, err, ids())
	}
	lines, err = h.CloseProject(context.Background(), port.CloseProject{Name: "pj", Ours: ours})
	if err != nil || len(lines) != 2 {
		t.Fatalf("close: %v, %v", lines, err)
	}
	if got := ids(); got != "w1:c w1:d w1:e" {
		t.Errorf("panes left %q, want another project's, the unlabelled and the caller's", got)
	}
	if again, err := h.CloseProject(context.Background(), port.CloseProject{Name: "pj", Ours: ours}); err != nil || len(again) != 0 {
		t.Errorf("second run: %v, %v, want nothing", again, err)
	}
}
