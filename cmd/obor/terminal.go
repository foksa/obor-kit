package main

// terminal.go: the one place this binary builds the terminal adapter. Every other caller asks for a port.Terminal,
// so a second terminal is a case here and nothing else.

import (
	"fmt"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/service"
)

// terminal is the project's terminal: one value for its panes and events, launch's pane operations, the wakes serve
// types and setup's placements. cfg names the kind (project.terminal; nil: config.DefaultTerminal), env finds its
// socket. A kind obor does not know is an error naming the ones it does.
func terminal(cfg *config.Config, env project.Env) (port.Terminal, error) {
	switch k := cfg.TerminalKind(); k {
	case config.DefaultTerminal:
		s := &herdr.Socket{Path: herdr.SocketPath(env)}
		p := herdr.Panes{S: s}
		return port.Terminal{Panes: p, Control: s, Typer: p, Setup: &herdr.Setup{Socket: s, Env: env}}, nil
	default:
		return port.Terminal{}, fmt.Errorf("terminal.kind %q: obor knows %s", k, strings.Join(config.TerminalKinds, ", "))
	}
}

// terminalEnv is the terminal's environment for a serve launchd starts: the adapter's own variables, read from the
// caller's env (the plist carries the ones set). A kind obor does not know has none.
func terminalEnv(cfg *config.Config, env project.Env) []service.EnvVar {
	switch cfg.TerminalKind() {
	case config.DefaultTerminal:
		var out []service.EnvVar
		for _, name := range herdr.EnvNames {
			if v := env.Get(name); v != "" {
				out = append(out, service.EnvVar{Name: name, Value: v})
			}
		}
		return out
	}
	return nil
}
