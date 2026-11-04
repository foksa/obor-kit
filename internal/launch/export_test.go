package launch

import (
	"os"
	"slices"

	"obor/internal/agents"
	"obor/internal/agents/claude"
)

// StartArgs is startArgs for the external tests' case table.
func StartArgs(kind, kit string, args []string, prompt string) []string {
	k, _ := agents.Lookup(kind)
	return startArgs(k, Start{Kind: kind, Args: args, Prompt: prompt}, kit, "")
}

// MCPArgs is the --mcp-config pair a fresh start of kind adds for kit's obor server (nil: none), as its argv shows it.
func MCPArgs(kind, kit string, args []string) []string {
	k, ok := agents.Lookup(kind)
	if !ok {
		return nil
	}
	pair := []string{"--mcp-config", claude.MCPConfig(kit)}
	argv := k.Start(agents.Start{Kit: kit, Args: args, Read: os.ReadFile})
	for i := range argv {
		if slices.Equal(argv[i:min(i+2, len(argv))], pair) {
			return pair
		}
	}
	return nil
}
