// restore.go: serve restores its project's panes when the terminal comes back or restarted while serve was
// down.
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/project"
)

// Rule: after serve's subscription comes back from a lost terminal connection, serve runs the restore once for its
// project (Options.Restore, port.Restorer: launch.Restorer's RestoreProject). A lost connection is what the terminal
// adapter reports as port.NotRunning: a Subscribe that failed so (the terminal down, at serve's start too), or a
// stream that ended so (its socket went); a stream ended by the terminal's answer (a refresh, events_lost) or by ctx
// is no loss. The first stream after a loss is the reconnect: the restore starts once its snapshot is applied.
//
// The terminal restarted while serve was down (a machine restart, herdr first and serve later; herdr restarted
// while serve was stopped or re-execing): its panes came back as shells and no stream of this serve saw the loss.
// Serve keeps the instance of the terminal it last connected to in InstanceFile (the snapshot's Instance,
// port.Snapshot), written at every connect where it changed, and at the connect that read it from the file the old
// name reads (the sweep retires that file). At its first connect it restores when the
// terminal is another one than recorded, or when nothing is recorded and the terminal started after serve's last
// activity (the newest record of the project's log as its replay left it, before this serve's start-up appended any:
// a brand-new project, or a terminal that ran before it, restores nothing). The same instance restores nothing: a serve re-exec (every build) or restart leaves a role agent stopped on
// purpose at its shell. Never at the first connect after obor stop (its pause in force when serve loaded the project:
// obor start wakes the project and its agents start on first use), nor when the terminal's start is not known.
//
// The restore runs in its own goroutine (it restarts agents: minutes), never under mu, so the stream's events go on
// meanwhile. Runs never overlap: a reconnect while one runs asks for one more run after it (several coalesce), which
// reads the panes afresh. The restore is idempotent (a pane whose agent runs is left alone, and a project's runs are
// one at a time under its restore lock), so a reconnect's run and the one it queued each restart a pane at most once.
//
// While a restore is pending or running, a briefed agent that is gone ends no brief (deliver): its pane may be one
// the restore restarts, and the restore keeps its pane only while its brief is open (OpenWork). Pending starts under
// the lock that applies the snapshot the restore follows (connect), so no pass sees its panes at their shells before
// it, and holds until the run starts; the hold ends when the last run ends (its one more run included). After it a
// restored agent finishes as any agent does, and a pane whose restart failed, still at its shell, ends its brief as
// gone at the next pass.

// restoreState is the restore's single flight (under App.mu).
type restoreState struct {
	pending bool // a snapshot a restore follows is applied (connect) and that restore has not started or queued yet
	running bool // a run is in progress
	again   bool // a reconnect came while it ran: one more run after it
	// woke: obor stop's pause was in force when serve loaded the project (loadProject; before serve's socket takes
	// obor start's request that ends it): the first connect restores nothing.
	woke bool
}

// InstanceFile is where serve keeps the instance of the terminal it last connected to:
// <root>/.obor/state/terminal-instance, one line (the terminal's start, RFC 3339 with nanoseconds).
func InstanceFile(root string) string {
	return filepath.Join(project.StateDir(root), "terminal-instance")
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

// noteWoke records, at the project's load, whether obor stop's pause is in force (under mu).
func (a *App) noteWoke(p *proj) {
	ps := p.paused(a.opt.Now())
	a.rst.woke = ps != nil && ps.Why == wire.StopPause
}

// restoreWhy is why a stream's snapshot is followed by a restore ("": it is not), read before it is applied: lost (the
// connection was lost since the last stream), or serve's first connect when the terminal restarted since (the rule
// above).
func (a *App) restoreWhy(snap port.Snapshot, first, lost bool) string {
	if a.opt.Restore == nil || a.opt.Root == "" {
		return ""
	}
	switch {
	case lost:
		return "reconnected"
	case !first:
		return ""
	case snap.Instance == "":
		a.logf("terminal: its start is not known (the adapter names no instance): no restore at serve's first connect")
		return ""
	}
	was, _, err := recordedInstance(a.opt.Root)
	if err != nil {
		a.logf("terminal: %v: read as no instance recorded", err)
	}
	a.mu.Lock()
	woke := a.rst.woke
	last := a.lastActivity()
	a.mu.Unlock()
	return firstRestore(was, snap.Instance, last, woke)
}

// connectRestore starts the restore why names ("": none; restoreWhy) once the stream's snapshot is applied, and
// records the terminal's instance: under the new name when it changed, and when it was read from the legacy file,
// whose name the sweep retires (so an upgraded project keeps its record after that pass).
func (a *App) connectRestore(ctx context.Context, wg *sync.WaitGroup, snap port.Snapshot, why string) {
	if a.opt.Restore == nil || a.opt.Root == "" {
		return
	}
	if why != "" {
		a.restore(ctx, wg, why)
	}
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

// SetRestore wires the restore serve runs when its terminal connection came back: serve's, after New, since the
// restore asks this app's state (launch.Restorer.OpenWork).
func (a *App) SetRestore(r port.Restorer) { a.opt.Restore = r }

// restore starts the restore of the served project (why: after a reconnect, or why the first connect restores), or
// asks the run in progress for one more; wg is Serve's: its end waits for the run, canceled with ctx.
func (a *App) restore(ctx context.Context, wg *sync.WaitGroup, why string) {
	a.mu.Lock()
	a.rst.pending = false // the run in progress or the one starting now holds the briefs from here
	if a.rst.running {
		a.rst.again = true
		a.mu.Unlock()
		a.logf("terminal: reconnected again: one more restore after the one in progress")
		return
	}
	a.rst.running = true
	a.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			a.logf("terminal: %s: restoring the project's labelled panes that run no agent", why)
			a.opt.Restore.RestoreProject(ctx, a.opt.Root)
			a.mu.Lock()
			again := a.rst.again && ctx.Err() == nil
			a.rst.again = false
			a.rst.running = again
			a.mu.Unlock()
			if !again {
				if ctx.Err() == nil {
					a.logf("terminal: restore done")
				}
				return
			}
		}
	}()
}

// restoring: a restore is pending or running (under mu): a gone agent ends no brief meanwhile (the rule above).
func (a *App) restoring() bool { return a.rst.pending || a.rst.running }
