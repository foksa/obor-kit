package checks

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The core table of docs/module-map.md lists every production file of internal/app (not its subpackages).
var mapRow = regexp.MustCompile("(?m)^\\| `([A-Za-z0-9_]+\\.go)` \\|")

// mapFileProblems names the files of dir missing from the module map's table, and the table's files not in dir.
func mapFileProblems(doc string, dir []string) []string {
	listed := map[string]bool{}
	for _, m := range mapRow.FindAllStringSubmatch(doc, -1) {
		listed[m[1]] = true
	}
	have := map[string]bool{}
	var out []string
	for _, f := range dir {
		have[f] = true
		if !listed[f] {
			out = append(out, "internal/app/"+f+" is not in the core table of docs/module-map.md")
		}
	}
	for f := range listed {
		if !have[f] {
			out = append(out, "docs/module-map.md lists internal/app/"+f+", which does not exist")
		}
	}
	sort.Strings(out)
	return out
}

func TestModuleMapAppFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	doc, err := os.ReadFile(filepath.Join(root, "docs", "module-map.md"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "internal", "app"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, n)
		}
	}
	if len(files) < 50 || len(mapRow.FindAllString(string(doc), -1)) < 50 {
		t.Fatalf("precondition: %d app files, table rows %d: the scan found too little", len(files), len(mapRow.FindAllString(string(doc), -1)))
	}
	for _, p := range mapFileProblems(string(doc), files) {
		t.Error(p)
	}
}

func TestModuleMapAppFilesNamesBothKinds(t *testing.T) {
	doc := "| `a.go` | x | `A` |\n| `gone.go` | y | `G` |\n"
	got := strings.Join(mapFileProblems(doc, []string{"a.go", "new.go"}), "\n")
	if !strings.Contains(got, "internal/app/new.go is not in the core table") {
		t.Errorf("an unlisted file is not named: %q", got)
	}
	if !strings.Contains(got, "lists internal/app/gone.go, which does not exist") {
		t.Errorf("a listed missing file is not named: %q", got)
	}
	if strings.Contains(got, "a.go is not") || strings.Contains(got, "lists internal/app/a.go") {
		t.Errorf("a file in both was named: %q", got)
	}
}
