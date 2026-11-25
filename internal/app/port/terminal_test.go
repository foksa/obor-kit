package port

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ctx is the guard's tests' context.
var ctx = context.Background()

// control is an in-memory PaneControl for the guard's rows: the pane it reports (or the error its read fails with) and
// each text its Prompt typed.
type control struct {
	pane    Pane
	readErr error
	typed   []string
}

var _ PaneControl = (*control)(nil)

func (c *control) Prompt(_ context.Context, pane, text string, _ bool) (bool, error) {
	c.typed = append(c.typed, pane+": "+text)
	return true, nil
}

func (c *control) Pane(context.Context, string) (Pane, error) { return c.pane, c.readErr }
func (c *control) Panes(context.Context) ([]Pane, error)      { return nil, nil }
func (c *control) ProcessInfo(context.Context, string) (ProcessInfo, error) {
	return ProcessInfo{}, nil
}
func (c *control) Screen(context.Context, string, int) (string, error) { return "", nil }
func (c *control) Recent(context.Context, string, int) (string, error) { return "", nil }
func (c *control) WaitStatus(context.Context, string, []string, time.Duration) error {
	return nil
}
func (c *control) RenamePane(context.Context, string, string) error { return nil }
func (c *control) Write(context.Context, string, string, bool) error {
	return nil
}
func (c *control) SendKeys(context.Context, string, ...string) error { return nil }
func (c *control) Named(context.Context, string) (Pane, error)       { return Pane{}, nil }

// starter is a control that also starts agents, as a terminal's adapter does.
type starter struct {
	control
	starts []AgentStart
}

func (s *starter) Start(_ context.Context, a AgentStart) (Pane, error) {
	s.starts = append(s.starts, a)
	return Pane{ID: a.Pane}, nil
}

// The guard in front of every terminal's Prompt: a pane that runs no agent is refused as NotFound, a blocked one as
// Blocked with its Note in the message (or without one), a failed read of the pane is returned; each of them before any
// key or text. An idle agent is typed to through the adapter's own Prompt.
func TestGuardPrompts(t *testing.T) {
	t.Parallel()
	readErr := errors.New("hc terminal.get: no core answered")
	for _, tc := range []struct {
		name  string
		c     *control
		check func(err error) bool
		want  string // the refusal's message; "" when the prompt goes through
	}{
		{"no agent", &control{pane: Pane{ID: "t1"}}, NotFound, "t1 runs no agent"},
		{"blocked", &control{pane: Pane{ID: "t1", Agent: "claude", Status: "blocked",
			Note: "permission: approve Bash: ls"}}, Blocked, "t1 is blocked (permission: approve Bash: ls): typing would answer its dialog"},
		{"blocked with no note", &control{pane: Pane{ID: "t1", Agent: "claude", Status: "blocked"}}, Blocked,
			"t1 is blocked: typing would answer its dialog"},
		{"pane read fails", &control{readErr: readErr}, func(err error) bool { return errors.Is(err, readErr) },
			readErr.Error()},
		{"idle", &control{pane: Pane{ID: "t1", Agent: "claude", Status: "idle"}}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			began, err := GuardPrompts(tc.c).Prompt(ctx, "t1", "wake", true)
			if tc.check == nil {
				if !began || err != nil {
					t.Fatalf("Prompt = began %v, %v; want it typed", began, err)
				}
				if got := tc.c.typed; len(got) != 1 || got[0] != "t1: wake" {
					t.Errorf("typed %q, want the wake through the adapter's Prompt", got)
				}
				return
			}
			if began || !tc.check(err) {
				t.Fatalf("Prompt = began %v, %v; want a refusal before any typing", began, err)
			}
			if err.Error() != tc.want {
				t.Errorf("refusal = %q, want %q", err.Error(), tc.want)
			}
			if got := tc.c.typed; len(got) != 0 {
				t.Errorf("typed %q, want nothing", got)
			}
		})
	}
}

// A terminal's own agent start survives the guard, and one that starts none does not gain one: launch asks its
// PaneControl for AgentStarter, and a terminal without one is refused as such.
func TestGuardPromptsKeepsAgentStart(t *testing.T) {
	t.Parallel()
	c := &starter{control: control{pane: Pane{ID: "t1", Agent: "claude", Status: "idle"}}}
	st, ok := GuardPrompts(c).(AgentStarter)
	if !ok {
		t.Fatalf("GuardPrompts(%T) is no AgentStarter; the terminal starts agents", c)
	}
	a := AgentStart{Pane: "t1", Kind: "claude"}
	if p, err := st.Start(ctx, a); err != nil || p.ID != "t1" {
		t.Errorf("Start through the guard = %+v, %v; want the terminal's start", p, err)
	}
	if len(c.starts) != 1 || c.starts[0].Pane != a.Pane || c.starts[0].Kind != a.Kind {
		t.Errorf("started %+v, want the terminal's own Start asked once", c.starts)
	}
	if _, ok := GuardPrompts(&control{}).(AgentStarter); ok {
		t.Error("GuardPrompts of a terminal with no agent start gained one")
	}
}
