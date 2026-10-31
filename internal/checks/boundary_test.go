package checks

import (
	"context"
	"encoding/json"
	"errors"
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

	"obor/internal/agents"
)

// The import boundaries of docs/module-map.md (T-317): every obor package has a layer, and a package imports only
// the layers its own layer allows. This table is the enforced copy of the map; change both together.
const (
	base     = "base"    // shared leaf packages: paths, config, roles, spec reader, process runner, version, the store
	bridge   = "bridge"  // what differs per agent CLI (agents and one package per kind): data and pure functions
	core     = "core"    // the app: task state, event log, scheduler rules, watchdogs, adapter interfaces
	adapter  = "adapter" // one outside system each: herdr, git/merge gate, agent CLIs, MCP, Claude Code hooks, setup
	entry    = "entry"   // cmd/obor: wiring and flags only
	testhelp = "test"    // test helpers
	tool     = "tool"    // standalone programs (probes, spikes)
	legacy   = "legacy"  // to be moved or deleted (module map table); nothing outside legacy may import it
)

var layerOf = map[string]string{
	"internal/project": base, "internal/config": base, "internal/roles": base, "internal/spec": base,
	"internal/run": base, "internal/version": base, "internal/store": base,

	"internal/agents": bridge,

	"internal/app": core,

	"internal/herdr": adapter, "internal/checks": adapter, "internal/launch": adapter, "internal/mcp": adapter,
	"internal/guard": adapter, "internal/setup": adapter, "internal/service": adapter,

	"cmd/obor": entry, "internal/affected": adapter,

	"internal/testkit": testhelp, "internal/herdr/herdrtest": testhelp,
}

// layerPrefixes give whole trees a layer: the app may grow subpackages; probes and spikes are standalone.
var layerPrefixes = []struct{ prefix, layer string }{
	{"internal/app/", core},
	{"internal/agents/", bridge},
	{"tests/", tool},
	{"spikes/", tool},
}

// boundaryAllowlist is today's violations ("<from> -> <to>", paths without "obor/"). It may only shrink: an entry
// that no longer occurs fails the test until it is removed.
var boundaryAllowlist = map[string]bool{
	"internal/setup -> internal/herdr":  true, // pane labels and plugin registration; through core (phase 1)
	"internal/setup -> internal/launch": true, // `obor setup agent` starts an agent; through core (phase 2)
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
// imports no obor package but the agents package itself (its kinds implement agents.Kind).
func allowed(fromPkg, from, toPkg, to string) bool {
	switch {
	case from == bridge:
		return toPkg == "internal/agents"
	case to == bridge:
		return true
	}
	switch from {
	case entry, testhelp, legacy:
		return true
	case base, tool:
		return to == base
	case core:
		return to == base || to == core
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
		{"internal/app", "internal/store", true},
		{"internal/store", "internal/app", false},
		{"internal/store", "internal/version", true},
		{"internal/checks", "internal/app", true},
		{"internal/checks", "internal/run", true},
		{"internal/checks", "internal/launch", false},
		{"internal/launch", "internal/herdr", false},
		{"internal/herdr", "internal/herdr/herdrtest", true},
		{"internal/guard", "internal/launch", false},
		{"internal/config", "internal/app", false},
		{"internal/project", "internal/herdr", false},
		{"cmd/obor", "internal/guard", true},
		{"tests/go/socketprobe", "internal/herdr", false},
		{"internal/agents/claude", "internal/agents", true},
		{"internal/agents/claude", "internal/agents/codex", false},
		{"internal/agents", "internal/config", false},
		{"internal/agents/opencode", "internal/project", false},
		{"internal/config", "internal/agents", true},
		{"internal/launch", "internal/agents/claude", true},
		{"internal/app", "internal/agents", true},
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
	seen := map[string]bool{}
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
			seen[edge] = true
			if !boundaryAllowlist[edge] {
				t.Errorf("forbidden import %s (%s may not import %s; docs/module-map.md)", edge, fl, layerName(to))
			}
		}
	}
	for edge := range boundaryAllowlist {
		if !seen[edge] {
			t.Errorf("allowlisted %s no longer occurs: remove it from boundaryAllowlist (it may only shrink)", edge)
		}
	}
}

func layerName(pkg string) string {
	if l := layer(pkg); l != "" {
		return l
	}
	return "an unmapped package"
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
	cmd := exec.CommandContext(ctx, goBin, "list", "-deps", "-json", "./...")
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

// kindLiteralFree are the trees that may name an agent kind as a string literal: the bridges, and the test fakes.
var kindLiteralFree = []string{"internal/agents/", "internal/herdr/herdrtest/", "internal/testkit/"}

// TestNoKindLiterals (T-495): per-kind behaviour lives in the bridges (docs/module-map.md "bridge"), so no Go file
// outside them, the fakes and tests holds a registered kind's name as a string literal: an adapter asks agents.Lookup
// instead. The kinds are agents.Names() (DD-v0.13 B1: a new kind is checked with no edit here; the test binary
// registers every kind through testkit). Comments and longer strings ("codex exec") do not count.
func TestNoKindLiterals(t *testing.T) {
	root := filepath.Join("..", "..")
	kinds := map[string]bool{}
	for _, n := range agents.Names() {
		kinds[n] = true
	}
	if !kinds["claude"] || len(kinds) < 3 {
		t.Fatalf("precondition: every kind registered, got %v", agents.Names())
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
				t.Errorf("%s: the kind literal %s outside internal/agents: ask agents.Lookup", fset.Position(pos), lit)
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
