// restore.go: serve restores its project's panes when the terminal comes back (T-1051).
package app

import (
	"context"
	"sync"
)

// Rule: after serve's subscription comes back from a lost terminal connection, serve runs the restore once for its
// project (Options.Restore, port.Restorer: launch.Restorer's RestoreProject). A lost connection is what the terminal
// adapter reports as port.NotRunning: a Subscribe that failed so (the terminal down, at serve's start too), or a
// stream that ended so (its socket went); a stream ended by the terminal's answer (a refresh, events_lost) or by ctx
// is no loss. The first stream after a loss is the reconnect: the restore starts once its snapshot is applied.
//
// The restore runs in its own goroutine (it restarts agents: minutes), never under mu, so the stream's events go on
// meanwhile. Runs never overlap: a reconnect while one runs asks for one more run after it (several coalesce), which
// reads the panes afresh. The restore is idempotent (a pane whose agent runs is left alone, and a project's runs are
// one at a time under its restore lock), so the herdr plugin's run, where linked, and serve's restart each pane at
// most once.

// restoreState is the restore's single flight (under App.mu).
type restoreState struct {
	running bool // a run is in progress
	again   bool // a reconnect came while it ran: one more run after it
	runs    int  // runs started, for the tests to wait on
}

// restore starts the restore of the served project after a reconnect, or asks the run in progress for one more; wg
// is Serve's: its end waits for the run, canceled with ctx.
func (a *App) restore(ctx context.Context, wg *sync.WaitGroup) {
	if a.opt.Restore == nil || a.opt.Root == "" {
		return
	}
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
			a.mu.Lock()
			a.rst.runs++
			a.mu.Unlock()
			a.logf("herdr: reconnected: restoring the project's labelled panes that run no agent")
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
