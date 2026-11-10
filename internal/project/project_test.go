package project

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// fixture: proj (main checkout, committed config) with lanes L-01 and review, and merge (the scratch), under proj-wt.
func fixture(t *testing.T, cfg string) (root, wt string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("OBOR_ROLE", "")
	t.Setenv("OBOR_LANE", "")
	base := Physical(t.TempDir())
	root, wt = filepath.Join(base, "proj"), filepath.Join(base, "proj-wt")
	os.MkdirAll(filepath.Join(root, ".obor"), 0o755)
	os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644)
	run(t, base, "git", "-c", "init.defaultBranch=main", "init", "-q", root)
	run(t, root, "git", "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "init")
	for _, l := range []string{"L-01", "review", "merge", "reviewer", "deep_dive", "integrator"} {
		run(t, root, "git", "worktree", "add", "-q", "--detach", filepath.Join(wt, l), "main")
	}
	return root, wt
}

func role(t *testing.T, dir string) Role {
	t.Helper()
	r, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	return DetectRole(r, cfg, OSEnv)
}

func TestFindFromALaneGivesTheMainCheckout(t *testing.T) {
	root, wt := fixture(t, `{"version":2,"project":{"agent_prefix":"pj"}}`)
	r, err := Find(filepath.Join(wt, "L-01"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Root != root || r.Top != filepath.Join(wt, "L-01") || !r.Linked() {
		t.Errorf("Find = %+v", r)
	}
	if r, _ := Find(root); r.Linked() || r.Root != root {
		t.Errorf("main checkout: %+v", r)
	}
	if _, err := Find(t.TempDir()); err == nil {
		t.Error("not a repo: no error")
	}
	// Find reads git's files itself: with no git on PATH it still finds a lane, from a nested dir.
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if err := os.MkdirAll(filepath.Join(wt, "L-01", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err = Find(filepath.Join(wt, "L-01", "subdir"))
	if err != nil || r.Root != root || r.Top != filepath.Join(wt, "L-01") {
		t.Fatalf("Find from nested lane = %+v, %v", r, err)
	}
}

func TestFindFallsBackToOneGitRevParse(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	common := filepath.Join(root, ".git-common")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(base, "git-calls")
	gitStub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GIT_CALLS\"\nprintf '%s\\n' \"$GIT_TOP\" \"$GIT_DIR\" \"$GIT_COMMON\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(gitStub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GIT_CALLS", calls)
	t.Setenv("GIT_TOP", root)
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	t.Setenv("GIT_COMMON", common)
	r, err := Find(root)
	if err != nil || r.Top != Physical(root) || r.Root != Physical(filepath.Dir(common)) {
		t.Fatalf("Find fallback = %+v, %v", r, err)
	}
	b, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(b), "\n") != 1 || !strings.Contains(string(b), "rev-parse --show-toplevel --git-dir --git-common-dir") {
		t.Fatalf("git calls = %q, %v", b, err)
	}
}

func TestLockTryHolderUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "merge.flock")
	lock := NewLock(path)
	if ok, err := lock.Try(); err != nil || !ok {
		t.Fatalf("first Try = %v, %v", ok, err)
	}
	if got := NewLock(path).Holder(); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("Holder = %q, want this pid", got)
	}
	other := NewLock(path)
	if ok, err := other.Try(); err != nil || ok {
		t.Fatalf("second Lock Try = %v, %v, want false (held)", ok, err)
	}
	if ok, err := lock.Try(); err != nil || ok {
		t.Fatalf("holder's own second Try = %v, %v, want false", ok, err)
	}
	if err := other.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a non-holder's Unlock removed the lock file: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Unlock left the lock file: %v", err)
	}
	if ok, err := other.Try(); err != nil || !ok {
		t.Fatalf("Try after Unlock = %v, %v", ok, err)
	}
	other.Unlock()
}

// A holder killed with SIGKILL frees the lock at once: the next Try takes it, no staleness wait.
func TestLockKilledHolderFreesAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.flock")
	child := startChild(t, "lock", path)
	if ok, err := NewLock(path).Try(); err != nil || ok {
		t.Fatalf("Try while the child holds = %v, %v, want false", ok, err)
	}
	if got := NewLock(path).Holder(); got != strconv.Itoa(child.Process.Pid) {
		t.Errorf("Holder = %q, want the child's pid %d", got, child.Process.Pid)
	}
	child.Process.Kill()
	child.Wait()
	start := time.Now()
	lock := NewLock(path)
	if ok, err := lock.Try(); err != nil || !ok {
		t.Fatalf("first Try after SIGKILL = %v, %v, want true", ok, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Try took %s", d)
	}
	if got := lock.Holder(); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("Holder after taking = %q, want this pid", got)
	}
	lock.Unlock()
}

// Unlock removes the file while others may have it open: never two holders at once. The contenders are
// processes (the bug is between open file descriptions), each holding the lock for a moment while it owns an O_EXCL
// sentinel: a second holder fails to create it.
func TestLockOneHolderUnderContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.flock")
	var children []*exec.Cmd
	for range 8 {
		children = append(children, startChild(t, "contend", path))
	}
	if err := os.WriteFile(path+".go", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	took := 0
	for i, c := range children {
		err := c.Wait()
		var ee *exec.ExitError
		switch {
		case err == nil:
			took++
		case errors.As(err, &ee) && ee.ExitCode() == 5:
		case errors.As(err, &ee) && ee.ExitCode() == 4:
			t.Errorf("contender %d: two holders at once", i)
		default:
			t.Errorf("contender %d: %v", i, err)
		}
	}
	if took == 0 {
		t.Fatal("precondition: the lock was never taken")
	}
}

// startChild runs this test binary as a helper that takes a lock (mode lock) or registers an op (mode op) at arg,
// prints ready and blocks; killed at cleanup.
func startChild(t *testing.T, mode, arg string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperChild$")
	cmd.Env = append(os.Environ(), "OBOR_TEST_CHILD="+mode, "OBOR_TEST_CHILD_ARG="+arg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper = %q, %v", line, err)
	}
	return cmd
}

func TestHelperChild(t *testing.T) {
	arg := os.Getenv("OBOR_TEST_CHILD_ARG")
	// The lock and the handle stay referenced: a collected *os.File is closed, which frees its flock.
	var held any
	switch os.Getenv("OBOR_TEST_CHILD") {
	case "lock":
		lock := NewLock(arg)
		if ok, err := lock.Try(); !ok || err != nil {
			os.Exit(3)
		}
		held = lock
	case "contend":
		// After ready, once arg.go exists: 2000 Try rounds; a holder owns arg.held (O_EXCL) for 50 µs. Exit 0: took the
		// lock and never saw a second holder; 4: arg.held already existed (two holders); 5: never took it.
		fmt.Println("ready")
		for _, err := os.Stat(arg + ".go"); err != nil; _, err = os.Stat(arg + ".go") {
			time.Sleep(time.Millisecond)
		}
		took := false
		for range 2000 {
			lock := NewLock(arg)
			if ok, err := lock.Try(); err != nil {
				os.Exit(3)
			} else if !ok {
				continue
			}
			took = true
			f, err := os.OpenFile(arg+".held", os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				os.Exit(4)
			}
			f.Close()
			time.Sleep(50 * time.Microsecond)
			os.Remove(arg + ".held")
			lock.Unlock()
		}
		if !took {
			os.Exit(5)
		}
		os.Exit(0)
	default:
		t.Skip("helper process only")
	}
	fmt.Println("ready")
	runtime.GC()
	time.Sleep(time.Hour)
	runtime.KeepAlive(held)
}

func TestProjectLockWaitContextAndHolderLine(t *testing.T) {
	root := t.TempDir()
	state := StateDir(root)
	lock := NewLock(filepath.Join(state, "merge.lock"))
	var output bytes.Buffer
	lock.Output = &output
	if ok, err := lock.Try(); err != nil || !ok {
		t.Fatalf("Try = %v, %v", ok, err)
	}
	lock.reportHolder()
	if got, want := output.String(), fmt.Sprintf("waiting for merge.lock: pid %d\n", os.Getpid()); got != want {
		t.Errorf("holder line = %q, want %q", got, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := NewLock(lock.Path).Wait(ctx, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait error = %v, want context deadline", err)
	}
	if err := NewLock(lock.Path).Wait(context.Background(), 0); !errors.Is(err, ErrLockTimeout) {
		t.Errorf("Wait(0) while held = %v, want ErrLockTimeout", err)
	}
	go func() { time.Sleep(200 * time.Millisecond); lock.Unlock() }()
	waiter := NewLock(lock.Path)
	if err := waiter.Wait(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("Wait once freed = %v", err)
	}
	if got := waiter.Holder(); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("Holder after Wait = %q", got)
	}
	waiter.Unlock()
}

func TestDetectRoleCwdEnvAndLanes(t *testing.T) {
	root, wt := fixture(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"roles":{"reviewer":"opus","deep_dive":"opus"}}}`)
	if got := role(t, filepath.Join(wt, "L-01")); got != (Role{"worker", "L-01", "cwd"}) {
		t.Errorf("lane: %+v", got)
	}
	// no name under lanes_dir is reserved for a role worktree (a leftover review worktree is a
	// lane of that name).
	if got := role(t, filepath.Join(wt, "review")); got != (Role{"worker", "review", "cwd"}) {
		t.Errorf("leftover worktree under lanes_dir: %+v", got)
	}
	if got := role(t, filepath.Join(wt, "merge")); got != (Role{"none", "", "none"}) {
		t.Errorf("merge scratch: %+v", got)
	}
	// the reviewer's, the deep dive's and the integrator's role worktrees are no lanes: their env role stands
	// there.
	for _, r := range []string{"reviewer", "deep_dive", "integrator"} {

		if got := role(t, filepath.Join(wt, r)); got != (Role{"none", "", "none"}) {
			t.Errorf("%s's role worktree, no env: %+v, want no lane", r, got)
		}
		t.Setenv("OBOR_ROLE", r)
		if got := role(t, filepath.Join(wt, r)); got != (Role{r, "", "env"}) {
			t.Errorf("%s in its role worktree: %+v", r, got)
		}
		t.Setenv("OBOR_ROLE", "")
	}

	if got := role(t, root); got != (Role{"none", "", "none"}) {
		t.Errorf("main checkout: %+v", got)
	}
	t.Setenv("OBOR_ROLE", "lead")
	if got := role(t, root); got != (Role{"lead", "", "env"}) {
		t.Errorf("env: %+v", got)
	}
	if got := role(t, filepath.Join(wt, "L-01")); got != (Role{"worker", "L-01", "env+cwd"}) {
		t.Errorf("env+cwd: %+v", got)
	}
	t.Setenv("OBOR_ROLE", "advisor")
	if got := role(t, root); got != (Role{"advisor", "", "env"}) {
		t.Errorf("table env role: %+v", got)
	}
	if got := role(t, filepath.Join(wt, "L-01")); got != (Role{"worker", "L-01", "env+cwd"}) {
		t.Errorf("table env role + worker cwd: %+v", got)
	}
	// the owner's drive agent in a lane is the driver, not its worker; out of a lane it is no lane's.
	t.Setenv("OBOR_ROLE", "driver")
	if got := role(t, filepath.Join(wt, "L-01")); got != (Role{"driver", "L-01", "env+cwd"}) {
		t.Errorf("driver in its lane: %+v", got)
	}
	if got := role(t, root); got != (Role{"driver", "", "env"}) {
		t.Errorf("driver env in the main checkout: %+v", got)
	}
}

// Self is the one "who am I": role + lane (cwd wins) + the current config → the derived name, for a lane
// worker (env or none: OpenCode's shared hook process), the drive agent, each role; a stale OBOR_AGENT is never read;
// a prefix change applies at once (nothing stored).
func TestSelfDerivesTheName(t *testing.T) {
	root, wt := fixture(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"roles":{"reviewer":"opus","deep_dive":"opus","advisor":"opus"}}}`)
	lane := filepath.Join(wt, "L-01")
	self := func(dir string, env map[string]string) string {
		t.Helper()
		r, err := Find(dir)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(r.Root)
		if err != nil {
			t.Fatal(err)
		}
		return Self(r, cfg, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	}
	stale := "pj-old-name"
	for _, c := range []struct {
		what, dir string
		env       map[string]string
		want      string
	}{
		{"lane worker, no env (opencode's hook process)", lane, nil, "pj-l-01"},
		{"lane worker, its env and a stale OBOR_AGENT", lane, map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", "OBOR_AGENT": stale}, "pj-l-01"},
		{"lane cwd wins over a role env", lane, map[string]string{"OBOR_ROLE": "lead", "OBOR_AGENT": stale}, "pj-l-01"},
		{"drive agent in its lane", lane, map[string]string{"OBOR_ROLE": "driver", "OBOR_LANE": "L-01", "OBOR_AGENT": stale}, "pj-l-01-drive"},
		{"lead", root, map[string]string{"OBOR_ROLE": "lead", "OBOR_AGENT": stale}, "pj-lead"},
		{"integrator", root, map[string]string{"OBOR_ROLE": "integrator"}, "pj-int"},
		{"reviewer", root, map[string]string{"OBOR_ROLE": "reviewer"}, "pj-rev"},
		{"deep dive", root, map[string]string{"OBOR_ROLE": "deep_dive"}, "pj-deep-dive"},
		{"advisor", root, map[string]string{"OBOR_ROLE": "advisor"}, "pj-advisor"},
		{"researcher", root, map[string]string{"OBOR_ROLE": "researcher"}, "pj-researcher"},
		{"worker by env outside its lane", root, map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01"}, "pj-l-01"},
		{"no role (the owner), a stale OBOR_AGENT only", root, map[string]string{"OBOR_AGENT": stale}, ""},
		{"a role outside the table", root, map[string]string{"OBOR_ROLE": "boss", "OBOR_AGENT": stale}, ""},
		{"a driver with no lane", root, map[string]string{"OBOR_ROLE": "driver"}, ""},
	} {
		if got := self(c.dir, c.env); got != c.want {
			t.Errorf("%s: Self = %q, want %q", c.what, got, c.want)
		}
	}
	// A rename is the config's: the next call derives the new name (no stored copy to go stale).
	os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"version":2,"project":{"agent_prefix":"nx"}}`), 0o644)
	if got := self(root, map[string]string{"OBOR_ROLE": "lead", "OBOR_AGENT": "pj-lead"}); got != "nx-lead" {
		t.Errorf("after a prefix change: Self = %q, want nx-lead", got)
	}
	if got := self(lane, nil); got != "nx-l-01" {
		t.Errorf("lane after a prefix change: Self = %q, want nx-l-01", got)
	}
}

func TestContextTokensLastMainChainLine(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(file, []byte(`{"type":"assistant","timestamp":"2026-10-13T10:00:00Z","message":{"usage":{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":3}}}}
{"type":"assistant","isSidechain":true,"timestamp":"2026-10-13T10:01:00Z","message":{"usage":{"input_tokens":900}}}
not json
`), 0o644)
	ctx, at, ttl, ok := ContextTokens(file)
	if !ok || ctx != 6 || ttl != "5m" || at.Unix() != 1791885600 {
		t.Errorf("ContextTokens = %d %v %q %v, want 6 at 10:00 5m (the main-chain line, not the sidechain)", ctx, at, ttl, ok)
	}
	os.WriteFile(file, []byte(`{"type":"event_msg","timestamp":"2026-10-13T10:02:00.5Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":42}}}}`+"\n"), 0o644)
	if ctx, _, ttl, ok := ContextTokens(file); !ok || ctx != 42 || ttl != "30m" {
		t.Errorf("ContextTokens = %d %q %v, want 42 30m (the Codex event)", ctx, ttl, ok)
	}
	os.WriteFile(file, []byte(`{"type":"user","timestamp":"2026-10-13T10:00:00Z"}`+"\n"), 0o644)
	if _, _, _, ok := ContextTokens(file); ok {
		t.Error("a transcript without a usage line must not count")
	}
	os.WriteFile(file, []byte(`{"type":"assistant","timestamp":"bad","message":{"usage":{"input_tokens":1}}}`+"\n"), 0o644)
	if _, _, _, ok := ContextTokens(file); ok {
		t.Error("a bad timestamp must not count")
	}
}

// writeTail writes lines (each + "\n") to a temp file and returns its path.
func writeTail(t *testing.T, lines ...string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "t.jsonl")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// TailLines reads backwards at most tailBudget bytes; a line cut by the budget is dropped.
func TestTailLinesBoundedAndLinear(t *testing.T) {
	t.Parallel()
	usage := `{"type":"assistant","timestamp":"2026-10-13T10:00:00Z","message":{"usage":{"input_tokens":7}}}`
	huge := strings.Repeat("x", 32<<20)
	file := writeTail(t, usage, huge)
	start := time.Now()
	lines, err := TailLines(file, 400)
	if err != nil || len(lines) != 0 {
		t.Errorf("a 32 MiB last line: %d lines (err %v), want none (the cut line is dropped, the usage line is beyond the budget)", len(lines), err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a 32 MiB last line took %s", d)
	}
	if _, _, _, ok := ContextTokens(file); ok {
		t.Error("ContextTokens: the usage line lies beyond the budget → ok=false")
	}
	big := strings.Repeat("y", 3<<20)
	file = writeTail(t, usage, big+"1", big+"2", big+"3")
	lines, err = TailLines(file, 400)
	if err != nil || len(lines) != 2 || lines[0] != big+"2" || lines[1] != big+"3" {
		t.Errorf("three 3 MiB lines: %d lines (err %v), want the last two whole (the third cut by 8 MiB)", len(lines), err)
	}
	if _, _, _, ok := ContextTokens(file); ok {
		t.Error("ContextTokens: the usage line lies beyond the budget → ok=false")
	}
	small := strings.Repeat("z", 1<<20)
	file = writeTail(t, usage, small, small)
	if ctx, _, _, ok := ContextTokens(file); !ok || ctx != 7 {
		t.Errorf("ContextTokens behind two 1 MiB lines = %d %v, want 7 (within the budget)", ctx, ok)
	}
}

func TestTailLinesShortAndEmpty(t *testing.T) {
	t.Parallel()
	var all []string
	for i := range 1000 {
		all = append(all, fmt.Sprintf(`{"n":%d,"pad":"%s"}`, i, strings.Repeat("p", i%300)))
	}
	lines, err := TailLines(writeTail(t, all...), 400)
	if err != nil || len(lines) != 400 || lines[0] != all[600] || lines[399] != all[999] {
		t.Errorf("1000-line file: %d lines (err %v), want the last 400", len(lines), err)
	}
	lines, err = TailLines(writeTail(t, "a", "b", "c"), 400)
	if err != nil || strings.Join(lines, ",") != "a,b,c" {
		t.Errorf("3-line file: %q (err %v)", lines, err)
	}
	file := filepath.Join(t.TempDir(), "noeol")
	os.WriteFile(file, []byte("a\nb"), 0o644)
	if lines, err := TailLines(file, 1); err != nil || strings.Join(lines, ",") != "b" {
		t.Errorf("no trailing newline, n=1: %q (err %v)", lines, err)
	}
	file = filepath.Join(t.TempDir(), "empty")
	os.WriteFile(file, nil, 0o644)
	if lines, err := TailLines(file, 400); err != nil || len(lines) != 0 {
		t.Errorf("empty file: %q (err %v), want no lines", lines, err)
	}
	if _, err := TailLines(filepath.Join(t.TempDir(), "missing"), 400); err == nil {
		t.Error("a missing file: no error")
	}
}
