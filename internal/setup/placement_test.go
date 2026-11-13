package setup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// A run's panes go to the project's herdr workspace — the one whose panes work in the project — never to the workspace
// of the pane obor was run from when that is another project's (live: axis-maps' panes landed in obor-kit's w5).
func TestPlacementFollowsTheProjectsGroup(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	lane := x.LaneDir("L-01")
	x.h.Panes = nil
	x.h.Tab("w5:t9", 200, 50, "w5:p9") // the caller's: another project's pane
	x.h.Tab("wA:t1", 200, 50, "wA:p1") // this project's
	if len(x.h.Panes) != 2 {
		t.Fatalf("precondition: %d scripted panes, want 2", len(x.h.Panes))
	}
	x.h.Pane("w5:p9").Cwd = "/elsewhere/other"
	x.h.Pane("wA:p1").Cwd = lane
	x.env = []string{"OBOR_PANE=w5:p9", "HERDR_WORKSPACE_ID=w5"}

	out := x.ok(t, "pane", "--start", "L-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+lane+" env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_LANE_CPUS=2,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=worker,OPENCODE_CONFIG=,OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=} focus=false label=lanes workspace_id=wA",
		"a lanes tab in the project's workspace")
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

// Placement table: with no pane of the project anywhere, a workspace of its own is made (never the caller's,
// maybe another project's); only a tab whose every pane works in the project is picked; an unknown cwd may be another
// project's; several workspaces: the one with most project panes; a user's own --tab into a mixed tab is honoured.
func TestPlacementTable(t *testing.T) {
	t.Parallel()
	t.Run("no project pane, caller in another project's workspace: a new workspace, both lanes there", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.lane(t, "L-02")
		x.h.Panes = nil
		x.h.Tab("w5:t9", 200, 50, "w5:p9")
		x.h.Pane("w5:p9").Cwd = "/elsewhere/other"
		x.env = []string{"OBOR_PANE=w5:p9", "HERDR_WORKSPACE_ID=w5"}
		if len(x.h.Panes) != 1 {
			t.Fatalf("precondition: %d panes, want 1 (no workspace of the project)", len(x.h.Panes))
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
			if at == nil || !strings.HasPrefix(at.ID, "wn1:") {
				t.Errorf("%s: pane %+v, want one in the new workspace wn1", name, at)
			}
		}
	})
	t.Run("a project pane in wA: wA, no workspace made", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.h.Panes = nil
		x.h.Tab("w5:t9", 200, 50, "w5:p9")
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Pane("w5:p9").Cwd = "/elsewhere/other"
		x.h.Pane("wA:p1").Cwd = x.Root
		x.env = []string{"OBOR_PANE=w5:p9", "HERDR_WORKSPACE_ID=w5"}

		out := x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "label=workers workspace_id=wA", "the new tab in wA")
		hasNot(t, calls, "workspace.create", "no workspace made")
		hasNot(t, out, "created for", "no workspace made")
	})
	t.Run("one project pane and two unknown-cwd panes: that tab is not split, a new tab", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		lane := x.LaneDir("L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1", "wA:p2", "wA:p3")
		x.h.Pane("wA:p1").Cwd = lane
		if x.h.Pane("wA:p2").Cwd != "" || x.h.Pane("wA:p3").Cwd != "" {
			t.Fatal("precondition: two panes with an unknown cwd")
		}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		hasNot(t, calls, "pane.split", "the mixed tab is not split")
		has(t, calls, "label=lanes workspace_id=wA", "a new tab in the project's workspace")
		hasNot(t, calls, "workspace.create", "wA is the project's")
	})
	t.Run("caller cd'd into the root in another project's workspace: a new workspace (T-634)", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.h.Panes = nil
		x.h.Tab("w5:t1", 200, 50, "w5:p1", "w5:p2") // another project's lead, and the caller cd'd into this root
		x.h.Tab("w5:t2", 200, 50, "w5:p3")          // another of that project's panes
		for _, id := range []string{"w5:p1", "w5:p2", "w5:p3"} {
			x.h.Pane(id).Cwd = "/elsewhere/other"
		}
		x.h.Pane("w5:p2").Cwd = x.Root
		x.env = []string{"OBOR_PANE=w5:p2", "HERDR_WORKSPACE_ID=w5"}
		if len(x.h.Panes) != 3 {
			t.Fatalf("precondition: %d panes, want 3 (all another project's)", len(x.h.Panes))
		}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "workspace.create cwd="+x.Root+" focus=false label=proj", "a workspace of its own")
		has(t, calls, "label=lanes workspace_id=wn1", "the pane in a new tab of it")
		hasNot(t, calls, "workspace_id=w5", "never the foreign workspace")
		hasNot(t, calls, "pane.split", "no pane of w5 is split")
	})
	t.Run("only project panes and unknown-cwd ones: that workspace (T-634)", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Tab("wA:t2", 200, 50, "wA:q1") // unknown cwd
		x.h.Tab("w5:t9", 200, 50, "w5:p9") // another project's, the caller
		x.h.Pane("wA:p1").Cwd = x.Root
		x.h.Pane("w5:p9").Cwd = "/elsewhere/other"
		x.env = []string{"OBOR_PANE=w5:p9", "HERDR_WORKSPACE_ID=w5"}
		if x.h.Pane("wA:q1").Cwd != "" {
			t.Fatal("precondition: wA:q1 has an unknown cwd")
		}

		x.ok(t, "pane", "--start", "L-01", "--new-tab", "workers")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "label=workers workspace_id=wA", "the new tab in wA")
		hasNot(t, calls, "workspace.create", "wA is the project's: no workspace made")
	})
	t.Run("caller with an unknown cwd: a new lanes tab in the project's workspace, not the caller's", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Tab("wA:t2", 200, 50, "wA:q1") // the caller's
		x.h.Pane("wA:p1").Cwd = x.Root
		x.env = []string{"OBOR_PANE=wA:q1", "HERDR_WORKSPACE_ID=wA"}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "label=lanes workspace_id=wA", "a new lanes tab in the project's workspace (wA:t1 holds a shell)")
		hasNot(t, calls, "target_pane_id=wA:q1", "not the caller's tab")
	})
	t.Run("several workspaces hold project panes: the one with most is the project's", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		lane := x.LaneDir("L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1")
		x.h.Tab("wB:t2", 200, 50, "wB:p1", "wB:p2")
		x.h.Tab("w5:t9", 200, 50, "w5:p9")
		for id, cwd := range map[string]string{"wA:p1": lane, "wB:p1": lane, "wB:p2": x.Root, "w5:p9": "/elsewhere"} {
			x.h.Pane(id).Cwd = cwd
		}
		if n := len(x.h.Panes); n != 4 {
			t.Fatalf("precondition: %d panes, want the 4 scripted", n)
		}
		x.env = []string{"OBOR_PANE=w5:p9", "HERDR_WORKSPACE_ID=w5"}

		x.ok(t, "pane", "--start", "L-01")
		calls := strings.Join(x.reqs(), "\n")
		has(t, calls, "label=lanes workspace_id=wB", "a new lanes tab in wB (two of its panes, wA one)")
		hasNot(t, calls, "target_pane_id=wA:p1", "not the workspace with one pane")
		hasNot(t, calls, "target_pane_id=w5:p9", "not the caller's")
	})
	t.Run("an explicit --tab into a mixed tab (a project pane, one with an unknown cwd): a split there", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.h.Panes = nil
		x.h.Tab("wA:t1", 200, 50, "wA:p1", "wA:p2")
		x.h.Tab("wA:t2", 200, 50, "wA:p3")
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
// and the workspace is not the project's (another project's pane there): a workspace of its own.
func TestPlacementSkipsAnotherProjectsLeadTab(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	lane := x.LaneDir("L-01")
	x.h.Panes = nil
	x.h.Tab("wA:t1", 200, 50, "wA:p1") // the project's: one pane
	x.h.Tab("wA:t9", 80, 50, "wA:p9")  // the caller's: another project's lead
	x.h.Pane("wA:p1").Cwd = lane
	x.h.Pane("wA:p9").Cwd = "/elsewhere/other"
	x.env = []string{"OBOR_PANE=wA:p9", "HERDR_WORKSPACE_ID=wA"}

	x.ok(t, "pane", "--start", "L-01")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "workspace.create cwd="+x.Root+" focus=false label=proj", "wA is mixed: a workspace of its own")
	has(t, calls, "label=lanes workspace_id=wn1", "the pane in a new tab there")
	hasNot(t, calls, "workspace_id=wA", "nothing new in the mixed workspace")
	hasNot(t, calls, "target_pane_id=wA:p9", "never another project's lead pane")
}

// A pane a new tab or a split makes is not an available shell at once (live 2026-10-31: "agent target pane w5:p10 is
// not an available shell (agent_pane_busy)"): the start waits for herdr to report a shell there, within the bound.
// setup's direct starter entry point into launch's one start (its shell wait and busy retry, the case table in
// internal/launch); past setup's bound, setup's own refusal.
func TestNewPaneShellIsWaitedFor(t *testing.T) {
	t.Parallel()
	t.Run("busy 0.3 s: the start waits and succeeds", func(t *testing.T) {
		t.Parallel()
		x := newFx(t)
		x.lane(t, "L-01")
		x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
		x.h.NewPaneBusy = 300 * time.Millisecond // three of the start's 100 ms looks: a wait, not a real 2 s
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
		x.lane(t, "L-01")
		x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
		x.h.NewPaneBusy = 15 * time.Second
		// The 10 s bound would make this test take 10 s: the same comparison (busy outlasts the bound), shorter.
		x.shellWait = 300 * time.Millisecond
		x.failing(t, "FAIL pane w1:n1 is not an available shell after 300ms: start the agent there with "+
			"obor restart pj-l-01 --fresh --pane w1:n1", "pane", "--start", "L-01", "--new-tab", "workers")
		if p := x.h.Pane("w1:n1"); p.Kind != "" {
			t.Errorf("no agent started: %+v", p)
		}
	})
}

// inTab is the panes of tab in the fake herdr.
func (x *fx) inTab(tab string) []*herdrtest.Pane {
	var out []*herdrtest.Pane
	for _, p := range x.h.Panes {
		if p.Tab == tab {
			out = append(out, p)
		}
	}
	return out
}

// tabOf is the tab of the pane labelled or named name ("" when none).
func (x *fx) tabOf(name string) string {
	for _, p := range x.h.Panes {
		if p.Label == name || p.Name == name {
			return p.Tab
		}
	}
	return ""
}

// kindFx is a project of five lanes and the lead, integrator, reviewer, advisor and a custom designer role, called
// from the lead's pane w1:p1 (alone in its tab w1:lead), with a shell tab w1:sh beside it; the fake numbers its new tabs w1:t1 …; lanes and roles
// add tabs of panes labelled with those agents' names: tab → labels.
func kindFx(t *testing.T, tabs map[string][]string) *fx {
	t.Helper()
	x := newFx(t)
	x.Write("docs/agents/designer.md", "# Role: designer\n")
	x.Config(testkit.Set("agents.roles", `{"lead": "opus", "integrator": "opus", "reviewer": "opus", "advisor": "opus", `+
		`"designer": "opus"}`))
	for _, l := range []string{"L-01", "L-02", "L-03", "L-04", "L-05"} {
		x.lane(t, l)
	}
	x.h.Panes = nil
	x.h.Tab("w1:lead", 200, 50, "w1:p1")
	x.h.Tab("w1:sh", 200, 50, "w1:q1")
	x.h.Pane("w1:p1").Cwd, x.h.Pane("w1:p1").Label = x.Root, "pj-lead"
	x.h.Pane("w1:q1").Cwd = x.Root
	for tab, labels := range tabs {
		for i, label := range labels {
			id := fmt.Sprintf("%s-%d", tab, i)
			x.h.Tab(tab, 200, 50, id)
			cwd := x.Root
			if strings.HasPrefix(label, "pj-l-") {
				cwd = x.LaneDir(strings.ToUpper(strings.TrimPrefix(label, "pj-")))
			}
			x.h.Pane(id).Cwd, x.h.Pane(id).Label = cwd, label
		}
	}
	x.env = []string{"OBOR_PANE=w1:p1", "HERDR_WORKSPACE_ID=w1"}
	if x.tabOf("pj-lead") != "w1:lead" || len(x.inTab("w1:lead")) != 1 {
		t.Fatal("precondition: the lead alone in its tab w1:lead")
	}
	x.reset()
	return x
}

// Placement by kind, with no flag: a lane's pane (a drive agent's too) goes to a project tab whose panes are all lane
// panes and which has room, else a new tab "lanes"; a role's pane to a tab of role panes with room, else a new tab
// "roles"; a tab with a shell pane is no kind's; the lead's tab is never split, not even by --tab.
func TestPlacementByKind(t *testing.T) {
	t.Parallel()
	lanes := []string{"pj-l-01", "pj-l-02"}
	for _, c := range []struct {
		name  string
		tabs  map[string][]string
		args  []string // obor setup's args; nil: obor drive of L-03
		split string   // the tab split ("" none)
		tab   string   // the new tab's label ("" none)
	}{
		{"setup pane L-03, a lanes tab with 2 lanes: split into it", map[string][]string{"w1:x3": lanes},
			[]string{"pane", "--start", "L-03"}, "w1:x3", ""},
		{"setup pane L-03, the lanes tab full: a new tab lanes",
			map[string][]string{"w1:x3": {"pj-l-01", "pj-l-02", "pj-l-04", "pj-l-05"}},
			[]string{"pane", "--start", "L-03"}, "", "lanes"},
		{"setup role designer, a roles tab with 3 roles: split into it",
			map[string][]string{"w1:x3": lanes, "w1:x4": {"pj-int", "pj-rev", "pj-advisor"}},
			[]string{"role", "designer", "--start"}, "w1:x4", ""},
		{"setup role designer, no roles tab: a new tab roles", map[string][]string{"w1:x3": lanes},
			[]string{"role", "designer", "--start"}, "", "roles"},
		{"obor drive, a lanes tab: split into it, not the lead's", map[string][]string{"w1:x3": lanes},
			nil, "w1:x3", ""},
		{"a tab with a shell pane and lanes: not a lanes tab", map[string][]string{"w1:x3": {"pj-l-01", "pj-l-02", ""}},
			[]string{"pane", "--start", "L-03"}, "", "lanes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := kindFx(t, c.tabs)
			agent := "pj-l-03"
			if c.args == nil {
				agent = "pj-l-03-drive"
				s, out := x.newSetup(t)
				s.Dir = x.Root
				if err := s.StartDrive(context.Background(), Drive{Task: "T-1", Agent: agent, Lane: "L-03",
					Cwd: x.LaneDir("L-03"), Prompt: "drive T-1"}); err != nil {
					t.Fatalf("StartDrive: %v\n%s", err, out)
				}
			} else {
				if c.args[0] == "role" {
					agent = "pj-" + c.args[1]
				}
				x.ok(t, c.args...)
			}
			calls := strings.Join(x.reqs(), "\n")
			hasNot(t, calls, "target_pane_id=w1:p1", "the lead's pane is never split")
			if n := len(x.inTab("w1:lead")); n != 1 {
				t.Errorf("the lead's tab holds %d panes, want the lead alone", n)
			}
			got := x.tabOf(agent)
			if c.split != "" {
				hasNot(t, calls, "tab.create", "no new tab")
				if got != c.split {
					t.Errorf("%s in tab %q, want %s", agent, got, c.split)
				}
				return
			}
			has(t, calls, "focus=false label="+c.tab+" workspace_id=w1", "a new tab "+c.tab+" in the project's workspace")
			hasNot(t, calls, "pane.split", "no split")
			if got == "" || c.tabs[got] != nil || got == "w1:lead" || got == "w1:sh" {
				t.Errorf("%s in tab %q, want the new tab", agent, got)
			}
		})
	}
	t.Run("--tab <the lead's tab>: refused, naming --new-tab", func(t *testing.T) {
		t.Parallel()
		x := kindFx(t, map[string][]string{"w1:x3": lanes})
		x.failing(t, "FAIL tab w1:lead holds the lead's pane, which keeps its tab alone: pass --new-tab <label> (or --pane <id>)",
			"pane", "--start", "L-03", "--tab", "w1:lead")
		calls := strings.Join(x.reqs(), "\n")
		for _, m := range []string{"pane.split", "tab.create", "agent.start"} {
			hasNot(t, calls, m, "refused: no "+m)
		}
	})
}

// obor init from a caller's pane puts the lanes in a tab "lanes" and the integrator and the reviewer in a tab "roles";
// the caller's tab is not split.
func TestOborInitPlacesByKind(t *testing.T) {
	t.Parallel()
	x := newFakeGitInitFx(t, true)
	x.h.Tab("w1:caller", 200, 50, "w1:p9")
	x.h.Pane("w1:p9").Cwd = x.Root
	x.env = append(x.env, "OBOR_PANE=w1:p9", "HERDR_WORKSPACE_ID=w1")
	if len(x.inTab("w1:caller")) != 1 {
		t.Fatal("precondition: the caller alone in its tab")
	}
	x.init(t)
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "target_pane_id=w1:p9", "the caller's pane is not split")
	if n := len(x.inTab("w1:caller")); n != 1 {
		t.Errorf("the caller's tab holds %d panes, want the caller alone", n)
	}
	lanes, roles := x.tabOf("proj-l-01"), x.tabOf("proj-int")
	if lanes == "" || x.tabOf("proj-l-02") != lanes || x.h.TabLabels[lanes] != "lanes" {
		t.Errorf("L-01 in %q, L-02 in %q (labelled %q), want both in a tab lanes", lanes, x.tabOf("proj-l-02"),
			x.h.TabLabels[lanes])
	}
	if roles == "" || roles == lanes || x.tabOf("proj-rev") != roles || x.h.TabLabels[roles] != "roles" {
		t.Errorf("integrator in %q, reviewer in %q (labelled %q), want both in a tab roles", roles, x.tabOf("proj-rev"),
			x.h.TabLabels[roles])
	}
}
