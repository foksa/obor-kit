package checks

import (
	"context"
	"slices"
	"testing"
	"time"
)

// commandCalls is a runner over dir whose onCommand trail the test reads: the command each start names and "" each end
// reports.
func commandCalls(t *testing.T, opts Options, dir string) (*runner, *[]string) {
	t.Helper()
	r := newRunner(context.Background(), &opts, dir, "T-5", "serve-changed-files")
	got := &[]string{}
	r.onCommand = func(name string) { *got = append(*got, name) }
	return &r, got
}

// T-673 (RV-75 #4): the runner tells serve's gate each command as it starts and "" as it ends, so obor status shows
// the gate with no command between two — a finished command is never shown as running.
func TestRunnerCommandEnds(t *testing.T) {
	x := newFx(t, cmd("check", "true"))
	o := x.opts()
	r, got := commandCalls(t, o, x.Root)
	if _, ok, h := r.exec("check"); !ok || h != nil {
		t.Fatalf("precondition: check ran, got ok %v, halt %+v", ok, h)
	}
	if want := []string{"check", ""}; !slices.Equal(*got, want) {
		t.Fatalf("one command: told %q; want %q", *got, want)
	}
	// Between two commands (here the second exec: in a gate the slot wait or git work) nothing runs.
	r.exec("check")
	if want := []string{"check", "", "check", ""}; !slices.Equal(*got, want) {
		t.Errorf("two commands: told %q; want %q", *got, want)
	}
}

// T-673: a command that times out, and one a signal cuts, end like any other: the runner reports no command running.
func TestRunnerCommandEndsOnTimeoutAndInterrupt(t *testing.T) {
	x := newFx(t, cmd("check", map[string]any{"cmd": "sleep 8", "timeout": 1}))
	o := x.opts()
	r, got := commandCalls(t, o, x.Root)
	out, ok, h := r.exec("check")
	if !ok || h != nil || out.rc != 124 {
		t.Fatalf("precondition: check timed out (rc 124), got ok %v, rc %d, halt %+v", ok, out.rc, h)
	}
	if want := []string{"check", ""}; !slices.Equal(*got, want) {
		t.Errorf("timed out: told %q; want %q", *got, want)
	}

	x2 := newFx(t, cmd("check", "sleep 8"))
	o2 := x2.opts()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r2 := newRunner(ctx, &o2, x2.Root, "T-5", "serve-changed-files")
	var calls []string
	started := make(chan struct{})
	r2.onCommand = func(name string) {
		if name != "" {
			close(started)
		}
		calls = append(calls, name)
	}
	go func() {
		<-started
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, ok, h := r2.exec("check"); !ok || h == nil || !h.signal {
		t.Fatalf("precondition: check was cut by the signal, got ok %v, halt %+v", ok, h)
	}
	if want := []string{"check", ""}; !slices.Equal(calls, want) {
		t.Errorf("interrupted: told %q; want %q", calls, want)
	}
}
