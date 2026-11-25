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

// describe is the committed copy of `rex api describe --json` for the methods the client calls.
type describe struct {
	APIRevision int `json:"api_revision"`
	Methods     []struct {
		Invoke struct {
			Method string `json:"method"`
		} `json:"invoke"`
		Input  *schemaNode `json:"input_schema"`
		Output *schemaNode `json:"output_schema"`
	} `json:"methods"`
}

func readDescribe(t *testing.T) map[string][2]*schemaNode {
	t.Helper()
	b, err := os.ReadFile("testdata/rex-api-18.describe.json")
	if err != nil {
		t.Fatal(err)
	}
	var d describe
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	out := map[string][2]*schemaNode{}
	for _, m := range d.Methods {
		out[m.Invoke.Method] = [2]*schemaNode{m.Input, m.Output}
	}
	if d.APIRevision != APIRevision || len(out) != len(Sent)+1 {
		t.Fatalf("precondition: the copy is API revision %d with %d methods, want revision %d with Sent's %d and block.method",
			d.APIRevision, len(out), APIRevision, len(Sent))
	}
	return out
}

// Every method obor calls is in the copy, with every param obor puts in it: a block method's session_id, block_id
// and args are block.method's, and its own input takes no params (obor sends args {}).
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
			if in != nil && len(in.Properties) > 0 {
				t.Errorf("%s takes params %v; obor sends args {}", method, in.Properties)
			}
			in = d["block.method"][0]
		}
		for _, p := range params {
			if in == nil || in.Properties[p] == nil {
				t.Errorf("Rex %d's %s has no param %s", APIRevision, method, p)
			}
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
