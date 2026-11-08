package app

import (
	"errors"
	"fmt"

	"obor/internal/app/wire"
)

// exitCall answers the socket's exit request (obor stop, T-711): for the project this serve serves, from the lead or
// the owner, serve ends gracefully (Options.Exit: its context cancelled, as a signal and the self-restart end it).
// The reply goes out before serve is down: the caller confirms that by serve.flock and the socket.
func (a *App) exitCall(c wire.ExitReq) (string, error) {
	a.mu.Lock()
	p := a.projAt(c.Root)
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodExit, c.Root)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("%s: the lead or the owner ends serve, not %s (role %s)", wire.MethodExit, Dash(c.Agent), c.Role)
	case a.opt.Exit == nil:
		return "", errors.New(wire.MethodExit + ": this serve cannot end itself")
	}
	a.opt.Exit()
	return "EXITING", nil
}
