package herdr_test

import (
	"fmt"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// contractTerminal is herdr's adapter over a fake herdr holding one shell pane, w1:p1 in /proj, its workspace w1
// labelled proj, as cmd/obor builds the terminal: the guard in front of its Prompt. That is the pane every contract row
// runs against. Its Stale hook freezes a pane's visible screen (herdrtest's Pane.Stale).
func contractTerminal(t *testing.T) testkit.Terminal {
	t.Helper()
	f := herdrtest.New()
	f.Shell("w1:p1", "", "/proj")
	f.Group("w1", "proj")
	srv := herdrtest.Serve(t, f)
	c := srv.Client()
	return testkit.Terminal{Control: port.GuardPrompts(c), Panes: herdr.Panes{S: c},
		Setup: &herdr.Setup{Socket: c, Env: func(string) (string, bool) { return "", false }}, Group: "proj", Cwd: "/proj",
		Stale: func(pane string) error {
			if !f.Set(pane, func(p *herdrtest.Pane) { p.Stale = true }) {
				return fmt.Errorf("no pane %s", pane)
			}
			return nil
		}}
}

// TestTerminalContract runs the terminal contract (testkit.Contract: start, status, instance, place, type, type with
// guard, keys, screen, recent output, pane busy, named, rename, process info, notify, reconnect) against herdr's adapter over
// herdrtest; the live herdr tier runs the same rows (tests/conformance, H-C).
func TestTerminalContract(t *testing.T) {
	t.Parallel()
	var names []string
	for _, row := range testkit.Contract {
		names = append(names, row.Name)
	}
	want := []string{"start", "status", "instance", "place", "type", "type with guard", "keys", "screen",
		"recent output", "pane busy", "named", "rename", "process info", "notify", "reconnect"}
	if len(names) != len(want) {
		t.Fatalf("contract rows %q, want %q", names, want)
	}
	for _, row := range testkit.Contract {
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			if err := row.Run(ctx, contractTerminal(t), "w1:p1"); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestTerminalContractFailsOnAMissingPane: every row that addresses the pane fails for a pane the terminal does not
// have, so a row that passes checked something. The rows that address no pane (notify, instance), and the two whose
// success is a refusal or a fallback (type with guard refuses the guarded type, place falls back to the project's
// group), are no such check.
func TestTerminalContractFailsOnAMissingPane(t *testing.T) {
	t.Parallel()
	for _, row := range testkit.Contract {
		switch row.Name {
		case "notify", "instance", "type with guard", "place":
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			if err := row.Run(ctx, contractTerminal(t), "w9:p9"); err == nil {
				t.Errorf("row %s passed for a missing pane", row.Name)
			}
		})
	}
}
