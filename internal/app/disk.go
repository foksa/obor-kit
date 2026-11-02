package app

import (
	"fmt"
	"time"
)

// Low disk (T-564): the project volume's free space below diskLow is the project's needs-you{disk low: …} until it is
// back above it. A gate or a nightly would only fail on a full disk (the RED line looked like a test failure), so
// neither starts while it is in force; they start once space is back, as a deferred one does.

// diskLow is the free space below which the disk is low (a constant, no config key).
const diskLow = 5 << 30

// diskEvery is how often the free space is read on a pass; a gate or nightly about to start reads it anyway.
const diskEvery = 3 * time.Minute

// Disk is the project volume's free space. Nil in Loop: not watched.
type Disk struct {
	Volume string                 // the volume's name in the needs-you (the project root)
	Free   func() (uint64, error) // free bytes available to the project
	Cache  func() string          // the Go build cache dir, `go env GOCACHE` ("": unknown); asked when the disk turns low
}

// diskTick reads the free space once diskEvery has passed since the last read.
func (a *App) diskTick(p *proj, fx *Loop) {
	if fx.Disk == nil {
		return
	}
	now := a.opt.Now()
	if !p.diskAt.IsZero() && now.Sub(p.diskAt) < diskEvery {
		return
	}
	a.diskIsLow(p, fx)
}

// diskIsLow reads the free space now and settles the needs-you: one record per transition (low → ok → low), none
// while it stays. True: the disk is low (a read that fails leaves the state as it is). Below the bound the reading
// alone holds a gate or nightly: a refused needs-you append is tried again on the next read (RV-59 #1).
func (a *App) diskIsLow(p *proj, fx *Loop) bool {
	d := fx.Disk
	if d == nil {
		return false
	}
	free, err := d.Free()
	a.mu.Lock()
	p.diskAt = a.opt.Now()
	held := len(p.folded.active[DiskTask]) > 0
	a.mu.Unlock()
	switch {
	case err != nil:
		a.logf("disk of %s: %v", p.name, err)
	case free < diskLow:
		if !held { // one record per transition: a refused append is tried again on the next read
			reason := fmt.Sprintf("disk low: %.1f GiB free on %s", float64(free)/(1<<30), d.Volume)
			if d.Cache != nil {
				if c := d.Cache(); c != "" {
					reason += "; Go build cache " + c + " (go clean -cache)"
				}
			}
			a.record(p, Record{Task: DiskTask, Step: StepNeedsYou, Kind: KindFact},
				FactData{Machine: Machine, Reason: reason})
		}
		held = true // the reading decides; keeping the record is best effort
	case held:
		held = !a.record(p, Record{Task: DiskTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine})
	}
	return held
}
