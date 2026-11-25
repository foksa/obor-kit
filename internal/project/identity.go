package project

import (
	"strings"

	"obor/internal/config"
)

// identity.go: the names a terminal server passes to the panes it makes. A server keeps the environment of the process
// that started it: a core keeps its caller's across restarts (docs/research/hc-probes.md P4, P11), a Rex block inherits
// the Rex server's (docs/research/rex-probes.md R4). A server started from another terminal's pane or from a Claude
// session therefore hands that one's identity to every pane it makes, so each adapter removes every identity name that
// is not its own before it makes a pane, and hc's core, which a running server keeps, is refused while it holds one.
// The list is here, beside the pane variables (pane.go), because hc and rex both read it and adapters never import each
// other.

// identityNames are the environment names that say which terminal (or agent session) a process runs in, each with the
// terminal kind it identifies ("" : obor's and a Claude session's, no terminal's own). A name that ends in "_" is a
// prefix, else the whole name.
var identityNames = []struct {
	Name string
	Kind string
}{
	{"OBOR_", ""}, // obor's: every pane it starts, every server it starts
	// a Claude session's markers: a Claude started in a server that holds them takes its parent's permission mode
	{"CLAUDECODE", ""},
	{"CLAUDE_CODE_", ""},
	{"HERDR_", config.DefaultTerminal},  // herdr's pane, its socket with it
	{"HC_TERMINAL_", config.TerminalHC}, // the pane a caller inside hc runs in
	{"REX_BLOCK", config.TerminalRex},   // Rex's block
	{"REX_SESSION", config.TerminalRex}, // the session the block belongs to
}

// identityMatch is whether the identity pattern covers the environment name: a pattern that ends in "_" is a prefix,
// else the whole name.
func identityMatch(pattern, name string) bool {
	if strings.HasSuffix(pattern, "_") {
		return strings.HasPrefix(name, pattern)
	}
	return name == pattern
}

// ForeignIdentity reports whether a server of kind must not carry the environment name: every identity name but its
// own kind's. obor's and a Claude session's are no terminal kind's own; a name that is no identity (hc's HC_SESSION,
// HC_SOCKET, the XDG_ dirs that pick the core) is no server's to drop, and a kind obor does not know owns none.
func ForeignIdentity(kind, name string) bool {
	for _, id := range identityNames {
		if id.Kind != "" && id.Kind == kind {
			continue
		}
		if identityMatch(id.Name, name) {
			return true
		}
	}
	return false
}
