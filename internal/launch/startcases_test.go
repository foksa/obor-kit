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
// its shell has the foreground (ProcessInfo), and each agent start's answer, or none before the start gives up on the
// call (late). Any other call panics (the nil embedded interface): the start makes none.
type memPane struct {
	port.PaneControl
	mu       sync.Mutex
	listed   port.Pane // the pane's agent as the listing shows it (Agent "" : no agent, an error)
	shell    bool
	starts   int
	looks    int                                        // Agent calls so far
	accepted int                                        // looks before the accepted start (pendingThen)
	start    func(m *memPane, n int) (port.Pane, error) // the nth start's answer (1-based), under mu
	onLook   func(m *memPane)                           // after each look, under mu (nil: nothing)
	late     bool                                       // herdr answers no start before the start cancels its call
	cut      int                                        // start calls cancelled unanswered (late)
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

// Start is the nth start's scripted effect and answer; late, the answer is held until the start cancels the call.
func (m *memPane) Start(ctx context.Context, _, _, _ string, _ ...string) (port.Pane, error) {
	m.mu.Lock()
	m.starts++
	a, err := m.start(m, m.starts)
	late := m.late
	m.mu.Unlock()
	if !late {
		return a, err
	}
	<-ctx.Done()
	m.mu.Lock()
	m.cut++
	m.mu.Unlock()
	return port.Pane{}, ctx.Err()
}

// claudeIn is a claude in the pane with status st as the listing shows it, launched (the terminal's word that no
// launch is pending) or not, under herdr name name ("old", "new": which agent a row returned).
func claudeIn(st string, launched bool, name string) port.Pane {
	return port.Pane{ID: memID, Agent: "claude", Status: st, Launched: launched, Name: name}
}

// pendingAnswer is herdr 0.9.5's at-once answer to an accepted start (live H-L1, H-L12): the launch pending, no kind.
// It is also the record herdr resets the pane's agent to at the acceptance (H-L12, H-L14: none/unknown, pending).
var pendingAnswer = port.Pane{ID: memID, Status: "unknown"}

// T-1058 (RV-151 #1): a start is up only on the terminal's own word, read after it accepted the start: its launch no
// longer pending (Launched) on a listing of the kind that is up. A listing alone (kind and status) never is: while the
// launch is pending the old agent's stale idle may show, and a launch whose CLI exits leaves no agent. T-1059 (live
// H-L14): the poll runs alongside the call, herdr's reset record (pending, status unknown) accepting the start before
// its answer, and a first-prompt start is up on its kind working although herdr keeps the launch pending. Each row
// through the one start with a restart's shell bound (busy retries) and a first prompt where it says so; a started row
// checks the observation returned and that the scripted transition happened, not only the result.
func TestStartCases(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	// resetThen: the accepted start's listing is first for looks reads, then next.
	resetThen := func(m *memPane, first port.Pane, looks int, next port.Pane) {
		m.listed, m.accepted = first, m.looks
		m.onLook = func(m *memPane) {
			if m.looks-m.accepted > looks {
				m.listed = next
			}
		}
	}
	// pendingThen: the accepted start's listing is the stale old claude, still pending, for looks reads; then next.
	pendingThen := func(m *memPane, looks int, next port.Pane) {
		resetThen(m, claudeIn("idle", false, "old"), looks, next)
	}
	busyFirst := func(then func(m *memPane) (port.Pane, error)) func(*memPane, int) (port.Pane, error) {
		return func(m *memPane, n int) (port.Pane, error) {
			if n == 1 {
				return port.Pane{}, codeErr("agent_pane_busy")
			}
			return then(m)
		}
	}
	for _, c := range []struct {
		name   string
		prompt string
		up     time.Duration // the launcher's bound on an agent coming up (0: bound)
		pane   *memPane
		ok     bool
		want   string // a started row's returned agent: its herdr name ("new": the replacement)
		looks  int    // a started row: at least this many looks after the acceptance
		status string // a started row's returned status ("": idle)
		starts int    // agent start calls the start made (-1: retried, at least 2)
		cut    int    // start calls the start cancelled unanswered (a late herdr)
		said   string // a line the start said ("": none checked)
		end    port.Pane
	}{
		{name: "busy, accepted pending, the old idle listed 3 looks, then the new agent ready: started with the ready one",
			pane: &memPane{shell: true, listed: claudeIn("idle", true, "old"), start: busyFirst(func(m *memPane) (port.Pane, error) {
				pendingThen(m, 3, claudeIn("idle", true, "new"))
				return pendingAnswer, nil
			})},
			ok: true, want: "new", looks: 3, starts: 2},
		{name: "busy, accepted pending, the replacement never comes up (pending past the bound): failed",
			up: shortWait,
			pane: &memPane{shell: true, listed: claudeIn("idle", true, "old"), start: busyFirst(func(m *memPane) (port.Pane, error) {
				pendingThen(m, 1<<30, port.Pane{})
				return pendingAnswer, nil
			})},
			ok: false, starts: 2, said: "no claude agent came up in w1:p3 within 60ms", end: claudeIn("idle", false, "old")},
		{name: "accepted, not pending, the new agent ready at once: started with herdr's answer",
			pane: &memPane{shell: true, start: func(m *memPane, _ int) (port.Pane, error) {
				m.listed = claudeIn("idle", true, "new")
				return m.listed, nil
			}},
			ok: true, want: "new", starts: 1},
		{name: "first-prompt start, herdr's at-once pending answer, the agent working with its launch pending (live H-L14): started with the working one",
			prompt: wake,
			pane: &memPane{shell: true, listed: claudeIn("idle", true, "old"), start: func(m *memPane, _ int) (port.Pane, error) {
				resetThen(m, claudeIn("unknown", false, "new"), 2, claudeIn("working", false, "new"))
				return pendingAnswer, nil
			}},
			ok: true, want: "new", looks: 2, status: "working", starts: 1},
		{name: "first-prompt start, herdr's answer later than Up, its reset record then the agent working (pending): started with the working one",
			prompt: wake, up: bound,
			pane: &memPane{shell: true, late: true, listed: claudeIn("idle", true, "old"), start: func(m *memPane, _ int) (port.Pane, error) {
				resetThen(m, pendingAnswer, 2, claudeIn("unknown", false, "new"))
				next := m.onLook
				m.onLook = func(m *memPane) {
					if next(m); m.looks-m.accepted > 4 {
						m.listed = claudeIn("working", false, "new")
					}
				}
				return port.Pane{}, nil
			}},
			ok: true, want: "new", looks: 4, status: "working", starts: 1, cut: 1},
		{name: "first-prompt start, herdr never answers, only the old agent's stale idle listed: failed", prompt: wake, up: shortWait,
			pane: &memPane{shell: true, late: true, listed: claudeIn("idle", true, "old"), start: func(*memPane, int) (port.Pane, error) {
				return port.Pane{}, nil
			}},
			ok: false, starts: 1, cut: 1, said: "no claude agent came up in w1:p3 within 60ms", end: claudeIn("idle", true, "old")},
		{name: "first-prompt start, herdr's answer later than Up, its reset record, then the CLI exits (no agent): failed",
			prompt: wake, up: shortWait,
			pane: &memPane{shell: true, late: true, listed: claudeIn("idle", true, "old"), start: func(m *memPane, _ int) (port.Pane, error) {
				resetThen(m, pendingAnswer, 2, port.Pane{})
				return port.Pane{}, nil
			}},
			ok: false, starts: 1, cut: 1, said: "no claude agent came up in w1:p3 within 60ms", end: port.Pane{}},
		{name: "first-prompt start: busy, then herdr's coded timeout, the new agent working: started with the working one",
			prompt: wake,
			pane: &memPane{shell: true, listed: claudeIn("idle", true, "old"), start: busyFirst(func(m *memPane) (port.Pane, error) {
				m.listed = claudeIn("working", true, "new")
				return port.Pane{}, codeErr("timeout")
			})},
			ok: true, want: "new", status: "working", starts: 2, said: "pj-l-01 runs in w1:p3 (working on its first prompt): started"},
		{name: "old still running, busy until the bound: failed, never started (T-1053)", prompt: wake,
			pane: &memPane{shell: true, listed: claudeIn("idle", true, "old"), start: func(*memPane, int) (port.Pane, error) {
				return port.Pane{}, codeErr("agent_pane_busy")
			}},
			ok: false, starts: -1, said: "is not an available shell yet (herdr: agent_pane_busy)", end: claudeIn("idle", true, "old")},
		{name: "accepted pending, the CLI exits (the pending launch ends, no agent): failed", up: shortWait,
			pane: &memPane{shell: true, start: func(m *memPane, _ int) (port.Pane, error) {
				pendingThen(m, 2, port.Pane{})
				return pendingAnswer, nil
			}},
			ok: false, starts: 1, said: "no claude agent came up in w1:p3 within 60ms", end: port.Pane{}},
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
			a, err := l.StartSeen(context.Background(), launch.Start{Agent: "pj-l-01", Kind: "claude", Pane: memID,
				Label: "pj-l-01", Prompt: c.prompt, Shell: shell})
			m.mu.Lock()
			starts, after, listed, cut := m.starts, m.looks-m.accepted, m.listed, m.cut
			m.mu.Unlock()
			eqInt(t, cut, c.cut, "start calls cancelled unanswered (herdr's answer later than the start)")
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
				eq(t, a.Name, c.want, "the agent the start returned (its herdr name)")
				status := c.status
				if status == "" {
					status = "idle"
				}
				if a.Agent != "claude" || a.Status != status {
					t.Errorf("returned %+v: want claude %s", a, status)
				}
				if c.prompt == "" && !a.Launched { // a first-prompt start may return its agent working, still pending
					t.Errorf("returned %+v: want its launch over (Launched)", a)
				}
				if after < c.looks {
					t.Errorf("%d looks after the acceptance: want at least %d (the scripted pending looks)", after, c.looks)
				}
				return
			}
			var se *launch.StartError
			if !errors.As(err, &se) {
				t.Fatalf("started, or no StartError: %v (returned %+v)\n%s", err, a, out.String())
			}
			if strings.Contains(out.String(), "): started") {
				t.Errorf("a failed start was said started:\n%s", out.String())
			}
			if listed != c.end {
				t.Errorf("the listing at the end = %+v, want %+v (the scripted transition)", listed, c.end)
			}
		})
	}
}
