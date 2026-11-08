// Command budget is the nightly test budget (T-1002, the test-suite rework's T-J): it measures every test package's
// two tiers and compares them with tests/budget.json. Run it with `make check-budget` (commands.check_budget, in
// merge.nightly_commands as a non-gating line, like conformance).
//
// It measures each package with tests at GOMAXPROCS=2, twice: the full tier and the -short tier (the lane's), both with
// -tags integration (T-1014: the whole suite is measured). A counting
// `git` wrapper is first on PATH for the whole run and a private TMPDIR is every test's temp dir; the package's `go
// test -json` elapsed is its wall time. A breach prints one line, `FAIL budget: <pkg> <metric> <got> > <max>`:
//
//	short_s / full_s   go test's package Elapsed against tests/budget.json (seconds; a cap under a second never fails
//	                   on wall time alone, minWallS: at that size go test's 0.1 s granularity is the measurement)
//	git_procs          git processes the full tier started, against git_procs
//	test/<name>        a top-level test over its `tests` cap, or over `untagged_s` with neither a cap nor testkit.Slow
//	                   (T-1004: a test's cost is its own Elapsed plus every paused subtest's; the -short tier's skip
//	                   names the Slow-marked ones)
//	tmp                entries left in the private TMPDIR after the runs
//
// A missing entry for a package with tests is a breach too, so a new package is never unbudgeted. A green run prints
// `budget OK: N packages` and exits 0; a breach exits 1 (the nightly records it as a red non-gating line).
//
// The budgets are seeded from one quiet machine's run x1.5: `go run ./tests/budget -seed` writes tests/budget.json
// itself, keeping the per-test caps and untagged_s of the file it finds (T-1007, T-1008). A budget is raised only by an
// owner line in docs/decisions.md and lowered by any task that gets under it (docs/plan/test-suite-rework.md §3).
// Per-test caps are hand-set after a seed: they record a cost the project accepts.
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

// Budget is one package's caps: the wall seconds of its -short and full tiers (go test's package Elapsed) and the git
// processes its full tier starts.
type Budget struct {
	ShortS   float64 `json:"short_s"`
	FullS    float64 `json:"full_s"`
	GitProcs int     `json:"git_procs"`
}

// File is tests/budget.json.
type File struct {
	UntaggedS float64            `json:"untagged_s"` // the cost at which a test with neither a cap nor testkit.Slow is reported
	Tests     map[string]float64 `json:"tests"`      // "<pkg> <TestName>" -> the cost it may take (seconds)
	Packages  map[string]Budget  `json:"packages"`
}

// Test is one top-level test's full-tier time.
type Test struct {
	Name    string
	Elapsed float64
}

// Run is one package's measurement: one `go test -json` tier. Passed is the package's own verdict (a build error, a
// failed test or a panic leaves it false); Elapsed is that verdict's Elapsed, the tier's wall time.
type Run struct {
	Elapsed float64
	Passed  bool
	Tests   []Test          // top-level tests that ran, sorted by name
	Failed  []string        // top-level tests that failed
	Slow    map[string]bool // tests the -short tier skipped through testkit.Slow (matched by its skip message)
	Git     int             // git processes the tier started
	note    string          // why the tier failed when Passed is false (the build output's last line)
}

// slowMark is testkit.Slow's skip message; the -short tier marks every Slow test with it.
const slowMark = "slow: runs in make check"

// minWallS is the floor of a package's wall-time cap (T-1024): go test's Elapsed moves by 0.1 s from run to run, so a
// cap under a second is the same size as that noise (internal/version 0.6 > 0.4, tests/budget 0.9 > 0.9 on a quiet
// machine). A package under it never fails on wall time alone: its wall time is that small. Per-test caps are hand-set
// (all a second or more) and get no floor.
const minWallS = 1.0

// wallLimit is the wall-time cap a package is measured against: its cap, or the floor when the cap is under it.
func wallLimit(cap float64) float64 { return math.Max(cap, minWallS) }

// check returns one FAIL budget line per breach and the number of packages it compared.
func check(budget File, pkgs []string, full, short map[string]Run, tmpLeft []string) (lines []string, n int) {
	n = len(pkgs)
	for _, pkg := range pkgs {
		b, ok := budget.Packages[pkg]
		if !ok {
			lines = append(lines, "FAIL budget: "+pkg+" missing from tests/budget.json")
			continue
		}
		f, s := full[pkg], short[pkg]
		if !f.Passed {
			lines = append(lines, "FAIL budget: "+pkg+" full run failed"+f.failure())
		} else {
			if limit := wallLimit(b.FullS); f.Elapsed > limit {
				lines = append(lines, fmt.Sprintf("FAIL budget: %s full_s %.1fs > %.1fs", pkg, f.Elapsed, limit))
			}
			if f.Git > b.GitProcs {
				lines = append(lines, fmt.Sprintf("FAIL budget: %s git_procs %d > %d", pkg, f.Git, b.GitProcs))
			}
		}
		if !s.Passed {
			lines = append(lines, "FAIL budget: "+pkg+" short run failed"+s.failure())
		} else if limit := wallLimit(b.ShortS); s.Elapsed > limit {
			lines = append(lines, fmt.Sprintf("FAIL budget: %s short_s %.1fs > %.1fs", pkg, s.Elapsed, limit))
		}
		for _, t := range f.Tests {
			if limit, ok := budget.Tests[pkg+" "+t.Name]; ok {
				if t.Elapsed > limit {
					lines = append(lines, fmt.Sprintf("FAIL budget: %s test/%s %.2fs > %.1fs", pkg, t.Name, t.Elapsed, limit))
				}
				continue
			}
			if t.Elapsed >= budget.UntaggedS && !s.Slow[t.Name] {
				lines = append(lines, fmt.Sprintf("FAIL budget: %s test/%s %.2fs >= %.1fs without testkit.Slow", pkg, t.Name, t.Elapsed, budget.UntaggedS))
			}
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

// parseRun reads one `go test -json` tier's output. Non-JSON lines (the go command's own, such as `? pkg [no test
// files]` or a `go: downloading` note) are ignored. A top-level test's cost is its own pass Elapsed plus the Elapsed of
// every subtest that paused: a parent's Elapsed excludes its parallel subtests (RV-139 #2: it reports 0 s for them),
// Go's Elapsed already excludes a test's wait between `pause` and `cont`, and a subtest that never paused is already
// inside its parent's Elapsed, so it is not added again.
func parseRun(out []byte) Run {
	r := Run{Slow: map[string]bool{}}
	outputs := map[string]*strings.Builder{}
	elapsed := map[string]float64{} // each test's own pass/fail Elapsed
	paused := map[string]bool{}     // the tests that had a pause event (they called t.Parallel)
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
		top := e.Test != "" && !strings.Contains(e.Test, "/")
		if e.Test != "" {
			if e.Output != "" {
				b := outputs[e.Test]
				if b == nil {
					b = &strings.Builder{}
					outputs[e.Test] = b
				}
				b.WriteString(e.Output)
			}
			switch e.Action {
			case "pause":
				paused[e.Test] = true
			case "pass", "fail", "skip":
				elapsed[e.Test] = e.Elapsed
			}
			switch e.Action {
			case "pass":
				if top {
					r.Tests = append(r.Tests, Test{Name: e.Test})
				}
			case "fail":
				if top {
					r.Failed = append(r.Failed, e.Test)
				}
			case "skip":
				if top && strings.Contains(outputs[e.Test].String(), slowMark) {
					r.Slow[e.Test] = true
				}
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
	for i, t := range r.Tests {
		cost := elapsed[t.Name]
		for name, sub := range elapsed {
			if paused[name] && strings.HasPrefix(name, t.Name+"/") {
				cost += sub
			}
		}
		r.Tests[i].Elapsed = cost
	}
	r.note = lastLine(pkgOut.String())
	slices.SortFunc(r.Tests, func(a, b Test) int { return strings.Compare(a.Name, b.Name) })
	slices.Sort(r.Failed)
	return r
}

// untaggedDefault is the tolerance a seeded file carries: a test without a cap or testkit.Slow is reported at this
// cost, so a test that measures 0.74 s once and 1.03 s the next time does not flap (T-1004).
const untaggedDefault = 1.3

// seed is the measured budgets x1.5, rounded up to a tenth of a second (git processes rounded up too): one quiet
// machine's run with headroom for the nightly's noise. prev's per-test caps and untagged_s are carried over unchanged
// (T-1007): they record a cost and a threshold a person set, so a re-measure never drops them. Per-test caps are never
// derived from a measurement.
func seed(full, short map[string]Run, prev File) File {
	f := File{UntaggedS: untaggedDefault, Tests: prev.Tests, Packages: map[string]Budget{}}
	if prev.UntaggedS > 0 {
		f.UntaggedS = prev.UntaggedS
	}
	for pkg, r := range full {
		f.Packages[pkg] = Budget{
			ShortS:   math.Ceil(short[pkg].Elapsed*1.5*10) / 10,
			FullS:    math.Ceil(r.Elapsed*1.5*10) / 10,
			GitProcs: int(math.Ceil(float64(r.Git) * 1.5)),
		}
	}
	return f
}

// seeded is -seed's file: the measurement, with the caps and threshold of an existing tests/budget.json carried over
// (T-1007); no file: seed's defaults.
func seeded(root string, full, short map[string]Run) (File, error) {
	prev, err := load(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return File{}, err
	}
	return seed(full, short, prev), nil
}

// seedFile is -seed's write (T-1008): the seeded file into tests/budget.json, through a temp file in the same dir and a
// rename, so the old file stays on any error. A tier that did not pass is no measurement and writes nothing; before, the
// Makefile's redirect truncated the file before seeded could read its caps (RV-139 #2).
func seedFile(root string, pkgs []string, full, short map[string]Run) error {
	for _, pkg := range pkgs {
		if !full[pkg].Passed || !short[pkg].Passed {
			return fmt.Errorf("%s did not pass; tests/budget.json left unchanged", pkg)
		}
	}
	f, err := seeded(root, full, short)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
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
	if _, err := tmp.Write(append(b, '\n')); err != nil {
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

// runner runs the tiers in one module root with the counting git wrapper first on PATH and a private TMPDIR.
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

// env is the tiers' environment: the runner's PATH, TMPDIR and GOMAXPROCS=2 in place of the caller's.
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

// run measures pkg's full tier, or its -short tier, as one `go test -json -count=1 -tags integration`.
func (r *runner) run(pkg string, short bool) (Run, error) {
	if err := os.Remove(r.count); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Run{}, err
	}
	args := testArgs(pkg, short)
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

// testArgs is one tier's go test: -tags integration, so the budget measures the tagged tests too (T-1014); a package
// whose only tests are tagged would otherwise never be measured.
func testArgs(pkg string, short bool) []string {
	args := []string{"test", "-json", "-count=1", "-tags", "integration"}
	if short {
		args = append(args, "-short")
	}
	return append(args, pkg)
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
	if f.UntaggedS <= 0 {
		return f, errors.New("tests/budget.json: no untagged_s")
	}
	return f, nil
}

func main() {
	os.Exit(run())
}

// run is main's work, returning the exit status: 0 green, 1 a breach, 2 the check itself could not run. It returns
// instead of exiting so the deferred close removes the work dir (a breached run leaves no temp dirs behind).
func run() int {
	seedFlag := flag.Bool("seed", false, "write tests/budget.json measured now (x1.5) and compare nothing")
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
	realGit, err := exec.LookPath("git")
	if err != nil {
		return fail(fmt.Errorf("git: %v (the budget counts its processes)", err))
	}
	r, err := newRunner(root, goBin, realGit)
	if err != nil {
		return fail(err)
	}
	defer r.close()

	pkgs, err := listPackages(root, goBin, r.env())
	if err != nil {
		return fail(err)
	}
	budget := File{}
	if !*seedFlag {
		if budget, err = load(root); err != nil {
			return fail(err)
		}
	}
	full, short := map[string]Run{}, map[string]Run{}
	for _, pkg := range pkgs {
		f, err := r.run(pkg, false)
		if err != nil {
			return fail(err)
		}
		s, err := r.run(pkg, true)
		if err != nil {
			return fail(err)
		}
		full[pkg], short[pkg] = f, s
		if *verbose {
			fmt.Fprintf(os.Stderr, "%s full=%.1fs git=%d short=%.1fs\n", pkg, f.Elapsed, f.Git, s.Elapsed)
		}
	}

	if *seedFlag {
		if err := seedFile(root, pkgs, full, short); err != nil {
			return fail(err)
		}
		fmt.Fprintln(os.Stderr, "seed: wrote tests/budget.json")
		return 0
	}
	lines, n := check(budget, pkgs, full, short, r.leftovers())
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
