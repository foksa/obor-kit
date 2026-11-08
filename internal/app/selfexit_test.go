package app

import (
	"context"
	"errors"
	"io"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/run"
	"obor/internal/store"
)

// T-656: serve's self-restart is a graceful exit, not a `launchctl kickstart -k` (a SIGKILL) on itself. The restarter
// (cmd/obor's serveRestarter) cancels serve's context; serve then ends as on a signal — here with a gate running in a
// process group of its own, as checks.Scratch runs one — and Serve returns only after that group is gone.

// openPanes is herdr that is there: a subscription whose stream stays open until its ctx ends.
type openPanes struct {
	*fakePanes
	snap port.Snapshot
}

func (o *openPanes) Subscribe(ctx context.Context) (*port.Stream, error) {
	c := make(chan port.Event)
	go func() {
		<-ctx.Done()
		close(c)
	}()
	return &port.Stream{Snapshot: o.snap, C: c, Err: func() error { return ctx.Err() }, Close: func() {}}, nil
}

// groupGate is a Scratch whose Gate runs a real command in its own process group (as checks.Scratch does) and returns
// once its ctx ended: a check still running when serve stops.
type groupGate struct {
	*schedScratch
	dir string
}

func (g *groupGate) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	// The child writes both pids before the gate waits: the group's leader (the bash run.Execute started) and a sleep
	// of its own, which the TERM to the group reaches only through the group.
	script := `sleep 60 & echo $! > child.pid; echo $$ > pid; wait`
	run.Execute(ctx, config.Command{Name: "gate", Cmd: script, Timeout: 60}, g.dir, nil, nil, io.Discard, io.Discard)
	g.gated = append(g.gated, in.Task)
	return port.GateOutcome{}, errors.New("the gate was cut off: serve is stopping")
}

// gateGroupHarness is pj with an integrator, lane L-01's T-1 submitted and its sha in: T-1's next step is its gate, run
// by g.
func gateGroupHarness(t *testing.T, dir string) (*harness, *groupGate) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	h.a = New(Options{Root: h.root, Now: func() time.Time { return h.now }, Tick: 20 * time.Millisecond,
		Retry: 5 * time.Millisecond}, &openPanes{fakePanes: h.panes, snap: h.snap}, h.lanes)
	g := &groupGate{schedScratch: &schedScratch{h: h}, dir: dir}
	h.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(g), Agents: g.schedScratch, Typer: g.schedScratch}, nil
	}
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	return h, g
}

// pidIn is the pid a test command wrote to path (waited for).
func pidIn(t *testing.T, path string) int {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 1 {
				return pid
			}
		}
	}
	t.Fatalf("no pid written to %s", path)
	return 0
}

// gone: pid is no longer running within a second (init reaps an orphan).
func gone(pid int) bool {
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestSelfRestartEndsServeAndItsGateGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, g := gateGroupHarness(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.a.Serve(ctx) }()
	pid := pidIn(t, filepath.Join(dir, "pid"))
	child := pidIn(t, filepath.Join(dir, "child.pid"))

	cancel() // the self-restart: what cmd/obor's serveRestarter does instead of launching kickstart -k
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v; want nil (serve exits 0 for launchd to start the new build)", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after the restart's cancellation")
	}
	if len(g.gated) != 1 {
		t.Errorf("gates: %v; want the one the restart cut off", g.gated)
	}
	for _, p := range []int{pid, child} {
		if !gone(p) {
			t.Errorf("pid %d of the gate's process group outlived the restart", p)
		}
	}
}
