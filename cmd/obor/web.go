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
	"syscall"
	"time"

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
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errf("obor web: %v", err)
		return 1
	}
	return 0
}
