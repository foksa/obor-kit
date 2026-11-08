package run

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

type fx struct {
	p     *testkit.Project
	lane  string
	slots string
	cfg   *config.Config
}

// setup is a project with lane L-01 and commands, its slots in a temp dir (slots.max 2, project "proj"); the lane
// ignores .task-run, as obor setup makes it.
func setup(t *testing.T, commands string) *fx {
	t.Helper()
	p := testkit.New(t)
	f := &fx{p: p, lane: p.Lane("L-01", "task/T-01"), slots: filepath.Join(p.Base, "slots")}
	p.Write(filepath.Join(f.lane, ".gitignore"), ".task-done.md\n.task-run\n")
	p.Config(testkit.Set("commands", commands), testkit.Set("slots", map[string]any{"dir": f.slots, "max": 2}))
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	return f
}

type result struct {
	rc          int
	out, errOut string
}

// run runs name in dir with env (VAR=value: both the obor env and the command's) and args.
func (f *fx) run(t *testing.T, dir string, env []string, interrupt <-chan int, name string, args ...string) result {
	t.Helper()
	var errOut bytes.Buffer
	return f.runErr(t, dir, env, interrupt, &errOut, name, args...)
}

// runErr is run with the caller's stderr writer (which must be safe to read while the command runs).
func (f *fx) runErr(t *testing.T, dir string, env []string, interrupt <-chan int, stderr interface {
	io.Writer
	String() string
}, name string, args ...string) result {
	t.Helper()
	var out bytes.Buffer
	errOut := stderr
	// No CHECK_* of the caller: under obor merge, CHECK_SLOT_HELD names the merge's own slot.
	var environ []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CHECK_") {
			environ = append(environ, kv)
		}
	}
	o := Options{Cfg: f.cfg, Env: f.p.Env(env...), Environ: append(environ, env...), Dir: dir, Name: name,
		Args: args, Stdout: &out, Stderr: errOut, PID: os.Getpid(), Interrupted: interrupt}
	s := SlotsFor(f.cfg, o.Env)
	s.Poll, s.Report = 20*time.Millisecond, time.Hour
	o.Slots = &s
	return result{Run(o), out.String(), errOut.String()}
}

// bash run.test.sh case_rc_passthrough, case_rc_zero_and_unknown: the command's rc, run from the lane root with the
// args appended; an unknown name is rc 2.
func TestRunRcArgs(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"t": "pwd -P > out.txt; bash tool.sh", "ok": {"cmd": "echo hi", "timeout": 5}}`)
	f.p.Write(filepath.Join(f.lane, "tool.sh"), `printf "<%s>" "$@" >> out.txt; exit 7`+"\n")
	os.Mkdir(filepath.Join(f.lane, "sub"), 0o755)
	if r := f.run(t, filepath.Join(f.lane, "sub"), nil, nil, "t", "a b", "c"); r.rc != 7 {
		t.Errorf("rc: %+v", r)
	}
	if got := f.p.Read(filepath.Join(f.lane, "out.txt")); got != f.lane+"\n<a b><c>" {
		t.Errorf("ran from the lane root with args appended: %q", got)
	}
	if r := f.run(t, f.lane, nil, nil, "ok"); r.rc != 0 || r.out != "hi\n" {
		t.Errorf("ok: %+v", r)
	}
	if r := f.run(t, f.lane, nil, nil, "nope"); r.rc != 2 || r.out != "FAIL no commands.nope in the config\n" {
		t.Errorf("unknown: %+v", r)
	}
}

// bash run.test.sh case_timeout: a command over its timeout is rc 124 with a note, ended at the timeout.
// Slow: it waits out config's whole-second 1 s timeout (T-1006).
func TestRunTimeout(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"slow": {"cmd": "sleep 5", "timeout": 1}}`)
	start := time.Now()
	if r := f.run(t, f.lane, nil, nil, "slow"); r.rc != 124 || !strings.Contains(r.errOut, "slow timed out after 1 s") {
		t.Errorf("timeout: %+v", r)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("the timeout took %v", time.Since(start))
	}
}

// bash run.test.sh case_lane_marker, case_lane_marker_unwritable: in a lane, <lane>/.task-run holds the pid during
// the command, removed after (also after a failure); the main checkout writes none; an unwritable lane root → a note,
// the command still runs.
func TestRunLaneMarker(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"m": "cat .task-run > seen.txt 2>/dev/null; ls -a > ls.txt; exit 3"}`)
	if r := f.run(t, f.lane, nil, nil, "m"); r.rc != 3 {
		t.Fatalf("rc: %+v", r)
	}
	if got := f.p.Read(filepath.Join(f.lane, "seen.txt")); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Errorf("marker during the command: %q", got)
	}
	if f.p.Exists(filepath.Join(f.lane, ".task-run")) {
		t.Error("marker left after a failure")
	}
	if r := f.run(t, f.p.Root, nil, nil, "m"); r.rc != 3 || strings.Contains(f.p.Read("ls.txt"), ".task-run") {
		t.Errorf("main checkout wrote a marker: %+v", r)
	}
	os.Chmod(f.lane, 0o555)
	defer os.Chmod(f.lane, 0o755)
	if r := f.run(t, f.lane, nil, nil, "m"); r.rc != 3 || !strings.Contains(r.errOut, "note: cannot write "+f.lane+"/.task-run") {
		t.Errorf("unwritable lane root: %+v", r)
	}
}

// T-214: in a lane whose .task-run is not ignored, one warning line and the command still runs; ignored: no warning.
func TestRunWarnsMarkerNotIgnored(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"m": "echo ran"}`)
	const warning = "warning: .task-run is not in .gitignore — run obor setup (or add the line)\n"
	f.p.Write(filepath.Join(f.lane, ".gitignore"), ".task-done.md\n")
	if r := f.run(t, f.lane, nil, nil, "m"); r.rc != 0 || r.out != "ran\n" || strings.Count(r.errOut, warning) != 1 {
		t.Errorf("not ignored: %+v", r)
	}
	f.p.Write(filepath.Join(f.lane, ".gitignore"), ".task-done.md\n.task-run\n")
	if r := f.run(t, f.lane, nil, nil, "m"); r.rc != 0 || r.out != "ran\n" || r.errOut != "" {
		t.Errorf("ignored: %+v", r)
	}
}

// bash run.test.sh case_heavy_holds_slot, case_not_heavy_no_slot_held, case_heavy_nested_reuses_slot: a heavy command
// holds slot-0 while it runs (and gets CHECK_SLOT_HELD = it), released after; not heavy: no slot, no CHECK_SLOT_HELD.
// A heavy command gets CHECK_CPUS (slots.cpus here, else cores / slots.max), a light one does not.
func TestRunHeavyHoldsSlot(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"heavy": {"cmd": "ls \"$SLOTS\" > seen.txt; echo \"held=$CHECK_SLOT_HELD cpus=$CHECK_CPUS\" >> seen.txt", "heavy": true},
		"light": "echo \"held=$CHECK_SLOT_HELD cpus=$CHECK_CPUS\" > seen.txt"}`)
	env := []string{"SLOTS=" + f.slots}
	cpus := strconv.Itoa(max(1, runtime.NumCPU()/2))
	if r := f.run(t, f.lane, env, nil, "heavy"); r.rc != 0 {
		t.Fatalf("heavy: %+v", r)
	}
	if got, want := f.p.Read(filepath.Join(f.lane, "seen.txt")), "slot-0\nheld="+f.slots+"/slot-0 cpus="+cpus+"\n"; got != want {
		t.Errorf("during the command: %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(f.slots, "slot-0")); err == nil {
		t.Error("slot not released")
	}
	f.cfg.Data.Slots.CPUs = new(int)
	*f.cfg.Data.Slots.CPUs = 3
	if r := f.run(t, f.lane, env, nil, "heavy"); r.rc != 0 ||
		!strings.Contains(f.p.Read(filepath.Join(f.lane, "seen.txt")), "cpus=3\n") {
		t.Errorf("heavy, slots.cpus 3: %+v %q", r, f.p.Read(filepath.Join(f.lane, "seen.txt")))
	}
	if r := f.run(t, f.lane, env, nil, "light"); r.rc != 0 || f.p.Read(filepath.Join(f.lane, "seen.txt")) != "held= cpus=\n" {
		t.Errorf("not heavy: %+v %q", r, f.p.Read(filepath.Join(f.lane, "seen.txt")))
	}
}

// bash run.test.sh case_heavy_waits_for_slot, case_heavy_waits_for_merge_marker, case_heavy_merge_prio_waits: a full
// machine (slots.max live holders) or a young live merge marker of the project → a wait line, then the run once it
// frees; under CHECK_PRIORITY=merge the project's merge marker is claimed (another live one waited for).
func TestRunHeavyWaits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, prio string
		files      []string
		wait       string
		claims     string
	}{
		{name: "slots full", files: []string{"slot-0", "slot-1"}, wait: "waiting for a check slot (2 running)", claims: "slot-0"},
		{name: "merge marker", files: []string{"merge-running-proj"}, wait: "waiting: a merge check is running (pid ", claims: "slot-0"},
		{name: "merge priority", prio: "merge", files: []string{"merge-running-proj"}, wait: "waiting for the other merge check (pid ",
			claims: "merge-running-proj"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := setup(t, `{"heavy": {"cmd": "ls \"$SLOTS\" > seen.txt", "heavy": true}}`)
			os.MkdirAll(f.slots, 0o755)
			for _, name := range c.files {
				pid, _ := testkit.Sleeper(t)
				os.WriteFile(filepath.Join(f.slots, name), []byte(strconv.Itoa(pid)+" "+strconv.FormatInt(time.Now().UnixMilli(), 10)+"\n"), 0o644)
			}
			// The holders finish once the run has said it waits (not on a timer: under load the run may start late).
			errOut := &lockedBuffer{}
			go func() {
				for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
					if strings.Contains(errOut.String(), c.wait) {
						break
					}
				}
				for _, name := range c.files {
					os.Remove(filepath.Join(f.slots, name))
				}
			}()
			env := []string{"SLOTS=" + f.slots, "CHECK_PRIORITY=" + c.prio}
			r := f.runErr(t, f.lane, env, nil, errOut, "heavy")
			if r.rc != 0 || !strings.Contains(r.errOut, c.wait) {
				t.Errorf("run: %+v, want the wait line %q", r, c.wait)
			}
			if seen := f.p.Read(filepath.Join(f.lane, "seen.txt")); !strings.Contains(seen, c.claims) {
				t.Errorf("claimed: %q, want %s", seen, c.claims)
			}
			if _, err := os.Stat(filepath.Join(f.slots, c.claims)); err == nil {
				t.Errorf("%s not released", c.claims)
			}
		})
	}
}

// bash run.test.sh case_heavy_inside_merge_check, case_heavy_slot_held_live_no_slot, case_heavy_slot_held_dead_acquires:
// CHECK_SLOT_HELD naming a live holder → no slot claimed, the file untouched; a dead holder → a normal acquire.
func TestRunHeavySlotHeld(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"heavy": {"cmd": "ls \"$SLOTS\" > seen.txt", "heavy": true}}`)
	os.MkdirAll(f.slots, 0o755)
	held := filepath.Join(f.slots, "merge-running-proj")
	parent, _ := testkit.Sleeper(t) // the merge holding the marker
	content := strconv.Itoa(parent) + " 1759917600123\n"
	os.WriteFile(held, []byte(content), 0o644)
	env := []string{"SLOTS=" + f.slots, "CHECK_PRIORITY=merge", "CHECK_SLOT_HELD=" + held}
	if r := f.run(t, f.lane, env, nil, "heavy"); r.rc != 0 || r.errOut != "" ||
		f.p.Read(filepath.Join(f.lane, "seen.txt")) != "merge-running-proj\n" || f.p.Read(held) != content {
		t.Errorf("live hand-off: %+v, saw %q, marker %q", r, f.p.Read(filepath.Join(f.lane, "seen.txt")), f.p.Read(held))
	}
	os.WriteFile(held, []byte(strconv.Itoa(testkit.DeadPID(t))+" 1\n"), 0o644)
	env = []string{"SLOTS=" + f.slots, "CHECK_SLOT_HELD=" + held}
	if r := f.run(t, f.lane, env, nil, "heavy"); r.rc != 0 || !strings.Contains(f.p.Read(filepath.Join(f.lane, "seen.txt")), "slot-0") {
		t.Errorf("dead hand-off: %+v, saw %q", r, f.p.Read(filepath.Join(f.lane, "seen.txt")))
	}
}

// bash run.test.sh case_lane_marker_signal, case_heavy_signal_releases: a signal while the command runs → the command
// group is killed, the slot and marker released, the signal's code returned.
func TestRunSignalReleases(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"heavy": {"cmd": "touch started; sleep 30", "heavy": true}}`)
	interrupt := make(chan int, 1)
	go func() {
		for !f.p.Exists(filepath.Join(f.lane, "started")) {
			time.Sleep(10 * time.Millisecond)
		}
		interrupt <- 143
	}()
	start := time.Now()
	r := f.run(t, f.lane, nil, interrupt, "heavy")
	if r.rc != 143 || time.Since(start) > 10*time.Second {
		t.Errorf("TERM: %+v after %v", r, time.Since(start))
	}
	if f.p.Exists(filepath.Join(f.lane, ".task-run")) {
		t.Error("marker left")
	}
	if _, err := os.Stat(filepath.Join(f.slots, "slot-0")); err == nil {
		t.Error("slot left")
	}
}

// childPID waits for the pid a test command's child writes to path.
func childPID(t *testing.T, path string) int {
	t.Helper()
	b, _ := os.ReadFile(path)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 1 {
		t.Fatalf("the child wrote no pid: %q", b)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// gone: pid is no longer running within a second (the init process reaps the orphan).
func gone(pid int) bool {
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// RV-26 #2: nothing of the command's group outlives Execute: a TERM-ignoring child at the timeout is KILLed after the
// grace.
// Slow: it waits a config command's 3 s timeout plus project.Grace before the KILL (T-1006).
func TestExecuteLeavesNoGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ignorer := `bash -c 'trap "" TERM; echo $$ > pid; while :; do sleep 0.1; done' &
until [ -s pid ]; do sleep 0.05; done; wait`
	start := time.Now()
	var out bytes.Buffer
	// 3 s: under a loaded full suite the child bash may take over a second to write its pid (T-438's gate).
	rc, timedOut := Execute(context.Background(), config.Command{Name: "t", Cmd: ignorer, Timeout: 3}, dir, nil, nil, &out, &out)
	pid := childPID(t, filepath.Join(dir, "pid"))
	if rc == 0 || !timedOut {
		t.Errorf("timed out: rc %d timedOut %v %q", rc, timedOut, out.String())
	}
	if !gone(pid) {
		t.Errorf("the TERM-ignoring child %d survived the timeout", pid)
	}
	if d := time.Since(start); d > 3*time.Second+project.Grace+2*time.Second {
		t.Errorf("KILL after the grace, took %v", d)
	}
}

// RV-26 #2, RV-140: a descendant of a command that exited normally does not outlive Execute.
func TestExecuteLeavesNoDescendant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	survivor := `bash -c 'trap "" TERM; echo $$ > pid; exec sleep 30' > /dev/null 2>&1 &
until [ -s pid ]; do sleep 0.05; done; echo done`
	var out bytes.Buffer
	rc, timedOut := Execute(context.Background(), config.Command{Name: "t", Cmd: survivor, Timeout: 10}, dir, nil, nil, &out, &out)
	pid := childPID(t, filepath.Join(dir, "pid"))
	if rc != 0 || timedOut || out.String() != "done\n" {
		t.Errorf("exited normally: rc %d timedOut %v %q", rc, timedOut, out.String())
	}
	if !gone(pid) {
		t.Errorf("the descendant %d of a normally exiting command survived", pid)
	}
}

// laneIndex is the lane's index file, its HEAD and its staged files: what computing a tree must not change.
func laneIndex(t *testing.T, f *fx) string {
	t.Helper()
	idx := strings.TrimSpace(f.p.Git(f.lane, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	b, _ := os.ReadFile(idx)
	return f.p.Git(f.lane, "rev-parse", "HEAD") + "|" + f.p.Git(f.lane, "diff", "--cached", "--name-only") + "|" + string(b)
}

// obor run exports OBOR_CHANGED_FILES in a lane: committed and uncommitted (also untracked) changes against the merge
// base with the target, the done file not; the lane's index and HEAD stay as they were.
func TestRunExportsChangedFiles(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"files": {"cmd": "cat \"$OBOR_CHANGED_FILES\"", "heavy": true}, "light": "echo \"[$OBOR_CHANGED_FILES]\""}`)
	f.p.Write(filepath.Join(f.lane, ".gitignore"), ".task-run\n") // the done file is not ignored: only the tree leaves it out
	f.p.Commit(f.lane)                                            // work.txt, committed
	f.p.Write(filepath.Join(f.lane, "shared.txt"), "tracked edit\n")
	f.p.Git(f.lane, "add", "shared.txt") // staged
	f.p.Write(filepath.Join(f.lane, "new.txt"), "untracked\n")
	f.p.Submit("L-01")
	before := laneIndex(t, f)
	if !strings.Contains(before, "shared.txt") {
		t.Fatalf("precondition: shared.txt staged: %q", before)
	}

	r := f.run(t, f.lane, nil, nil, "files")
	if r.rc != 0 {
		t.Fatalf("rc: %+v", r)
	}
	got := strings.Fields(r.out)
	want := []string{".gitignore", "new.txt", "shared.txt", "work.txt"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("changed files %v, want %v (committed + staged + untracked, no done file)", got, want)
	}
	if after := laneIndex(t, f); after != before {
		t.Errorf("the lane's index or HEAD changed while computing the tree:\nbefore %q\nafter  %q", before, after)
	}
	if r := f.run(t, f.lane, nil, nil, "light"); r.out != "[]\n" {
		t.Errorf("a light command gets no changed-file list: %q", r.out)
	}
	if r := f.run(t, f.p.Root, nil, nil, "files"); r.out != "" {
		t.Errorf("outside a lane there is no list: %+v", r)
	}
}

// lockedBuffer is a bytes.Buffer safe for a reader while the run writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }
