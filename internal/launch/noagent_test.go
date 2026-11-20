package launch_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/launch"
)

// noAgent runs a NoAgent launch of req (the pane with no start: obor setup's pane without --start): its result and
// what the launcher said.
func (x *fx) noAgent(req launch.Request) (port.Started, string, error) {
	x.t.Helper()
	req.NoAgent, req.NoStop = true, true
	var out bytes.Buffer
	st, err := x.launcher(&out).Launch(context.Background(), req)
	return st, out.String(), err
}

// A NoAgent launch of an agent with no pane and a placement: the placement is called once with the env of the agent's
// role and lane (the terminal types it), the pane it placed is labelled with the agent's name, and no agent is started
// (no start call, no session, no first prompt).
func TestNoAgentPlacesAndLabelsAPaneAndStartsNothing(t *testing.T) {
	t.Parallel()
	x := setup(t)
	if len(x.f.All) != 0 {
		t.Fatalf("precondition: no pane, got %d", len(x.f.All))
	}
	var pl placer
	st, out, err := x.noAgent(launch.Request{Agent: "pj-l-01", Place: pl.at(x, "w1:p9", x.lane)})
	if err != nil {
		t.Fatalf("NoAgent launch: %v\n%s", err, out)
	}
	if pl.calls != 1 {
		t.Fatalf("placement called %d times, want once", pl.calls)
	}
	for _, v := range []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01", "OBOR_AGENT="} {
		if !slices.Contains(pl.env, v) {
			t.Errorf("placement env %q, want %s in it", pl.env, v)
		}
	}
	if st.Pane != "w1:p9" || st.Idle || st.Line != "" {
		t.Errorf("result %+v, want the placed pane w1:p9, not idle, no line", st)
	}
	if l := x.f.At("w1:p9").Label; l != "pj-l-01" {
		t.Errorf("placed pane labelled %q, want pj-l-01", l)
	}
	if !strings.Contains(out, "labelled pane w1:p9 pj-l-01") {
		t.Errorf("said %q, want the label line", out)
	}
	if got := x.calls("Start"); len(got) != 0 {
		t.Errorf("an agent was started: %v", got)
	}
	if got := x.calls("Write pane=w1:p9"); len(got) != 0 {
		t.Errorf("a placed pane gets its env from the placement, not an env line of the launcher: %v", got)
	}
	if got := x.startReports(); len(got) != 0 {
		t.Errorf("a start was reported: %v", got)
	}
}

// A role's pane takes the role's env and no lane's.
func TestNoAgentRoleEnv(t *testing.T) {
	t.Parallel()
	x := setup(t)
	var pl placer
	if _, out, err := x.noAgent(launch.Request{Agent: "pj-int", Place: pl.at(x, "w1:p9", "")}); err != nil {
		t.Fatalf("NoAgent launch: %v\n%s", err, out)
	}
	if !slices.Contains(pl.env, "OBOR_ROLE=integrator") || !slices.Contains(pl.env, "OBOR_LANE=") {
		t.Errorf("placement env %q, want the integrator's role and no lane", pl.env)
	}
}

// A --pane target at its shell: its env line is typed there (the pane may hold the env of the agent that ran in it
// before), it is labelled with the agent's name, nothing is placed and no agent is started.
func TestNoAgentTypesTheEnvLineAtAnExistingPane(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p5", "", "")
	var pl placer
	st, out, err := x.noAgent(launch.Request{Agent: "pj-l-01", Pane: "w1:p5", Place: pl.at(x, "w1:p9", x.lane)})
	if err != nil {
		t.Fatalf("NoAgent launch: %v\n%s", err, out)
	}
	if st.Pane != "w1:p5" || st.Idle {
		t.Errorf("result %+v, want the target w1:p5, not idle", st)
	}
	if pl.calls != 0 {
		t.Errorf("placement called %d times, want none for a pane that is there", pl.calls)
	}
	runs := x.calls("Write pane=w1:p5")
	if len(runs) == 0 {
		t.Fatal("no env line typed at the target pane")
	}
	for _, want := range []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01", x.lane} {
		if !strings.Contains(runs[0], want) {
			t.Errorf("env line %q, want %s in it", runs[0], want)
		}
	}
	if l := x.f.At("w1:p5").Label; l != "pj-l-01" {
		t.Errorf("target labelled %q, want pj-l-01", l)
	}
	for _, line := range []string{"pane w1:p5 (existing)", "labelled pane w1:p5 pj-l-01"} {
		if !strings.Contains(out, line) {
			t.Errorf("said %q, want the line %q", out, line)
		}
	}
	if got := x.calls("Start"); len(got) != 0 {
		t.Errorf("an agent was started: %v", got)
	}
}

// The agent's pane there already (labelled with its name, at its shell) with no --pane named (a rerun): Idle, nothing
// typed, renamed or placed.
func TestNoAgentLeavesItsLabelledPaneAlone(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p3", "pj-l-01", x.lane)
	var pl placer
	st, out, err := x.noAgent(launch.Request{Agent: "pj-l-01", Place: pl.at(x, "w1:p9", x.lane)})
	if err != nil {
		t.Fatalf("NoAgent launch: %v\n%s", err, out)
	}
	if st != (port.Started{Pane: "w1:p3", Idle: true}) {
		t.Errorf("result %+v, want idle in w1:p3", st)
	}
	if pl.calls != 0 {
		t.Errorf("placement called %d times, want none", pl.calls)
	}
	for _, q := range []string{"Write", "RenamePane", "Start"} {
		if got := x.calls(q); len(got) != 0 {
			t.Errorf("a rerun made %s calls: %v", q, got)
		}
	}
}

// NoAgent refuses as a NoStop launch does, with the same texts, and touches nothing: the agent itself running (a
// *port.Running), another agent in its lane, and a pane named that runs another program.
func TestNoAgentRefusesARunningAgent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		req     launch.Request
		panes   func(x *fx)
		want    string
		running bool
	}{
		{name: "the agent itself runs", req: launch.Request{Agent: "pj-l-01"},
			panes: func(x *fx) { x.agent("w1:p3", "pj-l-01", "claude", "idle").Cwd = x.lane },
			want:  "pj-l-01 already running (pane w1:p3)", running: true},
		{name: "a configured agent in the lane", req: launch.Request{Agent: "pj-l-01"},
			panes: func(x *fx) { x.agent("w1:p4", "pj-int", "claude", "idle").Cwd = x.lane },
			want:  "pj-int still runs in lane L-01 (pane w1:p4): obor restart pj-int --stop first"},
		{name: "another program in the pane named", req: launch.Request{Agent: "pj-int", Pane: "w1:p5"},
			panes: func(x *fx) {
				x.f.Agent("w1:p5", "", "idle", "", func(p *mpane) {
					p.Kind = "codex"
					p.Procs = []port.Process{{PID: 2, Argv0: "codex"}}
				})
			},
			want: "w1:p5 runs another program (codex)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.panes(x)
			var pl placer
			req := c.req
			req.Place = pl.at(x, "w1:p9", x.lane)
			_, out, err := x.noAgent(req)
			if err == nil {
				t.Fatalf("NoAgent launch went ahead:\n%s", out)
			}
			if err.Error() != c.want {
				t.Errorf("refusal %q, want %q", err.Error(), c.want)
			}
			var running *port.Running
			if errors.As(err, &running) != c.running {
				t.Errorf("refusal %T, a *port.Running is %v", err, c.running)
			}
			if pl.calls != 0 {
				t.Errorf("placement called %d times, want none", pl.calls)
			}
			for _, q := range []string{"Write", "RenamePane", "Start"} {
				if got := x.calls(q); len(got) != 0 {
					t.Errorf("a refusal made %s calls: %v", q, got)
				}
			}
		})
	}
}

// No pane is the agent's, none is named and nothing places one: refused, as a start is.
func TestNoAgentWithNoPaneAndNoPlacementIsRefused(t *testing.T) {
	t.Parallel()
	x := setup(t)
	if _, _, err := x.noAgent(launch.Request{Agent: "pj-l-01"}); err == nil ||
		err.Error() != "no pane labelled pj-l-01 (pass --pane)" {
		t.Errorf("refusal %v, want no pane labelled pj-l-01 (pass --pane)", err)
	}
}
