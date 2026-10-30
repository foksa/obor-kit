package launch

import (
	"testing"

	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	_ "obor/internal/agents/opencode"
)

// T-500: the leader an agent of a kind leads its group as: the kind's Stop.Program or its Name(); a kind with no
// bridge: its name; never another program, never none.
func TestLeads(t *testing.T) {
	for _, c := range []struct {
		kind, leader string
		want         bool
	}{
		{"claude", "claude", true}, {"codex", "codex", true}, {"opencode", "opencode", true}, {"gemini", "gemini", true},
		{"claude", "codex", false}, {"opencode", "node", false}, {"codex", "zsh", false}, {"claude", "", false},
		{"", "", false},
	} {
		if got := leads(c.kind, c.leader); got != c.want {
			t.Errorf("leads(%q, %q) = %v, want %v", c.kind, c.leader, got, c.want)
		}
	}
}
