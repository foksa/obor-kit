package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/store"
)

// T-718: the merge loop's publish waits while the reviewer or the deep dive works (an open brief, a pane not idle),
// for at most holdCap; gates go on; the hold is never recorded.

// seedHold is seedSched's project with a reviewer (pane w1:rev) and a deep dive (pane w1:dd), both idle in the main
// checkout, and T-1 accepted.
func seedHold(t *testing.T) (*harness, *schedScratch) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"name":"pj","prefix":"pj","roles":{"integrator":{},"reviewer":{},"deep_dive":{}}}`)
	h.snap.Panes = append(h.snap.Panes,
		port.Pane{ID: "w1:rev", Workspace: "w1", Cwd: h.root, Agent: "claude", Label: "pj-rev", Status: "idle"},
		port.Pane{ID: "w1:dd", Workspace: "w1", Cwd: h.root, Agent: "claude", Label: "pj-deep-dive", Status: "idle"})
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1"}
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "O-01", Agent: "pj-o-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "O-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outConflict})
	acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 1, "candidate1")
	return h, f
}

// work sets pane id working (or idle) as a herdr event reports it.
func (h *harness) work(id string, working bool) {
	h.status(id, func(p *port.Pane) {
		p.Status = map[bool]string{true: "working", false: "idle"}[working]
	})
}

// state is T-1's merge-loop state.
func (h *harness) state(task string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if mt := h.a.p.folded.loop[task]; mt != nil {
		return mt.state
	}
	return ""
}

// taskHold is obor status' hold of task.
func (h *harness) taskHold(task string) string {
	st := h.a.Status()
	for _, ts := range st.Project.Tasks {
		if ts.Task == task {
			return ts.Hold
		}
	}
	return ""
}

// leadLinesWith is the lines to the lead in the log that contain text.
func (h *harness) leadLinesWith(text string) []string {
	h.t.Helper()
	recs, err := event.ReadLog(h.root)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		var m event.Message
		if r.Step != event.StepMessage || json.Unmarshal(r.Data, &m) != nil || m.To != "pj-lead" {
			continue
		}
		if strings.Contains(m.Line, text) {
			out = append(out, m.Line)
		}
	}
	return out
}

func TestMergeHoldWaitsWhileARoleWorks(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ pane, role string }{{"w1:rev", "reviewer"}, {"w1:dd", "deep_dive"}} {
		t.Run(c.role, func(t *testing.T) {
			t.Parallel()
			h, _ := seedHold(t)
			ctx := context.Background()
			h.work(c.pane, true)
			h.pass(ctx)
			if got := h.state("T-1"); got != stAccepted || countStep(t, h, nth(event.StepPublish, 1)) != 0 {
				t.Fatalf("%s working: T-1 %s, %d publish records; want accepted, none", c.role, got,
					countStep(t, h, nth(event.StepPublish, 1)))
			}
			if got, want := h.taskHold("T-1"), "merge held: "+c.role+" working"; got != want {
				t.Errorf("status hold = %q, want %q", got, want)
			}
			h.work(c.pane, false)
			h.pass(ctx)
			if got := h.state("T-1"); got != stMerged {
				t.Errorf("%s idle: T-1 %s, want merged", c.role, got)
			}
			if got := h.taskHold("T-1"); got != "" {
				t.Errorf("merged: status hold = %q", got)
			}
		})
	}
}

// A brief open to the reviewer holds the merge though its pane is idle: one waiting for its delivery (a line), or a
// fresh start's (no line) until the reviewer worked on it and is idle again.
func TestMergeHoldWaitsForAnOpenBrief(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"verify #1", ""} {
		t.Run(map[bool]string{true: "undelivered line", false: "fresh start"}[note != ""], func(t *testing.T) {
			t.Parallel()
			h, _ := seedHold(t)
			ctx := context.Background()
			if _, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", To: "pj-rev", Note: note, Role: "lead"}); err != nil {
				t.Fatal(err)
			}
			h.pass(ctx)
			if got := h.state("T-1"); got != stAccepted || h.taskHold("T-1") != "merge held: reviewer working" {
				t.Fatalf("brief open: T-1 %s, hold %q; want accepted, held by the reviewer", got, h.taskHold("T-1"))
			}
			if note != "" {
				return // a claude reviewer's line waits for its inbox: the brief stays open
			}
			h.work("w1:rev", true) // it works on the brief, then is idle: the brief is over
			h.work("w1:rev", false)
			h.pass(ctx)
			if got := h.state("T-1"); got != stMerged {
				t.Errorf("brief finished: T-1 %s, want merged", got)
			}
		})
	}
}

// After holdCap the merge goes ahead and the lead gets one line.
func TestMergeHoldCap(t *testing.T) {
	t.Parallel()
	h, _ := seedHold(t)
	ctx := context.Background()
	h.work("w1:rev", true)
	h.pass(ctx)
	h.now = h.now.Add(holdCap - time.Minute)
	h.pass(ctx)
	if got := h.state("T-1"); got != stAccepted || len(h.leadLinesWith("went ahead")) != 0 {
		t.Fatalf("29m held: T-1 %s, lines %q; want accepted, no line", got, h.leadLinesWith("went ahead"))
	}
	h.now = h.now.Add(time.Minute)
	h.pass(ctx)
	if got := h.state("T-1"); got != stMerged {
		t.Errorf("30m held: T-1 %s, want merged", got)
	}
	want := []string{"[obor] merge of T-1 went ahead: reviewer still working after 30m"}
	h.pass(ctx)
	if got := h.leadLinesWith("went ahead"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lead lines = %q, want %q", got, want)
	}
}

// A gate runs while the merge is held: only the publish waits.
func TestMergeHoldGatesGoOn(t *testing.T) {
	t.Parallel()
	h, f := seedHold(t)
	ctx := context.Background()
	h.work("w1:rev", true)
	addRecord(t, h, "T-2", event.StepSubmitted, event.KindFact, submitData{Lane: "O-02"})
	addRecord(t, h, "T-2", event.StepSubmit, event.KindResult, submitOut{SHA: "s2"})
	h.pass(ctx)
	if got := h.state("T-1"); got != stAccepted {
		t.Fatalf("precondition: T-1 %s, want accepted (held)", got)
	}
	gated := strings.Join(f.gated, ",")
	if !strings.Contains(gated, "T-2") || h.state("T-2") == stSubmitted {
		t.Errorf("T-2 not gated during the hold: gated %q, T-2 %s", gated, h.state("T-2"))
	}
}

// The hold is never recorded: a restart replays T-1 accepted, the hold is decided afresh (still held while the
// reviewer works) and the publish runs once it is idle.
func TestMergeHoldReplay(t *testing.T) {
	t.Parallel()
	h, _ := seedHold(t)
	ctx := context.Background()
	h.work("w1:rev", true)
	before := countTask(t, h, "T-1")
	h.pass(ctx)
	if got := h.state("T-1"); got != stAccepted || h.taskHold("T-1") == "" {
		t.Fatalf("precondition: T-1 %s, hold %q; want held", got, h.taskHold("T-1"))
	}
	if after := countTask(t, h, "T-1"); after != before {
		t.Errorf("the held pass recorded %d records of T-1", after-before)
	}
	p := reloadProj(t, h)
	if mt := p.folded.loop["T-1"]; mt.state != stAccepted || len(p.holds) != 0 {
		t.Fatalf("replayed: T-1 %s, holds %v; want accepted, no hold", mt.state, p.holds)
	}
	h.pass(ctx)
	if got := h.state("T-1"); got != stAccepted || h.taskHold("T-1") != "merge held: reviewer working" {
		t.Errorf("after replay, reviewer working: T-1 %s, hold %q; want held", got, h.taskHold("T-1"))
	}
	h.work("w1:rev", false)
	h.pass(ctx)
	if got := h.state("T-1"); got != stMerged {
		t.Errorf("after replay, reviewer idle: T-1 %s, want merged", got)
	}
}

// countTask is the number of task's records in the log.
func countTask(t *testing.T, h *harness, task string) int {
	t.Helper()
	n := 0
	for _, r := range readLog(t, h.root) {
		if r.Task == task {
			n++
		}
	}
	return n
}

// T-732: a worked brief whose reviewer exits at its shell before serve sees it idle ends (its finished line), and the
// merge it held goes on at once, not after holdCap.
func TestMergeHoldEndsWhenTheBriefedAgentIsGone(t *testing.T) {
	t.Parallel()
	h, _ := seedHold(t)
	ctx := context.Background()
	if _, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", To: "pj-rev", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	h.work("w1:rev", true)
	h.pass(ctx)
	if got := h.state("T-1"); got != stAccepted || h.taskHold("T-1") != "merge held: reviewer working" {
		t.Fatalf("brief worked on: T-1 %s, hold %q; want accepted, held by the reviewer", got, h.taskHold("T-1"))
	}
	h.status("w1:rev", func(p *port.Pane) { p.Agent, p.Status = "", "" }) // the agent exits: its pane at its shell
	h.pass(ctx)
	if got := h.state("T-1"); got != stMerged {
		t.Errorf("reviewer gone: T-1 %s, want merged", got)
	}
	if n := len(h.leadLinesWith("[obor] pj-rev finished (briefed ")); n != 1 {
		t.Errorf("finished lines %d, want 1", n)
	}
	if len(h.a.Status().Project.Briefs) != 0 {
		t.Errorf("briefs %+v, want none open", h.a.Status().Project.Briefs)
	}
}
