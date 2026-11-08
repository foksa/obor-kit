// Command affected selects the Go packages a change can break (T-1040, moved from internal/affected, T-347, T-351):
// the packages of the changed files plus every package whose imports or test imports reach them. A changed test file
// selects only its own package (T-954): no other package imports it. The kit's Makefile runs it for `make
// check-affected`; it is a kit tool, not part of the obor binary, and imports the standard library only.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	os.Exit(run(".", os.Args[1:], os.Getenv, os.Stdout))
}

// run is the tool: root is the module root (whose .obor/config.json carries merge.affected_full), getenv the
// environment (OBOR_CHANGED_FILES, the changed files one repo-relative path per line), out where the import paths, or
// the one "full: <why>" line, go. It returns 1 when the list cannot be made (the Makefile then runs the full suite) and
// 2 on a bad flag.
func run(root string, args []string, getenv func(string) string, out io.Writer) int {
	direct := false
	for _, a := range args {
		if a != "--direct" {
			fmt.Fprintln(os.Stderr, "usage: affected [--direct] (changed files: $OBOR_CHANGED_FILES)")
			return 2
		}
		direct = true
	}
	fullGlobs, err := affectedFull(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "affected: %v\n", err)
		return 1
	}
	changed := getenv("OBOR_CHANGED_FILES")
	if changed == "" {
		fmt.Fprintln(out, "full: OBOR_CHANGED_FILES not set")
		return 0
	}
	data, err := os.ReadFile(changed)
	if err != nil {
		fmt.Fprintf(out, "full: OBOR_CHANGED_FILES unreadable (%v)\n", err)
		return 0
	}
	var files []string
	for _, f := range strings.Split(string(data), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	res, err := Go(root, files, fullGlobs, direct)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "affected: %v\n", err)
		return 1
	case res.Full != "":
		fmt.Fprintln(out, "full: "+res.Full)
	default:
		for _, p := range res.Pkgs {
			fmt.Fprintln(out, p)
		}
	}
	return 0
}

// affectedFull is merge.affected_full, the globs whose change forces the full suite, from root's .obor/config.json.
func affectedFull(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ".obor", "config.json"))
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Merge struct {
			AffectedFull []string `json:"affected_full"`
		} `json:"merge"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf(".obor/config.json: %v", err)
	}
	return cfg.Merge.AffectedFull, nil
}

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
// With direct, only the changed packages themselves are returned (T-991): no reverse dependencies, while the full
// answers (go.mod, fullGlobs, no package for a .go file) stay as they are.
func Go(root string, files, fullGlobs []string, direct bool) (Result, error) {
	for _, f := range files {
		if f == "go.mod" || f == "go.sum" {
			return Result{Full: "changed " + f}, nil
		}
		for _, g := range fullGlobs {
			if match(f, g) {
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
	changed := map[string]bool{}   // files that are imported (or embedded): their packages and reverse deps
	testFiles := map[string]bool{} // _test.go files: their own package only
	for _, f := range files {
		if f = path.Clean(f); f == "." || strings.HasPrefix(f, "../") {
			continue
		}
		test := strings.HasSuffix(f, "_test.go")
		for dir := path.Dir(f); ; dir = path.Dir(dir) {
			if ip, ok := byDir[dir]; ok {
				if test {
					testFiles[ip] = true
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
		if changed[name] || testFiles[name] {
			set[owner] = true
			continue
		}
		if direct {
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

// match follows obor_glob_match as internal/spec does (the tool may not import it, standard library only): * and ** both
// span '/', and **/ also matches zero directories.
func match(file, glob string) bool {
	re, err := regexp.Compile(globRegexp(glob))
	return err == nil && re.MatchString(file)
}

func globRegexp(glob string) string {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(glob); {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i += 2
				if i < len(glob) && glob[i] == '/' {
					b.WriteString("(.*/)?")
					i++
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString(".*")
				i++
			}
		case '?':
			b.WriteByte('.')
			i++
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				i++
				continue
			}
			end += i + 1
			class := glob[i+1 : end]
			b.WriteByte('[')
			if strings.HasPrefix(class, "!") {
				b.WriteByte('^')
				class = class[1:]
			}
			b.WriteString(class)
			b.WriteByte(']')
			i = end + 1
		default:
			if strings.ContainsRune(`.+()|{}^$\\`, rune(glob[i])) {
				b.WriteByte('\\')
			}
			b.WriteByte(glob[i])
			i++
		}
	}
	b.WriteByte('$')
	return b.String()
}
