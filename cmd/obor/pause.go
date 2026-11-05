package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// pauseCmd is `obor pause [--model <name>] [--for <dur>] "<why>" | [--model <name>] --clear`, the lead's or the
// owner's: a request to obor serve that records the project's pause (wire.MethodPause): while it holds, dispatches are
// refused and waiting tasks wait; running gates and merges finish. With --model only that model's (T-765): dispatch
// skips it and a level's next model is used. <dur> is a Go duration (90m, 3h). Serve's reply (PAUSED until …, PAUSE
// CLEARED, NOT PAUSED), or FAIL: serve down, its refusal or a usage error (exit 1).
func pauseCmd(args []string) int {
	var c wire.Call
	if len(args) >= 2 && args[0] == "--model" {
		c.Model, args = args[1], args[2:]
		if c.Model == "" || strings.HasPrefix(c.Model, "-") {
			fmt.Fprintln(out, "FAIL "+help.Usage("pause"))
			return 1
		}
	}
	switch {
	case len(args) == 1 && args[0] == "--clear":
		c.Clear = true
	case len(args) == 3 && args[0] == "--for":
		d, err := time.ParseDuration(args[1])
		if err != nil || d <= 0 {
			fmt.Fprintln(out, "FAIL bad duration: "+args[1]+" ("+help.Usage("pause")+")")
			return 1
		}
		c.Until, c.Note = time.Now().Add(d).Unix(), args[2]
	case len(args) == 1:
		c.Note = args[0]
	default:
		fmt.Fprintln(out, "FAIL "+help.Usage("pause"))
		return 1
	}
	if !c.Clear && (c.Note == "" || strings.HasPrefix(c.Note, "--")) { // a mistyped flag (--stat), not a reason
		fmt.Fprintln(out, "FAIL "+help.Usage("pause"))
		return 1
	}
	return request(context.Background(), project.OSEnv, wire.MethodPause, func(_ *config.Config, call *wire.Call) error {
		call.Note, call.Until, call.Clear, call.Model = c.Note, c.Until, c.Clear, c.Model
		return nil
	})
}
