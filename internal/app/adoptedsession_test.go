package app

import (
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/store"
)

// adopted is the report a start that resumed herdr's live session live-1 in w1:p1 makes once its agent runs (launch's
// reportResume, through serve's launcher's Report): no start time (serve stamps it), the start's kind and pane.
var adopted = store.Session{Agent: "pj-l-01", Kind: "claude", ID: "live-1", Pane: "w1:p1", Lane: "L-01",
	Source: "launch"}

// staleRecord records pj-l-01's earlier session sid-old of another kind, from the same pane w1:p1: the record a start
// that adopted herdr's live session left current before launch recorded what it resumed (RV-240 #2).
func staleRecord(t *testing.T, h *harness) {
	t.Helper()
	if err := h.a.Report(h.root, store.Session{Agent: "pj-l-01", Kind: "codex", ID: "sid-old", Pane: "w1:p1",
		Lane: "L-01", Started: 100, Source: "launch"}); err != nil {
		t.Fatal(err)
	}
}

// The keep lane's reuse check (startFits) reads the session a start adopted once launch records it: the start row
// names herdr's live-1, the pane runs it, and a stale record of another kind from the pane made the agent no fit
// (stopped and started fresh); the adopted session's record makes it the start's (reused).
func TestStartFitsSeesTheSessionAStartAdopted(t *testing.T) {
	t.Parallel()
	h, _ := seedSched(t, false)
	dispatchSonnet(t, h)
	cfg, err := config.Load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	addRecord(t, h, "T-1", event.StepStartWork, event.KindResult, event.StartOut{Line: "RESTARTED", Agent: "pj-l-01",
		Pane: "w1:p1", Kind: "claude", Session: "live-1", Config: cfg.Rev()})
	staleRecord(t, h)
	pane := port.Pane{ID: "w1:p1", Session: "live-1"} // herdr's agent_session: the session the pane runs
	fits := func() bool {
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		return h.proj().startFits(cfg, "pj-l-01", pane, "sonnet")
	}
	if fits() {
		t.Fatal("precondition: startFits with the stale record from the pane = true, want false (the record wins)")
	}
	if err := h.a.Report(h.root, adopted); err != nil {
		t.Fatal(err)
	}
	if !fits() {
		t.Error("startFits with the adopted session recorded = false, want true: the agent is the start's, reused")
	}
}

// The message identity check of a pane (inboxedAt) reads the session a start adopted once launch records it: the
// agent's inbox names live-1, the pane runs it, and a stale record of another kind from the pane made the inbox
// another session's (not the pane's); the adopted session's record makes it the pane's.
func TestInboxIdentitySeesTheSessionAStartAdopted(t *testing.T) {
	t.Parallel()
	h, _ := seedSched(t, false)
	h.a.opt.Inboxed = func(agent string) (string, bool) { return "live-1", agent == "pj-l-01" }
	staleRecord(t, h)
	w := Where{State: Identified, Pane: port.Pane{ID: "w1:p1", Session: "live-1"}}
	inboxed := func() bool {
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		return h.a.inboxedAt(h.proj(), "pj-l-01", w)
	}
	if inboxed() {
		t.Fatal("precondition: inboxedAt with the stale record from the pane = true, want false (the record wins)")
	}
	if err := h.a.Report(h.root, adopted); err != nil {
		t.Fatal(err)
	}
	if !inboxed() {
		t.Error("inboxedAt with the adopted session recorded = false, want true: the inbox is the pane's session")
	}
}
