package herdr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/port"
)

// termErr is herdr's error as the port's predicates read it: a pane that is no available shell, a start it gave up
// waiting on.
type termErr string

const (
	errPaneBusy termErr = "pane busy"
	errTimedOut termErr = "timed out"
)

func (e termErr) Error() string  { return "herdr: " + string(e) }
func (e termErr) PaneBusy() bool { return e == errPaneBusy }
func (e termErr) TimedOut() bool { return e == errTimedOut }

// memPane is an in-memory herdr of one pane w1:p3 for a start (agentCalls), scripted per row: what its listing shows
// (read), and each agent start's answer, or none before the start gives up on the call (late).
type memPane struct {
	mu       sync.Mutex
	listed   Pane // the pane as the listing shows it (Agent "": no agent runs there)
	starts   int
	looks    int                                   // reads so far
	accepted int                                   // looks before the accepted start (pendingThen)
	start    func(m *memPane, n int) (Pane, error) // the nth start's answer (1-based), under mu
	onLook   func(m *memPane)                      // after each look, under mu (nil: nothing)
	late     bool                                  // herdr answers no start before the start cancels its call
	cut      int                                   // start calls cancelled unanswered (late)
}

const memID = "w1:p3"

func (m *memPane) read(_ context.Context, target string) (Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.looks++
	if m.onLook != nil {
		m.onLook(m)
	}
	if target != memID {
		return Pane{}, &Error{Kind: NotFound, Op: "agent.get", Msg: "not found"}
	}
	return m.listed, nil
}

// startAgent is the nth start's scripted effect and answer; late, the answer is held until the start cancels the call.
func (m *memPane) startAgent(ctx context.Context, _, _, _ string, _ func(format string, args ...any), _ ...string) (Pane, error) {
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
	return Pane{}, ctx.Err()
}

// claudeIn is a claude in the pane with status st as the listing shows it, launched (herdr's word that no launch is
// pending) or not, with session ("old", "new": which agent a row returned).
func claudeIn(st string, launched bool, session string) Pane {
	return Pane{Pane: port.Pane{ID: memID, Agent: "claude", Status: st, Session: session}, Launched: launched}
}

// pendingAnswer is herdr 0.9.5's at-once answer to an accepted start (live H-L1, H-L12): the launch pending, no kind.
// It is also the record herdr resets the pane's agent to at the acceptance (H-L12, H-L14: none/unknown, pending).
var pendingAnswer = Pane{Pane: port.Pane{ID: memID, Status: "unknown"}}

// a start is up only on herdr's own word, read after it accepted the start: its launch no longer pending (Launched)
// on a listing of the kind that is up. A listing alone (kind and status) never is: while the launch is pending the old
// agent's stale idle may show, and a launch whose CLI exits leaves no agent. A live run (H-L14): the poll runs
// alongside the call, herdr's reset record (pending, status unknown) accepting the start before its answer, and a
// first-prompt start is up on its kind working although herdr keeps the launch pending. Each row through the start,
// a PaneBusy answer started again within a restart's shell bound (as launch does) and a first prompt where it says
// so; a started row checks the observation returned and that the scripted transition happened, not only the result.
func TestStartCases(t *testing.T) {
	t.Parallel()
	const (
		wake      = "[obor job T-1/1/work] call the obor job_next tool now (connected; it returns your job)"
		bound     = 10 * time.Second
		shortWait = 60 * time.Millisecond
	)
	// resetThen: the accepted start's listing is first for looks reads, then next.
	resetThen := func(m *memPane, first Pane, looks int, next Pane) {
		m.listed, m.accepted = first, m.looks
		m.onLook = func(m *memPane) {
			if m.looks-m.accepted > looks {
				m.listed = next
			}
		}
	}
	// pendingThen: the accepted start's listing is the stale old claude, still pending, for looks reads; then next.
	pendingThen := func(m *memPane, looks int, next Pane) {
		resetThen(m, claudeIn("idle", false, "old"), looks, next)
	}
	busyFirst := func(then func(m *memPane) (Pane, error)) func(*memPane, int) (Pane, error) {
		return func(m *memPane, n int) (Pane, error) {
			if n == 1 {
				return Pane{}, errPaneBusy
			}
			return then(m)
		}
	}
	for _, c := range []struct {
		name   string
		prompt string
		up     time.Duration // the start's bound on an agent coming up (0: bound)
		pane   *memPane
		ok     bool
		want   string // a started row's returned agent: its session ("new": the replacement)
		looks  int    // a started row: at least this many looks after the acceptance
		status string // a started row's returned status ("": idle)
		starts int    // agent start calls the start made (-1: started again, at least 2)
		cut    int    // start calls the start cancelled unanswered (a late herdr)
		said   string // a line the start said or its error ("": none checked)
		end    Pane
	}{
		{name: "busy, accepted pending, the old idle listed 3 looks, then the new agent ready: started with the ready one",
			pane: &memPane{listed: claudeIn("idle", true, "old"), start: busyFirst(func(m *memPane) (Pane, error) {
				pendingThen(m, 3, claudeIn("idle", true, "new"))
				return pendingAnswer, nil
			})},
			ok: true, want: "new", looks: 3, starts: 2},
		{name: "busy, accepted pending, the replacement never comes up (pending past the bound): failed",
			up: shortWait,
			pane: &memPane{listed: claudeIn("idle", true, "old"), start: busyFirst(func(m *memPane) (Pane, error) {
				pendingThen(m, 1<<30, Pane{})
				return pendingAnswer, nil
			})},
			ok: false, starts: 2, said: "no claude agent came up in w1:p3 within 60ms", end: claudeIn("idle", false, "old")},
		{name: "accepted, not pending, the new agent ready at once: started with herdr's answer",
			pane: &memPane{start: func(m *memPane, _ int) (Pane, error) {
				m.listed = claudeIn("idle", true, "new")
				return m.listed, nil
			}},
			ok: true, want: "new", starts: 1},
		{name: "first-prompt start, herdr's at-once pending answer, the agent working with its launch pending (live H-L14): started with the working one",
			prompt: wake,
			pane: &memPane{listed: claudeIn("idle", true, "old"), start: func(m *memPane, _ int) (Pane, error) {
				resetThen(m, claudeIn("unknown", false, "new"), 2, claudeIn("working", false, "new"))
				return pendingAnswer, nil
			}},
			ok: true, want: "new", looks: 2, status: "working", starts: 1},
		{name: "first-prompt start, herdr's answer later than Up, its reset record then the agent working (pending): started with the working one",
			prompt: wake, up: bound,
			pane: &memPane{late: true, listed: claudeIn("idle", true, "old"), start: func(m *memPane, _ int) (Pane, error) {
				resetThen(m, pendingAnswer, 2, claudeIn("unknown", false, "new"))
				next := m.onLook
				m.onLook = func(m *memPane) {
					if next(m); m.looks-m.accepted > 4 {
						m.listed = claudeIn("working", false, "new")
					}
				}
				return Pane{}, nil
			}},
			ok: true, want: "new", looks: 4, status: "working", starts: 1, cut: 1},
		{name: "first-prompt start, herdr never answers, only the old agent's stale idle listed: failed", prompt: wake, up: shortWait,
			pane: &memPane{late: true, listed: claudeIn("idle", true, "old"), start: func(*memPane, int) (Pane, error) {
				return Pane{}, nil
			}},
			ok: false, starts: 1, cut: 1, said: "no claude agent came up in w1:p3 within 60ms", end: claudeIn("idle", true, "old")},
		{name: "first-prompt start, herdr's answer later than Up, its reset record, then the CLI exits (no agent): failed",
			prompt: wake, up: shortWait,
			pane: &memPane{late: true, listed: claudeIn("idle", true, "old"), start: func(m *memPane, _ int) (Pane, error) {
				resetThen(m, pendingAnswer, 2, Pane{})
				return Pane{}, nil
			}},
			ok: false, starts: 1, cut: 1, said: "no claude agent came up in w1:p3 within 60ms", end: Pane{}},
		{name: "first-prompt start: busy, then herdr's coded timeout, the new agent working: started with the working one",
			prompt: wake,
			pane: &memPane{listed: claudeIn("idle", true, "old"), start: busyFirst(func(m *memPane) (Pane, error) {
				m.listed = claudeIn("working", true, "new")
				return Pane{}, errTimedOut
			})},
			ok: true, want: "new", status: "working", starts: 2, said: "pj-l-01 runs in w1:p3 (working on its first prompt): started"},
		{name: "old still running, busy until the bound: failed, never started (T-1053)", prompt: wake,
			pane: &memPane{listed: claudeIn("idle", true, "old"), start: func(*memPane, int) (Pane, error) {
				return Pane{}, errPaneBusy
			}},
			ok: false, starts: -1, said: "herdr: pane busy", end: claudeIn("idle", true, "old")},
		{name: "accepted pending, the CLI exits (the pending launch ends, no agent): failed", up: shortWait,
			pane: &memPane{start: func(m *memPane, _ int) (Pane, error) {
				pendingThen(m, 2, Pane{})
				return pendingAnswer, nil
			}},
			ok: false, starts: 1, said: "no claude agent came up in w1:p3 within 60ms", end: Pane{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m := c.pane
			up := c.up
			if up == 0 {
				up = bound
			}
			var out strings.Builder
			var outMu sync.Mutex
			say := func(format string, args ...any) {
				outMu.Lock()
				defer outMu.Unlock()
				fmt.Fprintf(&out, format+"\n", args...)
			}
			shell := bound
			if !c.ok {
				shell = shortWait // a failing row waits its bound out
			}
			s := port.AgentStart{Pane: memID, Label: "pj-l-01", Kind: "claude", Prompt: c.prompt, Bound: up,
				Poll: time.Millisecond, Say: say}
			// launch starts again while the pane is no available shell, within its shell bound
			by := time.Now().Add(shell)
			a, err := start(context.Background(), m, s)
			for port.PaneBusy(err) && time.Now().Before(by) {
				time.Sleep(time.Millisecond)
				a, err = start(context.Background(), m, s)
			}
			m.mu.Lock()
			starts, after, listed, cut := m.starts, m.looks-m.accepted, m.listed, m.cut
			m.mu.Unlock()
			outMu.Lock()
			said := out.String()
			outMu.Unlock()
			if cut != c.cut {
				t.Errorf("start calls cancelled unanswered (herdr's answer later than the start) = %d, want %d", cut, c.cut)
			}
			switch {
			case c.starts > 0 && starts != c.starts:
				t.Errorf("agent start calls = %d, want %d", starts, c.starts)
			case c.starts < 0 && starts < 2:
				t.Errorf("agent start calls %d: want the busy start made again within the bound", starts)
			}
			if c.said != "" && !strings.Contains(said+fmt.Sprint(err), c.said) {
				t.Errorf("said %q, error %v: want %q", said, err, c.said)
			}
			if c.ok {
				if err != nil {
					t.Fatalf("not started: %v\n%s", err, said)
				}
				if a.Session != c.want {
					t.Errorf("the agent the start returned has session %q, want %q", a.Session, c.want)
				}
				status := c.status
				if status == "" {
					status = "idle"
				}
				if a.Agent != "claude" || a.Status != status {
					t.Errorf("returned %+v: want claude %s", a, status)
				}
				if c.prompt == "" && !listed.Launched { // a first-prompt start may return its agent working, still pending
					t.Errorf("listed %+v when the start returned: want its launch over (Launched)", listed)
				}
				if after < c.looks {
					t.Errorf("%d looks after the acceptance: want at least %d (the scripted pending looks)", after, c.looks)
				}
				return
			}
			if err == nil {
				t.Fatalf("started: returned %+v\n%s", a, said)
			}
			if strings.Contains(said, "): started") {
				t.Errorf("a failed start was said started:\n%s", said)
			}
			if listed != c.end {
				t.Errorf("the listing at the end = %+v, want %+v (the scripted transition)", listed, c.end)
			}
		})
	}
}
