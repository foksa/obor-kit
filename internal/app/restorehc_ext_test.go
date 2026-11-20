package app_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
)

// lostRestorer records each restore run's lost panes (port.Restorer).
type lostRestorer struct {
	mu   sync.Mutex
	runs []port.Lost
}

func (f *lostRestorer) RestoreProject(_ context.Context, _ string, lost port.Lost) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, lost)
}

func (f *lostRestorer) all() []port.Lost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// hcRig is serve on hc (hc.Panes over an in-memory hctest core) for project pj: its lead at its shell in t1, its
// worker pj-l-01 in t2, and t3, another project's terminal; a restorer that records what each run restores.
type hcRig struct {
	t      *testing.T
	core   *hctest.Core
	root   string
	fr     *lostRestorer
	app    *app.App
	out    *syncBuf
	cancel context.CancelFunc
	done   chan struct{}
}

// The terminals' first shells.
const (
	leadPID, workerPID, otherPID = 41001, 41002, 41003
)

func newHCRig(t *testing.T) *hcRig {
	t.Helper()
	r := &hcRig{t: t, core: hctest.New(), root: filepath.Join(t.TempDir(), "pj"), fr: &lostRestorer{}}
	if err := os.MkdirAll(filepath.Join(r.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	for _, tm := range []struct {
		addr, project, agent string
		pid                  int
	}{{"t1", "pj", "pj-lead", leadPID}, {"t2", "pj", "pj-l-01", workerPID}, {"t3", "other", "other-lead", otherPID}} {
		r.core.Add(hc.Record{Address: tm.addr, PID: tm.pid, Cwd: r.root,
			Labels:     map[string]string{hc.LabelProject: tm.project, hc.LabelAgent: tm.agent},
			Foreground: &hc.Foreground{PID: tm.pid, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: r.root}})
	}
	return r
}

// start runs serve until stop (or the test's end) and waits for its connect.
func (r *hcRig) start() {
	r.t.Helper()
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.root, Tick: 2 * time.Millisecond, Retry: 5 * time.Millisecond,
		Out: r.out, Procs: quietProcs, Refresh: port.Refreshed, Restore: r.fr}, hc.Panes{C: r.core.Client()}, &laneTable{})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	r.settled(0)
}

func (r *hcRig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
}

func (r *hcRig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	trail := func() string { return "app output:\n" + r.out.String() }
	if how := poll(r.t, what, 5*time.Second, time.Millisecond, trail, ok); how != "" {
		r.t.Fatalf("%s waiting for %s; %s", how, what, trail())
	}
}

// subscribes is how many subscriptions the core answered.
func (r *hcRig) subscribes() int {
	n := 0
	for _, m := range r.core.Requests() {
		if m == "events.subscribe" {
			n++
		}
	}
	return n
}

// settled waits for a subscription after the before-th, serve connected to it, and a few checks after: that connect's
// restore decision is made, and a restore it started has run.
func (r *hcRig) settled(before int) {
	r.t.Helper()
	r.waitFor("a new subscription", func() bool { return r.subscribes() > before && r.core.Subscribers() == 1 })
	r.waitFor("connected", func() bool { return r.app.Status().Connected })
	checks, _ := app.Counts(r.app)
	r.waitFor("checks after the connect", func() bool { c, _ := app.Counts(r.app); return c >= checks+3 })
}

// shells replaces the programs of addrs by fresh shells (pid+100), with no event: what a stop and start leaves.
func (r *hcRig) shells(addrs ...string) {
	for _, a := range addrs {
		r.core.Update(a, func(rec *hc.Record) {
			rec.PID += 100
			rec.Status, rec.Foreground = nil, &hc.Foreground{PID: rec.PID, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: r.root}
		})
	}
}

// noRestore: no restore ran or was logged; the lead at its shell in t1 is not started.
func (r *hcRig) noRestore(what string) {
	r.t.Helper()
	if runs := r.fr.all(); len(runs) != 0 {
		r.t.Errorf("%s: restores %+v, want none (the lead at its shell stays there):\n%s", what, runs, r.out)
	}
	if strings.Contains(r.out.String(), "restoring") {
		r.t.Errorf("%s: a restore line:\n%s", what, r.out)
	}
}

// restored: one restore ran, with exactly panes (in order).
func (r *hcRig) restored(what string, panes ...string) {
	r.t.Helper()
	r.waitFor("a restore", func() bool { return len(r.fr.all()) >= 1 })
	r.waitFor("the restore's done line", func() bool { return strings.Contains(r.out.String(), "terminal: restore done") })
	runs := r.fr.all()
	if len(runs) != 1 || runs[0].All || !slices.Equal(runs[0].Panes, panes) {
		r.t.Errorf("%s: restores %+v, want one of panes %v:\n%s", what, runs, panes, r.out)
	}
}

// Every way hc's stream ends or changes with every program kept restores nothing: a core restart (bare EOF, seq from
// 0, same pids), an update (a holder added while the old one drains, then a restart), a stalled reader hc dropped
// (EOF, the core up), and another project's lost holder (its terminal's restarted). The lead at its shell is left
// there each time.
func TestServeOnHCNoRestoreWithEveryProgramKept(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		do   func(core *hctest.Core)
	}{
		{"core restart", func(core *hctest.Core) { core.Down(); core.Up() }},
		{"core update", func(core *hctest.Core) {
			core.SetHolders("3b6b8d65e9f538dd68637cd26c3be7e2", "9c1e0ad4b2f7e6a85d3c0b1a2f4e6d8c")
			core.Down()
			core.Up()
		}},
		{"stalled reader dropped", func(core *hctest.Core) { core.Drop() }},
		{"another project's restarted", func(core *hctest.Core) { core.Restart("t3", otherPID+100) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newHCRig(t)
				r.start()
				r.noRestore("the first connect")
				before := r.subscribes()
				c.do(r.core)
				r.settled(before)
				r.noRestore(c.name)
			})
		})
	}
}

// hc server stop, then start: the kept terminals come back as fresh shells (new pids); serve restores exactly its
// project's (the lead's and the worker's), not the other project's terminal.
func TestServeOnHCRestoresAfterStopAndStart(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newHCRig(t)
		r.start()
		before := r.subscribes()
		r.core.Stop()
		r.waitFor("the gone line", func() bool { return strings.Contains(r.out.String(), "terminal: gone") })
		r.shells("t1", "t2", "t3")
		r.core.Up()
		r.settled(before)
		r.restored("after the stop and start", "t1", "t2")
	})
}

// One holder lost (hc runs a fresh shell in the worker's kept terminal: lost, then restarted): only the worker's pane
// is restored, the lead at its shell is not.
func TestServeOnHCRestoresOnlyTheLostHoldersPanes(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newHCRig(t)
		r.start()
		before := r.subscribes()
		r.core.Restart("t2", workerPID+100)
		r.settled(before)
		r.restored("after the worker's holder was lost", "t2")
	})
}

// Serve's first connect compares each pane's run with what the last serve recorded: unchanged pids restore nothing (a
// serve restart leaves the lead stopped at its shell), changed ones restore exactly those panes.
func TestServeOnHCFirstConnect(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		changed []string
	}{
		{"unchanged pids", nil},
		{"the worker's pid changed", []string{"t2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newHCRig(t)
				r.start()
				r.stop()
				b, err := os.ReadFile(app.RunsFile(r.root))
				if err != nil || !strings.Contains(string(b), `"t2":"41002"`) || strings.Contains(string(b), "t3") {
					t.Fatalf("precondition: the runs recorded = %s, %v; want pj's panes' pids, not t3's", b, err)
				}
				r.shells(c.changed...)
				r.start()
				if c.changed == nil {
					r.noRestore("serve's first connect, pids unchanged")
					return
				}
				r.restored("serve's first connect, a pid changed", c.changed...)
			})
		})
	}
}

// A pane created while serve runs has its run recorded from its event: when its program is replaced while serve is
// down, the next serve's first connect restores it.
func TestServeOnHCRecordsACreatedPanesRun(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newHCRig(t)
		r.start()
		r.core.Add(hc.Record{Address: "t4", PID: 41004, Cwd: r.root,
			Labels: map[string]string{hc.LabelProject: "pj", hc.LabelAgent: "pj-rev"}})
		r.waitFor("t4's run recorded", func() bool {
			b, _ := os.ReadFile(app.RunsFile(r.root))
			return strings.Contains(string(b), `"t4":"41004"`)
		})
		r.stop()
		r.shells("t4")
		r.start()
		r.restored("serve's first connect, the created pane's pid changed", "t4")
	})
}
