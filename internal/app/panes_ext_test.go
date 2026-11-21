package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
)

func TestReconnectReadsANewSnapshot(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.start()
		if n := r.calls(opSnapshot); n == 0 {
			t.Fatal("no snapshot on connect")
		}
		// connectedLine is connect's line: a stream applied, its snapshot read before.
		const connectedLine = "terminal: connected; "
		connects := strings.Count(r.out.String(), connectedLine)
		before := r.calls(opSnapshot)
		// herdr restarts; meanwhile L-01's pane is gone and L-02's agent got blocked.
		r.removePane("w1:p1")
		r.setPane("w1:p2", func(p *port.Pane) { p.Status = "blocked" })
		r.endStream()
		// A reconnect is a new stream: wait for its connected line, then for the count its snapshot raised; the facts
		// that snapshot gives follow.
		r.waitFor("the reconnect", func() bool { return strings.Count(r.out.String(), connectedLine) > connects })
		if n := r.calls(opSnapshot); n <= before {
			t.Errorf("snapshots = %d, want more than the %d before the drop", n, before)
		}
		r.waitFacts("T-1 needs-you{pane exited}", "T-2 needs-you{blocked}")
		r.waitConnected()

		// events_lost ends the stream too: the app says why, then subscribes again, from a new snapshot.
		before = r.calls(opSnapshot)
		r.streamLost("events_lost", "subscriber lagged")
		r.waitFor("the app to say the stream ended: events_lost", func() bool {
			return strings.Contains(r.out.String(), "events_lost")
		})
		r.waitFor("a snapshot after events_lost", func() bool { return r.calls(opSnapshot) > before })
	})
}

// A workspace opened after the connect ends the stream (Code refresh); the app subscribes again at once, with no
// stream-ended line, and its new snapshot registers the workspace's panes.
func TestRefreshSubscribesAgainQuietly(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.ungroup("w1") // pj's workspace w1 is not open yet
		r.setPane("w1:p2", func(p *port.Pane) { p.Status = "working" })
		r.start()
		if n := len(r.app.Status().Project.Agents); n != 0 {
			t.Fatalf("agents before pj's workspace opened: %d, want 0", n)
		}
		before := r.calls(opSnapshot)
		r.waitSubscribed(t, refreshEnds, "")
		r.group("w1", "pj")
		r.refresh()
		r.waitFor("a snapshot after the refresh", func() bool { return r.calls(opSnapshot) > before })
		r.waitFor("pj's agents", func() bool { return len(r.app.Status().Project.Agents) == 2 })
		if strings.Contains(r.out.String(), "stream ended") {
			t.Errorf("a refresh logged a stream end:\n%s", r.out)
		}
		// T-1's quiet baseline at 0 before the clock moves: the stall counts from the first sample of the identified
		// agent, and the refresh's check is not the test's to order (a sample taken at 11 min never stalls).
		r.waitQuiet("T-1", r.clock.Now())
		r.clock.Add(11 * time.Minute)
		r.waitFacts("T-1 stalled{idle}")
	})
}
