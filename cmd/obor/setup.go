package main

import (
	"context"
	"io"
	"os"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/mcp"
	"obor/internal/project"
	"obor/internal/service"
	"obor/internal/setup"
)

// setupCmd is `obor setup <lane|pane|role|githooks|agents|mcp> …` (internal/setup): progress lines, then OK …
// or FAIL … (exit 1).
func setupCmd(args []string) int {
	cwd, _ := os.Getwd()
	kit := project.KitRoot(project.OSEnv)
	var cfg *config.Config // the project's, when obor setup runs in one (obor init: none, the default kind)
	if repo, err := project.Find(cwd); err == nil {
		cfg, _, _ = config.LoadReader(repo.Root)
	}
	t, err := terminal(cfg, project.OSEnv)
	if err != nil {
		errf("FAIL obor setup: %v", err)
		return 1
	}
	s := &setup.Setup{Kit: kit, Dir: cwd, Env: project.OSEnv, T: t.Setup, Out: out, McpCheck: mcp.Check,
		Starter: launcherFor(project.OSEnv, kit, t.Control, out, nil), Serve: serveInstaller(project.OSEnv),
		Installed: serveInstalled(project.OSEnv)}
	return s.Run(context.Background(), args)
}

// initCmd is `obor init [--name N] [--prefix P]` (P7-2): the project set up with no questions
// (internal/setup/oborinit.go), its summary, OK or FAIL.
func initCmd(args []string) int { return setupCmd(append([]string{"obor-init"}, args...)) }

// serveInstalled is setup's Installed: the plist of cfg's serve LaunchAgent (service) when it is there.
func serveInstalled(env project.Env) func(*config.Config) (string, bool) {
	return func(cfg *config.Config) (string, bool) {
		plist := serviceOf(cfg, env).PlistPath()
		_, err := os.Stat(plist)
		return plist, err == nil
	}
}

// serveInstaller is obor init's last step: `obor serve install` for the project (service), already when its
// current plist is loaded, skipped off darwin.
func serveInstaller(env project.Env) func(context.Context, *config.Config) (string, string, error) {
	return func(ctx context.Context, cfg *config.Config) (string, string, error) {
		s := serviceOf(cfg, env)
		if !s.Supported() {
			return "skipped", service.Elsewhere, nil
		}
		ctx, cancel := context.WithTimeout(ctx, service.Deadline)
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
func launcherFor(env project.Env, kit string, h port.PaneControl, w io.Writer, tune func(*launch.Launcher)) func(*config.Config) port.Starter {
	return func(cfg *config.Config) port.Starter {
		l := &launch.Launcher{Cfg: cfg, Env: env, Kit: kit, H: h, Out: w}
		if tune != nil {
			tune(l)
		}
		return l
	}
}
