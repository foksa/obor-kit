package affected

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture module fx: top → mid → leaf; tester imports leaf from its tests only; other stands alone (with an embedded
// file); leaf has a testdata/ file.
// fullGlobs are obor-kit's merge.affected_full.
var fullGlobs = []string{"Makefile", "internal/testkit/**", "internal/herdrtest/**", "internal/herdr/herdrtest/**"}

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
	for _, c := range []struct{ files, want, full string }{
		{"leaf/leaf.go", leafAndImporters, ""},           // a leaf: it plus its importers, direct, indirect and test-only
		{"leaf/leaf_test.go", leafAndImporters, ""},      // a test file only: its package plus importers
		{"leaf/testdata/in.txt", leafAndImporters, ""},   // testdata/: its package
		{"top/top.go", "fx/top", ""},                     // nothing imports top
		{"tester/tester_test.go", "fx/tester", ""},       // a test-only importer is not imported back
		{"other/tmpl/x.txt", "fx/other", ""},             // a non-Go file in a package dir (embedded) selects it
		{"top/top.go docs/a.md README.md", "fx/top", ""}, // files under no package select nothing
		{"README.md docs/a.md", "", ""},                  // no Go files: empty
		{"leaf/leaf.go go.mod", "", "changed go.mod"},
		{"go.sum", "", "changed go.sum"},
		{"Makefile", "", "changed Makefile"},
		{"top/top.go internal/testkit/tk.go", "", "changed internal/testkit/tk.go"},
		{"internal/herdrtest/h.go", "", "changed internal/herdrtest/h.go"},
		{"internal/herdr/herdrtest/fake.go", "", "changed internal/herdr/herdrtest/fake.go"},
		{"internal/other/x.go", "", "no package for internal/other/x.go"},
		{"gone/gone.go", "", "no package for gone/gone.go"}, // a removed package
	} {
		res, err := Go(root, strings.Fields(c.files), fullGlobs)
		if err != nil {
			t.Fatalf("%s: %v", c.files, err)
		}
		if got := strings.Join(res.Pkgs, " "); got != c.want || res.Full != c.full {
			t.Errorf("%s: pkgs %q full %q, want %q full %q", c.files, got, res.Full, c.want, c.full)
		}
	}
}

func TestGoBrokenModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := Go(root, []string{"a/a.go"}, nil); err == nil {
		t.Error("no go.mod: want an error (the Makefile then runs the full suite)")
	}
}

// This repo, read-only (T-347 spec): a guard change does not select app; an app change selects cmd/obor but not the
// packages below app (spec, project). internal/setup IS selected by an app change: setup imports herdr, which imports
// app.
func TestGoOborKit(t *testing.T) {
	t.Parallel()
	root, _ := filepath.Abs("../..")
	for _, c := range []struct {
		file      string
		with, not []string
	}{
		{"internal/guard/guard.go", []string{"obor/internal/guard", "obor/cmd/obor"}, []string{"obor/internal/app"}},
		{"internal/app/loop.go", []string{"obor/internal/app", "obor/cmd/obor", "obor/internal/setup"},
			[]string{"obor/internal/spec", "obor/internal/project", "obor/internal/roles", "obor/internal/version",
				"obor/internal/herdr"}},
		// T-690: herdr imports the ports leaf, not app: a port change selects it, an app change does not.
		{"internal/app/port/effects.go", []string{"obor/internal/app/port", "obor/internal/app", "obor/internal/herdr"}, nil},
	} {
		res, err := Go(root, []string{c.file}, fullGlobs)
		if err != nil || res.Full != "" {
			t.Fatalf("%s: full %q err %v", c.file, res.Full, err)
		}
		pkgs := res.Pkgs
		for _, p := range c.with {
			if !slices.Contains(pkgs, p) {
				t.Errorf("%s: %s not selected (%v)", c.file, p, pkgs)
			}
		}
		for _, p := range c.not {
			if slices.Contains(pkgs, p) {
				t.Errorf("%s: %s selected (%v)", c.file, p, pkgs)
			}
		}
		t.Logf("%s: %s", c.file, strings.Join(pkgs, " "))
	}
}

// T-351: merge.affected_full is the project's list; with none configured a Makefile or testkit change selects packages
// as any other file, while go.mod stays full.
func TestGoFullGlobsAreConfig(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	res, err := Go(root, []string{"internal/testkit/tk.go"}, nil)
	if err != nil || res.Full != "" || strings.Join(res.Pkgs, " ") != "fx/internal/testkit" {
		t.Errorf("no globs: testkit change got %+v err %v, want just its own package, fx/internal/testkit", res, err)
	}
	res, err = Go(root, []string{"leaf/leaf.go"}, []string{"leaf/*.go"})
	if err != nil || res.Full != "changed leaf/leaf.go" {
		t.Errorf("a configured glob: got %+v err %v, want full", res, err)
	}
	if res, _ := Go(root, []string{"go.mod"}, nil); res.Full != "changed go.mod" {
		t.Errorf("go.mod without globs: got %+v, want full", res)
	}
}
