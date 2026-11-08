package launch_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
)

// T-1053: a restart's start runs only once the pane is its shell again, and its line is RESTARTED only when the start
// succeeded. The owner's `obor restart ob-lead --fresh` started at once after the stop, herdr answered agent_pane_busy
// (the old agent's exit not yet reaped), the start took the old agent for the new one and printed RESTARTED.
func TestRestartWaitsForTheShellAfterTheStop(t *testing.T) {
	t.Parallel()
	t.Run("agent_pane_busy twice: the start waits for the pane, then RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		p.StartBusy = 2 // herdr's exit lag: the two first starts answer agent_pane_busy
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
		eqInt(t, len(x.calls("agent.start")), 3, "a start after each agent_pane_busy, then the one that ran")
		eqInt(t, p.StartBusy, 0, "precondition: the busy answers were the two first starts'")
		contains(t, out, "agent_pane_busy", "herdr's answer said")
		contains(t, out, "is not an available shell yet", "the wait said")
		lacks(t, out, "runs in w1:p3", "the old agent never taken for the new one")
		eq(t, p.Kind, "claude", "the agent runs in the pane again")
	})
	t.Run("agent.start keeps answering agent_pane_busy: FAIL, no RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shellBound = shortWait // the bound of the wait and the start retries
		p := x.agent("w1:p6", "pj-lead", "claude", "idle")
		p.OnQuit = "linger" // herdr still lists the old agent while it refuses the start
		p.StartBusy = 1 << 30
		out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true})
		if ok || strings.Contains(out, "RESTARTED") {
			t.Fatalf("a refused start said RESTARTED:\n%s", out)
		}
		eq(t, failLine(out), "FAIL start failed in w1:p6 (herdr: the pane is not an available shell: the old agent "+
			"has not left it; run obor restart pj-lead --pane w1:p6 again or start it by hand)", "result")
		contains(t, out, "agent_pane_busy", "herdr's answer said")
		lacks(t, out, "runs in w1:p6", "the old agent never taken for the new one")
	})
	t.Run("the detached run of the handoff: the same wait", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		p.StartBusy = 2
		out, ok, spawns := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, false, nil)
		if !ok || len(spawns) != 0 {
			t.Fatalf("the detached run: %v\n%s", spawns, out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
		eqInt(t, len(x.calls("agent.start")), 3, "a start after each agent_pane_busy, then the one that ran")
	})
	t.Run("the shell never comes back: FAIL naming the pane, no env line, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shellBound = shortWait
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		var quit atomic.Bool
		x.quitTyped(&quit)
		x.onExit = func() { p.Procs, x.gone["2"] = nil, true }
		x.procInfo(func(p *herdrtest.Pane) {
			if quit.Load() && p.Kind == "" {
				p.Foreground = 2 // herdr never gives the exited agent's pane its shell back
			}
		})
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok || strings.Contains(out, "RESTARTED") {
			t.Fatalf("a pane that never came back to its shell started:\n%s", out)
		}
		eq(t, failLine(out), "FAIL pane w1:p3 is not a shell after 60ms (the old agent has not left): nothing started", "result")
		lacks(t, strings.Join(x.calls("pane.send_input"), "\n"), "unset OBOR_AGENT", "no env line")
		eq(t, x.startCall(), "", "no start")
	})
	t.Run("the pane out of its shell after the stop: the env line only once it is a shell", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		var quit atomic.Bool
		x.quitTyped(&quit)
		// The agent's exit is reaped (its pid gone, no processes) yet herdr keeps the pane out of its shell for a few
		// looks: the exit wait sees the exit at once, the restart's own wait is the one that sees the shell come back.
		x.onExit = func() { p.Procs, x.gone["2"] = nil, true }
		left := 6
		x.procInfo(func(p *herdrtest.Pane) {
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
