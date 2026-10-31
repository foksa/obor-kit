package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/service"
)

// serviceOf is the launchd agent of cfg's project (T-596): labelled by its config name (else its root's base, as
// serve names it), running the kit's bin/obor with the caller's PATH and HERDR_SOCKET_PATH.
func serviceOf(cfg *config.Config, env project.Env) service.Service {
	name := cfg.Data.Name
	if name == "" {
		name = filepath.Base(cfg.Root)
	}
	return service.Service{Name: name, Root: cfg.Root, Kit: project.KitRoot(env), Home: env.Get("HOME"),
		UID: os.Getuid(), GOOS: runtime.GOOS, Path: env.Get("PATH"), Herdr: env.Get("HERDR_SOCKET_PATH")}
}

// serveService is `obor serve install|uninstall|restart` for the project around cwd: its line, or FAIL. Install
// says so when a serve already runs in a pane (launchd's waits on the flock until that one is stopped).
func serveService(verb, cwd string, env project.Env) int {
	repo, err := project.Find(cwd)
	if err != nil || !configured(repo.Root) {
		errf("FAIL obor serve %s: %s is not in an obor project (no .obor/config.json): run it in the project", verb, cwd)
		return 1
	}
	cfg, err := config.LoadDaemon(repo.Root)
	if err != nil {
		errf("FAIL obor serve %s: %v", verb, err)
		return 1
	}
	s := serviceOf(cfg, env)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pane := 0
	if verb == "install" && s.Supported() {
		ask, stop := context.WithTimeout(ctx, 2*time.Second)
		_, err := app.Ask(ask, app.SocketPath(cfg.Root))
		stop()
		if pid := servePID(cfg.Root); err == nil && pid != 0 {
			if lpid, _ := s.PID(ctx); lpid != pid {
				pane = pid
			}
		}
	}
	var line string
	switch verb {
	case "install":
		line, err = s.Install(ctx)
	case "uninstall":
		line, err = s.Uninstall(ctx)
	default:
		line, err = s.Restart(ctx)
	}
	if err != nil {
		errf("FAIL obor serve %s: %v", verb, err)
		return 1
	}
	fmt.Fprintln(out, line)
	if pane != 0 {
		fmt.Fprintf(out, "obor serve runs in a pane (pid %d): stop it there; launchd's serve takes over once it is gone\n", pane)
	}
	return 0
}

// servePID is the pid serve wrote into root's serve.flock (0: none). Read it only while serve answers: the file
// outlives a serve that crashed, and taking the lock to test it would race a serve starting.
func servePID(root string) int {
	pid, _ := strconv.Atoi(project.NewLock(app.FlockFile(root)).Holder())
	return pid
}
