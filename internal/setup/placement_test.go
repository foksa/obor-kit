package setup

import (
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
)

// A run's panes go to the project's herdr workspace — the one whose panes work in the project — never to the workspace
// of the pane obor was run from when that is another project's (T-628, live: axis-maps' panes landed in obor-kit's w5).
func TestPlacementFollowsTheProjectsWorkspace(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	lane := x.LaneDir("L-01")
	x.h.Panes = nil
	x.h.Tab("w5:t9", 200, 50, "w5:p9") // the caller's: another project's pane
	x.h.Tab("wA:t1", 200, 50, "wA:p1") // this project's
	if len(x.h.Panes) != 2 {
		t.Fatalf("precondition: %d scripted panes, want 2", len(x.h.Panes))
	}
	x.h.Pane("w5:p9").Cwd, x.h.Pane("w5:p9").Workspace = "/elsewhere/other", "w5"
	x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = lane, "wA"
	x.env = []string{"HERDR_PANE_ID=w5:p9", "HERDR_WORKSPACE_ID=w5"}

	out := x.ok(t, "pane", "--start", "L-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "pane.split cwd="+lane+" direction=right env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_OPENCODE_MODEL=,OBOR_ROLE=worker,OPENCODE_CONFIG=} target_pane_id=wA:p1",
		"the project's pane")
	hasNot(t, calls, "target_pane_id=w5:p9", "never the caller's, another project's")
	hasNot(t, out, "warning:", "a project workspace was found")

	x.noAgents()
	x.h.Pane("wA:n1").Label = ""
	x.reset()
	x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
	calls = strings.Join(x.reqs(), "\n")
	has(t, calls, "label=workers", "precondition: the new tab was made")
	has(t, calls, "workspace_id=wA", "a new tab in the project's workspace")
	hasNot(t, calls, "workspace_id=w5", "never the caller's workspace")
}

// Placement table (T-632): with no pane of the project anywhere, a workspace of its own is made (never the caller's,
// maybe another project's); only a tab whose every pane works in the project is picked; an unknown cwd may be another
// project's; several workspaces: the one with most project panes; a user's own --tab into a mixed tab is honoured (T-634).
func TestPlacementTable(t *testing.T) {
	t.Parallel()
	t.Run("no project pane, caller in another project's workspace: a new workspace, both lanes there", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.ok(t, "lane", "L-02")
		x.h.Panes = nil
		x.h.Tab("w5:t9", 200, 50, "w5:p9")
		x.h.Pane("w5:p9").Cwd, x.h.Pane("w5:p9").Workspace = "/elsewhere/other", "w5"
		x.env = []string{"HERDR_PANE_ID=w5:p9", "HERDR_WORKSPACE_ID=w5"}
		if len(x.h.Panes) != 1 || len(x.h.Workspaces) != 0 {
			t.Fatalf("precondition: %d panes, %d workspaces; want 1, 0", len(x.h.Panes), len(x.h.Workspaces))
		}

		out := x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
		out += x.ok(t, "pane", "--start", "L-02")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "workspace.create cwd="+x.Root+" focus=false label=proj", "a workspace named after the project, in its root")
		if n := strings.Count(calls, "workspace.create"); n != 1 {
			t.Errorf("%d workspace.create calls, want 1 (the second lane finds the first's workspace)", n)
		}
		has(t, out, "workspace wn1 created for proj", "the log line")
		hasNot(t, calls, "workspace_id=w5", "never the caller's workspace")
		hasNot(t, calls, "target_pane_id=w5:p9", "never the caller's pane")
		for _, name := range []string{"pj-l-01", "pj-l-02"} {
			var at *herdrtest.Pane
			for _, p := range x.h.Panes {
				if p.Name == name {
					at = p
				}
			}
			if at == nil || at.Workspace != "wn1" {
				t.Errorf("%s: pane %+v, want one in the new workspace wn1", name, at)
			}
		}
	})
	t.Run("a project pane in wA: wA, no workspace made", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.h.Panes = nil
		x.h.Tab("w5:t9", 200, 50, "w5:p9")
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Pane("w5:p9").Cwd, x.h.Pane("w5:p9").Workspace = "/elsewhere/other", "w5"
		x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = x.Root, "wA"
		x.env = []string{"HERDR_PANE_ID=w5:p9", "HERDR_WORKSPACE_ID=w5"}

		out := x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "label=workers workspace_id=wA", "the new tab in wA")
		hasNot(t, calls, "workspace.create", "no workspace made")
		hasNot(t, out, "created for", "no workspace made")
	})
	t.Run("one project pane and two unknown-cwd panes: that tab is not split, a new tab", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		lane := x.LaneDir("L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1", "wA:p2", "wA:p3")
		for _, id := range []string{"wA:p1", "wA:p2", "wA:p3"} {
			x.h.Pane(id).Workspace = "wA"
		}
		x.h.Pane("wA:p1").Cwd = lane
		if x.h.Pane("wA:p2").Cwd != "" || x.h.Pane("wA:p3").Cwd != "" {
			t.Fatal("precondition: two panes with an unknown cwd")
		}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		hasNot(t, calls, "pane.split", "the mixed tab is not split")
		has(t, calls, "label=workers workspace_id=wA", "a new tab in the project's workspace")
		hasNot(t, calls, "workspace.create", "wA is the project's")
	})
	t.Run("caller cd'd into the root in another project's workspace: a new workspace (T-634)", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.h.Panes = nil
		x.h.Tab("w5:t1", 200, 50, "w5:p1", "w5:p2") // another project's lead, and the caller cd'd into this root
		x.h.Tab("w5:t2", 200, 50, "w5:p3")          // another of that project's panes
		for _, id := range []string{"w5:p1", "w5:p2", "w5:p3"} {
			x.h.Pane(id).Workspace, x.h.Pane(id).Cwd = "w5", "/elsewhere/other"
		}
		x.h.Pane("w5:p2").Cwd = x.Root
		x.env = []string{"HERDR_PANE_ID=w5:p2", "HERDR_WORKSPACE_ID=w5"}
		if len(x.h.Panes) != 3 || len(x.h.Workspaces) != 0 {
			t.Fatalf("precondition: %d panes, %d workspaces; want 3, 0", len(x.h.Panes), len(x.h.Workspaces))
		}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "workspace.create cwd="+x.Root+" focus=false label=proj", "a workspace of its own")
		has(t, calls, "label=workers workspace_id=wn1", "the pane in a new tab of it")
		hasNot(t, calls, "workspace_id=w5", "never the foreign workspace")
		hasNot(t, calls, "pane.split", "no pane of w5 is split")
	})
	t.Run("only project panes and unknown-cwd ones: that workspace (T-634)", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Tab("wA:t2", 200, 50, "wA:q1") // unknown cwd
		x.h.Tab("w5:t9", 200, 50, "w5:p9") // another project's, the caller
		x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = x.Root, "wA"
		x.h.Pane("wA:q1").Workspace = "wA"
		x.h.Pane("w5:p9").Cwd, x.h.Pane("w5:p9").Workspace = "/elsewhere/other", "w5"
		x.env = []string{"HERDR_PANE_ID=w5:p9", "HERDR_WORKSPACE_ID=w5"}
		if x.h.Pane("wA:q1").Cwd != "" {
			t.Fatal("precondition: wA:q1 has an unknown cwd")
		}

		x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "label=workers workspace_id=wA", "the new tab in wA")
		hasNot(t, calls, "workspace.create", "wA is the project's: no workspace made")
	})
	t.Run("caller with an unknown cwd: not its tab", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Tab("wA:t2", 200, 50, "wA:q1") // the caller's
		x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = x.Root, "wA"
		x.h.Pane("wA:q1").Workspace = "wA"
		x.env = []string{"HERDR_PANE_ID=wA:q1", "HERDR_WORKSPACE_ID=wA"}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "target_pane_id=wA:p1", "the project's tab")
		hasNot(t, calls, "target_pane_id=wA:q1", "not the caller's tab")
	})
	t.Run("several workspaces hold project panes: the one with most is the project's", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		lane := x.LaneDir("L-01")
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

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "target_pane_id=wB:p2", "the largest pane of wB's tab (two of its panes, wA one) — the last of equals")
		hasNot(t, calls, "target_pane_id=wA:p1", "not the workspace with one pane")
		hasNot(t, calls, "target_pane_id=w5:p9", "not the caller's")
	})
	t.Run("an explicit --tab into a mixed tab (a project pane, one with an unknown cwd): a split there", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1", "wA:p2")
		x.h.Tab("wA:t2", 200, 50, "wA:p3")
		for _, id := range []string{"wA:p1", "wA:p2", "wA:p3"} {
			x.h.Pane(id).Workspace = "wA"
		}
		x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p3").Cwd = x.Root, x.Root
		if x.h.Pane("wA:p2").Cwd != "" {
			t.Fatal("precondition: wA:p2 has an unknown cwd")
		}

		x.ok(t, "pane", "--start", "L-01", "--tab", "wA:t1")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "pane.split", "a split of the named tab")
		hasNot(t, calls, "target_pane_id=wA:p3", "not the other tab")
		hasNot(t, calls, "tab.create", "no new tab")
		var in string
		for _, p := range x.h.Panes {
			if p.Name == "pj-l-01" {
				in = p.Tab
			}
		}
		if in != "wA:t1" {
			t.Errorf("L-01 in tab %q, want the named wA:t1", in)
		}
	})
}

// The caller's tab holds another project's lead, in the workspace of this project's pane: that pane is never split
// (T-628), and the workspace is not the project's (T-634: another project's pane there): a workspace of its own.
func TestPlacementSkipsAnotherProjectsLeadTab(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.ok(t, "lane", "L-01")
	lane := x.LaneDir("L-01")
	x.h.Panes = nil
	x.h.Tab("wA:t1", 200, 50, "wA:p1") // the project's: one pane
	x.h.Tab("wA:t9", 80, 50, "wA:p9")  // the caller's: another project's lead
	x.h.Pane("wA:p1").Cwd, x.h.Pane("wA:p1").Workspace = lane, "wA"
	x.h.Pane("wA:p9").Cwd, x.h.Pane("wA:p9").Workspace = "/elsewhere/other", "wA"
	x.env = []string{"HERDR_PANE_ID=wA:p9", "HERDR_WORKSPACE_ID=wA"}

	x.ok(t, "pane", "--start", "L-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "workspace.create cwd="+x.Root+" focus=false label=proj", "wA is mixed: a workspace of its own")
	has(t, calls, "label=workers workspace_id=wn1", "the pane in a new tab there")
	hasNot(t, calls, "workspace_id=wA", "nothing new in the mixed workspace")
	hasNot(t, calls, "target_pane_id=wA:p9", "never another project's lead pane")
}

// A pane a new tab or a split makes is not an available shell at once (live 2026-10-31: "agent target pane w5:p10 is
// not an available shell (agent_pane_busy)"): the start waits for herdr to report a shell there, within the bound.
func TestNewPaneShellIsWaitedFor(t *testing.T) {
	t.Parallel()
	t.Run("busy 2 s: the start waits and succeeds", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
		x.h.NewPaneBusy = 2 * time.Second
		begin := time.Now()
		out := x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
		if d := time.Since(begin); d < x.h.NewPaneBusy {
			t.Errorf("started after %s, before the pane's shell was up (%s)", d, x.h.NewPaneBusy)
		}
		if last(out) != "OK L-01: pj-l-01 in pane w1:n1" {
			t.Errorf("last line %q", last(out))
		}
		if p := x.h.Pane("w1:n1"); p.Kind != "codex" || p.Name != "pj-l-01" {
			t.Errorf("the agent in the new pane: %+v", p)
		}
	})
	t.Run("busy 15 s: FAIL with the hint", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.ok(t, "lane", "L-01")
		x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
		x.h.NewPaneBusy = 15 * time.Second
		// The 10 s bound would make this test take 10 s: the same comparison (busy outlasts the bound), shorter.
		x.shellWait = 300 * time.Millisecond
		x.failing(t, "FAIL pane w1:n1 is not an available shell after 300ms: start the agent there by hand "+
			"(herdr agent start pj-l-01 --kind codex --pane w1:n1)", "pane", "--start", "L-01", "--new-tab", "workers")
		if p := x.h.Pane("w1:n1"); p.Kind != "" {
			t.Errorf("no agent started: %+v", p)
		}
	})
}
