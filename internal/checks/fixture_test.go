package checks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
	"obor/internal/testkit"
)

// tmpl is the base of the one fixture repo these tests copy (T-998): built on first use, after testkit.Run armed New
// (T-1019), and removed after the run.
var tmpl = sync.OnceValue(buildTemplate)

var (
	tmplMu       sync.Mutex
	tmplDirs     []string // every template base built for the run, removed after it
	mainCleanups []func() // mainTB's Cleanups: the temp dirs testkit's fixtures made, removed after the run
)

// keep notes d as a template base TestMain removes.
func keep(d string) string {
	tmplMu.Lock()
	tmplDirs = append(tmplDirs, d)
	tmplMu.Unlock()
	return d
}

func TestMain(m *testing.M) {
	testkit.Isolate()
	code := testkit.Run(m) // removes the project template testkit.New copies (T-1009)
	for _, d := range tmplDirs {
		os.RemoveAll(d)
	}
	for i := len(mainCleanups) - 1; i >= 0; i-- {
		mainCleanups[i]()
	}
	os.Exit(code)
}

// mainTB is a testing.TB for the templates built outside a test: failures panic, and Cleanup's work (testkit's short
// temp dirs) runs after the run, as a test's would at its end: the fixture must outlive every test that copies it
// (T-1024: before, Cleanup was a no-op and each fixture's hk<digits> dir stayed in TMPDIR).
type mainTB struct{ testing.TB }

func (mainTB) Helper()             {}
func (mainTB) Log(...any)          {}
func (mainTB) Logf(string, ...any) {}

func (mainTB) Cleanup(f func()) {
	tmplMu.Lock()
	mainCleanups = append(mainCleanups, f)
	tmplMu.Unlock()
}
func (mainTB) Fatal(args ...any)                 { panic(fmt.Sprint(args...)) }
func (mainTB) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
func (mainTB) Errorf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
func (mainTB) Name() string                      { return "TestMain" }
func (mainTB) TempDir() string {
	d, err := os.MkdirTemp("", "hk-tmpl")
	if err != nil {
		panic(err)
	}
	return keep(d)
}

// shared is one fixture state built once, on first use, and copied per test: the checked resolutions the mismatch and
// replay tables move (T-G: one conflict fixture, not one per row).
type shared struct {
	once     sync.Once
	base     string
	sha, res string         // a resolved fixture's submitted sha and checked resolution
	cand     port.Candidate // its green resolved candidate
}

// resolvedFx is the Go fixture after resolvedGreen.
var resolvedFx shared

// resolved copies the Go fixture at resolvedGreen: its submitted sha, checked resolution and green candidate.
func (s *shared) resolved(t *testing.T) (*sx, string, string, port.Candidate) {
	t.Helper()
	s.once.Do(func() {
		y := newGoSx(mainTB{})
		s.sha, s.res, s.cand = resolvedGreen(y)
		s.base = y.Base
	})
	return copySx(t, s.base), s.sha, s.res, s.cand
}

// snapshot copies src's tree into a fresh temp dir of the test's and returns it.
func snapshot(t testing.TB, src string) string {
	t.Helper()
	dst := testkit.Dir(t)
	if out, err := exec.Command("cp", "-R", src+"/.", dst).CombinedOutput(); err != nil {
		t.Fatalf("copy the fixture template: %v\n%s", err, out)
	}
	return dst
}

// copySx is a fresh sx at a copy of src's template in the test's own temp dir, its worktrees repaired.
func copySx(t *testing.T, src string) *sx {
	t.Helper()
	base := snapshot(t, src)
	p := testkit.Adopt(t, base)
	x := &fx{Project: p, t: t, lane: p.LaneDir("L-01"), scratch: filepath.Join(base, "proj-wt", "merge"),
		slots: filepath.Join(base, "slots"), sig: make(chan int, 1)}
	repairWorktrees(t, p, base)
	if err := os.MkdirAll(p.Path(".obor/state"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return &sx{fx: x, s: Scratch{Options: x.opts()}, cut: filepath.Join(base, "cut"), serve: x.scratch, real: real}
}

// repairWorktrees points every copied worktree under proj-wt at base.
func repairWorktrees(t testing.TB, p *testkit.Project, base string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(base, "proj-wt"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			paths = append(paths, filepath.Join(base, "proj-wt", e.Name()))
		}
	}
	if len(paths) > 0 {
		p.Git(p.Root, append([]string{"worktree", "repair"}, paths...)...)
	}
}

// fxConfig is the fixture's whole config: its defaults, then edits.
func fxConfig(edits ...func(map[string]any)) []func(map[string]any) {
	return append([]func(map[string]any){
		testkit.Set("lanes", 2),
		testkit.Set("merge", map[string]any{"trailer": trailer,
			"summary_regex": "[0-9]+ pass|[0-9]+ fail", "fail_grep": "Expected"}),
	}, edits...)
}

// buildTemplate builds the fixture repo once: the fixture commit, lane L-01 on task/T-05 with its uncommitted
// feature.txt, and the config commit on main after it (so a lane forks before the config, as every test expects).
func buildTemplate() string {
	p := testkit.New(mainTB{})
	p.Write(".gitignore", ".obor/state/\n")
	p.Write("shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "fixture")
	lane := p.Lane("L-01", "task/T-05")
	p.Config(fxConfig()...)
	p.Write(filepath.Join(lane, "feature.txt"), "feature\n")
	return p.Base
}

const trailer = "Co-Authored-By: Test <test@example.invalid>"

// fx is the fixture these tests copy from the template (T-998): .obor/state/ gitignored, a 10-line shared.txt, lane
// L-01 on task/T-05 with an uncommitted feature.txt, merge.trailer / summary_regex / fail_grep set, a per-test check
// slot dir. lane2 adds the idle lane L-02 on task/T-20 when a test uses it. The scratch is the default proj-wt/merge.
type fx struct {
	*testkit.Project
	t          testing.TB
	lane       string // the lane L-01 dir
	lane2Dir   string // lane L-02's dir, once a test asks for it
	scratch    string
	slots      string
	out        syncBuffer
	env        []string       // project.Env (OBOR_GATE, OBOR_FULL)
	environ    []string       // more command environment
	windowWait time.Duration  // > 0: the nightly lock's wait (T-864: no merge.window_wait key)
	cand       port.Candidate // the last green gate's (gate)
	gated      string         // the last green gate's line (gate: the test's is its result's)
	spec       string         // the task's stored spec gate passes (GateInput.Spec; "": none)
	sig        chan int
}

// newFx copies the template into the test's own temp dir; edits (when any) are committed on main. L-01 is the copied
// lane; a test that needs L-02 asks lane2, which adds it then.
func newFx(t testing.TB, edits ...func(map[string]any)) *fx {
	t.Helper()
	base := snapshot(t, tmpl())
	p := testkit.Adopt(t, base)
	x := &fx{Project: p, t: t, lane: p.LaneDir("L-01"), sig: make(chan int, 1)}
	repairWorktrees(t, p, base)
	if len(edits) > 0 {
		p.Config(fxConfig(edits...)...)
	}
	x.scratch = filepath.Join(p.Base, "proj-wt", "merge")
	x.slots = filepath.Join(p.Base, "slots")
	if err := os.MkdirAll(p.Path(".obor/state"), 0o755); err != nil { // serve's state dir (its database lives there)
		t.Fatal(err)
	}
	return x
}

// lane2 is lane L-02 on task/T-20, made when a test first asks for it (most tests use L-01 alone).
func (x *fx) lane2() string {
	x.t.Helper()
	if x.lane2Dir == "" {
		x.lane2Dir = x.Lane("L-02", "task/T-20")
	}
	return x.lane2Dir
}

// cmd is a config edit: commands.<name> = v (a string or an object).
func cmd(name string, v any) func(map[string]any) {
	return func(c map[string]any) { c["commands"].(map[string]any)[name] = v }
}

func (x *fx) opts() Options {
	x.t.Helper()
	cfg, err := config.Load(x.Root)
	if err != nil {
		x.t.Fatal(err)
	}
	// Without what an obor merge running this test exports (CHECK_PRIORITY, CHECK_SLOT_HELD, OBOR_MERGE, OBOR_GATE) and
	// what a lane test does (CHECK_SHORT, CHECK_DIRECT: T-989, T-991): the fixture's commands would see the outer merge's.
	environ := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		for _, k := range []string{"CHECK_PRIORITY=", "CHECK_SLOT_HELD=", "OBOR_MERGE=", "OBOR_GATE=", "CHECK_SHORT=",
			"CHECK_DIRECT="} {
			if strings.HasPrefix(kv, k) {
				return true
			}
		}
		return false
	})
	environ = append(environ, "GIT_AUTHOR_NAME=obor test", "GIT_AUTHOR_EMAIL=obor-test@example.invalid",
		"GIT_COMMITTER_NAME=obor test", "GIT_COMMITTER_EMAIL=obor-test@example.invalid", "CHECK_SLOT_DIR="+x.slots,
		"TMP_DIR="+x.Base)
	// The socket calls end at once: no test here runs a serve, and a nightly's backup must not wait out its deadline
	// (T-685).
	callCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	x.t.Cleanup(cancel)
	slots := &run.Slots{Dir: x.slots, Max: 2, BlockMax: 900 * time.Second, Marker: "merge-running-proj",
		Poll: 20 * time.Millisecond, Report: time.Hour}
	return Options{
		Cfg: cfg, Env: x.Env(x.env...), Environ: append(environ, x.environ...), Out: &x.out, PID: os.Getpid(),
		Interrupted: x.sig, callCtx: callCtx, WindowWait: x.windowWait,
		Slots: slots,
	}
}

// gate submits lane's work for task (worker label "worker"), gates it in serve's worktree and tests the candidate in
// the lane, as obor serve does (Scratch.Submit, a lane Gate, then Test: T-961): green is OK, red Red, fail and conflict
// Fail, each with the gate's line or, past a green gate, the test's; a refusal or error is Fail with its text. A green
// gate's candidate is x.cand, its line x.gated.
func (x *fx) gate(lane, task, summary string) Result {
	x.t.Helper()
	return x.gateCtx(context.Background(), lane, task, summary)
}

// gateCtx is gate whose Gate ends with ctx.
func (x *fx) gateCtx(ctx context.Context, lane, task, summary string) Result {
	x.out.Reset()
	s := Scratch{Options: x.opts()}
	sha, err := s.Submit(context.Background(), task, "1", lane, summary, "worker")
	if err != nil {
		return Result{Code: Fail, Line: err.Error()}
	}
	o, err := s.Gate(ctx, port.GateInput{Task: task, Lane: lane, Kind: port.InputLane, Input: sha, Summary: summary,
		Spec: x.spec})
	switch {
	case err != nil:
		return Result{Code: Fail, Line: err.Error()}
	case o.Kind == port.OutcomeRed:
		return Result{Code: Red, Line: o.Line}
	case o.Kind != port.OutcomeGreen:
		return Result{Code: Fail, Line: o.Line}
	}
	x.cand, x.gated = o.Candidate, o.Line
	t, err := s.Test(ctx, port.TestInput{Task: task, Lane: lane, SHA: o.Candidate.SHA, Target: o.Candidate.Target})
	switch {
	case err != nil:
		return Result{Code: Fail, Line: err.Error()}
	case t.Kind == port.OutcomeGreen:
		return Result{Code: OK, Line: t.Line}
	case t.Kind == port.OutcomeRed:
		return Result{Code: Red, Line: t.Line}
	}
	return Result{Code: Fail, Line: t.Line}
}

func (x *fx) nightly() Result {
	x.t.Helper()
	x.out.Reset()
	return Nightly(x.opts())
}

// is fails unless r has the code and its line matches re.
func (x *fx) is(r Result, code int, re string) {
	x.t.Helper()
	if r.Code != code || !regexp.MustCompile(re).MatchString(r.Line) {
		x.t.Fatalf("result %d %q; want %d /%s/\noutput:\n%s", r.Code, r.Line, code, re, x.out.String())
	}
}

func (x *fx) eq(got, want, what string) {
	x.t.Helper()
	if got != want {
		x.t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

func (x *fx) match(got, re, what string) {
	x.t.Helper()
	if !regexp.MustCompile(re).MatchString(got) {
		x.t.Errorf("%s: %q does not match /%s/", what, got, re)
	}
}

func (x *fx) contains(got, want, what string) {
	x.t.Helper()
	if !strings.Contains(got, want) {
		x.t.Errorf("%s: %q lacks %q", what, got, want)
	}
}

func (x *fx) lacks(got, want, what string) {
	x.t.Helper()
	if strings.Contains(got, want) {
		x.t.Errorf("%s: %q holds %q", what, got, want)
	}
}

func (x *fx) gone(path, what string) {
	x.t.Helper()
	if x.Exists(path) {
		x.t.Errorf("%s: %s exists", what, path)
	}
}

func (x *fx) there(path, what string) {
	x.t.Helper()
	if !x.Exists(path) {
		x.t.Errorf("%s: no %s", what, path)
	}
}

func (x *fx) head(dir string) string { x.t.Helper(); return x.Git(dir, "rev-parse", "HEAD") }

// mergeHead reports whether the scratch has a MERGE_HEAD.
func (x *fx) mergeHead() bool {
	return gitOK(x.scratch, "rev-parse", "-q", "--verify", "MERGE_HEAD")
}

// clean fails unless the scratch has no MERGE_HEAD and nothing uncommitted.
func (x *fx) clean(what string) {
	x.t.Helper()
	if x.mergeHead() {
		x.t.Errorf("%s: MERGE_HEAD in the scratch", what)
	}
	x.eq(x.Git(x.scratch, "status", "--porcelain"), "", what+": scratch clean")
}

// change writes a new worker change into lane L-01.
func (x *fx) change(name, text string) { x.Write(filepath.Join(x.lane, name), text+"\n") }

// nightlyDir is nightly's own worktree, <merge.scratch_dir>-nightly.
func (x *fx) nightlyDir() string { return x.scratch + "-nightly" }

// window is .obor/state/nightly.flock, the nightly lock.
func (x *fx) window() string { return x.Path(".obor/state/nightly.flock") }

// holdWindow takes the nightly lock as another obor nightly would and returns its release.
func (x *fx) holdWindow() func() {
	x.t.Helper()
	lock := project.NewLock(x.window())
	if ok, err := lock.Try(); !ok || err != nil {
		x.t.Fatalf("precondition: take nightly.flock: %v, %v", ok, err)
	}
	var once sync.Once
	release := func() { once.Do(func() { lock.Unlock() }) }
	x.t.Cleanup(release)
	return release
}

// stub writes an executable script under Base/bin and returns its path.
func (x *fx) stub(name, body string) string {
	path := x.Write(filepath.Join(x.Base, "bin", name), "#!/bin/sh\n"+body+"\n")
	os.Chmod(path, 0o755)
	return path
}

// waitFor polls until path exists (max 10 s).
func (x *fx) waitFor(path string) {
	x.t.Helper()
	for range 500 {
		if x.Exists(path) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	x.t.Fatalf("no %s after 10 s\noutput:\n%s", path, x.out.String())
}

// pin is the sha refs/obor/candidate/<task> pins, "" when none.
func (x *fx) pin(task string) string {
	x.t.Helper()
	if !gitOK(x.Root, "rev-parse", "-q", "--verify", candidateRefs+task) {
		return ""
	}
	return x.Git(x.Root, "rev-parse", candidateRefs+task)
}

func gitOK(dir string, args ...string) bool {
	s := &session{}
	return s.ok(dir, args...)
}

// syncBuffer is a bytes.Buffer safe for a merge writing while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func (b *syncBuffer) Reset()         { b.mu.Lock(); defer b.mu.Unlock(); b.b.Reset() }

// q quotes a path for a regexp.
func q(s string) string { return regexp.QuoteMeta(s) }

// ref is the sha ref points at, "" when none.
func (x *fx) ref(ref string) string {
	if !gitOK(x.Root, "rev-parse", "-q", "--verify", ref) {
		return ""
	}
	return x.Git(x.Root, "rev-parse", ref)
}
