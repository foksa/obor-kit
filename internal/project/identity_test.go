package project

import (
	"testing"

	"obor/internal/config"
)

// The one list of identity names, per terminal kind: a server of a kind keeps its own terminal's identity names and
// must not carry any other's; obor's and a Claude session's are no kind's own; a name that is no identity (hc's
// HC_SESSION, HC_SOCKET, the XDG_ dirs) is no server's to drop, and a kind obor does not know owns none.
func TestForeignIdentity(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		label, name, kind string
		foreign           bool
	}{
		{"obor's identity is foreign to herdr", "OBOR_ROLE", config.DefaultTerminal, true},
		{"obor's identity is foreign to hc", "OBOR_PANE", config.TerminalHC, true},
		{"obor's identity is foreign to Rex", "OBOR_ROLE", config.TerminalRex, true},
		{"herdr keeps its pane", "HERDR_PANE_ID", config.DefaultTerminal, false},
		{"herdr keeps its socket", "HERDR_SOCKET_PATH", config.DefaultTerminal, false},
		{"herdr's pane is foreign to hc", "HERDR_PANE_ID", config.TerminalHC, true},
		{"herdr's pane is foreign to Rex", "HERDR_PANE_ID", config.TerminalRex, true},
		{"hc keeps its terminal", "HC_TERMINAL_ID", config.TerminalHC, false},
		{"hc keeps its terminal's socket", "HC_TERMINAL_SOCKET", config.TerminalHC, false},
		{"hc's terminal is foreign to herdr", "HC_TERMINAL_ID", config.DefaultTerminal, true},
		{"hc's terminal is foreign to Rex", "HC_TERMINAL_ID", config.TerminalRex, true},
		{"Rex keeps its block", "REX_BLOCK", config.TerminalRex, false},
		{"Rex keeps its session", "REX_SESSION", config.TerminalRex, false},
		{"Rex's block is foreign to herdr", "REX_BLOCK", config.DefaultTerminal, true},
		{"Rex's block is foreign to hc", "REX_BLOCK", config.TerminalHC, true},
		{"Rex's session is foreign to hc", "REX_SESSION", config.TerminalHC, true},
		{"a Claude session's marker is foreign to herdr", "CLAUDECODE", config.DefaultTerminal, true},
		{"a Claude session's marker is foreign to hc", "CLAUDECODE", config.TerminalHC, true},
		{"a Claude session's marker is foreign to Rex", "CLAUDE_CODE_SESSION_ID", config.TerminalRex, true},
		{"hc's session names pick the core", "HC_SESSION", config.TerminalHC, false},
		{"hc's socket picks the core", "HC_SOCKET", config.TerminalHC, false},
		{"the XDG dirs pick the core", "XDG_RUNTIME_DIR", config.TerminalHC, false},
		{"a name that is no identity", "PATH", config.TerminalRex, false},
		{"a kind obor does not know owns none", "HERDR_PANE_ID", "zellij", true},
	} {
		t.Run(c.label, func(t *testing.T) {
			t.Parallel()
			if got := ForeignIdentity(c.kind, c.name); got != c.foreign {
				t.Errorf("ForeignIdentity(%q, %q) = %v, want %v", c.kind, c.name, got, c.foreign)
			}
		})
	}
}

// Every kind the config knows keeps its own pane variable and no other kind's: a terminal kind without an identity row
// fails here.
func TestEveryTerminalKindKeepsItsPaneVariable(t *testing.T) {
	t.Parallel()
	for _, kind := range config.TerminalKinds {
		pv := config.TerminalPaneVars[kind]
		if pv == "" {
			t.Errorf("terminal kind %s has no pane variable", kind)
			continue
		}
		if ForeignIdentity(kind, pv) {
			t.Errorf("%s: its own pane variable %s is foreign", kind, pv)
		}
		for _, other := range config.TerminalKinds {
			if other != kind && !ForeignIdentity(other, pv) {
				t.Errorf("%s: %s's pane variable %s is not foreign", other, kind, pv)
			}
		}
	}
}
