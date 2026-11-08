package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/port"
)

// T-879: the lane table is a cached observation. A file dirtied by hand in a lane (no pane event, no obor operation)
// shows in it within one tick: the tick scans before it decides. (That serve's adapter reads such an edit as
// dirty-free is git's: internal/checks' TestLaneStates.)
func TestHandEditShowsWithinOneTick(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.tick = 200 * time.Millisecond
	r.start()
	r.waitFor("L-01 free in the cached table", func() bool {
		l, ok := app.CachedLane(r.app, r.p.Root, "L-01")
		return ok && l.State == "free"
	})
	r.setLane(func(l *port.Lane) { l.State = "dirty-free" }) // the adapter's answer after the hand edit
	n, _ := app.Counts(r.app)
	// The check in flight may have scanned before the edit; the one after it began after the edit.
	r.waitFor("two more ticks", func() bool { c, _ := app.Counts(r.app); return c >= n+2 })
	if l, _ := app.CachedLane(r.app, r.p.Root, "L-01"); l.State != "dirty-free" {
		t.Errorf("the hand edit is not in the table one tick on: L-01 %+v", l)
	}
}

// T-879: the reuse preflight reads git itself: a lane the cached table calls free but git now says dirty is refused,
// as before the cache.
func TestDispatchPreflightReadsGitNotTheCache(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
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
