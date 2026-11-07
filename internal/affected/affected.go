// Package affected selects the Go packages a change can break (T-347, T-351): the
// packages of the changed files plus every package whose imports or test imports reach them. A changed test file
// selects only its own package (T-954): no other package imports it. `obor affected go` prints them for a project's
// check_affected command.
package affected

import (
	"bytes"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"obor/internal/spec"
)

// Result is what to test: Full non-empty means the whole suite (the reason), else Pkgs (sorted import paths; none: no
// package is affected).
type Result struct {
	Pkgs []string
	Full string
}

// Go returns the packages the changed files (repo-relative, as in OBOR_CHANGED_FILES) affect in the module at root. A
// changed go.mod, go.sum or file matching one of fullGlobs (merge.affected_full) forces the full suite. A file maps to
// the package of its nearest directory that holds one (a testdata/ or embedded file selects its package); a file under
// no package selects nothing, except a .go file (a removed package), which forces full. A changed _test.go file selects
// its directory's package only, never the packages that import it: a test binary is built from that directory alone.
func Go(root string, files, fullGlobs []string) (Result, error) {
	for _, f := range files {
		if f == "go.mod" || f == "go.sum" {
			return Result{Full: "changed " + f}, nil
		}
		for _, g := range fullGlobs {
			if spec.Match(f, g) {
				return Result{Full: "changed " + f}, nil
			}
		}
	}
	list, err := goList(root)
	if err != nil {
		return Result{}, err
	}
	byDir := map[string]string{} // package dir (module-relative, "." for the root) → import path
	for _, p := range list {
		if p.ForTest == "" && !strings.Contains(p.ImportPath, " [") {
			byDir[p.Rel] = p.ImportPath
		}
	}
	changed := map[string]bool{} // files that are imported (or embedded): their packages and reverse deps
	direct := map[string]bool{}  // _test.go files: their own package only
	for _, f := range files {
		if f = path.Clean(f); f == "." || strings.HasPrefix(f, "../") {
			continue
		}
		test := strings.HasSuffix(f, "_test.go")
		for dir := path.Dir(f); ; dir = path.Dir(dir) {
			if ip, ok := byDir[dir]; ok {
				if test {
					direct[ip] = true
				} else {
					changed[ip] = true
				}
				break
			}
			if dir == "." {
				if strings.HasSuffix(f, ".go") {
					return Result{Full: "no package for " + f}, nil
				}
				break
			}
		}
	}
	set := map[string]bool{}
	for _, p := range list {
		name := strip(p.ImportPath)
		if p.Name == "main" && p.ForTest == "" && strings.HasSuffix(name, ".test") {
			continue // the generated test main: its test variants are listed on their own
		}
		owner := name
		if p.ForTest != "" {
			owner = p.ForTest
		}
		if changed[name] || direct[name] {
			set[owner] = true
			continue
		}
		for _, d := range p.Deps {
			if changed[strip(d)] {
				set[owner] = true
				break
			}
		}
	}
	var res Result
	for ip := range set {
		res.Pkgs = append(res.Pkgs, ip)
	}
	sort.Strings(res.Pkgs)
	return res, nil
}

type pkg struct {
	ImportPath, ForTest, Name, Rel string
	Deps                           []string
}

// goList lists the module's packages and their test variants with their transitive deps (go list -test ./...).
func goList(root string) ([]pkg, error) {
	cmd := exec.Command("go", "list", "-e", "-test", "-f",
		"{{.ImportPath}}\t{{.ForTest}}\t{{.Name}}\t{{with .Module}}{{.Path}}{{end}}\t{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var list []pkg
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || strings.HasPrefix(fields[0], "./") { // "./...": nothing matched (no go.mod)
			return nil, fmt.Errorf("go list: unexpected line %q", line)
		}
		p := pkg{ImportPath: fields[0], ForTest: fields[1], Name: fields[2], Deps: strings.Fields(fields[4])}
		name, mod := strip(p.ImportPath), fields[3]
		switch {
		case name == mod:
			p.Rel = "."
		case strings.HasPrefix(name, mod+"/"):
			p.Rel = name[len(mod)+1:]
		}
		list = append(list, p)
	}
	return list, nil
}

// strip drops a test variant's " [p.test]" suffix.
func strip(importPath string) string {
	name, _, _ := strings.Cut(importPath, " [")
	return name
}
