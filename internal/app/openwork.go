// openwork.go: whether a role agent has work open, the restore's question before it restarts the agent's pane
// (launch.Restorer.OpenWork).
package app

import "errors"

// OpenWork reports whether agent in role has work open in the served project: a brief not finished (waiting or
// delivered), the integrator's open job, or the reviewer's open review or verify. An error: no project is served
// (the restore restarts the pane, so a real job is never left stopped).
func (a *App) OpenWork(agent, role string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p == nil {
		return false, errors.New("open work: no served project")
	}
	return a.p.openWork(agent, role), nil
}

// openWork: agent in role has work open in the fold (under mu): a brief not finished, or its role's work — the
// integrator's open job, the reviewer's open review or verify.
func (p *proj) openWork(agent, role string) bool {
	if p.out.Brief(agent) != nil {
		return true
	}
	switch role {
	case "integrator":
		for _, mt := range p.folded.loop {
			if _, ok := mt.integratorJob(); ok {
				return true
			}
		}
	case "reviewer":
		return p.folded.review != nil || p.folded.rv.Cycle().Open() != "" || p.verifyDue
	}
	return false
}
