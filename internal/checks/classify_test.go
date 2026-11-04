package checks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{"", nil, false, "src/ui/a.ts tools/x.sh", "full:mixed ui/tools (tools/x.sh)"},
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
		{"", nil, false, "tools/x.sh STATUS.md", "full:mixed tools/ui (STATUS.md)"},
		{noMD, nil, false, "skills/x/SKILL.md", "full:unmatched skills/x/SKILL.md"},
		{noMD, nil, false, "docs/a.md src/content/help.md", "full:unmatched src/content/help.md"},
		{noMD, nil, false, "docs/a.md", "partial:neutral"},
		// bash case_neutral_only, case_neutral_two_classes
		{"", nil, false, "docs/a.md docs/b.txt docs/sub/c.md", "partial:neutral"},
		{"", nil, true, "docs/a.md", "full:forced"},
		{"", func(m *config.Merge) { m.Guard = "" }, false, "docs/a.md", "full:no-guard"},
		{"", nil, false, "docs/a.md src/ui/a.ts README.md tools/x.sh docs/b.md", "full:mixed ui/tools (tools/x.sh)"},
		{"", nil, false, "docs/a.md src/engine/e.ts", "full:full-class core (src/engine/e.ts)"},
		{"", nil, false, "README.md package.json", "full:unmatched package.json"},
		// bash case_neutral_custom
		{"", func(m *config.Merge) { m.Neutral = []string{"tools/**"} }, false, "src/ui/a.ts tools/x.sh", "partial:ui"},
		{"", func(m *config.Merge) { m.Neutral = []string{"tools/**"} }, false, "docs/x.txt", "full:unmatched docs/x.txt"},
		{"", func(m *config.Merge) { m.Neutral = []string{"tools/**"} }, false, "tools/x.sh", "partial:neutral"},
		{"", func(m *config.Merge) { m.Neutral = []string{} }, false, "docs/x.txt", "full:unmatched docs/x.txt"},
		{"", func(m *config.Merge) { m.Neutral = []string{} }, false, "README.md tools/x.sh", "full:mixed ui/tools (tools/x.sh)"},
		// bash case_neutral_class_name
		{`[{"name": "neutral", "paths": ["n/**"], "checks": ["c"]}, {"name": "ui", "paths": ["src/ui/**"], "checks": ["u"]}]`,
			nil, false, "n/a.sh", "full:full-class neutral (n/a.sh)"},
		// bash case_rename_both_paths
		{"", nil, false, "src/engine/a.ts src/ui/a.ts", "full:full-class core (src/engine/a.ts)"},
		{"", nil, false, "src/ui/a.ts src/engine/a.ts", "full:full-class core (src/engine/a.ts)"},
		{"", nil, false, "src/ui/a.ts tools/a.sh", "full:mixed ui/tools (tools/a.sh)"},
		{"", nil, false, "src/ui/a.ts src/ui/b.ts", "partial:ui"},
		// bash case_duplicate_names: entries are told apart by index
		{`[{"name": "same", "paths": ["src/core/**"], "full": true}, {"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]},
			{"name": "same", "paths": ["lib/**"], "checks": ["lib"]}]`, nil, false, "src/ui/a.ts src/core/a.ts", "full:full-class same (src/core/a.ts)"},
		{`[{"name": "same", "paths": ["src/core/**"], "full": true}, {"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]},
			{"name": "same", "paths": ["lib/**"], "checks": ["lib"]}]`, nil, false, "src/ui/a.ts src/ui/b.ts", "partial:same"},
		{`[{"name": "same", "paths": ["src/core/**"], "full": true}, {"name": "same", "paths": ["src/ui/**"], "checks": ["ui"]},
			{"name": "same", "paths": ["lib/**"], "checks": ["lib"]}]`, nil, false, "src/ui/a.ts lib/a.sh", "full:mixed same/same (lib/a.sh)"},
	} {
		m := classes(t, c.classes)
		if c.edit != nil {
			c.edit(&m)
		}
		if got := Classify(m, strings.Fields(c.files), c.forced).Mode(); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.files, got, c.want)
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

// obor-kit's own config (bash classify.test.sh case_real_valid, case_real_docs): it loads and validates, docs-only
// merges are partial neutral, Go-only merges are partial go and run check_affected (the same heavy gate on the
// affected packages, T-347), and every other code path runs the full check.
func TestOborKitConfig(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../..")
	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, fails := cfg.Validate(config.ValidateOptions{}); len(fails) > 0 {
		t.Errorf("obor config validate: %v", fails)
	}
	check, ok := cfg.Command("check")
	if check.Cmd != "make -s check" || !check.Heavy || !ok {
		t.Errorf("commands.check: %+v (want make -s check, heavy)", check)
	}
	if c, ok := cfg.Command("check_affected"); c.Cmd != "make -s check-affected" || !c.Heavy || c.Timeout != check.Timeout || !ok {
		t.Errorf("commands.check_affected: %+v (want make -s check-affected, heavy, check's timeout %d)", c, check.Timeout)
	}
	m := cfg.Data.Merge
	if got := Classify(m, []string{"docs/tasks/T-75.md", "STATUS.md", "AGENTS.md"}, false).Mode(); got != "partial:neutral" {
		t.Errorf("docs-only merge: %s", got)
	}
	for _, files := range [][]string{{"internal/app/loop.go", "internal/app/loop_test.go"},
		{"internal/app/loop.go", "cmd/obor/main.go", "internal/checks/merge.go", "docs/tasks/T-75.md"}} {
		c := Classify(m, files, false)
		if c.Mode() != "partial:go" {
			t.Errorf("%v: %s (want partial:go)", files, c.Mode())
			continue
		}
		if got, ok := classChecks(m, c.Name); !ok || strings.Join(got, " ") != "check_affected" {
			t.Errorf("%v: class checks %v ok %v (want check_affected)", files, got, ok)
		}
	}
	// Every other code path (alone or with Go files) runs the full heavy gate.
	for _, f := range []string{"Makefile", "go.mod", "go.sum", "bin/obor", "hooks/hooks.json", "githooks/pre-commit",
		"skills/lead/SKILL.md", "templates/agents/worker.md", "tests/go/socketprobe/main.go"} {
		if c := Classify(m, []string{"internal/app/loop.go", f, "docs/tasks/T-75.md"}, false); c.Partial {
			t.Errorf("%s: %s (want a full merge)", f, c.Mode())
		}
	}
	if got := Classify(m, []string{"internal/app/loop.go", "Makefile"}, false).Mode(); got != "full:unmatched Makefile" {
		t.Errorf("Makefile with Go files: %s (want full:unmatched Makefile)", got)
	}
	// T-817: the kit's nightly commands, in order: the conformance tier, then the Go build cache trim.
	if got := strings.Join(m.NightlyCommands, " "); got != "conformance trim_go_cache" {
		t.Errorf("merge.nightly_commands: %q (want \"conformance trim_go_cache\")", got)
	}
	trim, ok := cfg.Command("trim_go_cache")
	if trim.Cmd != "make -s trim-go-cache" || trim.Timeout != 600 || !ok {
		t.Errorf("commands.trim_go_cache: %+v (want make -s trim-go-cache, timeout 600)", trim)
	}
}

// T-817: commands.trim_go_cache runs make trim-go-cache, which deletes the Go build cache's entries unused for 48 h
// and prints "go cache: freed <size>, <size> left". GOCACHE names a throwaway cache: a test never trims the machine's.
func TestOborKitTrimGoCache(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../..")
	cache := t.TempDir()
	env := append(os.Environ(), "GOCACHE="+cache)
	// Precondition: the target trims the cache this test made, never the machine's — it reads GOCACHE with `go env`.
	probe := exec.Command("go", "env", "GOCACHE")
	probe.Env = env
	got, err := probe.Output()
	if err != nil {
		t.Skipf("no go to ask for GOCACHE: %v", err)
	}
	if want := cache; strings.TrimSpace(string(got)) != want {
		t.Fatalf("precondition: go env GOCACHE = %q, want the test's %q", got, want)
	}
	// cacheFile is a cache entry of size bytes, unused for age.
	cacheFile := func(name string, size int64, age time.Duration) string {
		t.Helper()
		path := filepath.Join(cache, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, size); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil { // after the truncate: it sets the mtime
			t.Fatal(err)
		}
		return path
	}
	old := cacheFile("aa/aa-1-a", 3<<20, 72*time.Hour)
	fresh := cacheFile("bb/bb-1-a", 2<<20, time.Hour)
	for _, p := range []string{old, fresh} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("precondition: %v", err)
		}
	}
	cmd := exec.Command("make", "-s", "-C", repo, "trim-go-cache")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make trim-go-cache: %v\n%s", err, out)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("an entry unused for 72 h is still there (%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("an entry unused for an hour is gone: %v", err)
	}
	if want := "go cache: freed 3.0 MiB, 2.0 MiB left"; !strings.Contains(string(out), want) {
		t.Errorf("output %q lacks %q", out, want)
	}
}
