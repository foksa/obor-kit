// follow.go: serve follows a new obor binary: on its own tick (Options.Follow: herdr connected or not,
// the watchdogs' tick counts only while it is) it asks Options.Binary and takes the self-restart path (Loop.Restart),
// preferring a moment when nothing runs, and waiting for one at most followWait.
package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"obor/internal/app/event"
)

// follow is the binary watch's state: the tick it last asked at (App.follows) and whether its restart failed (no
// further try in this serve: its needs-you says so).
type follow struct {
	tick   int
	failed bool
}

// followTicks is the binary watch's timer until ctx ends: each Options.Follow counts a tick (App.follows) and wakes
// the merge loop, whose pass asks (followBinary). It runs off herdr's subscription and the watchdogs' count, so a
// serve started without herdr, or whose stream dropped, still follows.
func (a *App) followTicks(ctx context.Context) {
	t := time.NewTicker(a.opt.Follow)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		a.follows++
		a.mu.Unlock()
		a.wake()
	}
}

// followWait bounds a pending self-restart's wait for work that may never end: an effect stuck in the terminal, a lane
// test whose command hangs, a nightly full check. Past it the restart goes anyway and cuts that work; the log keeps its
// intent, so the next serve runs the gate, the test or the nightly again. It is longer than the work a project sets for
// itself (merge.check.timeout is 20 minutes by default), so a slow gate, test or nightly is not cut; nothing cuts a
// main-checkout writer or a verdict call (p.admits, a.admitted), each bounded by its own request.
const followWait = 30 * time.Minute

// followBinary restarts serve on a new obor binary (Options.Binary): each tick asks, and once one is seen the restart
// goes at the first pass where serve is idle (p.restartWork: no slot step, no effect, no lane test, no nightly; no
// verdict call admitted), and at the latest followWait after it was seen. Nothing new is held back for it. A task
// waiting on a person holds nothing: a needs-you in force is no work of the task's (p.restartWork), so the owner may
// take hours. Never while a main-checkout writer is admitted (p.admits: a config_set's or a review close's commit),
// whatever runs (a writer is bounded by its own request timeout). One a.mu section decides and claims the restart
// (p.restartClaim): admit refuses while it is claimed, so no writer starts under the restart's cancel. While it waits,
// p.restarting shows it in obor status with what holds it (restartHolds). The log names the binary once, when it is
// seen ("a new obor binary: <path> (<version>)"), and what holds the restart each time that changes. A failed restart
// is a needs-you "serve restart on the new obor binary failed: <err>" (key serve), ended by the next serve's start;
// the claim clears with p.restarting. True: serve is ending for the restart (the pass stops here). A serve already
// ending (ctx done: a restart canceled it, and a tick and a kick were pending) restarts nothing again.
func (a *App) followBinary(ctx context.Context, p *proj) bool {
	if ctx.Err() != nil {
		return true
	}
	if a.opt.Binary == nil || a.opt.Loop == nil || a.follow.failed {
		return false
	}
	a.mu.Lock()
	tick := a.follows
	first := p.restarting.IsZero() // nothing pending yet: a tick may see the new binary now
	a.mu.Unlock()
	if tick != a.follow.tick { // ask once per tick: a stat of the binary, not one per pass
		a.follow.tick = tick
		if first {
			bin, ok := a.opt.Binary()
			if !ok {
				return false
			}
			a.logf("%s: a new obor binary: %s (%s): restarting serve on it once nothing runs (at the latest in %s)",
				p.name, bin.Path, orNone(bin.Version), followWait)
		}
	} else if first {
		return false
	}
	a.mu.Lock()
	now := a.opt.Now()
	if p.restarting.IsZero() {
		p.restarting = now
	}
	// A lane test counts as running too (p.tests): it runs minutes, and a test whose start has not reached
	// p.exec yet is still no idle moment. A new one starts beside the pending restart, and the restart goes
	// in the first pass (the gap) after the last ends, or at followWait (p.restartWork).
	holds, cut := a.restartHolds(p, now)
	claim := len(holds) == 0
	p.restartClaim = claim
	a.mu.Unlock()
	if !claim {
		if w := strings.Join(holds, ", "); w != p.restartWaits {
			p.restartWaits = w
			a.logf("%s: the restart on the new obor binary waits for: %s", p.name, w)
		}
		return false
	}
	fx, err := a.opt.Loop(p.root)
	if err == nil && fx.Restart == nil {
		a.mu.Lock()
		p.restartClaim = false
		a.mu.Unlock()
		return false // no restart wired: nothing to do but show it
	}
	if len(cut) > 0 {
		a.logf("%s: restarting serve on the new obor binary (waited %s; cuts %s)", p.name, followWait, strings.Join(cut, ", "))
	} else {
		a.logf("%s: restarting serve on the new obor binary", p.name)
	}
	if err == nil {
		err = fx.Restart(ctx)
	}
	if err == nil || ctx.Err() != nil {
		return true
	}
	a.follow.failed = true
	a.mu.Lock()
	p.restarting, p.restartClaim = time.Time{}, false
	a.mu.Unlock()
	p.restartWaits = ""
	a.settleNeeds(p, event.ServeTask, "serve restart on the new obor binary failed: "+err.Error())
	return false
}

// orNone is s, or "no version" when the binary's version printed nothing.
func orNone(s string) string {
	if s == "" {
		return "no version"
	}
	return s
}

// restartHolds is what a pending self-restart waits for now, by name (under mu): the admitted main-checkout writers
// ("config_set (writer)", "review close (writer)") and verdict calls ("accept T-3 (verdict)"), whatever has run how
// long, then, before followWait, the work restartWork names (restartWork). cut: that work once followWait is past
// (the restart goes and cuts it). Both empty: the restart may go.
func (a *App) restartHolds(p *proj, now time.Time) (holds, cut []string) {
	for _, w := range p.admitOps.sorted() {
		holds = append(holds, w+" (writer)")
	}
	for _, v := range a.verdictOps.sorted() {
		holds = append(holds, v+" (verdict)")
	}
	if p.restarting.IsZero() || now.Sub(p.restarting) < followWait {
		return append(holds, p.restartWork()...), nil
	}
	return holds, p.restartWork()
}

// restartWork is the work a pending self-restart waits for by name, each once, in a stable order (under mu): the
// nightly ("nightly check"), the slot step, the lane tests (p.tests), then the other effects running (p.exec: a gate,
// a lane test, a prompt, a start, the nightly's run), each "<step> <task>" (workName). Work of a task a person must
// answer is no work at all: a needs-you in force means its next step is not started (the merge loop picks none of a
// paused task, the worker start rule needs no needs-you), so whatever an effect of it still holds replays after the
// restart.
func (p *proj) restartWork() []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if p.night.running {
		add("nightly check")
	}
	if p.running.Task != "" && !p.onAPerson(p.running.Task) {
		add(workName(p.running))
	}
	var rest []string
	for task, k := range p.tests {
		if !p.onAPerson(task) {
			rest = append(rest, workName(k))
		}
	}
	sort.Strings(rest)
	for _, n := range rest {
		add(n)
	}
	rest = rest[:0]
	for k := range p.exec {
		if !p.onAPerson(k.Task) {
			rest = append(rest, workName(k))
		}
	}
	sort.Strings(rest)
	for _, n := range rest {
		add(n)
	}
	return out
}

// workName is a running step's name in a restart's wait: its base step and its task ("gate T-3", "test T-4"), the
// nightly's run "nightly check".
func workName(k event.Key) string {
	if strings.HasPrefix(k.Step, event.StepNightly+".") || k.Step == event.StepNightly {
		return "nightly check"
	}
	base, _ := event.StepN(k.Step)
	if k.Task == "" {
		return base
	}
	return base + " " + k.Task
}

// ops is a multiset of the operations in force by name (an admission, a verdict call): a pending restart names
// what it waits for from it. The zero value is empty; add makes it.
type ops map[string]int

func (o *ops) add(name string) {
	if *o == nil {
		*o = ops{}
	}
	(*o)[name]++
}

func (o ops) del(name string) {
	if o[name]--; o[name] <= 0 {
		delete(o, name)
	}
}

// sorted is the names in force, each once, in order.
func (o ops) sorted() []string {
	out := make([]string, 0, len(o))
	for n := range o {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
