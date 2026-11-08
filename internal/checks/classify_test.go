package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
)

// bash classify.test.sh's fixture: classes ui (checks), core (full), tools (checks), notes (no checks), guard
// check_imports, default merge.neutral (docs/**).
func classes(t *testing.T, edit string) config.Merge {
	t.Helper()
	m := config.Merge{Guard: "check_imports"}
	text := `[{"name": "ui", "paths": ["src/ui/**", "e2e/**", "*.md"], "checks": ["check_ui"]},
		{"name": "core", "paths": ["src/engine/**", "src/ai/**"], "full": true},
		{"name": "tools", "paths": ["tools/**"], "checks": ["check_tools"]},
		{"name": "notes", "paths": ["notes/**"]}]`
	if edit != "" {
		text = edit
	}
	if err := json.Unmarshal([]byte(text), &m.Classes); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestClassify(t *testing.T) {
	t.Parallel()
	noMD := `[{"name": "ui", "paths": ["src/ui/**", "e2e/**"], "checks": ["check_ui"]},
		{"name": "tools", "paths": ["tools/**"], "checks": ["check_tools"]}]`
	for _, c := range []struct {
		classes string // "" the fixture's
		edit    func(*config.Merge)
		forced  bool
		files   string
		want    string
	}{
		// bash case_all_ui, case_mixed, case_unmatched, case_full_class
		{"", nil, false, "src/ui/a.ts e2e/flow.spec.ts README.md", "partial:ui"},
		{"", nil, false, "tools/x.sh", "partial:tools"},
		{"", nil, false, "src/ui/a.ts tools/x.sh", "partial:ui+tools"},
		{"", nil, false, "tools/x.sh src/ui/a.ts", "partial:ui+tools"}, // config order, not the files'
		{"", nil, false, "src/ui/a.ts package.json", "full:unmatched package.json"},
		{"", nil, false, "src/ui/a.ts src/engine/e.ts", "full:full-class core (src/engine/e.ts)"},
		{"", nil, false, "notes/x.txt", "full:full-class notes (notes/x.txt)"},
		// bash case_no_guard, case_forced, case_no_changes
		{"", func(m *config.Merge) { m.Guard = "" }, false, "src/ui/a.ts", "full:no-guard"},
		{"", nil, true, "src/ui/a.ts", "full:forced"},
		{"", nil, false, "", "full:no-changes"},
		// bash case_first_match
		{`[{"name": "first", "paths": ["src/**"], "checks": ["c1"]},
			{"name": "ui", "paths": ["*.md"], "checks": ["check_ui"]}, {"name": "notes", "paths": ["notes/**"]}]`,
			func(m *config.Merge) { m.Neutral = []string{} }, false, "src/ui/a.ts src/engine/e.ts", "partial:first"},
		{`[{"name": "ui", "paths": ["*.md"], "checks": ["check_ui"]}, {"name": "notes", "paths": ["notes/**"]}]`,
			func(m *config.Merge) { m.Neutral = []string{} }, false, "notes/a.md", "partial:ui"},
		// bash case_neutral_with_class
		{"", nil, false, "docs/a.md tools/x.sh", "partial:tools"},
		{"", nil, false, "tools/x.sh docs/sub/b.txt docs/a.md", "partial:tools"},
		{"", nil, false, "skills/x/SKILL.md", "partial:ui"},
		{"", nil, false, "tools/x.sh STATUS.md", "partial:ui+tools"},
		{noMD, nil, false, "skills/x/SKILL.md", "full:unmatched skills/x/SKILL.md"},
		{noMD, nil, false, "docs/a.md src/content/help.md", "full:unmatched src/content/help.md"},
		{noMD, nil, false, "docs/a.md", "partial:neutral"},
		// bash case_neutral_only, case_neutral_two_classes
		{"", nil, false, "docs/a.md docs/b.txt docs/sub/c.md", "partial:neutral"},
		{"", nil, true, "docs/a.md", "full:forced"},
		{"", func(m *config.Merge) { m.Guard = "" }, false, "docs/a.md", "full:no-guard"},
		{"", nil, false, "docs/a.md src/ui/a.ts README.md tools/x.sh docs/b.md", "partial:ui+tools"},
		{"", nil, false, "docs/a.md src/engine/e.ts", "full:full-class core (src/engine/e.ts)"},
		{"", nil, false, "README.md package.json", "full:unmatched package.json"},
		// bash case_neutral_custom
		{"", func(m *config.Merge) { m.Neutral = []string{"tools/**"} }, false, "src/ui/a.ts tools/x.sh", "partial:ui"},
		{"", func(m *config.Merge) { m.Neutral = []string{"tools/**"} }, false, "docs/x.txt", "full:unmatched docs/x.txt"},
		{"", func(m *config.Merge) { m.Neutral = []string{"tools/**"} }, false, "tools/x.sh", "partial:neutral"},
		{"", func(m *config.Merge) { m.Neutral = []string{} }, false, "docs/x.txt", "full:unmatched docs/x.txt"},
		{"", func(m *config.Merge) { m.Neutral = []string{} }, false, "README.md tools/x.sh", "partial:ui+tools"},
		// bash case_neutral_class_name
		{`[{"name": "neutral", "paths": ["n/**"], "checks": ["c"]}, {"name": "ui", "paths": ["src/ui/**"], "checks": ["u"]}]`,
			nil, false, "n/a.sh", "full:full-class neutral (n/a.sh)"},
		// bash case_rename_both_paths
		{"", nil, false, "src/engine/a.ts src/ui/a.ts", "full:full-class core (src/engine/a.ts)"},
		{"", nil, false, "src/ui/a.ts src/engine/a.ts", "full:full-class core (src/engine/a.ts)"},
		{"", nil, false, "src/ui/a.ts tools/a.sh", "partial:ui+tools"},
		{"", nil, false, "src/ui/a.ts src/ui/b.ts", "partial:ui"},
		// bash case_duplicate_names: entries are told apart by index
		{`[{"name": "same", "paths": ["src/core/**"], "full": true}, {"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]},
			{"name": "same", "paths": ["lib/**"], "checks": ["lib"]}]`, nil, false, "src/ui/a.ts src/core/a.ts", "full:full-class same (src/core/a.ts)"},
		{`[{"name": "same", "paths": ["src/core/**"], "full": true}, {"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]},
			{"name": "same", "paths": ["lib/**"], "checks": ["lib"]}]`, nil, false, "src/ui/a.ts src/ui/b.ts", "partial:same"},
		{`[{"name": "same", "paths": ["src/core/**"], "full": true}, {"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]},
			{"name": "same", "paths": ["lib/**"], "checks": ["lib"]}]`, nil, false, "src/ui/a.ts lib/a.sh", "partial:same"},
	} {
		m := classes(t, c.classes)
		if c.edit != nil {
			c.edit(&m)
		}
		if got := Classify(m, strings.Fields(c.files), c.forced).Mode(); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.files, got, c.want)
		}
	}
	// T-1008 (RV-139 #2): a forced or no-guard full records its reason and still scans the files, so it carries every
	// class it matched (their with_full checks run) and a later unmatched file does not replace the reason.
	for _, c := range []struct {
		name   string
		edit   func(*config.Merge)
		forced bool
		files  string
		reason string
		names  string
	}{
		{"forced", nil, true, "src/ui/a.ts tools/x.sh", "forced", "ui+tools"},
		{"no-guard", func(m *config.Merge) { m.Guard = "" }, false, "src/ui/a.ts tools/x.sh", "no-guard", "ui+tools"},
		{"forced keeps its reason beside an unmatched file", nil, true, "src/ui/a.ts package.json", "forced", "ui"},
		{"forced with no files", nil, true, "", "forced", ""},
		{"no-changes", nil, false, "", "no-changes", ""},
	} {
		m := classes(t, "")
		if c.edit != nil {
			c.edit(&m)
		}
		got := Classify(m, strings.Fields(c.files), c.forced)
		if got.Partial || got.Reason != c.reason {
			t.Errorf("%s: %s (want full:%s)", c.name, got.Mode(), c.reason)
		}
		if n := strings.Join(got.Names, "+"); n != c.names {
			t.Errorf("%s: names %q, want %q", c.name, n, c.names)
		}
	}

	// a partial class's checks: exactly one entry of that name
	m := classes(t, `[{"name": "same", "paths": ["a/**"], "checks": ["x"]}, {"name": "same", "paths": ["b/**"], "checks": ["y"]},
		{"name": "one", "paths": ["c/**"], "checks": ["z", "w"]}, {"name": "empty", "paths": ["d/**"], "checks": [""]}]`)
	for name, want := range map[string]string{"same": "", "one": "z w", "empty": "", "nope": ""} {
		got, ok := classChecks(m, name)
		if strings.Join(got, " ") != want || ok != (want != "") {
			t.Errorf("classChecks(%s) = %q %v, want %q", name, got, ok, want)
		}
	}
}

// counts: each top-level alternative of merge.summary_regex, summed over the parts of a chained command (axis: a RED
// with only the last part's '3290 pass, 1 fail' hid the others).
func TestCounts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ re, out, want string }{
		{"[0-9]+ pass|[0-9]+ fail", "1 pass\n9 pass\n0 fail\n", "10 pass, 0 fail"},
		{"[0-9]+ pass|[0-9]+ fail", "3290 pass\n1 fail\ntypecheck ok\n12 pass\n0 fail\n", "3302 pass, 1 fail"},
		{"[0-9]+ pass|[0-9]+ fail", "no counts\n", "rc=5"},
		{"", "7 pass\n", "rc=5"},
		{`(ok|done) \(([0-9]+)\)|[0-9]+ skipped`, "ok (2)\ndone (3)\n4 skipped\n", "done (5), 4 skipped"},
		{`Tests: +[0-9]+ passed`, "Tests:  4 passed\n", "Tests:  4 passed"},
	} {
		s := &session{Options: Options{Cfg: &config.Config{Data: config.Data{Merge: config.Merge{SummaryRegex: c.re}}}}}
		if got := s.counts(output{text: c.out, rc: 5}); got != c.want {
			t.Errorf("counts(%q, %q) = %q, want %q", c.re, c.out, got, c.want)
		}
	}
}

// T-901: obor-kit's own merge.classes (bash classify.test.sh case_real_valid, case_real_docs): docs-only merges are
// partial neutral (guard only); Go-only merges partial go and run check_affected (the same heavy gate on the affected
// packages, T-347); skills/, templates/, hooks/, bin/go-commands and .claude-plugin/ partial readers and run
// check_readers; web/src/ and the committed internal/web/dist partial web and run check_web; the kit's release tooling
// (Makefile, scripts/, install.sh, RELEASING.md, the marketplace, tests/release/, T-902) partial release and runs
// check_release; two of them in one merge are partial with both names (config order) and run both checks (T-1003);
// every other file still runs the full check.
func TestOborKitConfig(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../..")
	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Data.Merge
	if got := Classify(m, []string{"docs/tasks/T-75.md", "STATUS.md", "AGENTS.md", "BACKLOG.md"}, false).Mode(); got != "partial:neutral" {
		t.Errorf("docs-only merge: %s", got)
	}
	for _, c := range []struct {
		class, checks string
		files         [][]string
	}{
		{"go", "check_affected", [][]string{
			{"internal/app/loop.go", "internal/app/loop_test.go"},
			{"internal/app/loop.go", "cmd/obor/main.go", "internal/checks/merge.go", "docs/tasks/T-75.md"},
			{"tools/affected/main.go"}, // T-1040: the kit's Go tool is Go code the gate tests
		}},
		{"readers", "check_readers", [][]string{
			{"skills/lead/SKILL.md"}, {"templates/agents/worker.md"}, {"hooks/hooks.json"}, {"bin/go-commands"},
			{".claude-plugin/plugin.json"},
			{"skills/lead/SKILL.md", "templates/agents/worker.md", "bin/go-commands", "docs/tasks/T-75.md"},
		}},
		{"web", "check_web", [][]string{
			{"web/src/lib/format.ts"}, {"internal/web/dist/index.html"},
			{"web/src/pages/Project.svelte", "internal/web/dist/source.sha256"},
		}},
		{"release", "check_release", [][]string{
			{"Makefile"}, {"scripts/release"}, {"install.sh"}, {"RELEASING.md"},
			{".claude-plugin/marketplace.json"}, {"tests/release/release_test.go"},
			{"Makefile", "docs/tasks/T-75.md"},
		}},
	} {
		for _, files := range c.files {
			got := Classify(m, files, false)
			if got.Mode() != "partial:"+c.class {
				t.Errorf("%v: %s (want partial:%s)", files, got.Mode(), c.class)
				continue
			}
			if checks, ok := classChecks(m, got.String()); !ok || strings.Join(checks, " ") != c.checks {
				t.Errorf("%v: class checks %v ok %v (want %s)", files, checks, ok, c.checks)
			}
		}
	}
	// A merge in two classes is partial, named in config order, and runs both classes' checks (T-1003).
	for _, c := range []struct{ files, class, checks string }{
		{"internal/app/loop.go Makefile", "go+release", "check_affected check_release"},
		{"Makefile skills/lead/SKILL.md", "release+readers", "check_release check_readers"},
		{"internal/app/loop.go web/src/lib/format.ts", "web+go", "check_web check_affected"},
	} {
		files := strings.Fields(c.files)
		got := Classify(m, files, false)
		if got.Mode() != "partial:"+c.class {
			t.Errorf("%v: %s (want partial:%s)", files, got.Mode(), c.class)
			continue
		}
		var checks []string
		for _, name := range got.Names {
			cs, ok := classChecks(m, name)
			if !ok {
				t.Errorf("%v: class %s has no unique checks entry", files, name)
				continue
			}
			checks = append(checks, cs...)
		}
		if got := strings.Join(checks, " "); got != c.checks {
			t.Errorf("%v: checks %q, want %q", files, got, c.checks)
		}
	}
	// Anything no class names still runs the full suite, alone and beside a class's file.
	for _, f := range []string{"go.mod", "go.sum", "bin/obor", "githooks/pre-commit",
		"web/package.json", "README.md", "tests/go/socketprobe/main.go"} {
		if c := Classify(m, []string{f}, false); c.Partial {
			t.Errorf("%s: %s (want a full merge)", f, c.Mode())
		}
		if c := Classify(m, []string{"internal/app/loop.go", f, "docs/tasks/T-75.md"}, false); c.Partial {
			t.Errorf("%s beside Go files: %s (want a full merge)", f, c.Mode())
		}
	}
	// A full merge keeps the classes it matched, and release and web mark themselves with_full (T-1007): the kit's own
	// release or web files still run their checks beside an unmatched one; go's do not.
	for _, c := range []struct{ files, name, checks string }{
		{"Makefile go.mod", "release", "check_release"},
		{"web/src/lib/format.ts go.mod", "web", "check_web"},
		{"internal/app/loop.go go.mod", "go", ""},
	} {
		files := strings.Fields(c.files)
		got := Classify(m, files, false)
		if got.Partial || got.Mode() != "full:unmatched go.mod" {
			t.Errorf("%v: %s (want full:unmatched go.mod)", files, got.Mode())
			continue
		}
		if n := strings.Join(got.Names, "+"); n != c.name {
			t.Errorf("%v: full names %q, want %q", files, n, c.name)
		}
		if cs := withFullChecks(m, c.name); strings.Join(cs, " ") != c.checks {
			t.Errorf("%v: with_full checks %q, want %q", files, cs, c.checks)
		}
	}
}

// T-901: READER_PKGS in the Makefile is the survey of the packages whose Go files name a path the readers class maps to
// check_readers (skills/, templates/, hooks/, bin/go-commands, .claude-plugin/): a package that starts reading one and is
// missing there would run no test on a change to it. The survey is a grep, the same one the target's comment records;
// this file is skipped, its tables name the paths without reading them.
func TestOborKitReaders(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../..")
	listed, err := readerPkgs(filepath.Join(repo, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) == 0 {
		t.Fatal("precondition: no READER_PKGS line in the Makefile")
	}
	markers := []string{`"skills"`, `"skills/`, `"templates"`, `"templates/`, `"hooks"`, `"hooks/`, `"go-commands"`, `.claude-plugin`}
	found := map[string]bool{}
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repo, root), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || d.Name() == "classify_test.go" {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			text := string(b)
			for _, m := range markers {
				if !strings.Contains(text, m) {
					continue
				}
				rel, err := filepath.Rel(repo, filepath.Dir(p))
				if err != nil {
					return err
				}
				found["./"+filepath.ToSlash(rel)] = true
				break
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for p := range found {
		if !slices.Contains(listed, p) {
			t.Errorf("%s names a readers path: add it to READER_PKGS, with its line in check-readers' comment", p)
		}
	}
	for _, p := range listed {
		if !found[p] {
			t.Errorf("%s is in READER_PKGS but no Go file under it names a readers path: drop it", p)
		}
	}
}

// readerPkgs is the Makefile's READER_PKGS entries ("./dir" per package), nil when the line is absent.
func readerPkgs(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "READER_PKGS :="); ok {
			return strings.Fields(rest), nil
		}
	}
	return nil, nil
}
