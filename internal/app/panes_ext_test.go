package app_test

import (
	"strings"
	"testing"
	"time"
)

func TestReconnectReadsANewSnapshot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	if n := r.count("session.snapshot"); n == 0 {
		t.Fatal("no snapshot on connect")
	}
	before := r.count("session.snapshot")
	// herdr restarts; meanwhile L-01's pane is gone and L-02's agent got blocked.
	r.f.Remove("w1:p1")
	r.f.Pane("w1:p2").Status = "blocked"
	r.srv.DropConns()
	r.waitFacts("T-1 needs-you{pane exited}")
	if n := r.count("session.snapshot"); n <= before {
		t.Errorf("snapshots = %d, want more than the %d before the drop", n, before)
	}
	r.waitConnected()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 needs-you{pane exited}", "T-2 needs-you{blocked}")

	// events_lost ends the stream too: subscribe again, from a new snapshot.
	before = r.count("session.snapshot")
	r.srv.PushError("events_lost", "subscriber lagged")
	r.waitFor("a snapshot after events_lost", func() bool { return r.count("session.snapshot") > before })
	if !strings.Contains(r.out.String(), "events_lost") {
		t.Errorf("app output does not say why the stream ended:\n%s", r.out)
	}
}

// RV-36 #4: a workspace opened after the connect ends the stream (Code refresh); the app subscribes again at once,
// with no stream-ended line, and its new snapshot registers the workspace's panes.
func TestRefreshSubscribesAgainQuietly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Ungroup("w1") // pj's workspace w1 is not open yet
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	if n := len(r.app.Status().Project.Agents); n != 0 {
		t.Fatalf("agents before pj's workspace opened: %d, want 0", n)
	}
	before := r.count("session.snapshot")
	r.srv.WaitSubscribed(t, "workspace.created", "")
	r.f.Group("w1", "pj")
	r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
		"workspace": map[string]any{"workspace_id": "w1", "label": "pj"}})
	r.waitFor("a snapshot after the refresh", func() bool { return r.count("session.snapshot") > before })
	r.waitFor("pj's agents", func() bool { return len(r.app.Status().Project.Agents) == 2 })
	if strings.Contains(r.out.String(), "stream ended") {
		t.Errorf("a refresh logged a stream end:\n%s", r.out)
	}
	// T-1's quiet baseline at 0 before the clock moves: the stall counts from the first sample of the identified
	// agent, and the refresh's check is not the test's to order (a sample taken at 11 min never stalls).
	r.waitQuiet("T-1", r.clock.Now())
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
}
