// rolestop.go: serve stops the integrator and the reviewer once their work is done (T-672), as rules (T-870), and a
// role agent a fresh brief is sent to (T-794).
package app

import (
	"context"
	"obor/internal/app/port"
	"sort"
	"time"

	"obor/internal/config"
)

// Idle role agents hold memory, and nothing of a stopped one is resumed: its next use starts fresh (T-672;
// docs/state-model.md "Role agents stopped"). Serve stops two of them itself, each with launch.Stop (the agent quits,
// its labelled pane stays at a shell), as two rules on the rule pass (rules.go, T-870):
//   - the integrator, once a verdict came since its last start and no job is open or queued for it (a green task to
//     review, a conflict to resolve): each job starts a fresh session anyway (T-602), so the next job's start starts
//     it again. A stopped integrator with no open job is the normal state: no timer watches it (watchJob only runs for
//     an open job), and obor status shows it as stopped;
//   - the reviewer, once its review cycle closes (fact review.closed for the review serve started: obor review
//     close's call, T-926), never before: its verify runs in the same session.
//
// A stop is a rule action: no intent, no result, no retry clock; the agent's labelled pane left at its shell is what
// obor status shows as stopped, whoever stopped it (stoppedRoles: serve, or the lead's obor restart <agent> --stop).
// A stop runs only on an idle agent (its turn after the verdict ends first); one that fails is tried again on the next
// pass, and three failures in a row are the rule pass's needs-you. A wrong stop heals through the job's delivery retry.
// The advisor and the deep dive are the lead's to stop (skills/lead/SKILL.md); a brief to any stopped role agent but
// the integrator starts it fresh with the brief's line as its first prompt (messages.go, deliverLine). A fresh brief
// (obor brief --fresh, T-794) to a running one stops it right before that start, once the brief is recorded: that stop
// is part of the brief's delivery (freshStop), not a rule.

// stepRoleStop is the step prefix of an older log's role stop: stop.<agent>[.N] (before T-870).
const stepRoleStop = "stop."

// roleStopRetry is how long after a failed stop of a fresh brief serve tries it again.
const roleStopRetry = time.Minute

// The role stop rules' names: their keys in the rule pass.
const (
	ruleIntegratorStop = "role.integrator.stop"
	ruleReviewerStop   = "role.reviewer.stop"
)

// integratorStopRule is the integrator's stop rule: its pane runs it and is idle, no task has an integrator job open or
// queued, and a verdict came since its start (folded.verdicts, against the count at serve's last start of it). Done
// when the pane is at its shell: then it runs it no more.
func integratorStopRule(_ context.Context, a *App, p *proj, fx *Loop, _ time.Time) (int64, action, bool) {
	if fx == nil || fx.Agents == nil || p.integrator == "" {
		return 0, action{}, false
	}
	a.mu.Lock()
	w := a.whereIs(p, p.integrator)
	row, _ := p.reg.start(p.integrator)
	verdicts := p.folded.verdicts
	stop := w.State == Identified && Ready(w.Pane.Status) && !p.integratorBusy() && verdicts > row.Verdicts
	a.mu.Unlock()
	if !stop {
		return 0, action{}, false
	}
	return int64(verdicts), stopAction(a, p, fx, p.integrator), true
}

// reviewerStopRule is the reviewer's stop rule: the last review serve started is closed (its review.closed fact, the
// rule's key: T-926, no git read), the reviewer has no open brief (its verify runs in the same session), and its pane
// runs it and is idle. A failing stop counts on every pass while the condition holds, so ruleFails in a row are the
// rule pass's needs-you (T-875). Done when the pane is at its shell.
func reviewerStopRule(_ context.Context, a *App, p *proj, fx *Loop, _ time.Time) (int64, action, bool) {
	if fx == nil || fx.Agents == nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	closed := p.folded.rv.last().closed
	a.mu.Unlock()
	if closed == 0 {
		return 0, action{}, false
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	w := a.whereIs(p, s.Agent)
	stop := w.State == Identified && Ready(w.Pane.Status) && p.out.brief(s.Agent) == nil
	a.mu.Unlock()
	if !stop {
		return 0, action{}, false
	}
	return closed, stopAction(a, p, fx, s.Agent), true
}

// stopAction is a role stop rule's action: the agent stopped at the pane it is identified in, no force (the pane
// stays labelled, at its shell). A stop that says not found while the agent is still identified is the snapshot
// lagging: the goal met (stopAgent's lags).
func stopAction(a *App, p *proj, fx *Loop, agent string) action {
	return action{call: func(ctx context.Context) error {
		line, _, err := a.stopAgent(ctx, p, fx, agent, false, true)
		if err != nil {
			return err
		}
		a.logf("%s: role stop %s: %s", p.name, agent, line)
		return nil
	}}
}

// stoppedRoles is the role agents whose only labelled pane is at its shell (the one launch.Stop leaves), whoever
// stopped them — serve once their work was done (T-672) or the lead with obor restart <agent> --stop (T-677) — sorted
// (under mu): obor status, its --json and the view show them all as stopped. An agent two panes are
// labelled with, or one serve cannot see while herdr is not connected, is unknown, not stopped; no labelled pane at all
// leaves it "no pane".
func (a *App) stoppedRoles(p *proj) []string {
	var out []string
	for _, o := range a.obs {
		agent := o.pane.Label
		if agent == "" || o.pane.Agent != "" || a.projectOf(o.pane) != p {
			continue // not a labelled pane at a shell of this project
		}
		if p.roleAgent(agent) && a.atShell(p, agent) {
			out = append(out, agent)
		}
	}
	sort.Strings(out)
	return out
}

// atShell: agent's only labelled pane is at its shell, and no pane is identified as agent's (labelledShell; under mu).
// It is shellPane without the brief's gate on role agents: status reports the integrator stopped too.
func (a *App) atShell(p *proj, agent string) bool {
	_, ok := a.labelledShell(p, agent)
	return ok
}

// integratorBusy: a task of p has an integrator job open (out, also while a needs-you holds it) or queued (a green task
// with no kept accept to review, a conflict to resolve) (under mu).
func (p *proj) integratorBusy() bool {
	for _, mt := range p.folded.loop {
		switch {
		case mt.openJob(), mt.is(stConflict):
			return true
		case mt.is(stGreen) && (mt.accepted == "" || mt.accepted != mt.cand.Patch):
			return true
		}
	}
	return false
}

// shellPane is the pane labelled agent, a role agent a brief may start (roleAgentOf: never a worker, the lead or the
// integrator), that runs no agent (its shell: the agent stopped, by serve or by the lead's obor restart --stop) while
// no pane is identified as agent's (under mu; false: connected to no herdr, not such a role agent, or none).
func (a *App) shellPane(p *proj, agent string) (port.Pane, bool) {
	if !p.roleAgentOf(agent) {
		return port.Pane{}, false
	}
	return a.labelledShell(p, agent)
}

// labelledShell is agent's only labelled pane at its shell (the pane launch.Stop leaves, whichever stop left it) while no
// pane is identified as agent's (under mu; false: two are labelled so, none is, or herdr is not connected).
func (a *App) labelledShell(p *proj, agent string) (port.Pane, bool) {
	if w := a.whereIs(p, agent); w.State != Unavailable || w.Why == notConnected {
		return port.Pane{}, false
	}
	var ids []string
	for id, o := range a.obs {
		if o.pane.Label == agent && o.pane.Agent == "" && a.projectOf(o.pane) == p {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return port.Pane{}, false // none, or two labelled alike: nothing is started, nothing stopped
	}
	return a.obs[ids[0]].pane, true
}

// startRoute is route's post for a brief whose role agent is stopped (shellPane): its line is the first prompt of
// the agent's fresh start in that pane (deliverLine), not typed into a shell.
const startRoute = "start"

// stoppedAt: agent's labelled pane is at its shell and a brief to it starts it fresh (shellPane; under mu).
func (a *App) stoppedAt(p *proj, agent string) bool {
	_, ok := a.shellPane(p, agent)
	return ok
}

// roleAgentOf: agent is a role agent of p's config that a brief may start fresh: a role's or an agents entry's, not the
// lead, the integrator or a lane's worker (by the name rules launch resolves, the lane rows' agents included; under
// mu). A config that does not load names none.
func (p *proj) roleAgentOf(agent string) bool {
	return agent != p.integrator && p.roleAgent(agent)
}

// roleAgent: agent is a role agent of p's config on no lane, the integrator included: a role's or an agents entry's,
// not the lead and not a lane's worker (under mu): the agents obor status may report stopped.
func (p *proj) roleAgent(agent string) bool {
	if agent == "" || agent == p.out.lead() {
		return false
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return false
	}
	info, ok := cfg.AgentLookup(agent, "")
	if !ok || info.Role == "lead" || info.Role == "worker" {
		return false
	}
	for _, l := range p.lanes {
		if l.Agent == agent {
			return false
		}
	}
	return true
}
