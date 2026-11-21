package main

// obor web: a page for every obor project on the machine, in its own process (never inside serve: serve is the
// one writer, and a handler there would share its restarts). It listens on localhost only, serves the read model's JSON
// (view.Build as obor status --json builds it) and one embedded HTML page. No auth; its two verbs, the Setup page's
// edits, are requests to the project's serve, taken only from the page's own origin.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/web"
)

// webCmd is `obor web [--listen 127.0.0.1:<port>]`: says where it listens, then serves until Ctrl-C. A --listen that is
// not loopback is refused before anything starts.
func webCmd(args []string) int {
	listen := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--listen":
			if i+1 >= len(args) {
				errf("%s", help.Usage("web"))
				return 2
			}
			i, listen = i+1, args[i+1]
		case strings.HasPrefix(a, "--listen="):
			listen = strings.TrimPrefix(a, "--listen=")
		default:
			errf("%s", help.Usage("web"))
			return 2
		}
	}
	addr, err := web.ListenAddr(listen)
	if err != nil {
		errf("obor web: %v", err)
		return 2
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// One line and a non-zero exit: launchd's throttle starts the agent again once the port is free; the
		// port is not hunted.
		errf("obor web: %v%s", err, heldBy(project.OSEnv, addr))
		return 1
	}
	srv := &http.Server{Handler: (&web.Server{Events: statusEvents, Process: webProcess(project.OSEnv)}).Handler(),
		ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(out, "obor web: http://%s/ (localhost; Ctrl-C ends it)\n", ln.Addr())
	out.Flush() // said before the first request, not when the process ends
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	var followed atomic.Bool
	if changed := binaryWatch(project.OSEnv); changed != nil {
		go watchBinary(ctx, changed, func() { followed.Store(true); _ = srv.Close() })
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errf("obor web: %v", err)
		return 1
	}
	if !followed.Load() {
		return 0
	}
	// A web launchd runs leaves the new build to launchd, as serve does there: the label holds this pid, and
	// KeepAlive starts the kit's shim once this process has exited 0.
	if launched(context.Background(), webOf(nil, project.OSEnv), os.Getpid()) {
		fmt.Fprintln(out, "obor web: a new obor binary: exiting for launchd to start it")
		out.Flush()
		return 0
	}
	// Any other web: the same address again, so the owner's URL stays (an unset port would change); the
	// page's long poll reconnects.
	if err := selfExec(project.KitRoot(project.OSEnv), []string{"web", "--listen", ln.Addr().String()}); err != nil {
		errf("obor web: following the new obor binary: %v", err)
		return 1
	}
	return 0 // only a test's selfExec returns nil
}

// webProcess is obor web's process info of a project's panes: its own terminal's (webTerminal). A project whose
// terminal obor cannot build reads none, as obor status shows none.
func webProcess(env project.Env) func(root string) func(context.Context, string) (port.ProcessInfo, error) {
	return func(root string) func(context.Context, string) (port.ProcessInfo, error) {
		t, err := webTerminal(env, root)
		if err != nil {
			return nil
		}
		return t.Control.ProcessInfo
	}
}

// webTerminal is the terminal of the project at root (project.terminal), its config read at each call so a changed
// terminal counts.
func webTerminal(env project.Env, root string) (port.Terminal, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return port.Terminal{}, err
	}
	return terminal(cfg, env)
}

// webFollowEvery is how often obor web asks whether a new obor binary is there; a test shortens it.
var webFollowEvery = 30 * time.Second

// heldBy is a refused bind's hint at the port's holder, when it is known: on the default listen, the machine's
// obor.web agent when launchd runs it as another process. "" when the holder cannot be told.
func heldBy(env project.Env, addr string) string {
	if addr != web.DefaultListen {
		return ""
	}
	w := webOf(nil, env)
	if !w.Supported() {
		return ""
	}
	pid, loaded := w.PID(context.Background())
	if !loaded || pid == 0 || pid == os.Getpid() {
		return ""
	}
	return fmt.Sprintf(" (obor.web runs as pid %d)", pid)
}

// watchBinary calls follow once changed says a new obor binary is there, asking every webFollowEvery until ctx ends.
func watchBinary(ctx context.Context, changed func() bool, follow func()) {
	t := time.NewTicker(webFollowEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if changed() {
				follow()
				return
			}
		}
	}
}
