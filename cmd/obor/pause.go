package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

const pauseUsage = `FAIL usage: obor pause [--for <dur>] "<why>" | obor pause --clear`

// pauseCmd is `obor pause [--for <dur>] "<why>" | --clear`, the lead's or the owner's: a request to obor serve that
// records the project's pause (wire.MethodPause): while it holds, dispatches are refused and waiting tasks wait; running
// gates and merges finish. <dur> is a Go duration (90m, 3h). Serve's reply (PAUSED until …, PAUSE CLEARED, NOT
// PAUSED), or FAIL: serve down, its refusal or a usage error (exit 1).
func pauseCmd(args []string) int {
	var c wire.Call
	switch {
	case len(args) == 1 && args[0] == "--clear":
		c.Clear = true
	case len(args) == 3 && args[0] == "--for":
		d, err := time.ParseDuration(args[1])
		if err != nil || d <= 0 {
			fmt.Fprintf(out, "FAIL usage: obor pause: bad duration: %s\n", args[1])
			return 1
		}
		c.Until, c.Note = time.Now().Add(d).Unix(), args[2]
	case len(args) == 1:
		c.Note = args[0]
	default:
		fmt.Fprintln(out, pauseUsage)
		return 1
	}
	if !c.Clear && (c.Note == "" || strings.HasPrefix(c.Note, "--")) { // a mistyped flag (--stat), not a reason
		fmt.Fprintln(out, pauseUsage)
		return 1
	}
	return request(context.Background(), project.OSEnv, wire.MethodPause, func(_ *config.Config, call *wire.Call) error {
		call.Note, call.Until, call.Clear = c.Note, c.Until, c.Clear
		return nil
	})
}
