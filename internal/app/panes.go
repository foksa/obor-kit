// panes.go: connecting to herdr, observing panes and routing their events to the project.
package app

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// connect works from a fresh snapshot: every pane observed, the project's identity table reconciled, then the work
// its owned panes show noted for their briefs (RV-53 #4: the first snapshot after a replay too), then a check. One that shares no pane id with the panes known is a new herdr session (herdr
// restarted): nothing of the old one is evidence for it, so no pane of it "exited" and no lane "lost" its agent; the
// lanes find their panes again by workspace label and cwd. Otherwise a known pane gone from it exited, and a pane
// whose status and agent are unchanged keeps its timer.
func (a *App) connect(ctx context.Context, snap Snapshot) {
	now := a.opt.Now()
	a.mu.Lock()
	if len(a.obs) > 0 && !shares(a.obs, snap.Panes) {
		a.obs = map[string]*seen{}
		if a.p != nil {
			for _, w := range a.p.tasks {
				w.agent, w.lost = Pane{}, time.Time{} // the next check resets the task's timers
			}
		}
		a.logf("herdr: a new session (no pane id known): watching it afresh")
	}
	a.spaces = snap.Workspaces
	if a.spaces == nil {
		a.spaces = map[string]string{}
	}
	in := map[string]bool{}
	for _, p := range snap.Panes {
		in[p.ID] = true
		a.observe(p, now)
	}
	for id := range a.obs {
		if !in[id] {
			delete(a.obs, id)
		}
	}
	a.connected = true
	a.logf("herdr: connected; %d panes, %d in the project", len(snap.Panes), a.projectPanes())
	p := a.p
	if p != nil {
		a.reconcile(p)
	}
	for _, o := range a.obs {
		if p := a.projectOf(o.pane); p != nil {
			a.sawWork(p, o.pane)
		}
	}
	a.mu.Unlock()
	if p != nil { // a brief's work seen in the snapshot is in the log at once (RV-50 #3)
		a.logWorked(p)
	}
	a.check(ctx, true)
}

// shares: a pane of panes is known.
func shares(known map[string]*seen, panes []Pane) bool {
	for _, p := range panes {
		if known[p.ID] != nil {
			return true
		}
	}
	return false
}

// projectPanes is how many panes of the last snapshot are in the project.
func (a *App) projectPanes() int {
	n := 0
	for _, o := range a.obs {
		if a.projectOf(o.pane) != nil {
			n++
		}
	}
	return n
}

// observe stores pane as herdr reports it now; its timer restarts when its status or agent changed.
func (a *App) observe(pane Pane, now time.Time) {
	since := now
	if o := a.obs[pane.ID]; o != nil && o.pane.Status == pane.Status && o.pane.Agent == pane.Agent {
		since = o.since
	}
	a.obs[pane.ID] = &seen{pane: pane, since: since}
}

// event applies one herdr event; when the pane was or is in the project, it reconciles the project's identity table,
// notes the work the pane shows for its agent's brief, then decides for the project.
func (a *App) event(ctx context.Context, ev Event) {
	a.mu.Lock()
	var p *proj // the project, when the pane was or is in it
	add := func(pane Pane) {
		if q := a.projectOf(pane); q != nil {
			p = q
		}
	}
	old := a.obs[ev.Pane]
	if old != nil {
		add(old.pane)
	}
	switch ev.Kind {
	case EventCreated, EventStatus:
		pane := ev.New
		if pane.ID == "" {
			if old == nil {
				break
			}
			pane = old.pane
			pane.Status, pane.Agent = ev.Status, ev.Agent
		}
		if pane.Label == "" && old != nil { // a status event reads the agent entry, which has no label
			pane.Label = old.pane.Label
		}
		if pane.Terminal == "" && old != nil {
			pane.Terminal = old.pane.Terminal
		}
		a.observe(pane, a.opt.Now())
		add(pane)
	case EventClosed, EventExited:
		delete(a.obs, ev.Pane)
	}
	if p == nil {
		a.mu.Unlock()
		return
	}
	a.reconcile(p)
	if o := a.obs[ev.Pane]; o != nil && a.projectOf(o.pane) == p {
		a.sawWork(p, o.pane)
	}
	a.mu.Unlock()
	a.logWorked(p) // a brief's work this event showed is in the log at once (RV-50 #3)
	a.check(ctx, false)
}

// projectOf is the served project whose workspace holds pane (nil: none, the pane is ignored).
func (a *App) projectOf(pane Pane) *proj {
	label, ok := a.spaces[pane.Workspace]
	if !ok {
		return nil
	}
	if a.p != nil && a.p.name == label {
		return a.p
	}
	return nil
}

// inDir: dir is lane dir l or below it.
func inDir(dir, l string) bool {
	dir = filepath.Clean(dir)
	return l != "" && (dir == l || strings.HasPrefix(dir, l+"/"))
}

// laneOf is the lane of p whose worktree holds dir, as last read.
func (p *proj) laneOf(dir string) (Lane, bool) {
	for _, l := range p.lanes {
		if inDir(dir, l.Dir) {
			return l, true
		}
	}
	return Lane{}, false
}

// agentOf is lane l's agent where it runs (under mu): l.Agent by whereIs, Identified only in l's worktree (identified
// elsewhere: Unavailable "outside the lane"), and how many of p's panes are in that worktree. A lane that names no
// agent: the agent whoIs finds in its worktree (two or more: Ambiguous, their panes). No pane is taken for being an
// agent's in the lane dir alone.
func (a *App) agentOf(p *proj, l Lane) (Where, int) {
	n := 0
	var names, ids []string
	for _, o := range a.obs {
		if !inDir(o.pane.Cwd, l.Dir) || a.projectOf(o.pane) != p {
			continue
		}
		n++
		if agent, ok := a.whoIs(p, o.pane); ok && !slices.Contains(names, agent) {
			names, ids = append(names, agent), append(ids, o.pane.ID)
		}
	}
	agent := l.Agent
	if agent == "" {
		switch len(names) {
		case 0:
			return a.whereIs(p, ""), n
		case 1:
			agent = names[0]
		default:
			sort.Strings(ids)
			return Where{State: Ambiguous, Panes: ids}, n
		}
	}
	w := a.whereIs(p, agent)
	if w.State == Identified && !inDir(w.Pane.Cwd, l.Dir) {
		return Where{State: Unavailable, Why: "outside the lane"}, n
	}
	return w, n
}
