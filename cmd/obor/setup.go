package main

import (
	"context"
	"os"

	"obor/internal/herdr"
	"obor/internal/mcp"
	"obor/internal/project"
	"obor/internal/setup"
)

// setupCmd is `obor setup <init|lane|pane|role|githooks|herdr-plugin|agents|mcp> …` (internal/setup): progress lines, then OK …
// or FAIL … (exit 1).
func setupCmd(args []string) int {
	cwd, _ := os.Getwd()
	s := &setup.Setup{Kit: project.KitRoot(project.OSEnv), Dir: cwd, Env: project.OSEnv,
		H: &herdr.Socket{Path: herdr.SocketPath(project.OSEnv)}, Out: out, McpCheck: mcp.Check}
	return s.Run(context.Background(), args)
}

// migrateCmd is `obor migrate` (P7-0b; until v0.17): the herd → obor move of this project and machine, step by step
// (internal/setup/migrate.go). It never loads the config: a herd-era project does not load.
func migrateCmd(args []string) int { return setupCmd(append([]string{"migrate"}, args...)) }
