package setup

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
)

// fakeLauncher is the launcher setup's start drives (port.Launcher.Launch): it records each request and answers it as
// the launcher does for an agent with no pane: the request's placement called once with env, the start in the pane it
// returns. running: the agent runs in that pane (a NoStop request's *port.Running, nothing placed); idle: a NoAgent request
// finds its labelled pane there with no agent (Started.Idle); trust: the start is
// held by a trust dialog. Stop is not setup's start's (nil: a call panics).
type fakeLauncher struct {
	port.Launcher
	reqs    []port.Launch
	env     []string
	running string
	idle    string // a NoAgent launch finds its labelled pane here, no agent in it (Started.Idle)
	trust   bool
}

func (f *fakeLauncher) Launch(ctx context.Context, req port.Launch) (port.Started, error) {
	f.reqs = append(f.reqs, req)
	if f.running != "" && req.NoStop {
		return port.Started{}, &port.Running{Agent: req.Agent, Pane: f.running}
	}
	if f.idle != "" && req.NoAgent && req.Pane == "" {
		return port.Started{Pane: f.idle, Idle: true}, nil
	}
	pane := req.Pane
	if pane == "" && req.Place != nil {
		var err error
		if pane, err = req.Place.Place(ctx, f.env); err != nil {
			return port.Started{}, err
		}
	}
	if req.NoAgent {
		return port.Started{Pane: pane}, nil
	}
	st := port.Started{Line: "RESTARTED " + req.Agent, Pane: pane, Kind: "claude"}
	if f.trust {
		return st, &port.TrustDialog{Hint: req.Agent + " waits at a trust dialog in pane " + pane}
	}
	return st, nil
}

// setupWith is x's Setup whose launcher is f.
func (x *fx) setupWith(t *testing.T, f *fakeLauncher) (*Setup, *strings.Builder) {
	t.Helper()
	s, _ := x.newSetup(t)
	var out strings.Builder
	s.Dir, s.Out = x.Root, &out
	s.Launcher = func(*config.Config) port.Launcher { return f }
	return s, &out
}

// setup's --start is one Launch: a fresh session, refused rather than stopping what runs (NoStop), its first prompt the
// agent's own ("" for a lane or a role: the launcher gives the role's), its placement setup's (called once by the
// launcher, with the env it passes: the new pane of the lane's kind in the tab --tab names, readied by the start); the
// result names the pane the launcher started it in. A --pane target is the request's pane, nothing placed.
func TestStartIsOneLaunchWithSetupsPlacement(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	f := &fakeLauncher{env: []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01", "OBOR_AGENT="}}
	s, out := x.setupWith(t, f)
	x.reset()
	p, err := s.lanePane(context.Background(), "L-01", where{tab: "w1:t1"}, true)
	if err != nil {
		t.Fatalf("lanePane: %v\n%s", err, out)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("%d launches, want one: %+v", len(f.reqs), f.reqs)
	}
	r := f.reqs[0]
	if r.Agent != "pj-l-01" || r.Pane != "" || !r.Fresh || !r.NoStop || r.Prompt != "" || r.Again != "" ||
		r.Type != "" || r.Place == nil {
		t.Errorf("launch %+v; want pj-l-01 fresh, NoStop, no pane, prompt, again or type, with a placement", r)
	}
	calls := strings.Join(x.reqs(), "\n")
	if n := strings.Count(calls, "pane.split"); n != 1 {
		t.Fatalf("%d splits, want the one placement:\n%s", n, calls)
	}
	has(t, calls, "env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_ROLE=worker}", "the placement takes the launcher's env")
	has(t, calls, "target_pane_id=w1:p1", "the placement in the tab --tab names")
	if p != (placed{pane: "w1:n1", started: true}) {
		t.Errorf("result %+v, want started in the placed pane w1:n1", p)
	}
	if got := last(out.String()); got != "OK L-01: pj-l-01 in pane w1:n1" {
		t.Errorf("last line %q", got)
	}

	f.reqs = nil
	x.reset()
	if _, err := s.lanePane(context.Background(), "L-01", where{pane: "w1:q1"}, true); err != nil {
		t.Fatalf("lanePane --pane: %v\n%s", err, out)
	}
	if len(f.reqs) != 1 || f.reqs[0].Pane != "w1:q1" {
		t.Errorf("launch %+v; want the --pane target as its pane", f.reqs)
	}
	hasNot(t, strings.Join(x.reqs(), "\n"), "pane.split", "a --pane target: nothing placed")
}

// a drive's start carries its first prompt, its model and effort and its own command (obor drive <T-xx>, named by a
// failed start's hint).
func TestStartDriveLaunchCarriesItsPromptAndCommand(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	f := &fakeLauncher{}
	s, out := x.setupWith(t, f)
	if err := s.StartDrive(context.Background(), Drive{Task: "T-07", Agent: "pj-l-01-drive", Lane: "L-01",
		Cwd: x.LaneDir("L-01"), Type: "luna", Effort: "high", Prompt: "drive T-07"}); err != nil {
		t.Fatalf("StartDrive: %v\n%s", err, out)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("%d launches, want one", len(f.reqs))
	}
	r := f.reqs[0]
	if r.Agent != "pj-l-01-drive" || r.Prompt != "drive T-07" || r.Again != "obor drive T-07" || r.Type != "luna" ||
		r.Effort != "high" || !r.Fresh || !r.NoStop || r.Place == nil {
		t.Errorf("launch %+v; want the drive's prompt, command, model and effort, fresh, NoStop, placed", r)
	}
}

// the agent running already (the launcher's *port.Running): OK, nothing done, the result its pane; a start held by a
// trust dialog is a failed start to setup, in the launcher's words.
func TestStartRunningAndTrustHeld(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	f := &fakeLauncher{running: "w1:x1"}
	s, out := x.setupWith(t, f)
	x.reset()
	p, err := s.lanePane(context.Background(), "L-01", where{}, true)
	if err != nil {
		t.Fatalf("lanePane: %v\n%s", err, out)
	}
	if len(f.reqs) != 1 || !f.reqs[0].NoStop {
		t.Fatalf("precondition: one NoStop launch, got %+v", f.reqs)
	}
	if p != (placed{pane: "w1:x1", running: true}) {
		t.Errorf("result %+v, want running in w1:x1", p)
	}
	if got := last(out.String()); got != "OK L-01: pj-l-01 already running" {
		t.Errorf("last line %q", got)
	}
	hasNot(t, strings.Join(x.reqs(), "\n"), "pane.split", "a running agent: nothing placed")

	f2 := &fakeLauncher{trust: true}
	s2, out2 := x.setupWith(t, f2)
	_, err = s2.lanePane(context.Background(), "L-01", where{}, true)
	if want := "start failed in w1:n1 (pj-l-01 waits at a trust dialog in pane w1:n1)"; err == nil || err.Error() != want {
		t.Errorf("a start held by a trust dialog: %v, want %q\n%s", err, want, out2)
	}
}

// setup's pane without --start is the same one Launch with the NoAgent option (the launcher decides the name, the
// running agent, the lane's occupancy, the env and the label once, as for a start): a pane placed by setup's placement
// (the terminal readies it, no start types its env), the result its pane, and setup's own lines: its label, "ready
// for", OK. The agent running → OK, nothing done; its labelled pane there already (a rerun) → OK, nothing done.
func TestPaneWithoutStartIsOneNoAgentLaunch(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	f := &fakeLauncher{env: []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01", "OBOR_AGENT="}}
	s, out := x.setupWith(t, f)
	x.reset()
	p, err := s.lanePane(context.Background(), "L-01", where{tab: "w1:t1"}, false)
	if err != nil {
		t.Fatalf("lanePane: %v\n%s", err, out)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("%d launches, want one: %+v", len(f.reqs), f.reqs)
	}
	if r := f.reqs[0]; r.Agent != "pj-l-01" || !r.NoAgent || !r.NoStop || r.Pane != "" || r.Place == nil {
		t.Errorf("launch %+v; want pj-l-01, NoAgent and NoStop, no pane, with a placement", r)
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "target_pane_id=w1:p1", "the placement in the tab --tab names")
	has(t, calls, "env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_ROLE=worker}", "the placement takes the launcher's env")
	if p != (placed{pane: "w1:n1"}) {
		t.Errorf("result %+v, want the placed pane w1:n1, no agent", p)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if n := len(lines); n < 2 || lines[n-2] != "pane w1:n1 ready for pj-l-01, no agent started (started on first use; --start starts it now)" ||
		lines[n-1] != "OK L-01: pane w1:n1" {
		t.Errorf("lines %q, want the ready line and OK last", lines)
	}

	f.reqs, f.running = nil, "w1:x1"
	out.Reset()
	if p, err = s.lanePane(context.Background(), "L-01", where{}, false); err != nil {
		t.Fatalf("lanePane, running: %v\n%s", err, out)
	}
	if p != (placed{pane: "w1:x1", running: true}) || last(out.String()) != "OK L-01: pj-l-01 already running" {
		t.Errorf("a running agent: %+v, last line %q", p, last(out.String()))
	}

	f.reqs, f.running, f.idle = nil, "", "w1:q1"
	out.Reset()
	if p, err = s.lanePane(context.Background(), "L-01", where{}, false); err != nil {
		t.Fatalf("lanePane, rerun: %v\n%s", err, out)
	}
	if p != (placed{pane: "w1:q1", idle: true}) || last(out.String()) != "OK L-01: pane w1:q1" ||
		!strings.Contains(out.String(), "skip pane: pj-l-01 already labelled pj-l-01 (pane w1:q1)") {
		t.Errorf("a rerun: %+v, output %q", p, out.String())
	}
}
