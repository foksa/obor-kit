package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
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
		`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{`+
			strings.Replace(harnessModels, `"id":"sonnet"`, `"id":"sonnet-4-6"`, 1)+`}}`)
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

// A keep lane's handoff size is the kind's read of its session's transcript (project.ContextOf over the project's
// kinds): the probe line no compiled kind reads sizes pj-l-01's session only through the fake kind, and the kind's
// count, not a fixed yes, is what the threshold sees.
func TestPastHandoffReadsTheKindsContext(t *testing.T) {
	t.Parallel()
	h, _ := seedSched(t, false)
	probe := `{"type":"ctx-probe","tokens":%d,"at":"2026-10-13T10:00:00Z"}`
	for _, name := range []string{"claude", "codex"} {
		k, ok := (agents.Kinds{}).Lookup(name)
		if !ok {
			t.Fatalf("precondition: kind %s", name)
		}
		if _, ok := k.Context([]byte(fmt.Sprintf(probe+"\n", 900000))); ok {
			t.Fatalf("precondition: %s reads the probe line", name)
		}
	}
	file := filepath.Join(t.TempDir(), "t.jsonl")
	write := func(tokens int) {
		t.Helper()
		if err := os.WriteFile(file, []byte(fmt.Sprintf(probe+"\n", tokens)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(900000)
	h.session("pj-l-01", "s1", file, 100)
	h.a.opt.Kinds = func() agents.Kinds { return agents.WithPlugins([]agents.Kind{ctxKind{}}) }
	p := reloadProj(t, h)
	cfg := loadCfg(t, h.root)
	pane := port.Pane{ID: "w1:p1"}
	past := func() bool {
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		return h.a.pastHandoff(p, cfg, pane)
	}
	if !past() {
		t.Error("pastHandoff = false, want true: the kind reads 900000 tokens of pj-l-01's session")
	}
	write(10)
	if past() {
		t.Error("pastHandoff = true at the kind's 10 tokens, want false")
	}
}

// ctxKind is a plugin kind whose context read is the probe line's (its own format, no compiled kind's).
type ctxKind struct{ agents.Kind }

func (ctxKind) Name() string      { return "ctxprobe" }
func (ctxKind) Caps() agents.Caps { return agents.Caps{Transcript: true} }
func (ctxKind) Context(tail []byte) (agents.Context, bool) {
	var l struct {
		Type   string `json:"type"`
		Tokens int    `json:"tokens"`
		At     string `json:"at"`
	}
	if json.Unmarshal(bytes.TrimSpace(tail), &l) != nil || l.Type != "ctx-probe" {
		return agents.Context{}, false
	}
	at, err := time.Parse(time.RFC3339, l.At)
	return agents.Context{Tokens: l.Tokens, At: at}, err == nil
}

// A kept Claude agent herdr reports no session for is the start's when the session recorded from its pane is the
// start row's (reused), and is not when another session was recorded from the pane since (stopped, started fresh).
func TestStartFitsReadsTheRecordedSession(t *testing.T) {
	t.Parallel()
	h, _ := seedSched(t, false)
	dispatchSonnet(t, h)
	cfg, err := config.Load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	startReported(t, h, cfg.Rev())
	pane := port.Pane{ID: "w1:p1"} // herdr reports no agent_session
	fits := func() bool {
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		return h.proj().startFits(cfg, "pj-l-01", pane, "sonnet")
	}
	if fits() {
		t.Fatal("precondition: startFits with no session recorded and none from herdr = true, want false")
	}
	h.session("pj-l-01", "new-session", "/t/new-session.jsonl", 100)
	if !fits() {
		t.Error("startFits with the start's session recorded from the pane = false, want true: the agent is reused")
	}
	h.session("pj-l-01", "by-hand", "/t/by-hand.jsonl", 200)
	if fits() {
		t.Error("startFits with another session recorded from the pane = true, want false: a fresh start")
	}
}
