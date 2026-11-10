//go:build integration

package checks

import (
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

// laneAt is the fixed dispatch time of these tests.
var laneAt = time.Unix(1760000000, 0)

// laneFx is a project with lane L-01 (type opus) on branch task/T-01, a config committed on main after the lane was
// made: what a lane.prepare finds. target is main's HEAD (the dispatch fact's target).
func laneFx(t *testing.T, edits ...func(map[string]any)) (*testkit.Project, string, string) {
	t.Helper()
	p := testkit.NewAt(t, testkit.Dir(t))
	p.Write(".gitignore", ".obor/state/\n.task-done.md\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "fixture")
	lane := p.Lane("L-01", "task/T-01")
	p.Config(edits...)
	return p, lane, p.Git(p.Root, "rev-parse", "HEAD")
}

// laneAdapter is checks.Lane over p's config; git "" is real git.
func laneAdapter(t *testing.T, p *testkit.Project, git string) Lane {
	t.Helper()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	return Lane{Cfg: cfg, Out: io.Discard, Git: git}
}

// laneInput is the lane.prepare step of task T-01 in lane L-01 at laneAt.
func laneInput(lane, target string) port.PrepareInput {
	return port.PrepareInput{Task: "T-01", Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target,
		Agent: "pj-l-01", At: laneAt}
}

// TestLanePrepare: a fresh prepare puts the branch at the target; base is the target (serve's lane row), and it writes
// no lane state file (serve's row replaces it). A redo keeps the lane's commits (no checkout).
func TestLanePrepare(t *testing.T) {
	t.Parallel()
	p, lane, target := laneFx(t)
	l := laneAdapter(t, p, "")
	got, err := l.Prepare(context.Background(), laneInput(lane, target))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if want := (port.Prepared{Base: target}); got != want {
		t.Errorf("prepared %+v, want %+v", got, want)
	}
	if got := p.Git(lane, "rev-parse", "HEAD"); got != target {
		t.Errorf("lane HEAD = %s, want the target %s", got, target)
	}
	if got := p.Git(lane, "branch", "--show-current"); got != "task/T-01" {
		t.Errorf("lane branch = %q, want task/T-01", got)
	}
	if p.Exists(".obor/state/lanes/L-01.json") {
		t.Error("prepare wrote a lane state file (serve's row replaces it, T-439)")
	}

	p.Commit(lane)
	work := p.Git(lane, "rev-parse", "HEAD")
	if work == target {
		t.Fatal("precondition: the lane has a commit on top of the target")
	}
	in := laneInput(lane, target)
	in.Redo = true
	got, err = l.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("redo prepare: %v", err)
	}
	if got := p.Git(lane, "rev-parse", "HEAD"); got != work {
		t.Errorf("redo moved the lane HEAD: %s, want its commit %s", got, work)
	}
	if got.Base != target {
		t.Errorf("redo prepared %+v, want base the target %q", got, target)
	}
}

// TestLanePrepareRefused: no lane dir, lane_setup red, lane_ready still red and lane_ready red without lane_setup are
// final refusals (not retries).
func TestLanePrepareRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		edits []func(map[string]any)
		noDir bool
		want  string
	}{
		{"no lane dir", nil, true, "no lane dir"},
		{"lane_setup red", []func(map[string]any){
			testkit.Set("lanes.ready", "test -f ready"),
			testkit.Set("lanes.setup", "echo install broke; exit 3")}, false,
			"lane_setup in L-01: rc 3: install broke"},
		{"lane_ready still red", []func(map[string]any){
			testkit.Set("lanes.ready", "test -f ready"),
			testkit.Set("lanes.setup", "true")}, false,
			"lane_setup in L-01: ran, but lanes.ready still fails"},
		{"lane_ready red, no lane_setup", []func(map[string]any){
			testkit.Set("lanes.ready", "false")}, false,
			"lanes.setup is not set"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, lane, target := laneFx(t, c.edits...)
			in := laneInput(lane, target)
			if c.noDir {
				in.Dir = filepath.Join(p.Base, "no-such-lane")
			}
			_, err := laneAdapter(t, p, "").Prepare(context.Background(), in)
			var refused port.Refused
			if !errors.As(err, &refused) {
				t.Fatalf("prepare: %v; want an port.Refused", err)
			}
			if !strings.Contains(refused.Line, c.want) {
				t.Errorf("refusal %q lacks %q", refused.Line, c.want)
			}
		})
	}
}

// TestLanePrepareRepeats: a failure at the checkout git call is a plain error (retry), and preparing again converges on
// the same result as a clean prepare.
func TestLanePrepareRepeats(t *testing.T) {
	t.Parallel()
	p, lane, target := laneFx(t)
	in := laneInput(lane, target)
	shim := gitShim(t, p.Base, 1, 1)
	if _, err := laneAdapter(t, p, shim).Prepare(context.Background(), in); err == nil {
		t.Fatal("prepare with a failing git call: no error")
	} else {
		var refused port.Refused
		if errors.As(err, &refused) {
			t.Errorf("git failure is a refusal %q, want a plain error to retry", refused.Line)
		}
	}
	l := laneAdapter(t, p, "")
	first, err := l.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	second, err := l.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("second repeat: %v", err)
	}
	if want := (port.Prepared{Base: target}); first != want || second != want {
		t.Errorf("repeats prepared %+v, %+v; want %+v", first, second, want)
	}
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

// TestLaneFree (obor drop): a dirty lane and a branch with commits not in the target are refused, nothing
// changed; --discard drops both; then the lane is detached at the target and the branch deleted, and freeing again
// converges.
func TestLaneFree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name           string
		dirty, commit  bool
		discard        bool
		refusal        string // "": freed
		discardedLines []string
	}{
		{name: "clean"},
		{name: "dirty", dirty: true, refusal: "lane L-01 is dirty"},
		{name: "commits", commit: true, refusal: "task/T-01 has commits not in the target"},
		{name: "dirty, discard", dirty: true, discard: true, discardedLines: []string{"discarded in L-01:", "scratch.txt"}},
		{name: "commits, discard", commit: true, discard: true,
			discardedLines: []string{"discarded commits on task/T-01:", "work in L-01"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, lane, target := laneFx(t)
			l := laneAdapter(t, p, "")
			if _, err := l.Prepare(context.Background(), laneInput(lane, target)); err != nil {
				t.Fatal(err)
			}
			if c.commit {
				p.Commit(lane)
			}
			if c.dirty {
				p.Write(filepath.Join(lane, "scratch.txt"), "x\n")
			}
			in := port.FreeInput{Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target, Discard: c.discard}
			line, err := l.Free(context.Background(), in)
			if c.refusal != "" {
				var refused port.Refused
				if !errors.As(err, &refused) || !strings.Contains(refused.Line, c.refusal) {
					t.Fatalf("free: %v; want refused with %q", err, c.refusal)
				}
				if b := p.Git(lane, "branch", "--show-current"); b != "task/T-01" {
					t.Errorf("a refused free moved the lane to %q", b)
				}
				return
			}
			if err != nil {
				t.Fatalf("free: %v", err)
			}
			for _, want := range c.discardedLines {
				if !strings.Contains(line, want) {
					t.Errorf("line %q lacks %q", line, want)
				}
			}
			if len(c.discardedLines) == 0 && line != "" {
				t.Errorf("a clean free says %q", line)
			}
			if b := p.Git(lane, "branch", "--show-current"); b != "" {
				t.Errorf("lane on %q, want detached", b)
			}
			if h := p.Git(lane, "rev-parse", "HEAD"); h != target {
				t.Errorf("lane HEAD %s, want the target %s", h, target)
			}
			if st := p.Git(lane, "status", "--porcelain"); st != "" {
				t.Errorf("lane not clean after free: %q", st)
			}
			if out := p.Git(p.Root, "branch", "--list", "task/T-01"); out != "" {
				t.Errorf("task branch kept: %q", out)
			}
			if line, err := l.Free(context.Background(), in); err != nil || line != "" {
				t.Errorf("free again: %q, %v; want nothing to do", line, err)
			}
		})
	}
}

// A failed history read is unknown, never proof that a branch is merged.
// Calls 2–4 read the branch, ancestry and unmerged commits. None may turn a
// read failure into permission to detach or delete, even with --discard.
func TestLaneFreeHistoryFailurePreservesBranch(t *testing.T) {
	t.Parallel()
	for _, failAt := range []int{2, 3, 4} {
		for _, discard := range []bool{false, true} {
			t.Run(fmt.Sprintf("call %d discard %v", failAt, discard), func(t *testing.T) {
				t.Parallel()
				p, lane, target := laneFx(t)
				p.Commit(lane)
				head := p.Git(lane, "rev-parse", "HEAD")
				in := port.FreeInput{Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target, Discard: discard}
				code := 128 // 1 is the documented negative answer of the first two predicates
				if failAt == 4 {
					code = 1
				}
				_, err := laneAdapter(t, p, gitShim(t, p.Base, failAt, code)).Free(context.Background(), in)
				var refused port.Refused
				if err == nil || errors.As(err, &refused) {
					t.Fatalf("history failure: %v; want a retryable error", err)
				}
				if b := p.Git(lane, "branch", "--show-current"); b != in.Branch {
					t.Errorf("failed free moved lane to %q", b)
				}
				if got := p.Git(p.Root, "rev-parse", "refs/heads/"+in.Branch); got != head {
					t.Errorf("failed free lost branch head %q, want %q", got, head)
				}
				// A healthy retry still needs explicit discard for unmerged work.
				_, err = laneAdapter(t, p, "").Free(context.Background(), in)
				if discard && err != nil || !discard && !errors.As(err, &refused) {
					t.Errorf("retry: %v; want refusal without --discard, success with it", err)
				}
			})
		}
	}
}

// a drop whose branch delete fails after the lane was detached says the same drop finishes it, and it does:
// the lane is freed all the same (detached at the target), the branch kept, and a free without the failing git deletes
// it. Nothing before the detach carries the hint (that failure freed no lane).
func TestLaneFreeBranchDeleteFailsAfterTheLaneIsFreed(t *testing.T) {
	t.Parallel()
	p, lane, target := laneFx(t)
	if _, err := laneAdapter(t, p, "").Prepare(context.Background(), laneInput(lane, target)); err != nil {
		t.Fatal(err)
	}
	in := port.FreeInput{Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target}
	if b := p.Git(lane, "branch", "--show-current"); b != in.Branch {
		t.Fatalf("precondition: the lane is on %q, want %q", b, in.Branch)
	}
	// call 5 is the branch delete: status, rev-parse, ancestor, detach, delete.
	_, err := laneAdapter(t, p, gitShim(t, p.Base, 5, 128)).Free(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "git branch -D task/T-01") ||
		!strings.HasSuffix(err.Error(), " — run the same obor drop again to finish it") {
		t.Fatalf("branch delete failure: %v; want the retry hint", err)
	}
	if b := p.Git(lane, "branch", "--show-current"); b != "" {
		t.Errorf("the failed free left the lane on %q, want detached (freed)", b)
	}
	if h := p.Git(lane, "rev-parse", "HEAD"); h != target {
		t.Errorf("lane HEAD %s, want the target %s", h, target)
	}
	if out := p.Git(p.Root, "branch", "--list", in.Branch); out == "" {
		t.Fatal("precondition: the failed delete kept task/T-01")
	}
	if _, err := laneAdapter(t, p, "").Free(context.Background(), in); err != nil {
		t.Fatalf("the same free again: %v", err)
	}
	if out := p.Git(p.Root, "branch", "--list", in.Branch); out != "" {
		t.Errorf("the second free kept the task branch: %q", out)
	}
}

// an empty index.lock left in the lane's git dir makes lane.prepare fail 128 on its checkout; the error names
// the stale lock and what to do with it, and removes nothing.
func TestLanePrepareStaleIndexLock(t *testing.T) {
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
	_, err := laneAdapter(t, p, "").Prepare(context.Background(), laneInput(lane, target))
	want := "stale " + lock + " (no git running in the lane? remove it)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("prepare with a stale lock: %v; want %q", err, want)
	}
	if _, statErr := os.Stat(lock); statErr != nil {
		t.Errorf("prepare removed the stale lock: %v", statErr)
	}
	if b := p.Git(lane, "branch", "--show-current"); b != "task/T-01" {
		t.Errorf("the failed prepare left the lane on %q, want task/T-01", b)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := laneAdapter(t, p, "").Prepare(context.Background(), laneInput(lane, target)); err != nil {
		t.Errorf("prepare once the lock is gone: %v", err)
	}
}

// gitShim writes an executable git that fails its failAt-th call and is real git otherwise.
func gitShim(t *testing.T, dir string, failAt, code int) string {
	t.Helper()
	count := filepath.Join(dir, "git-calls")
	script := fmt.Sprintf("#!/bin/sh\nn=$(cat %s 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > %s\n"+
		"if [ \"$n\" = \"%d\" ]; then exit %d; fi\nexec git \"$@\"\n", count, count, failAt, code)
	shim := filepath.Join(dir, "bin", "git-shim")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}
