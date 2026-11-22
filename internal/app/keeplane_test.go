package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
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

// A keep lane's handoff size is its session's kind's read of the session's transcript (project.ContextOf, that kind
// alone): the probe line no compiled kind reads sizes pj-l-01's session recorded as ctxprobe through that kind, and the
// kind's count, not a fixed yes, is what the threshold sees; the same transcript of a session recorded as claude is
// not past (ctxprobe is not asked).
func TestHandoffSizeReadsTheSessionsKind(t *testing.T) {
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
	record := func(kind, id string) {
		t.Helper()
		if _, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: "pj-l-01", Kind: kind, ID: id,
			Transcript: file, Pane: "w1:p1", Lane: "L-01", Task: "T-1", Started: 100, Source: "SessionStart hook"})); err != nil {
			t.Fatal(err)
		}
	}
	write(900000)
	record("ctxprobe", "s1")
	h.a.opt.Kinds = func() agents.Kinds { return agents.WithPlugins([]agents.Kind{ctxKind{}}) }
	p := reloadProj(t, h)
	cfg := loadCfg(t, h.root)
	pane := port.Pane{ID: "w1:p1"}
	past := func() bool {
		h.a.mu.Lock()
		r := h.a.sizeOf(p, "pj-l-01", pane)
		h.a.mu.Unlock()
		return h.a.pastRead(p, r, cfg.HandoffTokens())
	}
	if !past() {
		t.Error("pastRead = false, want true: the kind reads 900000 tokens of pj-l-01's session")
	}
	write(10)
	if past() {
		t.Error("pastRead = true at the kind's 10 tokens, want false")
	}
	write(900000)
	record("claude", "s2")
	p = reloadProj(t, h)
	if past() {
		t.Error("pastRead = true for a session recorded as claude: only its kind reads its transcript")
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

// readKind is the probe kind (ctxKind) whose context read first runs during, a test's hook: a kind call that blocks
// or changes serve's state while it runs.
type readKind struct {
	ctxKind
	during func()
}

func (k readKind) Context(tail []byte) (agents.Context, bool) {
	if k.during != nil {
		k.during()
	}
	return k.ctxKind.Context(tail)
}

// handoffSeed is pj with L-01 a keep lane (handoff_tokens 1000) between tasks: T-1 dispatched there in keep mode and
// merged, pj-l-01 idle in w1:p1, its session recorded as the probe kind with a transcript of 900000 tokens, the probe
// kind's read k's (readKind); the project dispatches. It returns the project and the rule pass's adapters.
func handoffSeed(t *testing.T, k *readKind) (*harness, *proj, *Loop) {
	t.Helper()
	h, f := seedSched(t, false)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},`+
		`"lanes":{"list":[{"name":"L-01","keep_agent":true}]},"agents":{"handoff_tokens":1000,`+harnessModels+`}}`)
	file := filepath.Join(t.TempDir(), "t.jsonl")
	h.write(file, `{"type":"ctx-probe","tokens":900000,"at":"2026-10-13T10:00:00Z"}`+"\n")
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
		Agent: "pj-l-01", Type: "sonnet", Target: "target", Branch: "task/T-1", Keep: true})
	addRecord(t, h, "T-1", event.StepMerged, event.KindFact, event.MergedData{Head: "m1"})
	if _, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: "pj-l-01", Kind: "ctxprobe", ID: "s1",
		Transcript: file, Pane: "w1:p1", Lane: "L-01", Task: "T-1", Started: 100, Source: "SessionStart hook"})); err != nil {
		t.Fatal(err)
	}
	h.a.opt.Kinds = func() agents.Kinds { return agents.WithPlugins([]agents.Kind{k}) }
	p := reloadProj(t, h)
	h.a.mu.Lock()
	p.dispatching = true
	key, between := p.keepBetween("L-01")
	h.a.mu.Unlock()
	if !between || key == 0 {
		t.Fatalf("precondition: L-01 between tasks (key %d, %v), want T-1's merge", key, between)
	}
	return h, p, withAppCfg(h.a, &Loop{Agents: f})
}

// A kind whose context read blocks holds no lock of serve's: while the handoff rule's read of the kept agent's
// session blocks, obor status (a verb that takes App.mu) completes within its deadline, and once the read returns the
// rule acts on the lane (the session past handoff_tokens).
func TestHandoffRuleReadsTheSizeOutsideTheLock(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	k := &readKind{during: func() {
		once.Do(func() { close(entered) })
		<-release
	}}
	h, p, fx := handoffSeed(t, k)
	type ruled struct {
		key int64
		ok  bool
	}
	done := make(chan ruled, 1)
	go func() {
		key, _, ok := handoffRule(context.Background(), h.a, p, fx, h.now)
		done <- ruled{key, ok}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("precondition: the handoff rule never read the kept agent's size")
	}
	status := make(chan struct{})
	go func() {
		h.a.Status()
		close(status)
	}()
	select {
	case <-status:
	case <-time.After(5 * time.Second):
		t.Error("obor status blocked while the kind's read blocked: the rule reads under App.mu")
	}
	close(release)
	<-status
	r := <-done
	h.a.mu.Lock()
	key, _ := p.keepBetween("L-01")
	h.a.mu.Unlock()
	if !r.ok || r.key != key {
		t.Errorf("handoffRule after the read = key %d, %v; want T-1's merge key %d, true", r.key, r.ok, key)
	}
}

// A handoff candidate that changes between its read and the check under the lock again is not acted on this pass:
// the same lane read with nothing changed acts (the precondition); a new session recorded from the agent's pane, or
// the agent turning busy, while its size is read, and the rule does nothing.
func TestHandoffRuleSkipsACandidateChangedSinceItsRead(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		change func(h *harness)
	}{
		{"new session", func(h *harness) { h.session("pj-l-01", "s2", "/t/s2.jsonl", 200) }},
		{"busy", func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Status = "working" }) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := &readKind{}
			h, p, fx := handoffSeed(t, k)
			if _, _, ok := handoffRule(context.Background(), h.a, p, fx, h.now); !ok {
				t.Fatal("precondition: handoffRule with nothing changed = false, want true")
			}
			changed := false
			k.during = func() {
				if changed {
					return
				}
				changed = true
				done := make(chan struct{})
				go func() { // the change takes App.mu: a rule that reads under it would never let it in
					c.change(h)
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("the change did not get App.mu while the kind read")
				}
			}
			_, _, ok := handoffRule(context.Background(), h.a, p, fx, h.now)
			if !changed {
				t.Fatal("precondition: the rule did not read the size, so nothing changed during it")
			}
			if ok {
				t.Errorf("handoffRule = true after a %s during the read, want false: not the candidate read", c.name)
			}
		})
	}
}

// A keep lane dispatch's work job goes to the lane's kept agent with the session's size read outside App.mu: T-2,
// dispatched to L-01 after T-1 merged there, its step selected (nextDispatch under the lock) and run. While its read
// blocks, obor status completes; then a session under handoff_tokens gets T-2's work job, one past it waits for the
// handoff rule (nothing sent), and one whose agent changed session during the read waits for the next pass's read.
func TestKeptWorkReadsTheSizeOutsideTheLock(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		tokens   int
		change   bool
		wantSent bool
	}{
		{"under the threshold", 10, false, true},
		{"past the threshold", 5000, false, false},
		{"session changed", 10, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := seedSched(t, false)
			h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},`+
				`"lanes":{"list":[{"name":"L-01","keep_agent":true}]},"agents":{"handoff_tokens":1000,`+harnessModels+`}}`)
			cfg := loadCfg(t, h.root)
			p := h.proj()
			p.dispatching = true
			disp := func(task string) dispatchData {
				return dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01", Type: "sonnet", Target: "target",
					Branch: "task/" + task, Keep: true}
			}
			addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, disp("T-1"))
			startReported(t, h, cfg.Rev())
			h.session("pj-l-01", "new-session", "/t/new-session.jsonl", 100)
			addRecord(t, h, "T-1", event.StepMerged, event.KindFact, event.MergedData{Head: "m1"})
			addRecord(t, h, "T-2", event.StepDispatched, event.KindFact, disp("T-2"))
			addRecord(t, h, "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})

			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			h.a.opt.Tokens = func(string) (int, bool) {
				once.Do(func() { close(entered) })
				<-release
				return c.tokens, true
			}
			h.a.mu.Lock()
			mt := *p.folded.loop["T-2"]
			kept := h.a.kept(p, mt, cfg)
			h.a.mu.Unlock()
			if !kept || mt.started || mt.work != "" {
				t.Fatalf("precondition: T-2's work goes to its kept agent (kept %v, started %v, work %q)",
					kept, mt.started, mt.work)
			}
			fx, err := h.a.opt.Loop(h.root)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan bool, 1)
			go func() { // the pass's selection under the lock, then its step
				h.a.mu.Lock()
				s := h.a.nextDispatch(p, mt, cfg)
				h.a.mu.Unlock()
				done <- s != nil && s(context.Background(), fx)
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("precondition: T-2's step never read the kept agent's size")
			}
			status := make(chan struct{})
			go func() {
				h.a.Status()
				close(status)
			}()
			select {
			case <-status:
			case <-time.After(5 * time.Second):
				t.Error("obor status blocked while the size read blocked: the step reads under App.mu")
			}
			if c.change {
				changed := make(chan struct{})
				go func() { // the change takes App.mu: a step that reads under it would never let it in
					h.session("pj-l-01", "by-hand", "/t/by-hand.jsonl", 200)
					close(changed)
				}()
				select {
				case <-changed:
				case <-time.After(5 * time.Second):
					t.Error("the session change did not get App.mu while the size read blocked")
				}
			}
			close(release)
			<-status
			ran := <-done
			h.a.mu.Lock()
			work := p.folded.loop["T-2"].work
			h.a.mu.Unlock()
			if ran != c.wantSent || (work != "") != c.wantSent {
				t.Errorf("T-2's step = %v, its work job %q; want sent %v", ran, work, c.wantSent)
			}
		})
	}
}
