// Command budget is the nightly test budget (T-1002, the test-suite rework's T-J): it measures every test package and
// compares it with tests/budget.json. Run it with `make check-budget` (commands.check_budget, in
// merge.nightly_commands as a non-gating line, like conformance).
//
// It measures each package with tests once at GOMAXPROCS=2, with -tags integration (T-1014: the whole suite is
// measured; T-1056: the -short tier is gone, so there is one measurement). A counting
// `git` wrapper is first on PATH for the whole run and a private TMPDIR is every test's temp dir; the package's `go
// test -json` elapsed is its wall time. A breach prints one line, `FAIL budget: <pkg> <metric> <got> > <max>`:
//
//	full_s             go test's package Elapsed against tests/budget.json (seconds; a cap under a second never fails
//	                   on wall time alone, minWallS: at that size go test's 0.1 s granularity is the measurement)
//	git_procs          git processes the run started, against git_procs
//	tmp                entries left in the private TMPDIR after the runs
//
// A missing entry for a package with tests is a breach too, so a new package is never unbudgeted. A green run prints
// `budget OK: N packages` and exits 0; a breach exits 1 (the nightly records it as a red non-gating line).
//
// `-missing` is the budget's own listing: it compares the packages with tests with tests/budget.json, runs no tests and
// prints the command that seeds a package without an entry, or the entry whose package is gone (T-1073; a step of
// make check-affected, so the lane that adds a package fails there instead of at the next nightly).
//
// The budgets are seeded from one quiet machine's run x1.5: `go run ./tests/budget -seed <pkg>...` measures the named
// packages and writes their entries into tests/budget.json, every other entry unchanged, so the task that adds a
// package seeds it alone; `-seed` with no arguments re-measures the whole file (T-1007, T-1008, T-1073). A budget is
// raised only by an owner line in docs/decisions.md and lowered by any task that gets under it
// (docs/plan/test-suite-rework.md §3).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Budget is one package's caps: the wall seconds of its run (go test's package Elapsed) and the git processes it starts.
type Budget struct {
	FullS    float64 `json:"full_s"`
	GitProcs int     `json:"git_procs"`
}

// File is tests/budget.json.
type File struct {
	Packages map[string]Budget `json:"packages"`
}

// Run is one package's measurement: one `go test -json` run. Passed is the package's own verdict (a build error, a
// failed test or a panic leaves it false); Elapsed is that verdict's Elapsed, the run's wall time.
type Run struct {
	Elapsed float64
	Passed  bool
	Failed  []string // top-level tests that failed
	Git     int      // git processes the run started
	note    string   // why the run failed when Passed is false (the build output's last line)
}

// minWallS is the floor of a package's wall-time cap (T-1024): go test's Elapsed moves by 0.1 s from run to run, so a
// cap under a second is the same size as that noise (internal/version 0.6 > 0.4, tests/budget 0.9 > 0.9 on a quiet
// machine). A package under it never fails on wall time alone: its wall time is that small.
const minWallS = 1.0

// wallLimit is the wall-time cap a package is measured against: its cap, or the floor when the cap is under it.
func wallLimit(cap float64) float64 { return math.Max(cap, minWallS) }

// check returns one FAIL budget line per breach and the number of packages it compared.
func check(budget File, pkgs []string, runs map[string]Run, tmpLeft []string) (lines []string, n int) {
	n = len(pkgs)
	for _, pkg := range pkgs {
		b, ok := budget.Packages[pkg]
		if !ok {
			lines = append(lines, missingBudget(pkg))
			continue
		}
		r := runs[pkg]
		if !r.Passed {
			lines = append(lines, "FAIL budget: "+pkg+" run failed"+r.failure())
			continue
		}
		if limit := wallLimit(b.FullS); r.Elapsed > limit {
			lines = append(lines, fmt.Sprintf("FAIL budget: %s full_s %.1fs > %.1fs", pkg, r.Elapsed, limit))
		}
		if r.Git > b.GitProcs {
			lines = append(lines, fmt.Sprintf("FAIL budget: %s git_procs %d > %d", pkg, r.Git, b.GitProcs))
		}
	}
	if len(tmpLeft) > 0 {
		shown := tmpLeft
		if len(shown) > 10 {
			shown = append(shown[:10:10], fmt.Sprintf("and %d more", len(tmpLeft)-10))
		}
		lines = append(lines, fmt.Sprintf("FAIL budget: tmp %d entries left in the private TMPDIR: %s", len(tmpLeft), strings.Join(shown, ", ")))
	}
	return lines, n
}

// failure is the run's note for a breach line: the failed top-level tests, else the note, else nothing.
func (r Run) failure() string {
	if len(r.Failed) > 0 {
		return ": " + strings.Join(r.Failed, ", ")
	}
	if r.note != "" {
		return ": " + r.note
	}
	return ""
}

// missingBudget is the line for a package with tests and no entry: check's breach and the start of -missing's line.
func missingBudget(pkg string) string {
	return "FAIL budget: " + pkg + " missing from tests/budget.json"
}

// missing lists -missing's lines (T-1073): the packages with tests and no entry, each with the seed command that writes
// it, then the entries whose package is gone (deleted or moved), each with what to do. It runs no tests.
func missing(pkgs []string, budget File) []string {
	have := make(map[string]bool, len(pkgs))
	var lines []string
	for _, pkg := range pkgs {
		have[pkg] = true
		if _, ok := budget.Packages[pkg]; !ok {
			lines = append(lines, missingBudget(pkg)+" — run: go run ./tests/budget -seed "+pkg)
		}
	}
	var gone []string
	for pkg := range budget.Packages {
		if !have[pkg] {
			gone = append(gone, pkg)
		}
	}
	sort.Strings(gone)
	for _, pkg := range gone {
		lines = append(lines, "FAIL budget: "+pkg+" in tests/budget.json has no package with tests — delete its entry")
	}
	return lines
}

// parseRun reads one `go test -json` run's output. Non-JSON lines (the go command's own, such as `? pkg [no test
// files]` or a `go: downloading` note) are ignored.
func parseRun(out []byte) Run {
	var r Run
	var pkgOut strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e struct {
			Action  string
			Test    string
			Elapsed float64
			Output  string
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Test != "" {
			if e.Action == "fail" && !strings.Contains(e.Test, "/") {
				r.Failed = append(r.Failed, e.Test)
			}
			continue
		}
		switch e.Action {
		case "pass":
			r.Passed, r.Elapsed = true, e.Elapsed
		case "fail":
			r.Passed, r.Elapsed = false, e.Elapsed
		case "build-fail":
			r.Passed = false
		case "build-output", "output":
			pkgOut.WriteString(e.Output)
		}
	}
	r.note = lastLine(pkgOut.String())
	slices.Sort(r.Failed)
	return r
}

// seed is the measured budgets x1.5, rounded up to a tenth of a second (git processes rounded up too): one quiet
// machine's run with headroom for the nightly's noise.
func seed(runs map[string]Run) File {
	f := File{Packages: map[string]Budget{}}
	for pkg, r := range runs {
		f.Packages[pkg] = Budget{
			FullS:    math.Ceil(r.Elapsed*1.5*10) / 10,
			GitProcs: int(math.Ceil(float64(r.Git) * 1.5)),
		}
	}
	return f
}

// seeded is -seed's file, ready to write: with names, the measured entries merged into tests/budget.json, every other
// entry kept byte for byte (T-1073); with none, the whole measurement, as before: a package with no tests any more is
// dropped. The per-test caps and untagged_s an older tests/budget.json carried are not carried over: they measured the
// -short tier, which is gone (T-1056).
func seeded(root string, named []string, runs map[string]Run) ([]byte, error) {
	measured := seed(runs)
	if len(named) == 0 {
		return marshal(measured)
	}
	var f struct {
		Packages map[string]json.RawMessage `json:"packages"`
	}
	b, err := os.ReadFile(filepath.Join(root, "tests", "budget.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("tests/budget.json: %v", err)
		}
	}
	if f.Packages == nil {
		f.Packages = map[string]json.RawMessage{}
	}
	for _, pkg := range named {
		raw, err := json.Marshal(measured.Packages[pkg])
		if err != nil {
			return nil, err
		}
		f.Packages[pkg] = raw
	}
	return marshal(f)
}

// marshal is JSON encoded in the file's shape: two-space indent and a trailing newline.
func marshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// seedFile is -seed's write (T-1008, T-1073): the measured entries into tests/budget.json, through a temp file in the
// same dir and a rename, so the old file stays on any error. named are the packages to write; none is the whole
// measurement. A run that did not pass is no measurement and writes nothing; before, the Makefile's redirect truncated
// the file before seeded could read its caps (RV-139 #2).
func seedFile(root string, named []string, runs map[string]Run) error {
	var failed []string
	for pkg, run := range runs {
		if !run.Passed {
			failed = append(failed, pkg)
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("%s did not pass; tests/budget.json left unchanged", strings.Join(failed, ", "))
	}
	b, err := seeded(root, named, runs)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "tests")
	tmp, err := os.CreateTemp(dir, ".budget-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a failed write or rename leaves no temp file behind
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, "budget.json"))
}

// runner runs the measurements in one module root with the counting git wrapper first on PATH and a private TMPDIR.
type runner struct {
	goBin string
	dir   string
	path  string // PATH with the wrapper first
	count string // the wrapper's counter file
	tmp   string // the private TMPDIR the runs get
	work  string // the runner's own dir (removed by close)
}

// newRunner makes work: bin/git (the wrapper) and tmp (the private TMPDIR). realGit is exec.LookPath's git: the wrapper
// calls it, so a test can pass a stub.
func newRunner(dir, goBin, realGit string) (*runner, error) {
	work, err := os.MkdirTemp(tempRoot(), "obor-budget-")
	if err != nil {
		return nil, err
	}
	r := &runner{
		goBin: goBin,
		dir:   dir,
		count: filepath.Join(work, "git-count"),
		tmp:   filepath.Join(work, "tmp"),
		work:  work,
	}
	bin := filepath.Join(work, "bin")
	r.path = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if err := os.MkdirAll(r.tmp, 0o755); err != nil {
		r.close()
		return nil, err
	}
	if err := writeGitWrapper(bin, realGit, r.count); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// writeGitWrapper writes bin/git, a counting wrapper: one byte in count per call, then the real git.
func writeGitWrapper(bin, realGit, count string) error {
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	script := "#!/bin/sh\nprintf x >> " + quote(count) + "\nexec " + quote(realGit) + " \"$@\"\n"
	return os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755)
}

// tempRoot is where the runner's work dir goes: /tmp when it exists. A private TMPDIR must be short: obor's tests put
// a Unix socket under it, and macOS allows 104 bytes of path (a directory under macOS's own TMPDIR, 49 bytes before the
// project, overflows once the socket's name is added).
func tempRoot() string {
	if st, err := os.Stat("/tmp"); err == nil && st.IsDir() {
		return "/tmp"
	}
	return ""
}

// quote is a path for /bin/sh: single-quoted, escaping any single quote in it.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// env is the runs' environment: the runner's PATH, TMPDIR and GOMAXPROCS=2 in place of the caller's.
func (r *runner) env() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "TMPDIR", "GOTMPDIR", "GOMAXPROCS":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PATH="+r.path, "TMPDIR="+r.tmp, "GOMAXPROCS=2")
}

// run measures pkg as one `go test -json -count=1 -tags integration`.
func (r *runner) run(pkg string) (Run, error) {
	if err := os.Remove(r.count); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Run{}, err
	}
	args := testArgs(pkg)
	cmd := exec.Command(r.goBin, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	run := parseRun(stdout.Bytes())
	run.Git = r.gitCount()
	if err != nil && run.Passed {
		run.Passed = false
		if run.note == "" {
			run.note = firstLine(stderr.String())
		}
	}
	return run, nil
}

// testArgs is a package's go test: -tags integration, so the budget measures the tagged tests too (T-1014); a package
// whose only tests are tagged would otherwise never be measured.
func testArgs(pkg string) []string {
	return []string{"test", "-json", "-count=1", "-tags", "integration", pkg}
}

// gitCount is the wrapper's calls since the last run.
func (r *runner) gitCount() int {
	b, err := os.ReadFile(r.count)
	if err != nil {
		return 0
	}
	return len(b)
}

// leftovers is the private TMPDIR's entries after the runs: every test's temp dir should be gone.
func (r *runner) leftovers() []string {
	ents, err := os.ReadDir(r.tmp)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func (r *runner) close() {
	if r.work != "" {
		os.RemoveAll(r.work)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// lastLine is s's last non-empty line, trimmed: a build error's message is its last line.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// findRoot walks up from the working directory to the module root (go.mod).
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// listPackages names the module's packages that have tests, in the order go list gives them. The tag is passed too, so
// a package whose tests are all behind `//go:build integration` still counts (T-1014: an unmeasured package is a
// breach).
func listPackages(root, goBin string, env []string) ([]string, error) {
	cmd := exec.Command(goBin, "list", "-tags", "integration", "-f", "{{.ImportPath}} {{len .TestGoFiles}} {{len .XTestGoFiles}}", "./...")
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, firstLine(string(out)))
	}
	var pkgs []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		a, err1 := strconv.Atoi(f[1])
		b, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil && a+b > 0 {
			pkgs = append(pkgs, f[0])
		}
	}
	return pkgs, nil
}

// load reads tests/budget.json.
func load(root string) (File, error) {
	var f File
	b, err := os.ReadFile(filepath.Join(root, "tests", "budget.json"))
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("tests/budget.json: %v", err)
	}
	if len(f.Packages) == 0 {
		return f, errors.New("tests/budget.json: no packages")
	}
	return f, nil
}

func main() {
	os.Exit(run())
}

// run is main's work, returning the exit status: 0 green, 1 a breach, 2 the check itself could not run. It returns
// instead of exiting so the deferred close removes the work dir (a breached run leaves no temp dirs behind).
func run() int {
	seedFlag := flag.Bool("seed", false, "write tests/budget.json measured now (x1.5); with package arguments only those")
	missingFlag := flag.Bool("missing", false, "print packages with tests and no tests/budget.json entry, and entries whose package is gone; runs no tests")
	verbose := flag.Bool("v", false, "print each package's measurement to stderr")
	flag.Parse()

	root, err := findRoot()
	if err != nil {
		return fail(err)
	}
	goBin := os.Getenv("GO")
	if goBin == "" {
		goBin = "go"
	}
	if *missingFlag {
		pkgs, err := listPackages(root, goBin, os.Environ())
		if err != nil {
			return fail(err)
		}
		budget, err := load(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		lines := missing(pkgs, budget)
		for _, line := range lines {
			fmt.Println(line)
		}
		if len(lines) > 0 {
			return 1
		}
		return 0
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		return fail(fmt.Errorf("git: %v (the budget counts its processes)", err))
	}
	r, err := newRunner(root, goBin, realGit)
	if err != nil {
		return fail(err)
	}
	defer r.close()

	named := flag.Args()
	var pkgs []string
	if *seedFlag && len(named) > 0 {
		pkgs = named // -seed <pkg>...: measure only these, and merge them into the file (T-1073)
	} else {
		pkgs, err = listPackages(root, goBin, r.env())
		if err != nil {
			return fail(err)
		}
	}
	budget := File{}
	if !*seedFlag {
		if budget, err = load(root); err != nil {
			return fail(err)
		}
	}
	runs := map[string]Run{}
	for _, pkg := range pkgs {
		m, err := r.run(pkg)
		if err != nil {
			return fail(err)
		}
		runs[pkg] = m
		if *verbose {
			fmt.Fprintf(os.Stderr, "%s full=%.1fs git=%d\n", pkg, m.Elapsed, m.Git)
		}
	}

	if *seedFlag {
		if err := seedFile(root, named, runs); err != nil {
			return fail(err)
		}
		fmt.Fprintln(os.Stderr, "seed: wrote tests/budget.json")
		return 0
	}
	lines, n := check(budget, pkgs, runs, r.leftovers())
	for _, line := range lines {
		fmt.Println(line)
	}
	if len(lines) > 0 {
		return 1
	}
	fmt.Printf("budget OK: %d packages\n", n)
	return 0
}

// fail prints err as the check's own failure and returns its exit status.
func fail(err error) int {
	fmt.Fprintln(os.Stderr, "FAIL budget:", err)
	return 2
}
