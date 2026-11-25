package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// noPane is the harness's snapshot before its connect: the lane's pane is outside it, so T-1's start takes the no-pane
// path and no watchdog sees an agent exit.
func noPane(h *harness) {
	h.pane("w1:p1").Cwd = h.root
}

// freshPane is a pane of pj's workspace as a fresh read of herdr's panes lists it (fn: any other field the read's).
func freshPane(h *harness, id, cwd string, fn func(p *port.Pane)) port.Pane {
	p := port.Pane{ID: id, Group: "pj", Cwd: cwd, Agent: "claude", Label: "pj-l-01", Status: "idle"}
	if fn != nil {
		fn(&p)
	}
	return p
}

// snapshot is panes as herdr's next read answers them.
func snapshot(panes ...port.Pane) *port.Snapshot { return &port.Snapshot{Panes: panes} }

// noPaneReasons is the reason of every needs-you{no pane in lane …} fact T-1 has.
func noPaneReasons(h *harness) []string {
	var out []string
	for _, r := range h.records() {
		if r.Task != "T-1" || r.Kind != event.KindFact || r.Step != event.StepNeedsYou ||
			!strings.Contains(string(r.Data), causeNoPane) {
			continue
		}
		var f event.FactData
		_ = json.Unmarshal(r.Data, &f)
		out = append(out, f.Reason)
	}
	return out
}

// noPaneFacts is how many needs-you{no pane in lane …} facts T-1 has.
func noPaneFacts(h *harness) int { return len(noPaneReasons(h)) }

// reread_test.go: the re-read a lane's start makes when no pane of the lane is in the snapshot — what it may
// change and what it may not.

// A failed re-read is no answer. It starts no start and no needs-you, leaves the no-pane clock where it was
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
	if len(h.placed) != 0 {
		t.Errorf("a failed re-read placed a pane in %v, want none (nothing is placed on no answer)", h.placed)
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
	if len(ag.Launches()) != 1 {
		t.Errorf("starts after the retry: %d, want 1", len(ag.Launches()))
	}
	if len(h.placed) != 0 {
		t.Errorf("a pane the read found was not used: placed one in %v", h.placed)
	}
}

// A canceled re-read (serve shutting down, herdr gone) records nothing either: no needs-you, the clock kept,
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
	s := h.a.nextDispatch(p, *p.folded.loop["T-1"], loadCfg(t, p.root))
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
	if len(h.placed) != 0 {
		t.Errorf("a canceled re-read placed a pane in %v, want none", h.placed)
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

// A re-read that answers while serve stops records nothing either: its answer ran with the stop already in force, so
// the pass places no pane and raises nothing, and the next serve starts the wait again.
func TestRereadCanceledAfterItsAnswerRecordsNothing(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, noPane)
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	if since(h).IsZero() {
		t.Fatal("precondition: the no-pane clock started")
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot() // the read finds no pane: its answer would be the needs-you
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.panes.after = cancel // serve stops once the read has answered
	p := h.proj()
	h.a.mu.Lock()
	s := h.a.nextDispatch(p, *p.folded.loop["T-1"], loadCfg(t, p.root))
	h.a.mu.Unlock()
	if s == nil {
		t.Fatal("precondition: the wait's step")
	}
	went := s(ctx, fx)
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} from a read serve's stop cut from its use: %d, want none", n)
	}
	if len(h.placed) != 0 {
		t.Errorf("a read serve's stop cut from its use placed a pane in %v, want none", h.placed)
	}
	if went {
		t.Error("the canceled pass went on")
	}
	if len(ag.Launches()) != 0 {
		t.Errorf("starts with no pane: %d", len(ag.Launches()))
	}
	h.panes.after = nil
	if h.step(t, fx) {
		t.Fatal("precondition: the pass of the next serve waits again")
	}
	if got := since(h); !got.Equal(h.now) {
		t.Errorf("the no-pane clock after serve's stop is %s, want the wait started again at %s", got, h.now)
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot()
	h.step(t, fx)
	if len(h.placed) != 1 || h.placed[0] != "L-01" {
		t.Errorf("the next serve placed a pane for %v, want one for L-01", h.placed)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} of a placement: %d, want none", n)
	}
}

// A pane the stream closes between the read and its use is not raised again: the fresh list answers only
// which pane is in the lane, never what the snapshot knows of it. A later read that finds none places a pane.
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
	if len(h.placed) != 1 || h.placed[0] != "L-01" {
		t.Errorf("a read that finds none placed a pane for %v, want one for L-01", h.placed)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} of a placement: %d, want none", n)
	}
	if len(ag.Launches()) != 0 {
		t.Errorf("starts before the placed pane exists: %d", len(ag.Launches()))
	}
}

// The re-read updates one pane's cwd and nothing else: the pane's other fields and every other pane stay
// what the stream last said, not what the older read lists.
func TestRereadUpdatesOnlyThePanesCwd(t *testing.T) {
	t.Parallel()
	h, fx, _, _ := promptRig(t, false, noPane)
	other := port.Pane{ID: "w1:p3", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-l-03", Status: "idle"}
	h.snap.Panes = append(h.snap.Panes, other)
	eventNow(h.a, port.Event{Kind: port.EventCreated, Pane: "w1:p3", New: other})
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

// relabelRig is T-1's start with the lane's only pane labelled pj-l-02 (lane L-02's worker; lanes: the config's
// count), past its first pass (the no-pane clock started) and Gone later: the next pass reads herdr's panes again.
func relabelRig(t *testing.T, lanes int) (*harness, *Loop, *FakeAgents) {
	t.Helper()
	h, fx, _, ag := promptRig(t, false, func(h *harness) {
		h.write(filepath.Join(h.root, ".obor", "config.json"), fmt.Sprintf(`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":%d},`+
			`"agents":{"roles":{"integrator":""}}}`, lanes))
		h.pane("w1:p1").Label = "pj-l-02"
	})
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds only a pane of another agent waits")
	}
	if since(h).IsZero() {
		t.Fatal("precondition: the no-pane clock started")
	}
	h.now = h.now.Add(2 * minute)
	return h, fx, ag
}

// The lane's only pane, labelled for another agent, is relabelled to the lane's agent (a rename gives no
// event: only the fresh read has it). The re-read brings the label into the snapshot, the wait ends, and the next
// pass launches there once — not the clock started again with no launch and no needs-you.
func TestRereadSeesAPaneRelabelledToTheLanesAgent(t *testing.T) {
	t.Parallel()
	h, fx, ag := relabelRig(t, 2)
	h.panes.snap = snapshot(freshPane(h, "w1:p1", h.lane("L-01"), nil)) // labelled pj-l-01 now
	if got := known(h, "w1:p1").Label; got != "pj-l-02" {
		t.Fatalf("precondition: the snapshot's label is %q, want the old pj-l-02", got)
	}
	if !h.step(t, fx) {
		t.Fatalf("the re-read did not settle the wait (%d reads)", h.panes.snaps)
	}
	if got := known(h, "w1:p1").Label; got != "pj-l-01" {
		t.Errorf("the pane's label after the re-read is %q, want the read's pj-l-01", got)
	}
	if got := since(h); !got.IsZero() {
		t.Errorf("the no-pane clock after a usable re-read is %s, want cleared", got)
	}
	h.step(t, fx) // the start the re-read found the pane for
	if len(ag.Launches()) != 1 || lastPane(ag) != "w1:p1" {
		t.Errorf("launched %d time(s) in %q, want once in w1:p1", len(ag.Launches()), lastPane(ag))
	}
	if got := since(h); !got.IsZero() {
		t.Errorf("the no-pane clock started again: %s", got)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane}: %d, want 0", n)
	}
	if len(h.placed) != 0 {
		t.Errorf("a pane the read found was not used: placed one in %v", h.placed)
	}
}

// Relabelled to yet another agent of the project (pj-l-03), the pane is still not the lane's: the re-read
// finds none and serve places a pane for the lane's worker — no launch into the pane of another agent, no needs-you.
func TestRereadOfAPaneRelabelledToAnotherAgentPlacesAPane(t *testing.T) {
	t.Parallel()
	h, fx, ag := relabelRig(t, 3)
	h.panes.snap = snapshot(freshPane(h, "w1:p1", h.lane("L-01"), func(p *port.Pane) { p.Label = "pj-l-03" }))
	reads := h.panes.snaps
	h.step(t, fx)
	if h.panes.snaps != reads+1 {
		t.Fatalf("precondition: the pass read herdr's panes %d time(s), want once", h.panes.snaps-reads)
	}
	if len(h.placed) != 1 || h.placed[0] != "L-01" {
		t.Errorf("the re-read that finds none placed a pane for %v, want one for L-01", h.placed)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} after a placement: %d, want 0", n)
	}
	if got := since(h); !got.IsZero() {
		t.Errorf("the no-pane clock after the placement is %s, want cleared", got)
	}
	if len(ag.Launches()) != 0 {
		t.Errorf("launches into a pane of another agent: %d", len(ag.Launches()))
	}
}

// A dispatch to a lane with no pane: after Gone herdr's panes are read again, none is in the lane, and serve places
// one (PlaceLane, obor setup pane <LANE>'s placement); the created pane starts the worker, and no needs-you goes out.
func TestLaneWithNoPaneGetsAPanePlaced(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, noPane)
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	if since(h).IsZero() {
		t.Fatal("precondition: the no-pane clock started")
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot() // herdr lists no pane in the lane either
	if !h.step(t, fx) {
		t.Fatalf("the placement answered as no progress (%d reads)", h.panes.snaps)
	}
	if len(h.placed) != 1 || h.placed[0] != "L-01" {
		t.Fatalf("placed a pane for %v, want one for L-01", h.placed)
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} of a placement: %d, want 0", n)
	}
	// herdr made the pane: the created event carries its cwd and label into the snapshot, and the next pass starts
	// the worker in it.
	p := port.Pane{ID: "w1:p9", Group: "pj", Cwd: h.lane("L-01"), Label: "pj-l-01", Status: "idle"}
	h.snap.Panes = append(h.snap.Panes, p)
	eventNow(h.a, port.Event{Kind: port.EventCreated, Pane: p.ID, New: p})
	h.step(t, fx)
	if len(ag.Launches()) != 1 || lastPane(ag) != "w1:p9" {
		t.Errorf("launched %d time(s) in %q, want once in the placed pane w1:p9", len(ag.Launches()), lastPane(ag))
	}
	if n := noPaneFacts(h); n != 0 {
		t.Errorf("needs-you{no pane in lane} once the pane is placed: %d, want 0", n)
	}
	if len(h.placed) != 1 {
		t.Errorf("placements after the start: %v, want the one", h.placed)
	}
}

// A placement that fails is needs-you{no pane in lane} carrying the placement's error and the command that places the
// pane by hand; nothing starts.
func TestFailedPlacementIsNeedsYou(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, noPane)
	fx.PlaceLane = func(context.Context, string) (string, error) {
		return "", errors.New("terminal pane split w1:p1 failed: boom")
	}
	if h.step(t, fx) {
		t.Fatal("precondition: the pass that finds no pane waits")
	}
	h.now = h.now.Add(2 * minute)
	h.panes.snap = snapshot()
	h.step(t, fx)
	reasons := noPaneReasons(h)
	if len(reasons) != 1 {
		t.Fatalf("needs-you{no pane in lane} of a failed placement: %d, want 1", len(reasons))
	}
	for _, want := range []string{"no pane in lane L-01", "terminal pane split w1:p1 failed: boom", "obor setup pane L-01"} {
		if !strings.Contains(reasons[0], want) {
			t.Errorf("the needs-you's reason %q does not name %q", reasons[0], want)
		}
	}
	if len(ag.Launches()) != 0 {
		t.Errorf("a start without a pane: %d", len(ag.Launches()))
	}
	if len(h.placed) != 0 {
		t.Errorf("the harness's own placement ran: %v", h.placed)
	}
}
