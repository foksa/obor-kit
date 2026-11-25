package checks

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/testkit"
)

// The import boundaries of docs/module-map.md: every obor package has a layer, and a package imports only
// the layers its own layer allows. This table is the enforced copy of the map; change both together.
const (
	base     = "base"    // shared leaf packages: paths, config, roles, spec reader, process runner, version, the store
	bridge   = "bridge"  // what differs per agent CLI (agents and one package per kind): data and pure functions
	core     = "core"    // the app: task state, event log, scheduler rules, watchdogs, adapter interfaces
	adapter  = "adapter" // one outside system each: herdr, git/merge gate, agent CLIs, MCP, Claude Code hooks, setup
	view     = "view"    // the read model (internal/view): joins serve's status, git's lane table and the store
	entry    = "entry"   // cmd/obor: wiring and flags only
	testhelp = "test"    // test helpers
	tool     = "tool"    // standalone programs (probes, spikes)
	kindbin  = "kind"    // a plugin kind's own binary (kinds/<name>): the bridge and its own tree only
	legacy   = "legacy"  // to be moved or deleted (module map table); nothing outside legacy may import it
)

var layerOf = map[string]string{
	"internal/project": base, "internal/config": base, "internal/roles": base, "internal/spec": base,
	"internal/run": base, "internal/version": base, "internal/store": base, "internal/selfexec": base,
	"internal/help": base, "internal/background": base, "internal/progstatus": base, "internal/linestream": base,
	"internal/osnotify": base,
	// a plugin kind runs its command as a process group (project.Group) and caches its manifest: no bridge
	// (data and pure functions); the config builds it through agents.Plugins, setup caches it
	"internal/agents/plugin": base,

	"internal/agents": bridge,

	"internal/app": core,

	"internal/herdr": adapter, "internal/hc": adapter, "internal/rex": adapter, "internal/herdrapp": adapter, "internal/checks": adapter,
	"internal/launch": adapter, "internal/mcp": adapter, "internal/guard": adapter, "internal/setup": adapter,
	"internal/service": adapter,

	"internal/view": view,

	"cmd/obor": entry, "internal/web": entry,

	"internal/testkit": testhelp, "internal/herdr/herdrtest": testhelp, "internal/hc/hctest": testhelp,
	"internal/herdrapp/herdrapptest": testhelp,
}

// layerPrefixes give whole trees a layer: the app may grow subpackages; probes, spikes and the kit's tools are
// standalone.
var layerPrefixes = []struct{ prefix, layer string }{
	{"internal/app/", core},
	{"internal/agents/", bridge},
	{"tests/", tool},
	{"spikes/", tool},
	{"tools/", tool},
	{"kinds/", kindbin},
}

func layer(pkg string) string {
	if l, ok := layerOf[pkg]; ok {
		return l
	}
	for _, p := range layerPrefixes {
		if strings.HasPrefix(pkg, p.prefix) {
			return p.layer
		}
	}
	return ""
}

// allowed: may a package of layer `from` import pkg `to` of layer `toLayer`? Adapters reach each other only through
// core interfaces; nobody but legacy, entry and test code imports legacy. Every layer may import a bridge; a bridge
// imports no obor package but the agents package itself (its kinds implement agents.Kind). The view reads base, core
// and the adapters (the lane table, the restart lock); only entry (and test) packages import it, so every reader of
// project state goes through view.Build.
func allowed(fromPkg, from, toPkg, to string) bool {
	switch {
	case from == bridge:
		return toPkg == "internal/agents"
	case to == bridge:
		return true
	}
	// The herdr-app window is hc's second surface: the hc adapter drives it, and the conformance static tier checks
	// its command contract against the installed app. No other package imports it (docs/module-map.md).
	if toPkg == "internal/herdrapp" {
		return fromPkg == "internal/hc" || from == testhelp || strings.HasPrefix(fromPkg, "tests/")
	}
	switch from {
	case entry, testhelp, legacy:
		return true
	case kindbin:
		root := strings.Join(strings.SplitN(fromPkg, "/", 3)[:2], "/")
		return toPkg == root || strings.HasPrefix(toPkg, root+"/")
	case base, tool:
		return to == base
	case core:
		return to == base || to == core
	case view:
		return to == base || to == core || to == adapter
	case adapter:
		return to == base || to == core || strings.HasPrefix(toPkg, fromPkg+"/")
	}
	return false
}

func TestBoundaryRules(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"internal/app", "internal/config", true},
		{"internal/app", "internal/herdr", false},
		{"internal/app", "internal/checks", false},
		{"internal/app/store", "internal/app", true},
		{"internal/herdr", "internal/app/port", true},
		{"internal/app/port", "internal/herdr", false},
		{"internal/app/event", "internal/store", true},
		{"internal/app/review", "internal/app/outbox", true},
		{"internal/view", "internal/app/event", true},
		{"internal/app", "internal/store", true},
		{"internal/store", "internal/app", false},
		{"internal/store", "internal/version", true},
		{"internal/checks", "internal/app", true},
		{"internal/checks", "internal/run", true},
		{"internal/checks", "internal/launch", false},
		{"internal/launch", "internal/herdr", false},
		{"internal/herdr", "internal/herdr/herdrtest", true},
		{"internal/hc", "internal/app/port", true},
		{"internal/hc", "internal/agents", true},
		{"internal/hc", "internal/hc/hctest", true},
		{"internal/hc", "internal/herdr", false},
		{"internal/app", "internal/hc", false},
		{"internal/launch", "internal/hc", false},
		{"internal/hc/hctest", "internal/hc", true},
		{"internal/hc", "internal/herdrapp", true},
		{"internal/app", "internal/herdrapp", false},
		{"internal/checks", "internal/herdrapp", false},
		{"internal/launch", "internal/herdrapp", false},
		{"tests/conformance", "internal/herdrapp", true},
		{"internal/herdrapp/herdrapptest", "internal/herdrapp", true},
		{"internal/herdrapp", "internal/project", true},
		{"internal/herdrapp", "internal/hc", false},
		{"internal/rex", "internal/app/port", true},
		{"internal/rex", "internal/linestream", true},
		{"internal/rex", "internal/hc", false},
		{"internal/hc", "internal/rex", false},
		{"internal/app", "internal/rex", false},
		{"internal/launch", "internal/rex", false},
		{"internal/linestream", "internal/app/port", false},
		{"internal/hc", "internal/app/shellpane", true},
		{"internal/rex", "internal/app/shellpane", true},
		{"internal/app/shellpane", "internal/rex", false},
		{"internal/guard", "internal/launch", false},
		{"internal/setup", "internal/herdr", false},
		{"internal/setup", "internal/launch", false},
		{"internal/config", "internal/app", false},
		{"internal/project", "internal/herdr", false},
		{"cmd/obor", "internal/guard", true},
		{"tests/go/socketprobe", "internal/herdr", false},
		{"internal/agents/claude", "internal/agents", true},
		{"internal/agents/claude", "internal/agents/codex", false},
		{"internal/agents", "internal/config", false},
		{"kinds/opencode", "kinds/opencode/kind", true},
		{"kinds/opencode/kind", "internal/agents", true},
		{"kinds/opencode/kind", "internal/project", false},
		{"kinds/opencode", "kinds/other", false},
		{"internal/testkit", "kinds/opencode/kind", true},
		{"internal/config", "internal/agents", true},
		{"internal/launch", "internal/agents/claude", true},
		{"internal/app", "internal/agents", true},
		{"internal/view", "internal/app", true},
		{"internal/view", "internal/checks", true},
		{"internal/view", "internal/store", true},
		{"cmd/obor", "internal/view", true},
		{"internal/app", "internal/view", false},
		{"internal/checks", "internal/view", false},
		{"internal/mcp", "internal/view", false},
		{"internal/store", "internal/view", false},
	} {
		if got := allowed(c.from, layer(c.from), c.to, layer(c.to)); got != c.want {
			t.Errorf("allowed(%s -> %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// TestImportBoundaries: `go list -deps -json ./...` over the module; non-test imports only.
func TestImportBoundaries(t *testing.T) {
	pkgs := listPackages(t)
	for _, want := range []string{"internal/checks", "internal/project", "cmd/obor"} {
		if _, ok := pkgs[want]; !ok {
			t.Fatalf("go list did not report obor/%s (got %d obor packages)", want, len(pkgs))
		}
	}
	var names []string
	for p := range pkgs {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, from := range names {
		fl := layer(from)
		if fl == "" {
			t.Errorf("obor/%s has no layer: add it to docs/module-map.md and layerOf", from)
			continue
		}
		for _, to := range pkgs[from] {
			edge := from + " -> " + to
			if allowed(from, fl, to, layer(to)) {
				continue
			}
			t.Errorf("forbidden import %s (%s may not import %s; docs/module-map.md)", edge, fl, layerName(to))
		}
	}
}

func layerName(pkg string) string {
	if l := layer(pkg); l != "" {
		return l
	}
	return "an unmapped package"
}

// TestOneTerminalBuilder: only cmd/obor/terminal.go (non-test) builds a terminal adapter by importing internal/herdr,
// internal/hc or internal/rex; the conformance suite and each adapter itself (with its fake) may import it too. Every other
// package reaches the terminal through port.Terminal.
func TestOneTerminalBuilder(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || rel == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		if rel == "cmd/obor/terminal.go" || strings.HasPrefix(rel, "tests/conformance/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		scanned++
		for _, imp := range f.Imports {
			p := strings.TrimPrefix(strings.Trim(imp.Path.Value, `"`), "obor/")
			if (p == "internal/herdr" || p == "internal/hc" || p == "internal/rex") && !strings.HasPrefix(rel, p+"/") {
				t.Errorf("%s imports %s: only cmd/obor/terminal.go builds the terminal (port.Terminal)", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d Go files: not the module", scanned)
	}
}

// TestAppTestsNameTheRig: internal/app's flow tests (its external test files, package app_test) reach the terminal
// through the rig only, and the rig's terminal is testkit.Term: a scenario stated as port.Pane, port.Event and a call
// of a port operation is one every adapter has to meet, and one stated in an adapter's wire is not. The three
// boundary files prove a real adapter itself. Any other external test file, the rig's own among them, that imports a
// terminal adapter or its fake fails (an in-package test is the core's own, over fakePanes and the unit tables).
func TestAppTestsNameTheRig(t *testing.T) {
	dir := filepath.Join("..", "app")
	allowed := map[string]bool{
		"restore_integration_test.go": true, // herdr: serve's restore on the real adapter
		"restorehc_ext_test.go":       true, // hc's
		"adopthc_ext_test.go":         true, // hc's
	}
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	terminal := func(pkg string) bool {
		for _, p := range []string{"internal/herdr", "internal/hc", "internal/herdrapp"} {
			if pkg == p || strings.HasPrefix(pkg, p+"/") {
				return true
			}
		}
		return false
	}
	scanned, rigs := 0, 0
	for _, path := range files {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name.Name != "app_test" { // an in-package test is the core's own (fakePanes, the unit tables)
			continue
		}
		scanned++
		if strings.HasPrefix(filepath.Base(path), "rig_") {
			rigs++
		}
		if allowed[filepath.Base(path)] {
			continue
		}
		for _, imp := range f.Imports {
			pkg := strings.TrimPrefix(strings.Trim(imp.Path.Value, `"`), "obor/")
			if terminal(pkg) {
				t.Errorf("%s imports %s: a flow test names the rig, not a terminal adapter or its fake; "+
					"the rig's terminal is testkit.Term", path, pkg)
			}
		}
	}
	if scanned < 50 || rigs < 5 {
		t.Fatalf("scanned %d external test files, %d of them the rig's: not internal/app's", scanned, rigs)
	}
}

// listPackages maps each obor package (path without "obor/") to its obor imports.
func listPackages(t *testing.T) map[string][]string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "list", "-buildvcs=false", "-deps", "-json", "./...") // no VCS stamp: no git
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	pkgs := map[string][]string{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list: %v", err)
		}
		from, ok := strings.CutPrefix(p.ImportPath, "obor/")
		if !ok {
			continue
		}
		var imps []string
		for _, imp := range p.Imports {
			if to, ok := strings.CutPrefix(imp, "obor/"); ok {
				imps = append(imps, to)
			}
		}
		pkgs[from] = imps
	}
	return pkgs
}

// kindLiteralFree are the trees that may name an agent kind as a string literal: the bridges, a plugin kind's own binary
// (kinds/<name>), the test fakes, the upgrade model list with each kind's base args (internal/config, data only) and
// obor init's starter model list (internal/setup, data only).
var kindLiteralFree = []string{"internal/agents/", "kinds/", "internal/herdr/herdrtest/", "internal/testkit/",
	"internal/config/models.go", "internal/setup/models.go"}

// TestNoKindLiterals: per-kind behaviour lives in the bridges (docs/module-map.md "bridge"), so no Go file
// outside them, the fakes and tests holds a kind's name as a string literal: an adapter asks the project's kinds
// (config.Config.Kind) instead. The kinds are testkit.Kinds' (a new kind is checked with no edit here: the compiled
// ones and the kit's plugin kinds). Comments and longer strings ("codex exec") do not count.
func TestNoKindLiterals(t *testing.T) {
	root := filepath.Join("..", "..")
	kinds := map[string]bool{}
	for _, n := range testkit.Kinds().Names() {
		kinds[n] = true
	}
	if !kinds["claude"] || len(kinds) < 3 {
		t.Fatalf("precondition: every kind, got %v", testkit.Kinds().Names())
	}
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || rel == "tests" || rel == "spikes") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		for _, p := range kindLiteralFree {
			if strings.HasPrefix(rel, p) {
				return nil
			}
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		fset := token.NewFileSet()
		var sc scanner.Scanner
		sc.Init(fset.AddFile(rel, -1, len(src)), src, nil, 0)
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			if v, err := strconv.Unquote(lit); tok == token.STRING && err == nil && kinds[v] {
				t.Errorf("%s: the kind literal %s outside internal/agents: ask the config's kinds", fset.Position(pos), lit)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned %d Go files: not the module", scanned)
	}
}
