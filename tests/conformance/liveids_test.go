package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// liveIDFiles are the files of each kind's live tier (the static tier's rows sit elsewhere).
var liveIDFiles = map[string]string{
	"herdr": "live_herdr.go", "claude": "live_claude.go", "codex": "live_codex.go",
	"opencode": "live_opencode.go", "agy": "live_agy.go",
}

// A kind's live tier reports only ids liveIDs names, so a SKIP line when the kind cannot run still names every case.
func TestLiveIDsHoldEveryReportedID(t *testing.T) {
	for kind, file := range liveIDFiles {
		want := liveIDs[kind]
		if len(want) == 0 {
			t.Fatalf("%s: no live ids", kind)
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				return true
			}
			if strings.HasSuffix(fd.Name.Name, "Static") { // a static row: no live SKIP
				return false
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "report" {
					return true
				}
				lit, ok := call.Args[1].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				id, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if !slices.Contains(want, id) {
					t.Errorf("%s: %s's live row %s is not in liveIDs[%s]", file, fd.Name.Name, id, kind)
				}
				return true
			})
			return false
		})
	}
}
