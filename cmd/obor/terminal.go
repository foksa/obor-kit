package main

// terminal.go: the one place this binary builds the terminal adapter. Every other caller asks for a port.Terminal,
// so a second terminal is a case here and nothing else.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/service"
	"obor/internal/setup"
)

// terminal is the project's terminal: one value for its panes and events, launch's pane operations with the wakes
// serve types, and setup's placements. cfg names the kind (project.terminal; nil: config.DefaultTerminal), env finds its
// socket. A kind obor does not know is an error naming the ones it does.
func terminal(cfg *config.Config, env project.Env) (port.Terminal, error) {
	switch k := cfg.TerminalKind(); k {
	case config.DefaultTerminal:
		s := &herdr.Socket{Path: herdr.SocketPath(env)}
		return port.Terminal{Panes: herdr.Panes{S: s}, Control: s, Setup: &herdr.Setup{Socket: s, Env: env}}, nil
	case config.TerminalHC:
		cl := hc.New(env)
		return port.Terminal{Panes: hc.Panes{C: cl}, Control: cl, Setup: cl, Check: cl.Check}, nil
	default:
		return port.Terminal{}, fmt.Errorf("terminal.kind %q: obor knows %s", k, strings.Join(config.TerminalKinds, ", "))
	}
}

// serveTerminal is t as serve watches it: on hc, each subscribe first starts a core that is down from serve's own
// environment scrubbed, and refuses a core whose environment holds a HERDR_ or OBOR_ name (hc.Core); obor's other
// commands only dial. herdr's is t.
func serveTerminal(cfg *config.Config, t port.Terminal) port.Terminal {
	if p, ok := t.Panes.(hc.Panes); ok && cfg.TerminalKind() == config.TerminalHC {
		p.Core = &hc.Core{Environ: os.Environ()}
		t.Panes = p
	}
	return t
}

// terminalEnv is the terminal's environment for a serve launchd starts: the adapter's own variables, read from the
// caller's env (the plist carries the ones set). A kind obor does not know has none.
func terminalEnv(cfg *config.Config, env project.Env) []service.EnvVar {
	var names []string
	switch cfg.TerminalKind() {
	case config.DefaultTerminal:
		names = herdr.EnvNames
	case config.TerminalHC:
		names = hc.EnvNames
	}
	var out []service.EnvVar
	for _, name := range names {
		if v := env.Get(name); v != "" {
			out = append(out, service.EnvVar{Name: name, Value: v})
		}
	}
	return out
}

// adopter is serve's adoption of the project's hidden terminals over term: nil when its setup has no such capability
// (herdr). The project's config is read at each call, so a merged change counts.
func adopter(term port.Terminal, root string) func(ctx context.Context) (int, error) {
	ad, ok := term.Setup.(port.Adopter)
	if !ok {
		return nil
	}
	return func(ctx context.Context) (int, error) {
		cfg, err := config.Load(root)
		if err != nil {
			return 0, err
		}
		return ad.Adopt(ctx, setup.PlaceProjectOf(cfg, root))
	}
}
