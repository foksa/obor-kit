package main

import (
	"context"
	"io"
	"os"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/mcp"
	"obor/internal/project"
	"obor/internal/setup"
)

// setupCmd is `obor setup <init|lane|pane|role|githooks|herdr-plugin|agents|mcp> …` (internal/setup): progress lines, then OK …
// or FAIL … (exit 1).
func setupCmd(args []string) int {
	cwd, _ := os.Getwd()
	kit := project.KitRoot(project.OSEnv)
	h := &herdr.Socket{Path: herdr.SocketPath(project.OSEnv)}
	s := &setup.Setup{Kit: kit, Dir: cwd, Env: project.OSEnv, H: h, Out: out, McpCheck: mcp.Check,
		Starter: launcherFor(project.OSEnv, kit, h, out, nil)}
	return s.Run(context.Background(), args)
}

// launcherFor is how obor setup starts an agent (setup does not import launch): a launch.Launcher for the config it
// loaded; tune (nil: none) adjusts it.
func launcherFor(env project.Env, kit string, h app.PaneControl, w io.Writer, tune func(*launch.Launcher)) func(*config.Config) app.Starter {
	return func(cfg *config.Config) app.Starter {
		l := &launch.Launcher{Cfg: cfg, Env: env, Kit: kit, H: h, Out: w}
		if tune != nil {
			tune(l)
		}
		return l
	}
}

// migrateCmd is `obor migrate` (P7-0b; until v0.17): the herd → obor move of this project and machine, step by step
// (internal/setup/migrate.go). It never loads the config: a herd-era project does not load.
func migrateCmd(args []string) int { return setupCmd(append([]string{"migrate"}, args...)) }
