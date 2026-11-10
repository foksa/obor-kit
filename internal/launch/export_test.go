package launch

import (
	"context"
	"os"
	"slices"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/app/port"
)

// StartArgs is a kind's start over launch args, as a launch's build runs it (a compiled kind's agents.StartOf second
// pass, a plugin kind's one start; the prompt one line), for the external tests' case table.
func StartArgs(kind, kit string, args []string, prompt string) []string {
	k, _ := agents.Lookup(kind)
	in := agents.Start{Kit: kit, Args: args, Prompt: oneLine(prompt), Read: os.ReadFile}
	if s, ok := k.(agents.Split); ok {
		return s.Start(in)
	}
	in.WriteState = func(name string, _ []byte) (string, error) { return "/state/" + name, nil }
	l, _ := agents.StartOf(k, in)
	return l.Args
}

// MCPArgs is the --mcp-config pair a fresh start of kind adds for kit's obor server (nil: none), as its argv shows it.
func MCPArgs(kind, kit string, args []string) []string {
	k, ok := agents.Lookup(kind)
	if !ok {
		return nil
	}
	pair := []string{"--mcp-config", claude.MCPConfig(kit)}
	argv := k.(agents.Split).Start(agents.Start{Kit: kit, Args: args, Read: os.ReadFile})
	for i := range argv {
		if slices.Equal(argv[i:min(i+2, len(argv))], pair) {
			return pair
		}
	}
	return nil
}

// StartSeen is Start with the agent the start ended with (the case table asserts the observation returned).
func (l *Launcher) StartSeen(ctx context.Context, s Start) (port.Pane, error) { return l.start(ctx, s) }
