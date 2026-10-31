package setup

import (
	"strings"
	"testing"
	"time"
)

// A run's panes go to the project's herdr workspace — the one whose panes work in the project — never to the workspace
// of the pane obor was run from when that is another project's (T-628, live: axis-maps' panes landed in obor-kit's w5).
func TestPlacementFollowsTheProjectsWorkspace(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "O-01", "opus")
	lane := x.LaneDir("O-01")
	x.h.Panes = nil
	x.h.Tab("w5:t9", 200, 50, "w5:p9") // the caller's: another project's pane
	x.h.Tab("wA:t1", 200, 50, "wA:p1") // this project's
	if len(x.h.Panes) != 2 {
		t.Fatalf("precondition: %d scripted panes, want 2", len(x.h.Panes))
	}
	x.h.Pane("w5:p9").Cwd, x.h.Pane("w5:p9").Workspace = "/elsewhere/other", "w5"
	x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = lane, "wA"
	x.env = []string{"HERDR_PANE_ID=w5:p9", "HERDR_WORKSPACE_ID=w5"}

	out := x.ok(t, "pane", "O-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "pane.split cwd="+lane+" direction=right env={OBOR_AGENT=pj-o-01,OBOR_LANE=O-01,OBOR_ROLE=worker} target_pane_id=wA:p1",
		"the project's pane")
	hasNot(t, calls, "target_pane_id=w5:p9", "never the caller's, another project's")
	hasNot(t, out, "warning:", "a project workspace was found")

	x.noAgents()
	x.h.Pane("wA:n1").Label = ""
	x.reset()
	x.ok(t, "pane", "O-01", "--new-tab", "workers")
	calls = strings.Join(x.reqs(), "\n")
	has(t, calls, "label=workers", "precondition: the new tab was made")
	has(t, calls, "workspace_id=wA", "a new tab in the project's workspace")
	hasNot(t, calls, "workspace_id=w5", "never the caller's workspace")
}

// Several workspaces hold project panes: the one with most of them is the project's.
func TestPlacementPicksTheProjectsBusiestWorkspace(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "O-01", "opus")
	lane := x.LaneDir("O-01")
	x.h.Panes = nil
	x.h.Tab("wA:t1", 200, 50, "wA:p1")
	x.h.Tab("wB:t2", 200, 50, "wB:p1", "wB:p2")
	x.h.Tab("w5:t9", 200, 50, "w5:p9")
	for id, v := range map[string][2]string{
		"wA:p1": {"wA", lane}, "wB:p1": {"wB", lane}, "wB:p2": {"wB", x.Root}, "w5:p9": {"w5", "/elsewhere"},
	} {
		p := x.h.Pane(id)
		p.Workspace, p.Cwd = v[0], v[1]
	}
	if n := len(x.h.Panes); n != 4 {
		t.Fatalf("precondition: %d panes, want the 4 scripted", n)
	}
	x.env = []string{"HERDR_PANE_ID=w5:p9", "HERDR_WORKSPACE_ID=w5"}

	x.ok(t, "pane", "O-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "target_pane_id=wB:p2", "the largest pane of wB's tab (two of its panes, wA one) — the last of equals")
	hasNot(t, calls, "target_pane_id=wA:p1", "not the workspace with one pane")
	hasNot(t, calls, "target_pane_id=w5:p9", "not the caller's")
}

// No pane works in the project yet: the caller's workspace, with one warning line naming the root.
func TestPlacementFallsBackToTheCallerWithAWarning(t *testing.T) {
	t.Parallel()
	x := newFx(t) // its panes have no cwd: no herdr workspace is the project's
	x.ok(t, "lane", "O-01", "opus")
	x.env = []string{"HERDR_PANE_ID=w1:q1", "HERDR_WORKSPACE_ID=w1"}

	out := x.ok(t, "pane", "O-01")
	has(t, out, "warning: no herdr workspace has a pane in "+x.Root+": using the caller's (w1)", "the warning")
	has(t, strings.Join(x.reqs(), "\n"), "target_pane_id=w1:q1", "the caller's tab")

	x.noAgents()
	x.h.Pane("w1:n1").Label = ""
	x.reset()
	x.ok(t, "pane", "O-01", "--new-tab", "workers")
	has(t, strings.Join(x.reqs(), "\n"), "workspace_id=w1", "the caller's workspace")
}

// The caller's tab holds another project's lead, in this project's own workspace: that pane is never split, and the
// project's other tab is the one used (T-628).
func TestPlacementSkipsAnotherProjectsLeadTab(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "O-01", "opus")
	lane := x.LaneDir("O-01")
	x.h.Panes = nil
	x.h.Tab("wA:t1", 200, 50, "wA:p1") // the project's: one pane
	x.h.Tab("wA:t9", 80, 50, "wA:p9")  // the caller's: another project's lead
	x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = lane, "wA"
	x.h.Pane("wA:p9").Cwd, x.h.Pane("wA:p9").Workspace = "/elsewhere/other", "wA"
	x.env = []string{"HERDR_PANE_ID=wA:p9", "HERDR_WORKSPACE_ID=wA"}

	x.ok(t, "pane", "O-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "target_pane_id=wA:p1", "the project's own tab")
	hasNot(t, calls, "target_pane_id=wA:p9", "never another project's lead pane")
}

// A pane a new tab or a split makes is not an available shell at once (live 2026-10-31: "agent target pane w5:p10 is
// not an available shell (agent_pane_busy)"): the start waits for herdr to report a shell there, within the bound.
func TestNewPaneShellIsWaitedFor(t *testing.T) {
	t.Parallel()
	t.Run("busy 2 s: the start waits and succeeds", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "T-01", "luna")
		x.h.NewPaneBusy = 2 * time.Second
		begin := time.Now()
		out := x.ok(t, "pane", "T-01", "--new-tab", "workers")
		if d := time.Since(begin); d < x.h.NewPaneBusy {
			t.Errorf("started after %s, before the pane's shell was up (%s)", d, x.h.NewPaneBusy)
		}
		if last(out) != "OK T-01: pj-t-01 in pane w1:n1" {
			t.Errorf("last line %q", last(out))
		}
		if p := x.h.Pane("w1:n1"); p.Kind != "codex" || p.Name != "pj-t-01" {
			t.Errorf("the agent in the new pane: %+v", p)
		}
	})
	t.Run("busy 15 s: FAIL with the hint", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "T-01", "luna")
		x.h.NewPaneBusy = 15 * time.Second
		// The 10 s bound would make this test take 10 s: the same comparison (busy outlasts the bound), shorter.
		x.shellWait = 300 * time.Millisecond
		x.failing(t, "FAIL pane w1:n1 is not an available shell after 300ms: start the agent there by hand "+
			"(herdr agent start pj-t-01 --kind codex --pane w1:n1)", "pane", "T-01", "--new-tab", "workers")
		if p := x.h.Pane("w1:n1"); p.Kind != "" {
			t.Errorf("no agent started: %+v", p)
		}
	})
}
