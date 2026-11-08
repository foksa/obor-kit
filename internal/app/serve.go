// serve.go: New, Serve and the run loop, shutdown and the load of the served project.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"obor/internal/app/port"
	"path/filepath"
	"sync"
	"time"
)

// New is an app over panes and lanes.
func New(opt Options, panes port.Panes, lanes port.Lanes) *App {
	if opt.Idle <= 0 {
		opt.Idle = 10 * time.Minute
	}
	if opt.Gone <= 0 {
		opt.Gone = time.Minute
	}
	if opt.Tick <= 0 {
		opt.Tick = 30 * time.Second
	}
	if opt.Follow <= 0 {
		opt.Follow = 30 * time.Second
	}
	if opt.Retry <= 0 {
		opt.Retry = 2 * time.Second
	}
	if opt.RetryMax < opt.Retry {
		opt.RetryMax = max(5*time.Second, opt.Retry)
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Out == nil {
		opt.Out = io.Discard
	}
	if opt.Refresh == nil {
		opt.Refresh = func(error) bool { return false }
	}
	a := &App{opt: opt, panes: panes, lanes: lanes,
		obs: map[string]*seen{}, kick: make(chan struct{}, 1), rescan: make(chan struct{}, 1),
		scanned: make(chan struct{}, 1), loaded: make(chan struct{})}
	a.life, a.stop = context.WithCancel(context.Background())
	a.since = opt.Now()
	return a
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.opt.Out, "%s %s\n", a.opt.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// Serve runs until ctx ends: it loads the project at Options.Root (replaying its log), subscribes to herdr, works
// from the stream's snapshot, and subscribes again whenever the stream ends; the watchdogs look every Tick, and each
// look wakes the merge loop (its own goroutine: a gate takes minutes). Herdr down (at the start, or its socket gone
// under the stream) never ends it (T-596): one line when herdr goes away, subscribes again with a backoff (Retry
// doubling up to RetryMax), one line when it is back, then it works from the fresh snapshot; the socket requests
// and the merge loop's goroutine go on meanwhile. It returns once the merge loop has stopped and the socket requests
// in progress, canceled, have ended (their process groups cleaned up). The binary watch (follow.go) has its own timer,
// herdr connected or not (T-737).
func (a *App) Serve(ctx context.Context) error {
	defer a.shutdown()
	tick := time.NewTicker(a.opt.Tick)
	defer tick.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.refresher(ctx)
	}()
	if a.opt.Loop != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.mergeLoop(ctx)
		}()
		if a.opt.Binary != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.followTicks(ctx)
			}()
		}
	}
	wait, down := a.opt.Retry, false
	var ended error // the last stream's end, not logged yet (nil: none, or a refresh)
	for ctx.Err() == nil {
		a.loadProject()
		st, err := a.panes.Subscribe(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if !down {
				down = true
				if ended != nil {
					a.logf("herdr: gone (stream ended: %v; subscribe: %v); waiting for it, retrying up to every %s",
						ended, err, a.opt.RetryMax)
				} else {
					a.logf("herdr: gone (%v); waiting for it, retrying up to every %s", err, a.opt.RetryMax)
				}
			}
			ended = nil
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			wait = min(2*wait, a.opt.RetryMax)
			continue
		}
		switch {
		case down:
			a.logf("herdr: back")
		case ended != nil:
			a.logf("herdr stream ended: %v; subscribed again", ended)
		}
		wait, down, ended = a.opt.Retry, false, nil
		a.connect(ctx, st.Snapshot)
		a.run(ctx, st, tick.C)
		st.Close()
		a.mu.Lock()
		a.connected = false
		a.mu.Unlock()
		switch err := st.Err(); {
		case ctx.Err() != nil || a.opt.Refresh(err):
		case port.ErrorCode(err) != "": // herdr ended it with an answer (events_lost): it is there, say so now
			a.logf("herdr stream ended: %v; subscribing again", err)
		case err == nil:
			ended = errors.New("closed")
		default: // the socket went: the next subscribe says whether herdr is gone (one line either way)
			ended = err
		}
	}
	return nil
}

// shutdown ends the daemon's lifetime: the socket takes no more requests, the effects of those in progress are
// canceled and waited for, and the project's database is closed.
func (a *App) shutdown() {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
	a.stop()
	a.reqs.Wait()
	a.closeLog()
}

// closeLog closes the project's database.
func (a *App) closeLog() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p != nil {
		_ = a.p.log.Close()
	}
}

// run handles one stream's events, the ticks and the refresher's scans (a decision on the table it read, T-879) until
// the stream or ctx ends. The stream's snapshot is applied before: an event is never older than the snapshot it
// follows.
func (a *App) run(ctx context.Context, st *port.Stream, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-st.C:
			if !ok {
				return
			}
			a.event(ctx, ev)
		case <-tick:
			a.loadProject()
			a.check(ctx, true)
		case <-a.scanned:
			a.check(ctx, false)
		}
	}
}

// loadProject loads the project at Options.Root once (its log replayed) and is a no-op after; a load that failed is
// tried again on the next call (each subscribe, each tick). It runs on Serve's goroutine, as check does.
func (a *App) loadProject() {
	a.mu.Lock()
	loaded := a.p != nil
	a.mu.Unlock()
	if loaded || a.opt.Root == "" {
		return
	}
	p, err := a.load(a.opt.Root)
	if err != nil {
		a.logf("project %s: %v", a.opt.Root, err)
		return
	}
	a.mu.Lock()
	a.p = p
	a.reconcile(p) // its identity table from the panes known now (none before the first snapshot)
	a.mu.Unlock()
	a.endServeNeeds(p)
	a.loadOnce.Do(func() { close(a.loaded) })
}

// Loaded is closed once Serve has loaded the project (its log replayed, loadProject): obor serve binds its socket
// only then (T-659), so no request is ever admitted to wait for the replay.
func (a *App) Loaded() <-chan struct{} { return a.loaded }

// projAt is the served project when root is its root (nil: not loaded yet, or another project's root; under mu).
func (a *App) projAt(root string) *proj {
	if a.p == nil || a.p.root != filepath.Clean(root) {
		return nil
	}
	return a.p
}

// FlockFile is a project's serve lock: <root>/.obor/state/serve.flock. The serve holding it (obor serve takes it at
// its start) is the project's one: its log's one writer. It names that serve's pid.
func FlockFile(root string) string { return filepath.Join(root, ".obor", "state", "serve.flock") }
