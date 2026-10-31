package main

import (
	"context"
	"io"
	"os"
	"time"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/mcp"
	"obor/internal/project"
	"obor/internal/service"
	"obor/internal/setup"
)

// setupCmd is `obor setup <lane|pane|role|githooks|herdr-plugin|agents|mcp> …` (internal/setup): progress lines, then OK …
// or FAIL … (exit 1).
func setupCmd(args []string) int {
	cwd, _ := os.Getwd()
	kit := project.KitRoot(project.OSEnv)
	h := &herdr.Socket{Path: herdr.SocketPath(project.OSEnv)}
	s := &setup.Setup{Kit: kit, Dir: cwd, Env: project.OSEnv, H: h, Out: out, McpCheck: mcp.Check,
		Starter: launcherFor(project.OSEnv, kit, h, out, nil), Serve: serveInstaller(project.OSEnv)}
	return s.Run(context.Background(), args)
}

// initCmd is `obor init [--name N] [--prefix P]` (P7-2): the project set up with no questions
// (internal/setup/oborinit.go), its summary, OK or FAIL.
func initCmd(args []string) int { return setupCmd(append([]string{"obor-init"}, args...)) }

// serveInstaller is obor init's last step: `obor serve install` for the project (service), already when its
// current plist is loaded, skipped off darwin.
func serveInstaller(env project.Env) func(context.Context, *config.Config) (string, string, error) {
	return func(ctx context.Context, cfg *config.Config) (string, string, error) {
		s := serviceOf(cfg, env)
		if !s.Supported() {
			return "skipped", service.Elsewhere, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if s.Current(ctx) {
			return "already", s.Label() + " installed and loaded (" + s.PlistPath() + ")", nil
		}
		line, err := s.Install(ctx)
		return "done", line, err
	}
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
