// reread_test.go: the re-read a lane's start makes when no pane of the lane is in the snapshot (T-745) — what it may
// change and what it may not (T-747, RV-89 #1 and #2).
package app

import (
	"context"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"strings"
	"testing"
	"time"
)

// noPane is the harness's snapshot before its connect: the lane's pane is outside it, so T-1's start takes the no-pane
// path and no watchdog sees an agent exit.
func noPane(h *harness) {
	h.pane("w1:p1").Cwd = h.root
}

// freshPane is a pane of pj's workspace as a fresh read of herdr's panes lists it (fn: any other field the read's).
func freshPane(h *harness, id, cwd string, fn func(p *port.Pane)) port.Pane {
	p := port.Pane{ID: id, Workspace: "w1", Cwd: cwd, Agent: "claude", Label: "pj-l-01", Status: "idle"}
	if fn != nil {
		fn(&p)
	}
	return p
}

// snapshot is panes as herdr's next read answers them.
func snapshot(panes ...port.Pane) *port.Snapshot { return &port.Snapshot{Panes: panes} }

// known is pane id as the snapshot has it now (zero: not known).
func known(h *harness, id string) port.Pane {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if o := h.a.obs[id]; o != nil {
		return o.pane
	}
	return port.Pane{}
}

// since is when the no-pane clock of T-1 started (zero: not set).
func since(h *harness) time.Time {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.p.nopane["T-1"]
}

// noPaneFacts is how many needs-you{no pane in lane …} facts T-1 has.
func noPaneFacts(h *harness) int {
	n := 0
	for _, r := range h.records() {
		if r.Task == "T-1" && r.Kind == event.KindFact && r.Step == event.StepNeedsYou &&
			strings.Contains(string(r.Data), causeNoPane) {
			n++
		}
	}
	return n
}

// RV-89 #1: a failed re-read is no answer. It starts no start and no needs-you, leaves the no-pane clock where it was
// (so the wait is the same), and the next pass reads herdr's panes again: the pane found then starts the agent.
func TestRereadErrorRecordsNothingAndRetries(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, noPane)
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	at := since(h)
	if at.IsZero() {
		t.Fatal("precondition: the no-pane clock started")
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snapErr = errors.New("herdr is restarting") // the read fails: herdr down, a timeout
	if h.step(t, fx) {
		t.Fatal("a failed re-read answered the pass")
	}
	if got := since(h); !got.Equal(at) {
		t.Errorf("the no-pane clock after a failed re-read is %s, want %s (the wait is not restarted)", got, at)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} after a failed re-read: %d", n)
	}
	h.panes.snapErr = nil
	h.panes.snap = snapshot(freshPane(h, "w1:p1", h.lane("L-01"), nil))
	if !h.step(t, fx) {
		t.Fatalf("the next pass did not read herdr's panes again (%d reads)", h.panes.snaps)
	}
	if got := known(h, "w1:p1").Cwd; got != h.lane("L-01") {
		t.Errorf("the pane's cwd after the retry is %q, want the lane %q", got, h.lane("L-01"))
	}
	h.step(t, fx) // the start the re-read found the pane for
	if ag.restarts != 1 {
		t.Errorf("starts after the retry: %d, want 1", ag.restarts)
	}
}

// RV-89 #1: a canceled re-read (serve shutting down, herdr gone) records nothing either: no needs-you, the clock kept,
// and a pass with a live context reads again.
func TestRereadCanceledContextRecordsNothing(t *testing.T) {
	t.Parallel()
	h, fx, _, _ := promptRig(t, false, noPane)
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	at := since(h)
	if at.IsZero() {
		t.Fatal("precondition: the no-pane clock started")
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot(freshPane(h, "w1:p1", h.lane("L-01"), nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := h.proj()
	h.a.mu.Lock()
	s := h.a.nextDispatch(p, *p.folded.loop["T-1"])
	h.a.mu.Unlock()
	if s == nil || s(ctx, fx) {
		t.Fatal("a canceled re-read answered the pass")
	}
	if got := since(h); !got.Equal(at) {
		t.Errorf("the no-pane clock after a canceled re-read is %s, want %s", got, at)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} after a canceled re-read: %d", n)
	}
	if got := known(h, "w1:p1").Cwd; got != h.root {
		t.Errorf("the pane's cwd after a canceled re-read is %q, want the snapshot's %q", got, h.root)
	}
	if !h.step(t, fx) {
		t.Fatal("the next pass did not read herdr's panes again")
	}
	if got := known(h, "w1:p1").Cwd; got != h.lane("L-01") {
		t.Errorf("the pane's cwd after the retry is %q, want the lane %q", got, h.lane("L-01"))
	}
}

// RV-89 #2: a pane the stream closes between the read and its use is not raised again: the fresh list answers only
// which pane is in the lane, never what the snapshot knows of it. A later read that finds none is the needs-you.
func TestRereadDoesNotResurrectAClosedPane(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, noPane)
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	at := since(h)
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot(freshPane(h, "w1:p1", h.lane("L-01"), nil))
	h.panes.after = func() { h.remove("w1:p1", port.EventClosed) } // the stream's close lands between the read and its use
	if h.step(t, fx) {
		t.Error("the pass went on with a pane the stream had closed")
	}
	if got := since(h); !got.Equal(at) {
		t.Errorf("the no-pane clock is %s after a pane the stream closed, want %s (the wait goes on)", got, at)
	}
	if got := known(h, "w1:p1"); got.ID != "" {
		t.Errorf("the closed pane came back: %+v", got)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} with a pane the read found: %d", n)
	}
	if n := h.count(event.KindIntent, event.StepStartWork); n != 0 {
		t.Errorf("a start into the closed pane: %d intents", n)
	}
	h.panes.after = nil
	h.panes.snap = snapshot() // herdr agrees with the stream: the pane is gone
	h.step(t, fx)
	if n := noPaneFacts(h); n != 1 {
		t.Errorf("needs-you{no pane in lane} after a read that finds none: %d, want 1", n)
	}
	if ag.restarts != 0 {
		t.Errorf("starts with no pane: %d", ag.restarts)
	}
}

// RV-89 #2: the re-read updates one pane's cwd and nothing else: the pane's other fields and every other pane stay
// what the stream last said, not what the older read lists.
func TestRereadUpdatesOnlyThePanesCwd(t *testing.T) {
	t.Parallel()
	h, fx, _, _ := promptRig(t, false, noPane)
	other := port.Pane{ID: "w1:p3", Workspace: "w1", Cwd: h.root, Agent: "claude", Label: "pj-l-03", Status: "idle"}
	h.snap.Panes = append(h.snap.Panes, other)
	h.a.event(context.Background(), port.Event{Kind: port.EventCreated, Pane: "w1:p3", New: other})
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot(
		freshPane(h, "w1:p1", h.lane("L-01"), func(p *port.Pane) { p.Status, p.Label = "working", "stale" }),
		freshPane(h, "w1:p3", h.root, func(p *port.Pane) { p.Status = "working" }))
	h.panes.after = func() { h.status("w1:p3", func(p *port.Pane) { p.Status = "idle" }) } // the newer status, mid-read
	if !h.step(t, fx) {
		t.Fatal("the re-read did not find the pane in the lane")
	}
	if got := known(h, "w1:p1"); got.Cwd != h.lane("L-01") || got.Status != "idle" || got.Label != "pj-l-01" {
		t.Errorf("the lane's pane after the re-read is %+v, want the lane's cwd and the stream's status and label", got)
	}
	if got := known(h, "w1:p3"); got.Status != "idle" {
		t.Errorf("another pane's status after the re-read is %q, want the stream's idle", got.Status)
	}
}
