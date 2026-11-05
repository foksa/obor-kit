package main

import (
	"context"
	"fmt"
	"os"

	"obor/internal/help"
	"obor/internal/mcp"
	"obor/internal/project"
)

// mcpCmd is `obor mcp [--check]`: the stdio MCP server of the job tools (internal/mcp; T-375).
// Its stdout is the protocol, so it writes there directly, never through out's buffer. --check runs initialize and
// tools/list against the server itself and prints one line, the doctor's.
func mcpCmd(args []string) int {
	switch {
	case len(args) == 0:
		cwd, _ := os.Getwd()
		if err := mcp.Serve(context.Background(), mcp.Options{Env: project.OSEnv, Cwd: cwd, In: os.Stdin, Out: os.Stdout}); err != nil {
			errf("obor mcp: %v", err)
			return 1
		}
		return 0
	case len(args) == 1 && args[0] == "--check":
		line, err := mcp.Check(project.OSEnv)
		if err != nil {
			fmt.Fprintln(out, "FAIL mcp: "+err.Error())
			return 1
		}
		fmt.Fprintln(out, line)
		return 0
	}
	errf("%s", help.Usage("mcp"))
	return 2
}
