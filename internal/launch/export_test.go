package launch

import (
	"os"
	"slices"

	"obor/internal/agents"
	"obor/internal/agents/claude"
)

// StartArgs is a compiled kind's start over launch args, as a launch's build runs it (agents.StartOf's second pass,
// the prompt one line), for the external tests' case table.
func StartArgs(kind, kit string, args []string, prompt string) []string {
	k, _ := agents.Lookup(kind)
	return k.Start(agents.Start{Kit: kit, Args: args, Prompt: oneLine(prompt), Read: os.ReadFile})
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
