package main

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// idleCmd is `obor idle ["<why>"]`, the lead's or the owner's (T-803): with a text, it acknowledges the idle state
// obor serve announced ("[obor] idle: nothing in flight …") and records why nothing is in flight; with none, it shows
// the state (IDLE …, or NOT IDLE). A dispatch, a waiting dispatch or a review request ends the idle state as well.
// Serve's reply is printed; FAIL: serve down, its refusal (not the lead or the owner) or a usage error.
func idleCmd(args []string) int {
	if len(args) > 1 || len(args) == 1 && strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("idle"))
		return 1
	}
	why := ""
	if len(args) == 1 {
		why = args[0]
	}
	return request(context.Background(), project.OSEnv, wire.MethodIdle, func(cfg *config.Config, c *wire.Call) error {
		c.Note = why
		return nil
	})
}
