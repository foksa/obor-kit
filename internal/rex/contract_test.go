package rex

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// schemaNode is a JSON schema as far as the checks read it: an object's properties, an array's items.
type schemaNode struct {
	Properties map[string]*schemaNode `json:"properties"`
	Items      *schemaNode            `json:"items"`
}

// describe is the committed copy of `rex api describe --json` for the methods the client calls (a block method's
// own args in args_schema), with the shell flavor's options from block_creator.list.
type describe struct {
	APIRevision int `json:"api_revision"`
	Methods     []struct {
		Invoke struct {
			Method string `json:"method"`
		} `json:"invoke"`
		Input  *schemaNode `json:"input_schema"`
		Args   *schemaNode `json:"args_schema"`
		Output *schemaNode `json:"output_schema"`
	} `json:"methods"`
	Flavors []struct {
		Flavor  string      `json:"flavor"`
		Options *schemaNode `json:"options_schema"`
	} `json:"flavors"`
}

// readDescribe is the copy by method: its input (a block method's: its args), its output; and the shell flavor's
// options.
func readDescribe(t *testing.T) map[string][2]*schemaNode {
	t.Helper()
	d := readCopy(t)
	out := map[string][2]*schemaNode{}
	for _, m := range d.Methods {
		in := m.Input
		if m.Args != nil {
			in = m.Args
		}
		out[m.Invoke.Method] = [2]*schemaNode{in, m.Output}
	}
	if d.APIRevision != APIRevision || len(out) != len(Sent)+1 {
		t.Fatalf("precondition: the copy is API revision %d with %d methods, want revision %d with Sent's %d and block.method",
			d.APIRevision, len(out), APIRevision, len(Sent))
	}
	return out
}

func readCopy(t *testing.T) describe {
	t.Helper()
	b, err := os.ReadFile("testdata/rex-api-19.describe.json")
	if err != nil {
		t.Fatal(err)
	}
	var d describe
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// schemaHas: node holds each of fields, a dotted one inside its parent's properties.
func schemaHas(node *schemaNode, field string) bool {
	for _, step := range strings.Split(field, ".") {
		if node == nil {
			return false
		}
		node = node.Properties[step]
	}
	return node != nil
}

// Every method obor calls is in the copy, with every param obor puts in it: a block method's session_id, block_id
// and args are block.method's, and its own args (Args) are in its args schema; the shell flavor's options obor sets
// (Options) and block.event's event fields (Event) are in theirs.
func TestSchemaHasSent(t *testing.T) {
	d := readDescribe(t)
	for method, params := range Sent {
		s, ok := d[method]
		if !ok {
			t.Errorf("Rex %d has no method %s", APIRevision, method)
			continue
		}
		in := s[0]
		if strings.HasPrefix(method, Terminal+".") {
			for _, a := range Args[method] {
				if !schemaHas(in, a) {
					t.Errorf("Rex %d's %s takes no arg %s", APIRevision, method, a)
				}
			}
			in = d["block.method"][0]
		}
		for _, p := range params {
			if !schemaHas(in, p) {
				t.Errorf("Rex %d's %s has no param %s", APIRevision, method, p)
			}
		}
	}
	for _, f := range Event {
		if !schemaHas(d["block.event"][0], "event."+f) {
			t.Errorf("Rex %d's block.event has no event field %s", APIRevision, f)
		}
	}
	c := readCopy(t)
	if len(c.Flavors) != 1 || c.Flavors[0].Flavor != ShellFlavor {
		t.Fatalf("precondition: the copy's flavors %+v, want %s's options", c.Flavors, ShellFlavor)
	}
	for _, o := range Options {
		if !schemaHas(c.Flavors[0].Options, o) {
			t.Errorf("Rex %d's %s takes no option %s", APIRevision, ShellFlavor, o)
		}
	}
}

// Every result field obor reads is in the copy, at the path that holds it.
func TestSchemaHasRead(t *testing.T) {
	d := readDescribe(t)
	for method, paths := range Read {
		s, ok := d[method]
		if !ok {
			t.Errorf("Rex %d has no method %s", APIRevision, method)
			continue
		}
		for path, fields := range paths {
			node := s[1]
			if path != "" {
				for _, step := range strings.Split(path, ".") {
					name, each := strings.CutSuffix(step, "[]")
					if node = node.Properties[name]; node != nil && each {
						node = node.Items
					}
					if node == nil {
						break
					}
				}
			}
			if node == nil {
				t.Errorf("Rex %d's %s has no %s", APIRevision, method, path)
				continue
			}
			for _, f := range fields {
				if node.Properties[f] == nil {
					t.Errorf("Rex %d's %s has no field %s in %q", APIRevision, method, f, path)
				}
			}
		}
	}
}

// Sent names exactly the methods the package's non-test files call (call's and callStarted's method argument: a
// string literal, or Terminal + a literal; call passing its own method on aside), and every string key of their params maps (map[string]any literals) is a
// param Sent lists.
func TestSentCoversCallSites(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	lit := func(e ast.Expr) (string, bool) {
		b, ok := e.(*ast.BasicLit)
		if !ok || b.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(b.Value)
		return v, err == nil
	}
	method := func(e ast.Expr) (string, bool) {
		if bin, ok := e.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
			if id, ok := bin.X.(*ast.Ident); ok && id.Name == "Terminal" {
				s, ok := lit(bin.Y)
				return Terminal + s, ok
			}
		}
		return lit(e)
	}
	methods, keys := map[string]bool{}, map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					// (*Client).call(ctx, out, method, params) and callStarted(ctx, out, method, params, started).
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "call" || sel.Sel.Name == "callStarted") && len(n.Args) >= 3 {
						if m, ok := method(n.Args[2]); ok {
							methods[m] = true
						} else if id, ok := n.Args[2].(*ast.Ident); !ok || id.Name != "method" { // call forwards its own
							t.Errorf("%s: a call whose method is no literal", fset.Position(n.Pos()))
						}
					}
				case *ast.CompositeLit:
					if mt, ok := n.Type.(*ast.MapType); ok {
						if k, ok := mt.Key.(*ast.Ident); ok && k.Name == "string" {
							if v, ok := mt.Value.(*ast.Ident); ok && v.Name == "any" {
								for _, e := range n.Elts {
									if kv, ok := e.(*ast.KeyValueExpr); ok {
										if s, ok := lit(kv.Key); ok {
											keys[s] = true
										}
									}
								}
							}
						}
					}
				}
				return true
			})
		}
	}
	var sent, called []string
	for m := range Sent {
		sent = append(sent, m)
	}
	for m := range methods {
		called = append(called, m)
	}
	sort.Strings(sent)
	sort.Strings(called)
	if !slices.Equal(sent, called) {
		t.Errorf("methods called %q, Sent %q: keep them equal", called, sent)
	}
	for k := range keys {
		known := false
		for _, ps := range Sent {
			known = known || slices.Contains(ps, k)
		}
		if !known {
			t.Errorf("param %q is sent but in no Sent entry", k)
		}
	}
	if !keys["session_id"] || !keys["block_id"] {
		t.Errorf("precondition: the scan found the params maps, got %v", keys)
	}
}
