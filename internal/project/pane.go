package project

import (
	"errors"
	"fmt"

	"obor/internal/agents"
	"obor/internal/config"
)

// The terminal's own pane variable (config.TerminalPaneVars) is read only as PaneOf's fallback for a pane obor did not
// start (a shell the owner opened, a pane started before OBOR_PANE), and only the project's own kind's: an hc terminal
// that inherited a herdr pane's variable (a core started from a herdr pane) is never taken for that pane. Adapters
// never import each other (internal/checks boundary_test), so the hooks, guard, mcp, launch and setup reach it through
// this base package.

// PaneVars are every variable PaneOf reads, for any kind: a process that is no agent's (the herdr plugin's command,
// serve's restore) drops them all.
var PaneVars = []string{agents.PaneVar, config.TerminalPaneVars[config.DefaultTerminal], config.TerminalPaneVars[config.TerminalHC]}

// ErrNoPane: the environment names no pane: the caller runs outside a pane.
var ErrNoPane = errors.New("no pane")

// PaneOf is the caller's own terminal pane, the one resolver of an agent's pane: OBOR_PANE (every start obor makes
// exports it), else the pane variable of cfg's terminal kind (config.TerminalPaneVars, the one table of them; nil cfg: the default kind's), else
// ErrNoPane naming what it read. An empty value reads as unset (hc's terminal.run sets a name empty, it never unsets
// one). It reads env only (hooks never ask the terminal).
func PaneOf(cfg *config.Config, env Env) (string, error) {
	vars := []string{agents.PaneVar}
	if v := config.TerminalPaneVars[cfg.TerminalKind()]; v != "" {
		vars = append(vars, v)
	}
	for _, v := range vars {
		if p := env.Get(v); p != "" {
			return p, nil
		}
	}
	if len(vars) == 1 {
		return "", fmt.Errorf("%w: %s is not set (terminal %s has no pane variable)", ErrNoPane, vars[0], cfg.TerminalKind())
	}
	return "", fmt.Errorf("%w: neither %s nor %s is set", ErrNoPane, vars[0], vars[1])
}
