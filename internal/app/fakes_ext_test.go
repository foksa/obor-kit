package app_test

import (
	"context"
	"slices"
	"sync"

	"obor/internal/app/port"
)

// fakeRestorer is a restore run (port.Restorer) for serve's tests: it records the roots asked for and the lost panes
// each run was given, and the most runs at once. A run waits for hold while hold is set (a restore that takes its
// time), then runs next's restore (nil: none).
type fakeRestorer struct {
	mu      sync.Mutex
	roots   []string
	lost    []port.Lost
	running int
	most    int
	hold    chan struct{}
	next    port.Restorer
}

func (f *fakeRestorer) RestoreProject(ctx context.Context, root string, lost port.Lost) {
	f.mu.Lock()
	f.roots = append(f.roots, root)
	f.lost = append(f.lost, lost)
	f.running++
	f.most = max(f.most, f.running)
	hold, next := f.hold, f.next
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	if next != nil {
		next.RestoreProject(ctx, root, lost)
	}
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
}

// runs is how many runs were asked for, how many run now, and the most at once.
func (f *fakeRestorer) runs() (n, running, most int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.roots), f.running, f.most
}

// all is the lost panes each run was given.
func (f *fakeRestorer) all() []port.Lost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.lost)
}
