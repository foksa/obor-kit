package version

import (
	"os"
	"testing"
)

// The stamped version is the Makefile's: a release tag, a dev checkout's newest-tag-dev+sha (dirty included), or a
// plain sha, whatever the build passed.
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

// Release groups builds by the release they belong to: a dev build's suffix is not a skew, anything else is its own
// release.
func TestRelease(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ v, want string }{
		{"v0.34.0", "v0.34.0"},
		{"v0.34.0-dev+21242f62", "v0.34.0"},
		{"v0.34.0-dev+21242f62-dirty", "v0.34.0"},
		{"dev", "dev"},
		{"abc1234", "abc1234"},
		{"", ""},
	} {
		if got := Release(c.v); got != c.want {
			t.Errorf("Release(%q) = %q, want %q", c.v, got, c.want)
		}
	}
}

// CurrentLine without shim metadata is the kit's own build: the short form, with no "dev build" word.
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

// Older reads dotted versions by number (0.1.9 is older than 0.1.21) and refuses a word.
func TestOlder(t *testing.T) {
	for _, c := range []struct {
		v, min      string
		older, read bool
	}{{"0.1.21", "0.1.21", false, true}, {"0.1.20", "0.1.21", true, true}, {"0.1.9", "0.1.21", true, true},
		{"0.1.22", "0.1.21", false, true}, {"0.2", "0.1.21", false, true}, {"1.0.0-rc1", "1.0.0", false, true},
		{"0.1.20+build", "0.1.21", true, true}, {"0.1.15", "0.1.16", true, true}, {"", "0.1.21", false, false},
		{"dev", "0.1.21", false, false}} {
		older, ok := Older(c.v, c.min)
		if older != c.older || ok != c.read {
			t.Errorf("Older(%q, %q) = %v, %v; want %v, %v", c.v, c.min, older, ok, c.older, c.read)
		}
	}
}
