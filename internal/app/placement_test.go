// placement_test.go: which pane of a lane dir serve starts the lane's agent in (paneIn, T-783): the label is the
// pane's identity (R-28 §2), so `obor setup pane L-0N` — a labelled shell with no agent in it yet — is the place the
// lane's first dispatch uses, not a pane of a lower id or one someone left an agent in.
package app

import (
	"path/filepath"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// labelRig is pj connected with lanes L-01 and L-02 (workers pj-l-01 and pj-l-02) and herdr's session exactly panes(h);
// before acts on the harness after the panes are in and before the connect takes them (a start row, T-504).
func labelRig(t *testing.T, panes func(h *harness) []port.Pane, before ...func(h *harness)) *harness {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","lanes":2}`)
	h.lanes.lanes[0].Agent = "pj-l-01"
	h.snap.Panes = panes(h)
	for _, fn := range before {
		fn(h)
	}
	h.connect()
	return h
}

// placed is paneIn over pj's snapshot for dir with names: the pane's id ("" none; under mu).
func placed(h *harness, dir string, names ...string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if o := h.a.placement(h.proj(), dir, names...); o != nil {
		return o.pane.ID
	}
	return ""
}

// labelPane is a pane of pj's workspace at cwd, at its shell, labelled label ("" none).
func labelPane(id, cwd, label string) port.Pane {
	return port.Pane{ID: id, Workspace: "w1", Cwd: cwd, Label: label}
}

// T-783: the pane labelled for the lane's agent wins over a pane at its shell of a lower id, and over one that runs an
// agent; a pane labelled for another agent is never chosen (not even when the table identifies it as the lane's), and
// identification still beats a label.
func TestPaneInRanksTheLabel(t *testing.T) {
	t.Run("a labelled shell beats a lower-id shell", func(t *testing.T) {
		t.Parallel()
		h := labelRig(t, func(h *harness) []port.Pane {
			return []port.Pane{labelPane("w1:p0", h.lane("L-01"), ""), labelPane("w1:p2", h.lane("L-01"), "pj-l-01")}
		})
		if got := placed(h, h.lane("L-01"), "pj-l-01"); got != "w1:p2" {
			t.Errorf("placed = %q, want the labelled pane w1:p2", got)
		}
	})

	t.Run("a labelled shell beats a lower-id pane running an agent", func(t *testing.T) {
		t.Parallel()
		h := labelRig(t, func(h *harness) []port.Pane {
			return []port.Pane{{ID: "w1:p0", Workspace: "w1", Cwd: h.lane("L-01"), Agent: "claude", Status: "idle"},
				labelPane("w1:p2", h.lane("L-01"), "pj-l-01")}
		})
		if got := placed(h, h.lane("L-01"), "pj-l-01"); got != "w1:p2" {
			t.Errorf("placed = %q, want the labelled pane w1:p2", got)
		}
	})

	t.Run("a pane labelled for another agent only: none", func(t *testing.T) {
		t.Parallel()
		h := labelRig(t, func(h *harness) []port.Pane {
			return []port.Pane{labelPane("w1:p0", h.lane("L-01"), "pj-l-02")}
		})
		if got := placed(h, h.lane("L-01"), "pj-l-01"); got != "" {
			t.Errorf("placed = %q, want no pane (w1:p0 is labelled for pj-l-02)", got)
		}
	})

	t.Run("identified as the lane's agent but labelled another: none", func(t *testing.T) {
		t.Parallel()
		h := labelRig(t, func(h *harness) []port.Pane {
			return []port.Pane{{ID: "w1:p1", Workspace: "w1", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-02",
				Status: "idle", Terminal: "t1"}}
		}, func(h *harness) { h.started("pj-l-01", "w1:p1", "t1", "s1") })
		if who := h.who("w1:p1"); who != "pj-l-01" {
			t.Fatalf("precondition: w1:p1 is %q by the table, want pj-l-01", who)
		}
		if got := placed(h, h.lane("L-01"), "pj-l-01"); got != "" {
			t.Errorf("placed = %q, want no pane (its label belongs to pj-l-02)", got)
		}
	})

	t.Run("identified beats labelled", func(t *testing.T) {
		t.Parallel()
		h := labelRig(t, func(h *harness) []port.Pane {
			return []port.Pane{
				{ID: "w1:p0", Workspace: "w1", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle",
					Terminal: "t0"},
				{ID: "w1:p1", Workspace: "w1", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle",
					Terminal: "t1"}}
		}, func(h *harness) { h.started("pj-l-01", "w1:p1", "t1", "s1") })
		if who := h.who("w1:p0"); who != "" {
			t.Fatalf("precondition: w1:p0 is %q by the table, want nobody (w1:p1 holds the start)", who)
		}
		if got := placed(h, h.lane("L-01"), "pj-l-01"); got != "w1:p1" {
			t.Errorf("placed = %q, want the identified pane w1:p1, not the lower-id labelled w1:p0", got)
		}
	})
}

// T-783: a lane dir whose only pane is labelled for another agent is no start: the dispatch's agent.start waits for a
// pane of its own (the no-pane clock) instead of launching into it and being refused.
func TestLabelOfAnotherAgentWaitsForItsOwnPane(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) { // pj-l-02 names lane L-02's worker
		h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","lanes":2,`+
			`"roles":{"integrator":""}}`)
		h.pane("w1:p1").Label = "pj-l-02"
	})
	if h.step(t, fx) {
		t.Fatal("the pass went on with a pane labelled for another agent")
	}
	if since(h).IsZero() {
		t.Error("the no-pane clock did not start")
	}
	if ag.restarts != 0 {
		t.Errorf("launches into a pane of another agent: %d", ag.restarts)
	}
	if n := h.count(event.KindIntent, event.StepStartWork); n != 0 {
		t.Errorf("agent.start intents: %d, want 0", n)
	}
}
