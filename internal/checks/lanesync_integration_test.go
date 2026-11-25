//go:build integration

package checks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// syncFx is laneFx with the lane's worker commit on task/T-01 and main moved on since by a commit adding main.txt:
// the lane's head and main's (the lane.sync's target).
func syncFx(t *testing.T) (*testkit.Project, Lane, string, string, string) {
	t.Helper()
	p, lane, _ := laneFx(t)
	p.Write(filepath.Join(lane, "work.txt"), "work\n")
	p.Git(lane, "add", "work.txt")
	p.Git(lane, "commit", "-q", "-m", "work in L-01")
	p.Write("main.txt", "main\n")
	p.Git(p.Root, "add", "main.txt")
	p.Git(p.Root, "commit", "-q", "-m", "main moves on")
	return p, laneAdapter(t, p, ""), lane, p.Git(lane, "rev-parse", "HEAD"), p.Git(p.Root, "rev-parse", "main")
}

// syncIn is attempt 2's lane.sync of T-01 in L-01 onto target.
func syncIn(lane, target string) port.SyncInput {
	return port.SyncInput{Task: "T-01", Attempt: 2, Lane: "L-01", Dir: lane, Branch: "task/T-01", Target: target}
}

// lane.sync on real git (port.LaneSetup.Sync): the target merged into the task branch, or the lane left as it was —
// current, a conflict (aborted), a dirty lane — and a repeat after a cut converges.
func TestLaneSync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("target ahead: merged, its file in the lane; again: the same merge", func(t *testing.T) {
		t.Parallel()
		p, l, lane, head, target := syncFx(t)
		if _, err := os.Stat(filepath.Join(lane, "main.txt")); err == nil {
			t.Fatal("precondition: main.txt in the lane before the sync")
		}
		s, err := l.Sync(ctx, syncIn(lane, target))
		if err != nil || s.Kind != port.SyncMerged {
			t.Fatalf("Sync: %+v, %v; want merged", s, err)
		}
		if got := p.Git(lane, "rev-parse", "HEAD"); s.SHA != got {
			t.Errorf("the merge's sha %s, the lane's HEAD %s", s.SHA, got)
		}
		if parents := strings.Fields(p.Git(lane, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 ||
			parents[1] != head || parents[2] != target {
			t.Errorf("HEAD's parents %q; want the lane's head %s and the target %s", parents, head, target)
		}
		if got := p.Git(lane, "branch", "--show-current"); got != "task/T-01" {
			t.Errorf("the lane is on %q; want task/T-01", got)
		}
		if subject := p.Git(lane, "log", "-1", "--format=%s"); subject != "Merge main into task/T-01 (obor: fix attempt 2)" {
			t.Errorf("the merge's subject %q", subject)
		}
		if b, err := os.ReadFile(filepath.Join(lane, "main.txt")); err != nil || string(b) != "main\n" {
			t.Errorf("main.txt in the lane: %q, %v", b, err)
		}
		again, err := l.Sync(ctx, syncIn(lane, target)) // a cut after the commit: the merge is there
		if err != nil || again.Kind != s.Kind || again.SHA != s.SHA {
			t.Errorf("Sync again: %+v, %v; want %+v", again, err, s)
		}
		if got := p.Git(lane, "rev-parse", "HEAD"); got != s.SHA {
			t.Errorf("Sync again committed: HEAD %s, want %s", got, s.SHA)
		}
	})
	t.Run("target in the branch's history: current, no commit", func(t *testing.T) {
		t.Parallel()
		p, l, lane, head, _ := syncFx(t)
		base := p.Git(lane, "rev-parse", "HEAD~1")
		s, err := l.Sync(ctx, syncIn(lane, base))
		if err != nil || s.Kind != port.SyncCurrent || s.SHA != "" {
			t.Fatalf("Sync: %+v, %v; want current", s, err)
		}
		if got := p.Git(lane, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD %s; want %s, untouched", got, head)
		}
	})
	t.Run("a conflicting change: aborted, HEAD and tree as they were, the files named", func(t *testing.T) {
		t.Parallel()
		p, l, lane, _, _ := syncFx(t)
		p.Write("shared.txt", "main's\n")
		p.Git(p.Root, "add", "shared.txt")
		p.Git(p.Root, "commit", "-q", "-m", "main's shared")
		target := p.Git(p.Root, "rev-parse", "main")
		p.Write(filepath.Join(lane, "shared.txt"), "the lane's\n")
		p.Git(lane, "add", "shared.txt")
		p.Git(lane, "commit", "-q", "-m", "the lane's shared")
		head := p.Git(lane, "rev-parse", "HEAD")
		s, err := l.Sync(ctx, syncIn(lane, target))
		if err != nil || s.Kind != port.SyncConflict || !slices.Equal(s.Files, []string{"shared.txt"}) {
			t.Fatalf("Sync: %+v, %v; want a conflict in shared.txt", s, err)
		}
		if got := p.Git(lane, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD %s; want %s, untouched", got, head)
		}
		if st := p.Git(lane, "status", "--porcelain"); st != "" {
			t.Errorf("the lane's tree after the abort:\n%s", st)
		}
		if b, _ := os.ReadFile(filepath.Join(lane, "shared.txt")); string(b) != "the lane's\n" {
			t.Errorf("shared.txt %q; want the lane's", b)
		}
		if _, err := os.Stat(filepath.Join(lane, "main.txt")); err == nil {
			t.Error("main.txt in the lane after an aborted merge")
		}
	})
	t.Run("a dirty lane: untouched, dirty", func(t *testing.T) {
		t.Parallel()
		p, l, lane, head, target := syncFx(t)
		p.Write(filepath.Join(lane, "wip.txt"), "wip\n")
		s, err := l.Sync(ctx, syncIn(lane, target))
		if err != nil || s.Kind != port.SyncDirty || !slices.Equal(s.Files, []string{"wip.txt"}) {
			t.Fatalf("Sync: %+v, %v; want dirty naming wip.txt", s, err)
		}
		if got := p.Git(lane, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD %s; want %s, untouched", got, head)
		}
		if b, _ := os.ReadFile(filepath.Join(lane, "wip.txt")); string(b) != "wip\n" {
			t.Errorf("wip.txt %q; want it kept", b)
		}
	})
	t.Run("a merge a cut sync left open: aborted, then merged", func(t *testing.T) {
		t.Parallel()
		p, l, lane, head, target := syncFx(t)
		p.Git(lane, "merge", "-q", "--no-commit", "--no-ff", target)
		s, err := l.Sync(ctx, syncIn(lane, target))
		if err != nil || s.Kind != port.SyncMerged {
			t.Fatalf("Sync: %+v, %v; want merged", s, err)
		}
		if parents := strings.Fields(p.Git(lane, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 ||
			parents[1] != head || parents[2] != target {
			t.Errorf("HEAD's parents %q; want %s and %s", parents, head, target)
		}
		if st := p.Git(lane, "status", "--porcelain"); st != "" {
			t.Errorf("the lane's tree after the merge:\n%s", st)
		}
	})
}
