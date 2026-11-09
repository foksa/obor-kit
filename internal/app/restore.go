// restore.go: serve restores its project's panes when the terminal comes back (T-1051) or restarted while serve was
// down (T-1135).
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
// The terminal restarted while serve was down (T-1135: a machine restart, herdr first and serve later; herdr restarted
// while serve was stopped or re-execing): its panes came back as shells and no stream of this serve saw the loss.
// Serve keeps the instance of the terminal it last connected to in InstanceFile (the snapshot's Started: herdr's socket
// file time, port.Snapshot), written at every connect where it changed. At its first connect it restores when the
// terminal is another one than recorded, or when nothing is recorded and the terminal started after serve's last
// activity (the newest record of the project's log as its replay left it, before this serve's start-up appended any:
// T-1136; a brand-new project, or a terminal that ran before it, restores nothing). The same instance restores nothing: a serve re-exec (every build) or restart leaves a role agent stopped on
// purpose at its shell. Never at the first connect after obor stop (its pause in force when serve loaded the project:
// obor start wakes the project and its agents start on first use), nor when the terminal's start is not known.
//
// The restore runs in its own goroutine (it restarts agents: minutes), never under mu, so the stream's events go on
// meanwhile. Runs never overlap: a reconnect while one runs asks for one more run after it (several coalesce), which
// reads the panes afresh. The restore is idempotent (a pane whose agent runs is left alone, and a project's runs are
// one at a time under its restore lock), so a reconnect's run and the one it queued each restart a pane at most once.

// restoreState is the restore's single flight (under App.mu).
type restoreState struct {
	running bool // a run is in progress
	again   bool // a reconnect came while it ran: one more run after it
	// woke: obor stop's pause was in force when serve loaded the project (loadProject; before serve's socket takes
	// obor start's request that ends it): the first connect restores nothing.
	woke bool
}

// InstanceFile is where serve keeps the instance of the terminal it last connected to:
// <root>/.obor/state/herdr-instance, one line (the terminal's start, RFC 3339 with nanoseconds).
func InstanceFile(root string) string { return filepath.Join(project.StateDir(root), "herdr-instance") }

// instanceOf is snap's terminal instance as InstanceFile holds it ("": not known).
func instanceOf(snap port.Snapshot) string {
	if snap.Started.IsZero() {
		return ""
	}
	return snap.Started.UTC().Format(time.RFC3339Nano)
}

// noteWoke records, at the project's load, whether obor stop's pause is in force (under mu).
func (a *App) noteWoke(p *proj) {
	ps := p.paused(a.opt.Now())
	a.rst.woke = ps != nil && ps.Why == wire.StopPause
}

// connectRestore restores after a stream's snapshot is applied, when lost (the connection was lost since the last
// stream) or at serve's first connect when the terminal restarted since (the rule above), and records the terminal's
// instance.
func (a *App) connectRestore(ctx context.Context, wg *sync.WaitGroup, snap port.Snapshot, first, lost bool) {
	if a.opt.Restore == nil || a.opt.Root == "" {
		return
	}
	cur := instanceOf(snap)
	recorded, err := os.ReadFile(InstanceFile(a.opt.Root))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.logf("herdr: %v: read as no instance recorded", err)
	}
	was := strings.TrimSpace(string(recorded))
	switch {
	case lost:
		a.restore(ctx, wg, "reconnected")
	case !first:
	case cur == "":
		a.logf("herdr: its start is not known (socket file unreadable): no restore at serve's first connect")
	default:
		a.mu.Lock()
		woke := a.rst.woke
		last := a.lastActivity()
		a.mu.Unlock()
		if why := firstRestore(was, snap.Started, last, woke); why != "" {
			a.restore(ctx, wg, why)
		}
	}
	if cur == "" || cur == was {
		return
	}
	if err := project.WriteAtomic(InstanceFile(a.opt.Root), []byte(cur+"\n")); err != nil {
		a.logf("herdr: instance not recorded: %v", err)
	}
}

// firstRestore is why serve's first connect restores ("": it does not), for the terminal started at started, the
// instance recorded was ("": none), serve's last activity last (zero: none known) and woke (obor stop's pause in force
// at the load).
func firstRestore(was string, started, last time.Time, woke bool) string {
	cur := started.UTC().Format(time.RFC3339Nano)
	switch {
	case woke || cur == was:
		return ""
	case was != "":
		return "restarted while serve was down (started " + cur + ", serve last connected to " + was + ")"
	case !last.IsZero() && started.After(last):
		return "started (" + cur + ") after serve's last activity (" + last.UTC().Format(time.RFC3339) +
			"), no instance recorded"
	}
	return ""
}

// lastActivity is the time of the newest record of the served project's log as its replay left it, before serve's
// start-up appended any (under mu; zero: not loaded, no record, or unreadable). A settlement at the load (a failed
// self-restart's needs-you ended: endServeNeeds) or the first connect's own facts are this serve's, never the last
// serve's activity, and would hide a terminal restarted while serve was down (T-1136).
func (a *App) lastActivity() time.Time {
	if a.p == nil {
		return time.Time{}
	}
	return a.p.lastBefore
}

// restore starts the restore of the served project (why: after a reconnect, or why the first connect restores), or
// asks the run in progress for one more; wg is Serve's: its end waits for the run, canceled with ctx.
func (a *App) restore(ctx context.Context, wg *sync.WaitGroup, why string) {
	a.mu.Lock()
	if a.rst.running {
		a.rst.again = true
		a.mu.Unlock()
		a.logf("herdr: reconnected again: one more restore after the one in progress")
		return
	}
	a.rst.running = true
	a.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			a.logf("herdr: %s: restoring the project's labelled panes that run no agent", why)
			a.opt.Restore.RestoreProject(ctx, a.opt.Root)
			a.mu.Lock()
			again := a.rst.again && ctx.Err() == nil
			a.rst.again = false
			a.rst.running = again
			a.mu.Unlock()
			if !again {
				if ctx.Err() == nil {
					a.logf("herdr: restore done")
				}
				return
			}
		}
	}()
}
