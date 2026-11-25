package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// holdRestorer is a restore run (port.Restorer) that waits for release, then asks OpenWork for each of its agents as
// launch.Restorer does before a restart, and restarts those with work open through restart (nil: the restart fails,
// the pane stays at its shell). open is OpenWork's answer per agent when the run reached it.
type holdRestorer struct {
	a       *App
	roles   map[string]string // agent → role
	release chan struct{}
	restart func(agent string)
	open    map[string]bool
}

func (r *holdRestorer) RestoreProject(ctx context.Context, _ string, _ port.Lost) {
	select {
	case <-r.release:
	case <-ctx.Done():
		return
	}
	for agent, role := range r.roles {
		open, err := r.a.OpenWork(agent, role)
		if err != nil {
			continue
		}
		r.open[agent] = open
		if open && r.restart != nil {
			r.restart(agent)
		}
	}
}

// rolePanes are the briefed role agents' panes of the tests below: agent → pane id.
var rolePanes = map[string]string{"pj-advisor": "w1:adv", "pj-rev": "w1:rev", "pj-int": "w1:int"}

// briefedRoles is the harness with a lead, an integrator, a reviewer and an advisor configured, each of agents in its
// labelled pane, running, briefed and seen working (the brief's work in the log), and restore as its restorer.
func briefedRoles(t *testing.T, agents map[string]string) (*harness, *holdRestorer) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},`+
		`"lanes":{"count":1},"agents":{"roles":{"lead":"","integrator":"","reviewer":"","advisor":""}}}`)
	for agent := range agents {
		h.snap.Panes = append(h.snap.Panes, port.Pane{ID: rolePanes[agent], Group: "pj", Cwd: h.root, Agent: "claude",
			Label: agent, Status: "idle"})
	}
	h.connect()
	for agent := range agents {
		h.brief(agent, "") // its start carried the line
		h.status(rolePanes[agent], func(p *port.Pane) { p.Status = "working" })
		if !h.worked(agent) {
			t.Fatalf("precondition: %s's brief saw no work", agent)
		}
	}
	h.a.deliver(context.Background(), h.proj(), linerLoop(&FakeAgents{})) // the work in the log; nothing finished
	r := &holdRestorer{a: h.a, roles: agents, release: make(chan struct{}), open: map[string]bool{}}
	r.restart = func(agent string) {
		h.status(rolePanes[agent], func(p *port.Pane) { p.Agent, p.Status = "claude", "working" })
	}
	h.a.opt.Restore = r
	return h, r
}

// atShells is h's snapshot after a terminal restart: every briefed role agent's labelled pane at its shell.
func atShells(h *harness, agents map[string]string) port.Snapshot {
	for agent := range agents {
		p := h.pane(rolePanes[agent])
		p.Agent, p.Status = "", ""
	}
	snap := h.snap
	snap.Panes = append([]port.Pane(nil), h.snap.Panes...)
	return snap
}

// finished is the finished lines recorded for agent's briefs.
func finished(h *harness, agent string) int {
	n := 0
	for _, rec := range h.records() {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && rec.Kind == event.KindFact &&
			json.Unmarshal(rec.Data, &m) == nil && m.Ends == agent {
			n++
		}
	}
	return n
}

// restoreRun is serve's connect to snap (Serve: restoreWhy, connect, connectRestore) with a merge pass's deliver
// after the snapshot is applied (the restore pending) and one while the restore runs, before it reaches the agents:
// neither ends a brief of agents, each agent's labelled pane at its shell. It returns once the restore ended.
func restoreRun(t *testing.T, h *harness, r *holdRestorer, snap port.Snapshot, first, lost bool) {
	t.Helper()
	ctx := context.Background()
	call := h.a.restoreWhy(snap, first, lost)
	if call.why == "" {
		t.Fatal("precondition: no restore follows the snapshot")
	}
	h.a.connect(ctx, snap, true)
	pass := func(when string) {
		t.Helper()
		h.a.mu.Lock()
		for agent := range r.roles {
			if _, shell := h.a.labelledShell(h.proj(), agent); !shell {
				t.Fatalf("precondition %s: %s's labelled pane is not at its shell", when, agent)
			}
		}
		h.a.mu.Unlock()
		h.a.deliver(ctx, h.proj(), linerLoop(&FakeAgents{}))
		for agent := range r.roles {
			if n := finished(h, agent); n != 0 {
				t.Errorf("%s: %d finished lines of %s, want none: the restore has not reached its pane", when, n, agent)
			}
			if h.proj().out.Brief(agent) == nil {
				t.Errorf("%s: %s's brief ended", when, agent)
			}
		}
	}
	pass("restore pending")
	var wg sync.WaitGroup
	h.a.connectRestore(ctx, &wg, snap, call)
	pass("restore running")
	close(r.release)
	wg.Wait()
	h.a.mu.Lock()
	restoring := h.a.restoring()
	h.a.mu.Unlock()
	if restoring {
		t.Fatal("the restore ended, still restoring")
	}
}

// restored: after the restore, agent had work open when the restore reached it, runs again in its pane, its brief open
// and no finished line, also after the next pass.
func restored(t *testing.T, h *harness, r *holdRestorer, agent string) {
	t.Helper()
	if !r.open[agent] {
		t.Errorf("OpenWork(%s) was false when the restore reached it: its pane stays at its shell", agent)
	}
	h.a.mu.Lock()
	w := h.a.whereIs(h.proj(), agent)
	h.a.mu.Unlock()
	if w.State != Identified || w.Pane.Agent == "" {
		t.Errorf("%s after the restore: %+v, want it running in its pane", agent, w)
	}
	h.a.deliver(context.Background(), h.proj(), linerLoop(&FakeAgents{}))
	if n := finished(h, agent); n != 0 || h.proj().out.Brief(agent) == nil {
		t.Errorf("%s after the restore: %d finished lines, brief open %v; want none and open", agent, n,
			h.proj().out.Brief(agent) != nil)
	}
}

// A reconnect (the terminal connection lost, its panes back at their shells): a briefed advisor whose work was seen
// keeps its brief while the restore is pending and running, so the restore finds its work open and restarts it.
func TestReconnectRestoreKeepsTheBrief(t *testing.T) {
	t.Parallel()
	agents := map[string]string{"pj-advisor": "advisor"}
	h, r := briefedRoles(t, agents)
	restoreRun(t, h, r, atShells(h, agents), false, true)
	restored(t, h, r, "pj-advisor")
}

// Serve's first connect after its start, to a terminal that restarted while serve was down (another instance than
// recorded): the brief replayed from the log is kept the same way, and the restore restarts its agent.
func TestFirstConnectRestoreKeepsTheBrief(t *testing.T) {
	t.Parallel()
	agents := map[string]string{"pj-advisor": "advisor"}
	h, r := briefedRoles(t, agents)
	h.a.closeLog() // serve stops; the terminal restarts meanwhile
	h.write(InstanceFile(h.root), "2026-10-24T08:00:00Z\n")
	h.a = newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	h.a.opt.Restore = r
	r.a = h.a
	h.a.loadProject()
	if b := h.proj().out.Brief("pj-advisor"); b == nil || !b.Worked() {
		t.Fatalf("precondition: the worked brief is not replayed: %+v", b)
	}
	snap := atShells(h, agents)
	snap.Instance = h.now.Format(time.RFC3339Nano)
	restoreRun(t, h, r, snap, true, false)
	restored(t, h, r, "pj-advisor")
}

// The reviewer with an open review and the integrator with an open job, both briefed and seen working, under the
// reconnect's sequence: neither brief gets a finished line before the restore reaches it.
func TestRestoreKeepsReviewerAndIntegratorBriefs(t *testing.T) {
	t.Parallel()
	agents := map[string]string{"pj-rev": "reviewer", "pj-int": "integrator"}
	h, r := briefedRoles(t, agents)
	h.a.mu.Lock()
	p := h.proj()
	p.folded.review = &reviewReq{seq: 1, at: h.now}
	p.folded.loop["T-9"] = &mtask{task: "T-9", attempt: 1, state: stAwaiting, job: event.StepReview}
	_, job := p.folded.loop["T-9"].integratorJob()
	h.a.mu.Unlock()
	if !job {
		t.Fatal("precondition: the integrator has no open job")
	}
	restoreRun(t, h, r, atShells(h, agents), false, true)
	restored(t, h, r, "pj-rev")
	restored(t, h, r, "pj-int")
}

// After the restore: a briefed role agent whose restart failed (its pane still at its shell) ends its brief as gone
// at the next pass.
func TestFailedRestoreEndsTheBriefAsGone(t *testing.T) {
	t.Parallel()
	agents := map[string]string{"pj-advisor": "advisor"}
	h, r := briefedRoles(t, agents)
	r.restart = nil // the restart fails
	restoreRun(t, h, r, atShells(h, agents), false, true)
	if !r.open["pj-advisor"] {
		t.Fatal("precondition: the restore found no open work: it tried no restart")
	}
	finishBriefs(h.a, h.proj(), linerLoop(&FakeAgents{}))
	if n := finished(h, "pj-advisor"); n != 1 || h.proj().out.Brief("pj-advisor") != nil {
		t.Errorf("after the restore: %d finished lines, brief open %v; want one, ended", n,
			h.proj().out.Brief("pj-advisor") != nil)
	}
}
