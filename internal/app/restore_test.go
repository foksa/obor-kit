package app

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// The first connect's rule: another instance than recorded restores; the same one, a woken project (obor
// stop's pause at the load) and, with nothing recorded, a herdr that started before serve's last activity (or no
// activity known: a new project) do not; nothing recorded and a herdr started after it restores.
func TestFirstRestore(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 11, 9, 8, 0, 0, 123, time.UTC)
	cur := started.Format(time.RFC3339Nano)
	other := started.Add(-time.Hour).Format(time.RFC3339Nano)
	for _, c := range []struct {
		name    string
		was     string
		last    time.Time
		woke    bool
		restore bool
	}{
		{"another instance recorded", other, time.Time{}, false, true},
		{"the same instance recorded", cur, started.Add(-time.Hour), false, false},
		{"another instance, woken from obor stop", other, time.Time{}, true, false},
		{"nothing recorded, herdr started after serve's last activity", "", started.Add(-time.Minute), false, true},
		{"nothing recorded, herdr started before serve's last activity", "", started.Add(time.Minute), false, false},
		{"nothing recorded, no activity known", "", time.Time{}, false, false},
		{"nothing recorded, woken from obor stop", "", started.Add(-time.Minute), true, false},
	} {
		if why := firstRestore(c.was, started, c.last, c.woke); (why != "") != c.restore {
			t.Errorf("%s: firstRestore = %q, want restore %v", c.name, why, c.restore)
		}
	}
}

// countRestorer counts the restores serve asks for.
type countRestorer struct{ calls int }

func (r *countRestorer) RestoreProject(context.Context, string) { r.calls++ }

// firstConnect loads h's project at h.now and runs serve's first connect to a terminal started at started, nothing
// recorded in InstanceFile: the restores asked for, and the instance recorded after it.
func firstConnect(t *testing.T, h *harness, started time.Time) (int, string) {
	t.Helper()
	if _, err := os.Stat(InstanceFile(h.root)); !os.IsNotExist(err) {
		t.Fatalf("precondition: an instance is recorded (%v)", err)
	}
	fr := &countRestorer{}
	h.a.opt.Restore = fr
	h.a.loadProject()
	var wg sync.WaitGroup
	h.a.connectRestore(context.Background(), &wg, port.Snapshot{Started: started}, true, false)
	wg.Wait()
	b, err := os.ReadFile(InstanceFile(h.root))
	if err != nil {
		t.Fatalf("instance not recorded: %v", err)
	}
	return fr.calls, strings.TrimSpace(string(b))
}

// The first start after the upgrade (no instance recorded) with the old serve's failed
// self-restart needs-you pending and herdr restarted after it: the load's settlement (a resumed fact, newer than herdr's
// start) does not hide the restart: the restore runs once and the instance is recorded.
func TestRV165UpgradeAfterFailedServeRestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.log(event.Record{Task: event.ServeTask, Step: event.StepNeedsYou, Kind: event.KindFact,
		Data: []byte(`{"reason":"serve restart on the new obor binary failed: failed"}`)})
	started := h.now.Add(time.Hour)
	h.now = h.now.Add(2 * time.Hour)
	calls, rec := firstConnect(t, h, started)
	recs := h.records()
	if last := recs[len(recs)-1]; last.Task != event.ServeTask || last.Step != event.StepResumed || last.TS <= started.UTC().Format(time.RFC3339) {
		t.Fatalf("precondition: the load did not settle the needs-you after herdr's start: last record %+v", last)
	}
	if calls != 1 {
		t.Fatalf("restore calls = %d, want 1 (herdr started %v, last activity %v)", calls, started, h.a.lastActivity())
	}
	if want := instanceOf(port.Snapshot{Started: started}); rec != want {
		t.Fatalf("instance recorded %q, want %q", rec, want)
	}
}

// Nothing recorded and herdr started before the log's last activity from before serve's start: no restore,
// and the instance is recorded.
func TestFirstConnectNoRestoreHerdrBeforeLastActivity(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.now.Add(-time.Hour)
	h.log(event.Record{Task: event.ServeTask, Step: event.StepNeedsYou, Kind: event.KindFact,
		Data: []byte(`{"reason":"serve restart on the new obor binary failed: failed"}`)})
	h.now = h.now.Add(time.Hour)
	calls, rec := firstConnect(t, h, started)
	if last := h.a.lastActivity(); !started.Before(last) {
		t.Fatalf("precondition: herdr started %v, not before the last activity %v", started, last)
	}
	if calls != 0 {
		t.Fatalf("restore calls = %d, want 0", calls)
	}
	if want := instanceOf(port.Snapshot{Started: started}); rec != want {
		t.Fatalf("instance recorded %q, want %q", rec, want)
	}
}
