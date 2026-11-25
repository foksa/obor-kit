//go:build integration

package main

import (
	"context"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
)

// The terminal this binary builds puts the blocked-prompt guard in front of its Prompt (port.GuardPrompts): a blocked
// agent is refused as Blocked, a shell pane as NotFound, and neither typed to. The guard's own table is
// internal/app/port's; this row is the wiring.
func TestTerminalGuardsPrompts(t *testing.T) {
	t.Parallel()
	f := herdrtest.New()
	f.Agent("w1:p1", "pj-l-01", "blocked", "/proj")
	f.Shell("w1:p2", "", "/proj")
	f.Group("w1", "proj")
	srv := herdrtest.Serve(t, f)
	env := project.Env(func(k string) (string, bool) {
		if k != "HERDR_SOCKET_PATH" {
			return "", false
		}
		return srv.Path, true
	})
	term, err := terminal(nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := term.Panes.(herdr.Panes); !ok {
		t.Fatalf("precondition: the default terminal's panes are %T, want herdr's", term.Panes)
	}
	began, err := term.Control.Prompt(context.Background(), "w1:p1", "a wake", true)
	if began || !port.Blocked(err) {
		t.Errorf("Prompt of a blocked pane = began %v, %v; want it refused as Blocked before any key", began, err)
	}
	if began, err := term.Control.Prompt(context.Background(), "w1:p2", "a wake", true); began || !port.NotFound(err) {
		t.Errorf("Prompt at a shell = began %v, %v; want it refused as NotFound before any key", began, err)
	}
	for _, r := range srv.Requests() {
		if r.Method == "agent.prompt" || r.Method == "agent.send_keys" || r.Method == "pane.send_input" {
			t.Errorf("the guard typed: a %s request", r.Method)
		}
	}
}
