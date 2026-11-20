package testkit

// The terminal contract table: one table of rows, each a call on the terminal's port with its expected result or
// refusal, plus what the core and setup rely on of an adapter, in terminal-neutral terms. internal/herdr runs it on
// its fake (TestTerminalContract) and the conformance suite's live herdr tier on a throwaway session (tests/conformance,
// H-C rows). A second adapter runs the same rows.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/app/port"
)

// Terminal is a terminal adapter as the contract drives it: the core's ports onto it (PaneControl for launch and
// setup, Panes for serve, TerminalSetup for a placement). Group is the group its pane is in, as a person reads it
// (port.Pane.Group); Cwd is that pane's cwd, where a placement puts a new pane. Stale, a test terminal's hook (nil on a
// live one), makes pane's visible screen stale: what its shell prints from then on reaches its recent output only, as
// a terminal's visible screen after an agent exited; an error when it has no such pane.
type Terminal struct {
	Control port.PaneControl
	Panes   port.Panes
	Setup   port.TerminalSetup
	Group   string
	Cwd     string
	Stale   func(pane string) error
}

// Row is one row of the terminal contract: Run checks it against a pane that is a shell, in t's group, with a known
// cwd; an error says what broke. Agent: the row starts an agent CLI in the pane (the live herdr session starts none:
// each kind's session proves a start).
type Row struct {
	Name  string
	Agent bool
	Run   func(ctx context.Context, t Terminal, pane string) error
}

// Contract is the terminal contract (docs/plan/architecture-target.md §2b): what the core and setup rely on of a
// terminal adapter, in terminal-neutral terms, one row each.
var Contract = []Row{
	{Name: "start", Agent: true, Run: func(ctx context.Context, t Terminal, pane string) error {
		const name = "obor-contract"
		if err := start(ctx, t, name, pane); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		if err := t.Control.WaitStatus(ctx, pane, []string{"idle", "done"}, 30*time.Second); err != nil {
			return fmt.Errorf("wait for the started agent to be idle: %w", err)
		}
		a, err := t.Control.Pane(ctx, pane)
		if err != nil || a.ID != pane || a.Agent != "claude" {
			return fmt.Errorf("the pane after the start = %+v, %v; want pane %s running a claude agent", a, err, pane)
		}
		return nil
	}},
	{Name: "status", Run: func(ctx context.Context, t Terminal, pane string) error {
		snap, err := t.Panes.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		p, ok := find(snap.Panes, pane)
		switch {
		case !ok:
			return fmt.Errorf("the snapshot lists no pane %s", pane)
		case p.Group != t.Group || p.Cwd == "":
			return fmt.Errorf("pane %s in the snapshot = %+v; want it in group %q with its cwd", pane, p, t.Group)
		case p.Agent != "" || p.Busy():
			return fmt.Errorf("pane %s in the snapshot = %+v; want a shell (no agent, not busy)", pane, p)
		}
		return nil
	}},
	{Name: "instance", Run: func(ctx context.Context, t Terminal, _ string) error {
		a, err := t.Panes.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
		b, err := t.Panes.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("snapshot again: %w", err)
		}
		switch {
		case a.Instance == "":
			return fmt.Errorf("the snapshot's instance is empty: %+v", a)
		case a.Instance != b.Instance:
			return fmt.Errorf("the instance changed between two snapshots: %q, then %q", a.Instance, b.Instance)
		}
		return nil
	}},
	{Name: "place", Run: func(ctx context.Context, t Terminal, _ string) error {
		switch {
		case t.Setup == nil:
			return errors.New("the terminal has no setup port: no placement")
		case t.Cwd == "":
			return errors.New("the contract's terminal has no cwd to place a pane in")
		}
		id, err := t.Setup.Place(ctx, port.Placement{Kind: port.KindRole, Cwd: t.Cwd,
			Project: port.PlaceProject{Name: t.Group, Root: t.Cwd, MaxPanes: 16,
				Has:    func(cwd string) bool { return cwd == t.Cwd || strings.HasPrefix(cwd, t.Cwd+string(filepath.Separator)) },
				KindOf: func(string) port.PaneKind { return "" }}})
		if err != nil {
			return fmt.Errorf("place: %w", err)
		}
		if id == "" {
			return errors.New("place: no pane id")
		}
		snap, err := t.Panes.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("snapshot after place: %w", err)
		}
		p, ok := find(snap.Panes, id)
		switch {
		case !ok:
			return fmt.Errorf("the snapshot lists no pane %s placed", id)
		case p.Group != t.Group || p.Cwd != t.Cwd:
			return fmt.Errorf("the placed pane = %+v; want it in group %q with cwd %s", p, t.Group, t.Cwd)
		}
		return nil
	}},
	{Name: "type", Run: func(ctx context.Context, t Terminal, pane string) error {
		return typed(ctx, t, pane, "typed")
	}},
	{Name: "type with guard", Run: func(ctx context.Context, t Terminal, pane string) error {
		const what = "obor-contract-guarded"
		if began, err := t.Control.Prompt(ctx, pane, what, false); began || !port.NotFound(err) {
			return fmt.Errorf("a prompt at a shell: began %v, %v; want it refused before typing (not found)", began, err)
		}
		text, err := t.Control.Screen(ctx, pane, 40)
		if err != nil || strings.Contains(text, what) {
			return fmt.Errorf("the guarded type typed: screen %q, %v", text, err)
		}
		return nil
	}},
	{Name: "keys", Run: func(ctx context.Context, t Terminal, pane string) error {
		if err := t.Control.SendKeys(ctx, pane, port.KeyCtrlU); err != nil {
			return fmt.Errorf("keys to the pane: %w", err)
		}
		if err := t.Control.SendKeys(ctx, missing, port.KeyCtrlU); !port.NotFound(err) {
			return fmt.Errorf("keys to a missing pane: %v; want a not-found error", err)
		}
		return nil
	}},
	{Name: "screen", Run: func(ctx context.Context, t Terminal, pane string) error {
		if err := typed(ctx, t, pane, "screen"); err != nil {
			return err
		}
		text, err := t.Control.Screen(ctx, pane, 40)
		if err != nil || !strings.Contains(text, "obor-contract-screen") {
			return fmt.Errorf("screen = %q, %v; want the printed word on it", text, err)
		}
		return nil
	}},
	{Name: "recent output", Run: func(ctx context.Context, t Terminal, pane string) error {
		// The env-line handshake's semantics: a line the shell printed is in the pane's recent output, also when the
		// visible screen went stale (Stale; a live terminal has no hook, its row reads a fresh screen's recent output).
		if t.Stale != nil {
			if err := t.Stale(pane); err != nil {
				return fmt.Errorf("make the visible screen stale: %w", err)
			}
		}
		if err := typed(ctx, t, pane, "recent"); err != nil {
			return err
		}
		if t.Stale != nil {
			if text, err := t.Control.Screen(ctx, pane, 40); err != nil || strings.Contains(text, "obor-contract-recent") {
				return fmt.Errorf("the stale visible screen = %q, %v; want it without the line printed after it went stale",
					text, err)
			}
		}
		return nil
	}},
	{Name: "pane busy", Agent: true, Run: func(ctx context.Context, t Terminal, pane string) error {
		// A pane that runs an agent is not an available shell: a start there is refused as PaneBusy, never a start.
		if err := start(ctx, t, "obor-contract-busy", pane); err != nil {
			return fmt.Errorf("a start into the pane's shell: %w", err)
		}
		if err := start(ctx, t, "obor-contract-busy-2", pane); !port.PaneBusy(err) {
			return fmt.Errorf("a start into the pane that runs an agent: %v; want a pane-busy error", err)
		}
		return nil
	}},
	{Name: "named", Agent: true, Run: func(ctx context.Context, t Terminal, pane string) error {
		// The terminal's holder of a name: the pane its live agent runs in; a name no live agent holds is NotFound.
		const name = "obor-contract-named"
		if _, err := t.Control.Named(ctx, name); !port.NotFound(err) {
			return fmt.Errorf("the holder of a name no agent holds: %v; want a not-found error", err)
		}
		if err := start(ctx, t, name, pane); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		a, err := t.Control.Named(ctx, name)
		if err != nil || a.ID != pane || a.Agent != "claude" {
			return fmt.Errorf("the holder of the started name = %+v, %v; want pane %s running a claude agent", a, err, pane)
		}
		return nil
	}},
	{Name: "rename", Run: func(ctx context.Context, t Terminal, pane string) error {
		for _, label := range []string{"obor-contract-label", ""} {
			if err := t.Control.RenamePane(ctx, pane, label); err != nil {
				return fmt.Errorf("rename to %q: %w", label, err)
			}
			panes, err := t.Control.Panes(ctx)
			if p, ok := find(panes, pane); err != nil || !ok || p.Label != label {
				return fmt.Errorf("pane %s after a rename to %q = %+v, %v", pane, label, p, err)
			}
		}
		return nil
	}},
	{Name: "process info", Run: func(ctx context.Context, t Terminal, pane string) error {
		pi, err := t.Control.ProcessInfo(ctx, pane)
		if err != nil || pi.ShellPID == 0 || !pi.AtShell() {
			return fmt.Errorf("process info = %+v, %v; want its shell, in the foreground", pi, err)
		}
		return nil
	}},
	{Name: "notify", Run: func(ctx context.Context, t Terminal, _ string) error {
		var shown *port.NotShown
		if err := t.Panes.Notify(ctx, "obor contract", "a terminal contract row"); err != nil && !errors.As(err, &shown) {
			return fmt.Errorf("notify: %v; want shown, or not shown with a reason", err)
		}
		return nil
	}},
	{Name: "reconnect", Run: func(ctx context.Context, t Terminal, pane string) error {
		for i := range 2 { // a stream that ended is followed by a new one, from a new snapshot
			st, err := t.Panes.Subscribe(ctx)
			if err != nil {
				return fmt.Errorf("subscribe %d: %w", i+1, err)
			}
			_, ok := find(st.Snapshot.Panes, pane)
			st.Close()
			if !ok {
				return fmt.Errorf("subscribe %d: its snapshot lists no pane %s", i+1, pane)
			}
			select {
			case _, open := <-st.C:
				for open {
					_, open = <-st.C
				}
			case <-time.After(10 * time.Second):
				return fmt.Errorf("subscribe %d: its events did not end after Close", i+1)
			}
			if st.Err() == nil {
				return fmt.Errorf("subscribe %d: an ended stream says no why", i+1)
			}
		}
		return nil
	}},
}

// missing is a pane id no terminal has.
const missing = "obor-contract-missing:p0"

// start starts a claude agent named name in pane through the terminal's AgentStarter (launch asks its PaneControl
// for one).
func start(ctx context.Context, t Terminal, name, pane string) error {
	st, ok := t.Control.(port.AgentStarter)
	if !ok {
		return fmt.Errorf("the terminal's PaneControl is no AgentStarter")
	}
	_, err := st.Start(ctx, port.AgentStart{Pane: pane, Label: name, Kind: "claude"})
	return err
}

// typed types a command line at the pane's shell that prints obor-contract-<word>, and reads the pane's recent output
// (the env-line handshake's read, not the visible screen) until that output shows (10 s). The line quotes word,
// so only the shell's output matches, never the typed line itself.
func typed(ctx context.Context, t Terminal, pane, word string) error {
	if err := t.Control.Write(ctx, pane, "true && echo obor-contract-\""+word+"\"", true); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		text, err := t.Control.Recent(ctx, pane, 40)
		if err == nil && strings.Contains(text, "obor-contract-"+word) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the typed line's output never showed: recent output %q, %v", text, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func find(panes []port.Pane, id string) (port.Pane, bool) {
	i := slices.IndexFunc(panes, func(p port.Pane) bool { return p.ID == id })
	if i < 0 {
		return port.Pane{}, false
	}
	return panes[i], true
}
