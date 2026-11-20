package hc_test

import (
	"context"
	"testing"

	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/testkit"
)

// placeRows are the contract rows hc does not answer yet: a placement (terminal.run, herdr-app's tabs by label) is
// the wiring task's, after the herdr-app client (docs/plan/herdr-core.md step 3, Place).
var placeRows = map[string]bool{"place": true}

// shellCore is a core with t1, a kept shell terminal (pid 41000) at /proj labelled obor.project=proj, and claude as a
// program that reports idle once up, as Claude 2.1.329 did (docs/research/hc-probes.md P1).
func shellCore() *hctest.Core {
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", PID: 41000, Cwd: "/proj", Labels: map[string]string{hc.LabelProject: "proj"},
		Foreground: &hc.Foreground{PID: 41000, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: "/proj"}})
	core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
	return core
}

// contractTerminal is hc's adapter over shellCore: the terminal every contract row runs against. hc's read is one
// text (Recent is Screen), so it has no stale view to make (Stale nil).
func contractTerminal() testkit.Terminal {
	c := shellCore().Client()
	return testkit.Terminal{Control: c, Panes: hc.Panes{C: c}, Group: "proj", Cwd: "/proj"}
}

// TestTerminalContract runs the terminal contract (testkit.Contract) against hc's adapter over hctest, as
// internal/herdr runs it over herdrtest; the place row is skipped until the wiring task.
func TestTerminalContract(t *testing.T) {
	t.Parallel()
	for _, row := range testkit.Contract {
		t.Run(row.Name, func(t *testing.T) {
			if placeRows[row.Name] {
				t.Skip("hc has no Place yet: the wiring task adds it (docs/plan/herdr-core.md step 3)")
			}
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
