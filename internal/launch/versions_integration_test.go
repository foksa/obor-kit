//go:build integration

package launch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// T-1176: CLIVersions runs each kind's CLI found on env's PATH with --version and keeps its first line. Stub
// executables only: the one found and answering has an entry; a CLI that hangs is cut at its timeout, and one that
// fails, prints nothing or is not on PATH has none.
func TestCLIVersions(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	args := filepath.Join(t.TempDir(), "args")
	long := strings.Repeat("v", 200)
	for name, body := range map[string]string{
		"claude":  `printf '%s\n' "$*" > '` + args + `'; echo '2.1.322 (Claude Code)'`,
		"codex":   `echo; echo '  codex-cli 0.167.0  '; echo 'a second line'`,
		"slow":    `sleep 30; echo 'slow 1.0'`,
		"fails":   `echo 'fails 1.0'; exit 1`,
		"silent":  `echo 'only on stderr' >&2`,
		"lengthy": `echo '` + long + `'`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "noexec"), []byte("#!/bin/sh\necho 'noexec 1.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(key string) (string, bool) {
		if key == "PATH" {
			return bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin", true
		}
		return "", false
	}
	// The answering stubs get a generous bound: macOS checks a new executable on its first run, which takes a while.
	start := time.Now()
	got := cliVersions(context.Background(), env,
		[]string{"claude", "codex", "fails", "silent", "lengthy", "noexec", "missing"}, 20*time.Second)
	t.Logf("seven kinds, five of them run for the first time: %s", time.Since(start))
	want := map[string]string{"claude": "2.1.322 (Claude Code)", "codex": "codex-cli 0.167.0", "lengthy": long[:versionChars]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("versions %q, want %q", got, want)
	}
	if b, err := os.ReadFile(args); err != nil || string(b) != "--version\n" {
		t.Errorf("the CLI was run with %q (%v), want --version alone", b, err)
	}

	start = time.Now()
	again := cliVersions(context.Background(), env, []string{"claude", "codex"}, 20*time.Second)
	t.Logf("two answering stubs, run before: %s", time.Since(start))
	if len(again) != 2 {
		t.Errorf("two answering stubs: %q, want both", again)
	}

	// slow sleeps 30 s and then answers: the read ends at its timeout with no entry (the upper bound leaves room for
	// a loaded machine).
	const timeout = 300 * time.Millisecond
	start = time.Now()
	hung := cliVersions(context.Background(), env, []string{"slow"}, timeout)
	took := time.Since(start)
	if len(hung) != 0 {
		t.Errorf("a CLI that does not answer in its timeout: %q, want no entry", hung)
	}
	if took < timeout || took > 15*time.Second {
		t.Errorf("the hanging read took %s, want at least its timeout %s and far less than the CLI's 30 s", took, timeout)
	}
	t.Logf("one hanging kind, timeout %s: %s", timeout, took)
}
