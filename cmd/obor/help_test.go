package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"

	"obor/internal/help"
)

// dispatched is the subcommands main's switch on sub dispatches (its case strings; -h and --help are help's aliases).
func dispatched(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "sub" {
			return true
		}
		for _, st := range sw.Body.List {
			for _, e := range st.(*ast.CaseClause).List {
				lit, ok := e.(*ast.BasicLit)
				if !ok {
					t.Fatalf("main.go: a case of the switch on sub is not a string literal: %T", e)
				}
				s, _ := strconv.Unquote(lit.Value)
				if !strings.HasPrefix(s, "-") {
					names = append(names, s)
				}
			}
		}
		return false
	})
	return names
}

// T-854: every subcommand main dispatches has a row in help.Commands and every row is dispatched: a removed command
// cannot stay listed, a new one cannot be missing. Each row's group is one of help.Groups and its usage names it.
func TestHelpTableMatchesDispatch(t *testing.T) {
	t.Parallel()
	got := dispatched(t)
	for _, want := range []string{"help", "whoami", "hook", "version"} {
		if !slices.Contains(got, want) {
			t.Fatalf("precondition: main.go's dispatch %v lacks %q", got, want)
		}
	}
	rows := map[string]bool{}
	for _, c := range help.Commands {
		if rows[c.Name] {
			t.Errorf("help.Commands lists %q twice", c.Name)
		}
		rows[c.Name] = true
		if !slices.Contains(got, c.Name) {
			t.Errorf("help.Commands lists %q, which main.go does not dispatch", c.Name)
		}
		if !slices.Contains(help.Groups, c.Group) {
			t.Errorf("%s: group %q is not one of %v", c.Name, c.Group, help.Groups)
		}
		if !strings.HasPrefix(c.Usage, "obor "+c.Name) || c.Desc == "" {
			t.Errorf("%s: usage %q (want it to start with obor %s), desc %q", c.Name, c.Usage, c.Name, c.Desc)
		}
	}
	for _, name := range got {
		if !rows[name] {
			t.Errorf("main.go dispatches %q, which help.Commands does not list", name)
		}
	}
}

// T-854: `obor help` (the binary) prints every group and every row, its usage line and its description, exit 0.
func TestHelpPrintsEveryGroupAndRow(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"help", "--help"} {
		rc, stdout, _ := run(t, t.TempDir(), nil, arg)
		if rc != 0 || !strings.HasPrefix(stdout, "usage: obor <subcommand>") {
			t.Fatalf("obor %s: exit %d, %q", arg, rc, stdout)
		}
		for _, g := range help.Groups {
			if !strings.Contains(stdout, "\n"+g+":\n") {
				t.Errorf("obor %s: no group %q:\n%s", arg, g, stdout)
			}
		}
		for _, c := range help.Commands {
			if !strings.Contains(stdout, c.Usage+"\n") || !strings.Contains(stdout, "  "+c.Name) ||
				!strings.Contains(stdout, c.Desc+"\n") {
				t.Errorf("obor %s: no row %s (%q, %q)", arg, c.Name, c.Usage, c.Desc)
			}
		}
	}
	// gone commands stay gone (they were in the shim's old list)
	_, stdout, _ := run(t, t.TempDir(), nil, "help")
	for _, gone := range []string{"answer", "sendback"} {
		if strings.Contains(stdout, "  "+gone+" ") {
			t.Errorf("obor help lists %q, a removed command", gone)
		}
	}
}
