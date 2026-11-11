package main

import (
	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/project"
)

// fakeTerminal is the project's terminal over a fake herdr socket, as terminal builds one for herdr (a test's).
func fakeTerminal(s *herdr.Socket, env project.Env) port.Terminal {
	p := herdr.Panes{S: s}
	return port.Terminal{Panes: p, Control: s, Setup: &herdr.Setup{Socket: s, Env: env}}
}
