package hc_test

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

	"obor/internal/hc"
)

// schema is the committed copy of hc 0.1.21's `hc schema` (OpenRPC), as far as the checks read it.
type schema struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Methods []struct {
		Name   string `json:"name"`
		Params []struct {
			Name string `json:"name"`
		} `json:"params"`
	} `json:"methods"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"schemas"`
	} `json:"components"`
}

func readSchema(t *testing.T) schema {
	t.Helper()
	b, err := os.ReadFile("testdata/hc-0.1.21.openrpc.json")
	if err != nil {
		t.Fatal(err)
	}
	var s schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Info.Version != hc.MinVersion || len(s.Methods) < 30 {
		t.Fatalf("precondition: the schema copy is hc %s with %d methods, want hc %s's 32", s.Info.Version, len(s.Methods), hc.MinVersion)
	}
	return s
}

// Every method obor sends is in hc 0.1.21's schema, with every param obor puts in it.
func TestSchemaHasSent(t *testing.T) {
	s := readSchema(t)
	params := map[string][]string{}
	for _, m := range s.Methods {
		params[m.Name] = []string{}
		for _, p := range m.Params {
			params[m.Name] = append(params[m.Name], p.Name)
		}
	}
	for method, ps := range hc.Sent {
		have, ok := params[method]
		if !ok {
			t.Errorf("hc 0.1.21 has no method %s", method)
			continue
		}
		for _, p := range ps {
			if !slices.Contains(have, p) {
				t.Errorf("hc 0.1.21's %s has no param %s (it has %q)", method, p, have)
			}
		}
	}
}

// Every result field obor reads is in hc 0.1.21's schema, in the component that holds it.
func TestSchemaHasRead(t *testing.T) {
	s := readSchema(t)
	for comp, fields := range hc.Read {
		c, ok := s.Components.Schemas[comp]
		if !ok {
			t.Errorf("hc 0.1.21 has no component %s", comp)
			continue
		}
		for _, f := range fields {
			if _, ok := c.Properties[f]; !ok {
				t.Errorf("hc 0.1.21's %s has no field %s", comp, f)
			}
		}
	}
}

// envelope are the keys of a JSON-RPC request line itself, not params.
var envelope = []string{"jsonrpc", "id", "method", "params"}

// Sent names exactly the methods the package's non-test files send ((*session).call's and (*Client).call's string
// literal) and every string key of their params maps (map[string]any literals) is a param Sent lists or the
// envelope's.
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
	methods, keys := map[string]bool{}, map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					// (*session).call(ctx, method, params, result) and (*Client).call(ctx, result, method, params): the
					// method is the first string literal argument.
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "call" {
						for _, a := range n.Args {
							if m, ok := lit(a); ok {
								methods[m] = true
								break
							}
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
	var sent []string
	for m := range hc.Sent {
		sent = append(sent, m)
	}
	var called []string
	for m := range methods {
		called = append(called, m)
	}
	sort.Strings(sent)
	sort.Strings(called)
	if !slices.Equal(sent, called) {
		t.Errorf("methods called %q, Sent %q: keep them equal", called, sent)
	}
	for k := range keys {
		known := slices.Contains(envelope, k)
		for _, ps := range hc.Sent {
			known = known || slices.Contains(ps, k)
		}
		if !known {
			t.Errorf("param %q is sent but in no Sent entry", k)
		}
	}
	if !keys["terminal"] || !keys["generation"] {
		t.Errorf("precondition: the scan found the params maps, got %v", keys)
	}
}
