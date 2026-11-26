package project

import (
	"path/filepath"
	"strings"
	"testing"
)

// ProbeDir is one dir per project and agent under os.TempDir, outside the checkout: the test points TMPDIR (the
// function's seam) at a temp dir of its own.
func TestProbeDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	cfg, _ := plainProject(t, `{"version":2,"project":{"name":"proj"}}`)

	got := ProbeDir(cfg, "pj-deep-dive")
	want := filepath.Join(base, "obor-probe", "proj", "pj-deep-dive")
	if got != want {
		t.Errorf("ProbeDir = %q, want %q", got, want)
	}
	if got == cfg.Root || strings.HasPrefix(got, cfg.Root+string(filepath.Separator)) {
		t.Errorf("ProbeDir %q is inside the project root %s", got, cfg.Root)
	}
	if other := ProbeDir(cfg, "pj-rev"); other == got || other != filepath.Join(base, "obor-probe", "proj", "pj-rev") {
		t.Errorf("another agent's ProbeDir = %q, want the agent in it", other)
	}
}
