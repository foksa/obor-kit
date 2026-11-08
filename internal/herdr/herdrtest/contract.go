package herdrtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"obor/internal/app/port"
)

// Terminal is a terminal adapter as the contract drives it: the core's two ports onto it (PaneControl for launch and
// setup, Panes for serve). Group is the group its pane is in, as a person reads it (port.Pane.Group).
type Terminal struct {
	Control port.PaneControl
	Panes   port.Panes
	Group   string
}

// Row is one row of the terminal contract: Run checks it against a pane that is a shell, in t's group, with a known
// cwd; an error says what broke. Agent: the row starts an agent CLI in the pane (the live herdr session starts none:
// each kind's session proves a start).
type Row struct {
	Name  string
	Agent bool
	Run   func(ctx context.Context, t Terminal, pane string) error
}

// Contract is the terminal contract (T-1044, docs/plan/architecture-target.md §2b): what the core and setup rely on of
// a terminal adapter, in terminal-neutral terms, one row each. internal/herdr's TestTerminalContract runs it against
// this fake; the conformance suite's live herdr tier (tests/conformance, H-C rows) against a throwaway herdr session.
// A second adapter runs the same rows.
var Contract = []Row{
	{Name: "start", Agent: true, Run: func(ctx context.Context, t Terminal, pane string) error {
		const name = "obor-contract"
		if _, err := t.Control.Start(ctx, name, "claude", pane); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		if err := t.Control.Wait(ctx, name, []string{"idle", "done"}, 30*time.Second); err != nil {
			return fmt.Errorf("wait for the started agent to be idle: %w", err)
		}
		a, err := t.Control.Agent(ctx, name)
		if err != nil || a.ID != pane || a.Name != name {
			return fmt.Errorf("the started agent by its name = %+v, %v; want it in pane %s under %s", a, err, pane, name)
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
	{Name: "type", Run: func(ctx context.Context, t Terminal, pane string) error {
		return typed(ctx, t, pane, "typed")
	}},
	{Name: "keys", Run: func(ctx context.Context, t Terminal, pane string) error {
		if err := t.Control.SendKeys(ctx, pane, "ctrl+u"); err != nil {
			return fmt.Errorf("keys to the pane: %w", err)
		}
		if err := t.Control.SendKeys(ctx, missing, "ctrl+u"); !port.NotFound(err) {
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
	{Name: "wait", Run: func(ctx context.Context, t Terminal, pane string) error {
		_, err := t.Control.WaitForOutput(ctx, pane, "obor-contract-never-printed", 300*time.Millisecond)
		if port.ErrorCode(err) != "timeout" {
			return fmt.Errorf("a wait for output never printed: %v; want error code timeout", err)
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

// typed types a command line at the pane's shell that prints obor-contract-<word>, and waits for that output. The
// line quotes word, so only the shell's output matches, never the typed line itself.
func typed(ctx context.Context, t Terminal, pane, word string) error {
	if err := t.Control.Type(ctx, pane, "true && echo obor-contract-\""+word+"\""); err != nil {
		return fmt.Errorf("type: %w", err)
	}
	if _, err := t.Control.WaitForOutput(ctx, pane, "obor-contract-"+word, 10*time.Second); err != nil {
		return fmt.Errorf("wait for the typed line's output: %w", err)
	}
	return nil
}

func find(panes []port.Pane, id string) (port.Pane, bool) {
	i := slices.IndexFunc(panes, func(p port.Pane) bool { return p.ID == id })
	if i < 0 {
		return port.Pane{}, false
	}
	return panes[i], true
}
