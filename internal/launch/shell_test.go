package launch_test

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/launch"
)

// A restart's start runs only once the pane is its shell again, and its line is RESTARTED only when the start
// succeeded: a restart issued straight after the stop meets the terminal refusing the start, PaneBusy (the old
// agent's exit not yet reaped), and must start again, never take the old agent's listing for the new one's.
func TestRestartWaitsForTheShellAfterTheStop(t *testing.T) {
	t.Parallel()
	t.Run("PaneBusy twice: the start waits for the pane, then RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		// the terminal's exit lag: the two first starts are refused PaneBusy
		x.f.Set(p.ID, func(p *mpane) { p.StartBusy = 2 })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
		eqInt(t, len(x.calls("Start")), 3, "a start after each PaneBusy, then the one that ran")
		eqInt(t, p.StartBusy, 0, "precondition: the busy answers were the two first starts'")
		contains(t, out, "is not an available shell yet", "the busy answer's wait said")
		lacks(t, out, "runs in w1:p3", "the old agent never taken for the new one")
		eq(t, p.Kind, "claude", "the agent runs in the pane again")
	})
	t.Run("the start keeps being refused PaneBusy: FAIL, no RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shellBound = shortWait // the bound of the wait and the start retries
		p := x.agent("w1:p6", "pj-lead", "claude", "idle")
		// the terminal still lists the old agent while it refuses the start
		x.f.Set(p.ID, func(p *mpane) { p.OnQuit, p.StartBusy = "linger", 1<<30 })
		out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true})
		if ok || strings.Contains(out, "RESTARTED") {
			t.Fatalf("a refused start said RESTARTED:\n%s", out)
		}
		eq(t, failLine(out), "FAIL start failed in w1:p6 (the pane is not an available shell: the old agent "+
			"has not left it; run obor restart pj-lead --fresh --pane w1:p6 again or start it by hand)", "result")
		lacks(t, out, "runs in w1:p6", "the old agent never taken for the new one")
	})
	t.Run("PaneBusy with a recorded session: the plain form", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shellBound = shortWait // the bound of the wait and the start retries
		x.sid("pj-lead", "sid-1")
		p := x.agent("w1:p6", "pj-lead", "claude", "idle")
		// the terminal still lists the old agent while it refuses the start
		x.f.Set(p.ID, func(p *mpane) { p.OnQuit, p.StartBusy = "linger", 1<<30 })
		out, ok := x.restart(launch.Request{Agent: "pj-lead"})
		if ok || strings.Contains(out, "RESTARTED") {
			t.Fatalf("a refused start said RESTARTED:\n%s", out)
		}
		eq(t, failLine(out), "FAIL start failed in w1:p6 (the pane is not an available shell: the old agent "+
			"has not left it; run obor restart pj-lead --pane w1:p6 again or start it by hand)", "result")
		lacks(t, out, "--fresh", "a recorded session resumes: no --fresh")
	})
	t.Run("the detached run of the handoff: the same wait", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		x.f.Set(p.ID, func(p *mpane) { p.StartBusy = 2 })
		out, ok, spawns := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, false, nil)
		if !ok || len(spawns) != 0 {
			t.Fatalf("the detached run: %v\n%s", spawns, out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
		eqInt(t, len(x.calls("Start")), 3, "a start after each PaneBusy, then the one that ran")
	})
	t.Run("the shell never comes back: FAIL naming the pane, no env line, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shellBound = shortWait
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		var quit atomic.Bool
		x.quitTyped(&quit)
		x.onExit = func() {
			x.f.Set(p.ID, func(p *mpane) { p.Procs = nil })
			x.gone["2"] = true
		}
		x.procInfo(func(p *mpane) {
			if quit.Load() && p.Kind == "" {
				p.Foreground = 2 // the terminal never gives the exited agent's pane its shell back
			}
		})
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok || strings.Contains(out, "RESTARTED") {
			t.Fatalf("a pane that never came back to its shell started:\n%s", out)
		}
		eq(t, failLine(out), "FAIL pane w1:p3 is not an available shell after 60ms: start the agent there with "+
			"obor restart pj-l-01 --pane w1:p3", "result")
		lacks(t, strings.Join(x.calls("Write"), "\n"), "unset OBOR_AGENT", "no env line")
		eq(t, x.startCall(), "", "no start")
	})
	t.Run("the pane out of its shell after the stop: the env line only once it is a shell", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		var quit atomic.Bool
		x.quitTyped(&quit)
		// The agent's exit is reaped (its pid gone, no processes) yet the terminal keeps the pane out of its shell for a few
		// looks: the exit wait sees the exit at once, the restart's own wait is the one that sees the shell come back.
		x.onExit = func() {
			x.f.Set(p.ID, func(p *mpane) { p.Procs = nil })
			x.gone["2"] = true
		}
		left := 6
		x.procInfo(func(p *mpane) {
			switch {
			case !quit.Load() || p.Kind != "" || left <= 0:
				p.Foreground = 0
			default:
				left--
				p.Foreground = 2
			}
		})
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		contains(t, out, "waiting for the shell of pane w1:p3", "the wait said")
		eqInt(t, left, 0, "the wait looked until the pane was its shell")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
	})
}

// Each entry point's start goes through the one start: the terminal still lists the old Claude after it exited
// (linger), the first start is refused PaneBusy and started again once the pane is its shell, the second starts a
// Claude that works on its first prompt (the terminal's start counts it: internal/herdr's TestStartCases).
func TestEveryEntryPointStartsThroughTheOneStart(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	probe := func(x *fx, pane, agent string) *mpane {
		p := x.agent(pane, agent, "claude", "idle")
		x.f.Set(p.ID, func(p *mpane) {
			p.OnQuit = "linger"   // the old Claude still listed after its exit
			p.StartBusy = 1       // the first start is refused PaneBusy
			p.OnStart = "working" // the new one works on its first prompt
		})
		return p
	}
	t.Run("obor restart with a first prompt, the old Claude still listed: RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		probe(x, "w1:p6", "pj-lead")
		out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true, Prompt: wake})
		eqInt(t, len(x.calls("Start")), 2, "precondition: busy, then the start the terminal made")
		if !ok {
			t.Fatalf("a running start said failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-lead (pane w1:p6, session new)", "result")
		eq(t, x.f.At("w1:p6").Status, "working", "the new agent works on its first prompt")
	})
	t.Run("the detached handoff restart, the old Claude still listed: RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		probe(x, "w1:p6", "pj-lead")
		out, ok, spawns := x.self("w1:p6", true, launch.Request{Agent: "pj-lead", Fresh: true, Prompt: wake}, false, nil)
		eqInt(t, len(x.calls("Start")), 2, "precondition: busy, then the start the terminal made")
		if !ok || len(spawns) != 0 {
			t.Fatalf("the detached run: %v\n%s", spawns, out)
		}
		eq(t, last(out), "RESTARTED pj-lead (pane w1:p6, session new)", "result")
	})
	t.Run("serve's worker start with its job's wake, the old Claude still listed: RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		probe(x, "w1:p3", "pj-l-01")
		var out bytes.Buffer
		l := x.launcher(&out)
		l.Job = func(context.Context, string, string) (string, error) { return "T-1/1/work", nil }
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		got, ok := result(t, &out, line, err)
		contains(t, x.startCall(), "T-1/1/work", "precondition: the job's wake is the first prompt")
		eqInt(t, len(x.calls("Start")), 2, "precondition: busy, then the start the terminal made")
		if !ok {
			t.Fatalf("a running start said failed:\n%s", got)
		}
		eq(t, last(got), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
	})
	t.Run("restore's restart of a labelled pane without an agent: busy retried, no shell wait, RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.StartBusy = 1 })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true})
		eqInt(t, len(x.calls("Start")), 2, "a start after the busy answer, then the one that ran")
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		lacks(t, out, "waiting for the shell of pane", "a pane that ran no agent is not waited on before the env line")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
	})
}
