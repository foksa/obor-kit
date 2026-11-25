//go:build integration

package checks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/project"
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

// linkIn is attempt 2's lane.sync of fx's lane: its worker branch, fx.cand the target.
func linkIn(fx checkoutFx) port.SyncInput {
	return port.SyncInput{Task: "T-01", Attempt: 2, Lane: "L-01", Dir: fx.lane, Branch: fx.branch, Target: fx.cand}
}

// laneShared makes the lane's own commit of shared.txt, which fx.cand tracks with other content, so fx.cand conflicts
// with the lane there; it returns the lane's head.
func laneShared(t *testing.T, fx checkoutFx) string {
	t.Helper()
	fx.p.Write(filepath.Join(fx.lane, "shared.txt"), "the lane's own\n")
	fx.p.Git(fx.lane, "add", "shared.txt")
	fx.p.Git(fx.lane, "commit", "-q", "-m", "the lane's shared")
	return fx.p.Git(fx.lane, "rev-parse", "HEAD")
}

// crashRemovedLink removes the lane's link at fx.path as a cut sync does before its merge (project.WithLinks'
// removal), and checks it is gone.
func crashRemovedLink(t *testing.T, fx checkoutFx) {
	t.Helper()
	dst := filepath.Join(fx.lane, fx.path)
	if err := os.Remove(dst); err != nil {
		t.Fatalf("remove the link: %v", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		t.Fatalf("precondition: %s is still there", dst)
	}
}

// commitFailShim is an executable git for the lane that fails its commit (CommitMerge) and runs git for everything else:
// a merge commit that fails after the merge itself succeeded.
func commitFailShim(t *testing.T) string {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "git-nocommit")
	script := `#!/bin/sh
if [ "$3" = commit ]; then
	echo "the commit failed" >&2
	exit 1
fi
exec git "$@"
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

// lane.sync keeps obor's links the way a checkout does (project.WithLinks): the target that starts tracking a link's
// path does not stop the merge (the link comes out of its way, the tracked file takes the path); a merge that stops
// puts the link back; the replay of a merge a crash left open reconciles it; an ordinary untracked file is never
// removed, and the lane stays as it was. A cut that left the link out does not outlive the sync either: a replay whose
// retry conflicts, a cut after the removal before the merge, and a merge commit that fails all put it back
// (project.ReconcileLinks).
func TestLaneSyncKeepsTheLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("the target starts tracking the settings link: merged, the tracked file takes it", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, project.PluginSettings)
		fx.isLink(t, "precondition: the lane's settings link")
		var out bytes.Buffer
		l := Lane{Cfg: fx.cfg, Out: &out}
		s, err := l.Sync(ctx, linkIn(fx))
		if err != nil || s.Kind != port.SyncMerged {
			t.Fatalf("Sync: %+v, %v; want merged", s, err)
		}
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != s.SHA {
			t.Errorf("the merge's sha %s, the lane's HEAD %s", s.SHA, h)
		}
		if parents := strings.Fields(fx.p.Git(fx.lane, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 ||
			parents[1] != fx.base || parents[2] != fx.cand {
			t.Errorf("HEAD's parents %q; want %s and %s", parents, fx.base, fx.cand)
		}
		fx.isTracked(t, "after the sync")
		if want := "removed link " + fx.path + ": " + fx.cand[:7] + " tracks it\n"; out.String() != want {
			t.Errorf("progress %q, want %q", out.String(), want)
		}
	})

	t.Run("the target starts tracking a lanes.links entry: merged, the entry reconciled", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, "conf/local.env")
		fx.isLink(t, "precondition: the configured link")
		s, err := Lane{Cfg: fx.cfg}.Sync(ctx, linkIn(fx))
		if err != nil || s.Kind != port.SyncMerged {
			t.Fatalf("Sync: %+v, %v; want merged", s, err)
		}
		fx.isTracked(t, "after the sync")
	})

	t.Run("a conflicting merge with the link: aborted, the link restored", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, project.PluginSettings, "shared.txt")
		fx.p.Write(filepath.Join(fx.lane, "shared.txt"), "the lane's own\n")
		fx.p.Git(fx.lane, "add", "shared.txt")
		fx.p.Git(fx.lane, "commit", "-q", "-m", "the lane's shared")
		head := fx.p.Git(fx.lane, "rev-parse", "HEAD")
		var out bytes.Buffer
		l := Lane{Cfg: fx.cfg, Out: &out}
		s, err := l.Sync(ctx, linkIn(fx))
		if err != nil || s.Kind != port.SyncConflict || !slices.Equal(s.Files, []string{"shared.txt"}) {
			t.Fatalf("Sync: %+v, %v; want a conflict in shared.txt", s, err)
		}
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != head {
			t.Errorf("HEAD %s; want %s, untouched", h, head)
		}
		fx.isLink(t, "after the aborted merge")
		keptRegular(t, filepath.Join(fx.lane, "shared.txt"), "the lane's own\n")
		if st := fx.p.Git(fx.lane, "status", "--porcelain", "--untracked-files=all"); st != "?? "+fx.path {
			t.Errorf("the lane's tree after the abort: %q, want only the link", st)
		}
		if want := "removed link " + fx.path + ": " + fx.cand[:7] + " tracks it\nlinked " + fx.path + "\n"; out.String() != want {
			t.Errorf("progress %q, want %q", out.String(), want)
		}
	})

	t.Run("a replay after a crash mid-merge: merged, the links reconciled", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, project.PluginSettings)
		// the crash: the cut sync had removed the link and its merge was left open, uncommitted
		dst := filepath.Join(fx.lane, project.PluginSettings)
		if err := os.Remove(dst); err != nil {
			t.Fatal(err)
		}
		fx.p.Git(fx.lane, "merge", "-q", "--no-commit", "--no-ff", fx.cand)
		if !gitOK(fx.lane, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
			t.Fatal("precondition: the crash left no MERGE_HEAD")
		}
		keptRegular(t, dst, "tracked\n")
		l := Lane{Cfg: fx.cfg}
		s, err := l.Sync(ctx, linkIn(fx))
		if err != nil || s.Kind != port.SyncMerged {
			t.Fatalf("Sync: %+v, %v; want merged", s, err)
		}
		fx.isTracked(t, "after the replay")
		if gitOK(fx.lane, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
			t.Error("the replay left the merge open")
		}
		if again, err := l.Sync(ctx, linkIn(fx)); err != nil || again.Kind != s.Kind || again.SHA != s.SHA {
			t.Errorf("Sync again: %+v, %v; want %+v", again, err, s)
		}
	})

	for _, path := range []string{project.PluginSettings, "conf/local.env"} {
		t.Run("the replay of a conflicting merge a crash left open, "+path+": conflict, the link back", func(t *testing.T) {
			t.Parallel()
			fx := newCheckoutFx(t, path, "shared.txt")
			head := laneShared(t, fx)
			// the crash: the earlier sync had removed the link, then the merge stopped on shared.txt and stayed open
			crashRemovedLink(t, fx)
			if _, err := project.Git(fx.lane, "merge", "-q", "--no-commit", "--no-ff", fx.cand); err == nil {
				t.Fatal("precondition: the crash's merge did not stop")
			}
			if !gitOK(fx.lane, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
				t.Fatal("precondition: the crash left no MERGE_HEAD")
			}
			s, err := Lane{Cfg: fx.cfg}.Sync(ctx, linkIn(fx))
			if err != nil || s.Kind != port.SyncConflict || !slices.Equal(s.Files, []string{"shared.txt"}) {
				t.Fatalf("Sync: %+v, %v; want a conflict in shared.txt", s, err)
			}
			fx.isLink(t, "after the replayed conflict")
			keptRegular(t, filepath.Join(fx.lane, "shared.txt"), "the lane's own\n")
			if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != head {
				t.Errorf("HEAD %s; want %s, untouched", h, head)
			}
			if gitOK(fx.lane, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
				t.Error("the replay left the merge open")
			}
		})
	}

	t.Run("a cut after the link's removal, before the merge: the conflict, the link back", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, project.PluginSettings, "shared.txt")
		head := laneShared(t, fx)
		crashRemovedLink(t, fx)
		if gitOK(fx.lane, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
			t.Fatal("precondition: the cut left a merge open")
		}
		s, err := Lane{Cfg: fx.cfg}.Sync(ctx, linkIn(fx))
		if err != nil || s.Kind != port.SyncConflict || !slices.Equal(s.Files, []string{"shared.txt"}) {
			t.Fatalf("Sync: %+v, %v; want a conflict in shared.txt", s, err)
		}
		fx.isLink(t, "after the conflict")
		keptRegular(t, filepath.Join(fx.lane, "shared.txt"), "the lane's own\n")
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != head {
			t.Errorf("HEAD %s; want %s, untouched", h, head)
		}
	})

	t.Run("a failed merge commit: aborted, the link back", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, project.PluginSettings)
		head := fx.p.Git(fx.lane, "rev-parse", "HEAD")
		l := Lane{Cfg: fx.cfg, Git: commitFailShim(t)}
		_, err := l.Sync(ctx, linkIn(fx))
		if err == nil || !strings.Contains(err.Error(), "commit the merge") {
			t.Fatalf("Sync: %v; want the merge commit's failure", err)
		}
		fx.isLink(t, "after the failed merge commit")
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != head {
			t.Errorf("HEAD %s; want %s, the abort's", h, head)
		}
		if gitOK(fx.lane, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
			t.Error("the failed commit's abort left the merge open")
		}
	})

	t.Run("an ordinary untracked file where the target tracks the path: not removed, no merge", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, project.PluginSettings)
		dst := filepath.Join(fx.lane, project.PluginSettings)
		if err := os.Remove(dst); err != nil {
			t.Fatal(err)
		}
		fx.p.Write(dst, "the lane's own\n")
		if tracked := fx.p.Git(fx.lane, "ls-tree", "--name-only", fx.cand, "--", project.PluginSettings); tracked == "" {
			t.Fatal("precondition: the target does not track the path")
		}
		if st := fx.p.Git(fx.lane, "status", "--porcelain", "--untracked-files=all"); st != "?? "+project.PluginSettings {
			t.Fatalf("precondition: the lane's own file's status %q, want it untracked", st)
		}
		s, err := Lane{Cfg: fx.cfg}.Sync(ctx, linkIn(fx))
		if err != nil || s.Kind != port.SyncDirty || !slices.Equal(s.Files, []string{project.PluginSettings}) {
			t.Fatalf("Sync: %+v, %v; want dirty naming %s", s, err, project.PluginSettings)
		}
		keptRegular(t, dst, "the lane's own\n")
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != fx.base {
			t.Errorf("HEAD %s; want %s, untouched", h, fx.base)
		}
	})
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
