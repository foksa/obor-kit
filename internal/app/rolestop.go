// rolestop.go: serve stops the integrator and the reviewer once their work is done (T-672).
package app

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"obor/internal/config"
)

// Idle role agents hold memory, and nothing of a stopped one is resumed: its next use starts fresh (T-672;
// docs/state-model.md "Role agents stopped"). Serve stops two of them itself, each with launch.Stop (the agent quits,
// its labelled pane stays at a shell):
//   - the integrator, once a job's verdict is recorded and no other job is open or queued for it (a green task to
//     review, a conflict to resolve): each job starts a fresh session anyway (T-602), so the next job's start starts
//     it again. A stopped integrator with no open job is the normal state: no timer watches it (watchJob only runs
//     for an open job), and obor status shows it as stopped;
//   - the reviewer, once its review cycle closes (the "RV-<n> closed" commit for the review serve started, review.go),
//     never before: its verify runs in the same session.
//
// A stop is intent/result role.stop on the task key RoleTask (S, step stop.<agent>[.N]): its result (roleStopOut)
// folds the agent as stopped until a start or a brief of it, so a replay keeps it. A stop runs only on an idle agent
// (its turn after the verdict ends first); one that fails is tried again on a pass at least roleStopRetry later.
// The advisor and the deep dive are the lead's to stop (skills/lead/SKILL.md); a brief to any stopped role agent but
// the integrator starts it fresh with the brief's line as its first prompt (messages.go, deliverLine).

// RoleTask is the task key of serve's stops of role agents.
const RoleTask = "roles"

// stepRoleStop is the step prefix of a role agent's stop: stop.<agent>[.N].
const stepRoleStop = "stop."

// roleStopRetry is how long after a failed stop serve tries it again.
const roleStopRetry = time.Minute

// roleStopOut is a role stop's result: the agent, its role, the review it was stopped for (the reviewer: review.started's
// seq) and the stop's line.
type roleStopOut struct {
	Agent  string `json:"agent"`
	Role   string `json:"role"`
	Review int64  `json:"review,omitempty"`
	Line   string `json:"line"`
}

// roleStops is the folded part of the role stops: how many each agent has (the next key), the agents stopped and not
// started or briefed since, whether an integrator job's verdict is in since its last job or stop (due), and the
// review the reviewer was last stopped for (review.started's seq).
type roleStops struct {
	n       map[string]int
	stopped map[string]bool
	due     bool
	review  int64
}

// foldRoles folds r into the role stops (foldRecord).
func (p *proj) foldRoles(r Record) {
	rs := &p.folded.roles
	if rs.n == nil {
		rs.n, rs.stopped = map[string]int{}, map[string]bool{}
	}
	switch step, _ := stepN(r.Step); {
	case r.Task == RoleTask && r.Kind == KindResult && strings.HasPrefix(r.Step, stepRoleStop):
		var out roleStopOut
		if json.Unmarshal(r.Data, &out) != nil || out.Agent == "" {
			return
		}
		rs.n[out.Agent]++
		rs.stopped[out.Agent] = true
		switch out.Role {
		case "integrator":
			rs.due = false
		case "reviewer":
			rs.review = max(rs.review, out.Review)
		}
	case r.Kind == KindIntent && (step == StepReview || step == StepResolve):
		rs.due = false // a new job: its own verdict makes the next stop due
	case r.Kind == KindResult && strings.HasPrefix(step, stepStart):
		var out startOut
		if json.Unmarshal(r.Data, &out) == nil && out.Agent != "" {
			delete(rs.stopped, out.Agent)
		}
	case r.Kind == KindResult:
		if m, _ := verdictMethod(r.Step); m != "" && m != MethodAnswer && p.folded.loop[r.Task] != nil {
			rs.due = true
		}
	case r.Kind == KindFact && r.Step == StepBriefed: // a brief starts a stopped agent (or its fresh start carried it)
		var m Message
		_ = json.Unmarshal(r.Data, &m)
		delete(rs.stopped, m.To)
	}
}

// stoppedRoles is the agents serve stopped that no pane runs now, sorted (under mu): obor status shows them as
// stopped.
func (a *App) stoppedRoles(p *proj) []string {
	var out []string
	for agent, ok := range p.folded.roles.stopped {
		if ok && a.whereIs(p, agent).State == Unavailable {
			out = append(out, agent)
		}
	}
	sort.Strings(out)
	return out
}

// stopRoles is a pass's stops of the role agents whose work is done (after the tasks' steps and the review pass).
func (a *App) stopRoles(ctx context.Context, p *proj, fx *Loop) {
	if fx.Agents == nil {
		return
	}
	a.mu.Lock()
	integrator := p.integrator != "" && p.folded.roles.due && !p.integratorBusy()
	a.mu.Unlock()
	if integrator {
		a.stopRole(ctx, p, fx, p.integrator, "integrator", 0)
	}
	if agent, review := a.reviewClosed(ctx, p, fx); agent != "" {
		a.stopRole(ctx, p, fx, agent, "reviewer", review)
	}
}

// integratorBusy: a task of p has an integrator job open (out, or a needs-you left it out) or queued (a green task
// with no kept accept to review, a conflict to resolve) (under mu).
func (p *proj) integratorBusy() bool {
	for _, mt := range p.folded.loop {
		switch {
		case mt.openJob(), mt.state == stConflict:
			return true
		case mt.state == stGreen && (mt.accepted == "" || mt.accepted != mt.cand.Patch):
			return true
		}
	}
	return false
}

// reviewClosed is the reviewer to stop and the review it is stopped for ("": none): the last review serve started is
// not stopped for yet, the reviewer has no open brief, and its "RV-<n> closed" commit is on the integration branch
// (read in git at most every reviewDueEvery while the review is open).
func (a *App) reviewClosed(ctx context.Context, p *proj, fx *Loop) (string, int64) {
	if fx.Review == nil {
		return "", 0
	}
	a.mu.Lock()
	started := p.folded.reviewed
	due := started.seq != 0 && started.seq > p.folded.roles.review
	now := a.opt.Now()
	fresh := p.rvRead.seq == started.seq && now.Sub(p.rvRead.at) < reviewDueEvery
	a.mu.Unlock()
	if !due || fresh {
		return "", 0
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return "", 0
	}
	open, err := reviewOpen(ctx, fx.Review, s.branch, started)
	if err != nil {
		a.logf("review of %s: is %s closed: %v", p.name, started.id, err)
		return "", 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if open != "" {
		p.rvRead = rvRead{seq: started.seq, at: now}
		return "", 0
	}
	if p.out.brief(s.agent) != nil {
		return "", 0 // briefed since: stopped once that brief is finished
	}
	return s.agent, started.seq
}

// rvRead is when the reviewer's stop last read git and found the review of seq still open (live).
type rvRead struct {
	seq int64
	at  time.Time
}

// stopRole stops agent (role; the reviewer: for review) once it is idle, under the intent stop.<agent>[.N]: no pane
// runs it, the stop is recorded as done ("stopped: no agent"); two panes claim it, or it works, nothing now. A stop
// that fails stays pending, tried again roleStopRetry later.
func (a *App) stopRole(ctx context.Context, p *proj, fx *Loop, agent, role string, review int64) {
	a.mu.Lock()
	w := a.whereIs(p, agent)
	key := Key{Task: RoleTask, Step: nth(stepRoleStop+agent, p.folded.roles.n[agent]+1)}
	late := a.opt.Now().Sub(p.roleFail[agent]) < roleStopRetry
	a.mu.Unlock()
	switch {
	case late, w.State == Ambiguous, w.State == Unavailable && w.Why == notConnected:
		return
	case w.State == Identified && !Ready(w.Pane.Status):
		return // its turn ends first
	}
	out := roleStopOut{Agent: agent, Role: role, Review: review}
	_, err := a.do(ctx, p, Intent{Key: key, Class: ClassStart, Data: stopData{Agent: agent}},
		func(ctx context.Context) (any, error) {
			if w.State != Identified {
				out.Line = "stopped: no agent (" + w.Why + ")"
				return out, nil
			}
			line, err := fx.Agents.Stop(ctx, agent, w.Pane.ID, false)
			switch {
			case err != nil && NotFound(err):
				line = "stopped: no agent (" + err.Error() + ")"
			case err != nil:
				return nil, err
			case line == "":
				line = "stopped " + agent
			}
			out.Line = line
			return out, nil
		})
	if err != nil && !cutByServe(ctx, err) {
		a.mu.Lock()
		if p.roleFail == nil {
			p.roleFail = map[string]time.Time{}
		}
		p.roleFail[agent] = a.opt.Now()
		a.mu.Unlock()
	}
}

// shellPane is the pane labelled agent, a role agent a brief may start (roleAgentOf: never a worker, the lead or the
// integrator), that runs no agent (its shell: the agent stopped, by serve or by the lead's obor restart --stop) while
// no pane is identified as agent's (under mu; false: connected to no herdr, not such a role agent, or none).
func (a *App) shellPane(p *proj, agent string) (Pane, bool) {
	if w := a.whereIs(p, agent); w.State != Unavailable || w.Why == notConnected || !p.roleAgentOf(agent) {
		return Pane{}, false
	}
	var ids []string
	for id, o := range a.obs {
		if o.pane.Label == agent && o.pane.Agent == "" && a.projectOf(o.pane) == p {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return Pane{}, false // none, or two labelled alike: nothing is started
	}
	return a.obs[ids[0]].pane, true
}

// startRoute is route's post for a brief whose role agent is stopped (shellPane): its line is the first prompt of
// the agent's fresh start in that pane (deliverLine), not typed into a shell.
const startRoute = "start"

// stoppedAt: agent's labelled pane is at its shell (shellPane; under mu).
func (a *App) stoppedAt(p *proj, agent string) bool {
	_, ok := a.shellPane(p, agent)
	return ok
}

// roleAgentOf: agent is a role agent of p's config that a brief may start fresh: a role's or an agents entry's, not the
// lead, the integrator or a lane's worker (by the name rules launch resolves, the lane rows' agents included; under
// mu). A config that does not load names none.
func (p *proj) roleAgentOf(agent string) bool {
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return false
	}
	info, ok := cfg.AgentLookup(agent, "", func(lane string) string { return p.folded.lr.rows[lane].Agent })
	if !ok || info.Role == "lead" || info.Role == "integrator" || info.Role == "worker" || agent == p.integrator ||
		agent == p.out.lead() {
		return false
	}
	for _, l := range p.lanes {
		if l.Agent == agent {
			return false
		}
	}
	return true
}
