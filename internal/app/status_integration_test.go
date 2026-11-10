//go:build integration

package app_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
)

func TestStatusSocket(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
	dir, err := os.MkdirTemp("", "app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go r.app.Listen(ln)
	st, err := wire.Ask(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Project == nil || len(st.Project.Agents) != 2 || len(st.Project.Facts) != 1 ||
		st.Project.Agents[1].Status != "working" {
		t.Errorf("status over the socket = %+v", st)
	}
	if _, err := wire.Ask(context.Background(), filepath.Join(dir, "none.sock")); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Errorf("Ask without a server: %v", err)
	}
}
