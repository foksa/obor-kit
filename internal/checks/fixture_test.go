package checks

import (
	"bytes"
	"context"
	"os"
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

func TestMain(m *testing.M) { testkit.Main(m) }

const trailer = "Co-Authored-By: Test <test@example.invalid>"

// fx is the one fixture of these tests (bash merge.test.sh setup): .obor/state/ gitignored (the done file not), a
// 10-line shared.txt, lane L-01 on task/T-05 with an uncommitted feature.txt and a leftover done file (never committed), an idle
// lane L-02 on task/T-20, merge.trailer / summary_regex / fail_grep set, a per-test check slot dir. The scratch is the
// default proj-wt/merge.
type fx struct {
	*testkit.Project
	t                    *testing.T
	lane, lane2, scratch string
	slots                string
	out                  syncBuffer
	env                  []string // project.Env (OBOR_GATE, OBOR_FULL)
	environ              []string // more command environment
	load                 []float64
	slotLoad             float64        // > 0: the slots see this machine load above their limit, their load wait cut short (T-720)
	cand                 port.Candidate // the last green gate's (gate)
	spec                 string         // the task's stored spec gate passes (GateInput.Spec; "": none)
	sig                  chan int
	mu                   sync.Mutex
}

func newFx(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	p := testkit.New(t)
	p.Write(".gitignore", ".obor/state/\n")
	p.Write("shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "fixture")
	x := &fx{Project: p, t: t, sig: make(chan int, 1)}
	x.lane = p.Lane("L-01", "task/T-05")
	x.lane2 = p.Lane("L-02", "task/T-20")
	p.Config(append([]func(map[string]any){testkit.Set("merge", map[string]any{"trailer": trailer,
		"summary_regex": "[0-9]+ pass|[0-9]+ fail", "fail_grep": "Expected"})}, edits...)...)
	x.scratch = filepath.Join(p.Base, "proj-wt", "merge")
	x.slots = filepath.Join(p.Base, "slots")
	if err := os.MkdirAll(p.Path(".obor/state"), 0o755); err != nil { // serve's state dir (its database lives there)
		t.Fatal(err)
	}
	p.Write(filepath.Join(x.lane, "feature.txt"), "feature\n")
	return x
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
	// Without what an obor merge running this test exports (CHECK_PRIORITY, CHECK_SLOT_HELD, OBOR_MERGE, OBOR_GATE):
	// the fixture's commands would see the outer merge's.
	environ := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		for _, k := range []string{"CHECK_PRIORITY=", "CHECK_SLOT_HELD=", "OBOR_MERGE=", "OBOR_GATE="} {
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
	if l := x.slotLoad; l > 0 {
		slots.LoadLimit, slots.LoadCap, slots.Load = 1, time.Millisecond, func() (float64, bool) { return l, true }
	}
	return Options{
		Cfg: cfg, Env: x.Env(x.env...), Environ: append(environ, x.environ...), Out: &x.out, PID: os.Getpid(),
		Interrupted: x.sig, LoadPoll: 10 * time.Millisecond, callCtx: callCtx,
		Slots: slots,
		Load: func() (float64, bool) {
			x.mu.Lock()
			defer x.mu.Unlock()
			if len(x.load) == 0 {
				return 0, false
			}
			v := x.load[0]
			if len(x.load) > 1 {
				x.load = x.load[1:]
			}
			return v, true
		},
	}
}

// gate submits lane's work for task (worker label "worker") and gates it in serve's worktree, as obor serve does
// (Scratch.Submit, then a lane Gate): green is OK, red Red, fail and conflict Fail, each with the gate's line; a
// refusal or error is Fail with its text. A green gate's candidate is x.cand.
func (x *fx) gate(lane, task, summary string) Result {
	x.t.Helper()
	return x.gateCtx(context.Background(), lane, task, summary)
}

// gateCtx is gate whose Gate ends with ctx.
func (x *fx) gateCtx(ctx context.Context, lane, task, summary string) Result {
	x.out.Reset()
	s := Scratch{x.opts()}
	sha, err := s.Submit(context.Background(), task, "1", lane, summary, "worker")
	if err != nil {
		return Result{Code: Fail, Line: err.Error()}
	}
	o, err := s.Gate(ctx, port.GateInput{Task: task, Lane: lane, Kind: port.InputLane, Input: sha, Summary: summary,
		Spec: x.spec})
	switch {
	case err != nil:
		return Result{Code: Fail, Line: err.Error()}
	case o.Kind == port.OutcomeGreen:
		x.cand = o.Candidate
		return Result{Code: OK, Line: o.Line}
	case o.Kind == port.OutcomeRed:
		return Result{Code: Red, Line: o.Line}
	}
	return Result{Code: Fail, Line: o.Line}
}

func (x *fx) nightly(label string) Result {
	x.t.Helper()
	x.out.Reset()
	return Nightly(x.opts(), label)
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

// waitOut polls until the output holds text (max 10 s).
func (x *fx) waitOut(text string) {
	x.t.Helper()
	for range 500 {
		if strings.Contains(x.out.String(), text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	x.t.Fatalf("no %q in the output after 10 s:\n%s", text, x.out.String())
}

// ref is the sha ref points at, "" when none.
func (x *fx) ref(ref string) string {
	if !gitOK(x.Root, "rev-parse", "-q", "--verify", ref) {
		return ""
	}
	return x.Git(x.Root, "rev-parse", ref)
}
