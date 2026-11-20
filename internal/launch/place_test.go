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

// placer is a Launch.Place that places pane (a shell with cwd) and counts its calls, keeping the env it was given.
type placer struct {
	calls int
	env   []string
}

func (pl *placer) at(x *fx, pane, cwd string) *port.Placer {
	return &port.Placer{Place: func(_ context.Context, env []string) (string, error) {
		pl.calls++
		pl.env = env
		x.shell(pane, "", cwd)
		return pane, nil
	}}
}

// An agent with no pane and a placement: the placement is called once, with the start's env, and the start goes into
// the pane it placed, labelled with the agent's name.
func TestLaunchPlacesAPaneForAnAgentWithNone(t *testing.T) {
	t.Parallel()
	x := setup(t)
	if len(x.f.All) != 0 {
		t.Fatalf("precondition: no pane, got %d", len(x.f.All))
	}
	var pl placer
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Place: pl.at(x, "w1:p9", x.lane)})
	if !ok {
		t.Fatalf("the start failed:\n%s", out)
	}
	if pl.calls != 1 {
		t.Fatalf("placement called %d times, want once", pl.calls)
	}
	if call := x.startCall(); !strings.Contains(call, "pane=w1:p9") {
		t.Errorf("start call %q, want it in the placed pane w1:p9", call)
	}
	if got := last(out); got != "RESTARTED pj-l-01 (pane w1:p9, session new)" {
		t.Errorf("result %q, want the start in pane w1:p9", got)
	}
	if l := x.f.At("w1:p9").Label; l != "pj-l-01" {
		t.Errorf("placed pane labelled %q, want pj-l-01", l)
	}
	for _, v := range []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01", "OBOR_AGENT="} {
		if !slices.Contains(pl.env, v) {
			t.Errorf("placement env %q, want %s in it", pl.env, v)
		}
	}
}

// An agent with a pane (its home: the pane labelled with its name at its shell): the placement is not called and the
// start goes into that pane.
func TestLaunchPlacesNoPaneForAnAgentWithOne(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p3", "pj-l-01", x.lane)
	var pl placer
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Place: pl.at(x, "w1:p9", x.lane)})
	if !ok {
		t.Fatalf("the start failed:\n%s", out)
	}
	if pl.calls != 0 {
		t.Errorf("placement called %d times, want none", pl.calls)
	}
	if call := x.startCall(); !strings.Contains(call, "pane=w1:p3") {
		t.Errorf("start call %q, want it in the home pane w1:p3", call)
	}
}

// NoStop: a running agent is refused with obor setup's text, nothing stopped: the agent itself (a *port.Running),
// another agent in its lane worktree (named by its label when the config names it, else by its pane), another program
// in the pane named.
func TestLaunchNoStopRefusesARunningAgent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		req      launch.Request
		panes    func(x *fx)
		occupant string // the pane whose agent must keep running
		want     string
		running  bool // the refusal is a *port.Running
	}{
		{name: "the agent itself runs", req: launch.Request{Agent: "pj-l-01", Fresh: true},
			panes: func(x *fx) {
				x.agent("w1:p3", "pj-l-01", "claude", "idle").Cwd = x.lane
			},
			occupant: "w1:p3", want: "pj-l-01 already running (pane w1:p3)", running: true},
		{name: "a configured agent in the lane", req: launch.Request{Agent: "pj-l-01", Fresh: true},
			panes: func(x *fx) {
				x.shell("w1:p3", "pj-l-01", x.lane)
				x.agent("w1:p4", "pj-int", "claude", "idle").Cwd = x.lane
			},
			occupant: "w1:p4", want: "pj-int still runs in lane L-01 (pane w1:p4): obor restart pj-int --stop first"},
		{name: "an unlabelled agent in the lane", req: launch.Request{Agent: "pj-l-01", Fresh: true},
			panes: func(x *fx) {
				x.shell("w1:p3", "pj-l-01", x.lane)
				x.f.Agent("w1:p4", "", "idle", x.lane, func(p *mpane) {
					p.Procs = []port.Process{{PID: 2, Argv0: "claude"}}
				})
			},
			occupant: "w1:p4", want: "claude still runs in lane L-01 (pane w1:p4): quit the agent in pane w1:p4 first"},
		{name: "another program in the pane named", req: launch.Request{Agent: "pj-int", Pane: "w1:p5", Fresh: true},
			panes: func(x *fx) {
				x.f.Agent("w1:p5", "", "idle", "", func(p *mpane) {
					p.Kind = "codex"
					p.Procs = []port.Process{{PID: 2, Argv0: "codex"}}
				})
			},
			occupant: "w1:p5", want: "w1:p5 runs another program (codex)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.panes(x)
			if x.f.At(c.occupant).Kind == "" {
				t.Fatalf("precondition: an agent runs in %s", c.occupant)
			}
			req := c.req
			req.NoStop = true
			var out bytes.Buffer
			_, err := x.launcher(&out).Launch(context.Background(), req)
			if err == nil {
				t.Fatalf("NoStop start went ahead:\n%s", out.String())
			}
			if err.Error() != c.want {
				t.Errorf("refusal %q, want %q", err.Error(), c.want)
			}
			var running *port.Running
			if errors.As(err, &running) != c.running {
				t.Errorf("refusal %T: a *port.Running %v, want %v", err, !c.running, c.running)
			}
			if x.f.At(c.occupant).Kind == "" {
				t.Errorf("the agent in %s was stopped", c.occupant)
			}
			for _, method := range []string{"Write", "SendKeys", "Prompt", "Start", "RenamePane"} {
				if calls := x.calls(method); len(calls) != 0 {
					t.Errorf("%s called: %q (nothing may be typed, stopped or started)", method, calls)
				}
			}
			if len(x.kills) != 0 {
				t.Errorf("signals sent: %q", x.kills)
			}
		})
	}
}

// A NoStop start of a role into the --pane the caller named takes a pane at its shell whatever its cwd (obor setup role
// integrator --pane <p> --start); a pane carrying another agent's label is refused as before, nothing typed or started.
func TestLaunchNoStopTakesTheNamedPaneWhateverItsCwd(t *testing.T) {
	t.Parallel()
	t.Run("a shell outside the project: started", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p5", "", "/elsewhere")
		if cwd := x.f.At("w1:p5").Cwd; cwd != "/elsewhere" || strings.HasPrefix(cwd, x.p.Root) {
			t.Fatalf("precondition: the pane's cwd is outside the project, got %q", cwd)
		}
		var out bytes.Buffer
		started, err := x.launcher(&out).Launch(context.Background(),
			launch.Request{Agent: "pj-int", Pane: "w1:p5", Fresh: true, NoStop: true})
		if err != nil || started.Pane != "w1:p5" {
			t.Fatalf("start: %+v, %v\n%s", started, err, out.String())
		}
		if call := x.startCall(); !strings.Contains(call, "pane=w1:p5") {
			t.Errorf("start call %q, want it in w1:p5", call)
		}
	})
	t.Run("another agent's label: refused", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p5", "pj-lead", "/elsewhere")
		var out bytes.Buffer
		_, err := x.launcher(&out).Launch(context.Background(),
			launch.Request{Agent: "pj-int", Pane: "w1:p5", Fresh: true, NoStop: true})
		if err == nil {
			t.Fatalf("a pane labelled pj-lead took pj-int's start:\n%s", out.String())
		}
		if x.f.At("w1:p5").Label != "pj-lead" {
			t.Errorf("the pane's label changed to %q", x.f.At("w1:p5").Label)
		}
		for _, method := range []string{"Write", "SendKeys", "Start", "RenamePane"} {
			if calls := x.calls(method); len(calls) != 0 {
				t.Errorf("%s called: %q", method, calls)
			}
		}
	})
}
