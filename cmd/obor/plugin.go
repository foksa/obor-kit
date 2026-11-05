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
	// A plugin command can carry HERDR_PANE_ID (axis's old kick had the event's pane), but the hook is no process of that
	// pane: a restart there would pass for the agent's own (launch: scheduled, T-301). Dropped for this run and its
	// children.
	os.Unsetenv("HERDR_PANE_ID")
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
	ctx := context.Background()
	p := &launch.Restorer{Env: project.OSEnv, H: herdrSocket(project.OSEnv),
		LaneState: func(cfg *config.Config, l config.Lane) string {
			return checks.NewLaneTable(ctx, cfg).LaneState(l).State
		}}
	p.Restart = func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
		l := &launch.Launcher{Cfg: cfg, Env: project.OSEnv, H: herdrSocket(project.OSEnv), Out: w}
		return l.Restart(ctx, req)
	}
	p.Restore(ctx)
	return 0
}
