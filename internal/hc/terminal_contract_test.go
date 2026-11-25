package hc_test

import (
	"context"
	"testing"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/herdrapp/herdrapptest"
	"obor/internal/osnotify"
	"obor/internal/testkit"
)

// shellCore is a core with t1, a kept shell terminal (pid 41000) at /proj labelled obor.project=proj, and claude as a
// program that reports idle once up, as Claude 2.1.329 did (docs/research/hc-probes.md P1).
func shellCore() *hctest.Core {
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", PID: 41000, Cwd: "/proj", Labels: map[string]string{hc.LabelProject: "proj"},
		Foreground: &hc.Foreground{PID: 41000, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: "/proj"}})
	core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
	return core
}

// contractTerminal is hc's adapter over shellCore, the herdr-app window bridged to it (its new tabs' terminals start
// on the core), as cmd/obor builds the terminal: the guard in front of its Prompt. That is the terminal every contract
// row runs against. hc's read is one text (Recent is Screen), so it has no stale view to make (Stale nil).
func contractTerminal() testkit.Terminal {
	core, win := shellCore(), herdrapptest.New()
	win.StartTerminals(core.AppTerminals(false))
	c := core.Client()
	c.App = win.Client()
	// The notify row shows nothing on the machine running the tests: its notifier is a stub.
	panes := hc.Panes{C: c, Notifier: osnotify.Notifier{Post: func(context.Context, string, string) error { return nil }}}
	return testkit.Terminal{Control: port.GuardPrompts(c), Setup: c, Panes: panes, Group: "proj", Cwd: "/proj"}
}

// TestTerminalContract runs the terminal contract (testkit.Contract) against hc's adapter over hctest, as
// internal/herdr runs it over herdrtest, the place row over herdr-app's in-memory window.
func TestTerminalContract(t *testing.T) {
	t.Parallel()
	for _, row := range testkit.Contract {
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			if err := row.Run(context.Background(), contractTerminal(), "t1"); err != nil {
				t.Error(err)
			}
		})
	}
	if len(testkit.Contract) < 15 {
		t.Errorf("precondition: %d contract rows, want the 15 herdr runs", len(testkit.Contract))
	}
}

// TestTerminalContractFailsOnAMissingPane: every row that addresses the pane fails for a terminal the core does not
// have, so a row that passes on t1 checked something (the rows herdr's twin of this test leaves out, for the same
// reasons).
func TestTerminalContractFailsOnAMissingPane(t *testing.T) {
	t.Parallel()
	for _, row := range testkit.Contract {
		switch row.Name {
		case "notify", "instance", "type with guard", "place":
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			if err := row.Run(context.Background(), contractTerminal(), "t9"); err == nil {
				t.Errorf("row %s passed for a missing terminal", row.Name)
			}
		})
	}
}
