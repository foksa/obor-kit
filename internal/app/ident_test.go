package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"obor/internal/store"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// T-504: the identity table (registry.go), its case table, on the watchdogs'
// harness: lane O-01's worker pj-o-01 in w1:p1 (terminal t1, session s1, labelled by its launch), a shell w1:p2 beside
// it. Each test asserts whereIs and the consumer's observable call (the fake Agents' Stop pane, the Prompter/Poster/
// Liner target, the queued line, the fact written). Rows 12–20 are the transitions RV-53 asks for.

// newIdentHarness is the harness with w1:p1 labelled pj-o-01 on terminal t1 (session s1), the lane row and the config
// naming pj-o-01 (lane O-01's worker), and a shell w1:p2 (terminal t2) in the project's workspace; its log lines in
// out.
func newIdentHarness(t *testing.T) (*harness, *bytes.Buffer) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","lanes":{"O-01":{"agent":"pj-o-01"}}}`)
	h.lanes.lanes[0].Agent = "pj-o-01"
	p := h.pane("w1:p1")
	p.Label, p.Terminal, p.Session = "pj-o-01", "t1", "s1"
	h.snap.Panes = append(h.snap.Panes, Pane{ID: "w1:p2", Workspace: "w1", Cwd: h.root, Terminal: "t2"})
	out := &bytes.Buffer{}
	h.a.opt.Out = out
	return h, out
}

// withLead gives pj a lead role (pj-lead), an integrator (pj-int) and the configured agent pj-other (agents.pj-other,
// no lane or role): every name a start row or a recorded session may prove a pane with (T-605), lane O-01's worker
// pj-o-01 included.
func (h *harness) withLead() {
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj",`+
		`"lanes":{"O-01":{"agent":"pj-o-01"}},"roles":{"integrator":{},"lead":{}},`+
		`"agents":{"pj-other":{"type":"claude"}}}`)
}

// sids makes agent → session each agent's current recorded session in the store (once the project is loaded): put in
// p.sessions directly, so two agents may hold one id (the tie cases; sessionCall refuses that).
func (h *harness) sids(m map[string]string) {
	h.sidMap = m
	h.applySids()
}

func (h *harness) applySids() {
	if h.a.p == nil {
		return
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	for agent, id := range h.sidMap {
		if cur, ok := store.Current(h.a.p.folded.sessions, agent); ok && cur.ID == id {
			continue
		}
		h.sidSeq++
		h.a.p.folded.sessions = append(h.a.p.folded.sessions, store.Session{Agent: agent, Kind: "claude", ID: id, Started: h.sidSeq})
	}
}

// started writes serve's start of agent in pane (terminal term, session sid; "": none) to the log: agent.start's
// intent and result.
func (h *harness) started(agent, pane, term, sid string) {
	in, _ := json.Marshal(startData{Lane: "O-01", Agent: agent, Pane: pane})
	res, _ := json.Marshal(startOut{Line: "STARTED " + agent, Agent: agent, Pane: pane, Terminal: term, Kind: "claude",
		Session: sid})
	h.log(Record{Task: "T-1", Attempt: 1, Step: StepStartWork, Kind: KindIntent, Class: ClassStart, Data: in},
		Record{Task: "T-1", Attempt: 1, Step: StepStartWork, Kind: KindResult, Data: res})
}

// where is whereIs(agent) in pj: "identified <pane> <source>", "ambiguous <panes>", "unavailable <why>".
func (h *harness) where(agent string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	w := h.a.whereIs(h.proj(), agent)
	switch w.State {
	case Identified:
		return "identified " + w.Pane.ID + " " + w.Source
	case Ambiguous:
		return "ambiguous " + strings.Join(w.Panes, " ")
	}
	return "unavailable " + w.Why
}

// who is whoIs of pane id in pj ("": nobody).
func (h *harness) who(id string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	o := h.a.obs[id]
	if o == nil {
		return ""
	}
	agent, _ := h.proj().reg.whoIs(o.pane)
	return agent
}

// table is the identity table as its lookups give it: whoIs of each pane of h.snap (by id) and whereIs of each agent.
func (h *harness) table(agents ...string) string {
	ids := make([]string, 0, len(h.snap.Panes))
	for _, pane := range h.snap.Panes {
		ids = append(ids, pane.ID)
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s=%s; ", id, h.who(id))
	}
	for _, agent := range agents {
		fmt.Fprintf(&b, "%s: %s; ", agent, h.where(agent))
	}
	return b.String()
}

// move replaces pane id in h.snap by pane to (its terminal kept, as herdr's pane.move) and connects again (herdr's
// pane.move is a refresh: a new snapshot).
func (h *harness) move(id, to string) {
	h.pane(id).ID = to
	h.connect()
}

// add puts pane into h.snap and sends its created event.
func (h *harness) add(pane Pane) {
	h.snap.Panes = append(h.snap.Panes, pane)
	h.a.event(context.Background(), Event{Kind: EventCreated, Pane: pane.ID, New: pane})
}

// twin is a second pane labelled pj-o-01 (w1:p3, terminal t3), no session.
func (h *harness) twin() Pane {
	return Pane{ID: "w1:p3", Workspace: "w1", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-o-01",
		Terminal: "t3"}
}

func eqs(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// idAgents is an Agents recording each Stop's agent and pane.
type idAgents struct{ stops []string }

func (f *idAgents) Launch(context.Context, Launch) (Started, error) {
	return Started{Line: "STARTED"}, nil
}
func (f *idAgents) Stop(_ context.Context, agent, pane string, _ bool) (string, error) {
	f.stops = append(f.stops, agent+" "+pane)
	return "STOPPED " + agent + " (pane " + pane + ")", nil
}

// stop runs the after-merge stop of pj-o-01 (stopWorker) while lane O-01 holds T-1.
func (h *harness) stop(f *idAgents) (string, error) {
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1", Agent: "pj-o-01"}
	h.a.mu.Unlock()
	return h.a.stopWorker(context.Background(), p, &Loop{Agents: f}, mtask{task: "T-1", attempt: 1, lane: "O-01",
		agent: "pj-o-01"})
}

// wake sends pj-o-01's work job T-1/1/work (send) through fx; true: sent.
func (h *harness) wake(fx *Loop) bool {
	return h.a.send(context.Background(), h.proj(), fx, mtask{task: "T-1", attempt: 1, lane: "O-01", agent: "pj-o-01"},
		Key{"T-1", 1, StepWork}, "T-1/1/work", promptData{Agent: "pj-o-01"}, wakeText, "work job")
}

// brief records a brief to agent (line "": its start carried it).
func (h *harness) brief(agent, line string) {
	if !h.a.record(h.proj(), Record{Task: MsgTask, Step: StepBriefed, Kind: KindFact},
		Message{To: agent, From: "pj-lead", Line: line}) {
		h.t.Fatal("brief not recorded")
	}
}

// worked: pj's open brief of agent saw its work.
func (h *harness) worked(agent string) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	b := h.proj().out.brief(agent)
	return b != nil && b.worked
}

// lines is the lines of pj not delivered yet, as "<to>: <line>".
func (h *harness) lines() []string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	var out []string
	for _, q := range h.proj().queuedLines() {
		out = append(out, q.msg.To+": "+q.msg.Line)
	}
	return out
}

// 1: serve's agent.start {w1:p1, t1, s1}: start before the pane shows s1, session once it does; the job wake is
// posted to its session.
func TestIdentStartByServe(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 start", "before the session shows")
	h.status("w1:p1", func(p *Pane) { p.Session = "s1" })
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "once the pane shows s1")

	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	in, typed := &inboxSpy{}, &boxPrompter{box: map[string]string{}}
	if !h.wake(&Loop{Inbox: in, Typer: typed}) {
		t.Fatal("wake not sent")
	}
	if len(in.calls) != 1 || !strings.HasPrefix(in.calls[0], "pj-o-01: [obor job T-1/1/work]") || len(typed.jobs) != 0 {
		t.Errorf("posted %q, typed %q; want one post to pj-o-01's session, nothing typed", in.calls, typed.jobs)
	}
}

// 2: the lead's obor restart pj-o-01 (no serve start): its recorded session s2 is the pane's session: identified by session; a
// brief to it (a typed kind) is typed into w1:p1.
func TestIdentRestartByLead(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.sids(map[string]string{"pj-o-01": "s2"})
	p := h.pane("w1:p1")
	p.Session, p.Agent = "s2", "codex"
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "where")
	h.brief("pj-o-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if !slices.Equal(l.calls, []string{"w1:p1: [obor pj-lead] verify"}) {
		t.Errorf("typed %q, want the brief into w1:p1", l.calls)
	}
}

// 3: --fresh: session recorded s3 and the pane shows s3: identified by the new session; work after the brief is marked.
func TestIdentFreshSession(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label = ""
	h.sids(map[string]string{"pj-o-01": "s1"})
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "before the fresh start")
	h.sids(map[string]string{"pj-o-01": "s3"})
	h.status("w1:p1", func(p *Pane) { p.Session = "s3" })
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "after the fresh start")
	h.brief("pj-o-01", "")
	if h.worked("pj-o-01") {
		t.Fatal("precondition: an idle pane is no work")
	}
	h.status("w1:p1", func(p *Pane) { p.Status = "working" })
	if !h.worked("pj-o-01") {
		t.Error("work on the new session not marked for the brief")
	}
}

// 4: herdr restarted, obor plugin restore: new pane ids and terminals, the same label, the session record kept: identified by
// session (by label for opencode, which records none); the lane sees no agent exit.
func TestIdentHerdrRestartRestore(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"claude", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			if kind == "claude" {
				h.sids(map[string]string{"pj-o-01": "s1"})
			}
			h.connect()
			seq := h.lastSeq()
			sess, src := "s1", "session"
			if kind == "opencode" {
				sess, src = "", "label"
			}
			h.snap = Snapshot{Workspaces: map[string]string{"w9": "pj"}, Panes: []Pane{{ID: "w9:p1", Workspace: "w9",
				Cwd: h.lane("O-01"), Agent: kind, Status: "idle", Label: "pj-o-01", Terminal: "t9", Session: sess}}}
			h.connect()
			eqs(t, h.where("pj-o-01"), "identified w9:p1 "+src, "where")
			h.tick(2 * h.a.opt.Gone)
			for _, f := range h.facts(seq) {
				if strings.Contains(f, "exited") {
					t.Errorf("fact after the restore: %s", f)
				}
			}
		})
	}
}

// 5: serve restarts with the panes alive: the start row {w1:p1, t1} replays; identified by start on the first pass,
// and the after-merge stop addresses w1:p1.
func TestIdentServeRestart(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label, h.pane("w1:p1").Session = "", "s7"
	h.started("pj-o-01", "w1:p1", "t1", "")
	h.connect()
	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 start", "first pass")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-o-01 w1:p1"}) {
		t.Errorf("stops %q (err %v), want pj-o-01 at w1:p1", f.stops, err)
	}
}

// 6: the pane closed and another opened under the same label, no session record: identified by label; obor status
// names it.
func TestIdentPaneReopened(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-o-01", "w1:p1", "t1", "")
	h.connect()
	h.remove("w1:p1", EventClosed)
	eqs(t, h.where("pj-o-01"), "unavailable exited", "after the close")
	h.add(Pane{ID: "w1:p5", Workspace: "w1", Cwd: h.lane("O-01"), Agent: "claude", Status: "idle", Label: "pj-o-01",
		Terminal: "t5", Session: "s5"})
	eqs(t, h.where("pj-o-01"), "identified w1:p5 label", "the new pane")
	st := h.a.Status()
	if st.Project == nil || len(st.Project.Agents) != 1 || st.Project.Agents[0].Name != "pj-o-01" ||
		st.Project.Agents[0].Pane != "w1:p5" {
		t.Errorf("status agents %+v, want pj-o-01 in w1:p5", st.Project)
	}
}

// 7: started by hand (claude in a labelled shell), no record: identified by label; a brief is delivered to it.
func TestIdentStartedByHand(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "where")
	h.brief("pj-o-01", "[obor pj-lead] review this")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if !slices.Equal(l.calls, []string{"w1:p1: [obor pj-lead] review this"}) || len(h.lines()) != 0 {
		t.Errorf("typed %q, queued %q; want the brief delivered into w1:p1", l.calls, h.lines())
	}
}

// 8: herdr drops the agent's name mid-task (app.Pane has no name field: nothing can read it): unchanged, and a typed
// line still addresses the pane id (RV-53 #5).
func TestIdentNameDropped(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent = "codex"
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	before := h.where("pj-o-01")
	h.a.event(context.Background(), Event{Kind: EventStatus, Pane: "w1:p1", Status: "idle", Agent: "codex"}) // no metadata
	eqs(t, h.where("pj-o-01"), before, "after herdr dropped the name")
	h.brief("pj-o-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if !slices.Equal(l.calls, []string{"w1:p1: [obor pj-lead] verify"}) {
		t.Errorf("typed %q, want the line addressed to w1:p1", l.calls)
	}
}

// 9: two panes labelled pj-o-01, no proof on either: Ambiguous, both named; the wake is not sent and nothing is
// recorded; obor status says so.
func TestIdentTwoLabels(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.snap.Panes = append(h.snap.Panes, h.twin())
	h.connect()
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "where")
	f := &boxPrompter{box: map[string]string{}}
	if h.wake(&Loop{Typer: f}) || len(f.jobs) != 0 || h.count(KindIntent, StepWork) != 0 {
		t.Errorf("typed %q, %d intents; want nothing", f.jobs, h.count(KindIntent, StepWork))
	}
	st := h.a.Status()
	for _, as := range st.Project.Agents {
		if as.Name != "pj-o-01 (not identified)" {
			t.Errorf("status names %s %q, want pj-o-01 (not identified)", as.Pane, as.Name)
		}
	}
}

// 10: the pane moved to another tab (same terminal, new id): identified at the new id by the same proof; the stop
// addresses it.
func TestIdentPaneMoved(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.started("pj-o-01", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 start", "before the move")
	h.move("w1:p1", "w1:p7")
	eqs(t, h.where("pj-o-01"), "identified w1:p7 start", "after the move")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-o-01 w1:p7"}) {
		t.Errorf("stops %q (err %v), want pj-o-01 at w1:p7", f.stops, err)
	}
}

// 11: the pane at its shell (the agent exited, the terminal there): Unavailable "at its shell"; the stop is
// "stopped: no agent", nothing signalled.
func TestIdentAtShell(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	h.status("w1:p1", func(p *Pane) { p.Agent, p.Status, p.Session = "", "", "" })
	eqs(t, h.where("pj-o-01"), "unavailable at its shell", "where")
	f := &idAgents{}
	line, err := h.stop(f)
	if err != nil || line != "stopped: no agent (at its shell)" || len(f.stops) != 0 {
		t.Errorf("stop %q (err %v), stops %q; want stopped: no agent, nothing stopped", line, err, f.stops)
	}
}

// 12 (RV-53 #1): move → stop through the after-merge step: the start row says w1:p1, the pane moved to w1:p7; Stop
// is called with w1:p7, never w1:p1, and the stop's intent records no pane.
func TestIdentMoveThenStop(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	h.move("w1:p1", "w1:p7")
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1", Agent: "pj-o-01"}
	h.a.mu.Unlock()
	f := &idAgents{}
	mt := mtask{task: "T-1", attempt: 1, lane: "O-01", agent: "pj-o-01", after: slices.Index(afterSteps, StepStop)}
	if !h.a.afterStep(context.Background(), p, &Loop{Agents: f}, mt) {
		t.Fatal("the stop step failed")
	}
	eqs(t, strings.Join(f.stops, ","), "pj-o-01 w1:p7", "Stop's agent and pane")
	for _, r := range h.records() {
		if r.Step == StepStop && strings.Contains(string(r.Data), "w1:p1") {
			t.Errorf("the stop's %s names the old pane: %s", r.Kind, r.Data)
		}
	}
}

// 13: as 12, the old address reused by an unrelated agent in the lane, the moved pane relabelled notes: the start
// proof still holds w1:p7; nothing goes to w1:p1.
func TestIdentMoveAddressReused(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	h.pane("w1:p1").Label = "notes"
	h.move("w1:p1", "w1:p7")
	h.add(Pane{ID: "w1:p1", Workspace: "w1", Cwd: h.lane("O-01"), Agent: "claude", Status: "idle", Terminal: "t8",
		Session: "s8"})
	eqs(t, h.where("pj-o-01"), "identified w1:p7 session", "where")
	eqs(t, h.who("w1:p1"), "", "the unrelated agent")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-o-01 w1:p7"}) {
		t.Errorf("stops %q (err %v), want pj-o-01 at w1:p7 only", f.stops, err)
	}
}

// 14 (RV-53 #2): a session claim and a label claim on one terminal: the lead's recorded session s1 is w1:p1's session, w1:p1 is
// labelled pj-other: the lead owns t1, pj-other has no proof; whoIs(w1:p1) is the lead.
func TestIdentSessionBeatsLabel(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = "pj-other"
	h.sids(map[string]string{"pj-lead": "s1"})
	h.connect()
	eqs(t, h.where("pj-lead"), "identified w1:p1 session", "the lead")
	eqs(t, h.where("pj-other"), "unavailable no proof", "the label")
	eqs(t, h.who("w1:p1"), "pj-lead", "whoIs(w1:p1)")
	var owned []string
	for _, pane := range h.snap.Panes {
		if h.who(pane.ID) != "" {
			owned = append(owned, pane.ID)
		}
	}
	if len(owned) != 1 {
		t.Errorf("owned panes %q, want one (t1)", owned)
	}
}

// 15 (RV-53 #2): row 6, then a second pane labelled pj-o-01 appears: Ambiguous (the old pane not kept), logged
// once; deliveries are held.
func TestIdentDuplicateLater(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "before")
	tw := h.twin()
	tw.Agent = "codex"
	h.add(tw)
	h.tick(0)
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "after the second label")
	want := "agent pj-o-01 not identified: panes w1:p1 and w1:p3 run pj-o-01 (close or relabel one)"
	if n := strings.Count(out.String(), want); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, out)
	}
	h.brief("pj-o-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if len(l.calls) != 0 || len(h.lines()) != 1 {
		t.Errorf("typed %q, queued %q; want the line held", l.calls, h.lines())
	}
}

// 16: row 9, then w1:p1 shows recorded session s1: identified at w1:p1 by session; w1:p3 logged once as a stray label.
func TestIdentOneLabelProven(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.snap.Panes = append(h.snap.Panes, h.twin())
	h.sids(map[string]string{"pj-o-01": "s1"})
	h.connect()
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "before")
	h.status("w1:p1", func(p *Pane) { p.Session = "s1" })
	h.tick(0)
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "after the session shows")
	want := "pane w1:p3 labelled pj-o-01 too, not its session"
	if n := strings.Count(out.String(), want); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, out)
	}
}

// 17: a start row {w1:p1, t1} whose pane now runs the integrator's session: pj-o-01 has none (the row retired,
// logged), pj-int owns t1; the stop of pj-o-01 signals nothing.
func TestIdentStartRowRetired(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.withLead()
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	p := h.pane("w1:p1")
	p.Label, p.Session = "pj-int", "s9"
	h.sids(map[string]string{"pj-int": "s9"})
	h.connect()
	eqs(t, h.where("pj-int"), "identified w1:p1 session", "pj-int")
	eqs(t, h.where("pj-o-01"), "unavailable no proof", "pj-o-01")
	if !strings.Contains(out.String(), "pj-o-01's start row retired: pane w1:p1 runs pj-int now") {
		t.Errorf("no retire line:\n%s", out)
	}
	f := &idAgents{}
	line, err := h.stop(f)
	if err != nil || !strings.HasPrefix(line, "stopped: no agent") || len(f.stops) != 0 {
		t.Errorf("stop %q (err %v), stops %q; want stopped: no agent, nothing signalled", line, err, f.stops)
	}
}

// T-605: an agent the config does not name owns no pane (the lane H-01 case: its worker before the rename, hk-h1,
// left a start row on the pane D-02 took over).

// T-605 #1: a stale start row of hk-h1 (the config names no such agent) on w1:p1, labelled pj-o-01: the label wins,
// and the stale row is retired in memory, logged once.
func TestIdentUnnamedStartRetired(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.started("hk-h1", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.who("w1:p1"), "pj-o-01", "whoIs(w1:p1)")
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "pj-o-01")
	eqs(t, h.where("hk-h1"), "unavailable no proof", "hk-h1")
	h.a.mu.Lock()
	_, kept := h.proj().reg.start("hk-h1")
	h.a.mu.Unlock()
	if kept {
		t.Error("hk-h1's start row kept after the pass")
	}
	h.connect()
	const line = "hk-h1's start row retired: the config names no such agent"
	if n := strings.Count(out.String(), line); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, out)
	}
}

// T-605 #2: nothing else in the ranking changes: a configured agent's start row still beats a label (pj-o-01's start
// on w1:p1, labelled pj-o-02; both are lane agents of the config).
func TestIdentNamedStartBeatsLabel(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"name":"pj","lanes":{"O-01":{"agent":"pj-o-01"},"O-02":{"agent":"pj-o-02"}}}`)
	h.pane("w1:p1").Label = "pj-o-02"
	h.started("pj-o-01", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.who("w1:p1"), "pj-o-01", "whoIs(w1:p1)")
	eqs(t, h.where("pj-o-01"), "identified w1:p1 start", "pj-o-01")
	eqs(t, h.where("pj-o-02"), "unavailable no proof", "pj-o-02")
}

// T-605 #3: an unconfigured agent's recorded session gives no session proof either: hk-sol's record is w1:p1's session
// s1, and it has a brief (a candidate), yet no pane proves it.
func TestIdentUnnamedSessionNoProof(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label = ""
	h.connect()
	h.sids(map[string]string{"hk-sol": "s1"})
	h.brief("hk-sol", "")
	h.connect()
	eqs(t, h.where("hk-sol"), "unavailable no proof", "hk-sol")
	eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1)")
}

// 18 (RV-53 #4): replay → brief work: a brief recorded, serve restarts, the first snapshot shows w1:p1 working with
// no label, its recorded session s1: identified by session and the brief's work marked (and logged) on that first snapshot.
func TestIdentReplayBriefWork(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label = ""
	sids := map[string]string{"pj-o-01": "s1"}
	h.sids(sids)
	h.connect()
	h.brief("pj-o-01", "")
	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	h.sids(sids)
	h.pane("w1:p1").Status = "working"
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "where")
	if !h.worked("pj-o-01") || h.count(KindFact, StepWorked) != 1 {
		t.Errorf("worked %v, %d worked facts; want the work marked on the first snapshot", h.worked("pj-o-01"),
			h.count(KindFact, StepWorked))
	}
}

// 19 (RV-53 #3): a wake pending, two labels appear before the send: Ambiguous; the key stays pending, nothing typed
// or recorded; sent once the ambiguity ends.
func TestIdentAmbiguityDuringWake(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.connect()
	h.add(h.twin())
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "where")
	f := &boxPrompter{box: map[string]string{}}
	fx := &Loop{Typer: f}
	if h.wake(fx) || len(f.jobs) != 0 {
		t.Fatalf("typed %q while ambiguous", f.jobs)
	}
	if h.count(KindIntent, StepWork) != 0 || h.count(KindFact, StepFailed) != 0 {
		t.Errorf("%d intents, %d failed facts; want none", h.count(KindIntent, StepWork), h.count(KindFact, StepFailed))
	}
	h.remove("w1:p3", EventClosed)
	if !h.wake(fx) || !slices.Equal(f.jobs, []string{"T-1/1/work"}) || f.box["w1:p1"] == "" {
		t.Errorf("after the ambiguity: typed %q into %v, want the job once into w1:p1", f.jobs, f.box)
	}
}

// 20: row 9, then w1:p3 closed: identified at w1:p1 by label; the held brief is delivered on that pass.
func TestIdentAmbiguityEnds(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
	h.snap.Panes = append(h.snap.Panes, h.twin())
	h.connect()
	h.brief("pj-o-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if len(l.calls) != 0 {
		t.Fatalf("precondition: held while ambiguous, typed %q", l.calls)
	}
	h.remove("w1:p3", EventClosed)
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "where")
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if !slices.Equal(l.calls, []string{"w1:p1: [obor pj-lead] verify"}) || len(h.lines()) != 0 {
		t.Errorf("typed %q, queued %q; want the brief delivered into w1:p1", l.calls, h.lines())
	}
}

// 21: not connected (before the first snapshot): Unavailable "not connected"; nothing attempted, lines queued.
func TestIdentNotConnected(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent = "codex"
	h.a.loadProject()
	eqs(t, h.where("pj-o-01"), "unavailable not connected", "where")
	f := &boxPrompter{box: map[string]string{}}
	if h.wake(&Loop{Typer: f}) || len(f.jobs) != 0 || h.count(KindIntent, StepWork) != 0 {
		t.Errorf("wake typed %q before the first snapshot", f.jobs)
	}
	h.brief("pj-o-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if len(l.calls) != 0 || len(h.lines()) != 1 {
		t.Errorf("typed %q, queued %q; want the line queued", l.calls, h.lines())
	}
}

// 22: the lead with two panes (a second opened by hand): the lead is Ambiguous, and its line is still posted by its
// inbox record.
func TestIdentLeadTwoPanes(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.snap.Panes = append(h.snap.Panes,
		Pane{ID: "w1:p8", Workspace: "w1", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-lead", Terminal: "t8"},
		Pane{ID: "w1:p9", Workspace: "w1", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-lead", Terminal: "t9"})
	h.connect()
	eqs(t, h.where("pj-lead"), "ambiguous w1:p8 w1:p9", "the lead")
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	if !h.a.record(h.proj(), Record{Task: MsgTask, Step: StepMessage, Kind: KindFact},
		Message{To: "pj-lead", From: "obor", Line: "[obor] hello"}) {
		t.Fatal("line not recorded")
	}
	in := &inboxSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: &linerSpy{}})
	if !slices.Equal(in.calls, []string{"pj-lead: [obor] hello"}) {
		t.Errorf("posted %q, want the lead line posted", in.calls)
	}
}

// 23: identified outside the lane dir (the agent cd'd elsewhere): agentOf(lane) has no agent; the watchdog treats the
// lane as having none (no pane left in it: pane exited).
func TestIdentOutsideLane(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.connect()
	seq := h.lastSeq()
	h.status("w1:p1", func(p *Pane) { p.Cwd = filepath.Join(h.wt, "elsewhere") })
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "where")
	h.a.mu.Lock()
	w, _ := h.a.agentOf(h.proj(), h.lanes.lanes[0])
	h.a.mu.Unlock()
	if w.State == Identified {
		t.Errorf("agentOf(O-01) = %s, want none in the lane", w.Pane.ID)
	}
	h.tick(2 * h.a.opt.Gone)
	if got := strings.Join(h.facts(seq), "; "); !strings.Contains(got, "T-1 needs-you{pane exited}") {
		t.Errorf("facts %q, want the lane's agent gone", got)
	}
}

// 24: the same claims in any order make the same table: the snapshot's panes shuffled (and Go's map order) over many
// runs.
func TestIdentOrderIndependent(t *testing.T) {
	t.Parallel()
	var first string
	for i := range 40 {
		h, _ := newIdentHarness(t)
		h.withLead()
		h.pane("w1:p1").Label = "pj-other"
		h.sids(map[string]string{"pj-lead": "s1"})
		h.started("pj-o-01", "w1:p4", "t4", "")
		h.snap.Panes = append(h.snap.Panes,
			Pane{ID: "w1:p4", Workspace: "w1", Cwd: h.lane("O-01"), Agent: "claude", Status: "idle", Terminal: "t4"},
			Pane{ID: "w1:p5", Workspace: "w1", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-o-02", Terminal: "t5"},
			Pane{ID: "w1:p6", Workspace: "w1", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-o-02", Terminal: "t6"},
			Pane{ID: "w1:p7", Workspace: "w1", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-o-01", Terminal: "t7"})
		rand.New(rand.NewSource(int64(i))).Shuffle(len(h.snap.Panes), func(a, b int) {
			h.snap.Panes[a], h.snap.Panes[b] = h.snap.Panes[b], h.snap.Panes[a]
		})
		h.connect()
		got := h.table("pj-lead", "pj-int", "pj-o-01", "pj-o-02", "pj-other")
		if i == 0 {
			first = got
			eqs(t, h.where("pj-lead"), "identified w1:p1 session", "the lead")
			eqs(t, h.where("pj-o-01"), "identified w1:p4 start", "pj-o-01")
			eqs(t, h.where("pj-o-02"), "ambiguous w1:p5 w1:p6", "pj-o-02")
			eqs(t, h.where("pj-other"), "unavailable no proof", "pj-other")
			continue
		}
		if got != first {
			t.Fatalf("run %d: table\n%s\nwant\n%s", i, got, first)
		}
	}
}

// DD-v0.13 A2: the after-merge stop while serve is cut off from herdr (identified at w1:p1, then the stream ends) is
// not settled as "no agent": no Stop, no result, the step pending; connected again, it stops w1:p1.
func TestIdentStopWhileDisconnected(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "before")
	p := h.proj()
	h.a.record(p, Record{Task: "T-1", Attempt: 1, Step: StepSubmitted, Kind: KindFact},
		submitData{Lane: "O-01", Agent: "pj-o-01"})
	h.a.mu.Lock()
	p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1", Agent: "pj-o-01"}
	mt := p.folded.loop["T-1"]
	mt.state = stPublished
	mt.after = slices.Index(afterSteps, StepStop)
	h.a.connected = false
	h.a.mu.Unlock()
	f := &idAgents{}
	if h.a.afterStep(context.Background(), p, &Loop{Agents: f}, *mt) || p.log.Done(Key{"T-1", 1, StepStop}) ||
		len(f.stops) != 0 {
		t.Fatalf("disconnected: stops %q, result %v; want no stop, the step pending", f.stops,
			p.log.Done(Key{"T-1", 1, StepStop}))
	}
	h.connect()
	if line, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-o-01 w1:p1"}) {
		t.Errorf("connected again: stop %q (%v), stops %q; want w1:p1 stopped", line, err, f.stops)
	}
}

// kindSpy is a Poster recording each post's kind, agent and line.
type kindSpy struct{ calls []string }

func (s *kindSpy) Post(_ context.Context, kind, agent, line string) (bool, error) {
	s.calls = append(s.calls, kind+" "+agent+": "+line)
	return true, nil
}

// DD-v0.13 A3 (and B1's kind): a wake and a line are posted, with the pane's kind, only when pj-o-01's inbox record
// names the identified pane's session (s1) or none; a record of another session (s9): the wake is typed into w1:p1,
// the line held.
func TestIdentInboxOfAnotherSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ rec, want string }{{"s1", "posted"}, {"", "posted"}, {"s9", "typed"}} {
		t.Run("record "+tc.rec, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.started("pj-o-01", "w1:p1", "t1", "s1")
			h.connect()
			eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "where")
			h.a.opt.Inboxed = func(string) (string, bool) { return tc.rec, true }
			in, typed := &kindSpy{}, &boxPrompter{box: map[string]string{}}
			if !h.wake(&Loop{Inbox: in, Typer: typed}) {
				t.Fatal("wake not sent")
			}
			h.brief("pj-o-01", "[obor pj-lead] verify")
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: &linerSpy{}})
			if tc.want == "posted" {
				if len(in.calls) != 2 || !strings.HasPrefix(in.calls[0], "claude pj-o-01: [obor job T-1/1/work]") ||
					in.calls[1] != "claude pj-o-01: [obor pj-lead] verify" || len(typed.jobs) != 0 {
					t.Errorf("posted %q, typed %q; want the wake and the line posted with the pane's kind", in.calls,
						typed.jobs)
				}
				return
			}
			if len(in.calls) != 0 || !slices.Equal(typed.jobs, []string{"T-1/1/work"}) || len(h.lines()) != 1 {
				t.Errorf("posted %q, typed %q, queued %q; want the wake typed, the line held", in.calls, typed.jobs,
					h.lines())
			}
		})
	}
}

// DD-v0.13 R1: a line is held only on a known mismatch. The lead's pane identified by label with no session from
// herdr: its line is posted (even against another session's record: its record is its own latest SessionStart). A
// role pane with no session: posted. A role pane whose session (s1) differs from the record's (s9): held.
func TestIdentSessionUnknown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, agent, pane, rec string
		want                   string
	}{
		{"lead, no pane session", "pj-lead", "", "sess-lead", "pj-lead: [obor] hello"},
		{"lead, other session", "pj-lead", "s1", "s9", "pj-lead: [obor] hello"},
		{"role, no pane session", "pj-o-01", "", "s9", "claude pj-o-01: [obor] hello"},
		{"role, known mismatch", "pj-o-01", "s1", "s9", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.withLead()
			h.snap.Panes = append(h.snap.Panes, Pane{ID: "w1:p8", Workspace: "w1", Cwd: h.root, Agent: "claude",
				Status: "idle", Label: "pj-lead", Terminal: "t8", Session: tc.pane})
			h.pane("w1:p1").Session = tc.pane
			h.connect()
			pane := "w1:p1"
			if tc.agent == "pj-lead" {
				pane = "w1:p8"
			}
			w := h.where(tc.agent)
			if !strings.HasPrefix(w, "identified "+pane+" ") {
				t.Fatalf("precondition: %s is %q, want identified at %s", tc.agent, w, pane)
			}
			h.a.opt.Inboxed = func(string) (string, bool) { return tc.rec, true }
			if tc.agent == "pj-lead" {
				if !h.a.record(h.proj(), Record{Task: MsgTask, Step: StepMessage, Kind: KindFact},
					Message{To: tc.agent, From: "obor", Line: "[obor] hello"}) {
					t.Fatal("line not recorded")
				}
			} else {
				h.brief(tc.agent, "[obor] hello")
			}
			in := &kindSpy{}
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: &linerSpy{}})
			if tc.want == "" {
				if len(in.calls) != 0 || len(h.lines()) != 1 {
					t.Errorf("posted %q, queued %q; want the line held", in.calls, h.lines())
				}
				return
			}
			if len(in.calls) != 1 || !strings.HasSuffix(in.calls[0], tc.want[strings.Index(tc.want, " ")+1:]) ||
				len(h.lines()) != 0 {
				t.Errorf("posted %q, queued %q; want %q posted", in.calls, h.lines(), tc.want)
			}
		})
	}
}

// DD-v0.13 A5: a second agent pane in the lane (w1:p0, sorting first, a session the owner opened) is not the
// dispatch's place while the table identifies the lane's agent at w1:p1; with no agent identified, the rule as before.
func TestIdentPlacementPrefersTheLaneAgent(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.snap.Panes = append(h.snap.Panes, Pane{ID: "w1:p0", Workspace: "w1", Cwd: h.lane("O-01"), Agent: "claude",
		Status: "idle", Terminal: "t0"})
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "where")
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if o := h.a.placement(h.proj(), h.lane("O-01"), "pj-o-01"); o == nil || o.pane.ID != "w1:p1" {
		t.Errorf("placement for pj-o-01 = %v, want w1:p1", o)
	}
	if o := h.a.placement(h.proj(), h.lane("O-01"), "pj-o-02"); o == nil || o.pane.ID != "w1:p0" {
		t.Errorf("placement for an unidentified agent = %v, want the lowest-id agent pane w1:p0", o)
	}
}

// DD-v0.13 D1: the integrator's type names a kind obor has no bridge for: its start is refused (no Launch), naming
// obor config validate.
func TestIdentIntegratorWithoutBridge(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj",`+
		`"roles":{"integrator":{"type":"cur"},"lead":{}},"lane_types":{"cur":{"kind":"pi","args":[]}}}`)
	h.connect()
	if h.proj().integrator != "pj-int" {
		t.Fatalf("precondition: integrator %q", h.proj().integrator)
	}
	f := &launchSpy{}
	_, err := h.a.startIntegrator(context.Background(), f, h.root, Launch{Prompt: "x"})
	if err == nil || err.Error() != "integrator pj-int: its kind pi has no bridge (obor config validate)" || f.n != 0 {
		t.Errorf("start: %v, %d launches; want refused, no launch", err, f.n)
	}
}

// launchSpy is an Agents counting its launches.
type launchSpy struct {
	idAgents
	n int
}

func (f *launchSpy) Launch(context.Context, Launch) (Started, error) {
	f.n++
	return Started{Line: "STARTED"}, nil
}

// T-504: the identity table's cases; each failed on the code it was found on.
// #3: two panes labelled pj-o-01: the production send attempts no wake (no name fallback).
func TestIdentAmbiguityStillPrompts(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.snap.Panes = append(h.snap.Panes, h.twin())
	h.pane("w1:p1").Session = ""
	h.connect()
	if w := h.where("pj-o-01"); w != "ambiguous w1:p1 w1:p3" {
		t.Fatalf("precondition: ambiguous, got %s", w)
	}
	f := &boxPrompter{box: map[string]string{}}
	if h.wake(&Loop{Typer: f}) || len(f.jobs) != 0 {
		t.Errorf("wake attempted while ambiguous: %q", f.jobs)
	}
}

// #2: the lead's session and pj-other's label on one terminal: one owner, the lead.
func TestIdentOnePaneTwoAgents(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = "pj-other"
	h.sids(map[string]string{"pj-lead": "s1"})
	h.connect()
	eqs(t, h.where("pj-lead"), "identified w1:p1 session", "session identity")
	eqs(t, h.where("pj-other"), "unavailable no proof", "label identity")
}

// #2: a label identity, then a second pane labelled the same: refused, not the old pane kept.
func TestIdentDuplicateLabelArrivesLater(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.connect()
	h.snap.Panes = append(h.snap.Panes, h.twin())
	h.connect()
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "after the duplicate")
}

// #1: the start row says w1:p1, the pane moved to w1:p7: the stop addresses w1:p7.
func TestIdentStopMovedPane(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	h.move("w1:p1", "w1:p7")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-o-01 w1:p7"}) {
		t.Errorf("stops %q (err %v), want w1:p7", f.stops, err)
	}
}

// #4: an unlabelled pane identified by session works on its brief: worked.
func TestIdentBriefWorkByRegistry(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label = ""
	h.sids(map[string]string{"pj-o-01": "s1"})
	h.connect()
	h.brief("pj-o-01", "")
	h.status("w1:p1", func(p *Pane) { p.Status = "working" })
	h.status("w1:p1", func(p *Pane) { p.Status = "idle" })
	if !h.worked("pj-o-01") {
		t.Error("work of the session-identified agent not marked")
	}
}

// #5: a typed brief addresses the identified pane's id (app.Pane has no herdr name to prefer).
func TestIdentBriefUsesPaneAddress(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent = "codex"
	h.connect()
	h.brief("pj-o-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if !slices.Equal(l.calls, []string{"w1:p1: [obor pj-lead] verify"}) {
		t.Errorf("typed %q, want w1:p1", l.calls)
	}
}

// T-505: the identity table's transitions; each failed on the code it was found on.
// V1: pj-o-01's and the lead's recorded session are both w1:p1's session s1 (equal proof of t1): t1 is unowned whatever its
// label names, a third label (pj-other) included; neither tied claimant is identified.
func TestIdentEqualProofTerminal(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"", "pj-o-01", "pj-lead", "pj-other"} {
		t.Run("label="+label, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.withLead()
			h.pane("w1:p1").Label = label
			h.sids(map[string]string{"pj-o-01": "s1", "pj-lead": "s1"})
			h.connect()
			eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1)")
			eqs(t, h.where("pj-o-01"), "unavailable no proof", "pj-o-01")
			eqs(t, h.where("pj-lead"), "unavailable no proof", "the lead")
			if label == "pj-other" {
				eqs(t, h.where("pj-other"), "unavailable no proof", "pj-other")
			}
		})
	}
}

// V1: the tie on t1, and serve's start of pj-other in w1:p1 (a lower-ranked start claim): t1 stays unowned; once the
// lead's session is no longer s1 the tie ends and pj-o-01 owns t1 again by session.
func TestIdentEqualProofStartClaim(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = ""
	sids := map[string]string{"pj-o-01": "s1", "pj-lead": "s1"}
	h.sids(sids)
	h.connect()
	eqs(t, h.who("w1:p1"), "", "precondition: the tie")
	// pj-other's start row (foldStart's) after the tie: one folded before it is retired by the lead's session.
	h.a.mu.Lock()
	h.proj().reg.recordStart("pj-other", startRow{Pane: "w1:p1", Terminal: "t1", Kind: "claude"})
	h.a.mu.Unlock()
	h.connect()
	h.a.mu.Lock()
	_, kept := h.proj().reg.start("pj-other")
	h.a.mu.Unlock()
	if !kept {
		t.Fatal("precondition: pj-other's start row")
	}
	eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1) during the tie")
	eqs(t, h.where("pj-other"), "unavailable no proof", "pj-other during the tie")
	delete(sids, "pj-lead")
	h.a.mu.Lock()
	h.proj().folded.sessions = slices.DeleteFunc(h.proj().folded.sessions, func(s store.Session) bool { return s.Agent == "pj-lead" })
	h.a.mu.Unlock()
	h.connect()
	eqs(t, h.where("pj-o-01"), "identified w1:p1 session", "pj-o-01 after the tie")
	eqs(t, h.who("w1:p1"), "pj-o-01", "whoIs(w1:p1) after the tie")
}

// V1 control: without the tie, pj-other's start claim owns t1 (the start claim is a real proof).
func TestIdentStartClaimWithoutTie(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = ""
	h.started("pj-other", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.where("pj-other"), "identified w1:p1 start", "pj-other")
}

// stopNotFound is an adapter's "no such agent" (NotFound).
type stopNotFound struct{}

func (stopNotFound) Error() string  { return "agent_not_found: pj-o-01" }
func (stopNotFound) NotFound() bool { return true }

// verifyStopRace is an Agents whose Stop runs during (a pane event before it returns) and then fails with err.
type verifyStopRace struct {
	during func()
	err    error
	stops  int
}

func (f *verifyStopRace) Launch(context.Context, Launch) (Started, error) { return Started{}, nil }
func (f *verifyStopRace) Stop(context.Context, string, string, bool) (string, error) {
	f.stops++
	f.during()
	return "", f.err
}

// stopWith runs the after-merge stop of pj-o-01 (stopWorker) through ag while lane O-01 holds T-1.
func (h *harness) stopWith(ag Agents) (string, error) {
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1", Agent: "pj-o-01"}
	h.a.mu.Unlock()
	return h.a.stopWorker(context.Background(), p, &Loop{Agents: ag}, mtask{task: "T-1", attempt: 1, lane: "O-01",
		agent: "pj-o-01"})
}

// V2: pj-o-01 identified by label; a second labelled pane appears while its stop fails (a plain refusal, or a
// not-found one): an error naming both panes, not "stopped: no agent". The pane gone instead: the no-agent success.
func TestIdentStopBecomesAmbiguous(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, after string
		err         error
		gone        bool
	}{
		{name: "refused", after: "ambiguous w1:p1 w1:p3", err: errors.New("stop refused before action")},
		{name: "not found", after: "ambiguous w1:p1 w1:p3", err: stopNotFound{}},
		{name: "gone", after: "unavailable exited", err: errors.New("stop refused before action"), gone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.pane("w1:p1").Session = ""
			h.connect()
			eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "before")
			f := &verifyStopRace{err: tc.err, during: func() { h.add(h.twin()) }}
			if tc.gone {
				f.during = func() { h.remove("w1:p1", EventClosed) }
			}
			line, err := h.stopWith(f)
			if f.stops != 1 {
				t.Fatalf("precondition: %d stops, want one", f.stops)
			}
			eqs(t, strings.Fields(h.where("pj-o-01"))[0], strings.Fields(tc.after)[0], "after")
			if tc.gone {
				if err != nil || !strings.HasPrefix(line, "stopped: no agent") {
					t.Errorf("stop %q (err %v), want stopped: no agent", line, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "panes w1:p1 and w1:p3 run pj-o-01") {
				t.Errorf("stop %q (err %v), want an error naming both panes", line, err)
			}
		})
	}
}

// V2: the same interleaving through afterStep and the log: the stop is not settled (no result), the step pending.
func TestIdentStopAmbiguityWritesNoResult(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.connect()
	p := h.proj()
	h.a.record(p, Record{Task: "T-1", Attempt: 1, Step: StepSubmitted, Kind: KindFact},
		submitData{Lane: "O-01", Agent: "pj-o-01"})
	h.a.mu.Lock()
	mt := p.folded.loop["T-1"]
	mt.state = stPublished
	mt.after = slices.Index(afterSteps, StepStop)
	before := mt.after
	h.a.mu.Unlock()
	f := &verifyStopRace{err: errors.New("stop refused before action"), during: func() { h.add(h.twin()) }}
	advanced := h.a.afterStep(context.Background(), p, &Loop{Agents: f}, *mt)
	if f.stops != 1 {
		t.Fatalf("precondition: %d stops, want one", f.stops)
	}
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "after")
	if advanced || p.log.Done(Key{"T-1", 1, StepStop}) || mt.after != before {
		t.Errorf("advanced %v, result %v, after %d (was %d): want the stop pending", advanced,
			p.log.Done(Key{"T-1", 1, StepStop}), mt.after, before)
	}
}

// verifyPost is a Poster whose post runs during before it returns (sent).
type verifyPost struct {
	during func()
	posts  []string
}

func (f *verifyPost) Post(_ context.Context, _, agent, line string) (bool, error) {
	f.posts = append(f.posts, agent+": "+line)
	f.during()
	return true, nil
}

// deliverIntents is the number of delivery intents in pj's log.
func (h *harness) deliverIntents() int {
	n := 0
	for _, r := range h.records() {
		if r.Kind == KindIntent && strings.HasPrefix(r.Step, stepDeliver) {
			n++
		}
	}
	return n
}

// queuedLeadThenBrief queues a lead line, then a brief to pj-o-01 (a Codex pane, typed).
func (h *harness) queuedLeadThenBrief() {
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.a.record(h.proj(), Record{Task: MsgTask, Step: StepMessage, Kind: KindFact},
		Message{To: "pj-lead", Line: "[obor] lead line"})
	h.brief("pj-o-01", "[obor] verify")
}

// V3: the lead's line posted first, its post moves pj-o-01's terminal t1 to w1:p7: the brief is typed into w1:p7.
func TestIdentQueuedBriefMoveBeforeEffect(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Agent = "codex"
	h.started("pj-o-01", "w1:p1", "t1", "s1")
	h.connect()
	h.queuedLeadThenBrief()
	lines := &linerSpy{}
	f := &verifyPost{during: func() { h.move("w1:p1", "w1:p7") }}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: f, Typer: lines})
	if !slices.Equal(f.posts, []string{"pj-lead: [obor] lead line"}) {
		t.Fatalf("precondition: posted %q, want the lead line", f.posts)
	}
	eqs(t, h.where("pj-o-01"), "identified w1:p7 session", "after the move")
	if !slices.Equal(lines.calls, []string{"w1:p7: [obor] verify"}) || len(h.lines()) != 0 {
		t.Errorf("typed %q, queued %q; want the brief typed into w1:p7", lines.calls, h.lines())
	}
}

// V3: the lead's post reveals a second pane labelled pj-o-01: the brief stays queued, no delivery intent, nothing
// typed; once the duplicate closes it is typed into w1:p1.
func TestIdentQueuedBriefAmbiguousBeforeEffect(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Agent = "codex"
	h.pane("w1:p1").Session = ""
	h.connect()
	h.queuedLeadThenBrief()
	lines := &linerSpy{}
	f := &verifyPost{during: func() {
		tw := h.twin()
		tw.Agent = "codex"
		h.add(tw)
	}}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: f, Typer: lines})
	if !slices.Equal(f.posts, []string{"pj-lead: [obor] lead line"}) {
		t.Fatalf("precondition: posted %q, want the lead line", f.posts)
	}
	eqs(t, h.where("pj-o-01"), "ambiguous w1:p1 w1:p3", "after the duplicate")
	if len(lines.calls) != 0 || !slices.Equal(h.lines(), []string{"pj-o-01: [obor] verify"}) ||
		h.deliverIntents() != 1 || h.count(KindFact, StepFailed) != 0 {
		t.Errorf("typed %q, queued %q, %d delivery intents, %d failed facts; want the brief held (the lead's intent only)",
			lines.calls, h.lines(), h.deliverIntents(), h.count(KindFact, StepFailed))
	}
	h.remove("w1:p3", EventClosed)
	f.during = func() {}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: f, Typer: lines})
	if !slices.Equal(lines.calls, []string{"w1:p1: [obor] verify"}) || len(h.lines()) != 0 {
		t.Errorf("after the duplicate closed: typed %q, queued %q; want the brief typed into w1:p1", lines.calls,
			h.lines())
	}
}

// A relabelled pane is identified by its new label without a serve restart: herdr emits nothing for pane.rename, so
// the stream delivers the label with the next event it rereads the pane for (a status event with the pane in New),
// and the app takes it as it takes the rest of the pane.
func TestIdentRelabelledPane(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label, h.pane("w1:p1").Session = "notes", ""
	h.connect()
	eqs(t, h.where("pj-o-01"), "unavailable no proof", "before the relabel: the label is notes")
	relabelled := *h.pane("w1:p1")
	relabelled.Label = "pj-o-01"
	h.a.event(context.Background(), Event{Kind: EventStatus, Pane: "w1:p1", Status: "idle", Agent: "claude", New: relabelled})
	eqs(t, h.where("pj-o-01"), "identified w1:p1 label", "after the relabel")
	eqs(t, h.who("w1:p1"), "pj-o-01", "whoIs(w1:p1)")
	// and a label cleared by the relabel is gone: the event's pane replaces what was known
	relabelled.Label = ""
	h.a.event(context.Background(), Event{Kind: EventStatus, Pane: "w1:p1", Status: "idle", Agent: "claude", New: relabelled})
	eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1) after --clear")
}
