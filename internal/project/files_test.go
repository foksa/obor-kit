package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// T-1052: one PATH search for setup and launch (project.LookPath), as exec.LookPath reads it: dirs in order, an empty
// component is the cwd, a directory named like the binary or a file without an execute bit does not match.
func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "codex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := LookPath(dir, "codex"); err != nil || got != exe {
		t.Errorf("an executable: %q, %v; want %q", got, err, exe)
	}

	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "codex"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := LookPath(plain, "codex"); !errors.Is(err, exec.ErrNotFound) || got != "" {
		t.Errorf("a non-executable file: %q, %v; want not found", got, err)
	}

	named := t.TempDir()
	if err := os.Mkdir(filepath.Join(named, "codex"), 0o755); err != nil { // an executable directory
		t.Fatal(err)
	}
	if got, err := LookPath(named, "codex"); !errors.Is(err, exec.ErrNotFound) || got != "" {
		t.Errorf("a directory named like the binary: %q, %v; want not found", got, err)
	}

	// An empty component is the cwd (exec.LookPath's rule), here the second one.
	t.Chdir(dir)
	path := filepath.Join(t.TempDir(), "nothing") + string(os.PathListSeparator)
	if got, err := LookPath(path, "codex"); err != nil || got != "codex" {
		t.Errorf("an empty PATH component: %q, %v; want the cwd's codex", got, err)
	}
}

// T-1049: xattr's com.apple.quarantine value carries its flags in hex before the first ';' (the time, agent and uuid
// follow): the user-approved bit's value reads as 0x01c1, and a value with no hex flags is no flag.
func TestQuarantineFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out    string
		flags  uint32
		wantOK bool
	}{
		{"0181;6af071dc;;d3b9-11ee\n", 0x0181, true},
		{"01c1;6af071dc;Safari;d3b9-11ee\n", 0x01c1, true},
		{"0083;\n", 0x0083, true},
		{"", 0, false},
		{"zz;6af071dc\n", 0, false},
	} {
		flags, ok := QuarantineFlags(tc.out)
		if flags != tc.flags || ok != tc.wantOK {
			t.Errorf("QuarantineFlags(%q) = %#x, %v; want %#x, %v", tc.out, flags, ok, tc.flags, tc.wantOK)
		}
	}
}
