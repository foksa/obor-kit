package app_test

import (
	"context"
	"obor/internal/app"
	"obor/internal/app/port"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/checks"
)

// T-879: the lane table is a cached observation. A file dirtied by hand in a lane (no pane event, no obor operation)
// shows in it within one tick: the tick scans before it decides.
func TestHandEditShowsWithinOneTick(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.lanes.mu.Lock()
	r.lanes.real = checks.Lanes{} // serve's own adapter over the fixture's worktrees
	r.lanes.mu.Unlock()
	r.tick = 200 * time.Millisecond
	r.start()
	r.waitFor("L-01 free in the cached table", func() bool {
		l, ok := app.CachedLane(r.app, r.p.Root, "L-01")
		return ok && l.State == "free"
	})
	if err := os.WriteFile(filepath.Join(r.lane, "edit.txt"), []byte("by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, _ := app.Counts(r.app)
	ls, err := checks.Lanes{}.Lanes(context.Background(), r.p.Root)
	if err != nil || len(ls) == 0 || ls[0].Name != "L-01" || ls[0].State == "free" {
		t.Fatalf("precondition: git does not see the edit: %+v, %v", ls, err)
	}
	// The check in flight may have scanned before the edit; the one after it began after the edit.
	r.waitFor("two more ticks", func() bool { c, _ := app.Counts(r.app); return c >= n+2 })
	if l, _ := app.CachedLane(r.app, r.p.Root, "L-01"); l.State != ls[0].State || l.Task != ls[0].Task {
		t.Errorf("the hand edit is not in the table one tick on: L-01 %+v, git says %+v", l, ls[0])
	}
}

// T-879: the reuse preflight reads git itself: a lane the cached table calls free but git now says dirty is refused,
// as before the cache.
func TestDispatchPreflightReadsGitNotTheCache(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.tick = time.Hour // no tick refreshes the table during the test
	r.start()
	if l, ok := app.CachedLane(r.app, r.p.Root, "L-01"); !ok || l.State != "free" {
		t.Fatalf("precondition: the cached table does not call L-01 free: %+v, %v", l, ok)
	}
	r.setLane(func(l *port.Lane) { l.State = "dirty-free" }) // git's answer now
	reply, err := r.dispatch("d1", "L-01", "sonnet")
	if err == nil || !strings.Contains(err.Error(), "lane L-01 is dirty-free (uncommitted changes; --force to dispatch anyway)") || reply != "" {
		t.Fatalf("dispatch on the dirty lane: %q, %v; want the dirty-free refusal", reply, err)
	}
}
