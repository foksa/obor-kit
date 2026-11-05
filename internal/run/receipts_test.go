package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

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

// The receipt of a green heavy run in a lane: written for tree, check and command text; found by exactly those, while
// it is young; nothing for a red run, a timeout, a signal, args, a light command, or a tree that changed during the run.
func TestRunWritesReceipts(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"ok": {"cmd": "echo fine", "heavy": true}, "bad": {"cmd": "exit 3", "heavy": true},
		"slow": {"cmd": "sleep 5", "timeout": 1, "heavy": true}, "light": "echo fine",
		"edits": {"cmd": "echo more > during.txt", "heavy": true}, "wait": {"cmd": "sleep 5", "heavy": true}}`)
	f.p.Write(filepath.Join(f.lane, "feature.txt"), "feature\n")
	state := project.StateDir(f.p.Root)
	tree, err := Tree(context.Background(), f.lane, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := func(name string) config.Command { c, _ := Command(f.cfg, name); return c }
	receipts := func() []string {
		es, _ := os.ReadDir(ReceiptDir(state))
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}

	for _, name := range []string{"bad", "slow", "light"} {
		f.run(t, f.lane, nil, nil, name)
		if n := receipts(); len(n) != 0 {
			t.Errorf("%s: receipts %v, want none", name, n)
		}
	}
	if r := f.run(t, f.lane, nil, nil, "ok", "extra"); r.rc != 0 || len(receipts()) != 0 {
		t.Errorf("a run with args proves nothing about the configured command: %+v %v", r, receipts())
	}
	sig := make(chan int, 1)
	go func() { time.Sleep(300 * time.Millisecond); sig <- 143 }()
	if r := f.run(t, f.lane, nil, sig, "wait"); r.rc != 143 || len(receipts()) != 0 {
		t.Errorf("a run cut by a signal: %+v receipts %v", r, receipts())
	}
	f.run(t, f.lane, nil, nil, "edits")
	if n := receipts(); len(n) != 0 {
		t.Errorf("the tree changed during the run: receipts %v, want none", n)
	}
	os.Remove(filepath.Join(f.lane, "during.txt"))

	if r := f.run(t, f.lane, nil, nil, "ok"); r.rc != 0 {
		t.Fatalf("ok: %+v", r)
	}
	if n := receipts(); len(n) != 1 || n[0] != tree+"-ok" {
		t.Fatalf("receipts %v, want [%s-ok]", n, tree)
	}
	b, _ := os.ReadFile(filepath.Join(ReceiptDir(state), tree+"-ok"))
	fields := strings.Fields(string(b))
	if len(fields) != 6 || fields[0] != tree || fields[1] != "ok" || fields[2] != CmdHash(cmd("ok")) || fields[4] != "L-01" {
		t.Errorf("receipt line %q", b)
	}

	c, now := cmd("ok"), time.Now() // after the run: a receipt dated later than now is the future's
	for _, tc := range []struct {
		name string
		tree string
		c    config.Command
		now  time.Time
		ok   bool
	}{
		{"same tree, check, text", tree, c, now, true},
		{"another tree", strings.Repeat("0", len(tree)), c, now, false},
		{"another check name", tree, cmd("bad"), now, false},
		{"command text changed", tree, config.Command{Name: "ok", Cmd: "echo changed", Heavy: true}, now, false},
		{"older than max age", tree, c, now.Add(2 * time.Hour), false},
		{"receipt from the future", tree, c, now.Add(-2 * time.Hour), false},
	} {
		lane, ok := FindReceipt(state, tc.tree, tc.c, time.Hour, tc.now)
		if ok != tc.ok || (ok && lane != "L-01") {
			t.Errorf("%s: found %v (%q), want %v", tc.name, ok, lane, tc.ok)
		}
	}
	if _, ok := FindReceipt(state, tree, c, 0, now); ok {
		t.Error("max age 0 reuses nothing")
	}
}

func TestCPUs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		slots map[string]any
		env   []string
		cores int
		want  int
	}{
		{"slots.cpus set", map[string]any{"max": 2, "cpus": 3}, nil, 16, 3},
		{"unset: cores / slots.max", map[string]any{"max": 2}, nil, 16, 8},
		{"unset: the default slots.max is 2", nil, nil, 10, 5},
		{"at least 1", map[string]any{"max": 4}, nil, 2, 1},
		{"slots.max 0 does not divide by zero", map[string]any{"max": 0}, nil, 6, 6},
		{"CHECK_SLOTS counts like slots.max", map[string]any{"max": 2}, []string{"CHECK_SLOTS=4"}, 16, 4},
	} {
		p := testkit.New(t)
		if tc.slots != nil {
			p.Config(testkit.Set("slots", tc.slots))
		} else {
			p.Config()
		}
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		if got := CPUs(cfg, p.Env(tc.env...), tc.cores); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A write removes a receipt older than maxAge and keeps a fresh one; FindReceipt still finds the new one.
func TestWriteReceiptPrunesExpired(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	dir := ReceiptDir(state)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	put := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	expired, fresh := put("t1-check", 2*time.Hour), put("t2-check", time.Minute)
	c := config.Command{Name: "check", Cmd: "true"}
	if err := WriteReceipt(state, "t3", c, "S-01", time.Second, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Errorf("the expired receipt stays: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("the fresh receipt is gone: %v", err)
	}
	if lane, ok := FindReceipt(state, "t3", c, time.Hour, now); !ok || lane != "S-01" {
		t.Errorf("FindReceipt = %q, %v, want S-01, true", lane, ok)
	}
}
