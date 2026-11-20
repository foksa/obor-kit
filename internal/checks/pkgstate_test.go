package checks

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// mutableAllowed are the package variables of a scanned package written outside an init, each "<pkg>.<var>" with
// why: process-wide on purpose, never state one project's read sets for another.
var mutableAllowed = map[string]string{
	"internal/config.Warn":     "where Load's warnings go: a command or test points it elsewhere",
	"internal/config.warned":   "the warn-once set: one warning per key and process",
	"internal/project.markSeq": "a counter that makes each env line's mark unique in the process",
}

// initOnly are package variables written by one function of their package that only an init calls: "<pkg>.<var>" →
// that function (the agents registry: Register, from each compiled kind's init).
var initOnly = map[string]string{"internal/agents.kinds": "Register"}

// syncWrites are the methods that change a sync.Map or a sync/atomic value.
var syncWrites = []string{"Store", "LoadOrStore", "LoadAndDelete", "Delete", "Swap", "CompareAndSwap", "Add", "And",
	"Or", "Clear"}

// TestBaseStateWrittenOnlyInInit: a base package (docs/module-map.md: paths, config, roles, the store, …), and the
// agents package whose registry every layer reads, holds no package variable that code writes outside an init (an
// assignment, ++/--, delete or clear, a sync.Map or atomic write), in its own package or through its import: a read
// (a config load) never changes what another caller of the package sees. mutableAllowed lists the deliberate
// exceptions, initOnly the registry's one writer, which only inits may call. Test files are not scanned (a test may
// point config.Warn at a buffer).
func TestBaseStateWrittenOnlyInInit(t *testing.T) {
	root := filepath.Join("..", "..")
	type pkgVar struct {
		names map[string]bool // the package-level vars
		sync  map[string]bool // those of a sync or sync/atomic type
	}
	bases := map[string]*pkgVar{}
	for pkg, l := range layerOf {
		if l != base && pkg != "internal/agents" {
			continue
		}
		v := &pkgVar{names: map[string]bool{}, sync: map[string]bool{}}
		files, err := goFiles(filepath.Join(root, pkg))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			for _, d := range f.file.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.VAR {
					continue
				}
				for _, s := range g.Specs {
					vs := s.(*ast.ValueSpec)
					for _, n := range vs.Names {
						v.names[n.Name] = true
						if syncType(f.file, vs) {
							v.sync[n.Name] = true
						}
					}
				}
			}
		}
		bases[pkg] = v
	}
	if v := bases["internal/config"]; v == nil || !v.names["Warn"] || !v.sync["warned"] {
		t.Fatalf("precondition: config's Warn and its warn-once sync.Map found, got %+v", v)
	}

	type write struct{ pos, fn string }  // where, in which function of the package ("": another's, or a literal)
	found := map[string][]write{}        // "<pkg>.<var>" → its writes outside an init
	outsideInit := map[string][]string{} // "<pkg>.<func>" of initOnly → the positions that call it outside an init
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		switch {
		case !d.IsDir():
			return nil
		case rel != "." && strings.HasPrefix(d.Name(), "."):
			return filepath.SkipDir
		}
		files, err := goFiles(path)
		if err != nil {
			return err
		}
		own := bases[rel]
		for _, f := range files {
			scanned++
			imports := map[string]string{} // the file's name of a scanned package → its path
			for _, im := range f.file.Imports {
				p, _ := strconv.Unquote(im.Path.Value)
				p = strings.TrimPrefix(p, "obor/")
				if bases[p] == nil {
					continue
				}
				name := filepath.Base(p)
				if im.Name != nil {
					name = im.Name.Name
				}
				imports[name] = p
			}
			top := map[*ast.ValueSpec]bool{}
			for _, d := range f.file.Decls {
				if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
					for _, s := range g.Specs {
						top[s.(*ast.ValueSpec)] = true
					}
				}
			}
			// target is the base package variable e names ("" when none): its own package's, unqualified, or a
			// qualified one of an imported base package; through fields, indexes and derefs.
			target := func(e ast.Expr) (string, bool) {
				for {
					switch x := e.(type) {
					case *ast.ParenExpr:
						e = x.X
					case *ast.StarExpr:
						e = x.X
					case *ast.IndexExpr:
						e = x.X
					case *ast.SelectorExpr:
						if id, ok := x.X.(*ast.Ident); ok && id.Obj == nil {
							if p, ok := imports[id.Name]; ok {
								return p + "." + x.Sel.Name, bases[p].sync[x.Sel.Name]
							}
						}
						e = x.X
					case *ast.Ident:
						if own == nil || !own.names[x.Name] {
							return "", false
						}
						if x.Obj != nil {
							vs, ok := x.Obj.Decl.(*ast.ValueSpec)
							if !ok || !top[vs] {
								return "", false // a local of that name
							}
						}
						return rel + "." + x.Name, own.sync[x.Name]
					default:
						return "", false
					}
				}
			}
			check := func(body ast.Node, fn string) {
				note := func(v string, pos token.Pos) {
					if v == "" {
						return
					}
					w := write{pos: f.fset.Position(pos).String()}
					if strings.HasPrefix(v, rel+".") {
						w.fn = fn
					}
					found[v] = append(found[v], w)
				}
				target1 := func(e ast.Expr) string { v, _ := target(e); return v }
				ast.Inspect(body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.AssignStmt:
						if x.Tok != token.DEFINE {
							for _, l := range x.Lhs {
								note(target1(l), x.Pos())
							}
						}
					case *ast.IncDecStmt:
						note(target1(x.X), x.Pos())
					case *ast.CallExpr:
						if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "delete" || id.Name == "clear") && len(x.Args) > 0 {
							note(target1(x.Args[0]), x.Pos())
						}
						if sel, ok := x.Fun.(*ast.SelectorExpr); ok && slices.Contains(syncWrites, sel.Sel.Name) {
							if v, isSync := target(sel.X); isSync {
								note(v, x.Pos())
							}
						}
						if callee := target1(x.Fun); callee != "" && slices.Contains(slices.Collect(maps.Values(initOnly)),
							callee[strings.LastIndex(callee, ".")+1:]) {
							outsideInit[callee] = append(outsideInit[callee], f.fset.Position(x.Pos()).String())
						}
					}
					return true
				})
			}
			for _, d := range f.file.Decls {
				switch x := d.(type) {
				case *ast.FuncDecl:
					if x.Body != nil && !(x.Recv == nil && x.Name.Name == "init") {
						check(x.Body, x.Name.Name)
					}
				case *ast.GenDecl: // a package-level func literal runs when called, not at init
					for _, s := range x.Specs {
						if vs, ok := s.(*ast.ValueSpec); ok {
							for _, v := range vs.Values {
								ast.Inspect(v, func(n ast.Node) bool {
									if fl, ok := n.(*ast.FuncLit); ok {
										check(fl.Body, "")
										return false
									}
									return true
								})
							}
						}
					}
				}
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
	if len(found["internal/config.warned"]) == 0 || len(found["internal/agents.kinds"]) == 0 {
		t.Fatal("precondition: warnOnce's write of config.warned and Register's of agents.kinds found")
	}
	var vars []string
	for v, ws := range found {
		if _, ok := mutableAllowed[v]; ok {
			continue
		}
		var at []string
		for _, w := range ws {
			if fn, ok := initOnly[v]; !ok || w.fn != fn {
				at = append(at, w.pos)
			}
		}
		if len(at) > 0 {
			vars = append(vars, v)
			found[v] = nil
			for _, p := range at {
				found[v] = append(found[v], write{pos: p})
			}
		}
	}
	sort.Strings(vars)
	for _, v := range vars {
		var at []string
		for _, w := range found[v] {
			at = append(at, w.pos)
		}
		t.Errorf("%s is written outside an init (%s): a base package holds no state a call changes for the next caller "+
			"(pass it, or return it as a value)", v, strings.Join(at, ", "))
	}
	for v, fn := range initOnly {
		callee := v[:strings.LastIndex(v, ".")+1] + fn
		if at := outsideInit[callee]; len(at) > 0 {
			t.Errorf("%s writes %s and is called outside an init (%s): only a kind package's init registers", callee, v,
				strings.Join(at, ", "))
		}
	}
}

// parsed is one non-test Go file of a package, its objects resolved within it.
type parsed struct {
	fset *token.FileSet
	file *ast.File
}

// goFiles are dir's non-test Go files (not its subdirectories').
func goFiles(dir string) ([]parsed, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []parsed
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed{fset, f})
	}
	return out, nil
}

// syncType: the var vs declares is of a sync or sync/atomic type (by its type or its composite literal's), as the
// file imports those packages.
func syncType(f *ast.File, vs *ast.ValueSpec) bool {
	pkgOf := func(e ast.Expr) string {
		if c, ok := e.(*ast.CompositeLit); ok {
			e = c.Type
		}
		if ix, ok := e.(*ast.IndexExpr); ok { // atomic.Pointer[T]
			e = ix.X
		}
		if sel, ok := e.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				return id.Name
			}
		}
		return ""
	}
	names := map[string]bool{}
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if p == "sync" || p == "sync/atomic" {
			n := filepath.Base(p)
			if im.Name != nil {
				n = im.Name.Name
			}
			names[n] = true
		}
	}
	if vs.Type != nil {
		return names[pkgOf(vs.Type)]
	}
	for _, v := range vs.Values {
		if names[pkgOf(v)] {
			return true
		}
	}
	return false
}
