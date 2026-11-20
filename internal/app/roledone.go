// roledone.go: serve's reminder to the lead for a role agent done with no open work, and obor status' hint of the
// command that stops it. A reminder, never a stop: the lead may still want a follow-up in that session.
package app

import "time"

// roleDoneAfter is how long a role agent's pane may sit idle or done, with no open work, before the lead hears of it:
// a constant, no config key.
const roleDoneAfter = time.Hour

// roleDone is the line's Of kind for agent: one kind per agent, its Seq the start of its done stretch (Unix seconds),
// so the line goes once per done stretch and the log keeps it (folded.did: a replay sends none for the same stretch).
func roleDone(agent string) string { return "role.done." + agent }

// roleDoneLine is the lead's line for agent, done for age with no open work.
func roleDoneLine(agent string, age time.Duration) string {
	return "[obor] " + agent + " done " + Age(age) + ", no open work — obor restart " + agent + " --stop (the next " +
		"obor brief starts it fresh)"
}

// roleStopHint is the command that stops agent: obor status ends its line with it (wire.AgentStatus.Hint).
func roleStopHint(agent string) string { return "obor restart " + agent + " --stop" }

// doneRoles is p's role agents done with no open work now, each with since, when its pane began being ready (idle or
// done): a pane of p that the identity table has for one agent (two are no agent) has been ready for roleDoneAfter,
// and that agent is a role agent of no lane, not the lead (roleAgents), with nothing open for it (openWork). A pane
// serve has not observed since it was ready (a zero ready timer) is no stretch. Under mu; obor status' hint and the
// lead's line are this one condition.
func (a *App) doneRoles(p *proj, now time.Time) map[string]time.Time {
	type stretch struct {
		agent string
		since time.Time
	}
	var cands []stretch
	for _, o := range a.obs {
		if a.projectOf(o.pane) != p || !Ready(o.pane.Status) || o.ready.IsZero() || now.Sub(o.ready) < roleDoneAfter {
			continue
		}
		if agent, ok := p.reg.WhoIs(o.pane); ok {
			cands = append(cands, stretch{agent, o.ready})
		}
	}
	out := map[string]time.Time{}
	if len(cands) == 0 {
		return out
	}
	roles := p.roleAgents()
	for _, s := range cands {
		if role, ok := roles[s.agent]; ok && !p.openWork(s.agent, role) {
			out[s.agent] = s.since
		}
	}
	return out
}
