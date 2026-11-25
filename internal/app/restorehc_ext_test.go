package app_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/launch"
	"obor/internal/store"
	"obor/internal/testkit"
)

// hcRig is serve on hc (hc.Panes over an in-memory hctest core) for project pj: its lead at its shell in t1, its
// worker pj-l-01 in t2, and t3, another project's terminal; a restorer that records what each run restores.
type hcRig struct {
	t      *testing.T
	core   *hctest.Core
	root   string
	home   string // Options.Home: the agents' home beside the project
	fr     *fakeRestorer
	app    *app.App
	adopt  func(ctx context.Context) (int, error) // Options.Adopt (nil: none)
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
	base := t.TempDir()
	r := &hcRig{t: t, core: hctest.New(), root: filepath.Join(base, "pj"), home: filepath.Join(base, "home"), fr: &fakeRestorer{}}
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
	r.app = app.New(app.Options{Root: r.root, Tick: rigTick(r.t), Retry: 5 * time.Millisecond,
		Out: r.out, Procs: quietProcs, Refresh: port.Refreshed, Restore: r.fr, Adopt: r.adopt, Home: r.home},
		hc.Panes{C: r.core.Client()}, &fakeLanes{})
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

// A terminal kind change (project.terminal, read at serve's start) drops the records serve kept for the old terminal:
// at the first connect under the new kind its instance and panes' runs are removed (the new terminal's are recorded
// after), so a pane whose run differs from
// the old record restores nothing (the same change under one kind restores it: TestServeOnHCFirstConnect), the log
// says so once, and terminal-kind names the new kind; the next serve under that kind drops nothing.
func TestServeKindChangeDropsTheOldTerminalsRecords(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newHCRig(t) // its config names no terminal: herdr's records
		r.start()
		r.stop()
		b, err := os.ReadFile(app.KindFile(r.root))
		runs, rerr := os.ReadFile(app.RunsFile(r.root))
		if err != nil || string(b) != "herdr\n" || rerr != nil || !strings.Contains(string(runs), `"t2":"41002"`) {
			t.Fatalf("precondition: kind %q (%v), runs %s (%v); want herdr and t2's run", b, err, runs, rerr)
		}
		write(t, app.InstanceFile(r.root), "2026-11-20T10:00:00.123456789Z\n")
		write(t, filepath.Join(r.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","terminal":"hc"}}`)
		r.shells("t2")
		r.start()
		r.noRestore("the first connect after the kind change")
		if !strings.Contains(r.out.String(), "terminal: the project's terminal is hc, its records were herdr's") {
			t.Errorf("no line for the kind change:\n%s", r.out)
		}
		if b, err := os.ReadFile(app.KindFile(r.root)); err != nil || string(b) != "hc\n" {
			t.Errorf("terminal-kind = %q, %v; want hc", b, err)
		}
		if b, err := os.ReadFile(app.InstanceFile(r.root)); err != nil || strings.Contains(string(b), "2026-11-20") {
			t.Errorf("terminal-instance = %q, %v; want the old terminal's dropped (the new one's recorded)", b, err)
		}
		r.stop()
		r.start()
		if n := strings.Count(r.out.String(), "its records were"); n != 0 {
			t.Errorf("the next serve under hc dropped records again (%d lines):\n%s", n, r.out)
		}
	})
}

// trialRig is serve on hc for a testkit project (proj, prefix pj) whose one holder holds five kept terminals: t5 and t6
// the workers of the free lanes L-01 and L-02 at their shells, t7 the integrator and t8 the reviewer at their shells
// with no open work, t9 the lead in a Claude turn. Its restore is launch's Restorer over the same core: its restarts
// are recorded (each succeeds), its lines go to the restore log.
type trialRig struct {
	*hcRig
	env      func() string // the restore log
	mu       sync.Mutex
	restarts []string
}

// The trial terminals' first shells, and the lead's Claude.
var trialPIDs = map[string]int{"t5": 32186, "t6": 32188, "t7": 32189, "t8": 32192, "t9": 32195}

const trialClaude = 32250

func newTrialRig(t *testing.T) *trialRig {
	t.Helper()
	p := testkit.NewPlain(t)
	lanes := map[string]string{"L-01": p.PlainLane("L-01", ""), "L-02": p.PlainLane("L-02", "")}
	p.Config(testkit.Set("agents.roles", map[string]any{"integrator": "", "reviewer": "opus"}))
	r := &trialRig{hcRig: &hcRig{t: t, core: hctest.New(), root: p.Root, home: t.TempDir(), fr: &fakeRestorer{}}}
	pl := &launch.Restorer{Env: p.Env(), H: r.core.Client(),
		Sessions:  store.CurrentAt,
		LaneState: func(*config.Config, config.Lane) string { return port.LaneFree },
		OpenWork:  func(string, string) (bool, error) { return false, nil }}
	pl.Restart = func(_ context.Context, _ *config.Config, req launch.Request, _ io.Writer) (string, error) {
		r.mu.Lock()
		r.restarts = append(r.restarts, req.Agent+" "+req.Pane)
		r.mu.Unlock()
		return "RESTARTED " + req.Agent, nil
	}
	r.fr.next = pl
	r.env = func() string { b, _ := os.ReadFile(launch.RestoreLog(pl.Env)); return string(b) }
	for _, tm := range []struct{ addr, agent, cwd string }{
		{"t5", "pj-l-01", lanes["L-01"]}, {"t6", "pj-l-02", lanes["L-02"]}, {"t7", "pj-int", p.Root},
		{"t8", "pj-rev", p.Root}, {"t9", "pj-lead", p.Root},
	} {
		pid := trialPIDs[tm.addr]
		r.core.Add(hc.Record{Address: tm.addr, PID: pid, Cwd: tm.cwd, Keep: true,
			Labels:     map[string]string{hc.LabelProject: "proj", hc.LabelAgent: tm.agent},
			Foreground: &hc.Foreground{PID: pid, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: tm.cwd}})
	}
	r.core.Update("t9", func(rec *hc.Record) {
		rec.Status, rec.Reported = &hc.Status{State: "working", App: "claude-code"}, true
		rec.Foreground = &hc.Foreground{PID: trialClaude, Name: "2.1.329", Argv: []string{"claude"}, Cwd: p.Root}
	})
	return r
}

// lose is the holder's loss up to the restarts: the lead's Claude gone with it (its status cleared), then lost for
// every terminal. Serve's precondition: no restore yet.
func (r *trialRig) lose() {
	r.t.Helper()
	r.core.SetStatus("t9", nil)
	r.waitFor("the lead gone from its pane", func() bool { return strings.Contains(r.out.String(), "agent pj-lead no longer identified") })
	r.core.Lose("t5", "t6", "t7", "t8", "t9")
	r.noRestore("lost, before any restarted")
}

// restarted gives addrs their fresh shells (pid+100) in one Restarted and waits for serve's connect after it.
func (r *trialRig) restarted(addrs ...string) {
	r.t.Helper()
	pids := map[string]int{}
	for _, a := range addrs {
		pids[a] = trialPIDs[a] + 100
	}
	before := r.subscribes()
	r.core.Restarted(pids)
	r.settled(before)
}

// restoredOnce: one restore ran with all five terminals lost, it restarted the lead only, and the restore log skips
// each other pane with its reason; RunsFile holds the new shells' pids.
func (r *trialRig) restoredOnce(what string) {
	r.t.Helper()
	r.restored(what, "t5", "t6", "t7", "t8", "t9")
	r.mu.Lock()
	restarts := slices.Clone(r.restarts)
	r.mu.Unlock()
	if !slices.Equal(restarts, []string{"pj-lead t9"}) {
		r.t.Errorf("%s: restarts %q, want the lead's in t9 only", what, restarts)
	}
	log := r.env()
	for _, want := range []string{
		" skip pj-l-01 (pane t5): lane L-01 is free", " skip pj-l-02 (pane t6): lane L-02 is free",
		" skip pj-int (pane t7): no open work", " skip pj-rev (pane t8): no open work", " restore pj-lead (pane t9) in ",
	} {
		if !strings.Contains(log, want) {
			r.t.Errorf("%s: %q not in the restore log:\n%s", what, want, log)
		}
	}
	b, _ := os.ReadFile(app.RunsFile(r.root))
	if want := `{"t5":"32286","t6":"32288","t7":"32289","t8":"32292","t9":"32295"}`; strings.TrimSpace(string(b)) != want {
		r.t.Errorf("%s: runs recorded %s, want %s", what, b, want)
	}
}

// One holder of five terminals lost (lost for each at once, then the restarts): one restore with all five lost
// restarts the lead (its Claude gone before the reconnect) and skips the rest with their reasons, whether every
// restart is in the reconnect's snapshot or they come over two reconnects (the first snapshot shows t8 and t9 with no
// program yet: the restore waits for them, and records nothing meanwhile).
func TestServeOnHCRestoresEveryPaneOfALostHolder(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		parts [][]string
	}{
		{"one reconnect", [][]string{{"t5", "t6", "t7", "t8", "t9"}}},
		{"split over a reconnect", [][]string{{"t5", "t6", "t7"}, {"t8", "t9"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newTrialRig(t)
				r.start()
				r.noRestore("the first connect")
				r.lose()
				for i, part := range c.parts {
					r.restarted(part...)
					if i == len(c.parts)-1 {
						break
					}
					r.noRestore("t8 and t9 not restarted yet")
					if !strings.Contains(r.out.String(), "t8, t9 not started yet: the restore waits for their fresh shells") {
						t.Errorf("no line for the wait:\n%s", r.out)
					}
					if b, _ := os.ReadFile(app.RunsFile(r.root)); !strings.Contains(string(b), `"t5":"32186"`) {
						t.Errorf("runs recorded while the restore waits = %s; want the old ones kept", b)
					}
				}
				r.restoredOnce(c.name)
			})
		})
	}
}

// A lost pane that ends instead of restarting (finished) ends the restore's wait for it: the event's step restores the
// panes that did restart.
func TestServeOnHCRestoresWhenAWaitedPaneFinishes(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newTrialRig(t)
		r.start()
		r.lose()
		r.restarted("t5", "t6", "t7", "t9")
		r.noRestore("t8 not restarted yet")
		r.core.Finish("t8", "holder_lost")
		r.restored("after t8 finished", "t5", "t6", "t7", "t9")
		r.mu.Lock()
		defer r.mu.Unlock()
		if !slices.Equal(r.restarts, []string{"pj-lead t9"}) {
			t.Errorf("restarts %q, want the lead's in t9 only", r.restarts)
		}
	})
}
