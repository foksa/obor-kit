//go:build integration

package main

import (
	"os"
	"path/filepath"
	"testing"

	"obor/internal/testkit"
)

// T-351: obor affected go prints the affected import paths (one per line) of the Go module it runs in, or one
// "full: <why>" line; the list comes from --changed or $OBOR_CHANGED_FILES; merge.affected_full globs force full.
func TestAffectedGoCommand(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config(func(c map[string]any) { c["merge"] = map[string]any{"affected_full": []string{"build/**"}} })
	p.Write("go.mod", "module fx\n\ngo 1.22\n")
	p.Write("a/a.go", "package a\n\nfunc F() int { return 1 }\n")
	p.Write("a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n")
	p.Write("b/b.go", "package b\n\nimport \"fx/a\"\n\nfunc G() int { return a.F() }\n")
	p.Write("c/c.go", "package c\n")
	list := func(lines ...string) string {
		f := filepath.Join(t.TempDir(), "changed")
		text := ""
		for _, l := range lines {
			text += l + "\n"
		}
		if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	for _, c := range []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{"--changed", []string{"affected", "go", "--changed", list("a/a.go")}, nil, "fx/a\nfx/b\n"},
		{"--direct", []string{"affected", "go", "--direct", "--changed", list("a/a.go")}, nil, "fx/a\n"}, // T-991: no importer b
		{"--direct after --changed", []string{"affected", "go", "--changed", list("a/a.go"), "--direct"}, nil, "fx/a\n"},
		{"--direct from env", []string{"affected", "go", "--direct"}, []string{"OBOR_CHANGED_FILES=" + list("a/a.go")}, "fx/a\n"},
		{"--direct affected_full", []string{"affected", "go", "--direct", "--changed", list("a/a.go", "build/x.txt")}, nil, "full: changed build/x.txt\n"},
		{"env default", []string{"affected", "go"}, []string{"OBOR_CHANGED_FILES=" + list("c/c.go", "README.md")}, "fx/c\n"},
		{"test file only", []string{"affected", "go", "--changed", list("a/a_test.go")}, nil, "fx/a\n"}, // T-954: b imports a, but nothing imports a test file
		{"no Go file", []string{"affected", "go", "--changed", list("README.md")}, nil, ""},
		{"go.mod", []string{"affected", "go", "--changed", list("go.mod")}, nil, "full: changed go.mod\n"},
		{"merge.affected_full", []string{"affected", "go", "--changed", list("a/a.go", "build/x.txt")}, nil, "full: changed build/x.txt\n"},
		{"unreadable list", []string{"affected", "go", "--changed", filepath.Join(t.TempDir(), "none")}, nil, ""},
		{"no list", []string{"affected", "go"}, []string{"OBOR_CHANGED_FILES="}, "full: OBOR_CHANGED_FILES not set\n"},
	} {
		rc, stdout, stderr := run(t, p.Root, c.env, c.args...)
		if c.name == "unreadable list" {
			if rc != 0 || len(stdout) < 6 || stdout[:6] != "full: " {
				t.Errorf("%s: exit %d, stdout %q", c.name, rc, stdout)
			}
			continue
		}
		if rc != 0 || stdout != c.want || stderr != "" {
			t.Errorf("%s: exit %d, stdout %q (want %q), stderr %q", c.name, rc, stdout, c.want, stderr)
		}
	}
	if rc, _, _ := run(t, p.Root, nil, "affected", "rust"); rc != 2 {
		t.Errorf("affected rust: exit %d, want 2", rc)
	}
	if rc, _, _ := run(t, p.Root, nil, "affected", "go", "--bogus"); rc != 2 {
		t.Errorf("affected go --bogus: exit %d, want 2", rc)
	}
	if rc, _, _ := run(t, p.Root, nil, "has", "affected", "go"); rc != 0 {
		t.Errorf("obor has affected go: exit %d, want 0", rc)
	}
}
