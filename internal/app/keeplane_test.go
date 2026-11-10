package app

import (
	"path/filepath"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/config"
)

// dispatchSonnet folds T-1's dispatch onto L-01 as sonnet: the start's row takes its ref at the fold (foldStart).
func dispatchSonnet(t *testing.T, h *harness) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
		Agent: "pj-l-01", Type: "sonnet", Target: "target", Branch: "task/T-1"})
}

// startReported folds pj-l-01's agent.start result in w1:p1 with the config revision rev ("": an older record, which
// carries none).
func startReported(t *testing.T, h *harness, rev string) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepStartWork, event.KindResult, startOut{Line: "STARTED", Agent: "pj-l-01",
		Pane: "w1:p1", Kind: "claude", Session: "new-session", Config: rev})
}

// startconfig_test.go: the config revision a start records. The identity table's start row keeps the revision
// the start's result carried, never the file's now: a keep lane's reuse checks the config its agent was launched under
// (startFits), and a replay of the log gives the same answer after the config changed under it.

// A start record without a config revision (an older one) is no fit — the kept agent is stopped and
// started fresh, the safe path — while the same start with the revision now recorded fits the same config: the
// revision is the one thing that differs.
func TestStartFitsNeedsTheRecordedConfigRev(t *testing.T) {
	t.Parallel()
	h, _ := seedSched(t, false)
	dispatchSonnet(t, h)
	startReported(t, h, "")
	cfg, err := config.Load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	pane := port.Pane{ID: "w1:p1", Session: "new-session"}
	h.a.mu.Lock()
	p := h.proj()
	row, ok := p.reg.Start("pj-l-01")
	fits := p.startFits(cfg, "pj-l-01", pane, "sonnet")
	h.a.mu.Unlock()
	if !ok || row.Ref != "sonnet" || row.Kind != "claude" || row.Session != "new-session" || row.Config != "" {
		t.Fatalf("precondition: the old record's row %+v, want ref sonnet, claude, the session, no config revision", row)
	}
	if fits {
		t.Error("startFits of a start recorded before T-1050 = true, want false: no revision, no match, a restart")
	}
	startReported(t, h, cfg.Rev())
	h.a.mu.Lock()
	row, _ = p.reg.Start("pj-l-01")
	fits = p.startFits(cfg, "pj-l-01", pane, "sonnet")
	h.a.mu.Unlock()
	if row.Config != cfg.Rev() || !fits {
		t.Errorf("startFits of the same start with its config revision = %v (row %+v), want true", fits, row)
	}
}

// The start row's config revision is the record's, not the config file's: after the config changes under the
// agent (models.sonnet.model another model id) and the log is replayed, the row keeps the revision it recorded and no
// reuse is a fit.
func TestStartRowKeepsItsConfigRevAcrossAReplay(t *testing.T) {
	t.Parallel()
	h, _ := seedSched(t, false)
	cfgA, err := config.Load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	dispatchSonnet(t, h)
	startReported(t, h, cfgA.Rev())
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"models":{"sonnet":{"kind":"claude","id":"sonnet-4-6"}}}}`)
	cfgB, err := config.Load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if cfgB.Rev() == cfgA.Rev() {
		t.Fatalf("precondition: another model id is another config revision: both %s", cfgA.Rev())
	}
	p, err := h.a.load(h.root) // the log replayed: the row is the record's, never the file's now
	if err != nil {
		t.Fatal(err)
	}
	h.a.mu.Lock()
	row, ok := p.reg.Start("pj-l-01")
	fits := p.startFits(cfgB, "pj-l-01", port.Pane{ID: "w1:p1", Session: "new-session"}, "sonnet")
	h.a.mu.Unlock()
	if !ok || row.Config != cfgA.Rev() {
		t.Errorf("the replayed start row %+v (ok %v), want the recorded revision %s", row, ok, cfgA.Rev())
	}
	if fits {
		t.Error("startFits after the replay = true, want false: the start recorded another config revision")
	}
}
