//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/testkit"
)

// A count raised to 3 while serve runs needs no obor init: serve adds L-03's worktree and runs its lane_setup (the
// lane's marker), the lane turns free, and a dispatch takes it.
func TestServeAddsARaisedLaneCount(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	for _, lane := range []string{"L-01", "L-02"} {
		dir := p.Lane(lane, "")
		// A dirty lane is not free: the dispatch takes the new L-03, not the first free lane.
		if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setupRan := filepath.Join(p.Base, "setup-ran")
	p.Config(testkit.Set("agents.roles.integrator", ""),
		testkit.Set("lanes.setup", "echo setup > "+project.ShellQuote(setupRan)))
	p.Spec("T-9", "# T-9\n\nbody\n")

	f := herdrtest.New()
	f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	srv := herdrtest.Serve(t, f)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path}
	cmd, out := startServe(t, p.Root, env)
	if ok := waitFor(func() bool {
		ss, err := wire.Ask(context.Background(), wire.SocketPath(p.Root))
		return err == nil && ss.Project != nil
	}); !ok {
		t.Fatalf("serve never loaded the project:\n%s", out.String())
	}

	reply, err := wire.Verdict(context.Background(), wire.SocketPath(p.Root), &wire.ConfigSetReq{
		Envelope: wire.Envelope{Root: p.Root, Agent: "pj-lead", Role: "lead", CallID: "raise"},
		Changes:  []wire.ConfigChange{{Key: "lanes.count", Value: json.RawMessage(`3`)}}, Source: "web"})
	if err != nil || !strings.Contains(reply, "SET lanes.count") {
		t.Fatalf("raise lanes.count: %q, %v; want SET", reply, err)
	}

	marker := filepath.Join(p.Root, ".obor", "state", "lanes", "L-03.setup")
	laneDir := p.LaneDir("L-03")
	if ok := waitFor(func() bool {
		_, m := os.Stat(marker)
		_, d := os.Stat(laneDir)
		_, s := os.Stat(setupRan)
		return m == nil && d == nil && s == nil
	}); !ok {
		t.Fatalf("L-03 was not added and set up (marker %s, setup %s):\n%s", marker, setupRan, out.String())
	}
	if top := p.Git(laneDir, "rev-parse", "--show-toplevel"); top != laneDir {
		t.Errorf("L-03's worktree top = %q, want %q", top, laneDir)
	}
	if b, err := os.ReadFile(marker); err != nil || !strings.Contains(string(b), "echo setup") {
		t.Errorf("L-03's setup marker = %q, %v; want the lane_setup command", b, err)
	}

	reply, err = wire.Verdict(context.Background(), wire.SocketPath(p.Root), &wire.DispatchReq{
		Envelope: wire.Envelope{Root: p.Root, Agent: "pj-lead", Role: "lead", CallID: "take"},
		Task:     "T-9", Types: []string{"sonnet"}})
	if err != nil || !strings.Contains(reply, "→ L-03") {
		t.Fatalf("dispatch onto the added lane: %q, %v; want L-03", reply, err)
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("serve after TERM: %v\n%s", err, out.String())
	}
}
