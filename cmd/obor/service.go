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
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/selfexec"
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
	ctx, cancel := context.WithTimeout(context.Background(), service.Deadline)
	defer cancel()
	pane := 0
	if verb == "install" && s.Supported() {
		ask, stop := context.WithTimeout(ctx, 2*time.Second)
		_, err := wire.Ask(ask, wire.SocketPath(cfg.Root))
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
	// launchd's restart returns when it has started the new serve; that one replays its log before it binds its
	// socket (T-659), so a caller that asks it at once finds no serve. Wait for its answer (T-862). A serve in a pane
	// is not waited for: install's launchd one waits on the pane's flock, nothing new answers yet.
	ready := false
	if s.Supported() && pane == 0 && (verb == "install" || verb == "restart") {
		d := serveWait(env)
		if dl, ok := ctx.Deadline(); ok {
			d = min(d, time.Until(dl)) // service.Deadline is the whole verb's
		}
		if err := waitServe(ctx, cfg.Root, d); err != nil {
			errf("FAIL obor serve %s: launchd restarted it but serve did not answer in %s (obor status; its log: %s)",
				verb, d.Round(time.Millisecond), s.LogPath())
			return 1
		}
		ready = true
	}
	fmt.Fprintln(out, line)
	if ready {
		fmt.Fprintln(out, "serve ready")
	}
	if pane != 0 {
		fmt.Fprintf(out, "obor serve runs in a pane (pid %d): stop it there; launchd's serve takes over once it is gone\n", pane)
	}
	return 0
}

// serveWaitDefault is how long install and restart wait for the new serve's answer: a big project's replay is long.
// It stays inside service.Deadline. OBOR_SERVE_READY_WAIT, in seconds (fractions allowed: 0.3), shortens it for tests.
const serveWaitDefault = 60 * time.Second

// The wait's timing: one ask's bound (a socket that accepts but never answers is tried again) and the pause between
// asks.
const (
	serveAsk  = 2 * time.Second
	servePoll = 250 * time.Millisecond
)

// serveWait is serveWaitDefault, or OBOR_SERVE_READY_WAIT seconds when that is set to a positive number.
func serveWait(env project.Env) time.Duration {
	if s := env.Get("OBOR_SERVE_READY_WAIT"); s != "" {
		if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return serveWaitDefault
}

// waitServe asks the app socket of root's project until serve answers, every servePoll, nil then. d (and ctx) end the
// wait; the last ask's error is the error.
func waitServe(ctx context.Context, root string, d time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		ask, stop := context.WithTimeout(ctx, serveAsk)
		_, err := wire.Ask(ask, wire.SocketPath(root))
		stop()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(servePoll):
		}
	}
}

// servePID is the pid serve wrote into root's serve.flock (0: none). Read it only while serve answers: the file
// outlives a serve that crashed, and taking the lock to test it would race a serve starting.
func servePID(root string) int {
	pid, _ := strconv.Atoi(project.NewLock(app.FlockFile(root)).Holder())
	return pid
}

// serveRestarter is serve's self-restart (app.Loop.Restart) on a new obor binary (T-719). It ends serve the way a
// signal does — end.stop cancels the daemon's context, running gates end their process groups, serve returns — and
// then: launchd's serve exits 0 and launchd's KeepAlive starts the shim, which runs the new build; any other serve
// (in a pane, on Linux, launchd's agent not installed) execs the kit's shim itself, same pid, same pane (serve, after
// its end). Not `launchctl kickstart -k` on itself: that signal is not known to be a TERM (a SIGKILL would orphan a
// gate's process groups), while a graceful exit is the path launchd restarts anyway. A kit without its shim is an
// error, nothing ended: restart it by hand then.
func serveRestarter(cfg *config.Config, env project.Env, end *serveEnd) func(context.Context) error {
	return func(ctx context.Context) error {
		if s := serviceOf(cfg, env); s.Supported() {
			if pid, loaded := s.PID(ctx); loaded && pid == os.Getpid() {
				end.stop()
				return nil
			}
		}
		shim := selfexec.Shim(project.KitRoot(env))
		if fi, err := os.Stat(shim); err != nil || fi.Mode()&0o111 == 0 {
			return fmt.Errorf("no kit shim %s to exec: restart serve by hand", shim)
		}
		end.exec.Store(true)
		end.stop()
		return nil
	}
}
