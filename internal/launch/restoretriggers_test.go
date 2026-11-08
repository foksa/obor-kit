package launch_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// triggerRfx is newRfx with two labelled obor panes at their shell (the worker of busy lane L-01 and the lead) and a
// restart that leaves its agent running in the pane, as obor restart does: the two triggers' rig (T-1051).
func triggerRfx(t *testing.T) *rfx {
	t.Helper()
	f := newRfx(t)
	lane := f.p.PlainLane("L-01", "task/T-01")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
	f.shell("w1:p1", lane, "pj-l-01")
	f.shell("w1:p2", f.p.Root, "pj-lead")
	restart := f.pl.Restart
	f.pl.Restart = func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
		line, err := restart(ctx, cfg, req, w)
		f.h.Set(req.Pane, func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "claude", req.Agent, "idle" })
		return line, err
	}
	return f
}

func (f *rfx) restartsOf(agent string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, " restart "+agent+" ") {
			n++
		}
	}
	return n
}

// The plugin's run (every project) and then serve's (its project): each labelled pane restarted once; serve's run
// finds both running their agent and skips them.
func TestRestorePluginThenServeRestartsEachOnce(t *testing.T) {
	f := triggerRfx(t)
	f.pl.Restore(context.Background())
	if f.restartsOf("pj-l-01") != 1 || f.restartsOf("pj-lead") != 1 {
		t.Fatalf("precondition: the plugin's run restarted %q, want each pane once", f.calls)
	}
	f.pl.RestoreProject(context.Background(), f.p.Root)
	if a, b := f.restartsOf("pj-l-01"), f.restartsOf("pj-lead"); a != 1 || b != 1 {
		t.Errorf("after serve's run: pj-l-01 restarted %d times, pj-lead %d; want once each: %q", a, b, f.calls)
	}
	log := f.restoreLog()
	has(t, log, " skip pj-l-01 (pane w1:p1): runs claude", "serve's run skips the restarted worker")
	has(t, log, " skip pj-lead (pane w1:p2): runs claude", "serve's run skips the restarted lead")
}

// The two runs at once (herdr's restart starts the plugin while serve reconnects): serve's run read the pane list
// while the plugin's restarts the first pane, so only the project's restore lock and the list read again under it keep
// it from restarting that pane a second time.
func TestRestoreTwoTriggersAtOnceRestartEachOnce(t *testing.T) {
	f := triggerRfx(t)
	restart := f.pl.Restart
	var once sync.Once
	serveDone := make(chan struct{})
	f.pl.Restart = func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
		once.Do(func() { // the plugin's first restart: serve's run starts and reads the list before it ends
			lists := f.paneLists()
			go func() {
				defer close(serveDone)
				f.pl.RestoreProject(context.Background(), f.p.Root)
			}()
			for deadline := time.Now().Add(5 * time.Second); f.paneLists() == lists; time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					t.Error("serve's run never read the pane list")
					return
				}
			}
		})
		return restart(ctx, cfg, req, w)
	}
	f.pl.Restore(context.Background())
	select {
	case <-serveDone:
	case <-time.After(10 * time.Second):
		t.Fatal("serve's run did not end")
	}
	if a, b := f.restartsOf("pj-l-01"), f.restartsOf("pj-lead"); a != 1 || b != 1 {
		t.Errorf("pj-l-01 restarted %d times, pj-lead %d; want once each: %q", a, b, f.calls)
	}
	if n := strings.Count(f.restoreLog(), " skip "); n != 2 {
		t.Errorf("%d skip lines, want 2 (serve's run, both panes running):\n%s", n, f.restoreLog())
	}
}

// paneLists is how many pane.list requests the fake herdr has answered.
func (f *rfx) paneLists() int {
	n := 0
	for _, r := range f.srv.Requests() {
		if r.Method == "pane.list" {
			n++
		}
	}
	return n
}

// Serve's run restores its own project alone: another obor project's labelled shell pane is the plugin's to restore.
func TestRestoreProjectOnlyItsProject(t *testing.T) {
	f := triggerRfx(t)
	other := testkit.NewPlain(t)
	other.Config()
	f.shell("w2:p1", other.Root, "pj-lead")
	f.pl.RestoreProject(context.Background(), f.p.Root)
	if f.restartsOf("pj-l-01") != 1 || f.restartsOf("pj-lead") != 1 {
		t.Fatalf("precondition: serve's run restarted %q, want its project's two panes once each", f.calls)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, other.Root+" ") {
			t.Errorf("serve's run restarted the other project's pane: %q", c)
		}
	}
	f.pl.Restore(context.Background())
	if got := strings.Join(f.calls, "\n"); !strings.Contains(got, other.Root+" restart pj-lead --pane w2:p1") {
		t.Errorf("the plugin's run (every project) did not restore the other project's pane: %q", f.calls)
	}
}
