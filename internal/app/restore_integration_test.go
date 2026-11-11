//go:build integration

package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/store"
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

// restoreRig is newRestoreRig with the fake herdr's instance recorded (serve ran on this herdr before), started and
// connected.
func restoreRig(t *testing.T, hold bool) (*rig, *fakeRestorer) {
	t.Helper()
	r, fr := newRestoreRig(t, hold)
	r.recordInstance(r.instance())
	r.start()
	r.waitConnected()
	return r, fr
}

// newRestoreRig is newRig with a fake restorer, nothing recorded, not started.
func newRestoreRig(t *testing.T, hold bool) (*rig, *fakeRestorer) {
	t.Helper()
	r := newRig(t)
	fr := &fakeRestorer{}
	if hold {
		fr.hold = make(chan struct{})
	}
	r.restore = fr
	return r, fr
}

// instance is the fake herdr's instance now, as serve records it.
func (r *rig) instance() string {
	r.t.Helper()
	snap, err := herdr.Panes{S: r.srv.Client()}.Snapshot(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	inst := app.InstanceOf(snap)
	if inst == "" {
		r.t.Fatal("the fake herdr has no instance")
	}
	return inst
}

// recorded is the instance serve recorded ("": none).
func (r *rig) recorded() string {
	b, _ := os.ReadFile(app.InstanceFile(r.root))
	return strings.TrimSpace(string(b))
}

// recordInstance writes inst as the instance serve recorded.
func (r *rig) recordInstance(inst string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(app.InstanceFile(r.root)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	write(r.t, app.InstanceFile(r.root), inst+"\n")
}

// appendFact appends rec to the project's log before the app starts, at the clock's now.
func (r *rig) appendFact(rec event.Record) {
	r.t.Helper()
	db, err := store.OpenRW(event.DBFile(r.root))
	if err != nil {
		r.t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err != nil {
		r.t.Fatal(err)
	}
	defer log.Close()
	if _, err := log.Append(rec, r.clock.Now()); err != nil {
		r.t.Fatal(err)
	}
}

// quiet waits until the connected app's run loop has done a few checks: its first connect's restore decision is made.
func (r *rig) quiet() {
	r.t.Helper()
	r.waitConnected()
	checks, _ := app.Counts(r.app)
	r.waitFor("checks after the connect", func() bool { c, _ := app.Counts(r.app); return c >= checks+3 })
}

// noRestore: no restore ran or was logged.
func noRestore(t *testing.T, r *rig, fr *fakeRestorer, what string) {
	t.Helper()
	if n, _, _ := fr.runs(); n != 0 {
		t.Errorf("%s: %d restores, want 0:\n%s", what, n, r.out)
	}
	if strings.Contains(r.out.String(), "restoring") {
		t.Errorf("%s: a restore line:\n%s", what, r.out)
	}
}

// Herdr gone (its socket dies under the stream, the subscribes fail) and back: serve restores its project's
// panes once, with the project's root, after it subscribed again.
func TestServeRestoresAfterTerminalBack(t *testing.T) {
	t.Parallel()
	r, fr := restoreRig(t, false)
	if n, _, _ := fr.runs(); n != 0 {
		t.Fatalf("precondition: %d restores at the first connect, want 0", n)
	}
	r.srv.Die()
	r.waitFor("the gone line", func() bool { return strings.Contains(r.out.String(), "terminal: gone") })
	was := r.recorded()
	before := r.count("session.snapshot")
	r.srv.Revive(t)
	if r.instance() == was {
		t.Fatalf("precondition: the revived herdr's instance is the recorded one (%s)", was)
	}
	r.waitFor("the back line", func() bool { return strings.Contains(r.out.String(), "terminal: back") })
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
		return strings.Contains(out, "terminal: reconnected: restoring") && strings.Contains(out, "terminal: restore done")
	})
	if got, want := r.recorded(), r.instance(); got != want {
		t.Errorf("recorded instance %q after the reconnect, want the revived herdr's %q", got, want)
	}
}

// No lost connection, no restore: serve's first connect on the herdr it recorded, and a stream herdr ends with its
// answer (events_lost) that serve subscribes again after, start none.
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
	r.waitFor("the done line", func() bool { return strings.Contains(r.out.String(), "terminal: restore done") })
	if n, _, most := fr.runs(); n != 2 || most != 1 {
		t.Errorf("%d restores, %d at once at most; want 2 runs, never two at once", n, most)
	}
}

// Herdr restarted while serve was down (a machine restart: herdr first, serve later): serve's first connect
// finds another instance than it recorded, restores once, and records the new one.
func TestServeRestoresAtFirstConnectAfterTerminalRestart(t *testing.T) {
	t.Parallel()
	r, fr := newRestoreRig(t, false)
	r.recordInstance(r.instance())
	was := r.recorded()
	r.srv.Restart(t, time.Now().Add(time.Hour))
	cur := r.instance()
	if cur == was {
		t.Fatalf("precondition: the restarted herdr's instance is the recorded one (%s)", was)
	}
	r.start()
	r.waitFor("a restore", func() bool { n, _, _ := fr.runs(); return n >= 1 })
	r.waitFor("the restore's lines", func() bool {
		out := r.out.String()
		return strings.Contains(out, "terminal: restarted while serve was down") && strings.Contains(out, "terminal: restore done")
	})
	r.quiet()
	if n, _, _ := fr.runs(); n != 1 {
		t.Errorf("%d restores at the first connect, want 1", n)
	}
	if got := r.recorded(); got != cur {
		t.Errorf("recorded instance %q, want the restarted herdr's %q", got, cur)
	}
}

// Serve restarts (a re-exec on a new build, a restart) on the herdr it recorded: no restore at either start,
// so a role agent stopped on purpose stays at its shell; the record stays.
func TestServeNoRestoreOnServeRestartSameTerminal(t *testing.T) {
	t.Parallel()
	r, fr := restoreRig(t, false)
	was := r.recorded()
	r.quiet()
	noRestore(t, r, fr, "serve's first start")
	r.stop()
	r.start()
	r.quiet()
	noRestore(t, r, fr, "serve's restart")
	if got := r.recorded(); got != was {
		t.Errorf("recorded instance %q after the restart, want it kept: %q", got, was)
	}
}

// Nothing recorded (serve's first start with this rule): a herdr started after the newest record of the
// project's log (serve's last activity) restarted while serve was down, one started before it did not; either way
// the instance is recorded.
func TestServeFirstStartNothingRecorded(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		start time.Duration // herdr's start from the log's newest record
		want  int
	}{
		{"herdr started after serve's last activity", time.Hour, 1},
		{"herdr started before serve's last activity", -time.Hour, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, fr := newRestoreRig(t, false)
			r.appendFact(event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact,
				Data: []byte(`{"lane":"L-01"}`)})
			r.srv.Restart(t, r.clock.Now().Add(c.start))
			if r.recorded() != "" {
				t.Fatalf("precondition: an instance recorded: %s", r.recorded())
			}
			r.start()
			if c.want == 1 {
				r.waitFor("a restore", func() bool { n, _, _ := fr.runs(); return n >= 1 })
				r.waitFor("the restore's line", func() bool {
					return strings.Contains(r.out.String(), "after serve's last activity")
				})
			}
			r.quiet()
			if n, _, _ := fr.runs(); n != c.want {
				t.Errorf("%d restores, want %d:\n%s", n, c.want, r.out)
			}
			if got, want := r.recorded(), r.instance(); got != want {
				t.Errorf("recorded instance %q, want herdr's %q", got, want)
			}
		})
	}
}

// The project was stopped (obor stop: its pause in force when serve loads the project) and herdr restarted
// meanwhile: obor start's serve restores nothing (the agents start on first use) and records the new instance.
func TestServeNoRestoreAfterOborStop(t *testing.T) {
	t.Parallel()
	r, fr := newRestoreRig(t, false)
	r.recordInstance(r.instance())
	r.appendFact(event.Record{Task: event.PauseTask, Step: event.StepPaused, Kind: event.KindFact,
		Data: []byte(`{"why":"` + wire.StopPause + `","reply":"PAUSED until cleared"}`)})
	r.srv.Restart(t, time.Now().Add(time.Hour))
	cur := r.instance()
	r.start()
	r.quiet()
	noRestore(t, r, fr, "the first connect after obor stop")
	if got := r.recorded(); got != cur {
		t.Errorf("recorded instance %q, want the restarted herdr's %q", got, cur)
	}
}
