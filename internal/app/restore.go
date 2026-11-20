// restore.go: serve restores its project's panes that lost their program: when the terminal replaced them while serve
// watched, or while serve was down.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

// Rule: serve restores a pane of its project only on the terminal's evidence that the pane lost its program, never
// because a socket closed. Each connect's snapshot (a stream's, after a subscribe) is compared with what serve knew
// before it is applied; the panes it shows lost go to the restore (Options.Restore, port.Restorer: launch.Restorer's
// RestoreProject, port.Lost names them). How a pane is shown lost depends on the adapter (port.Snapshot.PaneRuns):
//
// Per pane (PaneRuns, hc): a pane whose Run differs from the Run serve last saw for it lost its program (a lost holder's
// fresh shell, a stop and start's). Serve last saw it in the panes it observes (the last snapshot and the events
// since), and at its first connect in RunsFile, the project's panes' runs serve keeps there whenever they change. A
// pane serve has no run for, or one whose Run is "", shows nothing. A connection that closed with every program kept
// (a core restart or update, a stalled reader hc dropped) and another project's lost panes restore nothing here.
//
// Every pane (no PaneRuns, herdr): the adapter cannot tell which panes lost their program, so the evidence is the
// terminal's restart and every labelled pane is lost. After a lost connection (what the adapter reports as
// port.NotRunning: a Subscribe that failed so, the terminal down at serve's start too, or a stream that ended so, its
// socket gone) the first stream is the reconnect: herdr restarted. A stream ended by the terminal's answer (a refresh,
// events_lost) or by ctx is no loss. At serve's first connect the terminal restarted while serve was down (a machine
// restart, herdr first and serve later; herdr restarted while serve was stopped or re-execing) when the snapshot's
// Instance is another than InstanceFile records (written at every connect where it changed, and at the connect that
// read it from the file the old name reads: the sweep retires that file), or when nothing is recorded and the terminal
// started after serve's last activity (the newest record of the project's log as its replay left it, before this
// serve's start-up appended any: a brand-new project, or a terminal that ran before it, restores nothing). The same
// instance restores nothing, nor does a terminal whose start is not known.
//
// Either way a serve re-exec (every build) or restart on the same terminal restores nothing, so a role agent stopped on
// purpose stays at its shell, and the first connect after obor stop restores nothing (its pause in force when serve
// loaded the project: obor start wakes the project and its agents start on first use).
//
// The restore runs in its own goroutine (it restarts agents: minutes), never under mu, so the stream's events go on
// meanwhile. Runs never overlap: a connect with lost panes while one runs asks for one more run after it, with those
// panes (several coalesce, their panes together), which reads the panes afresh. The restore is idempotent (a pane whose
// agent runs is left alone, and a project's runs are one at a time under its restore lock), so a reconnect's run and
// the one it queued each restart a pane at most once.
//
// While a restore is pending or running, a briefed agent that is gone ends no brief (deliver): its pane may be one
// the restore restarts, and the restore keeps its pane only while its brief is open (OpenWork). Pending starts under
// the lock that applies the snapshot the restore follows (connect), so no pass sees its panes at their shells before
// it, and holds until the run starts; the hold ends when the last run ends (its one more run included). After it a
// restored agent finishes as any agent does, and a pane whose restart failed, still at its shell, ends its brief as
// gone at the next pass.

// restoreState is the restore's single flight and the panes' runs serve recorded (under App.mu).
type restoreState struct {
	pending bool      // a snapshot a restore follows is applied (connect) and that restore has not started or queued yet
	running bool      // a run is in progress
	next    port.Lost // the panes of the connects that came while it ran: one more run after it, with them
	// woke: obor stop's pause was in force when serve loaded the project (loadProject; before serve's socket takes
	// obor start's request that ends it): the first connect restores nothing.
	woke bool
	// runs is the project's panes' runs as RunsFile holds them (pane id → Run; nil before the first connect read it).
	runs map[string]string
}

// restoreCall is a connect's restore: why it runs ("": none) and the panes it restores.
type restoreCall struct {
	why  string
	lost port.Lost
}

// InstanceFile is where serve keeps the instance of the terminal it last connected to:
// <root>/.obor/state/terminal-instance, one line (the terminal's start, RFC 3339 with nanoseconds).
func InstanceFile(root string) string {
	return filepath.Join(project.StateDir(root), "terminal-instance")
}

// RunsFile is where serve keeps its project's panes' runs (port.Pane.Run) for its next first connect:
// <root>/.obor/state/terminal-runs, a JSON object of pane id → Run.
func RunsFile(root string) string {
	return filepath.Join(project.StateDir(root), "terminal-runs")
}

// KindFile is where serve keeps the terminal kind (config.TerminalKind) its InstanceFile and RunsFile belong to:
// <root>/.obor/state/terminal-kind, one line. None: herdr's, the one kind there was before it was kept.
func KindFile(root string) string {
	return filepath.Join(project.StateDir(root), "terminal-kind")
}

// dropOtherKind runs at serve's first connect, before the records are read: when the project's terminal kind (read at
// serve's start) is another than the one its records belong to (KindFile), the old terminal's instance and panes' runs
// are removed, so neither restores nor skips anything on the new terminal and serve watches it afresh; then KindFile
// names the project's kind. The identity table's start rows name the old terminal's pane and terminal ids, which no
// pane of the new one has (herdr's wN:pN and term_…, hc's tN), so every agent is identified afresh by its label and
// session; each agent's next start replaces its row.
func (a *App) dropOtherKind() {
	a.mu.Lock()
	kind := ""
	if a.p != nil {
		kind = a.p.terminal
	}
	a.mu.Unlock()
	if kind == "" {
		return
	}
	was := config.DefaultTerminal
	b, err := os.ReadFile(KindFile(a.opt.Root))
	switch {
	case err == nil:
		was = strings.TrimSpace(string(b))
	case !errors.Is(err, os.ErrNotExist):
		a.logf("terminal: %v: read as %s's records", err, was)
	}
	if was != kind {
		for _, f := range []string{InstanceFile(a.opt.Root), legacyInstanceFile(a.opt.Root), RunsFile(a.opt.Root)} {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				a.logf("terminal: %s's record %s not removed: %v", was, filepath.Base(f), err)
			}
		}
		a.logf("terminal: the project's terminal is %s, its records were %s's: the old terminal's instance and panes' runs "+
			"dropped, its panes identified afresh", kind, was)
	}
	if err == nil && was == kind {
		return
	}
	if err := project.WriteAtomic(KindFile(a.opt.Root), []byte(kind+"\n")); err != nil {
		a.logf("terminal: kind not recorded: %v", err)
	}
}

// legacyInstanceFile is where serve kept it under the name that named herdr: a serve that finds no InstanceFile reads
// this one once, so an upgrade is not read as a terminal restart. The sweep retires it.
func legacyInstanceFile(root string) string {
	return filepath.Join(project.StateDir(root), "herdr-instance")
}

// recordedInstance is the terminal instance serve recorded last ("": none), from InstanceFile, else from the file the
// old name reads (legacy: it came from there, and serve writes it under the new name at that connect). An error: neither
// file could be read.
func recordedInstance(root string) (inst string, legacy bool, err error) {
	b, err := os.ReadFile(InstanceFile(root))
	if err == nil {
		return strings.TrimSpace(string(b)), false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	b, err = os.ReadFile(legacyInstanceFile(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(b)), true, nil
}

// recordedRuns is the panes' runs serve recorded in RunsFile (empty: none recorded). An error: it could not be read.
func recordedRuns(root string) (map[string]string, error) {
	runs := map[string]string{}
	b, err := os.ReadFile(RunsFile(root))
	if errors.Is(err, os.ErrNotExist) {
		return runs, nil
	}
	if err != nil {
		return runs, err
	}
	if err := json.Unmarshal(b, &runs); err != nil {
		return map[string]string{}, err
	}
	return runs, nil
}

// noteWoke records, at the project's load, whether obor stop's pause is in force (under mu).
func (a *App) noteWoke(p *proj) {
	ps := p.paused(a.opt.Now())
	a.rst.woke = ps != nil && ps.Why == wire.StopPause
}

// restoreWhy is the restore a stream's snapshot is followed by (why "": none), read before it is applied: the panes it
// shows lost (the rule above). first: serve's first connect; lost: the connection was lost since the last stream.
func (a *App) restoreWhy(snap port.Snapshot, first, lost bool) restoreCall {
	if a.opt.Restore == nil || a.opt.Root == "" {
		return restoreCall{}
	}
	if first {
		a.dropOtherKind()
	}
	if snap.PaneRuns {
		return a.runsLost(snap, first)
	}
	every := func(why string) restoreCall {
		if why == "" {
			return restoreCall{}
		}
		return restoreCall{why: why, lost: port.Lost{All: true}}
	}
	switch {
	case lost:
		return every("reconnected")
	case !first:
		return restoreCall{}
	case snap.Instance == "":
		a.logf("terminal: its start is not known (the adapter names no instance): no restore at serve's first connect")
		return restoreCall{}
	}
	was, _, err := recordedInstance(a.opt.Root)
	if err != nil {
		a.logf("terminal: %v: read as no instance recorded", err)
	}
	a.mu.Lock()
	woke := a.rst.woke
	last := a.lastActivity()
	a.mu.Unlock()
	return every(firstRestore(was, snap.Instance, last, woke))
}

// runsLost is the restore of snap's panes of the project whose Run differs from the one serve last saw (PaneRuns): the
// panes it observes, at serve's first connect RunsFile's record (which it reads into rst.runs then).
func (a *App) runsLost(snap port.Snapshot, first bool) restoreCall {
	var recorded map[string]string
	if first {
		var err error
		if recorded, err = recordedRuns(a.opt.Root); err != nil {
			a.logf("terminal: %v: read as no runs recorded", err)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	was := a.servedRuns()
	if first {
		a.rst.runs, was = recorded, recorded
		if a.rst.woke {
			return restoreCall{}
		}
	}
	var ids []string
	for _, p := range snap.Panes {
		if r := was[p.ID]; a.projectOf(p) != nil && r != "" && p.Run != "" && p.Run != r {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return restoreCall{}
	}
	why := "the terminal replaced the program of " + strings.Join(ids, ", ")
	if first {
		why += " while serve was down"
	}
	return restoreCall{why: why, lost: port.Lost{Panes: ids}}
}

// servedRuns is the Run of each pane of the project serve observes that names one (under mu).
func (a *App) servedRuns() map[string]string {
	runs := map[string]string{}
	for id, o := range a.obs {
		if o.pane.Run != "" && a.projectOf(o.pane) != nil {
			runs[id] = o.pane.Run
		}
	}
	return runs
}

// changedRuns is the project's panes' runs when they differ from what RunsFile holds (rst.runs, which it then holds),
// nil otherwise (under mu): the caller writes them (writeRuns). Nothing before the first connect read the file.
func (a *App) changedRuns() map[string]string {
	if a.rst.runs == nil {
		return nil
	}
	runs := a.servedRuns()
	if maps.Equal(runs, a.rst.runs) {
		return nil
	}
	a.rst.runs = runs
	return runs
}

// writeRuns records runs (changedRuns; nil: nothing changed) in RunsFile.
func (a *App) writeRuns(runs map[string]string) {
	if runs == nil || a.opt.Root == "" {
		return
	}
	b, _ := json.Marshal(runs) // a map of strings: its keys sorted
	if err := project.WriteAtomic(RunsFile(a.opt.Root), append(b, '\n')); err != nil {
		a.logf("terminal: panes' runs not recorded: %v", err)
	}
}

// connectRestore starts the restore call names (why "": none; restoreWhy) once the stream's snapshot is applied, and
// records the panes' runs when they changed, and the terminal's instance: under the new name when it changed, and when
// it was read from the legacy file, whose name the sweep retires (so an upgraded project keeps its record after that
// pass).
func (a *App) connectRestore(ctx context.Context, wg *sync.WaitGroup, snap port.Snapshot, call restoreCall) {
	if a.opt.Restore == nil || a.opt.Root == "" {
		return
	}
	if call.why != "" {
		a.restore(ctx, wg, call)
	}
	a.mu.Lock()
	runs := a.changedRuns()
	a.mu.Unlock()
	a.writeRuns(runs)
	cur := snap.Instance
	was, legacy, _ := recordedInstance(a.opt.Root) // an error reads as no instance recorded
	if cur == "" || cur == was && !legacy {
		return
	}
	if err := project.WriteAtomic(InstanceFile(a.opt.Root), []byte(cur+"\n")); err != nil {
		a.logf("terminal: instance not recorded: %v", err)
	}
}

// firstRestore is why serve's first connect restores ("": it does not), for the terminal's instance cur (the adapter's
// RFC 3339 start), the instance recorded was ("": none), serve's last activity last (zero: none known) and woke (obor
// stop's pause in force at the load).
func firstRestore(was, cur string, last time.Time, woke bool) string {
	switch {
	case woke || cur == was:
		return ""
	case was != "":
		return "restarted while serve was down (started " + cur + ", serve last connected to " + was + ")"
	}
	started, err := time.Parse(time.RFC3339Nano, cur)
	if err != nil || last.IsZero() || !started.After(last) {
		return ""
	}
	return "started (" + cur + ") after serve's last activity (" + last.UTC().Format(time.RFC3339) +
		"), no instance recorded"
}

// lastActivity is the time of the newest record of the served project's log as its replay left it, before serve's
// start-up appended any (under mu; zero: not loaded, no record, or unreadable). A settlement at the load (a failed
// self-restart's needs-you ended: endServeNeeds) or the first connect's own facts are this serve's, never the last
// serve's activity, and would hide a terminal restarted while serve was down.
func (a *App) lastActivity() time.Time {
	if a.p == nil {
		return time.Time{}
	}
	return a.p.lastBefore
}

// SetRestore wires the restore serve runs when a connect shows panes that lost their program: serve's, after New,
// since the restore asks this app's state (launch.Restorer.OpenWork).
func (a *App) SetRestore(r port.Restorer) { a.opt.Restore = r }

// restore starts the restore of call's panes of the served project (call.why: why the connect restores), or asks the
// run in progress for one more with them; wg is Serve's: its end waits for the run, canceled with ctx.
func (a *App) restore(ctx context.Context, wg *sync.WaitGroup, call restoreCall) {
	a.mu.Lock()
	a.rst.pending = false // the run in progress or the one starting now holds the briefs from here
	if a.rst.running {
		a.rst.next = a.rst.next.Add(call.lost)
		a.mu.Unlock()
		a.logf("terminal: reconnected again: one more restore after the one in progress")
		return
	}
	a.rst.running = true
	a.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		why, lost := call.why, call.lost
		for {
			what := "the project's labelled panes that run no agent"
			if !lost.All {
				what = "those of the project's labelled panes that run no agent"
			}
			a.logf("terminal: %s: restoring %s", why, what)
			a.opt.Restore.RestoreProject(ctx, a.opt.Root, lost)
			a.mu.Lock()
			lost, a.rst.next = a.rst.next, port.Lost{}
			again := !lost.None() && ctx.Err() == nil
			a.rst.running = again
			a.mu.Unlock()
			if !again {
				if ctx.Err() == nil {
					a.logf("terminal: restore done")
				}
				return
			}
			if !lost.All {
				why = "the terminal replaced the program of " + strings.Join(lost.Panes, ", ")
			}
		}
	}()
}

// restoring: a restore is pending or running (under mu): a gone agent ends no brief meanwhile (the rule above).
func (a *App) restoring() bool { return a.rst.pending || a.rst.running }
