package shellpane_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/shellpane"
)

var ctx = context.Background()

// term is a shell pane as a start sees it: shell is what a read shows before the command line is typed, agent what it
// shows after (the program the line ran, as it reports itself), each read of it once at most every poll. typeErr
// fails the typing (began: the line may have reached the pane).
type term struct {
	mu      sync.Mutex
	shell   shellpane.Look
	agent   shellpane.Look
	typed   []string
	marks   []string
	typeErr error
	began   bool
}

func (t *term) Name() string { return "tm" }

func (t *term) Look(context.Context, string) (shellpane.Look, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.typed) == 0 {
		return t.shell, nil
	}
	return t.agent, nil
}

func (t *term) Mark(_ context.Context, pane, label, kind string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.marks = append(t.marks, pane+" "+label+" "+kind)
	return nil
}

func (t *term) Type(_ context.Context, _ string, line string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.typeErr != nil {
		return t.began, t.typeErr
	}
	t.typed = append(t.typed, line)
	return true, nil
}

// atShell is a pane whose shell holds the foreground; running a pane whose program reports pane's status.
var atShell = shellpane.Look{Up: true, Pane: port.Pane{ID: "p1"}}

func running(program string, reported bool, p port.Pane) shellpane.Look {
	p.ID = "p1"
	return shellpane.Look{Up: true, Program: program, Reported: reported, Pane: p}
}

// A start types the kind's program and its argv, each word quoted, at the pane's shell after marking the pane with
// the agent and its kind, and is up once the program in the foreground reports a status naming the kind: idle, or
// working on its first prompt. A program that reports blocked waits at a dialog (NotReady at once: Claude's trust
// dialog); one that reports nothing by the bound may wait at a silent one (NotReady at the bound: Codex's trust
// dialog), as may one whose status names another kind; a shell that never gives the foreground up is TimedOut. Each
// failure after the typing is Accepted. A pane whose shell is not up, or that runs a program, is PaneBusy, nothing
// marked or typed.
func TestStart(t *testing.T) {
	idle := port.Pane{Agent: "claude", Status: "idle", Label: "pj-w1"}
	for _, tc := range []struct {
		name   string
		shell  shellpane.Look
		agent  shellpane.Look
		prompt string
		check  func(err error) bool // nil: up
		after  time.Duration        // when the start returns
		typed  bool
	}{
		{name: "up", shell: atShell, agent: running("claude", true, idle), typed: true},
		{name: "working on its first prompt", shell: atShell, prompt: "go", typed: true,
			agent: running("claude", true, port.Pane{Agent: "claude", Status: "working", Label: "pj-w1"})},
		{name: "trust dialog", shell: atShell, typed: true,
			agent: running("claude", true, port.Pane{Agent: "claude", Status: "blocked", Note: "permission: trust this folder"}),
			check: func(err error) bool {
				return port.NotReady(err) && port.Accepted(err) && strings.Contains(err.Error(), "permission: trust this folder")
			}},
		{name: "silent dialog", shell: atShell, agent: running("claude", false, port.Pane{Status: "unknown"}), typed: true,
			check: func(err error) bool { return port.NotReady(err) && port.Accepted(err) }, after: 5 * time.Second},
		{name: "another kind's status", shell: atShell, agent: running("codex", true, port.Pane{Agent: "codex", Status: "idle"}),
			typed: true, check: func(err error) bool { return port.NotReady(err) && port.Accepted(err) }, after: 5 * time.Second},
		{name: "no agent", shell: atShell, agent: atShell, typed: true,
			check: func(err error) bool { return port.TimedOut(err) && port.Accepted(err) }, after: 5 * time.Second},
		{name: "shell not up", shell: shellpane.Look{Pane: port.Pane{ID: "p1"}},
			check: func(err error) bool { return port.PaneBusy(err) && !port.Accepted(err) }},
		{name: "a program runs there", shell: running("vim", false, port.Pane{Status: "unknown"}),
			check: func(err error) bool {
				return port.PaneBusy(err) && !port.Accepted(err) && strings.Contains(err.Error(), "vim runs there")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tm := &term{shell: tc.shell, agent: tc.agent}
				began := time.Now()
				p, err := shellpane.Start(ctx, tm, port.AgentStart{Pane: "p1", Label: "pj-w1", Kind: "claude", Program: "claude",
					Argv: []string{"--model", "a b's"}, Prompt: tc.prompt, Bound: 5 * time.Second, Poll: time.Second})
				if d := time.Since(began); d != tc.after {
					t.Errorf("Start returned after %s, want %s", d, tc.after)
				}
				switch {
				case tc.check == nil && (err != nil || p.ID != "p1" || p.Agent != "claude" || p.Label != "pj-w1"):
					t.Errorf("Start = %+v, %v; want p1 running claude labelled pj-w1", p, err)
				case tc.check != nil && !tc.check(err):
					t.Errorf("Start = %+v, %v; want the %s failure", p, err, tc.name)
				}
				typed, marks := []string(nil), []string(nil)
				if tc.typed {
					typed, marks = []string{`claude --model 'a b'\''s'`}, []string{"p1 pj-w1 claude"}
				}
				if !slices.Equal(tm.typed, typed) || !slices.Equal(tm.marks, marks) {
					t.Errorf("typed %q, marked %q; want %q, %q", tm.typed, tm.marks, typed, marks)
				}
			})
		})
	}
}

// A start reads no kind: it types the program the caller gives (a plugin kind the terminal's kinds do not hold yet
// starts), and with no program it is refused before anything is marked or typed: the program is the launcher's to
// give, and no default stands in for it.
func TestStartTypesTheStartsProgram(t *testing.T) {
	tm := &term{shell: atShell, agent: running("opencode", true, port.Pane{Agent: "opencode", Status: "idle"})}
	if p, err := shellpane.Start(ctx, tm, port.AgentStart{Pane: "p1", Kind: "opencode", Program: "opencode"}); err != nil || p.Agent != "opencode" {
		t.Errorf("Start = %+v, %v; want opencode up in p1", p, err)
	}
	if !slices.Equal(tm.typed, []string{"opencode"}) {
		t.Errorf("typed %q, want the program alone", tm.typed)
	}
	tm = &term{shell: atShell}
	_, err := shellpane.Start(ctx, tm, port.AgentStart{Pane: "p1", Label: "pj-w1", Kind: "claude"})
	if err == nil || err.Error() != "tm: no program for agent kind claude" {
		t.Errorf("Start with no program = %v; want the no-program refusal", err)
	}
	if len(tm.typed) != 0 || len(tm.marks) != 0 {
		t.Errorf("typed %q, marked %q; want nothing", tm.typed, tm.marks)
	}
}

// A typing that fails is Accepted only when the line may have reached the pane (began): a refusal before any key is
// the terminal's error alone.
func TestStartTypingFails(t *testing.T) {
	for _, began := range []bool{true, false} {
		tm := &term{shell: atShell, typeErr: errors.New("send failed"), began: began}
		_, err := shellpane.Start(ctx, tm, port.AgentStart{Pane: "p1", Kind: "claude", Program: "claude"})
		if err == nil || port.Accepted(err) != began || !strings.Contains(err.Error(), "send failed") {
			t.Errorf("began %v: Start = %v (accepted %v); want the typing's error, accepted %v", began, err, port.Accepted(err), began)
		}
	}
}

func TestCommandLine(t *testing.T) {
	if got := shellpane.CommandLine("claude", []string{"--model", "a b's", ""}); got != `claude --model 'a b'\''s' ''` {
		t.Errorf("CommandLine = %s", got)
	}
}
