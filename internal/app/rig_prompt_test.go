package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The project's lines survive a restart (orderly and SIGKILL) and an off-target checkout.

// refusePrompt is a FakeAgents.OnPrompt that refuses every prompt before typing.
func refusePrompt(context.Context, string, string, bool) (bool, error) {
	return false, errors.New("agent_not_ready: no typing")
}

// hangPrompt is a FakeAgents.OnPrompt whose typing begins, runs hang and never returns (the process is killed there).
func hangPrompt(hang func()) func(context.Context, string, string, bool) (bool, error) {
	return func(context.Context, string, string, bool) (bool, error) {
		hang()
		park()
		return true, nil
	}
}

// linerLoop is a loop whose lines go to l, typed and posted.
func linerLoop(l *FakeAgents) *Loop { return &Loop{Prompter: l, Inbox: l} }

// lineRoot is a merging project with a reviewer role.
func lineRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":2,"project":{"name":"proj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":""}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// lineApp loads root's log and connects to a snapshot with the lead and the reviewer pj-rev idle.
func lineApp(t *testing.T, root string, now *time.Time) (*App, *proj) {
	t.Helper()
	a := newTestApp(Options{Now: func() time.Time { return *now }, Loop: func(root string) (*Loop, error) { return withCfg(root, &Loop{}), nil }},
		&fakePanes{}, &fakeLanes{})
	p, err := a.load(root)
	if err != nil {
		t.Fatal(err)
	}
	a.p = p
	t.Cleanup(func() { p.log.Close(); a.closeLog() })
	a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{
		{ID: "w:lead", Group: "proj", Cwd: root, Agent: "claude", Label: p.out.Lead(), Status: "idle"},
		{ID: "w:rev", Group: "proj", Cwd: root, Agent: "codex", Label: "pj-rev", Status: "idle"},
	}}, false)
	return a, p
}

// lineReload closes a's log and loads it again (an orderly restart).
func lineReload(t *testing.T, a *App, p *proj, now *time.Time) (*App, *proj) {
	t.Helper()
	p.log.Close()
	a.closeLog()
	return lineApp(t, p.root, now)
}

// lineStatus sets pane id's status as herdr reports it (an event at a's clock: observed, reconciled, its work noted).
func lineStatus(a *App, id, status string, _ time.Time) {
	pane := a.obs[id].pane
	pane.Status = status
	eventNow(a, port.Event{Kind: port.EventStatus, Pane: id, Status: status, Agent: pane.Agent, New: pane})
}

// queueLine queues one line of kind (nothing typed yet).
func queueLine(t *testing.T, a *App, p *proj, kind string, now time.Time) {
	t.Helper()
	var err error
	switch kind {
	case "send":
		_, err = a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "send1", Role: "lead"},
			From: "other", Text: "hello"})
	case "brief":
		_, err = a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev", Text: "verify finding 1"})
	case "finished":
		_, err = a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev"}) // a fresh start's brief
		lineStatus(a, "w:rev", "working", now)
		lineStatus(a, "w:rev", "idle", now)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// briefCount is the number of p's open briefs, by its outbox's allBriefs.
func (p *proj) briefCount() int {
	n := 0
	for range p.out.Briefs() {
		n++
	}
	return n
}

// park blocks the calling goroutine with a pending timer, so the runtime's deadlock detector leaves the process for the
// parent's SIGKILL. A bare select{} let a loaded child exit 2 ("all goroutines are asleep") before the kill landed, red
// in a gate run.
func park() {
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	<-t.C
}

// inboxSpy records each line posted to an inbox.
type inboxSpy struct{ calls []string }

func (s *inboxSpy) Post(_ context.Context, _, agent, line string) (bool, error) {
	s.calls = append(s.calls, agent+": "+line)
	return true, nil
}

// replayed is h's project loaded again from its log (an orderly restart).
func replayed(t *testing.T, h *harness) *proj {
	t.Helper()
	h.a.closeLog()
	a := newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	p, err := a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.log.Close() })
	return p
}

var logNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("x", 3600))

// logRoot is a project root with an empty state dir: no database yet.
func logRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// openLog opens root's database as serve does and replays it; closed when the test ends.
func openLog(t *testing.T, root string) (*event.Log, []event.Record) {
	t.Helper()
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l, recs, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	return l, recs
}

func fact(t *testing.T, task, step, reason string) event.Record {
	t.Helper()
	r := event.Record{Task: task, Step: step, Kind: event.KindFact}
	if reason != "" {
		b, err := json.Marshal(event.FactData{Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		r.Data = b
	}
	return r
}

// The choices of one merge-loop task over p's loop map never follow Go's map order: a lane's task is its newest
// dispatch's, the other choices are by task name. Each test has two tasks the choice could take and asks it many
// times: map order would pick the other one in about half of them.

const orderTries = 64

// twoInLane is a project whose loop holds T-2 and T-1, both dispatched to lane L1 by agent w1, their work jobs out;
// T-2's dispatch is the newer (seq 7 after T-1's 3): name order and dispatch order disagree.
func twoInLane() *proj {
	mk := func(task string, seq int64) *mtask {
		return &mtask{task: task, lane: "L1", agent: "w1", attempt: 1, state: stDispatched, attemptVars: attemptVars{work: event.StepWork}, dispSeq: seq}
	}
	return &proj{merging: true, log: &event.Log{}, ended: map[string]bool{}, held: map[string]string{},
		folded: folded{loop: map[string]*mtask{"T-2": mk("T-2", 7), "T-1": mk("T-1", 3)}, calls: map[string]string{}}}
}

// A needs-you is a block on the task's phase. These golden folds were captured with the
// code before the change (the three encodings: fail, needs-you with left, merged/rejected with needs): each case is a
// log in the event log's format, replayed, and what obor status, the needs-you list, the task verbs, the next step
// and obor resume make of it (each action folded in turn) must stay the same. OBOR_UPDATE_GOLDEN=1 rewrites the file.

const blockTask = "T-1"

// docs/state-model.md "Prompts" (the wake contract) and row 4's agent.start replay. The first
// action after the setup is the case's entry.

// scripted is a FakeAgents.OnPrompt whose calls take the next of outcomes, then type.
func scripted(outcomes ...promptOutcome) func(context.Context, string, string, bool) (bool, error) {
	return func(context.Context, string, string, bool) (bool, error) {
		o := promptOutcome{typed: true}
		if len(outcomes) > 0 {
			o, outcomes = outcomes[0], outcomes[1:]
		}
		return o.typed, o.err
	}
}

type promptOutcome struct {
	typed bool
	err   error
}

// boxOf is what f's prompts left in pane's input box, which held before: its agents never take a prompt by themselves,
// so what is typed stays there and a second typing on top of an unsent one shows.
func boxOf(f *FakeAgents, pane, before string) string {
	box := before
	for _, p := range f.Prompts() {
		if p.Pane != pane {
			continue
		}
		if p.Clear {
			box = ""
		}
		if p.Began {
			box += "[obor job " + jobOf(p.Text) + "]"
		}
	}
	return box
}

// jobOf is the job id in a typed wake's [obor job <id>] tag ("": a line, no job tag).
func jobOf(text string) string {
	rest, ok := strings.CutPrefix(text, "[obor job ")
	if !ok {
		return ""
	}
	job, _, _ := strings.Cut(rest, "]")
	return job
}

// startIn is a FakeAgents.OnLaunch that puts a fresh session (sid, "": none known) into the pane it starts in (its
// input's).
func startIn(h *harness, sid string) func(context.Context, port.Launch) (port.Started, error) {
	return func(_ context.Context, in port.Launch) (port.Started, error) {
		o := h.a.obs[in.Pane]
		if o == nil {
			h.t.Fatalf("launch in pane %s, which the snapshot does not have", in.Pane)
		}
		o.pane.Agent, o.pane.Session, o.pane.Transcript = "claude", sid, ""
		return port.Started{Line: "STARTED " + in.Agent}, nil
	}
}

// lastPane is the pane of f's last launch ("": none).
func lastPane(f *FakeAgents) string {
	l := f.Launches()
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1].Pane
}

// promptRig is T-1 dispatched onto L-01 (lane prepared, rows removed; started: its worker started too), the switch
// on, with a FakeAgents as its Prompter (boxOf) and one as its Agents (startIn, session new-session). before, when
// given, acts on the harness's snapshot before the connect takes it.
func promptRig(t *testing.T, started bool, before ...func(h *harness)) (*harness, *Loop, *FakeAgents, *FakeAgents) {
	h, s := seedSched(t, true, before...)
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, event.Dispatch{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Target: "target", Branch: "task/T-1"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, event.PrepareOut{Base: "target"})
	if started {
		addRecord(t, h, "T-1", event.StepStartWork, event.KindResult, event.StartOut{Line: "STARTED", Pane: "w1:p1"})
	}
	h.a.p.dispatching = true
	kit, _ := filepath.Abs("../..")
	f, ag := &FakeAgents{}, &FakeAgents{OnLaunch: startIn(h, "new-session")}
	return h, withAppCfg(h.a, &Loop{Workflow: WorkflowOf(s), Agents: ag, PlaceLane: h.placeLane, Prompter: f, Kit: kit}), f, ag
}
