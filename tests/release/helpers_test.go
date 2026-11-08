//go:build release

// Package release is the kit's own release tooling tests (T-902): the Makefile's check/release/publish targets,
// scripts/release, scripts/ship, scripts/publish and install.sh, run against fixture repos in a temp dir. They live
// outside the product's packages because they test obor-kit, not obor, and behind `//go:build release` (T-1001) so a
// merge's go test ./... leaves them out: `make check-release` and the nightly's release_tests command run them, and the
// release class in .obor/config.json selects them in a lane whose release paths changed.
package release

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The helpers below are copies of the ones cmd/obor's tests share (its shim_test.go and skills_test.go): a helper is
// not exported from the product, so a test tree outside it carries its own.

// shimAsset is the release asset name of version v on this platform (bin/obor's uname mapping is Go's GOOS/GOARCH).
func shimAsset(v string) string { return "obor-" + v + "-" + runtime.GOOS + "-" + runtime.GOARCH }

// treeFile is a file of the kit checkout these tests run in (this package sits two levels down, like cmd/obor).
func treeFile(t *testing.T, rel string) []byte {
	t.Helper()
	return []byte(readFile(t, filepath.Join("..", "..", rel)))
}

func writeExec(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// plainMakeEnv is the test process's environment without the make variables an enclosing make exports into every
// recipe: the gate run as `MAKEFLAGS=' V=0.17.2' make check` (or a test inside `make release V=…`) would otherwise put
// a test's own `make release` on the V= path (T-641). OBOR_RELEASE_LOGGED is the enclosing release's marker too: left in,
// a fixture's `make release` thinks it is already the logged inner run and writes no log (T-937).
func plainMakeEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch name, _, _ := strings.Cut(kv, "="); name {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "V", "DRY", "OBOR_RELEASE_LOGGED":
			continue
		}
		env = append(env, kv)
	}
	return env
}
