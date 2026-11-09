package app

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/port"
	"obor/internal/store"
)

// 1: serve's agent.start {w1:p1, t1, s1}: start before the pane shows s1, session once it does; the job wake is
// posted to its session.
func TestIdentStartByServe(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 start", "before the session shows")
	h.status("w1:p1", func(p *port.Pane) { p.Session = "s1" })
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "once the pane shows s1")

	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	in, typed := &inboxSpy{}, &boxPrompter{box: map[string]string{}}
	if !h.wake(&Loop{Inbox: in, Typer: typed}) {
		t.Fatal("wake not sent")
	}
	if len(in.calls) != 1 || !strings.HasPrefix(in.calls[0], "pj-l-01: [obor job T-1/1/work]") || len(typed.jobs) != 0 {
		t.Errorf("posted %q, typed %q; want one post to pj-l-01's session, nothing typed", in.calls, typed.jobs)
	}
}

// 2: the lead's obor restart pj-l-01 (no serve start): its recorded session s2 is the pane's session: identified by session; a
// brief to it (a typed kind) is typed into w1:p1.
func TestIdentRestartByLead(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.sids(map[string]string{"pj-l-01": "s2"})
	p := h.pane("w1:p1")
	p.Session, p.Agent = "s2", "codex"
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "where")
	h.brief("pj-l-01", "[obor pj-lead] verify")
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
	h.sids(map[string]string{"pj-l-01": "s1"})
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "before the fresh start")
	h.sids(map[string]string{"pj-l-01": "s3"})
	h.status("w1:p1", func(p *port.Pane) { p.Session = "s3" })
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "after the fresh start")
	h.brief("pj-l-01", "")
	if h.worked("pj-l-01") {
		t.Fatal("precondition: an idle pane is no work")
	}
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	if !h.worked("pj-l-01") {
		t.Error("work on the new session not marked for the brief")
	}
}

// 4: herdr restarted, serve's restore: new pane ids and terminals, the same label, the session record kept: identified by
// session (by label for opencode, which records none); the lane sees no agent exit.
func TestIdentHerdrRestartRestore(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"claude", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			if kind == "claude" {
				h.sids(map[string]string{"pj-l-01": "s1"})
			}
			h.connect()
			seq := h.lastSeq()
			sess, src := "s1", "session"
			if kind == "opencode" {
				sess, src = "", "label"
			}
			h.snap = port.Snapshot{Panes: []port.Pane{{ID: "w9:p1", Group: "pj",
				Cwd: h.lane("L-01"), Agent: kind, Status: "idle", Label: "pj-l-01", Terminal: "t9", Session: sess}}}
			h.connect()
			eqs(t, h.where("pj-l-01"), "identified w9:p1 "+src, "where")
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
	h.started("pj-l-01", "w1:p1", "t1", "")
	h.connect()
	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 start", "first pass")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-l-01 w1:p1"}) {
		t.Errorf("stops %q (err %v), want pj-l-01 at w1:p1", f.stops, err)
	}
}

// 6: the pane closed and another opened under the same label, no session record: identified by label; obor status
// names it.
func TestIdentPaneReopened(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-l-01", "w1:p1", "t1", "")
	h.connect()
	h.remove("w1:p1", port.EventClosed)
	eqs(t, h.where("pj-l-01"), "unavailable exited", "after the close")
	h.add(port.Pane{ID: "w1:p5", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Status: "idle", Label: "pj-l-01",
		Terminal: "t5", Session: "s5"})
	eqs(t, h.where("pj-l-01"), "identified w1:p5 label", "the new pane")
	st := h.a.Status()
	if st.Project == nil || len(st.Project.Agents) != 1 || st.Project.Agents[0].Name != "pj-l-01" ||
		st.Project.Agents[0].Pane != "w1:p5" {
		t.Errorf("status agents %+v, want pj-l-01 in w1:p5", st.Project)
	}
}

// 7: started by hand (claude in a labelled shell), no record: identified by label; a brief is delivered to it.
func TestIdentStartedByHand(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "where")
	h.brief("pj-l-01", "[obor pj-lead] review this")
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
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	before := h.where("pj-l-01")
	eventNow(h.a, port.Event{Kind: port.EventStatus, Pane: "w1:p1", Status: "idle", Agent: "codex"}) // no metadata
	eqs(t, h.where("pj-l-01"), before, "after herdr dropped the name")
	h.brief("pj-l-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if !slices.Equal(l.calls, []string{"w1:p1: [obor pj-lead] verify"}) {
		t.Errorf("typed %q, want the line addressed to w1:p1", l.calls)
	}
}

// 9: two panes labelled pj-l-01, no proof on either: Ambiguous, both named; the wake is not sent and nothing is
// recorded; obor status says so.
func TestIdentTwoLabels(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.snap.Panes = append(h.snap.Panes, h.twin())
	h.connect()
	eqs(t, h.where("pj-l-01"), "ambiguous w1:p1 w1:p3", "where")
	f := &boxPrompter{box: map[string]string{}}
	if h.wake(&Loop{Typer: f}) || len(f.jobs) != 0 || h.count(event.KindIntent, event.StepWork) != 0 {
		t.Errorf("typed %q, %d intents; want nothing", f.jobs, h.count(event.KindIntent, event.StepWork))
	}
	st := h.a.Status()
	for _, as := range st.Project.Agents {
		if as.Name != "pj-l-01 (not identified)" {
			t.Errorf("status names %s %q, want pj-l-01 (not identified)", as.Pane, as.Name)
		}
	}
}

// 10: the pane moved to another tab (same terminal, new id): identified at the new id by the same proof; the stop
// addresses it.
func TestIdentPaneMoved(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = ""
	h.started("pj-l-01", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 start", "before the move")
	h.move("w1:p1", "w1:p7")
	eqs(t, h.where("pj-l-01"), "identified w1:p7 start", "after the move")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-l-01 w1:p7"}) {
		t.Errorf("stops %q (err %v), want pj-l-01 at w1:p7", f.stops, err)
	}
}

// 11: the pane at its shell (the agent exited, the terminal there): Unavailable "at its shell"; the stop is
// "stopped: no agent", nothing signalled.
func TestIdentAtShell(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	h.status("w1:p1", func(p *port.Pane) { p.Agent, p.Status, p.Session = "", "", "" })
	eqs(t, h.where("pj-l-01"), "unavailable at its shell", "where")
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
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	h.move("w1:p1", "w1:p7")
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	f := &idAgents{}
	mt := mtask{task: "T-1", attempt: 1, lane: "L-01", agent: "pj-l-01", after: slices.Index(afterSteps, event.StepStop)}
	if !h.a.afterStep(context.Background(), p, &Loop{Agents: f}, mt) {
		t.Fatal("the stop step failed")
	}
	eqs(t, strings.Join(f.stops, ","), "pj-l-01 w1:p7", "Stop's agent and pane")
	for _, r := range h.records() {
		if r.Step == event.StepStop && strings.Contains(string(r.Data), "w1:p1") {
			t.Errorf("the stop's %s names the old pane: %s", r.Kind, r.Data)
		}
	}
}

// 13: as 12, the old address reused by an unrelated agent in the lane, the moved pane relabelled notes: the start
// proof still holds w1:p7; nothing goes to w1:p1.
func TestIdentMoveAddressReused(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	h.pane("w1:p1").Label = "notes"
	h.move("w1:p1", "w1:p7")
	h.add(port.Pane{ID: "w1:p1", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Status: "idle", Terminal: "t8",
		Session: "s8"})
	eqs(t, h.where("pj-l-01"), "identified w1:p7 session", "where")
	eqs(t, h.who("w1:p1"), "", "the unrelated agent")
	f := &idAgents{}
	if _, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-l-01 w1:p7"}) {
		t.Errorf("stops %q (err %v), want pj-l-01 at w1:p7 only", f.stops, err)
	}
}

// 14 (RV-53 #2): a session claim and a label claim on one terminal: the lead's recorded session s1 is w1:p1's session, w1:p1 is
// labelled pj-advisor: the lead owns t1, pj-advisor has no proof; whoIs(w1:p1) is the lead.
func TestIdentSessionBeatsLabel(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = "pj-advisor"
	h.sids(map[string]string{"pj-lead": "s1"})
	h.connect()
	eqs(t, h.where("pj-lead"), "identified w1:p1 session", "the lead")
	eqs(t, h.where("pj-advisor"), "unavailable no proof", "the label")
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

// 15 (RV-53 #2): row 6, then a second pane labelled pj-l-01 appears: Ambiguous (the old pane not kept), logged
// once; deliveries are held.
func TestIdentDuplicateLater(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.pane("w1:p1").Agent, h.pane("w1:p1").Session = "codex", ""
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "before")
	tw := h.twin()
	tw.Agent = "codex"
	h.add(tw)
	h.tick(0)
	eqs(t, h.where("pj-l-01"), "ambiguous w1:p1 w1:p3", "after the second label")
	want := "agent pj-l-01 not identified: panes w1:p1 and w1:p3 run pj-l-01 (close or relabel one)"
	if n := strings.Count(out.String(), want); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, out)
	}
	h.brief("pj-l-01", "[obor pj-lead] verify")
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
	h.sids(map[string]string{"pj-l-01": "s1"})
	h.connect()
	eqs(t, h.where("pj-l-01"), "ambiguous w1:p1 w1:p3", "before")
	h.status("w1:p1", func(p *port.Pane) { p.Session = "s1" })
	h.tick(0)
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "after the session shows")
	want := "pane w1:p3 labelled pj-l-01 too, not its session"
	if n := strings.Count(out.String(), want); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, out)
	}
}

// 17: a start row {w1:p1, t1} whose pane now runs the integrator's session: pj-l-01 has none (the row retired,
// logged), pj-int owns t1; the stop of pj-l-01 signals nothing.
func TestIdentStartRowRetired(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.withLead()
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	p := h.pane("w1:p1")
	p.Label, p.Session = "pj-int", "s9"
	h.sids(map[string]string{"pj-int": "s9"})
	h.connect()
	eqs(t, h.where("pj-int"), "identified w1:p1 session", "pj-int")
	eqs(t, h.where("pj-l-01"), "unavailable no proof", "pj-l-01")
	if !strings.Contains(out.String(), "pj-l-01's start row retired: pane w1:p1 runs pj-int now") {
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

// T-605 #1: a stale start row of hk-h1 (the config names no such agent) on w1:p1, labelled pj-l-01: the label wins,
// and the stale row is retired in memory, silently (T-1099: it is retired again at every start).
func TestIdentUnnamedStartRetired(t *testing.T) {
	t.Parallel()
	h, out := newIdentHarness(t)
	h.started("hk-h1", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.who("w1:p1"), "pj-l-01", "whoIs(w1:p1)")
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "pj-l-01")
	eqs(t, h.where("hk-h1"), "unavailable no proof", "hk-h1")
	h.a.mu.Lock()
	_, kept := h.proj().reg.Start("hk-h1")
	h.a.mu.Unlock()
	if kept {
		t.Error("hk-h1's start row kept after the pass")
	}
	h.connect()
	if n := strings.Count(out.String(), "start row retired"); n != 0 {
		t.Errorf("logged a retirement %d times, want none (T-1099):\n%s", n, out)
	}
}

// T-605 #2: nothing else in the ranking changes: a configured agent's start row still beats a label (pj-l-01's start
// on w1:p1, labelled pj-l-02; both are lane agents of the config).
func TestIdentNamedStartBeatsLabel(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"name":"pj","prefix":"pj","lanes":2}`)
	h.pane("w1:p1").Label = "pj-l-02"
	h.started("pj-l-01", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.who("w1:p1"), "pj-l-01", "whoIs(w1:p1)")
	eqs(t, h.where("pj-l-01"), "identified w1:p1 start", "pj-l-01")
	eqs(t, h.where("pj-l-02"), "unavailable no proof", "pj-l-02")
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
	sids := map[string]string{"pj-l-01": "s1"}
	h.sids(sids)
	h.connect()
	h.brief("pj-l-01", "")
	h.a.closeLog()
	h.a = New(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	h.sids(sids)
	h.pane("w1:p1").Status = "working"
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "where")
	if !h.worked("pj-l-01") || h.count(event.KindFact, event.StepWorked) != 1 {
		t.Errorf("worked %v, %d worked facts; want the work marked on the first snapshot", h.worked("pj-l-01"),
			h.count(event.KindFact, event.StepWorked))
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
	eqs(t, h.where("pj-l-01"), "ambiguous w1:p1 w1:p3", "where")
	f := &boxPrompter{box: map[string]string{}}
	fx := &Loop{Typer: f}
	if h.wake(fx) || len(f.jobs) != 0 {
		t.Fatalf("typed %q while ambiguous", f.jobs)
	}
	if h.count(event.KindIntent, event.StepWork) != 0 || h.count(event.KindFact, event.StepFailed) != 0 {
		t.Errorf("%d intents, %d failed facts; want none", h.count(event.KindIntent, event.StepWork), h.count(event.KindFact, event.StepFailed))
	}
	h.remove("w1:p3", port.EventClosed)
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
	h.brief("pj-l-01", "[obor pj-lead] verify")
	l := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Typer: l})
	if len(l.calls) != 0 {
		t.Fatalf("precondition: held while ambiguous, typed %q", l.calls)
	}
	h.remove("w1:p3", port.EventClosed)
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "where")
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
	eqs(t, h.where("pj-l-01"), "unavailable not connected", "where")
	f := &boxPrompter{box: map[string]string{}}
	if h.wake(&Loop{Typer: f}) || len(f.jobs) != 0 || h.count(event.KindIntent, event.StepWork) != 0 {
		t.Errorf("wake typed %q before the first snapshot", f.jobs)
	}
	h.brief("pj-l-01", "[obor pj-lead] verify")
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
		port.Pane{ID: "w1:p8", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-lead", Terminal: "t8"},
		port.Pane{ID: "w1:p9", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-lead", Terminal: "t9"})
	h.connect()
	eqs(t, h.where("pj-lead"), "ambiguous w1:p8 w1:p9", "the lead")
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
		event.Message{To: "pj-lead", From: "obor", Line: "[obor] hello"}) {
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
	h.status("w1:p1", func(p *port.Pane) { p.Cwd = filepath.Join(h.wt, "elsewhere") })
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "where")
	h.a.mu.Lock()
	w, _ := h.a.agentOf(h.proj(), h.lanes.lanes[0])
	h.a.mu.Unlock()
	if w.State == Identified {
		t.Errorf("agentOf(L-01) = %s, want none in the lane", w.Pane.ID)
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
		h.pane("w1:p1").Label = "pj-advisor"
		h.sids(map[string]string{"pj-lead": "s1"})
		h.started("pj-l-01", "w1:p4", "t4", "")
		h.snap.Panes = append(h.snap.Panes,
			port.Pane{ID: "w1:p4", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Status: "idle", Terminal: "t4"},
			port.Pane{ID: "w1:p5", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-l-02", Terminal: "t5"},
			port.Pane{ID: "w1:p6", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-l-02", Terminal: "t6"},
			port.Pane{ID: "w1:p7", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-l-01", Terminal: "t7"})
		rand.New(rand.NewSource(int64(i))).Shuffle(len(h.snap.Panes), func(a, b int) {
			h.snap.Panes[a], h.snap.Panes[b] = h.snap.Panes[b], h.snap.Panes[a]
		})
		h.connect()
		got := h.table("pj-lead", "pj-int", "pj-l-01", "pj-l-02", "pj-advisor")
		if i == 0 {
			first = got
			eqs(t, h.where("pj-lead"), "identified w1:p1 session", "the lead")
			eqs(t, h.where("pj-l-01"), "identified w1:p4 start", "pj-l-01")
			eqs(t, h.where("pj-l-02"), "ambiguous w1:p5 w1:p6", "pj-l-02")
			eqs(t, h.where("pj-advisor"), "unavailable no proof", "pj-advisor")
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
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "before")
	p := h.proj()
	h.a.record(p, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		submitData{Lane: "L-01", Agent: "pj-l-01"})
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	mt := p.folded.loop["T-1"]
	mt.state = stPublished
	mt.after = slices.Index(afterSteps, event.StepStop)
	h.a.connected = false
	h.a.mu.Unlock()
	f := &idAgents{}
	if h.a.afterStep(context.Background(), p, &Loop{Agents: f}, *mt) || p.log.Done(event.Key{Task: "T-1", Attempt: 1, Step: event.StepStop}) ||
		len(f.stops) != 0 {
		t.Fatalf("disconnected: stops %q, result %v; want no stop, the step pending", f.stops,
			p.log.Done(event.Key{Task: "T-1", Attempt: 1, Step: event.StepStop}))
	}
	h.connect()
	if line, err := h.stop(f); err != nil || !slices.Equal(f.stops, []string{"pj-l-01 w1:p1"}) {
		t.Errorf("connected again: stop %q (%v), stops %q; want w1:p1 stopped", line, err, f.stops)
	}
}

// DD-v0.13 A3 (and B1's kind): a wake and a line are posted, with the pane's kind, only when pj-l-01's inbox record
// names the identified pane's session (s1) or none; a record of another session (s9): the wake and the line are typed
// into w1:p1 (T-746: a line to a ready agent is delivered, never held on a session mismatch).
func TestIdentInboxOfAnotherSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ rec, want string }{{"s1", "posted"}, {"", "posted"}, {"s9", "typed"}} {
		t.Run("record "+tc.rec, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.started("pj-l-01", "w1:p1", "t1", "s1")
			h.connect()
			eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "where")
			h.a.opt.Inboxed = func(string) (string, bool) { return tc.rec, true }
			in, typed, liner := &kindSpy{}, &boxPrompter{box: map[string]string{}}, &linerSpy{}
			if !h.wake(&Loop{Inbox: in, Typer: typed}) {
				t.Fatal("wake not sent")
			}
			h.brief("pj-l-01", "[obor pj-lead] verify")
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
			if tc.want == "posted" {
				if len(in.calls) != 2 || !strings.HasPrefix(in.calls[0], "claude pj-l-01: [obor job T-1/1/work]") ||
					in.calls[1] != "claude pj-l-01: [obor pj-lead] verify" || len(typed.jobs) != 0 {
					t.Errorf("posted %q, typed %q; want the wake and the line posted with the pane's kind", in.calls,
						typed.jobs)
				}
				return
			}
			if len(in.calls) != 0 || !slices.Equal(typed.jobs, []string{"T-1/1/work"}) ||
				!slices.Equal(liner.calls, []string{"w1:p1: [obor pj-lead] verify"}) || len(h.lines()) != 0 {
				t.Errorf("posted %q, typed %q and %q, queued %q; want the wake and the line typed into w1:p1", in.calls,
					typed.jobs, liner.calls, h.lines())
			}
		})
	}
}

// DD-v0.13 R1: a line is held only on a known mismatch. The lead's pane identified by label with no session from
// herdr: its line is posted (even against another session's record: its record is its own latest SessionStart). A
// role pane with no session: posted. A role pane whose session (s1) differs from the record's (s9): typed into its
// pane (T-746: no longer held).
func TestIdentSessionUnknown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, agent, pane, rec string
		want                   string
	}{
		{"lead, no pane session", "pj-lead", "", "sess-lead", "pj-lead: [obor] hello"},
		{"lead, other session", "pj-lead", "s1", "s9", "pj-lead: [obor] hello"},
		{"role, no pane session", "pj-l-01", "", "s9", "claude pj-l-01: [obor] hello"},
		{"role, known mismatch", "pj-l-01", "s1", "s9", "typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.withLead()
			h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w1:p8", Group: "pj", Cwd: h.root, Agent: "claude",
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
				if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
					event.Message{To: tc.agent, From: "obor", Line: "[obor] hello"}) {
					t.Fatal("line not recorded")
				}
			} else {
				h.brief(tc.agent, "[obor] hello")
			}
			in, liner := &kindSpy{}, &linerSpy{}
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
			if tc.want == "typed" {
				if len(in.calls) != 0 || !slices.Equal(liner.calls, []string{"w1:p1: [obor] hello"}) || len(h.lines()) != 0 {
					t.Errorf("posted %q, typed %q, queued %q; want the line typed into w1:p1", in.calls, liner.calls,
						h.lines())
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
	h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w1:p0", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude",
		Status: "idle", Terminal: "t0"})
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "where")
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if o := h.a.placement(h.proj(), h.lane("L-01"), "pj-l-01"); o == nil || o.pane.ID != "w1:p1" {
		t.Errorf("placement for pj-l-01 = %v, want w1:p1", o)
	}
	if o := h.a.placement(h.proj(), h.lane("L-01"), "pj-l-02"); o == nil || o.pane.ID != "w1:p0" {
		t.Errorf("placement for an unidentified agent = %v, want the lowest-id agent pane w1:p0", o)
	}
}

// DD-v0.13 D1: the integrator's type names a kind obor has no bridge for: its start is refused (no Launch), naming
// obor config validate.
func TestIdentIntegratorWithoutBridge(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj",`+
		`"roles":{"integrator":"cur","lead":""},"models":{"cur":{"kind":"pi","model":"x"}}}`)
	h.connect()
	if h.proj().integrator != "pj-int" {
		t.Fatalf("precondition: integrator %q", h.proj().integrator)
	}
	f := &launchSpy{}
	_, err := h.a.startIntegrator(context.Background(), f, h.root, port.Launch{Prompt: "x"})
	if err == nil || err.Error() != "integrator pj-int: its kind pi has no bridge (obor config validate)" || f.n != 0 {
		t.Errorf("start: %v, %d launches; want refused, no launch", err, f.n)
	}
}

// launchSpy is an Agents counting its launches.
type launchSpy struct {
	idAgents
	n int
}

func (f *launchSpy) Launch(context.Context, port.Launch) (port.Started, error) {
	f.n++
	return port.Started{Line: "STARTED"}, nil
}

// T-504: the identity table's cases; each failed on the code it was found on.
// #4: an unlabelled pane identified by session works on its brief: worked.
func TestIdentBriefWorkByRegistry(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Label = ""
	h.sids(map[string]string{"pj-l-01": "s1"})
	h.connect()
	h.brief("pj-l-01", "")
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	if !h.worked("pj-l-01") {
		t.Error("work of the session-identified agent not marked")
	}
}

// T-505: the identity table's transitions; each failed on the code it was found on.
// V1: pj-l-01's and the lead's recorded session are both w1:p1's session s1 (equal proof of t1): t1 is unowned whatever its
// label names, a third label (pj-advisor) included; neither tied claimant is identified.
func TestIdentEqualProofTerminal(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"", "pj-l-01", "pj-lead", "pj-advisor"} {
		t.Run("label="+label, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.withLead()
			h.pane("w1:p1").Label = label
			h.sids(map[string]string{"pj-l-01": "s1", "pj-lead": "s1"})
			h.connect()
			eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1)")
			eqs(t, h.where("pj-l-01"), "unavailable no proof", "pj-l-01")
			eqs(t, h.where("pj-lead"), "unavailable no proof", "the lead")
			if label == "pj-advisor" {
				eqs(t, h.where("pj-advisor"), "unavailable no proof", "pj-advisor")
			}
		})
	}
}

// V1: the tie on t1, and serve's start of pj-advisor in w1:p1 (a lower-ranked start claim): t1 stays unowned; once the
// lead's session is no longer s1 the tie ends and pj-l-01 owns t1 again by session.
func TestIdentEqualProofStartClaim(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = ""
	sids := map[string]string{"pj-l-01": "s1", "pj-lead": "s1"}
	h.sids(sids)
	h.connect()
	eqs(t, h.who("w1:p1"), "", "precondition: the tie")
	// pj-advisor's start row (foldStart's) after the tie: one folded before it is retired by the lead's session.
	h.a.mu.Lock()
	h.proj().reg.RecordStart("pj-advisor", identity.StartRow{Pane: "w1:p1", Terminal: "t1", Kind: "claude"})
	h.a.mu.Unlock()
	h.connect()
	h.a.mu.Lock()
	_, kept := h.proj().reg.Start("pj-advisor")
	h.a.mu.Unlock()
	if !kept {
		t.Fatal("precondition: pj-advisor's start row")
	}
	eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1) during the tie")
	eqs(t, h.where("pj-advisor"), "unavailable no proof", "pj-advisor during the tie")
	delete(sids, "pj-lead")
	h.a.mu.Lock()
	rows := slices.DeleteFunc(slices.Clone(h.proj().folded.sessions.All()), func(s store.Session) bool { return s.Agent == "pj-lead" })
	h.proj().folded.sessions = store.FoldSessions(rows)
	h.a.mu.Unlock()
	h.connect()
	eqs(t, h.where("pj-l-01"), "identified w1:p1 session", "pj-l-01 after the tie")
	eqs(t, h.who("w1:p1"), "pj-l-01", "whoIs(w1:p1) after the tie")
}

// V1 control: without the tie, pj-advisor's start claim owns t1 (the start claim is a real proof).
func TestIdentStartClaimWithoutTie(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Label = ""
	h.started("pj-advisor", "w1:p1", "t1", "")
	h.connect()
	eqs(t, h.where("pj-advisor"), "identified w1:p1 start", "pj-advisor")
}

// stopNotFound is an adapter's "no such agent" (NotFound).
type stopNotFound struct{}

func (stopNotFound) Error() string { return "agent_not_found: pj-l-01" }

func (stopNotFound) NotFound() bool { return true }

// verifyStopRace is an Agents whose Stop runs during (a pane event before it returns) and then fails with err.
type verifyStopRace struct {
	during func()
	err    error
	stops  int
}

func (f *verifyStopRace) Launch(context.Context, port.Launch) (port.Started, error) {
	return port.Started{}, nil
}

func (f *verifyStopRace) Stop(context.Context, string, string, bool) (string, error) {
	f.stops++
	f.during()
	return "", f.err
}

// V2: pj-l-01 identified by label; a second labelled pane appears while its stop fails (a plain refusal, or a
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
			eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "before")
			f := &verifyStopRace{err: tc.err, during: func() { h.add(h.twin()) }}
			if tc.gone {
				f.during = func() { h.remove("w1:p1", port.EventClosed) }
			}
			line, err := h.stopWith(f)
			if f.stops != 1 {
				t.Fatalf("precondition: %d stops, want one", f.stops)
			}
			eqs(t, strings.Fields(h.where("pj-l-01"))[0], strings.Fields(tc.after)[0], "after")
			if tc.gone {
				if err != nil || !strings.HasPrefix(line, "stopped: no agent") {
					t.Errorf("stop %q (err %v), want stopped: no agent", line, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "panes w1:p1 and w1:p3 run pj-l-01") {
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
	h.a.record(p, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		submitData{Lane: "L-01", Agent: "pj-l-01"})
	h.a.mu.Lock()
	mt := p.folded.loop["T-1"]
	mt.state = stPublished
	mt.after = slices.Index(afterSteps, event.StepStop)
	before := mt.after
	h.a.mu.Unlock()
	f := &verifyStopRace{err: errors.New("stop refused before action"), during: func() { h.add(h.twin()) }}
	advanced := h.a.afterStep(context.Background(), p, &Loop{Agents: f}, *mt)
	if f.stops != 1 {
		t.Fatalf("precondition: %d stops, want one", f.stops)
	}
	eqs(t, h.where("pj-l-01"), "ambiguous w1:p1 w1:p3", "after")
	if advanced || p.log.Done(event.Key{Task: "T-1", Attempt: 1, Step: event.StepStop}) || mt.after != before {
		t.Errorf("advanced %v, result %v, after %d (was %d): want the stop pending", advanced,
			p.log.Done(event.Key{Task: "T-1", Attempt: 1, Step: event.StepStop}), mt.after, before)
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

// V3: the lead's line posted first, its post moves pj-l-01's terminal t1 to w1:p7: the brief is typed into w1:p7.
func TestIdentQueuedBriefMoveBeforeEffect(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Agent = "codex"
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	h.queuedLeadThenBrief()
	lines := &linerSpy{}
	f := &verifyPost{during: func() { h.move("w1:p1", "w1:p7") }}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: f, Typer: lines})
	if !slices.Equal(f.posts, []string{"pj-lead: [obor] lead line"}) {
		t.Fatalf("precondition: posted %q, want the lead line", f.posts)
	}
	eqs(t, h.where("pj-l-01"), "identified w1:p7 session", "after the move")
	if !slices.Equal(lines.calls, []string{"w1:p7: [obor] verify"}) || len(h.lines()) != 0 {
		t.Errorf("typed %q, queued %q; want the brief typed into w1:p7", lines.calls, h.lines())
	}
}

// V3: the lead's post reveals a second pane labelled pj-l-01: the brief stays queued, no delivery intent, nothing
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
	eqs(t, h.where("pj-l-01"), "ambiguous w1:p1 w1:p3", "after the duplicate")
	if len(lines.calls) != 0 || !slices.Equal(h.lines(), []string{"pj-l-01: [obor] verify"}) ||
		h.deliverIntents() != 1 || h.count(event.KindFact, event.StepFailed) != 0 {
		t.Errorf("typed %q, queued %q, %d delivery intents, %d failed facts; want the brief held (the lead's intent only)",
			lines.calls, h.lines(), h.deliverIntents(), h.count(event.KindFact, event.StepFailed))
	}
	h.remove("w1:p3", port.EventClosed)
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
	eqs(t, h.where("pj-l-01"), "unavailable no proof", "before the relabel: the label is notes")
	relabelled := *h.pane("w1:p1")
	relabelled.Label = "pj-l-01"
	eventNow(h.a, port.Event{Kind: port.EventStatus, Pane: "w1:p1", Status: "idle", Agent: "claude", New: relabelled})
	eqs(t, h.where("pj-l-01"), "identified w1:p1 label", "after the relabel")
	eqs(t, h.who("w1:p1"), "pj-l-01", "whoIs(w1:p1)")
	// and a label cleared by the relabel is gone: the event's pane replaces what was known
	relabelled.Label = ""
	eventNow(h.a, port.Event{Kind: port.EventStatus, Pane: "w1:p1", Status: "idle", Agent: "claude", New: relabelled})
	eqs(t, h.who("w1:p1"), "", "whoIs(w1:p1) after --clear")
}

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
	return port.Pane{ID: id, Group: "pj", Cwd: cwd, Label: label}
}

// placement_test.go: which pane of a lane dir serve starts the lane's agent in (paneIn, T-783): the label is the
// pane's identity (R-28 §2), so `obor setup pane L-0N` — a labelled shell with no agent in it yet — is the place the
// lane's first dispatch uses, not a pane of a lower id or one someone left an agent in.

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
			return []port.Pane{{ID: "w1:p0", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Status: "idle"},
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
			return []port.Pane{{ID: "w1:p1", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-02",
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
				{ID: "w1:p0", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle",
					Terminal: "t0"},
				{ID: "w1:p1", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle",
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

// pendingStart is a pending agent.start intent of T-1 in pane: serve recorded the start and never its result.
func pendingStart(t *testing.T, h *harness, pane string) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepStartWork, event.KindIntent, startData{Lane: "L-01", Agent: "pj-l-01", Pane: pane})
}

// replays_test.go: agent.start's replay of a pending intent whose recorded pane is gone (T-783, rev 2): the start
// chooses the pane again through paneIn — a new labelled pane in the lane dir is where the agent starts — and no
// launch names the dead id.

// T-783 (rev 2): the pane an agent.start intent recorded is gone (its tab closed, then obor setup pane made a new
// labelled one, at its shell): the replay starts the agent once in the pane chosen again, records it in the start's
// result, and is no needs-you (RV-101 #1: a pane at its shell is still a launch).
func TestStartReplayStartsInThePaneChosenAgain(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) {
		// The snapshot has no w1:p1 at all: that tab was closed. w1:p3 is the new pane, labelled for the lane's agent.
		h.snap.Panes = []port.Pane{{ID: "w1:p3", Group: "pj", Cwd: h.lane("L-01"), Label: "pj-l-01"}}
	})
	pendingStart(t, h, "w1:p1")
	if got := known(h, "w1:p1"); got.ID != "" {
		t.Fatalf("precondition: w1:p1 is in the snapshot: %+v", got)
	}
	if got := known(h, "w1:p3"); got.Label != "pj-l-01" || got.Agent != "" {
		t.Fatalf("precondition: the new pane is %+v, want at its shell, labelled pj-l-01", got)
	}
	if !h.step(t, fx) {
		t.Fatal("the replay did not start in the pane chosen again")
	}
	if ag.pane != "w1:p3" || ag.restarts != 1 {
		t.Errorf("launched %d time(s) in %q, want once in w1:p3", ag.restarts, ag.pane)
	}
	var out startOut
	if err := json.Unmarshal([]byte(h.result(event.StepStartWork)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Pane != "w1:p3" || out.Refused != "" {
		t.Errorf("start result %+v, want the start in w1:p3", out)
	}
	if n := h.count(event.KindFact, event.StepNeedsYou); n != 0 {
		t.Errorf("needs-you facts: %d, want 0", n)
	}
}

// T-783 (rev 2): the same replay with no pane left in the lane takes the usual no-pane wait — no launch, no needs-you
// on the first pass — not a start into the recorded, dead id.
func TestStartReplayWithNoPaneLeftWaits(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) { h.snap.Panes = nil }) // every pane closed
	pendingStart(t, h, "w1:p1")
	if h.step(t, fx) {
		t.Fatal("the replay went on with no pane in the lane")
	}
	if since(h).IsZero() {
		t.Error("the no-pane clock did not start")
	}
	if ag.restarts != 0 {
		t.Errorf("launches with no pane: %d", ag.restarts)
	}
	if n := h.count(event.KindFact, event.StepNeedsYou); n != 0 {
		t.Errorf("needs-you facts on the first pass: %d, want 0", n)
	}
}

// RV-101 #1: the same replay when the pane chosen again already runs an agent — the start that ran there before a
// crash (or a launch error after its side effect), its result never recorded: the intent is still the gone pane's, so
// nothing tells that agent from another one. cannot tell, never a launch that would stop a worker at its job; the
// needs-you names the gone pane, the pane chosen again and what runs there.
func TestStartReplayIntoAPaneRunningAnAgentCannotTell(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false, func(h *harness) {
		h.snap.Panes = []port.Pane{{ID: "w1:p3", Group: "pj", Cwd: h.lane("L-01"), Label: "pj-l-01",
			Agent: "claude", Session: "new-session", Status: "working"}}
	})
	pendingStart(t, h, "w1:p1")
	if got := known(h, "w1:p1"); got.ID != "" {
		t.Fatalf("precondition: w1:p1 is in the snapshot: %+v", got)
	}
	if got := known(h, "w1:p3"); got.Agent != "claude" || got.Label != "pj-l-01" {
		t.Fatalf("precondition: w1:p3 is %+v, want claude labelled pj-l-01", got)
	}
	h.step(t, fx)
	if ag.restarts != 0 {
		t.Errorf("launched %d time(s) in %q, want none", ag.restarts, ag.pane)
	}
	var out startOut
	if err := json.Unmarshal([]byte(h.result(event.StepStartWork)), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cannot tell", "w1:p1", "w1:p3", "claude", "pj-l-01", "obor resume T-1"} {
		if !strings.Contains(out.Refused, want) {
			t.Errorf("start result refused %q, want it to name %q", out.Refused, want)
		}
	}
	h.step(t, fx)
	if !h.has("needs-you{start: cannot tell") {
		t.Errorf("no needs-you{start: cannot tell …}: %v", h.facts(0))
	}
	if ag.restarts != 0 {
		t.Errorf("launches after the needs-you: %d, want 0", ag.restarts)
	}
}
