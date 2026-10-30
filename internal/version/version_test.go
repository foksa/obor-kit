package version

import (
	"os"
	"testing"
)

// The stamped version is the Makefile's: a release tag, a dev checkout's newest-tag-dev+sha (dirty included), or a
// plain sha, whatever the build passed (T-554, T-559).
func TestLineFromDescribe(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	for _, c := range []struct {
		describe, binary, source, want string
	}{
		{"v0.14.0", "/kit/dist/obor", "dev build", "obor v0.14.0 (/kit/dist/obor)"},
		{"v0.14.0-dev+21242f62", "/kit/dist/obor", "dev build", "obor v0.14.0-dev+21242f62 (/kit/dist/obor)"},
		{"v0.14.0-dev+21242f62-dirty", "/kit/dist/obor", "dev build", "obor v0.14.0-dev+21242f62-dirty (/kit/dist/obor)"},
		{"abc1234", "/kit/dist/obor", "dev build", "obor abc1234 (/kit/dist/obor)"},
		{"v0.14.0", "/home/me/.obor/bin/obor-0.14.0-darwin-arm64", "pinned",
			"obor v0.14.0 (/home/me/.obor/bin/obor-0.14.0-darwin-arm64, kit /kit, pinned)"},
		{"v0.14.0", "/home/me/.obor/bin/obor-0.14.0-darwin-arm64", "kit default",
			"obor v0.14.0 (/home/me/.obor/bin/obor-0.14.0-darwin-arm64, kit /kit, kit default)"},
	} {
		Version = c.describe
		if got := Line(c.binary, "/kit", c.source); got != c.want {
			t.Errorf("Line(%q, %q, %q) with version %q = %q, want %q", c.binary, "/kit", c.source, c.describe, got, c.want)
		}
	}
}

// CurrentLine without shim metadata is the kit's own build: the short form, with no "dev build" word (T-554).
func TestCurrentLineDropsDevBuild(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v0.14.0-dev+21242f62"
	t.Setenv("OBOR_VERSION_SOURCE", "")
	exe, err := os.Executable()
	if err != nil {
		exe = "obor"
	}
	want := "obor v0.14.0-dev+21242f62 (" + exe + ")"
	if got := CurrentLine(); got != want {
		t.Errorf("CurrentLine = %q, want %q", got, want)
	}
}
