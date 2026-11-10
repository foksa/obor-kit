package project

import (
	"errors"

	"obor/internal/agents"
)

// terminalPaneVar is the herdr adapter's own pane variable: herdr sets it in every pane it runs. Read here only as
// Pane's fallback for a pane obor did not start (a shell the owner opened, a pane started before OBOR_PANE):
// adapters never import each other (internal/checks boundary_test), so the hooks, guard, mcp, launch and setup reach
// it through this base package, not through internal/herdr.
const terminalPaneVar = "HERDR_PANE_ID"

// PaneVars are the variables Pane reads, in order: a process that is no agent's (the herdr plugin's command) drops
// them all.
var PaneVars = []string{agents.PaneVar, terminalPaneVar}

// ErrNoPane: the environment names no pane (neither OBOR_PANE nor HERDR_PANE_ID set): the caller runs outside a pane.
var ErrNoPane = errors.New("no pane: neither " + agents.PaneVar + " nor " + terminalPaneVar + " is set")

// Pane is the caller's own terminal pane, the one resolver of an agent's pane: OBOR_PANE (every start obor
// makes exports it), else the terminal's HERDR_PANE_ID, else ErrNoPane. It reads env only (hooks never ask herdr).
func Pane(env Env) (string, error) {
	for _, v := range PaneVars {
		if p := env.Get(v); p != "" {
			return p, nil
		}
	}
	return "", ErrNoPane
}
