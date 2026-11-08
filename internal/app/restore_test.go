package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"obor/internal/app"
)

// fakeRestorer records serve's restore runs (port.Restorer): the roots asked for, and the most that ran at once. A
// run waits for hold while hold is set (a restore that takes its time).
type fakeRestorer struct {
	mu      sync.Mutex
	roots   []string
	running int
	most    int
	hold    chan struct{}
}

func (f *fakeRestorer) RestoreProject(ctx context.Context, root string) {
	f.mu.Lock()
	f.roots = append(f.roots, root)
	f.running++
	f.most = max(f.most, f.running)
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
}

func (f *fakeRestorer) runs() (n, running, most int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.roots), f.running, f.most
}

// restoreRig is newRig with a fake restorer, started and connected.
func restoreRig(t *testing.T, hold bool) (*rig, *fakeRestorer) {
	t.Helper()
	r := newRig(t)
	fr := &fakeRestorer{}
	if hold {
		fr.hold = make(chan struct{})
	}
	r.restore = fr
	r.start()
	r.waitConnected()
	return r, fr
}

// settled waits until serve has subscribed again after the snapshot count before and has run some checks on that
// stream: a restore it started on the reconnect has been called by then.
func (r *rig) settled(before int) {
	r.t.Helper()
	r.waitFor("a new snapshot", func() bool { return r.count("session.snapshot") > before })
	r.waitConnected()
	checks, _ := app.Counts(r.app)
	r.waitFor("checks on the new stream", func() bool { c, _ := app.Counts(r.app); return c >= checks+3 })
}

// T-1051: herdr gone (its socket dies under the stream, the subscribes fail) and back: serve restores its project's
// panes once, with the project's root, after it subscribed again.
func TestServeRestoresAfterTerminalBack(t *testing.T) {
	t.Parallel()
	r, fr := restoreRig(t, false)
	if n, _, _ := fr.runs(); n != 0 {
		t.Fatalf("precondition: %d restores at the first connect, want 0", n)
	}
	r.srv.Die()
	r.waitFor("the gone line", func() bool { return strings.Contains(r.out.String(), "herdr: gone") })
	before := r.count("session.snapshot")
	r.srv.Revive(t)
	r.waitFor("the back line", func() bool { return strings.Contains(r.out.String(), "herdr: back") })
	r.waitFor("a restore", func() bool { n, _, _ := fr.runs(); return n >= 1 })
	r.settled(before)
	if n, _, _ := fr.runs(); n != 1 {
		t.Errorf("%d restores after herdr came back, want 1", n)
	}
	fr.mu.Lock()
	roots := append([]string(nil), fr.roots...)
	fr.mu.Unlock()
	if len(roots) == 0 || roots[0] != r.root {
		t.Errorf("restored roots %q, want the served project %s", roots, r.root)
	}
	r.waitFor("the restore's lines", func() bool {
		out := r.out.String()
		return strings.Contains(out, "herdr: reconnected: restoring") && strings.Contains(out, "herdr: restore done")
	})
}

// No lost connection, no restore: serve's first connect, and a stream herdr ends with its answer (events_lost) that
// serve subscribes again after, start none.
func TestServeNoRestoreWithoutLoss(t *testing.T) {
	t.Parallel()
	r, fr := restoreRig(t, false)
	before := r.count("session.snapshot")
	r.srv.PushError("events_lost", "subscriber lagged")
	r.waitFor("the stream end line", func() bool { return strings.Contains(r.out.String(), "events_lost") })
	r.settled(before)
	if n, _, _ := fr.runs(); n != 0 {
		t.Errorf("%d restores with no lost connection, want 0:\n%s", n, r.out)
	}
	if strings.Contains(r.out.String(), "restoring") {
		t.Errorf("a restore line with no lost connection:\n%s", r.out)
	}
}

// Two quick reconnects (herdr drops every connection and the next subscribe succeeds at once) while the first restore
// still runs: the second does not start beside it; it runs once after it, so two runs, never two at once.
func TestServeRestoresDoNotOverlap(t *testing.T) {
	t.Parallel()
	r, fr := restoreRig(t, true)
	before := r.count("session.snapshot")
	r.srv.DropConns()
	r.waitFor("the first restore running", func() bool { _, running, _ := fr.runs(); return running == 1 })
	r.settled(before)
	before = r.count("session.snapshot")
	r.srv.DropConns()
	r.waitFor("the second reconnect's line", func() bool {
		return strings.Contains(r.out.String(), "reconnected again: one more restore")
	})
	r.settled(before)
	if n, running, _ := fr.runs(); n != 1 || running != 1 {
		t.Fatalf("while the first restore runs: %d runs, %d running; want 1 and 1", n, running)
	}
	close(fr.hold)
	r.waitFor("the queued restore", func() bool { n, running, _ := fr.runs(); return n == 2 && running == 0 })
	r.waitFor("the done line", func() bool { return strings.Contains(r.out.String(), "herdr: restore done") })
	if n, _, most := fr.runs(); n != 2 || most != 1 {
		t.Errorf("%d restores, %d at once at most; want 2 runs, never two at once", n, most)
	}
}
