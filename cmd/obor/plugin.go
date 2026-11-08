package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
)

// pluginCmd is `obor plugin restore` (launch.Restorer), the obor.events herdr plugin's command (hidden: herdr runs it,
// nobody types it). It starts itself again with --now in a new session, detached, and exits 0 at once, so herdr never
// waits on it; --now does the work in the foreground. Always exit 0 but on a usage error.
func pluginCmd(args []string) int {
	if len(args) < 1 || len(args) > 2 || args[0] != "restore" || len(args) == 2 && args[1] != "--now" {
		errf(help.Usage("plugin"))
		return 2
	}
	// A plugin command can carry a pane's variables (axis's old kick had the event's pane), but the hook is no process
	// of that pane: a restart there would pass for the agent's own (launch: scheduled, T-301). Every variable
	// project.Pane reads is dropped for this run and its children.
	for _, v := range project.PaneVars {
		os.Unsetenv(v)
	}
	// herdr runs its plugins as children of its server: make sure `herdr` (HERDR_BIN_PATH) is on PATH for obor too.
	if bin := os.Getenv("HERDR_BIN_PATH"); bin != "" {
		os.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if len(args) == 1 {
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe, "plugin", args[0], "--now")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if cmd.Start() == nil {
				cmd.Process.Release()
			}
		}
		return 0
	}
	restorer(project.OSEnv).Restore(context.Background())
	return 0
}

// restorer is the restore over env's herdr with obor restart's launch: the plugin's (every project) and serve's after
// its terminal connection came back (its own project, T-1051). env must name no pane: the restore is no process of one.
func restorer(env project.Env) *launch.Restorer {
	return &launch.Restorer{Env: env, H: herdrSocket(env),
		LaneState: func(cfg *config.Config, l config.Lane) string {
			return checks.NewLaneTable(context.Background(), cfg).LaneState(l).State
		},
		Restart: func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
			l := &launch.Launcher{Cfg: cfg, Env: env, H: herdrSocket(env), Out: w}
			return l.Restart(ctx, req)
		}}
}

// paneless is env without the variables project.Pane reads: serve may run in a pane, but its restore restarts other
// agents' panes, and a restart there would pass for the agent's own (launch: scheduled, T-301).
func paneless(env project.Env) project.Env {
	return func(key string) (string, bool) {
		for _, v := range project.PaneVars {
			if key == v {
				return "", false
			}
		}
		return env(key)
	}
}
