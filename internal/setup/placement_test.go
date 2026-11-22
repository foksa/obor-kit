package setup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
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
	has(t, calls, "tab.create cwd="+lane+" env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_LANE_CPUS=2,OBOR_OPENCODE_MODEL=,OBOR_OPENCODE_RESUME=,OBOR_PANE=,OBOR_ROLE=worker,OPENCODE_CONFIG=,OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=} focus=false label=lanes workspace_id=wA",
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

// serve places a lane's missing pane at a worker's start (PlaceLane): obor setup pane <LANE>'s own placement, a pane in
// the lane's worktree labelled the lane's agent, no agent started in it (the start that wanted it runs there).
func TestPlaceLanePlacesALanePane(t *testing.T) {
	t.Parallel()
	x := kindFx(t, map[string][]string{"w1:x3": {"pj-l-01"}})
	x.lane(t, "L-03")
	s, out := x.newSetup(t)
	s.Dir = x.Root
	x.reset()
	pane, err := s.PlaceLane(context.Background(), "L-03")
	if err != nil {
		t.Fatalf("PlaceLane: %v\n%s", err, out)
	}
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "agent.start", "no agent started: the start that wanted the pane runs there")
	if p := x.h.Pane(pane); p == nil || p.Cwd != x.LaneDir("L-03") || p.Label != "pj-l-03" {
		t.Errorf("placed pane %s = %+v, want the lane's worktree and the label pj-l-03", pane, p)
	}
	if got := x.tabOf("pj-l-03"); got != "w1:x3" {
		t.Errorf("pj-l-03 in tab %q, want the lanes tab w1:x3", got)
	}
	has(t, out.String(), "pane "+pane+" placed in lane L-03 for pj-l-03", "the placement said")

	_, err = s.PlaceLane(context.Background(), "L-09")
	if want := "unknown lane L-09 (lanes: 5; obor setup lane L-09 first)"; err == nil || err.Error() != want {
		t.Errorf("PlaceLane of an unknown lane: %v, want %q", err, want)
	}
}

// placeSpy is a terminal that records each placement it is asked for and places it on the terminal it wraps.
type placeSpy struct {
	port.TerminalSetup
	got []port.Placement
}

func (s *placeSpy) Place(ctx context.Context, p port.Placement) (string, error) {
	s.got = append(s.got, p)
	return s.TerminalSetup.Place(ctx, p)
}

// A placement says Starts only when a start follows that types the env line itself (--start): a pane without --start,
// and serve's PlaceLane (its worker's start comes in a later pass), is the terminal's to ready, so the pane's cwd is
// set once either way.
func TestPlacementStartsOnlyWithAStart(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	x.lane(t, "L-02")
	x.lane(t, "L-03")
	run := func(args ...string) []port.Placement {
		t.Helper()
		s, out := x.newSetup(t)
		s.Dir = x.Root
		spy := &placeSpy{TerminalSetup: s.T}
		s.T = spy
		if rc := s.Run(context.Background(), args); rc != 0 {
			t.Fatalf("obor setup %q: exit %d\n%s", args, rc, out)
		}
		return spy.got
	}
	if got := run("pane", "L-01"); len(got) != 1 || got[0].Starts {
		t.Errorf("pane L-01 without --start placed %+v, want one placement without Starts", got)
	}
	x.noAgents()
	if got := run("pane", "--start", "L-02"); len(got) != 1 || !got[0].Starts {
		t.Errorf("pane --start L-02 placed %+v, want one placement with Starts", got)
	}
	s, out := x.newSetup(t)
	s.Dir = x.Root
	spy := &placeSpy{TerminalSetup: s.T}
	s.T = spy
	if _, err := s.PlaceLane(context.Background(), "L-03"); err != nil || len(spy.got) != 1 || spy.got[0].Starts ||
		spy.got[0].Cwd != x.LaneDir("L-03") {
		t.Errorf("PlaceLane L-03 = %v, placed %+v; want one placement in the lane's cwd without Starts\n%s", err, spy.got, out)
	}
}
