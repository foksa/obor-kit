package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture module fx: top → mid → leaf; tester imports leaf from its tests only; other stands alone (with an embedded
// file); leaf has a testdata/ file; cmd/app is a main package with a test.
// fixtureFull is the fixture's -full globs (the Makefile's): a build file and the shared test-support packages.
var fixtureFull = []string{"Makefile", "internal/testkit/**", "internal/herdrtest/**"}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":                  "module fx\n\ngo 1.22\n",
		"README.md":               "fx\n",
		"leaf/leaf.go":            "package leaf\n\nfunc F() int { return 1 }\n",
		"leaf/leaf_test.go":       "package leaf\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n",
		"leaf/testdata/in.txt":    "in\n",
		"mid/mid.go":              "package mid\n\nimport \"fx/leaf\"\n\nfunc G() int { return leaf.F() }\n",
		"top/top.go":              "package top\n\nimport \"fx/mid\"\n\nfunc H() int { return mid.G() }\n",
		"tester/tester.go":        "package tester\n",
		"tester/tester_test.go":   "package tester_test\n\nimport (\n\t\"testing\"\n\n\t\"fx/leaf\"\n)\n\nfunc TestT(t *testing.T) { _ = leaf.F() }\n",
		"other/other.go":          "package other\n\nimport _ \"embed\"\n\n//go:embed tmpl/x.txt\nvar X string\n",
		"other/tmpl/x.txt":        "x\n",
		"internal/testkit/tk.go":  "package testkit\n",
		"internal/herdrtest/h.go": "package herdrtest\n",
		"cmd/app/app.go":          "package main\n\nfunc main() {}\n",
		"cmd/app/app_test.go":     "package main\n\nimport \"testing\"\n\nfunc TestApp(t *testing.T) {}\n",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGoFixture(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	leafAndImporters := "fx/leaf fx/mid fx/tester fx/top"
	for _, c := range []struct {
		files, want, full string
		globs             []string
	}{
		{"leaf/leaf.go", leafAndImporters, "", fixtureFull},                   // a leaf: it plus its importers, direct, indirect and test-only
		{"leaf/leaf_test.go", "fx/leaf", "", fixtureFull},                     // a test file only selects its own package
		{"leaf/leaf.go leaf/leaf_test.go", leafAndImporters, "", fixtureFull}, // a non-test file in the change still expands
		{"leaf/testdata/in.txt", leafAndImporters, "", fixtureFull},           // testdata/: its package
		{"top/top.go", "fx/top", "", fixtureFull},                             // nothing imports top
		{"tester/tester_test.go", "fx/tester", "", fixtureFull},               // a changed test file picks its package, not the leaf its test imports
		{"cmd/app/app_test.go", "fx/cmd/app", "", fixtureFull},                // a main package's test file selects it alone
		{"other/tmpl/x.txt", "fx/other", "", fixtureFull},                     // a non-Go file in a package dir (embedded) selects it
		{"top/top.go docs/a.md README.md", "fx/top", "", fixtureFull},         // files under no package select nothing
		{"/abs/x.go", "", "", fixtureFull},                                    // an absolute path is skipped, not walked to "/"
		{"README.md docs/a.md", "", "", fixtureFull},                          // no Go files: empty
		{"leaf/leaf.go go.mod", "", "changed go.mod", fixtureFull},
		{"go.sum", "", "changed go.sum", fixtureFull},
		{"Makefile", "", "changed Makefile", fixtureFull},
		{"top/top.go internal/testkit/tk.go", "", "changed internal/testkit/tk.go", fixtureFull},
		{"internal/herdrtest/h.go", "", "changed internal/herdrtest/h.go", fixtureFull},
		{"internal/testkit/nested/tk.go", "", "changed internal/testkit/nested/tk.go", fixtureFull}, // a path under a full glob, no package lookup
		{"internal/other/x.go", "", "no package for internal/other/x.go", fixtureFull},
		{"gone/gone.go", "", "no package for gone/gone.go", fixtureFull}, // a removed package
		// the -full globs are the project's list; with none the testkit change selects its own
		// package, while go.mod stays full.
		{"internal/testkit/tk.go", "fx/internal/testkit", "", nil},
		{"leaf/leaf.go", "", "changed leaf/leaf.go", []string{"leaf/*.go"}},
		{"go.mod", "", "changed go.mod", nil},
	} {
		res, err := Go(root, strings.Fields(c.files), c.globs, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.files, err)
		}
		if got := strings.Join(res.Pkgs, " "); got != c.want || res.Full != c.full {
			t.Errorf("%s (globs %v): pkgs %q full %q, want %q full %q", c.files, c.globs, got, res.Full, c.want, c.full)
		}
	}
}

// -reads: a changed non-Go file selects the packages that read it, themselves only (not their importers), once beside
// the packages a changed Go file selects; no match leaves the list as it was.
func TestGoReads(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	reads := []Read{
		{Glob: "skills/**", Dirs: []string{"cmd/app", "top"}},
		{Glob: "README.md", Dirs: []string{"cmd/app"}},
		{Glob: "data/*.json", Dirs: []string{"leaf"}},
	}
	for _, c := range []struct {
		files, want, full string
	}{
		{"skills/lead/SKILL.md", "fx/cmd/app fx/top", ""},
		{"skills/lead/SKILL.md skills/setup/SKILL.md", "fx/cmd/app fx/top", ""},                 // two matching files: each reader once
		{"leaf/leaf.go skills/lead/SKILL.md", "fx/cmd/app fx/leaf fx/mid fx/tester fx/top", ""}, // beside the Go change's importers; top once
		{"README.md", "fx/cmd/app", ""},                                                         // README.md: cmd/app
		{"README.md skills/lead/SKILL.md", "fx/cmd/app fx/top", ""},                             // two globs naming cmd/app: once
		{"data/x.json", "fx/leaf", ""},                                                          // the reader only: mid, top, tester import it and stay out
		{"docs/a.md", "", ""},                                                                   // no match: nothing, as without -reads
		{"top/top.go docs/a.md", "fx/top", ""},                                                  // no match beside Go: the Go list alone
		{"skills/lead/SKILL.md go.mod", "", "changed go.mod"},                                   // a full change still wins
		{"gone/gone.go skills/lead/SKILL.md", "", "no package for gone/gone.go"},                // so does a removed package
	} {
		res, err := Go(root, strings.Fields(c.files), fixtureFull, reads)
		if err != nil {
			t.Fatalf("%s: %v", c.files, err)
		}
		if got := strings.Join(res.Pkgs, " "); got != c.want || res.Full != c.full {
			t.Errorf("%s: pkgs %q full %q, want %q full %q", c.files, got, res.Full, c.want, c.full)
		}
	}
	// A dir that holds no package is an error (the Makefile then runs the full suite), never a silent skip.
	if _, err := Go(root, []string{"skills/a.md"}, nil, []Read{{Glob: "skills/**", Dirs: []string{"nopkg"}}}); err == nil {
		t.Error("a -reads dir with no package: want an error")
	}
	// An entry no changed file matches is not asked: its missing dir is no error.
	if _, err := Go(root, []string{"docs/a.md"}, nil, []Read{{Glob: "skills/**", Dirs: []string{"nopkg"}}}); err != nil {
		t.Errorf("an unmatched -reads entry names a missing dir: %v (want none: it is not asked)", err)
	}
}

func TestReadFlag(t *testing.T) {
	t.Parallel()
	var r readFlag
	for _, ok := range []string{"skills/**=cmd/obor", "githooks/**=internal/setup,cmd/obor"} {
		if err := r.Set(ok); err != nil {
			t.Errorf("Set(%q): %v", ok, err)
		}
	}
	if got := r.String(); got != "skills/**=cmd/obor githooks/**=internal/setup,cmd/obor" {
		t.Errorf("parsed %q", got)
	}
	for _, bad := range []string{"skills/**", "=cmd/obor", "skills/**=", "skills/**=a,,b"} {
		if err := r.Set(bad); err == nil {
			t.Errorf("Set(%q): want an error", bad)
		}
	}
}

func TestGoBrokenModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := Go(root, []string{"a/a.go"}, nil, nil); err == nil {
		t.Error("no go.mod: want an error (the Makefile then runs the full suite)")
	}
}
