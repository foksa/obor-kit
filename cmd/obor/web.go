package main

// obor web (T-667): a read-only page for every obor project on the machine, in its own process (never inside serve:
// serve is the one writer, and a handler there would share its restarts). It listens on localhost only, serves the read
// model's JSON (view.Build as obor status --json builds it) and one embedded HTML page. No verbs, no auth.

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
				errf("usage: obor web [--listen 127.0.0.1:<port>]")
				return 2
			}
			i, listen = i+1, args[i+1]
		case strings.HasPrefix(a, "--listen="):
			listen = strings.TrimPrefix(a, "--listen=")
		default:
			errf("usage: obor web [--listen 127.0.0.1:<port>]")
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
		errf("obor web: %v", err)
		return 1
	}
	srv := &http.Server{Handler: (&web.Server{Events: statusEvents}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(out, "obor web: http://%s/ (read-only, localhost; Ctrl-C ends it)\n", ln.Addr())
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
	// A new obor binary (T-719): the same address again, so the owner's URL stays (an unset port would change); the
	// page's long poll reconnects.
	if err := selfExec(project.KitRoot(project.OSEnv), []string{"web", "--listen", ln.Addr().String()}); err != nil {
		errf("obor web: following the new obor binary: %v", err)
		return 1
	}
	return 0 // only a test's selfExec returns nil
}

// webFollowEvery is how often obor web asks whether a new obor binary is there (T-719); a test shortens it.
var webFollowEvery = 30 * time.Second

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
