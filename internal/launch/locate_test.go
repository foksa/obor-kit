package launch_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/launch"
	"obor/internal/project"
)

// identified is serve's where answer that agent runs in pane by src (session, start, label), the pane labelled label.
func identified(pane, src, label string) func(wire.WhereReq) (wire.WhereReply, error) {
	return func(wire.WhereReq) (wire.WhereReply, error) {
		return wire.WhereReply{State: wire.WhereIdentified, Pane: pane, Source: src, Label: label}, nil
	}
}

// A start whose agent is proven live in another pane is refused by Locate before anything is touched: no stop (no key,
// no input), no label, no env line, no agent.start, no session recorded (Report) and no start reported (Started); the
// holder and the target's own agent keep running. A plain restart (serve proves the holder by its start row), one with
// --force into a pane that runs an unlabelled agent (only the terminal's name proves the holder), and a fresh one (its
// session proves it; a fresh start would record the session obor chose before the agent runs).
func TestRestartRefusedTouchesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		req   launch.Request
		where func(wire.WhereReq) (wire.WhereReply, error)
		proof string
	}{
		{"plain", launch.Request{Agent: "pj-l-01", Pane: "w1:p8"}, identified("w1:p3", "start", ""), "start"},
		{"--force", launch.Request{Agent: "pj-l-01", Pane: "w1:p8", Force: true}, nil, "name"},
		{"fresh", launch.Request{Agent: "pj-l-01", Pane: "w1:p8", Fresh: true}, identified("w1:p3", "session", ""),
			"session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.sid("pj-l-01", "sid-1")
			holder := x.f.Agent("w1:p3", "pj-l-01", "idle", x.lane) // unlabelled: obor's label is not its proof
			other := x.f.Agent("w1:p8", "", "working", x.lane)      // the target runs an agent nobody labelled
			x.where = tc.where
			if holder.Label != "" || holder.Name != "pj-l-01" || other.Kind == "" {
				t.Fatalf("precondition: an unlabelled holder of the name and a live target: %+v %+v", holder, other)
			}
			out, ok := x.restart(tc.req)
			if ok {
				t.Fatalf("restarted:\n%s", out)
			}
			eq(t, failLine(out), "FAIL pj-l-01 already runs in pane w1:p3 (proof: "+tc.proof+"; label none): obor "+
				"restart pj-l-01 --pane w1:p3 restarts it there, or quit it there first", "result")
			for _, call := range []string{"pane.send_keys", "pane.send_input", "pane.rename", "agent.start", "agent.rename"} {
				if n := len(x.calls(call)); n != 0 {
					t.Errorf("%d %s calls on a refusal: %q", n, call, x.srv.Calls())
				}
			}
			if len(x.reported) != 0 || len(x.startReports()) != 0 {
				t.Errorf("a refusal reported a session %+v or a start %q", x.reported, x.startReports())
			}
			if p := x.f.Pane("w1:p3"); p.Kind != "claude" || p.Name != "pj-l-01" {
				t.Errorf("the holder = %+v, want it running under its name", p)
			}
			if p := x.f.Pane("w1:p8"); p.Kind != "claude" || p.Status != "working" {
				t.Errorf("the target's agent = %+v, want it running", p)
			}
		})
	}
}

// Locate takes the agent's restart lock (its lane's) before it reads anything: a second start of the agent waits for
// the first, then finds the agent the first one started running (a restart in place), not the home pane it was at the
// first's start.
func TestLocateWaitsForTheRestartLock(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p3", "pj-l-01", x.lane)
	first := launch.RestartLock(x.p.Root, "L-01", "")
	if ok, err := first.Try(); !ok || err != nil {
		t.Fatalf("precondition: the first start holds the lock: %v, %v", ok, err)
	}
	type res struct {
		loc launch.Located
		end func()
		err error
	}
	var out lockedBuf
	l := x.launcher(&out)
	done := make(chan res, 1)
	go func() {
		loc, end, err := l.Locate(context.Background(), "pj-l-01", "", true)
		done <- res{loc, end, err}
	}()
	said := "waiting for the restart of lane L-01 (pid " + strconv.Itoa(os.Getpid()) + ")\n"
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(out.String(), said); {
		select {
		case r := <-done:
			t.Fatalf("Locate did not wait for the lock: %+v, %v", r.loc, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("Locate never said %q: %q", said, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	eqInt(t, len(x.calls("pane.list")), 0, "no pane read while it waits")
	x.agent("w1:p3", "pj-l-01", "claude", "idle") // the first start's agent, up before it releases the lock
	first.Unlock()
	var r res
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Locate still waits after the first start ended")
	}
	if r.err != nil {
		t.Fatalf("Locate: %v\n%s", r.err, out.String())
	}
	defer r.end()
	if r.loc != (launch.Located{Pane: "w1:p3", Runs: true, Proof: "label", Label: "pj-l-01"}) {
		t.Errorf("Locate = %+v, want pj-l-01 running in w1:p3 by its label", r.loc)
	}
	if ok, err := first.Try(); ok || err != nil {
		t.Errorf("the lock is free while Locate's caller holds it: %v, %v", ok, err)
	}
}

// An agent serve proves live in a pane a person relabelled is started there again, in place, and its label is put back
// with a line saying so: an obor restart with no pane, and serve's fresh start with no pane (an integrator's job start):
// never a second pane, never "no pane labelled".
func TestStartInARelabelledPanePutsTheLabelBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, agent string
		req         launch.Request
		want        string
	}{
		{"obor restart", "pj-l-01", launch.Request{Agent: "pj-l-01"}, "RESTARTED pj-l-01 (pane w1:p3, session sid-1)"},
		{"serve's fresh start of a job", "pj-int", launch.Request{Agent: "pj-int", Fresh: true, Prompt: "[obor job T-1/1/review]"},
			"RESTARTED pj-int (pane w1:p3, session new)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.sid(tc.agent, "sid-1")
			a := x.agent("w1:p3", tc.agent, "claude", "idle")
			a.Label = "notes"
			x.where = identified("w1:p3", "session", "notes")
			out, ok := x.restart(tc.req)
			if !ok {
				t.Fatalf("not started in its pane:\n%s", out)
			}
			eq(t, last(out), tc.want, "result")
			contains(t, out, "pane w1:p3 ("+tc.agent+" runs there: session)", "the pane Locate chose")
			contains(t, out, "label of pane w1:p3 put back: "+tc.agent+" (it was notes)", "the label put back, said")
			eqInt(t, len(x.calls("pane.rename label="+tc.agent+" pane_id=w1:p3")), 1, "the label put back")
			eq(t, x.f.Pane("w1:p3").Label, tc.agent, "the pane's label")
		})
	}
}

// A stop asks the same decision: the agent proven live in another pane than --pane is refused, nothing stopped; with no
// pane it stops the pane the agent is proven in, whatever its label.
func TestStopAsksLocate(t *testing.T) {
	t.Parallel()
	x := setup(t)
	a := x.agent("w1:p3", "pj-l-01", "claude", "idle")
	a.Label = "notes"
	x.shell("w1:p8", "", x.lane)
	x.where = identified("w1:p3", "session", "notes")
	out, ok := x.stop("pj-l-01", "w1:p8", false)
	if ok {
		t.Fatalf("stopped:\n%s", out)
	}
	eq(t, failLine(out), "FAIL pj-l-01 already runs in pane w1:p3 (proof: session; label notes): obor restart "+
		"pj-l-01 --pane w1:p3 restarts it there, or quit it there first", "refused")
	if n := len(x.calls("pane.send_input")) + len(x.calls("pane.send_keys")); n != 0 {
		t.Fatalf("a refused stop typed into a pane: %q", x.srv.Calls())
	}
	out, ok = x.stop("pj-l-01", "", false)
	if !ok {
		t.Fatalf("not stopped:\n%s", out)
	}
	eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "the pane it is proven in")
}

// A --pane whose label names nobody (notes, an alias the config no longer names) is a place for a start or a stop:
// only a label naming another agent the config names reserves the pane. The restart starts there and puts the agent's
// label back; the stop quits the pane's shell.
func TestRestartAndStopIntoAPaneLabelledNobody(t *testing.T) {
	t.Parallel()
	t.Run("restart: started there, the label put back", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p4", "notes", x.lane)
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Pane: "w1:p4"})
		if !ok {
			t.Fatalf("not started:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p4, session sid-1)", "result")
		contains(t, out, "label of pane w1:p4 put back: pj-l-01 (it was notes)", "the label put back, said")
		eq(t, x.f.Pane("w1:p4").Label, "pj-l-01", "the pane's label")
	})
	t.Run("--stop: stopped there", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p4", "notes", x.lane)
		out, ok := x.stop("pj-l-01", "w1:p4", false)
		if !ok {
			t.Fatalf("not stopped:\n%s", out)
		}
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p4, already at a shell)", "result")
	})
}

// Serve down (no socket at the project): a start beside a live agent of the project that no label accounts for is
// refused with the serve-down line, before anything is touched; the default where request to Cfg.Root's serve is what
// says it is down.
func TestStartRefusedWhileServeIsDownAndAnAgentIsUnaccounted(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-l-01", "sid-1")
	x.shell("w1:p3", "pj-l-01", x.lane)
	x.f.Agent("w1:p1", "", "idle", x.p.Root).Label = "notes"
	var out bytes.Buffer
	l := x.launcher(&out)
	l.Where = nil // serve's socket at the project: none
	line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01"})
	got, ok := result(t, &out, line, err)
	if ok {
		t.Fatalf("restarted:\n%s", got)
	}
	eq(t, failLine(got), "FAIL cannot tell whether the claude in pane w1:p1 (label notes) is pj-l-01: obor serve is "+
		"not running. obor serve restart, then retry; or obor restart pj-l-01 --pane w1:p1 to replace it", "refused")
	if n := len(x.calls("pane.send_input")) + len(x.calls("pane.rename")) + len(x.calls("agent.start")); n != 0 {
		t.Errorf("a refused start touched a pane: %q", x.srv.Calls())
	}
}

// The terminal's start lost a race (a live agent in another pane took the name after Locate): Start's refusal is a
// port.Refused, Locate's line naming the holder by the terminal's name.
func TestStartLostRaceIsRefused(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p3", "", x.lane)
	x.wrap = func(h port.PaneControl) port.PaneControl { return raceStart{h, x} }
	l := x.launcher(io.Discard)
	err := l.Start(context.Background(), launch.Start{Agent: "pj-l-01", Kind: "claude", Pane: "w1:p3",
		Args: []string{"--model", "opus"}})
	var refused port.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("Start = %v, want a port.Refused", err)
	}
	eq(t, refused.Line, "pj-l-01 already runs in pane w1:p1 (proof: name; label notes): obor restart pj-l-01 --pane "+
		"w1:p1 restarts it there, or quit it there first", "the refusal")
	var fail project.Fail
	if errors.As(err, &fail) {
		t.Errorf("the refusal is a counted Fail too: %v", err)
	}
}

// raceStart is a terminal whose start finds the name taken by a live agent in w1:p1 (labelled notes): it puts that
// agent there, then answers the start as herdr's adapter does.
type raceStart struct {
	port.PaneControl
	x *fx
}

func (r raceStart) Start(ctx context.Context, name, kind, pane string, say func(string, ...any), args ...string) (port.Pane, error) {
	r.x.f.Agent("w1:p1", name, "idle", r.x.lane).Label = "notes"
	return r.PaneControl.Start(ctx, name, kind, pane, say, args...)
}
