//go:build integration

package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
)

// herdrRig is the rig with the app on the real herdr adapter (herdr.Panes over the fake herdr's unix socket), whose
// instance is its socket file's: serve's restore at the terminal's boundary. Its panes are newRig's.
type herdrRig struct {
	*rig
	srv *herdrtest.Server
}

func newHerdrRig(t *testing.T) *herdrRig {
	t.Helper()
	r := newRig(t)
	f := herdrtest.New()
	f.Group("w1", "pj")
	f.Group("w2", "other")
	for _, lane := range []string{"L-01", "L-02"} {
		p := f.Agent("w1:p"+lane[3:], "pj-l-0"+lane[3:], "idle", r.lane(lane))
		p.Label = "pj-l-0" + lane[3:] // labelled by its launch: serve's identity of it
	}
	srv := herdrtest.Serve(t, f)
	srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	c := srv.Client()
	r.at = &adapter{Panes: herdr.Panes{S: c}, Info: c.ProcessInfo, Screen: c.Screen}
	return &herdrRig{rig: r, srv: srv}
}

// snapshots is how many session snapshots the fake herdr answered (a subscribe reads one).
func (r *herdrRig) snapshots() int {
	n := 0
	for _, req := range r.srv.Requests() {
		if req.Method == "session.snapshot" {
			n++
		}
	}
	return n
}

// settled waits until serve has subscribed again after the snapshot count before and has run some checks on that
// stream: a restore it started on the reconnect has been called by then.
func (r *herdrRig) settled(before int) {
	r.t.Helper()
	r.waitFor("a new snapshot", func() bool { return r.snapshots() > before })
	r.waitConnected()
	checks, _ := app.Counts(r.app)
	r.waitFor("checks on the new stream", func() bool { c, _ := app.Counts(r.app); return c >= checks+3 })
}

// restoreRig is newRestoreRig with the fake herdr's instance recorded (serve ran on this herdr before), started and
// connected.
func restoreRig(t *testing.T, hold bool) (*herdrRig, *fakeRestorer) {
	t.Helper()
	r, fr := newRestoreRig(t, hold)
	r.recordInstance(r.instance())
	r.start()
	r.waitConnected()
	return r, fr
}

// newRestoreRig is newHerdrRig with a fake restorer, nothing recorded, not started.
func newRestoreRig(t *testing.T, hold bool) (*herdrRig, *fakeRestorer) {
	t.Helper()
	r := newHerdrRig(t)
	fr := &fakeRestorer{}
	if hold {
		fr.hold = make(chan struct{})
	}
	r.restore = fr
	return r, fr
}

// instance is the fake herdr's instance now, as serve records it.
func (r *herdrRig) instance() string {
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
func noRestore(t *testing.T, r *herdrRig, fr *fakeRestorer, what string) {
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
	before := r.snapshots()
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
	before := r.snapshots()
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
	before := r.snapshots()
	r.srv.DropConns()
	r.waitFor("the first restore running", func() bool { _, running, _ := fr.runs(); return running == 1 })
	r.settled(before)
	before = r.snapshots()
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
