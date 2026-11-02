package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/affected"
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
		{"env default", []string{"affected", "go"}, []string{"OBOR_CHANGED_FILES=" + list("c/c.go", "README.md")}, "fx/c\n"},
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
	if rc, _, _ := run(t, p.Root, nil, "has", "affected", "go"); rc != 0 {
		t.Errorf("obor has affected go: exit %d, want 0", rc)
	}
}

// T-351: what `make check-affected` selects, here on this repo's own tree and config: an internal/app change selects
// the packages that import it (cmd/obor, herdr, queue) and none below it (T-347); and merge.affected_full in
// this tree's config lists the build and test-support paths T-347 hard-coded. affected.Go on the tree itself, not the
// binary: the binary reads the main checkout's config (project.Find), which a lane or the merge scratch does not
// control (T-573: an unmigrated main failed this test). TestAffectedGoCommand covers the command.
func TestAffectedGoOborKit(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	res, err := affected.Go(root, []string{"internal/app/loop.go"}, nil)
	if err != nil || res.Full != "" {
		t.Fatalf("affected.Go: %+v, %v; want a package list", res, err)
	}
	got, stdout := res.Pkgs, strings.Join(res.Pkgs, "\n")
	for _, p := range []string{"obor/internal/app", "obor/cmd/obor", "obor/internal/setup"} {
		if !slices.Contains(got, p) {
			t.Errorf("%s not selected by an internal/app change:\n%s", p, stdout)
		}
	}
	for _, p := range []string{"obor/internal/spec", "obor/internal/project", "obor/internal/roles", "obor/internal/version",
		"obor/internal/herdr"} {
		if slices.Contains(got, p) {
			t.Errorf("%s selected by an internal/app change:\n%s", p, stdout)
		}
	}
	// The ports leaf (T-690): herdr imports it, not app, so a port change selects herdr and an app change does not.
	pres, err := affected.Go(root, []string{"internal/app/port/effects.go"}, nil)
	if err != nil || pres.Full != "" {
		t.Fatalf("affected.Go port: %+v, %v; want a package list", pres, err)
	}
	for _, p := range []string{"obor/internal/app/port", "obor/internal/herdr", "obor/internal/app"} {
		if !slices.Contains(pres.Pkgs, p) {
			t.Errorf("%s not selected by an internal/app/port change:\n%s", p, strings.Join(pres.Pkgs, "\n"))
		}
	}
	// project.Find resolves a lane to the main checkout's config, so read this tree's own file.
	var cfg struct {
		Merge struct {
			AffectedFull []string `json:"affected_full"`
		} `json:"merge"`
	}
	if err := json.Unmarshal(treeFile(t, ".obor/config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"Makefile", "internal/testkit/**", "internal/herdrtest/**", "internal/herdr/herdrtest/**"} {
		if !slices.Contains(cfg.Merge.AffectedFull, g) {
			t.Errorf("merge.affected_full lacks %s: %v", g, cfg.Merge.AffectedFull)
		}
	}
}
