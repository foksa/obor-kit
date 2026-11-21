//go:build integration

package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/testkit"
)

// laneFx is a project with lane L-01 (type opus) on branch task/T-01, a config committed on main after the lane was
// made: what a lane.prepare finds. target is main's HEAD (the dispatch fact's target).
func laneFx(t *testing.T, edits ...func(map[string]any)) (*testkit.Project, string, string) {
	t.Helper()
	base := snapshot(t, builtBase("lane "+editsKey(edits), func(base string) string {
		p := testkit.NewAt(mainTB{}, base)
		p.Write(".gitignore", ".obor/state/\n.task-done.md\n")
		p.Git(p.Root, "add", "-A")
		p.Git(p.Root, "commit", "-q", "-m", "fixture")
		p.Lane("L-01", "task/T-01")
		p.Config(edits...)
		return base
	}))
	testkit.RepairWorktrees(t, base)
	p := testkit.Adopt(t, base)
	return p, p.LaneDir("L-01"), strings.TrimSpace(p.Read(".git/refs/heads/main"))
}

// laneAdapter is checks.Lane over p's config on real git; git "" is git on PATH, else a shim (lockShim).
func laneAdapter(t *testing.T, p *testkit.Project, git string) Lane {
	t.Helper()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	return Lane{Cfg: cfg, Out: io.Discard, Git: git}
}

// TestLanePrepareCanceledEndsSetup: a canceled prepare ends the configured setup check's process: the check
// gets no output after the cancel (its sleep and its marker never happen) and Prepare returns at once with a refusal.
func TestLanePrepareCanceledEndsSetup(t *testing.T) {
	p, lane, target := laneFx(t, testkit.Set("lanes.ready", "touch ready-started; sleep 3; touch ready-done"))
	l := laneAdapter(t, p, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := l.Prepare(ctx, laneInput(lane, target))
		done <- err
	}()
	started := filepath.Join(lane, "ready-started")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lane_ready never started: the fixture's command did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		var refused port.Refused
		if !errors.As(err, &refused) {
			t.Fatalf("prepare: %v; want the final refusal of a red lane_ready", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prepare outlived the canceled ctx: the setup check's process was not killed")
	}
	if _, err := os.Stat(filepath.Join(lane, "ready-done")); err == nil {
		t.Fatal("the setup check outlived the cancel: it wrote its marker after the cancel")
	}
}

// lane.prepare's git writes carry the stale-lock rule (gitLocked): a stale empty index.lock in the lane's git dir (an
// mtime older than staleLock, no git running) is removed once, logged, and the checkout goes on; a fresh lock (the one
// a live git holds) is waited for, not removed, and the checkout runs once it is gone.
func TestLanePrepareLock(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		stale bool
	}{
		{"a stale lock: removed once, logged, the checkout goes on", true},
		{"a fresh lock: waited for, not removed", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, lane, target := laneFx(t)
			gitDir := p.Git(lane, "rev-parse", "--git-dir")
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(lane, gitDir)
			}
			lock := filepath.Join(gitDir, "index.lock")
			if err := os.WriteFile(lock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			l := laneAdapter(t, p, "")
			l.Out = &out
			calls := filepath.Join(p.Base, "git-calls")
			if c.stale {
				old := time.Now().Add(-2 * staleLock)
				if err := os.Chtimes(lock, old, old); err != nil {
					t.Fatal(err)
				}
			} else {
				l.Git = lockShim(t, calls, lock, p.Base)
			}
			got, err := l.Prepare(context.Background(), laneInput(lane, target))
			if err != nil {
				t.Fatalf("prepare with the lock: %v", err)
			}
			if want := (port.Prepared{Base: target}); got != want {
				t.Errorf("prepared %+v, want %+v", got, want)
			}
			if b := p.Git(lane, "branch", "--show-current"); b != "task/T-01" {
				t.Errorf("lane on %q, want task/T-01", b)
			}
			if h := p.Git(lane, "rev-parse", "HEAD"); h != target {
				t.Errorf("lane HEAD %s, want the target %s", h, target)
			}
			n := strings.Count(out.String(), "removed stale git lock "+lock+" (")
			if c.stale && n != 1 {
				t.Errorf("stale lock removals logged %d times, want 1:\n%s", n, out.String())
			}
			if !c.stale && n != 0 {
				t.Errorf("a fresh lock was removed:\n%s", out.String())
			}
			if c.stale {
				return
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Errorf("the lock's holder did not remove it: %v", err)
			}
			// The links' status takes no lock; the checkout met it, then ran again once it was gone.
			emitted := p.Read(calls)
			if n := strings.Count(emitted, "rc="); n != 3 {
				t.Errorf("lane git calls %d, want the links' status, the checkout's failure and one retry:\n%s", n, emitted)
			}
			if strings.Count(emitted, "rc=128") != 1 || !strings.Contains(emitted, "index.lock") {
				t.Errorf("the checkout did not fail once on the lock:\n%s", emitted)
			}
		})
	}
}

// lane.prepare's error for a lock that outlives the wait keeps the lock's path: git's stderr is the lock's line, a
// blank line, then its hint, and the error names the lock's absolute path (in the main repository's
// .git/worktrees/<lane>/) and the hint.
func TestLanePrepareLockOutlastsTheWait(t *testing.T) {
	t.Parallel()
	p, lane, target := laneFx(t)
	lock := p.Git(lane, "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	l := laneAdapter(t, p, "")
	l.Out = &out
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := l.Prepare(ctx, laneInput(lane, target))
	if err == nil {
		t.Fatal("prepare with the lock held past the wait: no error")
	}
	var refused port.Refused
	if errors.As(err, &refused) {
		t.Errorf("a lock past the wait is a refusal %q, want a plain error to retry", refused.Line)
	}
	for _, want := range []string{"Unable to create '" + lock + "': File exists", " — still locked after the wait: " + lock} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("prepare's error %q lacks %q", err.Error(), want)
		}
	}
	if n := strings.Count(out.String(), "removed stale git lock "); n != 0 {
		t.Errorf("a fresh lock was removed %d times, want none:\n%s", n, out.String())
	}
}

// lockShim is an executable git for the fresh-lock case: it runs git, logs its exit code and stderr to calls, and after
// a failed call removes the lane's lock (its holder, a live git, finishing).
func lockShim(t *testing.T, calls, lock, dir string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
log='%[1]s'
git "$@" 2>"$log.err"
rc=$?
cat "$log.err" >&2
echo "rc=$rc" >> "$log"
cat "$log.err" >> "$log"
if [ $rc -ne 0 ]; then rm -f '%[2]s'; fi
exit $rc
`, calls, lock)
	shim := filepath.Join(dir, "bin", "git-lock")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

// trackedLink makes the state a commit of a linked file leaves: lane holds obor's link (a symlink to the main
// checkout's file) at conf/local.env, untracked, while the main checkout commits that file. It returns the new target
// (main's head) and the link's path in lane.
func trackedLink(t *testing.T, p *testkit.Project, lane string) (target, link string) {
	t.Helper()
	p.Write("conf/local.env", "from main\n")
	link = filepath.Join(lane, "conf", "local.env")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(p.Root, "conf", "local.env"), link); err != nil {
		t.Fatal(err)
	}
	if st := p.Git(lane, "status", "--porcelain", "--untracked-files=all", "--", "conf"); st != "?? conf/local.env" {
		t.Fatalf("precondition: the link's status %q, want it untracked", st)
	}
	p.Git(p.Root, "add", "conf/local.env")
	p.Git(p.Root, "commit", "-q", "-m", "track conf/local.env")
	return p.Git(p.Root, "rev-parse", "HEAD"), link
}

// keptRegular asserts path is a regular file with text.
func keptRegular(t *testing.T, path, text string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%s: %v, %v; want a regular file", path, fi, err)
	}
	if b, _ := os.ReadFile(path); string(b) != text {
		t.Errorf("%s holds %q, want %q", path, b, text)
	}
}

// A checkout in a lane removes obor's link where the target commit tracks that path (git would refuse to overwrite an
// untracked file): prepare succeeds, the lane holds the tracked file and a line says which link went. A regular file
// at that path is the lane's: prepare fails as it does without the rule, the file kept.
func TestLanePrepareRemovesALinkTheTargetTracks(t *testing.T) {
	t.Parallel()
	p, lane, _ := laneFx(t)
	target, link := trackedLink(t, p, lane)
	var out bytes.Buffer
	l := laneAdapter(t, p, "")
	l.Out = &out
	if _, err := l.Prepare(context.Background(), laneInput(lane, target)); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	keptRegular(t, link, "from main\n")
	if want := "removed link conf/local.env: " + target[:7] + " tracks it\n"; out.String() != want {
		t.Errorf("progress %q, want %q", out.String(), want)
	}
	if st := p.Git(lane, "status", "--porcelain"); st != "" {
		t.Errorf("lane not clean after prepare: %q", st)
	}
	if h := p.Git(lane, "rev-parse", "HEAD"); h != target {
		t.Errorf("lane HEAD %s, want the target %s", h, target)
	}

	// the lane's own file at the path
	p, lane, _ = laneFx(t)
	target, link = trackedLink(t, p, lane)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	p.Write(link, "the lane's own\n")
	out.Reset()
	l = laneAdapter(t, p, "")
	l.Out = &out
	if _, err := l.Prepare(context.Background(), laneInput(lane, target)); err == nil || !strings.Contains(err.Error(), "checkout -B task/T-01") {
		t.Fatalf("prepare over a regular file: %v, want the checkout's refusal", err)
	}
	keptRegular(t, link, "the lane's own\n")
	if out.Len() != 0 {
		t.Errorf("progress %q for a path that is no link, want none", out.String())
	}
}

// Free's detach does the same: obor's link at a path the target tracks goes, the lane is freed on the tracked file; a
// regular file there is the lane's dirt (the drop refuses it).
func TestLaneFreeRemovesALinkTheTargetTracks(t *testing.T) {
	t.Parallel()
	links := testkit.Set("lanes.links", []any{"conf/local.env"})
	p, lane, first := laneFx(t, links)
	l := laneAdapter(t, p, "")
	if _, err := l.Prepare(context.Background(), laneInput(lane, first)); err != nil {
		t.Fatal(err)
	}
	target, link := trackedLink(t, p, lane)
	var out bytes.Buffer
	l.Out = &out
	in := port.FreeInput{Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target}
	if line, err := l.Free(context.Background(), in); err != nil || line != "" {
		t.Fatalf("free: %q, %v; want freed, nothing discarded", line, err)
	}
	keptRegular(t, link, "from main\n")
	if want := "removed link conf/local.env: " + target[:7] + " tracks it\n"; out.String() != want {
		t.Errorf("progress %q, want %q", out.String(), want)
	}
	if b := p.Git(lane, "branch", "--show-current"); b != "" {
		t.Errorf("lane on %q, want detached", b)
	}
	if h := p.Git(lane, "rev-parse", "HEAD"); h != target {
		t.Errorf("lane HEAD %s, want the target %s", h, target)
	}

	p, lane, first = laneFx(t, links)
	l = laneAdapter(t, p, "")
	if _, err := l.Prepare(context.Background(), laneInput(lane, first)); err != nil {
		t.Fatal(err)
	}
	target, link = trackedLink(t, p, lane)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	p.Write(link, "the lane's own\n")
	in.Dir, in.Target = lane, target
	var refused port.Refused
	if _, err := l.Free(context.Background(), in); !errors.As(err, &refused) || !strings.Contains(refused.Line, "conf/local.env") {
		t.Fatalf("free over a regular file: %v, want refused as dirty naming conf/local.env", err)
	}
	keptRegular(t, link, "the lane's own\n")
}
