package launch_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/launch"
)

// codeErr is a terminal error with its code (herdr's agent_pane_busy, timeout, …).
type codeErr string

func (e codeErr) Error() string     { return "herdr: " + string(e) }
func (e codeErr) ErrorCode() string { return string(e) }

// memPane is an in-memory PaneControl of one pane w1:p3, scripted per row: what its listing shows (Agent), whether
// its shell has the foreground (ProcessInfo), and each agent start's answer. Any other call panics (the nil embedded
// interface): the start makes none.
type memPane struct {
	port.PaneControl
	mu     sync.Mutex
	listed port.Pane // the pane's agent as the listing shows it (Agent "" : no agent, an error)
	shell  bool
	starts int
	looks  int                                        // Agent calls so far
	start  func(m *memPane, n int) (port.Pane, error) // the nth start's answer (1-based), under mu
	onLook func(m *memPane)                           // after each look, under mu (nil: nothing)
}

const memID = "w1:p3"

func (m *memPane) Agent(_ context.Context, target string) (port.Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.looks++
	if m.onLook != nil {
		m.onLook(m)
	}
	if m.listed.Agent == "" || target != memID {
		return port.Pane{}, codeErr("agent_not_found")
	}
	return m.listed, nil
}

func (m *memPane) ProcessInfo(context.Context, string) (port.ProcessInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fg := 2
	if m.shell {
		fg = 1
	}
	return port.ProcessInfo{Pane: memID, ShellPID: 1, FgPGID: fg}, nil
}

func (m *memPane) Screen(context.Context, string, int) (string, error) { return "% ", nil }

func (m *memPane) RenamePane(context.Context, string, string) error { return nil }

func (m *memPane) Start(context.Context, string, string, string, ...string) (port.Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	return m.start(m, m.starts)
}

// claudeIn is a claude in the pane with status st, as the listing shows it.
func claudeIn(st string) port.Pane { return port.Pane{ID: memID, Agent: "claude", Status: st} }

const pending = "unknown" // herdr's launch_pending answer: the start made, no agent yet

// T-1057 (RV-151 #1): the one start decides when the new agent is up by the evidence rule: a listing of the requested
// kind counts as the new agent only after a start the terminal accepted, or in a pane that listed no agent of the kind
// before the attempt. Each row through Launcher.Start with a restart's shell bound (busy retries) and a first prompt
// where it says so.
func TestStartCases(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	for _, c := range []struct {
		name   string
		prompt string
		up     time.Duration // the launcher's bound on an agent coming up (0: bound)
		pane   *memPane
		ok     bool
		starts int    // agent start calls the start made
		said   string // a line the start said ("": none checked)
	}{
		{name: "old exited, shell back, listing current → start accepted → new up: started",
			pane: &memPane{shell: true, start: func(m *memPane, _ int) (port.Pane, error) {
				m.listed = claudeIn("idle")
				return port.Pane{ID: memID, Status: pending}, nil
			}},
			ok: true, starts: 1},
		{name: "old still running, busy until the bound: failed, never started (T-1053)", prompt: wake,
			pane: &memPane{shell: true, listed: claudeIn("idle"), start: func(*memPane, int) (port.Pane, error) {
				return port.Pane{}, codeErr("agent_pane_busy")
			}},
			ok: false, starts: -1, said: "is not an available shell yet (herdr: agent_pane_busy)"},
		{name: "busy, then accepted; the listing shows the old kind a while, then the new one working (RV-151): started",
			prompt: wake,
			pane: &memPane{shell: true, listed: claudeIn("idle"), start: func(m *memPane, n int) (port.Pane, error) {
				if n == 1 {
					return port.Pane{}, codeErr("agent_pane_busy")
				}
				left := 3
				m.onLook = func(m *memPane) {
					if left--; left < 0 {
						m.listed = claudeIn("working")
					}
				}
				return port.Pane{ID: memID, Status: pending}, nil
			}},
			ok: true, starts: 2},
		{name: "accepted but pending (herdr answers before the agent is up), then detected: started",
			pane: &memPane{shell: true, start: func(m *memPane, _ int) (port.Pane, error) {
				left := 3
				m.onLook = func(m *memPane) {
					if left--; left < 0 {
						m.listed = claudeIn("idle")
					}
				}
				return port.Pane{ID: memID, Status: pending}, nil
			}},
			ok: true, starts: 1},
		{name: "first-prompt start: busy, then accepted with the first-prompt timeout, new agent working: started",
			prompt: wake,
			pane: &memPane{shell: true, listed: claudeIn("idle"), start: func(m *memPane, n int) (port.Pane, error) {
				if n == 1 {
					return port.Pane{}, codeErr("agent_pane_busy")
				}
				m.listed = claudeIn("working")
				return port.Pane{}, codeErr("timeout")
			}},
			ok: true, starts: 2, said: "pj-l-01 runs in w1:p3 (working on its first prompt): started"},
		{name: "accepted, never detected within the bound: failed", up: shortWait,
			pane: &memPane{shell: true, start: func(*memPane, int) (port.Pane, error) {
				return port.Pane{ID: memID, Status: pending}, nil
			}},
			ok: false, starts: 1, said: "no claude agent came up in w1:p3 within 60ms"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m := c.pane
			up := c.up
			if up == 0 {
				up = bound
			}
			var out bytes.Buffer
			l := &launch.Launcher{H: m, Out: &out, Poll: time.Millisecond, Up: up,
				Install: func() agents.InstallHost { return agents.InstallHost{} }}
			shell := bound
			if !c.ok {
				shell = shortWait // a failing row waits its bound out
			}
			err := l.Start(context.Background(), launch.Start{Agent: "pj-l-01", Kind: "claude", Pane: memID,
				Label: "pj-l-01", Prompt: c.prompt, Shell: shell})
			m.mu.Lock()
			starts := m.starts
			m.mu.Unlock()
			switch {
			case c.starts > 0:
				eqInt(t, starts, c.starts, "agent start calls")
			case starts < 2:
				t.Errorf("agent start calls %d: want the busy start retried within the bound", starts)
			}
			if c.said != "" {
				contains(t, out.String(), c.said, "said")
			}
			if c.ok {
				if err != nil {
					t.Fatalf("not started: %v\n%s", err, out.String())
				}
				return
			}
			var se *launch.StartError
			if !errors.As(err, &se) {
				t.Fatalf("started, or no StartError: %v\n%s", err, out.String())
			}
			if strings.Contains(out.String(), "): started") {
				t.Errorf("a failed start was said started:\n%s", out.String())
			}
		})
	}
}
