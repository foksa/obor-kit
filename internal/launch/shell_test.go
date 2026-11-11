package launch_test

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
)

// A restart's start runs only once the pane is its shell again, and its line is RESTARTED only when the start
// succeeded: a restart issued straight after the stop meets herdr answering agent_pane_busy (the old agent's exit not
// yet reaped) and must retry, never take the old agent's listing for the new one's.
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
		contains(t, out, "is not an available shell yet", "the busy answer's wait said")
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
		eq(t, failLine(out), "FAIL start failed in w1:p6 (the pane is not an available shell: the old agent "+
			"has not left it; run obor restart pj-lead --pane w1:p6 again or start it by hand)", "result")
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

// Each entry point's start goes through the one start, whose evidence rule takes a listing of the
// kind for the new agent once herdr accepted the start. The fixture sets it up: herdr still lists the old Claude after
// it exited (linger), the first start answers agent_pane_busy, the second starts a Claude that works on its first
// prompt and herdr gives up waiting on it (timeout). The start must count the new agent, not the old listing.
func TestEveryEntryPointStartsThroughTheOneStart(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	probe := func(x *fx, pane, agent string) *herdrtest.Pane {
		p := x.agent(pane, agent, "claude", "idle")
		p.OnQuit = "linger" // the old Claude still listed after its exit
		p.StartBusy = 1
		p.OnStart = "working" // the new one works on its first prompt: herdr's timeout
		return p
	}
	t.Run("obor restart with a first prompt, the old Claude still listed: RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		probe(x, "w1:p6", "pj-lead")
		out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true, Prompt: wake})
		eqInt(t, len(x.calls("agent.start")), 2, "precondition: busy, then the start herdr made")
		contains(t, out, "(timeout)", "precondition: herdr gave up on the new agent")
		if !ok {
			t.Fatalf("a running start said failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-lead (pane w1:p6, session new)", "result")
		contains(t, out, "pj-lead runs in w1:p6 (working on its first prompt): started", "said")
	})
	t.Run("the detached handoff restart, the old Claude still listed: RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		probe(x, "w1:p6", "pj-lead")
		out, ok, spawns := x.self("w1:p6", true, launch.Request{Agent: "pj-lead", Fresh: true, Prompt: wake}, false, nil)
		eqInt(t, len(x.calls("agent.start")), 2, "precondition: busy, then the start herdr made")
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
		eqInt(t, len(x.calls("agent.start")), 2, "precondition: busy, then the start herdr made")
		if !ok {
			t.Fatalf("a running start said failed:\n%s", got)
		}
		eq(t, last(got), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
	})
	t.Run("restore's restart of a labelled pane without an agent: busy retried, no shell wait, RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "").StartBusy = 1
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true})
		eqInt(t, len(x.calls("agent.start")), 2, "a start after the busy answer, then the one that ran")
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		lacks(t, out, "waiting for the shell of pane", "a pane that ran no agent is not waited on before the env line")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
	})
}

// Every entry point's start is up only on herdr's word that its launch is no longer pending. The
// fixture's sequence through herdr: the old Claude still listed after its exit (linger), the first start answers
// agent_pane_busy, the second is accepted at once with launch_pending, and for three reads the pane lists the start's
// kind with a stale idle, still pending; then the new agent is up (interactive_ready). Each start waits through the
// pending reads; with no replacement ever up it fails.
func TestEveryEntryPointWaitsForTheLaunch(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	pending := func(p *herdrtest.Pane, reads int) *herdrtest.Pane {
		p.OnQuit, p.StartBusy, p.StartPending = "linger", 1, reads
		return p
	}
	// waited: the start was accepted pending and read the pane through every pending read (the transition happened).
	waited := func(t *testing.T, x *fx, p *herdrtest.Pane, pane string) {
		t.Helper()
		calls := x.srv.Calls()
		reads := 0
		for i := len(calls) - 1; i >= 0 && !strings.HasPrefix(calls[i], "agent.start "); i-- {
			if hasParts(calls[i], "agent.get target="+pane) {
				reads++
			}
		}
		x.f.Locked(func() {
			if p.StartPending != 0 || p.LaunchPending || !p.InteractiveReady {
				t.Errorf("pending reads left %d, launch_pending %t, interactive_ready %t: want the launch read until up",
					p.StartPending, p.LaunchPending, p.InteractiveReady)
			}
		})
		if reads < 4 {
			t.Errorf("%d reads of %s after the accepted start: want the 3 pending ones and the one up", reads, pane)
		}
	}
	t.Run("obor restart: RESTARTED once the new agent is up", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := pending(x.agent("w1:p6", "pj-lead", "claude", "idle"), 3)
		out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true})
		eqInt(t, len(x.calls("agent.start")), 2, "precondition: busy, then the start herdr accepted")
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-lead (pane w1:p6, session new)", "result")
		waited(t, x, p, "w1:p6")
	})
	t.Run("obor restart, the replacement never up: FAIL, no RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.up = shortWait
		pending(x.agent("w1:p6", "pj-lead", "claude", "idle"), 1<<30)
		out, ok := x.restart(launch.Request{Agent: "pj-lead", Fresh: true})
		eqInt(t, len(x.calls("agent.start")), 2, "precondition: busy, then the start herdr accepted")
		if ok || strings.Contains(out, "RESTARTED") {
			t.Fatalf("a start whose agent never came up said RESTARTED:\n%s", out)
		}
		contains(t, out, "no claude agent came up in w1:p6 within 60ms", "why")
	})
	t.Run("the detached handoff restart: RESTARTED once the new agent is up", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := pending(x.agent("w1:p6", "pj-lead", "claude", "idle"), 3)
		out, ok, spawns := x.self("w1:p6", true, launch.Request{Agent: "pj-lead", Fresh: true, Prompt: wake}, false, nil)
		if !ok || len(spawns) != 0 {
			t.Fatalf("the detached run: %v\n%s", spawns, out)
		}
		eq(t, last(out), "RESTARTED pj-lead (pane w1:p6, session new)", "result")
		waited(t, x, p, "w1:p6")
	})
	t.Run("serve's worker start with its job's wake: RESTARTED once the new agent is up", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := pending(x.agent("w1:p3", "pj-l-01", "claude", "idle"), 3)
		var out bytes.Buffer
		l := x.launcher(&out)
		l.Job = func(context.Context, string, string) (string, error) { return "T-1/1/work", nil }
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		got, ok := result(t, &out, line, err)
		contains(t, x.startCall(), "T-1/1/work", "precondition: the job's wake is the first prompt")
		if !ok {
			t.Fatalf("restart failed:\n%s", got)
		}
		eq(t, last(got), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
		waited(t, x, p, "w1:p3")
	})
	t.Run("restore's restart of a labelled pane without an agent: RESTARTED once the new agent is up", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := x.shell("w1:p3", "pj-l-01", "")
		p.StartBusy, p.StartPending = 1, 3
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
		waited(t, x, p, "w1:p3")
	})
}
