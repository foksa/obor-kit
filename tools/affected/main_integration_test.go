//go:build integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// the tool prints the affected import paths (one per line) of the Go module it runs in, or one
// "full: <why>" line; the list comes from $OBOR_CHANGED_FILES; the -full globs force full. The tool reads no config
// the Makefile passes the globs.
func TestRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fx\n\ngo 1.22\n")
	write("a/a.go", "package a\n\nfunc F() int { return 1 }\n")
	write("a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n")
	write("b/b.go", "package b\n\nimport \"fx/a\"\n\nfunc G() int { return a.F() }\n")
	write("c/c.go", "package c\n")
	list := func(lines ...string) string {
		f := filepath.Join(t.TempDir(), "changed")
		if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	for _, c := range []struct {
		name    string
		args    []string
		changed string
		want    string
	}{
		{"changed file", nil, list("a/a.go"), "fx/a\nfx/b\n"},
		{"test file only", nil, list("a/a_test.go"), "fx/a\n"}, // b imports a, but nothing imports a test file
		{"no Go file", nil, list("README.md"), ""},
		{"go.mod", nil, list("go.mod"), "full: changed go.mod\n"},
		{"full glob", []string{"-full", "build/**"}, list("a/a.go", "build/x.txt"), "full: changed build/x.txt\n"},
		{"full glob not matched", []string{"-full", "build/**"}, list("a/a.go"), "fx/a\nfx/b\n"},
		{"no glob, build file", nil, list("build/x.txt"), ""},
		{"no list", nil, "", "full: OBOR_CHANGED_FILES not set\n"},
	} {
		var out bytes.Buffer
		env := func(name string) string {
			if name == "OBOR_CHANGED_FILES" {
				return c.changed
			}
			return ""
		}
		if rc := run(root, c.args, env, &out); rc != 0 || out.String() != c.want {
			t.Errorf("%s: exit %d, stdout %q (want %q)", c.name, rc, out.String(), c.want)
		}
	}
	// An unreadable list is a full-suite answer too (the reason names the read error).
	var out bytes.Buffer
	missing := filepath.Join(t.TempDir(), "none")
	env := func(name string) string {
		if name == "OBOR_CHANGED_FILES" {
			return missing
		}
		return ""
	}
	if rc := run(root, nil, env, &out); rc != 0 || !strings.HasPrefix(out.String(), "full: ") {
		t.Errorf("unreadable list: exit %d, stdout %q", rc, out.String())
	}
	// An argument is a bad flag (--direct went with the promotion): exit 2, no list.
	out.Reset()
	if rc := run(root, []string{"--direct"}, env, &out); rc != 2 || out.String() != "" {
		t.Errorf("--direct: exit %d, stdout %q; want exit 2 and no list", rc, out.String())
	}
}
